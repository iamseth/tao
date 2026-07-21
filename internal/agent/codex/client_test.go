package codex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/iamseth/tao/internal/agent/perm"
	"github.com/iamseth/tao/internal/agent/process"
)

func TestClientStartsCodexWithPromptAndParsesStream(t *testing.T) {
	proc := newFakeProcess(t)
	var gotCwd, gotName string
	var gotArgs []string
	serverErr := make(chan error, 1)
	go func() {
		defer proc.finish()
		prompt, err := proc.readPrompt()
		if err != nil {
			serverErr <- err
			return
		}
		if prompt != "do work" {
			serverErr <- errors.New("prompt was not delivered on stdin")
			return
		}
		proc.writeEvent(`{"type":"thread.started","thread_id":"thread_1"}`)
		proc.writeEvent(`{"type":"item.completed","item":{"type":"agent_message","text":"working"}}`)
		proc.writeEvent(`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":4,"reasoning_output_tokens":3},"model":"gpt-5-codex"}`)
		proc.writeEvent(`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`)
		serverErr <- nil
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		gotCwd, gotName = cwd, name
		gotArgs = append([]string(nil), args...)
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{RepoRoot: "/repo", Prompt: "do work", PermissionMode: perm.PermissionModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if gotCwd != "/repo" || gotName != "codex" {
		t.Fatalf("unexpected process start: cwd=%q name=%q", gotCwd, gotName)
	}
	wantArgs := "exec --json --sandbox workspace-write --ask-for-approval never -"
	if strings.Join(gotArgs, " ") != wantArgs {
		t.Fatalf("args = %q, want %q", strings.Join(gotArgs, " "), wantArgs)
	}
	if result.FinalText != "done" || result.Output != "working\ndone" || result.SessionID != "thread_1" {
		t.Fatalf("unexpected result text/session: %#v", result)
	}
	if result.ProviderID != "openai" || result.ModelID != "gpt-5-codex" {
		t.Fatalf("unexpected provider/model: %#v", result)
	}
	if result.Usage.Input != 10 || result.Usage.CachedInput != 2 || result.Usage.Output != 4 || result.Usage.ReasoningOutput != 3 {
		t.Fatalf("expected accumulated token metrics, got %#v", result.Usage)
	}
	if result.Metrics.TotalTokens != 17 || result.Metrics.CacheReadTokens != 2 || result.Metrics.ReasoningTokens != 3 || result.Metrics.ProviderID != "openai" || result.MetricsWarning != "" {
		t.Fatalf("unexpected metrics: %#v warning=%q", result.Metrics, result.MetricsWarning)
	}
	if !strings.Contains(log.String(), "assistant: done") {
		t.Fatalf("expected assistant log, got %q", log.String())
	}
}

func TestClientPermissionModeFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode perm.PermissionMode
		want string
	}{
		{name: "default", want: "exec --json --sandbox workspace-write --ask-for-approval never -"},
		{name: "auto", mode: perm.PermissionModeAuto, want: "exec --json --sandbox workspace-write --ask-for-approval never -"},
		{name: "plan", mode: perm.PermissionModePlan, want: "exec --json --sandbox read-only --ask-for-approval never -"},
		{name: "bypass", mode: perm.PermissionModeBypassPermissions, want: "exec --json --dangerously-bypass-approvals-and-sandbox -"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := newFakeProcess(t)
			var gotArgs []string
			go func() {
				defer proc.finish()
				_, _ = proc.readPrompt()
				proc.writeEvent(`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`)
			}()
			client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
				gotArgs = append([]string(nil), args...)
				return proc, nil
			}}
			result, err := client.RunAgentSession(context.Background(), Request{Prompt: "work", PermissionMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			if result.FinalText != "ok" {
				t.Fatalf("expected final text, got %#v", result)
			}
			if got := strings.Join(gotArgs, " "); got != tc.want {
				t.Fatalf("args = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientRejectsUnsupportedPermissionMode(t *testing.T) {
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		t.Fatal("process should not start")
		return nil, nil
	}}
	_, err := client.RunAgentSession(context.Background(), Request{PermissionMode: "danger"})
	if err == nil || !strings.Contains(err.Error(), `unsupported codex permission mode "danger"`) {
		t.Fatalf("expected unsupported mode error, got %v", err)
	}
}

func TestClientReturnsAgentErrorAndLogs(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish()
		_, _ = proc.readPrompt()
		proc.writeEvent(`{"type":"error","error":{"message":"auth failed"}}`)
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("expected agent error, got %v", err)
	}
	if !proc.killed {
		t.Fatal("expected process kill")
	}
	if !strings.Contains(log.String(), "tao codex error: codex agent error: auth failed") {
		t.Fatalf("expected error log, got %q", log.String())
	}
}

func TestClientContextCancellationKillsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	proc := newFakeProcess(t)
	go func() {
		_, _ = proc.readPrompt()
		cancel()
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(ctx, Request{Prompt: "work"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if !proc.killed {
		t.Fatal("expected process kill")
	}
	proc.finish()
}

type fakeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
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
	return &fakeProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
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

func (p *fakeProcess) readPrompt() (string, error) {
	data, err := io.ReadAll(p.stdinReader)
	return string(data), err
}

func (p *fakeProcess) writeEvent(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}
