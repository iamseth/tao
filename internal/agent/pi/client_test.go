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

	"github.com/iamseth/tao/internal/agent/logrecord"
)

func TestClientRunsFreshSessionAndCollectsStateAndStats(t *testing.T) {
	ctx := context.Background()
	proc := newFakeProcess(t)
	proc.stderr = strings.NewReader("provider warning\n")
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

	var log lockedBuffer
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
	for _, want := range []string{`"type":"tool_call","name":"bash","payload":"{\"command\":\"go test ./...\"}"`, `"type":"tool_result","name":"bash","content":"ok ./..."`, `cancelled unsupported UI request \"dialog-1\"`, `tao pi stderr: provider warning`} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("expected %q in log, got %q", want, log.String())
		}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(log.String()), "\n") {
		if _, ok := logrecord.Parse(line); !ok {
			t.Fatalf("expected framed log record, got %q", line)
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
	if got := strings.Count(text, `"type":"tool_call","name":"bash"`); got != 1 {
		t.Fatalf("expected one logged bash call, got %d:\n%s", got, text)
	}
	if !strings.Contains(text, `git rev-parse --abbrev-ref HEAD`) || strings.Contains(text, `\"command\":\"git\"`) {
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
	if got := strings.Count(text, `"type":"tool_call","name":"bash"`); got != 1 {
		t.Fatalf("expected one logged bash call, got %d:\n%s", got, text)
	}
	if got := strings.Count(text, `"type":"tool_result","name":"bash"`); got != 1 {
		t.Fatalf("expected one logged bash result, got %d:\n%s", got, text)
	}
	if !strings.Contains(text, `git rev-parse --abbrev-ref HEAD`) || strings.Contains(text, `\"command\":\"git\"`) {
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
	var marker interface{ RetryableTransportFailure() }
	if !errors.As(err, &marker) {
		t.Fatal("expected structured provider transport error to be marked retryable")
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

func TestAgentMapErrorClassifiesOnlyStructuredProviderTransportFailures(t *testing.T) {
	tests := []struct {
		name        string
		values      map[string]any
		retryable   bool
		wantMessage string
	}{
		{
			name: "structured transport failure",
			values: map[string]any{
				"stopReason":   "error",
				"errorMessage": "WebSocket closed 1006 Connection ended",
				"diagnostics": []any{map[string]any{
					"type":  "provider_transport_failure",
					"error": map[string]any{"message": "WebSocket closed 1006 Connection ended"},
				}},
			},
			retryable:   true,
			wantMessage: "pi agent error: WebSocket closed 1006 Connection ended (provider_transport_failure: WebSocket closed 1006 Connection ended)",
		},
		{
			name: "same websocket text without diagnostic",
			values: map[string]any{
				"stopReason":   "error",
				"errorMessage": "WebSocket closed 1006 Connection ended",
			},
			wantMessage: "pi agent error: WebSocket closed 1006 Connection ended",
		},
		{
			name: "general diagnostic",
			values: map[string]any{
				"stopReason": "error",
				"diagnostics": []any{map[string]any{
					"type":    "provider_error",
					"message": "provider unavailable",
				}},
			},
			wantMessage: "pi agent error: provider_error: provider unavailable",
		},
		{
			name: "authentication diagnostic",
			values: map[string]any{
				"stopReason": "error",
				"diagnostics": []any{map[string]any{
					"type":    "authentication_failure",
					"message": "invalid API key",
				}},
			},
			wantMessage: "pi agent error: authentication_failure: invalid API key",
		},
		{
			name: "transport label only in diagnostic name",
			values: map[string]any{
				"stopReason": "error",
				"diagnostics": []any{map[string]any{
					"name":    "provider_transport_failure",
					"message": "WebSocket closed 1006 Connection ended",
				}},
			},
			wantMessage: "pi agent error: provider_transport_failure: WebSocket closed 1006 Connection ended",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := agentMapError(test.values, "message")
			if err == nil {
				t.Fatal("expected agent error")
			}
			var marker interface{ RetryableTransportFailure() }
			if got := errors.As(err, &marker); got != test.retryable {
				t.Fatalf("retryable = %t, want %t", got, test.retryable)
			}
			if err.Error() != test.wantMessage {
				t.Fatalf("error = %q, want %q", err.Error(), test.wantMessage)
			}
		})
	}
}

func TestRetryableTransportErrorPreservesErrorChain(t *testing.T) {
	original := errors.New("connection ended")
	err := fmt.Errorf("outer: %w", retryableTransportError{err: original})

	if !errors.Is(err, original) {
		t.Fatal("expected errors.Is to reach original error")
	}
	var marker interface{ RetryableTransportFailure() }
	if !errors.As(err, &marker) {
		t.Fatal("expected errors.As to find transport marker")
	}
	if err.Error() != "outer: connection ended" {
		t.Fatalf("error text = %q", err.Error())
	}
}

func TestClientNoProgressWatchdogRecognizesChainedDeclaredPnpmVerification(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readCommand()
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-1","name":"read","arguments":{"path":"package.json"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-1","toolName":"read","content":[{"type":"text","text":"{}"}],"isError":false}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-2","name":"bash","arguments":{"command":"git status --short &&  cd  packages/commonStudent  &&  pnpm test"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-2","toolName":"bash","content":[{"type":"text","text":"passed"}],"isError":false}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-3","name":"bash","arguments":{"command":"rg something packages"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-3","toolName":"bash","content":[{"type":"text","text":"result"}],"isError":false}}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
	}()
	client := Client{ProcessStarter: func(context.Context, string, string, []string) (Process, error) {
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{
		Prompt: "work", NoProgressToolLimit: 2, VerificationCommands: []string{"cd packages/commonStudent && pnpm test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "session-1" {
		t.Fatalf("session ID = %q, want session-1", result.SessionID)
	}
}

func TestNoProgressWatchdogRecognizesTaoSliceLifecycleActions(t *testing.T) {
	for _, action := range []string{"slice-complete", "slice-blocked"} {
		t.Run(action, func(t *testing.T) {
			watchdog := newNoProgressWatchdog(2, nil)
			if err := watchdog.observe(toolCall{name: "read"}); err != nil {
				t.Fatal(err)
			}
			if err := watchdog.observe(toolCall{name: "bash", arguments: `{"command":"git diff --check && tao ` + action + ` --plan-dir /plans/a"}`}); err != nil {
				t.Fatalf("chained lifecycle action was not productive: %v", err)
			}
			if err := watchdog.observe(toolCall{name: "bash", arguments: `{"command":"rg 'tao slice-complete' prompts"}`}); err != nil {
				t.Fatalf("lifecycle action did not reset watchdog before an ordinary search: %v", err)
			}
		})
	}
}

func TestNoProgressWatchdogDoesNotRecognizeQuotedLifecycleSearch(t *testing.T) {
	watchdog := newNoProgressWatchdog(1, nil)
	err := watchdog.observe(toolCall{name: "bash", arguments: `{"command":"rg 'tao slice-complete' prompts"}`})
	if err == nil || !strings.Contains(err.Error(), "no-progress watchdog") {
		t.Fatalf("quoted lifecycle search should be non-productive, got %v", err)
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

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fakeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdinDecoder *json.Decoder
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderr       io.Reader
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
func (p *fakeProcess) Stderr() io.Reader     { return p.stderr }
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
