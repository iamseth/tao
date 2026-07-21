package run

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
)

// testAgentExecutor builds the unified executor for a runtime kind, mirroring the
// production wiring (descriptor lookup + commit-prompt renderer) so tests can
// exercise per-kind behavior without an agent-kind switch.
func testAgentExecutor(kind AgentKind, options agentExecutorOptions, logAppender plan.LogAppender, eventAppender plan.EventAppender) agentExecutor {
	descriptor, _ := agent.Lookup(kind)
	return agentExecutor{descriptor: descriptor, options: options, logAppender: logAppender, eventAppender: eventAppender}
}

func TestSliceExecutorSelectsPiByDefault(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	promptSeen := false
	starter := fakePiSessionStarter(t, `done`, &promptSeen)
	execution := testRunExecution(ExecutionConfig{}, RunDependencies{LogAppender: repo, ProcessStarter: starter})
	resolveExecutorDefaults(&execution)
	executor, ok := execution.Dependencies.SliceExecutor.(agentExecutor)
	if !ok {
		t.Fatalf("expected agent slice executor, got %T", execution.Dependencies.SliceExecutor)
	}
	if executor.descriptor.Kind != AgentPi {
		t.Fatalf("expected pi slice executor, got %q", executor.descriptor.Kind)
	}
	if err := executor.RunSlice(context.Background(), SliceRun{PlanDir: t.TempDir(), SliceID: "001-a", RepoRoot: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if !promptSeen {
		t.Fatal("expected pi prompt command")
	}
}

func TestPullRequestBodyGeneratorSelectsPiWhenRequested(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi}}, RunDependencies{LogAppender: repo})
	resolveExecutorDefaults(&execution)
	creator, ok := execution.Dependencies.PullRequestCreator.(deterministicPullRequestCreator)
	if !ok {
		t.Fatalf("expected deterministic pull request creator, got %T", execution.Dependencies.PullRequestCreator)
	}
	bodyGenerator, ok := creator.bodyGenerator.(agentExecutor)
	if !ok || bodyGenerator.descriptor.Kind != AgentPi {
		t.Fatalf("expected pi pull request body generator, got %T", creator.bodyGenerator)
	}
}

func TestPiExecutorSessionInfoWarningDoesNotFailCompletedWork(t *testing.T) {
	planDir := writeMetricsPlan(t, "/repo", "plan-a")
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	promptSeen := false
	executor := testAgentExecutor(AgentPi, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: fakePiSessionInfoFailureStarter(t, &promptSeen)}}, repo, repo)

	if err := executor.RunSlice(context.Background(), SliceRun{PlanDir: planDir, SliceID: "001-a", RepoRoot: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if !promptSeen {
		t.Fatal("expected pi prompt command")
	}
	logText := readMetricsText(t, filepath.Join(planDir, "agent-run.log"))
	if !strings.Contains(logText, "tao telemetry warning: collect pi session info: pi rpc get_state: state unavailable") {
		t.Fatalf("expected best-effort session info warning in log, got:\n%s", logText)
	}

	var event plan.Event
	lines := strings.Split(strings.TrimSpace(readMetricsText(t, filepath.Join(planDir, "events.jsonl"))), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != plan.EventTypeAgentMetrics || event.Metrics == nil || event.Metrics.Status != plan.StatusCompleted || event.Metrics.SessionID != "session-1" {
		t.Fatalf("expected completed partial Pi metrics, got %#v", event)
	}
}

func TestPiExecutorCreatesPullRequestFromCapturedOutput(t *testing.T) {
	planDir := t.TempDir()
	repo := plan.NewFileRepository(t.TempDir())
	promptSeen := false
	executor := testAgentExecutor(AgentPi, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: fakePiSessionStarter(t, "created https://github.com/iamseth/tao/pull/456", &promptSeen)}, Now: func() time.Time {
		return time.Date(2026, 5, 28, 3, 0, 0, 0, time.UTC)
	}}, repo, nil)

	pr, err := executor.CreatePullRequest(context.Background(), PullRequestRun{PlanDir: planDir, PlanID: "plan-a", RepoRoot: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if !promptSeen {
		t.Fatal("expected pi prompt command")
	}
	if pr.Number != 456 || pr.URL != "https://github.com/iamseth/tao/pull/456" {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
}

func TestPiExecutorAppendsAgentMetricsEvent(t *testing.T) {
	planDir := writeMetricsPlan(t, "/repo", "plan-a")
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	promptSeen := false
	executor := testAgentExecutor(AgentPi, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: fakePiSessionStarterWithStats(t, "done", &promptSeen, `{"type":"session_stats","session_id":"session-1","total_tokens":12,"assistant_messages":2,"tool_calls":3}`)}}, repo, repo)

	if err := executor.RunSlice(context.Background(), SliceRun{PlanDir: planDir, SliceID: "001-a", RepoRoot: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if !promptSeen {
		t.Fatal("expected pi prompt command")
	}

	var event plan.Event
	lines := strings.Split(strings.TrimSpace(readMetricsText(t, filepath.Join(planDir, "events.jsonl"))), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != plan.EventTypeAgentMetrics || event.PlanID != "plan-a" || event.SliceID != "001-a" || event.Metrics == nil {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.Metrics.Agent != "pi" || event.Metrics.SessionID != "session-1" || event.Metrics.TotalTokens != 12 || event.Metrics.ToolCalls != 3 {
		t.Fatalf("unexpected metrics payload: %#v", event.Metrics)
	}
}

func TestPiExecutorMarksAgentMetricsFailedOnPiError(t *testing.T) {
	planDir := writeMetricsPlan(t, "/repo", "plan-a")
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	executor := testAgentExecutor(AgentPi, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		proc := newFakePiProcess(t)
		go func() {
			defer proc.finish()
			_, _ = proc.readCommand()
			proc.writeEvent(`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"WebSocket closed 1006 Connection ended"}}`)
		}()
		return proc, nil
	}}}, repo, repo)

	err := executor.RunSlice(context.Background(), SliceRun{PlanDir: planDir, SliceID: "001-a", RepoRoot: "/repo"})
	if err == nil || !strings.Contains(err.Error(), "WebSocket closed 1006") {
		t.Fatalf("expected Pi error, got %v", err)
	}

	var event plan.Event
	lines := strings.Split(strings.TrimSpace(readMetricsText(t, filepath.Join(planDir, "events.jsonl"))), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != plan.EventTypeAgentMetrics || event.Metrics == nil || event.Metrics.Status != "failed" || event.Metrics.Result != "failed" {
		t.Fatalf("expected failed Pi metrics, got %#v", event)
	}
}

func fakePiSessionStarter(t *testing.T, finalText string, promptSeen *bool) ProcessStarter {
	t.Helper()
	return fakePiSessionStarterWithStats(t, finalText, promptSeen, `{"type":"session_stats","session_id":"session-1"}`)
}

func fakePiSessionInfoFailureStarter(t *testing.T, promptSeen *bool) ProcessStarter {
	t.Helper()
	return func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		if name != "pi" || strings.Join(args, " ") != "--mode rpc" {
			t.Fatalf("unexpected pi process start: %s %#v", name, args)
		}
		proc := newFakePiProcess(t)
		go func() {
			defer proc.finish()
			cmd, err := proc.readCommand()
			if err != nil {
				return
			}
			if cmd["type"] == "prompt" {
				*promptSeen = true
			}
			proc.writeEvent(`{"type":"message","role":"assistant","text":"done"}`)
			proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":false,"error":"state unavailable"}`)
		}()
		return proc, nil
	}
}

func fakePiSessionStarterWithStats(t *testing.T, finalText string, promptSeen *bool, statsEvent string) ProcessStarter {
	t.Helper()
	return func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		if name != "pi" || strings.Join(args, " ") != "--mode rpc" {
			t.Fatalf("unexpected pi process start: %s %#v", name, args)
		}
		proc := newFakePiProcess(t)
		go func() {
			defer proc.finish()
			cmd, err := proc.readCommand()
			if err != nil {
				return
			}
			if cmd["type"] == "prompt" {
				*promptSeen = true
			}
			proc.writeEvent(`{"type":"message","role":"assistant","text":` + strconv.Quote(finalText) + `}`)
			proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"type":"state","session_id":"session-1"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(statsEvent)
		}()
		return proc, nil
	}
}

type fakePiProcess struct {
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

func newFakePiProcess(t *testing.T) *fakePiProcess {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	return &fakePiProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdinDecoder: json.NewDecoder(stdinReader), stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
}

func (p *fakePiProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *fakePiProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *fakePiProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (p *fakePiProcess) Wait() error {
	<-p.done
	return nil
}
func (p *fakePiProcess) Kill() error {
	p.killed = true
	return nil
}

func (p *fakePiProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stdinReader.Close()
		close(p.done)
	})
}

func (p *fakePiProcess) readCommand() (map[string]any, error) {
	var cmd map[string]any
	if err := p.stdinDecoder.Decode(&cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (p *fakePiProcess) writeEvent(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}
