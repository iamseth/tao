package term

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestRegionSequences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		write func(io.Writer) error
		want  string
	}{
		{name: "set scroll region", write: func(w io.Writer) error { return SetScrollRegion(w, 4, 24) }, want: "\x1b[4;24r"},
		{name: "reset scroll region", write: ResetScrollRegion, want: "\x1b[r"},
		{name: "save cursor", write: SaveCursor, want: "\x1b[s"},
		{name: "restore cursor", write: RestoreCursor, want: "\x1b[u"},
		{name: "position cursor", write: func(w io.Writer) error { return PositionCursor(w, 3, 17) }, want: "\x1b[3;17H"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := test.write(&output); err != nil {
				t.Fatalf("write sequence: %v", err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("sequence = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResetScrollRegionCanBeRepeated(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := ResetScrollRegion(&output); err != nil {
		t.Fatalf("first ResetScrollRegion() error = %v", err)
	}
	if err := ResetScrollRegion(&output); err != nil {
		t.Fatalf("second ResetScrollRegion() error = %v", err)
	}
	if got, want := output.String(), "\x1b[r\x1b[r"; got != want {
		t.Fatalf("repeated reset sequence = %q, want %q", got, want)
	}
}

func TestOutputTerminalSizeUsesOutputDescriptor(t *testing.T) {
	t.Parallel()

	reader, output, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = output.Close() }()

	terminal := NewOutputTerminal(output)
	terminal.operations.getWindowSize = func(fd uintptr) (windowSize, error) {
		if fd != output.Fd() {
			t.Fatalf("size fd = %d, want output fd %d", fd, output.Fd())
		}
		return windowSize{Rows: 35, Columns: 120}, nil
	}

	got, err := terminal.Size()
	if err != nil {
		t.Fatalf("Size() error = %v", err)
	}
	if want := (Size{Width: 120, Height: 35}); got != want {
		t.Fatalf("Size() = %#v, want %#v", got, want)
	}
}
