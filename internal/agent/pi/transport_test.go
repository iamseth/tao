package pi

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSendPromptDistinguishesNoAttemptFromPartialWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &session{proc: inertProcess{}, stdin: partialWriteCloser{err: errors.New("partial")}}
	attempted, err := s.sendPrompt(ctx, command{Type: "prompt", Message: "work"})
	if !errors.Is(err, context.Canceled) || attempted {
		t.Fatalf("cancelled send = attempted %t, error %v", attempted, err)
	}

	wantErr := errors.New("partial")
	s = &session{stdin: partialWriteCloser{err: wantErr}}
	attempted, err = s.sendPrompt(context.Background(), command{Type: "prompt", Message: "work"})
	if !errors.Is(err, wantErr) || !attempted {
		t.Fatalf("partial send = attempted %t, error %v", attempted, err)
	}
}

type partialWriteCloser struct{ err error }

func (w partialWriteCloser) Write(data []byte) (int, error) { return len(data) / 2, w.err }
func (partialWriteCloser) Close() error                     { return nil }

type inertProcess struct{}

func (inertProcess) Stdin() io.WriteCloser { return partialWriteCloser{} }
func (inertProcess) Stdout() io.Reader     { return strings.NewReader("") }
func (inertProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (inertProcess) Wait() error           { return nil }
func (inertProcess) Kill() error           { return nil }

func TestReadStdoutAcceptsLargeJSONLLine(t *testing.T) {
	payload := strings.Repeat("x", 2*1024*1024)
	results := collectReadResults(strings.NewReader(`{"type":"message","text":"` + payload + `"}` + "\n"))

	if len(results) != 1 {
		t.Fatalf("expected 1 event, got %d", len(results))
	}
	if results[0].err != nil {
		t.Fatalf("unexpected read error: %v", results[0].err)
	}
	if got := results[0].event["text"]; got != payload {
		t.Fatalf("expected payload length %d, got %#v", len(payload), got)
	}
}

func TestReadStdoutDeliversMultiEventStreamInOrder(t *testing.T) {
	results := collectReadResults(strings.NewReader(strings.Join([]string{
		`{"type":"message","text":"first"}`,
		`{"type":"message","text":"second"}`,
		`{"type":"agent_end","session_id":"session-1"}`,
	}, "\n") + "\n"))

	if len(results) != 3 {
		t.Fatalf("expected 3 events, got %d", len(results))
	}
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("event %d had unexpected error: %v", i, result.err)
		}
	}
	if got := results[0].event["text"]; got != "first" {
		t.Fatalf("expected first event text, got %#v", got)
	}
	if got := results[1].event["text"]; got != "second" {
		t.Fatalf("expected second event text, got %#v", got)
	}
	if got := results[2].event["session_id"]; got != "session-1" {
		t.Fatalf("expected final session id, got %#v", got)
	}
}

func TestReadStdoutAcceptsUnterminatedFinalLine(t *testing.T) {
	results := collectReadResults(strings.NewReader(`{"type":"agent_end","session_id":"session-1"}`))

	if len(results) != 1 {
		t.Fatalf("expected 1 event, got %d", len(results))
	}
	if results[0].err != nil {
		t.Fatalf("unexpected read error: %v", results[0].err)
	}
	if got := results[0].event["session_id"]; got != "session-1" {
		t.Fatalf("expected final session id, got %#v", got)
	}
}

func TestReadStdoutReportsMidstreamReaderError(t *testing.T) {
	boom := errors.New("boom")
	results := collectReadResults(io.MultiReader(
		strings.NewReader(`{"type":"message","text":"before"}`+"\n"),
		errorReader{err: boom},
	))

	if len(results) != 2 {
		t.Fatalf("expected event plus read error, got %d results", len(results))
	}
	if results[0].err != nil {
		t.Fatalf("unexpected first result error: %v", results[0].err)
	}
	if got := results[0].event["text"]; got != "before" {
		t.Fatalf("expected first event before error, got %#v", got)
	}
	if !errors.Is(results[1].err, boom) || !strings.Contains(results[1].err.Error(), "read pi rpc stdout") {
		t.Fatalf("expected loud wrapped read error, got %v", results[1].err)
	}
}

func collectReadResults(stdout io.Reader) []readResult {
	s := &session{events: make(chan readResult)}
	go s.readStdout(stdout)
	var results []readResult
	for result := range s.events {
		results = append(results, result)
	}
	return results
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
