package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

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

func TestGeneratePullRequestBodySessionCarriesNativeAcceptanceContract(t *testing.T) {
	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{FinalText: "body"}}
	body, err := generatePullRequestBodyWithAgentSession(context.Background(), executor, agentOperationOptions{}, PullRequestBodyRun{
		PlanDir: "/plans/a", PlanID: "plan-a", RepoRoot: "/repo", Title: "feat(pr): native format", DraftBody: "## Problem\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body != "body" || len(executor.requests) != 1 {
		t.Fatalf("body/session = %q/%d, want body/1", body, len(executor.requests))
	}
	prompt := executor.requests[0].Prompt
	for _, want := range []string{"Problem, Fix, Tests, Deploy, Scope", "Use only ## ATX syntax for level-two headings; do not add Setext headings", "Keep Tests exactly as drafted", "legitimate repository paths that contain the word Tao", "complete collapsed Changed files details block containing the exact diff stat", "paths that happen to contain the word Tao", "omits Tao lifecycle verification commands", "Do not include plan IDs", "Tao-specific prose in Problem, Fix, Tests, or Deploy", "merge guidance", "Do not add claims"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("body session prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestTimestampedAgentLogWriterAddsRecordTime(t *testing.T) {
	at := time.Date(2026, 8, 22, 12, 34, 56, 789, time.UTC)
	var output bytes.Buffer
	writer := timestampedAgentLogWriter{writer: &output, clock: func() time.Time { return at }}
	var input bytes.Buffer
	if err := logrecord.Write(&input, logrecord.Record{Type: logrecord.TypeAssistant, Content: "working"}); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write(input.Bytes()); err != nil || n != input.Len() {
		t.Fatalf("timestamped write bytes=%d error=%v", n, err)
	}
	record, ok := logrecord.Parse(strings.TrimSuffix(output.String(), "\n"))
	if !ok || record.Timestamp != at.Format(time.RFC3339Nano) || record.Content != "working" {
		t.Fatalf("timestamped record = %#v, parsed=%t", record, ok)
	}
}

func TestRunAgentSessionInvokesProviderExactlyOnce(t *testing.T) {
	calls := 0
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		calls++
		return agent.SessionResult{Output: "done"}, nil
	})
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, nil, io.Discard, time.Now())

	if _, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a",
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestRunAgentSessionCarriesVerificationCommandsToNeutralRuntime(t *testing.T) {
	want := []string{"cd packages/commonStudent && pnpm test"}
	var got agent.Session
	runtime := agentRuntimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
		got = session
		return agent.SessionResult{Output: "done"}, nil
	})
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, nil, io.Discard, time.Now())

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", VerificationCommands: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.VerificationCommands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("neutral session verification commands = %#v, want %#v", got.VerificationCommands, want)
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

func TestRunAgentSessionSliceBudgetCaps(t *testing.T) {
	tests := []struct {
		name       string
		outputCap  string
		costCap    string
		metrics    agent.Metrics
		wantMetric string
		wantValue  float64
	}{
		{name: "caps unset", metrics: agent.Metrics{OutputTokens: 200, Cost: 3}},
		{name: "output token cap", outputCap: "100", metrics: agent.Metrics{OutputTokens: 101}, wantMetric: "output_tokens", wantValue: 101},
		{name: "cost cap", costCap: "2.5", metrics: agent.Metrics{Cost: 2.75}, wantMetric: "cost", wantValue: 2.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(runtimeconfig.EnvMaxSliceOutputTokens, tt.outputCap)
			t.Setenv(runtimeconfig.EnvMaxSliceCost, tt.costCap)
			runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
				metrics := tt.metrics
				return agent.SessionResult{Output: "partial", Metrics: &metrics}, nil
			})
			repository := plan.NewFileRepository("")
			runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, repository, io.Discard, time.Now())

			got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
				PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", Metrics: &AgentSessionMetricsRequest{SliceID: "001-a"},
			})
			if got.Output != "partial" {
				t.Fatalf("output = %q, want partial", got.Output)
			}
			var budgetErr *budgetExceededError
			if (errors.As(err, &budgetErr)) != (tt.wantMetric != "") {
				t.Fatalf("error = %v, want budget exceeded=%t", err, tt.wantMetric != "")
			}
			detail, loadErr := plan.NewFileRepository(filepath.Dir(planDir)).GetPlan(context.Background(), filepath.Base(planDir))
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			var budgetEvent *plan.Event
			for i := range detail.Events {
				if detail.Events[i].Type == plan.EventTypeBudgetExceeded {
					budgetEvent = &detail.Events[i]
				}
			}
			if tt.wantMetric == "" {
				if budgetEvent != nil {
					t.Fatalf("unexpected budget event: %+v", *budgetEvent)
				}
				return
			}
			if budgetEvent == nil || budgetEvent.Metric != tt.wantMetric || budgetEvent.Observed == nil || *budgetEvent.Observed != tt.wantValue {
				t.Fatalf("budget event = %+v, want metric=%s observed=%g", budgetEvent, tt.wantMetric, tt.wantValue)
			}
		})
	}
}

func TestRunAgentSessionSliceBudgetAccumulatesPriorMetrics(t *testing.T) {
	t.Setenv(runtimeconfig.EnvMaxSliceOutputTokens, "100")
	t.Setenv(runtimeconfig.EnvMaxSliceCost, "")
	repository := plan.NewFileRepository("")
	current := agent.Metrics{SessionID: "current", OutputTokens: 41}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{Metrics: &current}, nil
	})
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, repository, io.Discard, time.Now())
	prior := plan.AgentMetrics{SessionID: "prior", OutputTokens: 60}
	if err := repository.AppendEvent(planDir, plan.Event{Type: plan.EventTypeAgentMetrics, Timestamp: time.Now().UTC(), PlanID: "plan-a", SliceID: "001-a", Metrics: &prior, Message: "prior"}); err != nil {
		t.Fatal(err)
	}

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", Metrics: &AgentSessionMetricsRequest{SliceID: "001-a"},
	})
	var budgetErr *budgetExceededError
	if !errors.As(err, &budgetErr) || budgetErr.observed != 101 {
		t.Fatalf("error = %#v, want cumulative output token observation 101", err)
	}
}

func TestSliceBudgetExceededBlocksCompletedSliceAndGuardsContinuation(t *testing.T) {
	plansRoot := t.TempDir()
	planDir := filepath.Join(plansRoot, "plan-a")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	persistRunArtifacts(t, planDir, detail)

	repository := plan.NewFileRepository(plansRoot)
	agentCalls := 0
	budgetErr := &budgetExceededError{metric: "output_tokens", threshold: 100, observed: 101}
	executor := sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
		agentCalls++
		active, err := repository.GetPlan(ctx, "plan-a")
		if err != nil {
			return err
		}
		record, err := repository.PlanRecord(active)
		if err != nil {
			return err
		}
		completedAt := time.Now().UTC()
		if err := record.RecordSliceCommitIntent(run.SliceID, plan.SliceCommitIntent{Hash: "intent", Policy: "slice", Message: "test completion", CreatedAt: completedAt}); err != nil {
			return err
		}
		if err := record.CompleteSliceWithOutcome(run.SliceID, "completed before metrics arrived", nil, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "commit-a"}, completedAt); err != nil {
			return err
		}
		return budgetErr
	})
	options := Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent}},
		RunDependencies: RunDependencies{SliceExecutor: executor, CommandRunner: runGitFake(&[]string{}, nil)},
	}
	service := NewService(repository, io.Discard, options)
	err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: options.ResolvedRunOptions})
	if err == nil {
		t.Fatal("run error = nil, want blocked telemetry-cap recovery guidance")
	}
	for _, want := range []string{
		"Blocked slice 001-a: slice agent metrics output_tokens cap exceeded: observed 101, threshold 100",
		"Resolve this blocker before continuing, then run:\n  tao run --continue plan-a",
		"Agent log:",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("run error = %v, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "may be resumed with --continue") {
		t.Fatalf("run error retains retry-first guidance: %v", err)
	}
	if !errors.Is(err, budgetErr) {
		t.Fatalf("run error = %v, want original agent budget error", err)
	}

	blocked, err := repository.GetPlan(context.Background(), "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	blockedSlice := blocked.Slices.Slices[0]
	if blocked.State.Status != plan.StatusBlocked || blockedSlice.Status != plan.StatusBlocked || blocked.State.Plan.CurrentSlice == nil || *blocked.State.Plan.CurrentSlice != "001-a" {
		t.Fatalf("persisted blocked lifecycle = state=%+v slice=%+v", blocked.State, blockedSlice)
	}
	if slices.Contains(blocked.State.Plan.CompletedSlices, "001-a") || !slices.Contains(blocked.State.Plan.PendingSlices, "001-a") {
		t.Fatalf("persisted queues = completed %v pending %v", blocked.State.Plan.CompletedSlices, blocked.State.Plan.PendingSlices)
	}
	if blockedSlice.CommitIntent == nil || blockedSlice.Completion == nil {
		t.Fatalf("completion recovery evidence was discarded: %+v", blockedSlice)
	}

	continueOptions := options
	continueOptions.Continue = true
	continueService := NewService(repository, io.Discard, continueOptions)
	err = continueService.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: continueOptions.ResolvedRunOptions})
	if err == nil || !strings.Contains(err.Error(), "interrupted post-intent completion transaction") {
		t.Fatalf("continue error = %v, want guarded completion recovery", err)
	}
	if agentCalls != 1 {
		t.Fatalf("agent calls = %d, want continuation not to rerun completed agent", agentCalls)
	}
}

func TestSliceBudgetExceededUsesInterruptedResumeBoundary(t *testing.T) {
	input := interruptedInput()
	before := input.Detail.Slices.Slices[0]
	input.Detail.State.Plan.ID = "plan-a"
	input.Detail.Slices.PlanID = "plan-a"
	input.Detail.Dir = t.TempDir()
	persistRunArtifacts(t, input.Detail.Dir, input.Detail)
	record, err := plan.NewPlanRecord(input.Detail.Dir, input.Detail)
	if err != nil {
		t.Fatal(err)
	}
	budgetErr := &budgetExceededError{metric: "output_tokens", threshold: 100, observed: 101}
	if err := record.BlockSliceForBudget(input.SliceID, budgetErr.Error(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := plan.NewFileRepository(filepath.Dir(input.Detail.Dir)).GetPlan(context.Background(), filepath.Base(input.Detail.Dir))
	if err != nil {
		t.Fatal(err)
	}
	input.Detail = reloaded
	input.ContinueBlocked = true

	got := ClassifyInterruptedSlice(input)
	if got.Disposition != InterruptedSliceBlockedContinue || got.ContinuationDisposition != InterruptedSliceResume {
		t.Fatalf("budget stop recovery = %#v, want blocked continuation through ordinary interrupted resume", got)
	}
	after := input.Detail.Slices.Slices[0]
	if before.ExecutionRoot != after.ExecutionRoot || before.ExecutionStart == nil || after.ExecutionStart == nil || *before.ExecutionStart != *after.ExecutionStart {
		t.Fatalf("recovery boundary changed: before=%+v after=%+v", before, after)
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
	runner := newAgentSessionRunner(agentSessionRunnerConfig{
		descriptor: agent.Descriptor{
			Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime },
		},
		logAppender:   plan.NewFileRepository(""),
		eventAppender: eventAppenderFunc(func(string, plan.Event) error { appendCalls++; return nil }),
		now:           time.Now,
	})

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
	return newAgentSessionRunner(agentSessionRunnerConfig{
		descriptor: agent.Descriptor{
			Label: "test", MetricsMessage: "captured test metrics",
			NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime },
		},
		logAppender:      plan.NewFileRepository(""),
		eventAppender:    appender,
		sessionLogWriter: logWriter,
		now:              func() time.Time { return timestamp },
	}), detail.Dir, repoRoot
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
	if err := record.PersistArtifacts(); err != nil {
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
