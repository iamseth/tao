package pi

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWarningResponseJoinsWriterBeforeTerminalCleanup(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	written := make(chan struct{})
	done := make(chan struct{})
	s := &session{
		stdin: writer, events: make(chan readResult, 2),
		warningSent: true, warningWritten: written, warningWriteDone: done,
	}
	s.events <- readResult{event: event{"type": "response", "command": "steer", "id": warningID, "success": false}}
	s.events <- readResult{event: event{"type": "agent_end"}}
	// Model a delivered command whose writer has not yet returned.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(written)
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.waitForAgentEnd(ctx); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		readDone <- err
	}()
	if _, err := writer.Write([]byte("get_state\n")); err != nil {
		t.Errorf("terminal cleanup closed stdin after acknowledged warning: %v", err)
	}
	_ = writer.Close()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
}

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

func TestReadStdoutRejectsBlankLines(t *testing.T) {
	for _, blank := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(blank, "\n", "LF"), func(t *testing.T) {
			results := collectReadResults(strings.NewReader("{\"type\":\"message\"}\n" + blank))
			// Assert the ordered stream: one event, then the blank-line error.
			if len(results) != 2 || results[0].err != nil || results[0].event["type"] != "message" {
				t.Fatalf("unexpected stream: %#v", results)
			}
			want := "parse pi rpc jsonl line 2: unexpected end of JSON input"
			if results[1].err == nil || results[1].err.Error() != want {
				t.Fatalf("got %v, want %s", results[1].err, want)
			}
		})
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

func TestWarningWritePreservesLiveSessionUntilCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	s := &session{stdin: writer}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.startWarning(ctx, "wrap up")
	select {
	case <-s.warningWriteDone:
		t.Fatal("warning closed stdin while session was live")
	case <-time.After(250 * time.Millisecond):
	}
	cancel()
	select {
	case <-s.warningWriteDone:
	case <-time.After(time.Second):
		_ = writer.Close()
		t.Fatal("warning work was not bounded")
	}
	if _, err := writer.Write([]byte("another command")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("stalled pipe remained writable: %v", err)
	}
}

func TestWarningDoesNotOverrideBufferedTerminal(t *testing.T) {
	warnings := make(chan string, 1)
	warnings <- "too late"
	s := &session{warningMessages: warnings, events: make(chan readResult, 1)}
	s.events <- readResult{event: event{"type": "agent_end"}}
	if _, err := s.waitForAgentEnd(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.warningSent || len(warnings) != 1 {
		t.Fatal("warning sent after terminal event")
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
