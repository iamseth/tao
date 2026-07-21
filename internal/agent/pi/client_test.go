package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestClientRunsFreshSessionAndCollectsStateAndStats(t *testing.T) {
	ctx := context.Background()
	proc := newFakeProcess(t)
	var gotCwd string
	var gotName string
	var gotArgs []string
	serverErr := make(chan error, 1)
	go func() {
		defer proc.finish()
		cmd, err := proc.readCommand()
		if err != nil {
			serverErr <- err
			return
		}
		if cmd["type"] != "prompt" || cmd["message"] != "review this" {
			serverErr <- errors.New("prompt command was not sent correctly")
			return
		}
		proc.writeEvent(`{"type":"message","role":"assistant","text":"{\"result\":true}"}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","name":"bash","arguments":{"command":"go test ./..."}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"ok ./..."}],"isError":false}}`)
		proc.writeEvent(`{"type":"extension_ui_request","request_id":"dialog-1","kind":"dialog"}`)
		cmd, err = proc.readCommand()
		if err != nil {
			serverErr <- err
			return
		}
		if cmd["type"] != "extension_ui_response" || cmd["request_id"] != "dialog-1" || cmd["cancelled"] != true {
			serverErr <- errors.New("ui request was not cancelled")
			return
		}
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
		cmd, err = proc.readCommand()
		if err != nil {
			serverErr <- err
			return
		}
		if cmd["type"] != "get_state" || cmd["session_id"] != "session-1" {
			serverErr <- errors.New("state command was not sent for completed session")
			return
		}
		proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":true,"data":{"sessionId":"session-1","cwd":"/repo","model":{"provider":"pi-provider","id":"pi-model"}}}`)
		cmd, err = proc.readCommand()
		if err != nil {
			serverErr <- err
			return
		}
		if cmd["type"] != "get_session_stats" || cmd["session_id"] != "session-1" {
			serverErr <- errors.New("stats command was not sent for completed session")
			return
		}
		proc.writeEvent(`{"id":"3","type":"response","command":"get_session_stats","success":true,"data":{"sessionId":"session-1","tokens":{"total":12}}}`)
		serverErr <- nil
	}()

	var log bytes.Buffer
	client := Client{
		Log: &log,
		ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
			gotCwd = cwd
			gotName = name
			gotArgs = append([]string(nil), args...)
			return proc, nil
		},
	}

	result, err := client.RunAgentSession(ctx, Request{RepoRoot: "/repo", Prompt: "review this", SessionInfoMode: SessionInfoBestEffort})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if gotCwd != "/repo" || gotName != "pi" || strings.Join(gotArgs, " ") != "--mode rpc" {
		t.Fatalf("unexpected process start: cwd=%q name=%q args=%#v", gotCwd, gotName, gotArgs)
	}
	if result.FinalText != `{"result":true}` || result.Output != result.FinalText {
		t.Fatalf("unexpected final text: %#v", result)
	}
	if result.SessionID != "session-1" {
		t.Fatalf("expected session id, got %q", result.SessionID)
	}
	if result.SessionInfoError != nil {
		t.Fatalf("unexpected session-info error: %v", result.SessionInfoError)
	}
	if result.Stats["sessionId"] != "session-1" {
		t.Fatalf("expected response data stats, got %#v", result.Stats)
	}
	statsTokens, _ := result.Stats["tokens"].(map[string]any)
	if statsTokens["total"] != float64(12) {
		t.Fatalf("expected stats, got %#v", result.Stats)
	}
	for _, want := range []string{"→ bash {\"command\":\"go test ./...\"}", "✓ bash\nok ./...", "cancelled unsupported UI request \"dialog-1\""} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("expected %q in log, got %q", want, log.String())
		}
	}
}

func TestClientSessionInfoNoneSkipsStateAndStats(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		_ = proc.stdinReader.Close()
		proc.writeEvent(`{"type":"message","role":"assistant","text":"done"}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{Prompt: "work", SessionInfoMode: SessionInfoNone})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "done" || result.SessionID != "session-1" || result.SessionInfoError != nil {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestClientSessionInfoBestEffortDoesNotFailCompletedSession(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		proc.writeEvent(`{"type":"message","role":"assistant","text":"done"}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
		_, _ = proc.readCommand()
		proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":false,"error":"state unavailable"}`)
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{Prompt: "work", SessionInfoMode: SessionInfoBestEffort})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "done" || result.SessionInfoError == nil || !strings.Contains(result.SessionInfoError.Error(), "pi rpc get_state: state unavailable") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestClientDeduplicatesAnonymousStreamingToolLogsAndWatchdog(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		proc.writeEvent(`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"bash","arguments":{"command":"git"}}]}}`)
		proc.writeEvent(`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"bash","arguments":{"command":"git rev"}}]}}`)
		proc.writeEvent(`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"bash","arguments":{"command":"git rev-parse --abbrev-ref HEAD"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"master"}],"isError":false}}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	if _, err := client.RunAgentSession(context.Background(), Request{Prompt: "work", NoProgressToolLimit: 2}); err != nil {
		t.Fatal(err)
	}
	text := log.String()
	if got := strings.Count(text, "→ bash"); got != 1 {
		t.Fatalf("expected one logged bash call, got %d:\n%s", got, text)
	}
	if !strings.Contains(text, `→ bash {"command":"git rev-parse --abbrev-ref HEAD"}`) || strings.Contains(text, `→ bash {"command":"git"}
`) {
		t.Fatalf("expected only the final anonymous streamed tool arguments in log:\n%s", text)
	}
}

func TestClientDeduplicatesStreamingToolLogs(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-1","name":"bash","arguments":{"command":"git"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-1","name":"bash","arguments":{"command":"git rev"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-1","name":"bash","arguments":{"command":"git rev-parse --abbrev-ref HEAD"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-1","toolName":"bash","content":[{"type":"text","text":"master"}],"isError":false}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-1","toolName":"bash","content":[{"type":"text","text":"master"}],"isError":false}}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	if _, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"}); err != nil {
		t.Fatal(err)
	}
	text := log.String()
	if got := strings.Count(text, "→ bash"); got != 1 {
		t.Fatalf("expected one logged bash call, got %d:\n%s", got, text)
	}
	if got := strings.Count(text, "✓ bash"); got != 1 {
		t.Fatalf("expected one logged bash result, got %d:\n%s", got, text)
	}
	if !strings.Contains(text, `→ bash {"command":"git rev-parse --abbrev-ref HEAD"}`) || strings.Contains(text, `→ bash {"command":"git"}
`) {
		t.Fatalf("expected only the final streamed tool arguments in log:\n%s", text)
	}
}

func TestClientAgentErrorIsReturnedAndLogged(t *testing.T) {
	proc := newFakeProcess(t)
	serverErr := make(chan error, 1)
	go func() {
		defer proc.finish()
		if _, err := proc.readCommand(); err != nil {
			serverErr <- err
			return
		}
		proc.writeEvent(`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"WebSocket closed 1006 Connection ended","diagnostics":[{"type":"provider_transport_failure","error":{"message":"WebSocket closed 1006 Connection ended"}}]}}`)
		cmd, err := proc.readCommand()
		if err != nil {
			serverErr <- err
			return
		}
		if cmd["type"] != "abort" {
			serverErr <- fmt.Errorf("cleanup command type = %v, want abort", cmd["type"])
			return
		}
		serverErr <- nil
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err == nil || !strings.Contains(err.Error(), "WebSocket closed 1006") || !strings.Contains(err.Error(), "provider_transport_failure") {
		t.Fatalf("expected provider transport error, got %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if !proc.killed {
		t.Fatal("expected failed agent session to kill the Pi RPC process")
	}
	if !strings.Contains(log.String(), "tao pi error: pi agent error") || !strings.Contains(log.String(), "WebSocket closed 1006") {
		t.Fatalf("expected pi error in log, got %q", log.String())
	}
	if !strings.Contains(log.String(), "agent session ended with an error; stopping the RPC process") {
		t.Fatalf("expected cleanup progress in log, got %q", log.String())
	}
}

func TestClientNoProgressWatchdogAbortsRunSessions(t *testing.T) {
	proc := newFakeProcess(t)
	serverErr := make(chan error, 1)
	go func() {
		defer proc.finish()
		if _, err := proc.readCommand(); err != nil {
			serverErr <- err
			return
		}
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-1","name":"read","arguments":{"path":"internal/one.go"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-1","toolName":"read","content":[{"type":"text","text":"one"}],"isError":false}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-2","name":"bash","arguments":{"command":"rg something internal"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-2","toolName":"bash","content":[{"type":"text","text":"two"}],"isError":false}}`)
		cmd, err := proc.readCommand()
		if err != nil {
			serverErr <- err
			return
		}
		if cmd["type"] != "abort" {
			serverErr <- errors.New("abort command was not sent")
			return
		}
		serverErr <- nil
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work", NoProgressToolLimit: 2})
	if err == nil || !strings.Contains(err.Error(), "no-progress watchdog") {
		t.Fatalf("expected watchdog error, got %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if !proc.killed {
		t.Fatal("expected watchdog to kill process")
	}
	if !strings.Contains(log.String(), "tao pi error: pi no-progress watchdog") {
		t.Fatalf("expected watchdog error in log, got %q", log.String())
	}
}

func TestPiClientExtractsFinalAssistantTextFromMessageEndContent(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		proc.writeEvent(`{"type":"message","role":"assistant","text":"thinking"}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"text","text":" second"}]}}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "first second" || result.Output != "thinking\nfirst second" {
		t.Fatalf("unexpected assistant text: %#v", result)
	}
}

func TestClientErrorResponseReturnsClearError(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		proc.writeEvent(`{"id":"1","type":"response","command":"prompt","success":false,"error":"Unknown command: undefined"}`)
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err == nil || !strings.Contains(err.Error(), "pi rpc prompt: Unknown command: undefined") {
		t.Fatalf("expected pi rpc response error, got %v", err)
	}
}

func TestClientMalformedJSONLReturnsClearError(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		_, _ = io.WriteString(proc.stdoutWriter, "not-json\n")
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err == nil || !strings.Contains(err.Error(), "parse pi rpc jsonl line 1") {
		t.Fatalf("expected malformed jsonl error, got %v", err)
	}
}

func TestClientContextCancellationSendsAbortAndCleansProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	proc := newFakeProcess(t)
	serverErr := make(chan error, 1)
	go func() {
		defer proc.finish()
		if _, err := proc.readCommand(); err != nil {
			serverErr <- err
			return
		}
		cancel()
		cmd, err := proc.readCommand()
		if err != nil {
			serverErr <- err
			return
		}
		if cmd["type"] != "abort" {
			serverErr <- errors.New("abort command was not sent")
			return
		}
		serverErr <- nil
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(ctx, Request{Prompt: "work"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if !proc.killed {
		t.Fatal("expected process to be killed")
	}
}

type fakeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdinDecoder *json.Decoder
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	done         chan struct{}
	once         sync.Once
	killed       bool
}

func newFakeProcess(t *testing.T) *fakeProcess {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	return &fakeProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdinDecoder: json.NewDecoder(stdinReader), stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
}

func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *fakeProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *fakeProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (p *fakeProcess) Wait() error {
	<-p.done
	return nil
}
func (p *fakeProcess) Kill() error {
	p.killed = true
	return nil
}

func (p *fakeProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stdinReader.Close()
		close(p.done)
	})
}

func (p *fakeProcess) readCommand() (map[string]any, error) {
	var cmd map[string]any
	if err := p.stdinDecoder.Decode(&cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (p *fakeProcess) writeEvent(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}
