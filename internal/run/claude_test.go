package run

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
)

func TestClaudeExecutorRunSliceUsesAutoPermissionAndLogsMetrics(t *testing.T) {
	planDir := writeMetricsPlan(t, "/repo", "plan-a")
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	var got fakeClaudeStart
	executor := testAgentExecutor(AgentClaude, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: fakeProcessStarter(t, &got, `{"type":"assistant","session_id":"claude-session","message":{"role":"assistant","model":"claude-sonnet","usage":{"input_tokens":11,"output_tokens":7},"content":[{"type":"text","text":"done"}]}}`)}}, repo, repo)

	if err := executor.RunSlice(context.Background(), SliceRun{PlanDir: planDir, SliceID: "001-a", RepoRoot: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if got.cwd != "/repo" || got.name != "claude" {
		t.Fatalf("unexpected claude process: %#v", got)
	}
	wantArgs := []string{"--print", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", "auto"}
	if !reflect.DeepEqual(got.args, wantArgs) {
		t.Fatalf("unexpected claude args: %#v", got.args)
	}
	if !strings.Contains(got.prompt, "Plan directory: `"+planDir+"`") {
		t.Fatalf("expected rendered work prompt, got %q", got.prompt)
	}
	logText := readMetricsText(t, filepath.Join(planDir, "agent-run.log"))
	if !strings.Contains(logText, "running 001-a") || !strings.Contains(logText, "assistant: done") {
		t.Fatalf("expected claude session log output, got:\n%s", logText)
	}

	var event plan.Event
	lines := strings.Split(strings.TrimSpace(readMetricsText(t, filepath.Join(planDir, "events.jsonl"))), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != plan.EventTypeAgentMetrics || event.Agent != "claude" || event.Metrics == nil {
		t.Fatalf("unexpected metrics event: %#v", event)
	}
	if event.Metrics.Agent != "claude" || event.Metrics.SessionID != "claude-session" || event.Metrics.ModelID != "claude-sonnet" || event.Metrics.InputTokens != 11 || event.Metrics.OutputTokens != 7 || event.Metrics.TotalTokens != 18 {
		t.Fatalf("unexpected claude metrics: %#v", event.Metrics)
	}
}

func TestClaudeExecutorCreatesPullRequestFromCapturedOutput(t *testing.T) {
	planDir := writeMetricsPlan(t, "/repo", "plan-a")
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	var got fakeClaudeStart
	executor := testAgentExecutor(AgentClaude, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: fakeProcessStarter(t, &got, `{"type":"result","result":"created https://github.com/iamseth/tao/pull/789"}`)}}, repo, repo)

	pr, err := executor.CreatePullRequest(context.Background(), PullRequestRun{PlanDir: planDir, PlanID: "plan-a", RepoRoot: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 789 || pr.URL != "https://github.com/iamseth/tao/pull/789" {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
	if !strings.Contains(got.prompt, "Create a GitHub pull request") || !strings.Contains(got.prompt, planDir) {
		t.Fatalf("expected PR prompt, got %q", got.prompt)
	}
}

func TestClaudeExecutorMissingMetricsWarnsWithoutEvent(t *testing.T) {
	planDir := writeMetricsPlan(t, "/repo", "plan-a")
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	var got fakeClaudeStart
	executor := testAgentExecutor(AgentClaude, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: fakeProcessStarter(t, &got, `{"type":"result","result":"done"}`)}}, repo, repo)

	if err := executor.RunSlice(context.Background(), SliceRun{PlanDir: planDir, SliceID: "001-a", RepoRoot: "/repo"}); err != nil {
		t.Fatal(err)
	}
	logText := readMetricsText(t, filepath.Join(planDir, "agent-run.log"))
	if !strings.Contains(logText, "tao telemetry warning: claude metrics absent from stream output") {
		t.Fatalf("expected missing metrics warning, got:\n%s", logText)
	}
	if strings.Contains(readMetricsText(t, filepath.Join(planDir, "events.jsonl")), `"type":"agent_metrics"`) {
		t.Fatal("did not expect agent_metrics event for absent Claude metrics")
	}
}

func TestClaudeExecutorSkipPermissionsUsesBypassMode(t *testing.T) {
	planDir := writeMetricsPlan(t, "/repo", "plan-a")
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	var got fakeClaudeStart
	executor := testAgentExecutor(AgentClaude, agentExecutorOptions{SkipPermissions: true, Deps: agent.RuntimeDeps{ProcessStarter: fakeProcessStarter(t, &got, `{"type":"result","result":"done"}`)}}, repo, nil)

	if err := executor.RunSlice(context.Background(), SliceRun{PlanDir: planDir, SliceID: "001-a", RepoRoot: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.args, " ") != "--print --output-format stream-json --verbose --no-session-persistence --permission-mode bypassPermissions" {
		t.Fatalf("expected bypass permission mode, got %#v", got.args)
	}
}

type fakeClaudeStart struct {
	cwd    string
	name   string
	args   []string
	prompt string
}

func fakeProcessStarter(t *testing.T, got *fakeClaudeStart, events ...string) ProcessStarter {
	t.Helper()
	return func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		got.cwd = cwd
		got.name = name
		got.args = append([]string{}, args...)
		proc := newFakeClaudeProcess(t)
		go func() {
			defer proc.finish()
			prompt, _ := io.ReadAll(proc.stdinReader)
			got.prompt = string(prompt)
			for _, event := range events {
				proc.writeEvent(event)
			}
		}()
		return proc, nil
	}
}

type fakeClaudeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	done         chan struct{}
	once         sync.Once
}

func newFakeClaudeProcess(t *testing.T) *fakeClaudeProcess {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	return &fakeClaudeProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
}

func (p *fakeClaudeProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *fakeClaudeProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *fakeClaudeProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (p *fakeClaudeProcess) Wait() error {
	<-p.done
	return nil
}
func (p *fakeClaudeProcess) Kill() error { return nil }

func (p *fakeClaudeProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stdinReader.Close()
		close(p.done)
	})
}

func (p *fakeClaudeProcess) writeEvent(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}
