package opencode

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

func TestClientStartsOpenCodeWithPromptAndParsesStream(t *testing.T) {
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
		proc.writeEvent(`{"type":"step_start","sessionID":"ses_1","part":{"type":"step-start"}}`)
		proc.writeEvent(`{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"working","metadata":{"openai":{"phase":"tool"}}}}`)
		proc.writeEvent(`{"type":"step_finish","sessionID":"ses_1","part":{"type":"step-finish","reason":"tool-calls","tokens":{"total":6625,"input":6525,"output":63,"reasoning":37,"cache":{"write":0,"read":0}},"cost":0.01}}`)
		proc.writeEvent(`{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"done"}}`)
		proc.writeEvent(`{"type":"step_finish","sessionID":"ses_1","part":{"type":"step-finish","reason":"stop","tokens":{"total":503,"input":494,"output":9,"reasoning":0,"cache":{"write":1,"read":6144}},"cost":0.02}}`)
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
	if gotCwd != "/repo" || gotName != "opencode" {
		t.Fatalf("unexpected process start: cwd=%q name=%q", gotCwd, gotName)
	}
	wantArgs := "run --format json"
	if strings.Join(gotArgs, " ") != wantArgs {
		t.Fatalf("args = %q, want %q", strings.Join(gotArgs, " "), wantArgs)
	}
	if result.FinalText != "done" || result.Output != "working\ndone" || result.SessionID != "ses_1" {
		t.Fatalf("unexpected result text/session: %#v", result)
	}
	if result.ProviderID != "openai" {
		t.Fatalf("expected provider inferred from metadata, got %q", result.ProviderID)
	}
	if result.Usage.Input != 7019 || result.Usage.Output != 72 || result.Usage.Total != 7128 || result.Cost != 0.03 {
		t.Fatalf("expected accumulated token/cost metrics, got %#v cost=%v", result.Usage, result.Cost)
	}
	if result.Metrics.TotalTokens != 7128 || result.Metrics.ProviderID != "openai" || result.MetricsWarning != "" {
		t.Fatalf("unexpected metrics: %#v warning=%q", result.Metrics, result.MetricsWarning)
	}
	if !strings.Contains(log.String(), "assistant: done") {
		t.Fatalf("expected assistant log, got %q", log.String())
	}
}

func TestClientAddsBypassFlagOnlyWhenRequested(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode perm.PermissionMode
		want string
	}{
		{name: "default", want: "run --format json"},
		{name: "plan", mode: perm.PermissionModePlan, want: "run --format json"},
		{name: "bypass", mode: perm.PermissionModeBypassPermissions, want: "run --format json --dangerously-skip-permissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := newFakeProcess(t)
			var gotArgs []string
			go func() {
				defer proc.finish(nil)
				_, _ = proc.readPrompt()
				proc.writeEvent(`{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"ok"}}`)
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
	if err == nil || !strings.Contains(err.Error(), `unsupported opencode permission mode "danger"`) {
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
	if !strings.Contains(log.String(), "tao opencode error: opencode agent error: auth failed") {
		t.Fatalf("expected error log, got %q", log.String())
	}
}

func TestClientReturnsParseErrorOnInvalidJSON(t *testing.T) {
	proc := newFakeProcess(t)
	go func() {
		defer proc.finish(nil)
		_, _ = proc.readPrompt()
		proc.writeEvent(`{not json`)
	}()
	client := Client{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (process.Process, error) {
		return proc, nil
	}}

	_, err := client.RunAgentSession(context.Background(), Request{Prompt: "work"})
	if err == nil || !strings.Contains(err.Error(), "parse opencode json line 1") {
		t.Fatalf("expected json parse error, got %v", err)
	}
	if !proc.killed {
		t.Fatal("expected process kill on parse failure")
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
	if err == nil || !strings.Contains(err.Error(), "opencode exited: exit status 1") {
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
