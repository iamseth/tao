package term

import (
	"bytes"
	"context"
	"errors"
	"io"
	"syscall"
	"testing"
	"time"
)

func TestANSIHelpers(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	for _, write := range []func(io.Writer) error{
		EnterAlternateScreen,
		HideCursor,
		ShowCursor,
		LeaveAlternateScreen,
	} {
		if err := write(&output); err != nil {
			t.Fatalf("ANSI helper error = %v", err)
		}
	}
	want := "\x1b[?1049h\x1b[?25l\x1b[?25h\x1b[?1049l"
	if got := output.String(); got != want {
		t.Fatalf("ANSI output = %q, want %q", got, want)
	}
}

func TestANSIHelperPropagatesWriteError(t *testing.T) {
	t.Parallel()

	want := errors.New("write failed")
	if err := HideCursor(errorWriter{err: want}); !errors.Is(err, want) {
		t.Fatalf("HideCursor() error = %v, want %v", err, want)
	}
}

func TestTerminalEnterRawAndRestore(t *testing.T) {
	t.Parallel()

	original := syscall.Termios{
		Iflag: syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON,
		Oflag: syscall.OPOST | syscall.ONLCR,
		Cflag: syscall.CSIZE | syscall.PARENB,
		Lflag: syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN,
	}
	var set []syscall.Termios
	terminal := &Terminal{
		fd: 42,
		operations: terminalOperations{
			getAttributes: func(fd uintptr) (syscall.Termios, error) {
				if fd != 42 {
					t.Fatalf("get attributes fd = %d, want 42", fd)
				}
				return original, nil
			},
			setAttributes: func(fd uintptr, value syscall.Termios) error {
				if fd != 42 {
					t.Fatalf("set attributes fd = %d, want 42", fd)
				}
				set = append(set, value)
				return nil
			},
		},
	}

	if err := terminal.EnterRaw(); err != nil {
		t.Fatalf("EnterRaw() error = %v", err)
	}
	if err := terminal.EnterRaw(); err != nil {
		t.Fatalf("second EnterRaw() error = %v", err)
	}
	if len(set) != 1 {
		t.Fatalf("raw attribute writes = %d, want 1", len(set))
	}
	raw := set[0]
	if raw.Iflag != 0 || raw.Oflag != original.Oflag {
		t.Errorf("raw input/output flags = %#x/%#x, want 0/%#x", raw.Iflag, raw.Oflag, original.Oflag)
	}
	if raw.Cflag&syscall.CSIZE != syscall.CS8 || raw.Cflag&syscall.PARENB != 0 {
		t.Errorf("raw control flags = %#x, want CS8 without parity", raw.Cflag)
	}
	if raw.Lflag != 0 {
		t.Errorf("raw local flags = %#x, want 0", raw.Lflag)
	}
	if raw.Cc[syscall.VMIN] != 1 || raw.Cc[syscall.VTIME] != 0 {
		t.Errorf("raw VMIN/VTIME = %d/%d, want 1/0", raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME])
	}

	if err := terminal.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if err := terminal.Restore(); err != nil {
		t.Fatalf("second Restore() error = %v", err)
	}
	if len(set) != 2 {
		t.Fatalf("total attribute writes = %d, want 2", len(set))
	}
	if set[1] != original {
		t.Fatalf("restored attributes = %#v, want %#v", set[1], original)
	}
}

func TestTerminalSize(t *testing.T) {
	t.Parallel()

	terminal := &Terminal{
		fd: 7,
		operations: terminalOperations{
			getWindowSize: func(fd uintptr) (windowSize, error) {
				if fd != 7 {
					t.Fatalf("size fd = %d, want 7", fd)
				}
				return windowSize{Rows: 24, Columns: 80}, nil
			},
		},
	}
	got, err := terminal.Size()
	if err != nil {
		t.Fatalf("Size() error = %v", err)
	}
	if want := (Size{Width: 80, Height: 24}); got != want {
		t.Fatalf("Size() = %#v, want %#v", got, want)
	}
}

func TestResizeEventsCloseWhenContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	changes := (&Terminal{}).ResizeEvents(ctx)
	cancel()
	select {
	case _, ok := <-changes:
		if ok {
			t.Fatal("ResizeEvents() sent a change after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("ResizeEvents() did not close after cancellation")
	}
}

func TestRestoreOnExitRestoresAndResumesPanic(t *testing.T) {
	t.Parallel()

	original := syscall.Termios{Iflag: syscall.ICRNL}
	var restored syscall.Termios
	terminal := &Terminal{
		original: &original,
		operations: terminalOperations{
			setAttributes: func(_ uintptr, value syscall.Termios) error {
				restored = value
				return nil
			},
		},
	}
	var output bytes.Buffer
	panicValue := errors.New("boom")
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		defer RestoreOnExit(terminal, &output)
		panic(panicValue)
	}()

	recoveredError, ok := recovered.(error)
	if !ok || !errors.Is(recoveredError, panicValue) {
		t.Fatalf("recovered panic = %v, want %v", recovered, panicValue)
	}
	if restored != original {
		t.Fatalf("restored attributes = %#v, want %#v", restored, original)
	}
	if want := "\x1b[?25h\x1b[?1049l"; output.String() != want {
		t.Fatalf("restore output = %q, want %q", output.String(), want)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}
