package term

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDecoderReadsKeyEvents(t *testing.T) {
	t.Parallel()

	input := "a界\r\n\t\x08\x7f\x1b[A\x1b[B\x1b[C\x1b[D\x03\x1b"
	decoder := NewDecoder(strings.NewReader(input))
	want := []KeyEvent{
		{Key: KeyRune, Rune: 'a'},
		{Key: KeyRune, Rune: '界'},
		{Key: KeyEnter},
		{Key: KeyEnter},
		{Key: KeyTab},
		{Key: KeyBackspace},
		{Key: KeyBackspace},
		{Key: KeyArrowUp},
		{Key: KeyArrowDown},
		{Key: KeyArrowRight},
		{Key: KeyArrowLeft},
		{Key: KeyCtrlC},
		{Key: KeyEsc},
	}

	for index, expected := range want {
		got, err := decoder.ReadKey()
		if err != nil {
			t.Fatalf("ReadKey() event %d error = %v", index, err)
		}
		if got != expected {
			t.Errorf("ReadKey() event %d = %#v, want %#v", index, got, expected)
		}
	}
	if _, err := decoder.ReadKey(); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadKey() after input error = %v, want EOF", err)
	}
}

func TestDecoderReadsSplitUTF8Rune(t *testing.T) {
	t.Parallel()

	decoder := NewDecoder(&oneByteReader{input: []byte("界")})
	got, err := decoder.ReadKey()
	if err != nil {
		t.Fatalf("ReadKey() error = %v", err)
	}
	if want := (KeyEvent{Key: KeyRune, Rune: '界'}); got != want {
		t.Fatalf("ReadKey() = %#v, want %#v", got, want)
	}
}

func TestDecoderReadsArrowSequencesOneByteAtATime(t *testing.T) {
	t.Parallel()

	decoder := NewDecoder(&oneByteReader{input: []byte("\x1b[A\x1b[B\x1b[C\x1b[D")})
	want := []Key{KeyArrowUp, KeyArrowDown, KeyArrowRight, KeyArrowLeft}
	for index, expected := range want {
		got, err := decoder.ReadKey()
		if err != nil {
			t.Fatalf("ReadKey() event %d error = %v", index, err)
		}
		if got.Key != expected {
			t.Errorf("ReadKey() event %d = %#v, want key %v", index, got, expected)
		}
	}
}

func TestDecoderWaitsBrieflyForStandaloneEscapeSequence(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	if _, err := writer.Write([]byte{'\x1b'}); err != nil {
		t.Fatalf("write escape: %v", err)
	}

	type readResult struct {
		event KeyEvent
		err   error
	}
	results := make(chan readResult, 1)
	started := time.Now()
	go func() {
		event, readErr := NewDecoder(reader).ReadKey()
		results <- readResult{event: event, err: readErr}
	}()

	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("ReadKey() error = %v", result.err)
		}
		if want := (KeyEvent{Key: KeyEsc}); result.event != want {
			t.Fatalf("ReadKey() = %#v, want %#v", result.event, want)
		}
		if elapsed := time.Since(started); elapsed < escapeSequenceTimeout/2 {
			t.Fatalf("ReadKey() returned standalone escape after %v, want a bounded sequence wait", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadKey() did not return standalone escape after sequence timeout")
	}
}

func TestDecoderPreservesUnknownEscapeSequenceInput(t *testing.T) {
	t.Parallel()

	decoder := NewDecoder(strings.NewReader("\x1b[Z"))
	want := []KeyEvent{
		{Key: KeyEsc},
		{Key: KeyRune, Rune: '['},
		{Key: KeyRune, Rune: 'Z'},
	}
	for index, expected := range want {
		got, err := decoder.ReadKey()
		if err != nil {
			t.Fatalf("ReadKey() event %d error = %v", index, err)
		}
		if got != expected {
			t.Errorf("ReadKey() event %d = %#v, want %#v", index, got, expected)
		}
	}
}

func TestDecoderReturnsUnknownForUnsupportedControl(t *testing.T) {
	t.Parallel()

	got, err := NewDecoder(strings.NewReader("\x01")).ReadKey()
	if err != nil {
		t.Fatalf("ReadKey() error = %v", err)
	}
	if want := (KeyEvent{Key: KeyUnknown}); got != want {
		t.Fatalf("ReadKey() = %#v, want %#v", got, want)
	}
}

type oneByteReader struct {
	input []byte
}

func (r *oneByteReader) Read(destination []byte) (int, error) {
	if len(r.input) == 0 {
		return 0, io.EOF
	}
	destination[0] = r.input[0]
	r.input = r.input[1:]
	return 1, nil
}
