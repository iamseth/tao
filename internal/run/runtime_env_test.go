package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func runEnvSnapshot(values map[string]string) *runtimeconfig.EnvSnapshot {
	snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	return &snapshot
}

func TestInvalidSessionCapsRejectBeforeLaunch(t *testing.T) {
	for _, key := range []string{runtimeconfig.EnvMaxSliceOutputTokensDeprecated, runtimeconfig.EnvMaxSliceCostDeprecated} {
		t.Run(key, func(t *testing.T) {
			runner := agentSessionRunner{runtimeEnv: *runEnvSnapshot(map[string]string{key: "invalid"})}
			// No collaborators are available: rejection must precede log, provider, and event work.
			_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleRework, EnforceSliceCaps: true}})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("cap admitted: %v", err)
			}
		})
	}
}

func TestSessionsIgnoreUnusedInvalidBudgets(t *testing.T) {
	for _, metrics := range []*AgentSessionMetricsRequest{
		nil,
		{Role: plan.AgentRoleReview},
		{SliceID: "001-a", Role: plan.AgentRoleReview, EnforceSliceCaps: true},
		{Role: plan.AgentRoleExecution, EnforceSliceCaps: true},
		{SliceID: "001-a", Role: plan.AgentRoleExecution},
	} {
		calls := 0
		runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
			calls++
			return agent.SessionResult{Metrics: &agent.Metrics{OutputTokens: 100}}, nil
		})
		runner, dir, root := sessionEventTestRunner(t, runtime, plan.NewFileRepository(""), io.Discard, time.Now())
		runner.runtimeEnv = *runEnvSnapshot(map[string]string{runtimeconfig.EnvMaxSliceCostDeprecated: "invalid", runtimeconfig.EnvMaxSliceOutputTokensDeprecated: "invalid", runtimeconfig.EnvBudgetPlanCostDeprecated: "invalid"})
		_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: dir, RepoRoot: root, Metrics: metrics})
		if err != nil || calls != 1 {
			t.Fatalf("unused budgets blocked session: calls=%d err=%v", calls, err)
		}
	}
}

func TestInjectedSessionCapsStableAcrossHandoffs(t *testing.T) {
	for _, tc := range []struct {
		key, warnKey, value, metric string
		metrics                     agent.Metrics
		threshold                   float64
	}{
		{runtimeconfig.EnvMaxSliceOutputTokensDeprecated, runtimeconfig.EnvBudgetSliceOutputTokensWarn, "0", "output_tokens", agent.Metrics{OutputTokens: 1}, 0},
		{runtimeconfig.EnvMaxSliceCostDeprecated, runtimeconfig.EnvBudgetSliceCostWarn, "2.5", "cost", agent.Metrics{Cost: 3}, 2.5},
	} {
		t.Run(tc.metric, func(t *testing.T) {
			// The stop cap may not sit below its warn threshold, so the warn key
			// follows the cap; Budget() admits every budget key together, so only
			// non-budget settings may be invalid here.
			snapshot := runEnvSnapshot(map[string]string{tc.key: tc.value, tc.warnKey: tc.value, runtimeconfig.EnvAgent: "invalid", runtimeconfig.EnvPlannerRoutingArms: "invalid"})
			t.Setenv(tc.key, "99999")
			calls, sessions := 0, 0
			descriptor := agent.Descriptor{Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime {
				sessions++
				return agentRuntimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
					calls++
					if session.Timeout != time.Minute {
						t.Fatalf("timeout changed: %v", session.Timeout)
					}
					metrics := tc.metrics
					return agent.SessionResult{Metrics: &metrics}, nil
				})
			}}
			detail := runPathSessionDetail(t, t.TempDir(), plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
			repository := plan.NewFileRepository("")
			config, err := prepareRequestConfig(ExecutionConfig{RuntimeEnv: snapshot}, Request{ResolvedRunOptions: ResolvedRunOptions{SessionTimeout: time.Minute}})
			if err != nil {
				t.Fatal(err)
			}
			executor := newAgentExecutor(descriptor, config, RunDependencies{LogAppender: repository, EventAppender: repository}, "", nil)
			for range 2 {
				_, err := executor.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: detail.State.Repo.Root, Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true}})
				var budgetErr *budgetExceededError
				if !errors.As(err, &budgetErr) || budgetErr.metric != tc.metric || budgetErr.threshold != tc.threshold {
					t.Fatalf("injected cap lost: %v", err)
				}
			}
			if calls != 2 || sessions != 2 {
				t.Fatalf("unexpected retry/session behavior: calls=%d sessions=%d", calls, sessions)
			}
			state, err := plan.ReadState(detail.Dir)
			if err != nil || state.Status != plan.StatusPlanned {
				t.Fatalf("metrics granted lifecycle authority: %+v, %v", state, err)
			}
		})
	}
}

func TestInvalidSliceBudgetsRejectBeforeWorkspace(t *testing.T) {
	for _, key := range []string{runtimeconfig.EnvMaxSliceCostDeprecated, runtimeconfig.EnvBudgetSliceToolCallsDeprecated, runtimeconfig.EnvBudgetPlanCostDeprecated} {
		t.Run(key, func(t *testing.T) {
			snapshot := runEnvSnapshot(map[string]string{key: "invalid"})
			detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
			service := NewService(nil, io.Discard, Options{})
			_, err := service.prepareRunExecution(context.Background(), detail, ExecutionConfig{RuntimeEnv: snapshot})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("budget reached workspace dependencies: %v", err)
			}
			_, err = createReviewWithAgentSession(context.Background(), nil, agentOperationOptions{RuntimeEnv: runEnvSnapshot(map[string]string{runtimeconfig.EnvBudgetPlanCostDeprecated: "invalid"})}, ReviewRun{}, nil)
			if err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvBudgetPlanCostDeprecated) {
				t.Fatalf("review threshold reached session: %v", err)
			}
		})
	}
}

func TestServiceExecuteInvalidBudgetsPreserveVerificationRepair(t *testing.T) {
	for _, key := range []string{
		runtimeconfig.EnvMaxSliceOutputTokensDeprecated, runtimeconfig.EnvMaxSliceCostDeprecated,
		runtimeconfig.EnvBudgetSliceOutputTokensDeprecated, runtimeconfig.EnvBudgetSliceCostDeprecated,
		runtimeconfig.EnvBudgetSliceToolCallsDeprecated, runtimeconfig.EnvBudgetSliceAssistantMessagesDeprecated,
		runtimeconfig.EnvBudgetSliceErroredMessagesDeprecated, runtimeconfig.EnvBudgetPlanOutputTokensDeprecated,
		runtimeconfig.EnvBudgetPlanCostDeprecated, runtimeconfig.EnvBudgetPlanToolCallsDeprecated,
		runtimeconfig.EnvBudgetPlanAssistantMessagesDeprecated, runtimeconfig.EnvBudgetPlanErroredMessagesDeprecated,
	} {
		t.Run(key, func(t *testing.T) {
			root, planDir := t.TempDir(), t.TempDir()
			detail := completedReviewPlanDetail(planDir)
			detail.State.Status = plan.StatusVerificationFailed
			detail.State.Repo.Root = root
			detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: root, Branch: "feature", HeadSHA: "head123"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{
				Command: "make verify", HeadSHA: "head123", Result: finalVerificationFailed,
				FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "initial-failure",
			}
			persistRunArtifacts(t, planDir, detail)
			repo := plan.NewFileRepository("")
			before, err := repo.ResolvePlan(context.Background(), planDir)
			if err != nil {
				t.Fatal(err)
			}
			request := Request{Input: planDir, RepairVerification: true, ResolvedRunOptions: ResolvedRunOptions{
				CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated,
			}}
			if err := CheckRequestCanStart(before, request); err != nil {
				t.Fatalf("fixture must admit explicit repair: %v", err)
			}
			artifacts := make(map[string][]byte)
			for _, name := range []string{"state.json", "slices.json"} {
				contents, err := os.ReadFile(filepath.Join(planDir, name)) // #nosec G304 -- fixed artifact names in a test-owned temporary directory.
				if err != nil {
					t.Fatal(err)
				}
				artifacts[name] = contents
			}
			providerCalls := 0
			service := NewService(repo, io.Discard, Options{
				ExecutionConfig: ExecutionConfig{RuntimeEnv: runEnvSnapshot(map[string]string{key: "invalid"})},
				RunDependencies: RunDependencies{
					CommandRunner: newScriptedGitRunner("").Run,
					AgentFactory: func(runExecution) agentRunCapabilities {
						providerCalls++
						t.Fatal("invalid budgets reached provider setup")
						return agentRunCapabilities{}
					},
				},
			})
			// Retrying the rejected invocation must retain the original recovery boundary.
			for range 2 {
				err := service.Execute(context.Background(), request)
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Errorf("expected named budget rejection, got %v", err)
				}
				for name, original := range artifacts {
					contents, err := os.ReadFile(filepath.Join(planDir, name)) // #nosec G304 -- fixed artifact names in a test-owned temporary directory.
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(original, contents) {
						t.Errorf("budget rejection changed %s", name)
					}
				}
				after, err := repo.ResolvePlan(context.Background(), planDir)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := plan.VerificationRepairAttemptCount(after), plan.VerificationRepairAttemptCount(before); got != want {
					t.Errorf("repair attempts = %d, want %d", got, want)
				}
				for _, eventType := range []string{plan.EventTypeVerificationRepairCreated, plan.EventTypeVerificationRepairStopped} {
					if got, want := countPlanEvents(after.Events, eventType), countPlanEvents(before.Events, eventType); got != want {
						t.Errorf("%s events = %d, want %d", eventType, got, want)
					}
				}
				if providerCalls != 0 {
					t.Errorf("provider calls = %d, want none", providerCalls)
				}
			}
		})
	}
}

func TestNonSlicePreparationIgnoresInvalidBudgets(t *testing.T) {
	for _, reverify := range []bool{true, false} {
		name := "pull request recovery"
		if reverify {
			name = "verification only"
		}
		t.Run(name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Repo.Root = t.TempDir()
			detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: detail.State.Repo.Root, Branch: "feature", HeadSHA: "head123"}
			config := ExecutionConfig{
				RuntimeEnv: runEnvSnapshot(map[string]string{
					runtimeconfig.EnvMaxSliceCostDeprecated: "invalid", runtimeconfig.EnvMaxSliceOutputTokensDeprecated: "invalid",
					runtimeconfig.EnvBudgetSliceToolCallsDeprecated: "invalid", runtimeconfig.EnvBudgetPlanCostDeprecated: "invalid",
				}),
				ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, PullRequest: true},
				Reverify:           reverify,
			}
			service := NewService(plan.NewFileRepository(""), io.Discard, Options{})
			if _, err := service.prepareRunExecution(context.Background(), detail, config); err != nil {
				t.Fatalf("unused budgets blocked non-slice preparation: %v", err)
			}
		})
	}
}

func TestSnapshotThresholdsReachPacketWarningsAndReview(t *testing.T) {
	// Budget() admits every budget key together, so only non-budget settings may be invalid here.
	snapshot := runEnvSnapshot(map[string]string{runtimeconfig.EnvBudgetSliceOutputTokensDeprecated: "7", runtimeconfig.EnvPlannerRoutingArms: "invalid", runtimeconfig.EnvAgent: "invalid"})
	t.Setenv(runtimeconfig.EnvBudgetSliceOutputTokensDeprecated, "99999")
	detail := runPathSessionDetail(t, t.TempDir(), plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
	detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, SliceID: "001-a", Metrics: &plan.AgentMetrics{OutputTokens: 8}}}
	detail.Slices.Slices[0].Verification.Commands = []string{"go version"}
	var out bytes.Buffer
	runner := SelectedSliceRunner{out: &out, execution: newRunExecution(ExecutionConfig{RuntimeEnv: snapshot}, RunDependencies{})}
	packet, err := runner.renderRunPacket(context.Background(), detail, detail.State.Repo.Root, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.validateSelectedSlice(detail, "001-a", detail.State.Repo.Root); err != nil {
		t.Fatal(err)
	}
	detail.State.Status = plan.StatusInReview
	detail.State.Plan.PendingSlices = nil
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices[0].Status = plan.StatusCompleted
	persistReviewState(t, detail.Dir, detail)
	detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, SliceID: "001-a", Metrics: &plan.AgentMetrics{OutputTokens: 8}}}
	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{Output: "```tao-review-json\n{\"verdict\":\"comment\",\"summary\":\"Inconclusive\",\"findings\":[]}\n```"}}
	_, err = createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{RuntimeEnv: snapshot, CommitPolicy: CommitPolicyNone, reviewGitFactory: fixedReviewGit(&fakeReviewGit{head: "head"})}, ReviewRun{Detail: detail}, fileReviewRecordFactory(plan.NewFileRepository("")))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"packet": packet, "warnings": out.String(), "review": executor.requests[0].Prompt} {
		if !strings.Contains(text, "observed 8 > threshold 7") {
			t.Fatalf("%s missing injected threshold: %s", name, text)
		}
	}
	// Direct callers do not inherit the live process threshold.
	defaults, err := runtimeEnv(nil).Budget()
	if err != nil || defaults.Warn() != plan.DefaultAgentBudgetThresholds() {
		t.Fatalf("direct defaults: %+v, %v", defaults, err)
	}
}

func TestDirectSessionUsesBuiltinBudgets(t *testing.T) {
	t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, "0")
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{Metrics: &agent.Metrics{OutputTokens: 1}}, nil
	})
	runner, dir, root := sessionEventTestRunner(t, runtime, plan.NewFileRepository(""), io.Discard, time.Now())
	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: dir, RepoRoot: root, Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true}})
	if err != nil {
		t.Fatalf("omitted configuration must use built-ins, not process caps: %v", err)
	}
}
