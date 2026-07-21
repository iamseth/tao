package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
)

func TestBatchAgentSessionHonorsConfiguredProviderPermissionsAndRoot(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	t.Setenv("TAO_DANGEROUSLY_SKIP_PERMISSIONS", "true")
	t.Setenv("TAO_SESSION_TIMEOUT", "30s")
	var got fakeClaudeStart
	var metricsCalled bool
	session, err := NewBatchAgentSession(BatchAgentSessionConfig{ProcessStarter: fakeProcessStarter(t, &got, `{"type":"result","result":"resolved"}`), Metrics: func(_ agent.Metrics, _ string) { metricsCalled = true }})
	if err != nil {
		t.Fatal(err)
	}
	text, err := session.Resolve(context.Background(), "/integration", "repair")
	if err != nil {
		t.Fatal(err)
	}
	if text != "resolved" || got.name != "claude" || got.cwd != "/integration" || got.prompt != "repair" {
		t.Fatalf("unexpected batch session: text=%q start=%#v", text, got)
	}
	if !strings.Contains(strings.Join(got.args, " "), "--permission-mode bypassPermissions") {
		t.Fatalf("batch permission was not propagated: %v", got.args)
	}
	if !metricsCalled {
		t.Fatal("best-effort metrics callback was not invoked")
	}
}

func TestRunSliceWithAgentSessionCarriesInterruptedResumePrompt(t *testing.T) {
	executor := &recordingAgentSessionExecutor{}
	err := runSliceWithAgentSession(context.Background(), executor, agentOperationOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated}, SliceRun{
		PlanDir: "/plans/a", SliceID: "001-a", RepoRoot: "/repo", Resuming: true, ResumeAttempt: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(executor.requests))
	}
	prompt := executor.requests[0].Prompt
	for _, want := range []string{"This is resume attempt 2", "staged, unstaged, and untracked", "Never run `git commit` manually"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("resume session prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRunPathAgentSessionTimeoutIsClassified(t *testing.T) {
	const sessionTimeout = time.Millisecond
	repoRoot := t.TempDir()
	detail := runPathSessionDetail(t, repoRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)

	runtime := agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
		if session.Timeout != sessionTimeout {
			t.Fatalf("session timeout = %s, want %s", session.Timeout, sessionTimeout)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected timeout decorator to set a run-path deadline")
		}
		<-ctx.Done()
		return agent.SessionResult{Output: "partial"}, ctx.Err()
	})
	execution := timeoutTestRunExecution(sessionTimeout, runtime, repoRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := executeDetailWithExecution(ctx, detail, nil, io.Discard, execution)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var timeoutErr *agent.SessionTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error = %T %v, want SessionTimeoutError", err, err)
	}
	if timeoutErr.Timeout != sessionTimeout {
		t.Fatalf("timeout error duration = %s, want %s", timeoutErr.Timeout, sessionTimeout)
	}
	if !strings.Contains(err.Error(), "agent session timed out after "+sessionTimeout.String()) {
		t.Fatalf("expected classified timeout message, got %v", err)
	}
}

func TestRunPathAgentSessionTimeoutLeavesFastSessionUnaffected(t *testing.T) {
	const sessionTimeout = time.Hour
	repoRoot := t.TempDir()
	detail := runPathSessionDetail(t, repoRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
	completed := runPathSessionDetail(t, repoRoot, plan.StatusCompleted, nil, []string{"001-a"}, plan.StatusCompleted)
	completed.Dir = detail.Dir

	called := false
	runtime := agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
		called = true
		if session.Timeout != sessionTimeout {
			t.Fatalf("session timeout = %s, want %s", session.Timeout, sessionTimeout)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected timeout decorator to set a run-path deadline")
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("fast session context error before return: %v", err)
		}
		return agent.SessionResult{Output: "done"}, nil
	})
	execution := timeoutTestRunExecution(sessionTimeout, runtime, repoRoot)

	err := executeDetailWithExecution(context.Background(), detail, func(context.Context, *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completed, nil
	}, io.Discard, execution)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected runtime to be called")
	}
}

func TestRunAgentSessionAppendsSessionTimeoutEvent(t *testing.T) {
	const sessionTimeout = 95 * time.Second
	timedOutAt := time.Date(2026, 7, 14, 2, 30, 0, 0, time.FixedZone("test", -5*60*60))
	timeoutErr := &agent.SessionTimeoutError{Timeout: sessionTimeout}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{}, timeoutErr
	})
	var events []plan.Event
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, eventAppenderFunc(func(_ string, event plan.Event) error {
		events = append(events, event)
		return nil
	}), io.Discard, timedOutAt)

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir:   planDir,
		RepoRoot:  repoRoot,
		LogAction: "running 001-a",
		Metrics:   &AgentSessionMetricsRequest{SliceID: "001-a"},
	})
	if !errors.Is(err, timeoutErr) {
		t.Fatalf("error = %v, want original timeout error", err)
	}
	if len(events) == 0 {
		t.Fatal("expected session_timeout event")
	}
	event := events[0]
	if event.Type != plan.EventTypeSessionTimeout || event.PlanID != "plan-a" || event.SliceID != "001-a" || event.Agent != "test" {
		t.Fatalf("session timeout event identity = %+v", event)
	}
	if event.DurationSeconds == nil || *event.DurationSeconds != 95 {
		t.Fatalf("duration_seconds = %v, want 95", event.DurationSeconds)
	}
	if !event.Timestamp.Equal(timedOutAt.UTC()) {
		t.Fatalf("timestamp = %s, want %s", event.Timestamp, timedOutAt.UTC())
	}
	if !strings.Contains(event.Message, "test") || !strings.Contains(event.Message, sessionTimeout.String()) {
		t.Fatalf("message = %q, want agent and timeout", event.Message)
	}
}

func TestRunAgentSessionTimeoutAppendFailurePreservesResult(t *testing.T) {
	timeoutErr := &agent.SessionTimeoutError{Timeout: 2 * time.Minute}
	want := AgentSessionResult{Output: "partial output", FinalText: "partial final text"}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{Output: want.Output, FinalText: want.FinalText}, timeoutErr
	})
	var log bytes.Buffer
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, eventAppenderFunc(func(string, plan.Event) error {
		return errors.New("journal unavailable")
	}), &log, time.Now())

	got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, LogAction: "reviewing plan"})
	if got != want || !errors.Is(err, timeoutErr) {
		t.Fatalf("result, error = %+v, %v; want %+v, original timeout error", got, err, want)
	}
	if !strings.Contains(log.String(), "tao telemetry warning: append session timeout event: journal unavailable") {
		t.Fatalf("session log missing append warning: %q", log.String())
	}
}

func TestRunAgentSessionNonTimeoutErrorSkipsSessionTimeoutEvent(t *testing.T) {
	runErr := errors.New("agent failed")
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{}, runErr
	})
	appendCalls := 0
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, eventAppenderFunc(func(string, plan.Event) error {
		appendCalls++
		return nil
	}), io.Discard, time.Now())

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running"})
	if !errors.Is(err, runErr) {
		t.Fatalf("error = %v, want original run error", err)
	}
	if appendCalls != 0 {
		t.Fatalf("event append calls = %d, want 0", appendCalls)
	}
}

func TestRunAgentSessionStateReadErrorSkipsSessionTimeoutEvent(t *testing.T) {
	timeoutErr := &agent.SessionTimeoutError{Timeout: time.Minute}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{}, timeoutErr
	})
	appendCalls := 0
	planDir := t.TempDir()
	runner := agentSessionRunner{
		runtime:       runtime,
		agentLabel:    "test",
		logAppender:   plan.NewFileRepository(""),
		eventAppender: eventAppenderFunc(func(string, plan.Event) error { appendCalls++; return nil }),
		nowFn:         time.Now,
	}

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, LogAction: "running"})
	if !errors.Is(err, timeoutErr) {
		t.Fatalf("error = %v, want original timeout error", err)
	}
	if appendCalls != 0 {
		t.Fatalf("event append calls = %d, want 0", appendCalls)
	}
}

func sessionEventTestRunner(t *testing.T, runtime agent.Runtime, appender plan.EventAppender, logWriter io.Writer, timestamp time.Time) (agentSessionRunner, string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	detail := runPathSessionDetail(t, repoRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
	return agentSessionRunner{
		runtime:          runtime,
		agentLabel:       "test",
		metricsMessage:   "captured test metrics",
		logAppender:      plan.NewFileRepository(""),
		eventAppender:    appender,
		sessionLogWriter: logWriter,
		nowFn:            func() time.Time { return timestamp },
	}, detail.Dir, repoRoot
}

func runPathSessionDetail(t *testing.T, repoRoot string, status string, pending []string, completed []string, sliceStatus string) *plan.PlanDetail {
	t.Helper()
	detail := runPlanDetail(status, pending, completed, "001-a", sliceStatus, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err != nil {
		t.Fatal(err)
	}
	return detail
}

func timeoutTestRunExecution(sessionTimeout time.Duration, runtime agent.Runtime, repoRoot string) runExecution {
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, SessionTimeout: sessionTimeout}}, RunDependencies{
		AgentFactory: func(execution runExecution) agentRunCapabilities {
			descriptor := agent.Descriptor{Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }}
			executor := newAgentExecutor(descriptor, execution.Config, execution.Dependencies, execution.StartingBranch, execution.StartingDirtyPaths)
			return agentRunCapabilities{sliceExecutor: executor, pullRequestBodyGenerator: executor}
		},
		LogAppender:       plan.NewFileRepository(""),
		PlanRecordFactory: memoryPlanRecordFactory,
	})
	execution.ExecutionRoot = repoRoot
	return execution
}

type agentRuntimeFunc func(context.Context, agent.Session) (agent.SessionResult, error)

func (f agentRuntimeFunc) RunSession(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
	return f(ctx, session)
}
