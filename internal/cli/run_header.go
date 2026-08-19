package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runheader"
	"github.com/iamseth/tao/internal/term"
)

const runHeaderRefreshInterval = time.Second

const eraseTerminalLineSequence = "\x1b[2K"

type runHeaderTerminal interface {
	Size() (term.Size, error)
	ResizeEvents(context.Context) <-chan struct{}
}

type runHeaderOutput struct {
	out      io.Writer
	terminal runHeaderTerminal
	useColor bool

	outputMu sync.Mutex
	stateMu  sync.RWMutex
	state    run.HeaderState
	size     term.Size
	cancel   context.CancelFunc
	pinned   bool
	closed   bool
}

// installRunHeader applies the pure activation gate and returns the writer and
// reporter used by one interactive run. Failure is presentation-only: callers
// retain the original writer and continue the run unchanged.
func installRunHeader(ctx context.Context, out io.Writer, noRunHeader bool) (io.Writer, run.HeaderReporter, func()) {
	terminal, ok := runHeaderTerminalForOutput(out)
	if !ok {
		return out, nil, func() {}
	}
	size, err := terminal.Size()
	if err != nil || !runHeaderEnabled(noRunHeader, outputIsTerminal(out), os.Getenv("TERM"), size.Height, size.Width) {
		return out, nil, func() {}
	}

	header := &runHeaderOutput{out: out, terminal: terminal, useColor: outputSupportsColor(out), size: size}
	if err := header.install(); err != nil {
		_ = term.ResetScrollRegion(out)
		return out, nil, func() {}
	}
	header.start(ctx)
	return header, header, header.Close
}

func runHeaderTerminalForOutput(out io.Writer) (runHeaderTerminal, bool) {
	if terminal, ok := out.(runHeaderTerminal); ok {
		return terminal, true
	}
	file, ok := out.(*os.File)
	if !ok {
		return nil, false
	}
	return term.NewOutputTerminal(file), true
}

func (w *runHeaderOutput) install() error {
	w.outputMu.Lock()
	defer w.outputMu.Unlock()
	if err := term.SetScrollRegion(w.out, runheader.LineCount+1, w.size.Height); err != nil {
		return err
	}
	w.pinned = true
	if err := w.paintLocked(false); err != nil {
		return err
	}
	return term.PositionCursor(w.out, runheader.LineCount+1, 1)
}

func (w *runHeaderOutput) start(ctx context.Context) {
	refreshCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	resizes := w.terminal.ResizeEvents(refreshCtx)
	ticker := time.NewTicker(runHeaderRefreshInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				w.tryRepaint()
			case _, ok := <-resizes:
				if !ok {
					resizes = nil
					continue
				}
				w.tryResize()
			}
		}
	}()
}

// SanitizeTerminalControls marks this writer as the pinned terminal path.
// logrecord.Render uses the marker to prevent provider output from escaping the
// managed scroll region without changing redirected presentation output.
func (*runHeaderOutput) SanitizeTerminalControls() bool { return true }

func (w *runHeaderOutput) Write(p []byte) (int, error) {
	w.outputMu.Lock()
	defer w.outputMu.Unlock()

	n, err := w.out.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err == nil && !w.closed && w.pinned {
		_ = w.paintLocked(true)
	}
	return n, err
}

// ReportHeader stores state without waiting for terminal output. The run path
// publishes best-effort snapshots and the writer repaints the newest snapshot
// on the next write or timer tick.
func (w *runHeaderOutput) ReportHeader(state run.HeaderState) {
	state.Slices = append([]run.HeaderSlice(nil), state.Slices...)
	w.stateMu.Lock()
	w.state = state
	w.stateMu.Unlock()
}

func (w *runHeaderOutput) tryRepaint() {
	if !w.outputMu.TryLock() {
		return
	}
	defer w.outputMu.Unlock()
	if !w.closed && w.pinned {
		_ = w.paintLocked(true)
	}
}

func (w *runHeaderOutput) tryResize() {
	if !w.outputMu.TryLock() {
		return
	}
	defer w.outputMu.Unlock()
	if w.closed {
		return
	}
	size, err := w.terminal.Size()
	if err != nil {
		return
	}

	wasPinned := w.pinned
	_ = term.SaveCursor(w.out)
	w.size = size
	if !runHeaderSizeEligible(size.Height, size.Width) {
		_ = term.ResetScrollRegion(w.out)
		w.pinned = false
	} else if err := term.SetScrollRegion(w.out, runheader.LineCount+1, size.Height); err == nil {
		w.pinned = true
		_ = w.paintLocked(false)
	} else {
		_ = term.ResetScrollRegion(w.out)
		w.pinned = false
	}
	if !wasPinned && w.pinned {
		_ = term.PositionCursor(w.out, runheader.LineCount+1, 1)
	} else {
		_ = term.RestoreCursor(w.out)
	}
}

func (w *runHeaderOutput) paintLocked(preserveCursor bool) error {
	if preserveCursor {
		if err := term.SaveCursor(w.out); err != nil {
			return err
		}
	}
	if preserveCursor {
		defer func() { _ = term.RestoreCursor(w.out) }()
	}

	w.stateMu.RLock()
	state := w.state
	state.Slices = append([]run.HeaderSlice(nil), state.Slices...)
	w.stateMu.RUnlock()
	for row, line := range runheader.Render(state, w.size.Width, w.useColor) {
		if err := term.PositionCursor(w.out, row+1, 1); err != nil {
			return err
		}
		if _, err := io.WriteString(w.out, eraseTerminalLineSequence); err != nil {
			return err
		}
		if _, err := io.WriteString(w.out, line); err != nil {
			return err
		}
	}
	return nil
}

// Close restores the full scroll region on every return path, including a
// recovered panic, then appends the last header snapshot in ordinary
// scrollback. All restoration writes are deliberately best-effort.
func (w *runHeaderOutput) Close() {
	if w.cancel != nil {
		w.cancel()
	}
	w.outputMu.Lock()
	defer w.outputMu.Unlock()
	if w.closed {
		return
	}
	w.closed = true

	if size, err := w.terminal.Size(); err == nil && size.Width > 0 && size.Height > 0 {
		w.size = size
	}
	_ = term.ResetScrollRegion(w.out)
	_ = term.ShowCursor(w.out)
	if w.size.Height > 0 {
		_ = term.PositionCursor(w.out, w.size.Height, 1)
	}
	_, _ = io.WriteString(w.out, "\n")

	w.stateMu.RLock()
	state := w.state
	state.Slices = append([]run.HeaderSlice(nil), state.Slices...)
	w.stateMu.RUnlock()
	_, _ = io.WriteString(w.out, strings.Join(runheader.Render(state, w.size.Width, w.useColor), "\n")+"\n")
}
