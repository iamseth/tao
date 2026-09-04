package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	piagent "github.com/iamseth/tao/internal/agent/pi"
)

// runtimes are exercised against a single fake process that satisfies the
// shared process interface (and Pi's alias of it).
var (
	_ Runtime = piRuntime{}
	_ Runtime = claudeRuntime{}
)

func TestPromptAcceptanceProvesOnlyPreAcceptanceOutcomes(t *testing.T) {
	for _, tc := range []struct {
		value PromptAcceptance
		want  bool
	}{
		{value: PromptAcceptanceUnknown},
		{value: PromptAcceptanceNotTransmitted, want: true},
		{value: PromptAcceptanceRejected, want: true},
		{value: PromptAcceptanceAccepted},
	} {
		if got := tc.value.ProvenPreAcceptance(); got != tc.want {
			t.Fatalf("%q ProvenPreAcceptance() = %t, want %t", tc.value, got, tc.want)
		}
	}
}

func TestClaudeRuntimeMapsPermissionMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode PermissionMode
		want string
	}{
		{name: "auto", mode: PermissionModeAuto, want: "auto"},
		{name: "plan", mode: PermissionModePlan, want: "plan"},
		{name: "bypass", mode: PermissionModeBypassPermissions, want: "bypassPermissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := newFakeProcess(t)
			var gotArgs []string
			go func() {
				defer proc.finish()
				_ = proc.readPrompt()
				proc.writeEvent(`{"type":"result","subtype":"success","result":"ok"}`)
			}()
			runtime := claudeRuntime{starter: func(ctx context.Context, cwd, name string, args []string) (Process, error) {
				gotArgs = append([]string(nil), args...)
				return proc, nil
			}}

			result, err := runtime.RunSession(context.Background(), Session{Prompt: "work", PermissionMode: tc.mode, VerificationCommands: []string{"pnpm test"}})
			if err != nil {
				t.Fatal(err)
			}
			if result.FinalText != "ok" || result.PromptAcceptance != PromptAcceptanceAccepted {
				t.Fatalf("unexpected result: %#v", result)
			}
			if got := gotArgs[len(gotArgs)-1]; got != tc.want {
				t.Fatalf("permission mode arg = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeRuntimeNormalizesMetrics(t *testing.T) {
	proc := newFakeProcess(t)
	var gotName string
	go func() {
		defer proc.finish()
		_ = proc.readPrompt()
		proc.writeEvent(`{"type":"system","subtype":"init","session_id":"session-1","model":"claude-sonnet-4"}`)
		proc.writeEvent(`{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":4}}}`)
		proc.writeEvent(`{"type":"result","subtype":"success","session_id":"session-1","total_cost_usd":0.02}`)
	}()
	var log bytes.Buffer
	var progress bytes.Buffer
	runtime := claudeRuntime{starter: func(ctx context.Context, cwd, name string, args []string) (Process, error) {
		gotName = name
		return proc, nil
	}}

	result, err := runtime.RunSession(context.Background(), Session{RepoRoot: "/repo", Prompt: "work", CollectMetrics: true, Log: &log, Progress: &progress})
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "claude" {
		t.Fatalf("expected claude process, got %q", gotName)
	}
	if result.Output != "done" || result.FinalText != "done" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.MetricsWarning != "" {
		t.Fatalf("unexpected metrics warning: %q", result.MetricsWarning)
	}
	want := Metrics{SessionID: "session-1", ProviderID: "anthropic", ModelID: "claude-sonnet-4", InputTokens: 10, OutputTokens: 4, TotalTokens: 14, Cost: 0.02}
	if result.Metrics == nil || *result.Metrics != want {
		t.Fatalf("metrics = %#v, want %#v", result.Metrics, want)
	}
	if !strings.Contains(log.String(), `"type":"assistant","content":"done"`) || !strings.Contains(log.String(), "@tao-agent-log-v1") {
		t.Fatalf("expected framed session log, got %q", log.String())
	}
	if progress.String() != "assistant: done\n" || strings.Contains(progress.String(), "@tao-agent-log-v1") {
		t.Fatalf("expected human-readable progress, got %q", progress.String())
	}
}

func TestClaudeRuntimeOmitsMetricsWhenNotCollected(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_ = proc.readPrompt()
		proc.writeEvent(`{"type":"result","subtype":"success","result":"ok"}`)
	}()
	runtime := claudeRuntime{starter: func(ctx context.Context, cwd, name string, args []string) (Process, error) {
		return proc, nil
	}}

	result, err := runtime.RunSession(context.Background(), Session{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metrics != nil || result.MetricsWarning != "" {
		t.Fatalf("expected no metrics, got %#v warning=%q", result.Metrics, result.MetricsWarning)
	}
}

func TestPiRuntimeNormalizesMetricsAndPassesNoProgressConfiguration(t *testing.T) {
	proc := newFakeProcess(t)
	var gotCwd, gotName string
	var gotArgs []string
	serverErr := make(chan error, 1)
	go func() {
		defer proc.finish()
		if err := proc.readCommand(); err != nil {
			serverErr <- err
			return
		}
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-1","name":"read","arguments":{"path":"package.json"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-1","toolName":"read","content":[{"type":"text","text":"{}"}],"isError":false}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-2","name":"bash","arguments":{"command":"cd packages/commonStudent && pnpm test"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-2","toolName":"bash","content":[{"type":"text","text":"passed"}],"isError":false}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-3","name":"read","arguments":{"path":"package.json"}}]}}`)
		proc.writeEvent(`{"type":"message_end","message":{"role":"toolResult","toolCallId":"call-3","toolName":"read","content":[{"type":"text","text":"{}"}],"isError":false}}`)
		proc.writeEvent(`{"type":"message","role":"assistant","text":"done"}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
		if err := proc.readCommand(); err != nil { // get_state
			serverErr <- err
			return
		}
		proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":true,"data":{"sessionId":"session-1","model":{"provider":"pi-provider","id":"pi-model"}}}`)
		if err := proc.readCommand(); err != nil { // get_session_stats
			serverErr <- err
			return
		}
		proc.writeEvent(`{"id":"3","type":"response","command":"get_session_stats","success":true,"data":{"sessionId":"session-1","provider_id":"pi-provider","model_id":"pi-model","tokens":{"input":100,"output":50,"reasoning":10,"cacheRead":5,"cacheWrite":3,"total":168},"cost":0.0123,"total_messages":6,"user_messages":2,"assistant_messages":3,"errored_messages":1,"tool_calls":4}}`)
		serverErr <- nil
	}()
	runtime := piRuntime{starter: func(ctx context.Context, cwd, name string, args []string) (piagent.Process, error) {
		gotCwd, gotName = cwd, name
		gotArgs = append([]string(nil), args...)
		return proc, nil
	}}

	result, err := runtime.RunSession(context.Background(), Session{RepoRoot: "/repo", Prompt: "work", CollectMetrics: true, NoProgressToolLimit: 2, VerificationCommands: []string{"cd packages/commonStudent && pnpm test"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if gotCwd != "/repo" || gotName != "pi" || strings.Join(gotArgs, " ") != "--mode rpc --no-session" {
		t.Fatalf("unexpected process start: cwd=%q name=%q args=%#v", gotCwd, gotName, gotArgs)
	}
	if result.PromptAcceptance != PromptAcceptanceAccepted {
		t.Fatalf("prompt acceptance = %q, want accepted", result.PromptAcceptance)
	}
	if result.Output != "done" || result.FinalText != "done" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.MetricsWarning != "" {
		t.Fatalf("unexpected metrics warning: %q", result.MetricsWarning)
	}
	want := Metrics{
		SessionID: "session-1", ProviderID: "pi-provider", ModelID: "pi-model",
		InputTokens: 100, OutputTokens: 50, ReasoningTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 3, TotalTokens: 168,
		Cost: 0.0123, TotalMessages: 6, UserMessages: 2, AssistantMessages: 3, ErroredMessages: 1, ToolCalls: 4,
	}
	if result.Metrics == nil || *result.Metrics != want {
		t.Fatalf("metrics = %#v, want %#v", result.Metrics, want)
	}
}

func TestPiRuntimeSurfacesSessionInfoErrorAsMetricsWarning(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_ = proc.readCommand()
		proc.writeEvent(`{"type":"message","role":"assistant","text":"done"}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
		_ = proc.readCommand()
		proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":false,"error":"state unavailable"}`)
	}()
	runtime := piRuntime{starter: func(ctx context.Context, cwd, name string, args []string) (piagent.Process, error) {
		return proc, nil
	}}

	result, err := runtime.RunSession(context.Background(), Session{Prompt: "work", CollectMetrics: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.MetricsWarning, "pi rpc get_state: state unavailable") {
		t.Fatalf("expected session-info warning, got %q", result.MetricsWarning)
	}
	if result.Metrics == nil {
		t.Fatal("expected best-effort metrics even with session-info warning")
	}
}

func TestPiRuntimeSkipsMetricsWhenNotCollected(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_ = proc.readCommand()
		_ = proc.stdinReader.Close()
		proc.writeEvent(`{"type":"message","role":"assistant","text":"done"}`)
		proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
	}()
	runtime := piRuntime{starter: func(ctx context.Context, cwd, name string, args []string) (piagent.Process, error) {
		return proc, nil
	}}

	result, err := runtime.RunSession(context.Background(), Session{Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metrics != nil || result.MetricsWarning != "" {
		t.Fatalf("expected no metrics without collection, got %#v warning=%q", result.Metrics, result.MetricsWarning)
	}
	if result.FinalText != "done" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

// fakeProcess satisfies every agent process alias. Pi tests decode JSON commands
// from stdin via readCommand; stream-json tests drain the prompt via readPrompt.
type fakeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdinDecoder *json.Decoder
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	done         chan struct{}
	once         sync.Once
	waitErr      error
	killed       bool
}

func newFakeProcess(t *testing.T) *fakeProcess {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	return &fakeProcess{
		t:            t,
		stdinReader:  stdinReader,
		stdinWriter:  stdinWriter,
		stdinDecoder: json.NewDecoder(stdinReader),
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		done:         make(chan struct{}),
	}
}

func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *fakeProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *fakeProcess) Stderr() io.Reader     { return strings.NewReader("") }

func (p *fakeProcess) Wait() error {
	<-p.done
	return p.waitErr
}

func (p *fakeProcess) Kill() error {
	p.killed = true
	p.finish()
	return nil
}

func (p *fakeProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stdinReader.Close()
		close(p.done)
	})
}

func (p *fakeProcess) readPrompt() error {
	_, err := io.ReadAll(p.stdinReader)
	return err
}

func (p *fakeProcess) readCommand() error {
	for {
		var cmd map[string]any
		if err := p.stdinDecoder.Decode(&cmd); err != nil {
			return err
		}
		id, _ := cmd["id"].(string)
		switch id {
		case "tao-readiness-state":
			p.writeEvent(`{"id":"tao-readiness-state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"test-provider","id":"test-model"}}}`)
		case "tao-readiness-models":
			p.writeEvent(`{"id":"tao-readiness-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"test-provider","id":"test-model"}]}}`)
		case "tao-prompt":
			p.writeEvent(`{"id":"tao-prompt","type":"response","command":"prompt","success":true}`)
			return nil
		default:
			return nil
		}
	}
}

func (p *fakeProcess) writeEvent(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}
