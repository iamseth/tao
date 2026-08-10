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

// App owns one interactive dashboard event loop. Its boundaries are injectable
// so terminal behavior can be tested without taking over a real terminal.
type App struct {
	Input     io.Reader
	Output    io.Writer
	Terminal  Terminal
	Ticker    Ticker
	Collector SnapshotCollector
	Actions   *Actions
	Details   DetailRepository
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
	snapshot      monitor.Snapshot
	selected      int
	size          term.Size
	showCompleted bool
	useColor      bool
	confirm       *confirmPrompt
	detail        *detailState
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
	state := loopState{
		snapshot:      snapshot,
		size:          size,
		showCompleted: true,
		useColor:      outputSupportsColor(a.Output),
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
			state.replaceSnapshot(snapshot)
			if a.Actions != nil {
				a.Actions.Reconcile(snapshot)
			}
			if state.detail != nil {
				state.refreshDetailRow()
				a.reloadDetail(ctx, &state)
			}
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

func (a App) writeFrame(state loopState) error {
	var frame bytes.Buffer
	if state.detail != nil {
		frame.WriteString(RenderDetail(DetailModel{
			Plan:        state.detail.plan,
			Row:         state.detail.row,
			Log:         state.detail.log,
			Width:       state.size.Width,
			Height:      state.size.Height,
			UseColor:    state.useColor,
			LoadError:   state.detail.loadError,
			FollowError: state.detail.followError,
		}))
	} else {
		frame.WriteString(Render(Model{
			Snapshot:       state.snapshot,
			Selected:       state.selected,
			Width:          state.size.Width,
			Height:         state.size.Height,
			HideCompleted:  !state.showCompleted,
			UseColor:       state.useColor,
			ConfirmMessage: state.confirmMessage(),
			ActionLabels:   a.Actions.labels(),
			ActionMessage:  a.Actions.statusMessage(),
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
	if state.detail != nil {
		if key.Key == term.KeyEsc {
			state.closeDetail()
		}
		return false
	}
	if state.confirm != nil {
		return state.handleKey(key)
	}
	row, selected := state.selectedRow()
	if key.Key == term.KeyEnter && selected && row.PlanDir != "" {
		a.openDetail(ctx, state, row)
		return false
	}
	if a.Actions != nil && key.Key == term.KeyRune && selected {
		switch key.Rune {
		case 'r', 'R':
			a.Actions.RunPlan(ctx, row)
			return false
		case 'q', 'Q':
			a.Actions.QueuePlan(ctx, row, state.snapshot)
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
		case key.Key == term.KeyEsc:
			s.resolveConfirm(false)
		}
		return false
	}

	switch {
	case key.Key == term.KeyEsc:
		return true
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		if s.selected > 0 {
			s.selected--
		}
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		if s.selected+1 < len(visibleRows(s.snapshot.Rows, s.showCompleted)) {
			s.selected++
		}
	case key.Key == term.KeyRune && (key.Rune == 'c' || key.Rune == 'C'):
		s.showCompleted = !s.showCompleted
		s.clampSelection()
	}
	return false
}

func (s loopState) selectedRow() (monitor.Row, bool) {
	rows := visibleRows(s.snapshot.Rows, s.showCompleted)
	if s.selected < 0 || s.selected >= len(rows) {
		return monitor.Row{}, false
	}
	return rows[s.selected], true
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
	selected, preserve := s.selectedRow()
	s.snapshot = snapshot
	if preserve && selected.RepositoryID != "" && selected.PlanID != "" {
		for index, row := range visibleRows(snapshot.Rows, s.showCompleted) {
			if row.RepositoryID == selected.RepositoryID && row.PlanID == selected.PlanID {
				s.selected = index
				return
			}
		}
	}
	s.clampSelection()
}

func (s *loopState) clampSelection() {
	count := len(visibleRows(s.snapshot.Rows, s.showCompleted))
	if count == 0 {
		s.selected = 0
		return
	}
	if s.selected < 0 {
		s.selected = 0
	}
	if s.selected >= count {
		s.selected = count - 1
	}
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
