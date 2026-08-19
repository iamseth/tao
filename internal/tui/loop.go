// Package tui implements Tao's interactive terminal dashboard.
package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
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

// NoteSnapshotCollector supplies read-only open-note refreshes.
type NoteSnapshotCollector interface {
	Collect(context.Context) (note.Snapshot, error)
}

// App owns one interactive dashboard event loop. Its boundaries are injectable
// so terminal behavior can be tested without taking over a real terminal.
type App struct {
	Input     io.Reader
	Output    io.Writer
	Terminal  Terminal
	Ticker    Ticker
	Collector SnapshotCollector
	Notes     NoteSnapshotCollector
	Actions   *Actions
	Details   DetailRepository
	Now       func() time.Time
}

type inputResult struct {
	key term.KeyEvent
	err error
}

type confirmPrompt struct {
	message string
	respond func(bool)
}

type loopState struct {
	snapshot            monitor.Snapshot
	noteSnapshot        note.Snapshot
	page                PageID
	selected            int
	pageSelections      map[PageID]int
	size                term.Size
	showCompleted       bool
	focusRepositoryID   string
	focusRepositoryName string
	focusRepositoryRoot string
	useColor            bool
	confirm             *confirmPrompt
	detail              *detailState
	noteDetail          *note.CatalogNote
	noteDetailOffset    int
	now                 func() time.Time
	lastRootEscape      time.Time
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
	state := loopState{
		snapshot:     snapshot,
		noteSnapshot: noteSnapshot,
		size:         size,
		useColor:     outputSupportsColor(a.Output),
		now:          a.Now,
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
			if a.handleKey(loopCtx, &state, result.key) {
				return nil
			}
			if err := a.writeFrame(state); err != nil {
				return err
			}
		case update, ok := <-state.detailUpdates():
			if !ok {
				state.detail.updates = nil
				continue
			}
			if update.err != nil {
				state.detail.followError = update.err.Error()
			} else {
				state.detail.log = tailDetailLog(state.detail.log+update.text, detailLogKeepLines)
			}
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
			state.clampNoteDetailOffset()
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

func (a App) writeFrame(state loopState) error {
	var frame bytes.Buffer
	switch {
	case state.noteDetail != nil:
		frame.WriteString(renderNoteDetail(*state.noteDetail, state.size.Width, state.size.Height, state.noteDetailOffset))
	case state.detail != nil:
		frame.WriteString(RenderDetail(DetailModel{
			Plan:            state.detail.plan,
			Row:             state.detail.row,
			Log:             state.detail.log,
			SelectedSliceID: state.detail.selectedSliceID,
			SliceOpen:       state.detail.sliceOpen,
			Width:           state.size.Width,
			Height:          state.size.Height,
			UseColor:        state.useColor,
			LoadError:       state.detail.loadError,
			FollowError:     state.detail.followError,
		}))
	default:
		frame.WriteString(Render(Model{
			Snapshot:            state.snapshot,
			NoteSnapshot:        state.noteSnapshot,
			Page:                state.activePage(),
			Selected:            state.selected,
			Width:               state.size.Width,
			Height:              state.size.Height,
			Now:                 state.currentTime(),
			HideCompleted:       !state.showCompleted,
			FocusRepositoryID:   state.focusRepositoryID,
			FocusRepositoryName: state.focusRepositoryName,
			UseColor:            state.useColor,
			ConfirmMessage:      state.confirmMessage(),
			ActionLabels:        a.Actions.labels(),
			ActionMessage:       a.Actions.statusMessage(),
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
		select {
		case results <- inputResult{key: key, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (a App) handleKey(ctx context.Context, state *loopState, key term.KeyEvent) bool {
	if key.Key == term.KeyCtrlC {
		return true
	}
	if state.confirm != nil {
		return state.handleKey(key)
	}
	if quitKey(key) {
		return true
	}
	if state.noteDetail != nil {
		state.interruptEscape()
		switch {
		case key.Key == term.KeyEsc:
			state.noteDetail = nil
			state.noteDetailOffset = 0
		case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
			state.moveNoteDetail(-1)
		case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
			state.moveNoteDetail(1)
		case key.Key == term.KeyRune && key.Rune == 'g':
			state.noteDetailOffset = 0
		case key.Key == term.KeyRune && key.Rune == 'G':
			state.noteDetailOffset = state.noteDetailMaxOffset()
		}
		return false
	}
	if state.detail != nil {
		state.interruptEscape()
		switch {
		case key.Key == term.KeyEsc && state.detail.sliceOpen:
			state.detail.sliceOpen = false
		case key.Key == term.KeyEsc:
			state.closeDetail()
		case !state.detail.sliceOpen && key.Key == term.KeyEnter:
			if _, ok := findDetailSlice(state.detail.plan, state.detail.selectedSliceID); ok {
				state.detail.sliceOpen = true
			}
		case !state.detail.sliceOpen && (key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k')):
			state.detail.moveSlice(-1)
		case !state.detail.sliceOpen && (key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j')):
			state.detail.moveSlice(1)
		}
		return false
	}
	if key.Key != term.KeyEsc {
		state.interruptEscape()
	}
	row, selected := state.selectedRow()
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
	detail := &detailState{row: row, cancel: cancel}
	state.detail = detail

	loaded, err := a.Details.ResolvePlan(detailCtx, row.PlanDir)
	if err != nil {
		detail.loadError = err.Error()
		return
	}
	detail.plan = loaded
	detail.reconcileSliceSelection()
	seed, err := a.Details.ReadLogTail(row.PlanDir, detailLogTailLines)
	if err != nil {
		detail.followError = err.Error()
		return
	}
	detail.log = seed
	updates := make(chan detailFollowUpdate, 16)
	detail.updates = updates
	go followDetailLog(detailCtx, a.Details, row.PlanDir, seed, updates)
}

func (a App) reloadDetail(ctx context.Context, state *loopState) {
	loaded, err := a.Details.ResolvePlan(ctx, state.detail.row.PlanDir)
	if err != nil {
		state.detail.loadError = err.Error()
		return
	}
	state.detail.plan = loaded
	state.detail.reconcileSliceSelection()
	state.detail.loadError = ""
}

func (s *loopState) detailUpdates() <-chan detailFollowUpdate {
	if s.detail == nil {
		return nil
	}
	return s.detail.updates
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

func (s *loopState) closeDetail() {
	if s.detail == nil {
		return
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
		case key.Key == term.KeyEsc || quitKey(key):
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
	case key.Key == term.KeyArrowLeft:
		s.switchPage(-1)
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		if s.selected > 0 {
			s.selected--
		}
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		if s.selected+1 < s.pageRowCount() {
			s.selected++
		}
	case s.activePage() == PagePlans && key.Key == term.KeyRune && (key.Rune == 'c' || key.Rune == 'C'):
		s.preserveSelection(func() { s.showCompleted = !s.showCompleted })
	case key.Key == term.KeyRune && (key.Rune == 'f' || key.Rune == 'F'):
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

func (s loopState) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s loopState) activePage() PageID {
	return normalizePage(s.page)
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
	if s.activePage() == PagePlans {
		return len(s.visibleRows())
	}
	return len(s.visibleNotes())
}

func (s loopState) visibleRows() []monitor.Row {
	return visibleRows(s.snapshot.Rows, s.showCompleted, s.focusRepositoryID)
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
	return visibleNotes(s.noteSnapshot, s.focusRepositoryID)
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
	if s.focusRepositoryID == "" {
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

func (s *loopState) preserveSelection(change func()) {
	selected, preserve := s.selectedRow()
	change()
	s.restoreSelection(selected, preserve)
}

func (s *loopState) restoreSelection(selected monitor.Row, preserve bool) {
	s.restorePlanSelection(selected, preserve)
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

func (s *loopState) clampNoteDetailOffset() {
	s.noteDetailOffset = max(0, min(s.noteDetailOffset, s.noteDetailMaxOffset()))
}

func (s *loopState) noteDetailMaxOffset() int {
	if s.noteDetail == nil {
		return 0
	}
	bodyLines := len(renderNoteText(s.noteDetail.Text, s.size.Width))
	bodyHeight := noteDetailBodyHeight(bodyLines, s.size.Height)
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

type colorTerminalWriter interface {
	IsTerminal() bool
}

func outputSupportsColor(output io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if terminal, ok := output.(colorTerminalWriter); ok {
		return terminal.IsTerminal()
	}
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
