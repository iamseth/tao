// Package tui implements Tao's interactive terminal dashboard.
package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

// Terminal is the terminal-state and resize boundary used by the event loop.
type Terminal interface {
	EnterRaw() error
	Restore() error
	Size() (term.Size, error)
	ResizeEvents(context.Context) <-chan struct{}
}

// Ticker is the refresh timer boundary used by the event loop.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// SnapshotCollector supplies read-only dashboard refreshes.
type SnapshotCollector interface {
	Collect(context.Context) (monitor.Snapshot, error)
}

// NoteSnapshotCollector supplies open-note refreshes.
type NoteSnapshotCollector interface {
	Collect(context.Context) (note.Snapshot, error)
}

// NoteEditor edits one selected note through an interactive external editor.
type NoteEditor interface {
	Edit(context.Context, note.CatalogNote) (bool, error)
}

// NoteActions applies non-interactive mutations to a selected open note.
type NoteActions interface {
	Delete(context.Context, note.CatalogNote) error
	SetTier(context.Context, note.CatalogNote, int) (bool, error)
}

// Clipboard copies text to the user's copy/paste buffer.
type Clipboard interface {
	Copy(context.Context, string) error
}

// DebugSnapshotCollector supplies read-only runtime and doctor diagnostics.
type DebugSnapshotCollector interface {
	Collect(context.Context) (DebugSnapshot, error)
}

// SettingsService supplies repository settings and persists confirmed updates.
type SettingsService interface {
	Collect(context.Context) (SettingsSnapshot, error)
	SetPullRequestDefault(context.Context, string, *bool) error
}

// App owns one interactive dashboard event loop. Its boundaries are injectable
// so terminal behavior can be tested without taking over a real terminal.
type App struct {
	Input       io.Reader
	Output      io.Writer
	Terminal    Terminal
	Ticker      Ticker
	Collector   SnapshotCollector
	Notes       NoteSnapshotCollector
	NoteEditor  NoteEditor
	NoteActions NoteActions
	Clipboard   Clipboard
	Debug       DebugSnapshotCollector
	Settings    SettingsService
	Actions     *Actions
	Details     DetailRepository
	Inspector   DetailInspector
	Now         func() time.Time
}

type inputResult struct {
	key    term.KeyEvent
	err    error
	resume chan struct{}
}

type confirmPrompt struct {
	message string
	respond func(bool)
}

type loopState struct {
	snapshot            monitor.Snapshot
	noteSnapshot        note.Snapshot
	debugSnapshot       DebugSnapshot
	settingsSnapshot    SettingsSnapshot
	page                PageID
	selected            int
	pageSelections      map[PageID]int
	size                term.Size
	focusRepositoryID   string
	focusRepositoryName string
	focusRepositoryRoot string
	profile             Profile
	showShortcuts       bool
	searchQuery         string
	searchActive        bool
	debugOffset         int
	settingsMessage     string
	confirm             *confirmPrompt
	detail              *detailState
	noteDetail          *note.CatalogNote
	noteDetailOffset    int
	noteEditMessage     string
	now                 func() time.Time
	lastRootEscape      time.Time
	listTopPending      bool
}

// Run enters terminal mode and processes input, refresh, resize, and
// cancellation events until the user quits or an error occurs.
func (a App) Run(ctx context.Context) (resultErr error) {
	if err := a.validate(); err != nil {
		return err
	}
	if a.Details == nil {
		a.Details = plan.NewFileRepository("")
	}
	if a.Now == nil {
		a.Now = time.Now
	}

	// Keep this as the outermost defer: every path after validation, including a
	// panic in a boundary, attempts all terminal restoration before returning or
	// resuming the panic.
	defer func() {
		panicValue := recover()
		restoreErr := restoreTerminalState(a.Terminal, a.Output)
		if panicValue != nil {
			panic(panicValue)
		}
		if resultErr == nil && restoreErr != nil {
			resultErr = restoreErr
		}
	}()
	defer a.Ticker.Stop()

	if err := a.Terminal.EnterRaw(); err != nil {
		return fmt.Errorf("enter terminal raw mode: %w", err)
	}
	if err := term.EnterAlternateScreen(a.Output); err != nil {
		return fmt.Errorf("enter alternate screen: %w", err)
	}
	if err := term.HideCursor(a.Output); err != nil {
		return fmt.Errorf("hide cursor: %w", err)
	}

	size, err := a.Terminal.Size()
	if err != nil {
		return fmt.Errorf("read terminal size: %w", err)
	}
	snapshot, err := a.Collector.Collect(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("refresh dashboard: %w", err)
	}
	noteSnapshot, err := a.collectNotes(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("refresh notes: %w", err)
	}
	debugSnapshot := a.collectDebug(ctx)
	settingsSnapshot := a.collectSettings(ctx)
	state := loopState{
		snapshot:         snapshot,
		noteSnapshot:     noteSnapshot,
		debugSnapshot:    debugSnapshot,
		settingsSnapshot: settingsSnapshot,
		size:             size,
		profile:          outputSupportsColor(a.Output),
		now:              a.Now,
	}
	state.clampSelection()
	if err := a.writeFrame(state); err != nil {
		return err
	}

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	input := make(chan inputResult)
	go readInput(loopCtx, a.Input, input)
	resizes := a.Terminal.ResizeEvents(loopCtx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-input:
			if result.err != nil {
				return fmt.Errorf("read terminal input: %w", result.err)
			}
			if result.key.Key == term.KeyCtrlG {
				handled, err := a.editSelectedNote(loopCtx, &state)
				if err != nil {
					return err
				}
				if handled {
					if err := a.writeFrame(state); err != nil {
						return err
					}
					close(result.resume)
					continue
				}
			}
			if a.handleKey(loopCtx, &state, result.key) {
				return nil
			}
			if err := a.writeFrame(state); err != nil {
				return err
			}
			close(result.resume)
		case update, ok := <-state.detailUpdates():
			if !ok {
				state.detail.updates = nil
				continue
			}
			if update.err != nil {
				state.detail.followError = update.err.Error()
			} else {
				wasFollowing := state.detail.activityOffset == state.detail.maxOffset(detailTabActivity, state.size)
				state.detail.appendLog(update.text)
				if wasFollowing {
					state.detail.activityOffset = state.detail.maxOffset(detailTabActivity, state.size)
				} else {
					state.detail.clampOffsets(state.size)
				}
			}
			if err := a.writeFrame(state); err != nil {
				return err
			}
		case update, ok := <-state.inspectionUpdates():
			if !ok {
				state.detail.inspectionUpdates = nil
				continue
			}
			if update.key != state.detail.inspectionKey {
				continue
			}
			state.detail.inspectionUpdates = nil
			if update.err != nil {
				state.detail.inspection = detailInspectionView{status: detailInspectionFailed, err: cells.Truncate(singleLineDetail(update.err.Error()), detailInspectionMaxText)}
			} else {
				result := boundedDetailInspection(update.result)
				state.detail.inspection = detailInspectionView{status: detailInspectionReady, findings: result.Findings}
			}
			state.detail.clampOffsets(state.size)
			if err := a.writeFrame(state); err != nil {
				return err
			}
		case <-a.Ticker.C():
			snapshot, err := a.Collector.Collect(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("refresh dashboard: %w", err)
			}
			noteSnapshot, err := a.collectNotes(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("refresh notes: %w", err)
			}
			state.replaceSnapshot(snapshot)
			state.replaceNoteSnapshot(noteSnapshot)
			if state.activePage() == PageDebug {
				state.debugSnapshot = a.collectDebug(ctx)
				state.clampDebugOffset()
			}
			if state.activePage() == PageSettings {
				selectedRepository, selected := state.selectedRepositorySetting()
				state.settingsSnapshot = a.collectSettings(ctx)
				if selected {
					state.restoreSettingsSelection(selectedRepository.ID)
				} else {
					state.clampSelection()
				}
			}
			if a.Actions != nil {
				a.Actions.Reconcile(snapshot)
			}
			if state.detail != nil {
				state.refreshDetailRow()
				a.reloadDetail(ctx, &state)
			}
			state.refreshNoteDetail()
			if err := a.writeFrame(state); err != nil {
				return err
			}
		case _, ok := <-resizes:
			if !ok {
				resizes = nil
				continue
			}
			size, err := a.Terminal.Size()
			if err != nil {
				return fmt.Errorf("read terminal size: %w", err)
			}
			state.size = size
			if state.detail != nil {
				state.detail.clampOffsets(size)
			}
			state.clampNoteDetailOffset()
			state.clampDebugOffset()
			if err := a.writeFrame(state); err != nil {
				return err
			}
		}
	}
}

func (a App) validate() error {
	switch {
	case a.Input == nil:
		return errors.New("terminal input is required")
	case a.Output == nil:
		return errors.New("terminal output is required")
	case a.Terminal == nil:
		return errors.New("terminal is required")
	case a.Ticker == nil:
		return errors.New("refresh ticker is required")
	case a.Collector == nil:
		return errors.New("snapshot collector is required")
	default:
		return nil
	}
}

func (a App) collectNotes(ctx context.Context) (note.Snapshot, error) {
	if a.Notes == nil {
		return note.Snapshot{}, nil
	}
	return a.Notes.Collect(ctx)
}

func (a App) editSelectedNote(ctx context.Context, state *loopState) (bool, error) {
	if state.showShortcuts || state.searchActive || state.confirm != nil || state.detail != nil {
		return false, nil
	}
	var item note.CatalogNote
	var ok bool
	if state.noteDetail != nil {
		item, ok = *state.noteDetail, true
	} else if state.activePage() == PageNotes {
		item, ok = state.selectedNote()
	}
	if !ok {
		return false, nil
	}
	state.setNoteEditMessage("")
	if a.NoteEditor == nil {
		state.setNoteEditMessage("Note editing is unavailable.")
		return true, nil
	}
	if err := restoreTerminalState(a.Terminal, a.Output); err != nil {
		return true, fmt.Errorf("suspend dashboard for note editor: %w", err)
	}
	changed, editErr := a.NoteEditor.Edit(ctx, item)
	if err := enterTerminalState(a.Terminal, a.Output); err != nil {
		return true, fmt.Errorf("resume dashboard after note editor: %w", err)
	}
	if editErr != nil {
		state.setNoteEditMessage("Note edit failed: " + cells.Truncate(singleLineDetail(editErr.Error()), 240))
		return true, nil
	}
	if !changed {
		state.setNoteEditMessage("Note unchanged.")
		return true, nil
	}
	refreshed, err := a.collectNotes(ctx)
	if err != nil {
		state.setNoteEditMessage("Note updated; refresh failed: " + cells.Truncate(singleLineDetail(err.Error()), 200))
		return true, nil
	}
	state.replaceNoteSnapshot(refreshed)
	state.refreshNoteDetail()
	state.setNoteEditMessage("Updated note " + singleLineNoteValue(item.ID) + ".")
	return true, nil
}

func (a App) copyNoteID(ctx context.Context, state *loopState, item note.CatalogNote) {
	state.setNoteEditMessage("")
	if a.Clipboard == nil {
		state.setNoteEditMessage("Clipboard copying is unavailable.")
		return
	}
	if err := a.Clipboard.Copy(ctx, item.ID); err != nil {
		state.setNoteEditMessage("Copy note ID failed: " + cells.Truncate(singleLineDetail(err.Error()), 220))
		return
	}
	state.setNoteEditMessage("Copied note ID " + singleLineNoteValue(item.ID) + ".")
}

func (a App) requestNoteDelete(ctx context.Context, state *loopState, item note.CatalogNote, confirm bool) {
	state.setNoteEditMessage("")
	if a.NoteActions == nil {
		state.setNoteEditMessage("Note deletion is unavailable.")
		return
	}
	apply := func(accepted bool) {
		if !accepted {
			return
		}
		if err := a.NoteActions.Delete(ctx, item); err != nil {
			state.setNoteEditMessage("Note delete failed: " + cells.Truncate(singleLineDetail(err.Error()), 220))
			return
		}
		refreshed, err := a.collectNotes(ctx)
		if err != nil {
			state.setNoteEditMessage("Note deleted; refresh failed: " + cells.Truncate(singleLineDetail(err.Error()), 200))
			return
		}
		state.replaceNoteSnapshot(refreshed)
		if state.noteDetail != nil && state.noteDetail.RepositoryID == item.RepositoryID && state.noteDetail.ID == item.ID {
			state.noteDetail = nil
			state.noteDetailOffset = 0
		}
		state.setNoteEditMessage("Deleted note " + singleLineNoteValue(item.ID) + ".")
	}
	if confirm {
		state.beginConfirm("Delete note "+singleLineNoteValue(item.ID)+"? This archives it and removes it from Notes.", apply)
		return
	}
	apply(true)
}

func (a App) setSelectedNoteTier(ctx context.Context, state *loopState, item note.CatalogNote, tier int) {
	state.setNoteEditMessage("")
	if a.NoteActions == nil {
		state.setNoteEditMessage("Note tier updates are unavailable.")
		return
	}
	changed, err := a.NoteActions.SetTier(ctx, item, tier)
	if err != nil {
		state.setNoteEditMessage("Note tier update failed: " + cells.Truncate(singleLineDetail(err.Error()), 210))
		return
	}
	if !changed {
		state.setNoteEditMessage(fmt.Sprintf("Note %s is already tier%d.", singleLineNoteValue(item.ID), tier))
		return
	}
	refreshed, err := a.collectNotes(ctx)
	if err != nil {
		state.setNoteEditMessage("Note tier updated; refresh failed: " + cells.Truncate(singleLineDetail(err.Error()), 190))
		return
	}
	state.replaceNoteSnapshot(refreshed)
	state.refreshNoteDetail()
	state.setNoteEditMessage(fmt.Sprintf("Updated note %s to tier%d.", singleLineNoteValue(item.ID), tier))
}

func (s *loopState) setNoteEditMessage(message string) {
	s.noteEditMessage = message
	s.clampNoteDetailOffset()
}

func enterTerminalState(terminal Terminal, output io.Writer) error {
	if err := terminal.EnterRaw(); err != nil {
		return fmt.Errorf("enter terminal raw mode: %w", err)
	}
	if err := term.EnterAlternateScreen(output); err != nil {
		return fmt.Errorf("enter alternate screen: %w", err)
	}
	if err := term.HideCursor(output); err != nil {
		return fmt.Errorf("hide cursor: %w", err)
	}
	return nil
}

func (a App) collectDebug(ctx context.Context) DebugSnapshot {
	if a.Debug == nil {
		return DebugSnapshot{CollectionError: "debug collector unavailable"}
	}
	snapshot, err := a.Debug.Collect(ctx)
	if err != nil {
		snapshot.CollectionError = err.Error()
	}
	return snapshot
}

func (a App) collectSettings(ctx context.Context) SettingsSnapshot {
	if a.Settings == nil {
		return SettingsSnapshot{CollectionError: "settings service unavailable"}
	}
	snapshot, err := a.Settings.Collect(ctx)
	if err != nil {
		snapshot.CollectionError = err.Error()
	}
	return snapshot
}

func (a App) writeFrame(state loopState) error {
	var frame bytes.Buffer
	switch {
	case state.noteDetail != nil:
		message := state.noteEditMessage
		if prompt := state.confirmMessage(); prompt != "" {
			message = prompt + " [y/n]"
		}
		frame.WriteString(renderNoteDetailWithMessage(*state.noteDetail, state.size.Width, state.size.Height, state.noteDetailOffset, message))
	case state.detail != nil:
		frame.WriteString(RenderDetail(DetailModel{
			Plan:            state.detail.plan,
			Row:             state.detail.row,
			Log:             state.detail.log,
			SliceLog:        state.detail.sliceLogs[state.detail.selectedSliceID],
			SelectedSliceID: state.detail.selectedSliceID,
			SliceOpen:       state.detail.sliceOpen,
			ActiveTab:       state.detail.activeTab,
			OverviewOffset:  state.detail.overviewOffset,
			ActivityOffset:  state.detail.activityOffset,
			SliceOffset:     state.detail.sliceOffset,
			Width:           state.size.Width,
			Height:          state.size.Height,
			UseColor:        state.profile.supportsColor(),
			Profile:         state.profile,
			ShowShortcuts:   state.showShortcuts,
			ScopeExpanded:   state.detail.scopeExpanded,
			LoadError:       state.detail.loadError,
			FollowError:     state.detail.followError,
			Inspection:      state.detail.inspection,
		}))
	default:
		frame.WriteString(Render(Model{
			Snapshot:            state.snapshot,
			NoteSnapshot:        state.noteSnapshot,
			DebugSnapshot:       state.debugSnapshot,
			SettingsSnapshot:    state.settingsSnapshot,
			Page:                state.activePage(),
			Selected:            state.selected,
			Width:               state.size.Width,
			Height:              state.size.Height,
			Now:                 state.currentTime(),
			FocusRepositoryID:   state.focusRepositoryID,
			FocusRepositoryName: state.focusRepositoryName,
			Profile:             state.profile,
			ShowShortcuts:       state.showShortcuts,
			SearchQuery:         state.searchQuery,
			SearchActive:        state.searchActive,
			DebugOffset:         state.debugOffset,
			ConfirmMessage:      state.confirmMessage(),
			ActionLabels:        a.Actions.labels(),
			ActionMessage:       a.Actions.statusMessage(),
			NoteMessage:         state.noteEditMessage,
			SettingsMessage:     state.settingsMessage,
		}))
	}
	contents := frame.Bytes()
	n, err := a.Output.Write(contents)
	if err != nil {
		return fmt.Errorf("render dashboard: %w", err)
	}
	if n != len(contents) {
		return fmt.Errorf("render dashboard: %w", io.ErrShortWrite)
	}
	return nil
}

func readInput(ctx context.Context, input io.Reader, results chan<- inputResult) {
	decoder := term.NewDecoder(input)
	for {
		key, err := decoder.ReadKey()
		resume := make(chan struct{})
		select {
		case results <- inputResult{key: key, err: err, resume: resume}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
		select {
		case <-resume:
		case <-ctx.Done():
			return
		}
	}
}

func (a App) handleKey(ctx context.Context, state *loopState, key term.KeyEvent) bool {
	if key.Key == term.KeyCtrlC {
		return true
	}
	if state.showShortcuts {
		switch {
		case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
			state.showShortcuts = false
			state.interruptEscape()
			return false
		case key.Key == term.KeyRune && key.Rune == '?':
			state.showShortcuts = false
			state.interruptEscape()
			return false
		case quitKey(key):
			return true
		default:
			return false
		}
	}
	if state.confirm != nil {
		return state.handleKey(key)
	}
	if state.searchActive {
		state.handleSearchKey(key)
		return false
	}
	if quitKey(key) {
		return true
	}
	if state.detail == nil && state.noteDetail == nil && state.handleListJumpKey(key) {
		return false
	}
	if state.noteDetail == nil && key.Key == term.KeyRune && key.Rune == '?' {
		state.showShortcuts = true
		state.interruptEscape()
		return false
	}
	if state.noteDetail != nil {
		state.interruptEscape()
		item := *state.noteDetail
		switch {
		case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
			state.noteDetail = nil
			state.noteDetailOffset = 0
		case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
			state.moveNoteDetail(-1)
		case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
			state.moveNoteDetail(1)
		case key.Key == term.KeyPageUp:
			state.moveNoteDetail(-state.noteDetailPageStep())
		case key.Key == term.KeyPageDown:
			state.moveNoteDetail(state.noteDetailPageStep())
		case key.Key == term.KeyRune && key.Rune == 'g':
			state.noteDetailOffset = 0
		case key.Key == term.KeyRune && key.Rune == 'G':
			state.noteDetailOffset = state.noteDetailMaxOffset()
		case key.Key == term.KeyRune && key.Rune == 'c':
			a.copyNoteID(ctx, state, item)
		case key.Key == term.KeyRune && key.Rune == 'd':
			a.requestNoteDelete(ctx, state, item, true)
		case key.Key == term.KeyRune && key.Rune == 'D':
			a.requestNoteDelete(ctx, state, item, false)
		case key.Key == term.KeyRune && key.Rune >= '0' && key.Rune <= '3':
			a.setSelectedNoteTier(ctx, state, item, int(key.Rune-'0'))
		}
		return false
	}
	if state.detail != nil {
		state.interruptEscape()
		switch {
		case (key.Key == term.KeyEsc || key.Key == term.KeyBackspace) && state.detail.sliceOpen:
			state.detail.sliceOpen = false
			state.detail.sliceOffset = 0
		case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
			state.closeDetail()
		case !state.detail.sliceOpen && key.Key == term.KeyTab:
			state.detail.moveTab(1, state.size)
		case !state.detail.sliceOpen && key.Key == term.KeyShiftTab:
			state.detail.moveTab(-1, state.size)
		case !state.detail.sliceOpen && key.Key == term.KeyArrowRight:
			a.movePlanDetail(ctx, state, 1)
		case !state.detail.sliceOpen && key.Key == term.KeyArrowLeft:
			a.movePlanDetail(ctx, state, -1)
		case !state.detail.sliceOpen && state.detail.activeTab == detailTabSlices && key.Key == term.KeyEnter:
			if _, ok := findDetailSlice(state.detail.plan, state.detail.selectedSliceID); ok {
				state.detail.sliceOpen = true
				state.detail.sliceOffset = 0
			}
		case !state.detail.sliceOpen && state.detail.activeTab == detailTabOverview && key.Key == term.KeyRune && (key.Rune == 'e' || key.Rune == 'E'):
			state.detail.scopeExpanded = !state.detail.scopeExpanded
			state.detail.clampOffsets(state.size)
		case state.detail.sliceOpen && (key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k')):
			state.detail.moveSliceDetail(-1, state.size)
		case state.detail.sliceOpen && (key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j')):
			state.detail.moveSliceDetail(1, state.size)
		case state.detail.sliceOpen && key.Key == term.KeyPageUp:
			state.detail.moveSliceDetail(-detailPageStep(state.size, 6), state.size)
		case state.detail.sliceOpen && key.Key == term.KeyPageDown:
			state.detail.moveSliceDetail(detailPageStep(state.size, 6), state.size)
		case state.detail.sliceOpen && key.Key == term.KeyRune && key.Rune == 'g':
			state.detail.sliceOffset = 0
		case state.detail.sliceOpen && key.Key == term.KeyRune && key.Rune == 'G':
			state.detail.sliceOffset = state.detail.sliceMaxOffset(state.size)
		case !state.detail.sliceOpen && (key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k')):
			state.detail.moveVertical(-1, state.size)
		case !state.detail.sliceOpen && (key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j')):
			state.detail.moveVertical(1, state.size)
		case !state.detail.sliceOpen && key.Key == term.KeyPageUp:
			state.detail.moveVertical(-state.detail.pageStep(state.size), state.size)
		case !state.detail.sliceOpen && key.Key == term.KeyPageDown:
			state.detail.moveVertical(state.detail.pageStep(state.size), state.size)
		case !state.detail.sliceOpen && key.Key == term.KeyRune && key.Rune == 'g':
			state.detail.jumpVertical(false, state.size)
		case !state.detail.sliceOpen && key.Key == term.KeyRune && key.Rune == 'G':
			state.detail.jumpVertical(true, state.size)
		}
		return false
	}
	if normalizedSearchQuery(state.searchQuery) != "" && (key.Key == term.KeyEsc || key.Key == term.KeyBackspace) {
		state.clearSearch()
		return false
	}
	if (state.activePage() == PagePlans || state.activePage() == PageNotes) && key.Key == term.KeyRune && key.Rune == '/' {
		state.setSearchQuery("")
		state.searchActive = true
		state.interruptEscape()
		return false
	}
	if key.Key != term.KeyEsc {
		state.interruptEscape()
	}
	if key.Key == term.KeyPageUp || key.Key == term.KeyPageDown {
		direction := 1
		if key.Key == term.KeyPageUp {
			direction = -1
		}
		state.movePage(direction)
		return false
	}
	if state.activePage() == PageSettings && key.Key == term.KeyRune && (key.Rune == 'p' || key.Rune == 'P') {
		repository, ok := state.selectedRepositorySetting()
		if !ok || a.Settings == nil {
			return false
		}
		next := nextPullRequestSetting(repository.PullRequest)
		currentLabel := pullRequestSetting(repository.PullRequest, state.settingsSnapshot.InheritedPullRequest)
		nextLabel := pullRequestSetting(next, state.settingsSnapshot.InheritedPullRequest)
		state.beginConfirm(fmt.Sprintf("Set %s pull_request from %s to %s?", displayValue(repository.Name), currentLabel, nextLabel), func(accepted bool) {
			if !accepted {
				return
			}
			if err := a.Settings.SetPullRequestDefault(ctx, repository.ID, next); err != nil {
				state.settingsMessage = "settings update failed: " + err.Error()
				return
			}
			state.settingsSnapshot = a.collectSettings(ctx)
			state.restoreSettingsSelection(repository.ID)
			state.settingsMessage = "Updated " + displayValue(repository.Name) + " pull_request to " + nextLabel + "."
		})
		return false
	}
	row, selected := state.selectedRow()
	if state.activePage() == PageNotes && key.Key == term.KeyRune {
		if item, ok := state.selectedNote(); ok {
			switch {
			case key.Rune == 'c':
				a.copyNoteID(ctx, state, item)
				return false
			case key.Rune == 'd':
				a.requestNoteDelete(ctx, state, item, true)
				return false
			case key.Rune == 'D':
				a.requestNoteDelete(ctx, state, item, false)
				return false
			case key.Rune >= '0' && key.Rune <= '3':
				a.setSelectedNoteTier(ctx, state, item, int(key.Rune-'0'))
				return false
			}
		}
	}
	if state.activePage() == PageNotes && key.Key == term.KeyEnter {
		if item, ok := state.selectedNote(); ok {
			state.noteDetail = &item
			state.noteDetailOffset = 0
		}
		return false
	}
	if state.activePage() == PagePlans && key.Key == term.KeyEnter && selected && row.PlanDir != "" {
		a.openDetail(ctx, state, row)
		return false
	}
	if state.activePage() == PagePlans && a.Actions != nil && key.Key == term.KeyRune {
		if key.Rune == 'M' {
			if repository, ok := state.mergeRepositoryRow(); ok {
				if message, prompt := a.Actions.MergeAllPrompt(repository); prompt {
					state.beginConfirm(message, func(accepted bool) {
						if accepted {
							a.Actions.MergeAll(ctx, repository)
						}
					})
				}
			}
			return false
		}
		if !selected {
			return state.handleKey(key)
		}
		switch key.Rune {
		case 'r', 'R':
			a.Actions.RunPlan(ctx, row)
			return false
		case 'a', 'A':
			if message, ok := a.Actions.ApprovalPrompt(row); ok {
				state.beginConfirm(message, func(accepted bool) {
					if accepted {
						a.Actions.ApproveSlice(ctx, row)
					}
				})
			}
			return false
		case 'm':
			if message, ok := a.Actions.MergePlanPrompt(row); ok {
				state.beginConfirm(message, func(accepted bool) {
					if accepted {
						a.Actions.MergePlan(ctx, row)
					}
				})
			}
			return false
		}
	}
	return state.handleKey(key)
}

func (a App) openDetail(ctx context.Context, state *loopState, row monitor.Row) {
	detailCtx, cancel := context.WithCancel(ctx)
	detail := &detailState{row: row, ctx: detailCtx, cancel: cancel, activeTab: detailTabOverview, inspection: detailInspectionView{status: detailInspectionUnavailable}}
	if row.Liveness == monitor.LivenessLive {
		detail.activeTab = detailTabActivity
	}
	state.detail = detail

	loaded, err := a.Details.ResolvePlan(detailCtx, row.PlanDir)
	if err != nil {
		detail.loadError = err.Error()
		return
	}
	detail.plan = loaded
	detail.reconcileSliceSelection()
	a.startDetailInspection(detail)
	seed, err := a.Details.ReadLogTail(row.PlanDir, 0)
	if err != nil {
		detail.followError = err.Error()
		return
	}
	detail.log = tailDetailLog(seed, detailLogTailLines)
	detail.sliceLogs, detail.activeLogSlice = projectSliceLogs(seed, detailLogKeepLines)
	detail.activityOffset = detail.maxOffset(detailTabActivity, state.size)
	updates := make(chan detailFollowUpdate, 16)
	detail.updates = updates
	go followDetailLog(detailCtx, a.Details, row.PlanDir, seed, updates)
}

func (a App) movePlanDetail(ctx context.Context, state *loopState, delta int) {
	if state.detail == nil || delta == 0 {
		return
	}
	rows := state.visibleRows()
	current := state.selected
	for index, row := range rows {
		if row.RepositoryID == state.detail.row.RepositoryID && row.PlanID == state.detail.row.PlanID {
			current = index
			break
		}
	}
	for candidate := current + delta; candidate >= 0 && candidate < len(rows); candidate += delta {
		if rows[candidate].PlanDir == "" {
			continue
		}
		tab := state.detail.activeTab
		state.closeDetail()
		state.selected = candidate
		a.openDetail(ctx, state, rows[candidate])
		if state.detail != nil {
			state.detail.activeTab = tab
			state.detail.clampOffsets(state.size)
		}
		return
	}
}

func (a App) startDetailInspection(detail *detailState) {
	key := detailInspectionKey(detail.plan)
	if key == detail.inspectionKey {
		return
	}
	if detail.inspectionCancel != nil {
		detail.inspectionCancel()
		detail.inspectionCancel = nil
	}
	detail.inspectionKey = key
	detail.inspectionUpdates = nil
	if a.Inspector == nil || detail.plan == nil {
		detail.inspection = detailInspectionView{status: detailInspectionUnavailable}
		return
	}
	inspectionCtx, cancel := context.WithCancel(detail.ctx)
	detail.inspectionCancel = cancel
	detail.inspection = detailInspectionView{status: detailInspectionLoading}
	updates := make(chan detailInspectionUpdate, 1)
	detail.inspectionUpdates = updates
	loaded := detail.plan
	go func() {
		result, err := a.Inspector.Inspect(inspectionCtx, loaded)
		update := detailInspectionUpdate{key: key, result: result, err: err}
		select {
		case updates <- update:
		case <-inspectionCtx.Done():
		}
		close(updates)
	}()
}

func (a App) reloadDetail(ctx context.Context, state *loopState) {
	loaded, err := a.Details.ResolvePlan(ctx, state.detail.row.PlanDir)
	if err != nil {
		state.detail.loadError = err.Error()
		return
	}
	state.detail.plan = loaded
	state.detail.reconcileSliceSelection()
	a.startDetailInspection(state.detail)
	state.detail.clampOffsets(state.size)
	state.detail.loadError = ""
}

func (s *loopState) detailUpdates() <-chan detailFollowUpdate {
	if s.detail == nil {
		return nil
	}
	return s.detail.updates
}

func (s *loopState) inspectionUpdates() <-chan detailInspectionUpdate {
	if s.detail == nil {
		return nil
	}
	return s.detail.inspectionUpdates
}

func (s *loopState) refreshDetailRow() {
	if s.detail == nil {
		return
	}
	for _, row := range s.snapshot.Rows {
		if row.RepositoryID == s.detail.row.RepositoryID && row.PlanID == s.detail.row.PlanID {
			s.detail.row = row
			return
		}
	}
}

func (d *detailState) appendLog(text string) {
	d.log = tailDetailLog(d.log+text, detailLogKeepLines)
	if d.sliceLogs == nil {
		d.sliceLogs = make(map[string]string)
	}
	appendSliceLogRecords(d.sliceLogs, &d.activeLogSlice, text, detailLogKeepLines)
}

func (d *detailState) reconcileSliceSelection() {
	ordered := orderedDetailSlices(d.plan)
	for _, slice := range ordered {
		if slice.ID == d.selectedSliceID {
			return
		}
	}
	d.selectedSliceID = ""
	if d.plan != nil && d.plan.State.Plan.CurrentSlice != nil {
		current := *d.plan.State.Plan.CurrentSlice
		for _, slice := range ordered {
			if slice.ID == current {
				d.selectedSliceID = current
				return
			}
		}
	}
	if len(ordered) > 0 {
		d.selectedSliceID = ordered[0].ID
	} else {
		d.sliceOpen = false
	}
}

func (d *detailState) moveSlice(delta int) {
	ordered := orderedDetailSlices(d.plan)
	if len(ordered) == 0 {
		return
	}
	index := 0
	for candidate := range ordered {
		if ordered[candidate].ID == d.selectedSliceID {
			index = candidate
			break
		}
	}
	index = max(0, min(len(ordered)-1, index+delta))
	d.selectedSliceID = ordered[index].ID
}

func (d *detailState) moveTab(delta int, size term.Size) {
	d.activeTab = detailTab((int(d.activeTab) + delta + int(detailTabCount)) % int(detailTabCount))
	d.clampOffsets(size)
}

func (d *detailState) moveVertical(delta int, size term.Size) {
	switch d.activeTab {
	case detailTabSlices:
		d.moveSlice(delta)
	case detailTabActivity:
		d.activityOffset = max(0, min(d.maxOffset(detailTabActivity, size), d.activityOffset+delta))
	default:
		d.overviewOffset = max(0, min(d.maxOffset(detailTabOverview, size), d.overviewOffset+delta))
	}
}

func (d *detailState) pageStep(size term.Size) int {
	fixedLines := planDetailFixedLines
	if d.activeTab == detailTabOverview {
		fixedLines = planOverviewFixedLines
	}
	return detailPageStep(size, fixedLines)
}

func detailPageStep(size term.Size, fixedLines int) int {
	return max(size.Height-fixedLines-1, 1)
}

func (d *detailState) jumpVertical(bottom bool, size term.Size) {
	if d.activeTab == detailTabSlices {
		ordered := orderedDetailSlices(d.plan)
		if len(ordered) > 0 {
			index := 0
			if bottom {
				index = len(ordered) - 1
			}
			d.selectedSliceID = ordered[index].ID
		}
		return
	}
	offset := 0
	if bottom {
		offset = d.maxOffset(d.activeTab, size)
	}
	if d.activeTab == detailTabActivity {
		d.activityOffset = offset
	} else {
		d.overviewOffset = offset
	}
}

func (d *detailState) moveSliceDetail(delta int, size term.Size) {
	d.sliceOffset = max(0, min(d.sliceMaxOffset(size), d.sliceOffset+delta))
}

func (d *detailState) sliceMaxOffset(size term.Size) int {
	log := d.sliceLogs[d.selectedSliceID]
	if log == "" {
		log = filterSliceLog(d.log, d.selectedSliceID)
	}
	return sliceDetailMaxOffset(d.plan, d.selectedSliceID, size.Width, size.Height, log)
}

func (d *detailState) clampOffsets(size term.Size) {
	d.overviewOffset = max(0, min(d.overviewOffset, d.maxOffset(detailTabOverview, size)))
	d.activityOffset = max(0, min(d.activityOffset, d.maxOffset(detailTabActivity, size)))
	d.sliceOffset = max(0, min(d.sliceOffset, d.sliceMaxOffset(size)))
}

func (d *detailState) maxOffset(tab detailTab, size term.Size) int {
	fixedLines := planDetailFixedLines
	if tab == detailTabOverview {
		fixedLines = planOverviewFixedLines
	}
	height := max(size.Height-fixedLines, 0)
	if height == 0 {
		return 0
	}
	var lines []string
	if tab == detailTabActivity {
		lines = renderActivityPane(d.log, d.followError, size.Width, int(^uint(0)>>1), 0)
	} else {
		lines = renderOverviewPane(d.plan, d.row, size.Width, int(^uint(0)>>1), 0, d.inspection, ProfileNone, d.scopeExpanded)
	}
	return max(len(lines)-height, 0)
}

func (s *loopState) closeDetail() {
	if s.detail == nil {
		return
	}
	if s.detail.inspectionCancel != nil {
		s.detail.inspectionCancel()
	}
	if s.detail.cancel != nil {
		s.detail.cancel()
	}
	s.detail = nil
}

func (s *loopState) handleKey(key term.KeyEvent) bool {
	if key.Key == term.KeyCtrlC {
		return true
	}
	if s.confirm != nil {
		switch {
		case key.Key == term.KeyRune && (key.Rune == 'y' || key.Rune == 'Y'):
			s.resolveConfirm(true)
		case key.Key == term.KeyRune && (key.Rune == 'n' || key.Rune == 'N'):
			s.resolveConfirm(false)
		case key.Key == term.KeyEsc || key.Key == term.KeyBackspace || quitKey(key):
			s.resolveConfirm(false)
		}
		return false
	}
	if quitKey(key) {
		return true
	}
	if key.Key != term.KeyEsc {
		s.interruptEscape()
	}
	if s.activePage() == PageDebug {
		handled := true
		switch key.Key {
		case term.KeyArrowUp:
			s.debugOffset = max(s.debugOffset-1, 0)
		case term.KeyArrowDown:
			s.debugOffset = min(s.debugOffset+1, s.debugPageMaxOffset())
		case term.KeyRune:
			switch key.Rune {
			case 'k':
				s.debugOffset = max(s.debugOffset-1, 0)
			case 'j':
				s.debugOffset = min(s.debugOffset+1, s.debugPageMaxOffset())
			case 'g':
				s.debugOffset = 0
			case 'G':
				s.debugOffset = s.debugPageMaxOffset()
			default:
				handled = false
			}
		default:
			handled = false
		}
		if handled {
			return false
		}
	}

	switch {
	case key.Key == term.KeyEsc:
		now := s.currentTime()
		if !s.lastRootEscape.IsZero() {
			elapsed := now.Sub(s.lastRootEscape)
			if elapsed >= 0 && elapsed <= time.Second {
				return true
			}
		}
		s.lastRootEscape = now
	case key.Key == term.KeyTab || key.Key == term.KeyArrowRight:
		s.switchPage(1)
	case key.Key == term.KeyShiftTab || key.Key == term.KeyArrowLeft:
		s.switchPage(-1)
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		if s.selected > 0 {
			s.selected--
		}
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		if s.selected+1 < s.pageRowCount() {
			s.selected++
		}
	case (s.activePage() == PagePlans || s.activePage() == PageNotes) && key.Key == term.KeyRune && (key.Rune == 'f' || key.Rune == 'F'):
		s.toggleRepositoryFocus()
	}
	return false
}

func quitKey(key term.KeyEvent) bool {
	return key.Key == term.KeyRune && (key.Rune == 'q' || key.Rune == 'Q')
}

func (s *loopState) interruptEscape() {
	s.lastRootEscape = time.Time{}
}

func (s *loopState) handleSearchKey(key term.KeyEvent) {
	switch key.Key {
	case term.KeyEnter:
		s.searchActive = false
		if normalizedSearchQuery(s.searchQuery) == "" {
			s.setSearchQuery("")
		}
	case term.KeyEsc, term.KeyBackspace:
		s.clearSearch()
	case term.KeyRune:
		s.setSearchQuery(s.searchQuery + string(key.Rune))
	}
	s.interruptEscape()
}

func (s *loopState) clearSearch() {
	s.searchActive = false
	s.setSearchQuery("")
	s.interruptEscape()
}

func (s *loopState) setSearchQuery(query string) {
	selectedPlan, preservePlan := s.planRowAt(s.planSelection())
	selectedNote, preserveNote := s.noteAt(s.noteSelection())
	s.searchQuery = query
	s.restorePlanSelection(selectedPlan, preservePlan)
	s.restoreNoteSelection(selectedNote, preserveNote)
}

func (s loopState) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s loopState) activePage() PageID {
	return normalizePage(s.page)
}

func (s *loopState) handleListJumpKey(key term.KeyEvent) bool {
	if s.activePage() != PagePlans && s.activePage() != PageNotes {
		s.listTopPending = false
		return false
	}
	if key.Key == term.KeyRune && key.Rune == 'g' {
		if s.listTopPending {
			s.selected = 0
			s.listTopPending = false
		} else {
			s.listTopPending = true
		}
		s.interruptEscape()
		return true
	}
	s.listTopPending = false
	if key.Key == term.KeyRune && key.Rune == 'G' {
		s.selected = max(s.pageRowCount()-1, 0)
		s.interruptEscape()
		return true
	}
	return false
}

func (s *loopState) movePage(direction int) {
	step := detailPageStep(s.size, 8)
	switch s.activePage() {
	case PageDebug:
		s.debugOffset = max(0, min(s.debugPageMaxOffset(), s.debugOffset+direction*step))
	default:
		count := s.pageRowCount()
		if count > 0 {
			s.selected = max(0, min(count-1, s.selected+direction*step))
		}
	}
}

func (s *loopState) switchPage(delta int) {
	current := s.activePage()
	if s.pageSelections == nil {
		s.pageSelections = make(map[PageID]int)
	}
	s.pageSelections[current] = s.selected
	s.page = adjacentPage(current, delta)
	s.selected = s.pageSelections[s.page]
	s.clampSelection()
}

func (s loopState) pageRowCount() int {
	switch s.activePage() {
	case PagePlans:
		return len(s.visibleRows())
	case PageNotes:
		return len(s.visibleNotes())
	case PageSettings:
		return len(s.settingsSnapshot.Repositories)
	default:
		return 0
	}
}

func (s loopState) selectedRepositorySetting() (RepositorySetting, bool) {
	if s.activePage() != PageSettings || s.selected < 0 || s.selected >= len(s.settingsSnapshot.Repositories) {
		return RepositorySetting{}, false
	}
	return s.settingsSnapshot.Repositories[s.selected], true
}

func (s *loopState) restoreSettingsSelection(repositoryID string) {
	for index, repository := range s.settingsSnapshot.Repositories {
		if repository.ID == repositoryID {
			s.selected = index
			return
		}
	}
	s.clampSelection()
}

func (s loopState) visibleRows() []monitor.Row {
	rows := FilterPlanRows(s.snapshot.Rows, s.searchQuery)
	return visibleRows(rows, s.focusRepositoryID)
}

func (s loopState) planSelection() int {
	if s.activePage() == PagePlans {
		return s.selected
	}
	return s.pageSelections[PagePlans]
}

func (s loopState) noteSelection() int {
	if s.activePage() == PageNotes {
		return s.selected
	}
	return s.pageSelections[PageNotes]
}

func (s loopState) visibleNotes() []note.CatalogNote {
	snapshot := FilterNoteSnapshot(s.noteSnapshot, s.searchQuery)
	return visibleNotes(snapshot, s.focusRepositoryID)
}

func (s loopState) noteAt(index int) (note.CatalogNote, bool) {
	items := s.visibleNotes()
	if index < 0 || index >= len(items) {
		return note.CatalogNote{}, false
	}
	return items[index], true
}

func (s loopState) selectedNote() (note.CatalogNote, bool) {
	if s.activePage() != PageNotes {
		return note.CatalogNote{}, false
	}
	return s.noteAt(s.selected)
}

func (s loopState) planRowAt(index int) (monitor.Row, bool) {
	rows := s.visibleRows()
	if index < 0 || index >= len(rows) {
		return monitor.Row{}, false
	}
	return rows[index], true
}

func (s loopState) selectedRow() (monitor.Row, bool) {
	if s.activePage() != PagePlans {
		return monitor.Row{}, false
	}
	return s.planRowAt(s.selected)
}

func (s *loopState) beginConfirm(message string, respond func(bool)) {
	s.confirm = &confirmPrompt{message: message, respond: respond}
}

func (s *loopState) resolveConfirm(accepted bool) {
	prompt := s.confirm
	s.confirm = nil
	if prompt != nil && prompt.respond != nil {
		prompt.respond(accepted)
	}
}

func (s loopState) confirmMessage() string {
	if s.confirm == nil {
		return ""
	}
	return s.confirm.message
}

func (s *loopState) replaceSnapshot(snapshot monitor.Snapshot) {
	selected, preserve := s.planRowAt(s.planSelection())
	s.snapshot = snapshot
	if s.focusRepositoryID != "" {
		for _, row := range snapshot.Rows {
			if row.RepositoryID == s.focusRepositoryID {
				s.updateFocusMetadata(row.RepositoryName, row.RepositoryRoot)
				break
			}
		}
	}
	s.restorePlanSelection(selected, preserve)
}

func (s *loopState) replaceNoteSnapshot(snapshot note.Snapshot) {
	selected, preserve := s.noteAt(s.noteSelection())
	s.noteSnapshot = snapshot
	if s.focusRepositoryID != "" {
		for _, item := range snapshot.Notes {
			if item.RepositoryID == s.focusRepositoryID {
				s.updateFocusMetadata(item.RepositoryName, item.RepositoryRoot)
				break
			}
		}
	}
	s.restoreNoteSelection(selected, preserve)
}

func (s *loopState) updateFocusMetadata(name, root string) {
	if name != "" {
		s.focusRepositoryName = name
	}
	if root != "" {
		s.focusRepositoryRoot = root
	}
}

func (s *loopState) toggleRepositoryFocus() {
	planSelected, preservePlan := s.planRowAt(s.planSelection())
	noteSelected, preserveNote := s.noteAt(s.noteSelection())
	if s.focusRepositoryID != "" {
		s.focusRepositoryID = ""
		s.focusRepositoryName = ""
		s.focusRepositoryRoot = ""
		s.restorePlanSelection(planSelected, preservePlan)
		s.restoreNoteSelection(noteSelected, preserveNote)
		return
	}
	if s.activePage() == PageNotes {
		if !preserveNote || noteSelected.RepositoryID == "" || noteSelected.ID == "" {
			return
		}
		s.focusRepositoryID = noteSelected.RepositoryID
		s.focusRepositoryName = noteSelected.RepositoryName
		s.focusRepositoryRoot = noteSelected.RepositoryRoot
	} else {
		if !preservePlan || planSelected.Kind == monitor.RowKindRepositoryWarning || planSelected.RepositoryID == "" || planSelected.PlanID == "" {
			return
		}
		s.focusRepositoryID = planSelected.RepositoryID
		s.focusRepositoryName = planSelected.RepositoryName
		s.focusRepositoryRoot = planSelected.RepositoryRoot
	}
	s.restorePlanSelection(planSelected, preservePlan)
	s.restoreNoteSelection(noteSelected, preserveNote)
}

func (s loopState) mergeRepositoryRow() (monitor.Row, bool) {
	selected, ok := s.selectedRow()
	if s.focusRepositoryID == "" || (ok && selected.Status == plan.StatusAbandoned) {
		return selected, ok
	}
	if ok && selected.RepositoryID == s.focusRepositoryID && actionableRow(selected) {
		return selected, true
	}
	if s.focusRepositoryRoot == "" {
		return monitor.Row{}, false
	}
	return monitor.Row{
		Kind:           monitor.RowKindPlan,
		RepositoryID:   s.focusRepositoryID,
		RepositoryName: s.focusRepositoryName,
		RepositoryRoot: s.focusRepositoryRoot,
		PlanID:         "repository batch",
	}, true
}

func (s *loopState) restoreNoteSelection(selected note.CatalogNote, preserve bool) {
	index := s.noteSelection()
	if preserve && selected.RepositoryID != "" && selected.ID != "" {
		identity := noteIdentity(selected)
		for candidate, item := range s.visibleNotes() {
			if noteIdentity(item) == identity {
				index = candidate
				if s.activePage() == PageNotes {
					s.selected = index
				} else {
					if s.pageSelections == nil {
						s.pageSelections = make(map[PageID]int)
					}
					s.pageSelections[PageNotes] = index
				}
				return
			}
		}
	}
	count := len(s.visibleNotes())
	if count == 0 {
		index = 0
	} else {
		index = max(0, min(index, count-1))
	}
	if s.activePage() == PageNotes {
		s.selected = index
	} else {
		if s.pageSelections == nil {
			s.pageSelections = make(map[PageID]int)
		}
		s.pageSelections[PageNotes] = index
	}
}

func (s *loopState) refreshNoteDetail() {
	if s.noteDetail == nil {
		return
	}
	identity := noteIdentity(*s.noteDetail)
	for _, item := range s.noteSnapshot.Notes {
		if noteIdentity(item) == identity {
			refreshed := item
			s.noteDetail = &refreshed
			s.clampNoteDetailOffset()
			return
		}
	}
	s.noteDetail = nil
	s.noteDetailOffset = 0
}

func (s *loopState) moveNoteDetail(delta int) {
	s.noteDetailOffset = max(0, min(s.noteDetailOffset+delta, s.noteDetailMaxOffset()))
}

func (s loopState) noteDetailPageStep() int {
	return detailPageStep(s.size, noteDetailHeaderLines+2)
}

func (s *loopState) clampNoteDetailOffset() {
	s.noteDetailOffset = max(0, min(s.noteDetailOffset, s.noteDetailMaxOffset()))
}

func (s *loopState) noteDetailMaxOffset() int {
	if s.noteDetail == nil {
		return 0
	}
	bodyLines := len(renderNoteText(s.noteDetail.Text, s.size.Width))
	footerLines := 1
	if strings.TrimSpace(s.noteEditMessage) != "" {
		footerLines++
	}
	bodyHeight := noteDetailBodyHeightWithFooter(bodyLines, s.size.Height, footerLines)
	return max(0, bodyLines-bodyHeight)
}

func (s *loopState) restorePlanSelection(selected monitor.Row, preserve bool) {
	index := s.planSelection()
	if preserve && selected.RepositoryID != "" && selected.PlanID != "" {
		for candidate, row := range s.visibleRows() {
			if row.RepositoryID == selected.RepositoryID && row.PlanID == selected.PlanID {
				index = candidate
				if s.activePage() == PagePlans {
					s.selected = index
				} else {
					if s.pageSelections == nil {
						s.pageSelections = make(map[PageID]int)
					}
					s.pageSelections[PagePlans] = index
				}
				return
			}
		}
	}
	count := len(s.visibleRows())
	if count == 0 {
		index = 0
	} else {
		index = max(0, min(index, count-1))
	}
	if s.activePage() == PagePlans {
		s.selected = index
	} else {
		if s.pageSelections == nil {
			s.pageSelections = make(map[PageID]int)
		}
		s.pageSelections[PagePlans] = index
	}
}

func (s *loopState) clampSelection() {
	count := s.pageRowCount()
	if count == 0 {
		s.selected = 0
		return
	}
	s.selected = max(0, min(s.selected, count-1))
}

func (s loopState) debugPageMaxOffset() int {
	return debugMaxOffset(Model{
		Snapshot:            s.snapshot,
		NoteSnapshot:        s.noteSnapshot,
		DebugSnapshot:       s.debugSnapshot,
		Width:               s.size.Width,
		Height:              s.size.Height,
		Profile:             s.profile,
		SearchQuery:         s.searchQuery,
		SearchActive:        s.searchActive,
		FocusRepositoryID:   s.focusRepositoryID,
		FocusRepositoryName: s.focusRepositoryName,
	})
}

func (s *loopState) clampDebugOffset() {
	s.debugOffset = max(0, min(s.debugOffset, s.debugPageMaxOffset()))
}

type colorTerminalWriter interface {
	IsTerminal() bool
}

func outputSupportsColor(output io.Writer) Profile {
	isTerminal := false
	if terminal, ok := output.(colorTerminalWriter); ok {
		isTerminal = terminal.IsTerminal()
	} else if file, ok := output.(*os.File); ok {
		info, err := file.Stat()
		isTerminal = err == nil && info.Mode()&os.ModeCharDevice != 0
	}
	return detectProfile(isTerminal, os.Getenv)
}

func restoreTerminalState(terminal Terminal, output io.Writer) error {
	var firstErr error
	if err := term.ShowCursor(output); err != nil {
		firstErr = fmt.Errorf("show cursor: %w", err)
	}
	if err := term.LeaveAlternateScreen(output); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("leave alternate screen: %w", err)
	}
	if err := terminal.Restore(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("restore terminal: %w", err)
	}
	return firstErr
}
