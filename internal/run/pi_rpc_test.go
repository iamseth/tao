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
	"github.com/iamseth/tao/internal/forge"
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
	if _, ok := creator.pullRequests.(forge.GitHub); !ok {
		t.Fatalf("expected GitHub pull request forge, got %T", creator.pullRequests)
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

func TestServiceExecuteRecoversStructuredPiTransportFailureThroughFreshRPCProcess(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	persistRunArtifacts(t, detail.Dir, detail)
	completed := cloneRunRestartDetail(t, detail)
	completed.State.Status = plan.StatusCompleted
	completed.State.Plan.CurrentSlice = nil
	completed.State.Plan.PendingSlices = nil
	completed.State.Plan.CompletedSlices = []string{"001-a"}
	completed.Slices.Slices[0].Status = plan.StatusCompleted
	completed.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "base"}

	var processes []*fakePiProcess
	starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		if name != "pi" || strings.Join(args, " ") != "--mode rpc" || cwd != root {
			t.Fatalf("unexpected pi process start: cwd=%q command=%s %#v", cwd, name, args)
		}
		proc := newFakePiProcess(t)
		processes = append(processes, proc)
		attempt := len(processes)
		go func() {
			defer proc.finish()
			if _, err := proc.readCommand(); err != nil {
				return
			}
			if attempt == 1 {
				proc.writeEvent(`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"WebSocket closed 1006 Connection ended","diagnostics":[{"type":"provider_transport_failure","error":{"message":"WebSocket closed 1006 Connection ended"}}]}}`)
				return
			}
			serveSuccessfulPiSession(proc, "session-2")
		}()
		return proc, nil
	}

	var events []plan.Event
	fileRepo := plan.NewFileRepository(filepath.Dir(detail.Dir))
	appender := eventAppenderFunc(func(planDir string, event plan.Event) error {
		events = append(events, event)
		return fileRepo.AppendEvent(planDir, event)
	})
	var delays []time.Duration
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail, detail, detail, completed}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: piTransportGitRunner(t, detail, root, func() string {
			if len(processes) < 2 {
				return " M partial.go\n"
			}
			return ""
		}),
		ProcessStarter: starter,
		TransportRetryDelay: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
		EventAppender: appender,
		LogAppender:   fileRepo,
	}})

	if err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, MaxSlices: 1}}); err != nil {
		t.Fatalf("recover structured Pi transport failure: %v", err)
	}
	if len(processes) != 2 || processes[0] == processes[1] {
		t.Fatalf("Pi process starts = %d, want two fresh RPC processes", len(processes))
	}
	if !processes[0].killed || !processes[0].waited {
		t.Fatalf("failed Pi process cleanup: killed=%t waited=%t", processes[0].killed, processes[0].waited)
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("retry delays = %v, want [1s]", delays)
	}

	failedMetrics := 0
	completedMetrics := 0
	started := 0
	for _, event := range append(detail.Events, events...) {
		switch {
		case event.Type == plan.EventTypeSliceStarted:
			started++
		case event.Type == plan.EventTypeAgentMetrics && event.Metrics != nil && event.Metrics.Status == "failed":
			failedMetrics++
		case event.Type == plan.EventTypeAgentMetrics && event.Metrics != nil && event.Metrics.Status == plan.StatusCompleted:
			completedMetrics++
		}
	}
	if failedMetrics != 1 || completedMetrics != 1 {
		t.Fatalf("Pi metrics failed/completed = %d/%d, want 1/1; events=%#v", failedMetrics, completedMetrics, events)
	}
	if started != 1 {
		t.Fatalf("slice_started events = %d, want exactly one", started)
	}
	assertTransportResumeEvents(t, events, []int{1, 2}, 1)
}

func TestServiceExecuteStructuredPiTransportFailuresStopAfterThirdSession(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	persistRunArtifacts(t, detail.Dir, detail)
	var processes []*fakePiProcess
	starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		proc := newFakePiProcess(t)
		processes = append(processes, proc)
		go func() {
			defer proc.finish()
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"WebSocket closed 1006 Connection ended","diagnostics":[{"type":"provider_transport_failure"}]}}`)
		}()
		return proc, nil
	}
	fileRepo := plan.NewFileRepository(filepath.Dir(detail.Dir))
	var events []plan.Event
	appender := eventAppenderFunc(func(planDir string, event plan.Event) error {
		events = append(events, event)
		return fileRepo.AppendEvent(planDir, event)
	})
	var delays []time.Duration
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner:  piTransportGitRunner(t, detail, root, func() string { return " M partial.go\n" }),
		ProcessStarter: starter,
		TransportRetryDelay: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
		EventAppender: appender,
		LogAppender:   fileRepo,
	}})

	err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if err == nil || !strings.Contains(err.Error(), "provider_transport_failure") {
		t.Fatalf("execute error = %v, want structured Pi transport failure", err)
	}
	if len(processes) != 3 || len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
		t.Fatalf("sessions=%d delays=%v, want three sessions and [1s 2s]", len(processes), delays)
	}
	failedMetrics := 0
	for _, event := range events {
		if event.Type == plan.EventTypeAgentMetrics && event.Metrics != nil && event.Metrics.Status == "failed" {
			failedMetrics++
		}
	}
	if failedMetrics != 3 {
		t.Fatalf("failed Pi metrics = %d, want 3", failedMetrics)
	}
	assertTransportResumeEvents(t, events, []int{1, 2, 3}, 3)
	for i, proc := range processes {
		if !proc.killed || !proc.waited {
			t.Fatalf("Pi process %d cleanup: killed=%t waited=%t", i+1, proc.killed, proc.waited)
		}
	}
}

func TestServiceExecuteDoesNotRetryUnstructuredPiWebSocketErrorOrTimeout(t *testing.T) {
	tests := []struct {
		name           string
		timeout        time.Duration
		serve          func(context.Context, *fakePiProcess)
		wantError      string
		wantTimeoutEvt bool
	}{
		{
			name: "unstructured WebSocket text",
			serve: func(_ context.Context, proc *fakePiProcess) {
				proc.writeEvent(`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"WebSocket closed 1006 Connection ended"}}`)
			},
			wantError: "WebSocket closed 1006",
		},
		{
			name:    "session timeout",
			timeout: time.Millisecond,
			serve: func(ctx context.Context, _ *fakePiProcess) {
				<-ctx.Done()
			},
			wantError:      "agent session timed out",
			wantTimeoutEvt: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			persistRunArtifacts(t, detail.Dir, detail)
			processStarts := 0
			starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
				processStarts++
				proc := newFakePiProcess(t)
				go func() {
					defer proc.finish()
					if _, err := proc.readCommand(); err != nil {
						return
					}
					tt.serve(ctx, proc)
				}()
				return proc, nil
			}
			fileRepo := plan.NewFileRepository(filepath.Dir(detail.Dir))
			var events []plan.Event
			appender := eventAppenderFunc(func(planDir string, event plan.Event) error {
				events = append(events, event)
				return fileRepo.AppendEvent(planDir, event)
			})
			delayCalls := 0
			service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner:       piTransportGitRunner(t, detail, root, func() string { return " M partial.go\n" }),
				ProcessStarter:      starter,
				TransportRetryDelay: func(context.Context, time.Duration) error { delayCalls++; return nil },
				EventAppender:       appender,
				LogAppender:         fileRepo,
			}})

			err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, SessionTimeout: tt.timeout}})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("execute error = %v, want %q", err, tt.wantError)
			}
			if processStarts != 1 || delayCalls != 0 {
				t.Fatalf("sessions=%d delays=%d, want one session and no retry delay", processStarts, delayCalls)
			}
			timeoutEvents := 0
			for _, event := range events {
				if event.Type == plan.EventTypeSessionTimeout {
					timeoutEvents++
				}
			}
			if (timeoutEvents == 1) != tt.wantTimeoutEvt {
				t.Fatalf("timeout events = %d, want event=%t", timeoutEvents, tt.wantTimeoutEvt)
			}
		})
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

func piTransportGitRunner(t *testing.T, detail *plan.PlanDetail, root string, status func() string) CommandRunner {
	t.Helper()
	base := interruptedServiceGitRunner(t, root, &[]string{}, status, "tao/plan-a", "base")
	return func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		actualCWD := cwd
		if len(args) >= 2 && args[0] == "-C" {
			actualCWD = args[1]
		}
		if actualCWD == detail.State.Repo.Root && name == "git" {
			switch runGitKey(args) {
			case "status --porcelain", "diff HEAD", "diff --name-only HEAD":
				return nil
			}
		}
		return base(ctx, cwd, name, args, stdout, stderr)
	}
}

func serveSuccessfulPiSession(proc *fakePiProcess, sessionID string) {
	proc.writeEvent(`{"type":"message","role":"assistant","text":"done"}`)
	proc.writeEvent(`{"type":"agent_end","session_id":"` + sessionID + `"}`)
	if _, err := proc.readCommand(); err != nil {
		return
	}
	proc.writeEvent(`{"type":"state","session_id":"` + sessionID + `"}`)
	if _, err := proc.readCommand(); err != nil {
		return
	}
	proc.writeEvent(`{"type":"session_stats","session_id":"` + sessionID + `"}`)
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
	waited       bool
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
	p.waited = true
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
