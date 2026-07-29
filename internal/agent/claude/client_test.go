package claude

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

func TestClientStartsClaudeWithPromptAndPermissionMode(t *testing.T) {
	proc := newFakeProcess(t)
	var gotCwd, gotName string
	var gotArgs []string
	serverErr := make(chan error, 1)
	go func() {
		defer proc.finish(nil)
		prompt, err := proc.readPrompt()
		if err != nil {
			serverErr <- err
			return
		}
		if prompt != "do work" {
			serverErr <- errors.New("prompt was not delivered on stdin")
			return
		}
		proc.writeEvent(`{"type":"system","subtype":"init","session_id":"session-1","model":"claude-sonnet-4"}`)
		proc.writeEvent(`{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":4}}}`)
		proc.writeEvent(`{"type":"result","subtype":"success","session_id":"session-1","total_cost_usd":0.02}`)
		serverErr <- nil
	}()
	var log bytes.Buffer
	client := Client{Log: &log, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		gotCwd, gotName = cwd, name
		gotArgs = append([]string(nil), args...)
		return proc, nil
	}}

	result, err := client.RunAgentSession(context.Background(), Request{RepoRoot: "/repo", Prompt: "do work", PermissionMode: perm.PermissionModePlan})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if gotCwd != "/repo" || gotName != "claude" {
		t.Fatalf("unexpected process start: cwd=%q name=%q", gotCwd, gotName)
	}
	wantArgs := "--print --output-format stream-json --verbose --no-session-persistence --permission-mode plan"
	if strings.Join(gotArgs, " ") != wantArgs {
		t.Fatalf("args = %q, want %q", strings.Join(gotArgs, " "), wantArgs)
	}
	if result.FinalText != "done" || result.Output != "done" || result.SessionID != "session-1" || result.Model != "claude-sonnet-4" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Usage["input_tokens"] != float64(10) || result.CostUSD != 0.02 {
		t.Fatalf("expected telemetry, got %#v", result)
	}
	if !strings.Contains(log.String(), `"type":"assistant","content":"done"`) {
		t.Fatalf("expected assistant log, got %q", log.String())
	}
}

func TestClientDefaultsPermissionModeAndAcceptsBypass(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode perm.PermissionMode
		want string
	}{
		{name: "default", want: "auto"},
		{name: "bypass", mode: perm.PermissionModeBypassPermissions, want: "bypassPermissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := newFakeProcess(t)
			var gotArgs []string
			go func() {
				defer proc.finish(nil)
				_, _ = proc.readPrompt()
				proc.writeEvent(`{"type":"result","subtype":"success","result":"ok"}`)
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
				t.Fatalf("expected result text fallback, got %#v", result)
			}
			if got := gotArgs[len(gotArgs)-1]; got != tc.want {
				t.Fatalf("permission mode = %q, want %q", got, tc.want)
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
	if err == nil || !strings.Contains(err.Error(), `unsupported claude permission mode "danger"`) {
		t.Fatalf("expected unsupported mode error, got %v", err)
	}
}

func TestClientReturnsAgentErrorAndLogs(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish(nil)
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
	if !strings.Contains(log.String(), "tao claude error: claude agent error: auth failed") {
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
	proc.finish(nil)
}

func TestClientWaitError(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish(errors.New("exit status 1"))
		_, _ = proc.readPrompt()
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err == nil || !strings.Contains(err.Error(), "claude exited: exit status 1") {
		t.Fatalf("expected wait error, got %v", err)
	}
}

type fakeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
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
	return &fakeProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
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
	p.finish(nil)
	return nil
}

func (p *fakeProcess) finish(err error) {
	p.once.Do(func() {
		p.waitErr = err
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
