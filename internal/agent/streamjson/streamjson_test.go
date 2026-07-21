package streamjson

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/iamseth/tao/internal/agent/process"
)

// fakeProcess is an in-memory process.Process: the test drives stdout via a pipe
// and signals exit through finish.
type fakeProcess struct {
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdinBuffer  bytes.Buffer
	stdinDone    chan struct{}
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	done         chan struct{}
	exitErr      error
	mu           sync.Mutex
	killed       bool
}

func newFakeProcess() *fakeProcess {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	proc := &fakeProcess{
		stdinReader:  stdinReader,
		stdinWriter:  stdinWriter,
		stdinDone:    make(chan struct{}),
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		done:         make(chan struct{}),
	}
	// Drain stdin like a real agent process would, so SendPrompt's write never
	// blocks; it ends when the runner closes stdin.
	go func() {
		_, _ = io.Copy(&proc.stdinBuffer, stdinReader)
		close(proc.stdinDone)
	}()
	return proc
}

func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *fakeProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *fakeProcess) Stderr() io.Reader     { return nil }

func (p *fakeProcess) Wait() error {
	<-p.done
	return p.exitErr
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.finish(nil)
	return nil
}

func (p *fakeProcess) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

func (p *fakeProcess) prompt() string {
	<-p.stdinDone
	return p.stdinBuffer.String()
}

// finish closes stdout and releases Wait exactly once.
func (p *fakeProcess) finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return
	default:
	}
	p.exitErr = err
	_ = p.stdoutWriter.Close()
	close(p.done)
}

func (p *fakeProcess) writeLine(line string) {
	_, _ = io.WriteString(p.stdoutWriter, line+"\n")
}

// collect appends each event's "text" field into result.Output.
func collect(ev Event, result *struct{ Output string }) error {
	if text, ok := ev["text"].(string); ok {
		result.Output += text
	}
	return nil
}

func TestRunSessionStartsProcessSendsPromptReadsAndWaits(t *testing.T) {
	proc := newFakeProcess()
	var gotCwd, gotName string
	var gotArgs []string
	starter := func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		gotCwd = cwd
		gotName = name
		gotArgs = append([]string(nil), args...)
		return proc, nil
	}
	go func() {
		proc.writeLine(`{"text":"a"}`)
		proc.writeLine(`{"text":"b"}`)
		proc.finish(nil)
	}()

	result, err := RunSession(context.Background(), SessionConfig[struct{ Output string }]{
		Starter:    starter,
		RepoRoot:   "/repo",
		Executable: "agent",
		Args:       []string{"--json"},
		Prompt:     "do work",
		StreamKind: "json",
		Handle:     collect,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotCwd != "/repo" || gotName != "agent" || strings.Join(gotArgs, " ") != "--json" {
		t.Fatalf("unexpected start: cwd=%q name=%q args=%q", gotCwd, gotName, strings.Join(gotArgs, " "))
	}
	if got := proc.prompt(); got != "do work" {
		t.Fatalf("prompt = %q, want do work", got)
	}
	if result.Output != "ab" {
		t.Fatalf("output = %q, want ab", result.Output)
	}
	if proc.wasKilled() {
		t.Fatal("process was killed on successful run")
	}
}

func TestRunSessionStarterErrorIsPreRead(t *testing.T) {
	wantErr := errors.New("missing executable")
	starter := func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return nil, wantErr
	}

	_, err := RunSession(context.Background(), SessionConfig[struct{ Output string }]{
		Starter:    starter,
		Executable: "agent",
		StreamKind: "json",
		Handle:     collect,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if !IsPreReadError(err) {
		t.Fatalf("expected pre-read error, got %v", err)
	}
}

func TestRunSessionHandlerErrorAbortsAndLogs(t *testing.T) {
	proc := newFakeProcess()
	starter := func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}
	go func() {
		proc.writeLine(`{"text":"boom"}`)
	}()
	var log bytes.Buffer
	handler := func(_ Event, _ *struct{ Output string }) error {
		return errors.New("handler failed")
	}

	_, err := RunSession(context.Background(), SessionConfig[struct{ Output string }]{
		Starter:    starter,
		Executable: "agent",
		Prompt:     "do work",
		StreamKind: "json",
		Log:        &log,
		Handle:     handler,
	})
	if err == nil || !strings.Contains(err.Error(), "handler failed") {
		t.Fatalf("expected handler error, got %v", err)
	}
	if !strings.Contains(log.String(), "tao agent error: handler failed") {
		t.Fatalf("expected logged error, got %q", log.String())
	}
	if !proc.wasKilled() {
		t.Fatal("expected process killed on handler error")
	}
}

func TestRunnerReadsAndSkipsBlankLines(t *testing.T) {
	proc := newFakeProcess()
	go func() {
		proc.writeLine(`{"text":"a"}`)
		proc.writeLine("")
		proc.writeLine(`{"text":"b"}`)
		proc.finish(nil)
	}()

	runner := New("test", "json", proc, nil, collect)
	defer runner.Close()
	if err := runner.SendPrompt(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ab" {
		t.Fatalf("expected blank line skipped, got %q", result.Output)
	}
	if err := runner.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerReadsLargeJSONLLine(t *testing.T) {
	payload := strings.Repeat("x", 2*1024*1024)
	proc := newFakeProcess()
	go func() {
		proc.writeLine(`{"text":"` + payload + `"}`)
		proc.finish(nil)
	}()

	runner := New("test", "json", proc, nil, collect)
	defer runner.Close()
	result, err := runner.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != payload {
		t.Fatalf("expected payload length %d, got %d", len(payload), len(result.Output))
	}
}

func TestRunnerDeliversMultiEventStreamInOrder(t *testing.T) {
	proc := newFakeProcess()
	go func() {
		proc.writeLine(`{"text":"first"}`)
		proc.writeLine(`{"text":"second"}`)
		proc.writeLine(`{"text":"third"}`)
		proc.finish(nil)
	}()

	handler := func(ev Event, result *[]string) error {
		if text, ok := ev["text"].(string); ok {
			*result = append(*result, text)
		}
		return nil
	}
	runner := New("test", "json", proc, nil, handler)
	defer runner.Close()
	result, err := runner.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result, ",") != "first,second,third" {
		t.Fatalf("events delivered out of order: %#v", result)
	}
}

func TestRunnerReadsUnterminatedFinalLine(t *testing.T) {
	proc := newFakeProcess()
	go func() {
		_, _ = io.WriteString(proc.stdoutWriter, `{"text":"final"}`)
		proc.finish(nil)
	}()

	runner := New("test", "json", proc, nil, collect)
	defer runner.Close()
	result, err := runner.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "final" {
		t.Fatalf("expected unterminated final event, got %q", result.Output)
	}
}

func TestRunnerReportsMidstreamReaderError(t *testing.T) {
	boom := errors.New("boom")
	proc := newFakeProcess()
	go func() {
		proc.writeLine(`{"text":"before"}`)
		_ = proc.stdoutWriter.CloseWithError(boom)
		proc.finish(nil)
	}()

	runner := New("test", "json", proc, nil, collect)
	defer runner.Close()
	result, err := runner.Read(context.Background())
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "read test stdout") {
		t.Fatalf("expected loud wrapped read error, got %v", err)
	}
	if result.Output != "before" {
		t.Fatalf("expected event before read error to be consumed, got %q", result.Output)
	}
	if !proc.wasKilled() {
		t.Fatal("expected process killed on read error")
	}
}

func TestRunnerMalformedJSONUsesNameAndStreamKindAndKills(t *testing.T) {
	proc := newFakeProcess()
	go func() {
		proc.writeLine("not-json")
		proc.finish(nil)
	}()

	runner := New("opencode", "json", proc, nil, collect)
	defer runner.Close()
	_, err := runner.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "parse opencode json line 1") {
		t.Fatalf("expected parse error labelled with name/streamKind, got %v", err)
	}
	if !proc.wasKilled() {
		t.Fatal("expected process killed on parse error")
	}
}

func TestRunnerWaitReportsExitError(t *testing.T) {
	proc := newFakeProcess()
	go func() {
		proc.writeLine(`{"text":"x"}`)
		proc.finish(errors.New("exit status 1"))
	}()

	runner := New("claude", "stream-json", proc, nil, collect)
	defer runner.Close()
	if _, err := runner.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := runner.Wait(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude exited: exit status 1") {
		t.Fatalf("expected exit error labelled with name, got %v", err)
	}
}

func TestRunnerHandlerErrorAbortsAndLogs(t *testing.T) {
	proc := newFakeProcess()
	go func() {
		proc.writeLine(`{"text":"boom"}`)
		proc.finish(nil)
	}()

	var log bytes.Buffer
	handler := func(ev Event, _ *struct{ Output string }) error {
		return errors.New("handler failed")
	}
	runner := New("claude", "stream-json", proc, &log, handler)
	defer runner.Close()
	_, err := runner.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "handler failed") {
		t.Fatalf("expected handler error surfaced, got %v", err)
	}
	if !strings.Contains(log.String(), "tao claude error: handler failed") {
		t.Fatalf("expected logged error, got %q", log.String())
	}
	if !proc.wasKilled() {
		t.Fatal("expected process killed on handler error")
	}
}

func TestRunnerContextCancellationAborts(t *testing.T) {
	proc := newFakeProcess()
	defer proc.finish(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := New("test", "json", proc, nil, collect)
	defer runner.Close()
	_, err := runner.Read(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if !proc.wasKilled() {
		t.Fatal("expected process killed on cancellation")
	}
}
