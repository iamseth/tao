// Package term provides the small, platform-specific terminal primitives used by
// Tao's interactive terminal UI.
package term

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

const (
	enterAlternateScreenSequence = "\x1b[?1049h"
	leaveAlternateScreenSequence = "\x1b[?1049l"
	hideCursorSequence           = "\x1b[?25l"
	showCursorSequence           = "\x1b[?25h"
)

// Size is a terminal's dimensions in character cells.
type Size struct {
	Width  int
	Height int
}

type windowSize struct {
	Rows    uint16
	Columns uint16
	XPixel  uint16
	YPixel  uint16
}

type terminalOperations struct {
	getAttributes func(uintptr) (syscall.Termios, error)
	setAttributes func(uintptr, syscall.Termios) error
	getWindowSize func(uintptr) (windowSize, error)
}

// Terminal manages terminal state for a file descriptor.
type Terminal struct {
	mu         sync.Mutex
	fd         uintptr
	operations terminalOperations
	original   *syscall.Termios
}

// NewTerminal creates a terminal backed by file. Interactive input normally
// uses os.Stdin.
func NewTerminal(file *os.File) *Terminal {
	return &Terminal{
		fd:         file.Fd(),
		operations: systemTerminalOperations(),
	}
}

// NewOutputTerminal creates a terminal whose size is read from output's file
// descriptor. It does not alter output or enter raw mode.
func NewOutputTerminal(output *os.File) *Terminal {
	return NewTerminal(output)
}

// EnterRaw saves the current terminal attributes and switches to raw input.
// Repeated calls before Restore are no-ops.
func (t *Terminal) EnterRaw() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.original != nil {
		return nil
	}
	attributes, err := t.operations.getAttributes(t.fd)
	if err != nil {
		return err
	}
	raw := attributes
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	// Preserve output post-processing. Dashboard frames contain bare newlines,
	// so OPOST/ONLCR must remain intact to return each row to column zero.
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := t.operations.setAttributes(t.fd, raw); err != nil {
		return err
	}
	original := attributes
	t.original = &original
	return nil
}

// Restore restores the attributes saved by EnterRaw. It is safe to call more
// than once. A failed restore remains retryable.
func (t *Terminal) Restore() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.original == nil {
		return nil
	}
	if err := t.operations.setAttributes(t.fd, *t.original); err != nil {
		return err
	}
	t.original = nil
	return nil
}

// Size returns the terminal's current dimensions.
func (t *Terminal) Size() (Size, error) {
	value, err := t.operations.getWindowSize(t.fd)
	if err != nil {
		return Size{}, err
	}
	return Size{Width: int(value.Columns), Height: int(value.Rows)}, nil
}

// ResizeEvents reports SIGWINCH notifications until ctx is canceled. The
// buffered channel coalesces resize bursts.
func (t *Terminal) ResizeEvents(ctx context.Context) <-chan struct{} {
	signals := make(chan os.Signal, 1)
	changes := make(chan struct{}, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	go func() {
		defer close(changes)
		defer signal.Stop(signals)
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				select {
				case changes <- struct{}{}:
				default:
				}
			}
		}
	}()
	return changes
}

// EnterAlternateScreen switches the terminal to its alternate screen buffer.
func EnterAlternateScreen(w io.Writer) error {
	return writeSequence(w, enterAlternateScreenSequence)
}

// LeaveAlternateScreen switches the terminal back to its main screen buffer.
func LeaveAlternateScreen(w io.Writer) error {
	return writeSequence(w, leaveAlternateScreenSequence)
}

// HideCursor hides the terminal cursor.
func HideCursor(w io.Writer) error {
	return writeSequence(w, hideCursorSequence)
}

// ShowCursor shows the terminal cursor.
func ShowCursor(w io.Writer) error {
	return writeSequence(w, showCursorSequence)
}

func writeSequence(w io.Writer, sequence string) error {
	n, err := io.WriteString(w, sequence)
	if err != nil {
		return err
	}
	if n != len(sequence) {
		return io.ErrShortWrite
	}
	return nil
}

// RestoreOnExit is intended to be installed as the outermost defer:
//
//	defer term.RestoreOnExit(terminal, os.Stdout)
//
// It attempts every visual and terminal-state restoration even if one fails,
// then resumes any panic so callers retain the original failure.
func RestoreOnExit(terminal *Terminal, output io.Writer) {
	panicValue := recover()
	_ = ShowCursor(output)
	_ = LeaveAlternateScreen(output)
	_ = restoreTerminal(terminal)
	if panicValue != nil {
		panic(panicValue)
	}
}

func restoreTerminal(terminal *Terminal) error {
	if terminal == nil {
		return nil
	}
	return terminal.Restore()
}
