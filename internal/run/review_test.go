package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

func TestAppendPriorReworkAndBudgetContext(t *testing.T) {
	thresholds := plan.DefaultAgentBudgetThresholds()
	tests := []struct {
		name       string
		detail     *plan.PlanDetail
		want       []string
		wantAbsent []string
	}{
		{
			name:       "no history",
			detail:     &plan.PlanDetail{},
			wantAbsent: []string{"Prior Rework and Budget Context"},
		},
		{
			name: "rework rounds",
			detail: &plan.PlanDetail{Events: []plan.Event{
				{Type: plan.EventTypeReworkRound, Round: 1, Fingerprint: "first"},
				{Type: plan.EventTypeReworkRound, Round: 2, Fingerprint: "second"},
			}},
			want: []string{"## Prior Rework and Budget Context", "advisory history", "- Rework rounds: 2", "- Distinct finding fingerprints: 2"},
		},
		{
			name: "stopped reason",
			detail: &plan.PlanDetail{Events: []plan.Event{
				{Type: plan.EventTypeReworkStopped, Reason: "automatic rework stalled", Fingerprint: "same"},
			}},
			want: []string{"- Latest rework stop: automatic rework stalled", "- Distinct finding fingerprints: 1"},
		},
		{
			name: "budget warning",
			detail: &plan.PlanDetail{Events: []plan.Event{
				{Type: plan.EventTypeAgentMetrics, SliceID: "001-a", Metrics: &plan.AgentMetrics{OutputTokens: thresholds.Slice.OutputTokens + 1}},
			}},
			want: []string{"## Prior Rework and Budget Context", "- Budget warning (slice 001-a): output_tokens observed 40001 > threshold 40000"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendPriorReworkAndBudgetContext("review prompt\n", tt.detail, thresholds)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("context missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Fatalf("context unexpectedly contains %q:\n%s", absent, got)
				}
			}
		})
	}
}

func TestAppendPriorReworkAndBudgetContextCapsWarnings(t *testing.T) {
	thresholds := plan.DefaultAgentBudgetThresholds()
	events := make([]plan.Event, 100)
	for i := range events {
		events[i] = plan.Event{
			Type:    plan.EventTypeAgentMetrics,
			SliceID: fmt.Sprintf("%03d-slice", i),
			Metrics: &plan.AgentMetrics{OutputTokens: thresholds.Slice.OutputTokens + int64(i) + 1},
		}
	}
	detail := &plan.PlanDetail{Events: events}
	warnings := plan.AgentBudgetWarnings(detail, thresholds)
	if len(warnings) <= maxReviewBudgetWarnings {
		t.Fatalf("test setup produced %d warnings, want more than %d", len(warnings), maxReviewBudgetWarnings)
	}

	prompt := "review prompt\n"
	got := appendPriorReworkAndBudgetContext(prompt, detail, thresholds)
	if count := strings.Count(got, "- Budget warning ("); count != maxReviewBudgetWarnings {
		t.Fatalf("budget warning count = %d, want %d", count, maxReviewBudgetWarnings)
	}
	wantOmitted := fmt.Sprintf("- Additional budget warnings omitted: %d", len(warnings)-maxReviewBudgetWarnings)
	if !strings.Contains(got, wantOmitted) {
		t.Fatalf("context missing %q:\n%s", wantOmitted, got)
	}
	contextBytes := len(got) - len(strings.TrimRight(prompt, "\n"))
	if contextBytes > maxReviewContextBytes {
		t.Fatalf("review context bytes = %d, want at most %d", contextBytes, maxReviewContextBytes)
	}
}

func TestCreateReviewWithAgentSessionPersistsParsedReview(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	reviewedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, &reviewedAt)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.BaseCommit = "base123"
	persistReviewState(t, planDir, detail)

	output := "Review looks focused.\n\n```tao-review-json\n{\n  \"verdict\": \"changes_requested\",\n  \"summary\": \"One finding should be fixed.\",\n  \"findings\": [\n    {\"severity\": \"major\", \"file\": \"internal/run/review.go\", \"line\": 42, \"message\": \"Fix this.\", \"suggestion\": \"Adjust the code.\"}\n  ]\n}\n```\n"
	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{Output: output}}
	calls := []string{}
	store := plan.NewFileRepository("")

	review, err := createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{Agent: "pi", CommandRunner: runGitFake(&calls, nil), Now: func() time.Time { return reviewedAt }}, ReviewRun{PlanDir: planDir, PlanID: "plan-a", Detail: detail, RepoRoot: repoRoot}, fileReviewRecordFactory(store))
	if err != nil {
		t.Fatal(err)
	}

	if review.Verdict != "changes_requested" || review.Summary != "One finding should be fixed." || review.FindingsCount != 1 || review.Base != "base123" || review.Head != "head123" || review.Agent != "pi" || !review.ReviewedAt.Equal(reviewedAt) {
		t.Fatalf("unexpected review: %+v", review)
	}
	wantFinding := plan.ReviewFinding{Severity: "major", File: "internal/run/review.go", Line: 42, Message: "Fix this.", Suggestion: "Adjust the code."}
	if len(review.Findings) != 1 || review.Findings[0] != wantFinding {
		t.Fatalf("unexpected review findings: %+v", review.Findings)
	}
	if len(calls) != 2 || calls[0] != "status --porcelain" || calls[1] != "rev-parse HEAD" {
		t.Fatalf("expected clean check and HEAD detection, got %#v", calls)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("expected one review request, got %#v", executor.requests)
	}
	request := executor.requests[0]
	if request.LogAction != "reviewing plan plan-a" || request.RepoRoot != repoRoot || !request.CaptureOutput {
		t.Fatalf("unexpected review request: %#v", request)
	}
	for _, want := range []string{"Plan ID: `plan-a`", "Plan directory: `" + planDir + "`", "Base: `base123`", "Head: `head123`"} {
		if !strings.Contains(request.Prompt, want) {
			t.Fatalf("review prompt missing %q:\n%s", want, request.Prompt)
		}
	}

	reviewArtifact, err := os.ReadFile(filepath.Join(planDir, plan.ReviewFile)) //nolint:gosec // test reads a t.TempDir-derived artifact.
	if err != nil {
		t.Fatal(err)
	}
	if string(reviewArtifact) != output {
		t.Fatalf("review.md did not preserve agent output:\n%s", string(reviewArtifact))
	}
	gotState, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotState.Plan.Review == nil || gotState.Plan.Review.Verdict != "changes_requested" || gotState.Plan.Review.FindingsCount != 1 || gotState.Plan.Review.Head != "head123" {
		t.Fatalf("review metadata not persisted: %+v", gotState.Plan.Review)
	}
	if len(gotState.Plan.Review.Findings) != 1 || gotState.Plan.Review.Findings[0] != wantFinding {
		t.Fatalf("review findings not persisted: %+v", gotState.Plan.Review.Findings)
	}
	if detail.State.Plan.Review == nil || detail.State.Plan.Review.Summary != "One finding should be fixed." {
		t.Fatalf("detail state not updated: %+v", detail.State.Plan.Review)
	}
	eventData, err := os.ReadFile(filepath.Join(planDir, "events.jsonl")) //nolint:gosec // test reads a t.TempDir-derived artifact.
	if err != nil {
		t.Fatal(err)
	}
	var event plan.Event
	if err := json.Unmarshal(bytes.TrimSpace(eventData), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != plan.EventTypePlanReviewed || event.PlanID != "plan-a" || event.Agent != "pi" || event.Review == nil || event.Review.Summary != "One finding should be fixed." {
		t.Fatalf("unexpected review event: %+v", event)
	}
	if len(event.Review.Findings) != 1 || event.Review.Findings[0] != wantFinding {
		t.Fatalf("unexpected review event findings: %+v", event.Review.Findings)
	}
}

func TestCreateReviewWithAgentSessionUsesWorkspaceBaseSHA(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	reviewedAt := time.Date(2026, 6, 28, 12, 30, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, &reviewedAt)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.BaseCommit = "plan-creation-base"
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, BaseSHA: "fork-point-base"}
	persistReviewState(t, planDir, detail)

	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{Output: "Looks good."}}
	calls := []string{}
	store := plan.NewFileRepository("")

	review, err := createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{Agent: "pi", CommandRunner: runGitFake(&calls, nil), Now: func() time.Time { return reviewedAt }}, ReviewRun{PlanDir: planDir, PlanID: "plan-a", Detail: detail, RepoRoot: repoRoot, Base: detail.State.Repo.BaseCommit}, fileReviewRecordFactory(store))
	if err != nil {
		t.Fatal(err)
	}

	if review.Base != "fork-point-base" {
		t.Fatalf("expected review base to use workspace fork point, got %+v", review)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("expected one review request, got %#v", executor.requests)
	}
	prompt := executor.requests[0].Prompt
	if !strings.Contains(prompt, "Base: `fork-point-base`") || strings.Contains(prompt, "Base: `plan-creation-base`") {
		t.Fatalf("review prompt did not use workspace fork point:\n%s", prompt)
	}
	gotState, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotState.Plan.Review == nil || gotState.Plan.Review.Base != "fork-point-base" {
		t.Fatalf("persisted review did not use workspace fork point: %+v", gotState.Plan.Review)
	}
}

func TestCreateReviewWithAgentSessionPrefersLiveMergeBaseAfterRebase(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	reviewedAt := time.Date(2026, 7, 2, 13, 0, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, &reviewedAt)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.BaseCommit = "plan-creation-base"
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Branch: "tao/plan-a", BaseBranch: "main", BaseSHA: "stale-fork-base"}
	persistReviewState(t, planDir, detail)

	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{Output: "Looks good."}}
	calls := []string{}
	runner := reviewGitFake(&calls, map[string]string{"merge-base main tao/plan-a": "live-merge-base"})
	store := plan.NewFileRepository("")

	review, err := createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{Agent: "pi", CommandRunner: runner, Now: func() time.Time { return reviewedAt }}, ReviewRun{PlanDir: planDir, PlanID: "plan-a", Detail: detail, RepoRoot: repoRoot, Base: detail.State.Repo.BaseCommit}, fileReviewRecordFactory(store))
	if err != nil {
		t.Fatal(err)
	}

	if review.Base != "live-merge-base" {
		t.Fatalf("expected review base to use live merge-base, got %+v", review)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("expected one review request, got %#v", executor.requests)
	}
	prompt := executor.requests[0].Prompt
	if !strings.Contains(prompt, "Base: `live-merge-base`") || strings.Contains(prompt, "stale-fork-base") {
		t.Fatalf("review prompt did not use live merge-base:\n%s", prompt)
	}
	gotState, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotState.Plan.Review == nil || gotState.Plan.Review.Base != "live-merge-base" {
		t.Fatalf("persisted review did not use live merge-base: %+v", gotState.Plan.Review)
	}
}

func TestCreateReviewWithAgentSessionFallsBackToRecordedBaseWhenMergeBaseFails(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	reviewedAt := time.Date(2026, 7, 2, 13, 15, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, &reviewedAt)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Branch: "tao/plan-a", BaseBranch: "main", BaseSHA: "fork-point-base"}
	persistReviewState(t, planDir, detail)

	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{Output: "Looks good."}}
	calls := []string{}
	store := plan.NewFileRepository("")

	review, err := createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{Agent: "pi", CommandRunner: reviewGitFake(&calls, nil), Now: func() time.Time { return reviewedAt }}, ReviewRun{PlanDir: planDir, PlanID: "plan-a", Detail: detail, RepoRoot: repoRoot}, fileReviewRecordFactory(store))
	if err != nil {
		t.Fatal(err)
	}

	if review.Base != "fork-point-base" {
		t.Fatalf("expected fallback to recorded workspace base, got %+v", review)
	}
}

func TestCreateReviewWithAgentSessionPreservesArtifactWhenRefreshedGateRefuses(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	reviewedAt := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, &reviewedAt)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.BaseCommit = "base123"
	detail.State.Plan.Review = &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Summary: "existing review", ReviewedAt: reviewedAt.Add(-time.Hour)}
	persistReviewState(t, planDir, detail)

	const existingArtifact = "existing review artifact\n"
	if err := os.WriteFile(filepath.Join(planDir, plan.ReviewFile), []byte(existingArtifact), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := agentSessionExecutorFunc(func(context.Context, AgentSessionRequest) (AgentSessionResult, error) {
		state := detail.State
		pending := "002-race"
		state.Status = plan.StatusInProgress
		state.Plan.CurrentSlice = &pending
		state.Plan.PendingSlices = []string{pending}
		slicesFile := detail.Slices
		slicesFile.Slices = append(append([]plan.Slice(nil), detail.Slices.Slices...), plan.Slice{ID: pending, Status: plan.StatusInProgress})
		for name, value := range map[string]any{"state.json": state, "slices.json": slicesFile} {
			payload, err := json.Marshal(value)
			if err != nil {
				return AgentSessionResult{}, err
			}
			if err := os.WriteFile(filepath.Join(planDir, name), payload, 0o600); err != nil {
				return AgentSessionResult{}, err
			}
		}
		return AgentSessionResult{Output: "replacement review artifact\n"}, nil
	})
	calls := []string{}

	_, err := createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{Agent: "pi", CommandRunner: runGitFake(&calls, nil), Now: func() time.Time { return reviewedAt }}, ReviewRun{PlanDir: planDir, PlanID: "plan-a", Detail: detail, RepoRoot: repoRoot}, fileReviewRecordFactory(plan.NewFileRepository("")))
	if err == nil || !strings.Contains(err.Error(), "002-race") {
		t.Fatalf("review error = %v, want refreshed pending-work refusal", err)
	}
	artifact, readErr := os.ReadFile(filepath.Join(planDir, plan.ReviewFile)) //nolint:gosec // test reads a t.TempDir-derived artifact.
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(artifact) != existingArtifact {
		t.Fatalf("refused review changed existing review artifact: %q", artifact)
	}
	state, readErr := plan.ReadState(planDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state.Plan.Review == nil || state.Plan.Review.Summary != "existing review" {
		t.Fatalf("refused review replaced metadata: %+v", state.Plan.Review)
	}
}

type agentSessionExecutorFunc func(context.Context, AgentSessionRequest) (AgentSessionResult, error)

func (f agentSessionExecutorFunc) RunAgentSession(ctx context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
	return f(ctx, request)
}

func fileReviewRecordFactory(repo *plan.FileRepository) PlanRecordFactory {
	return func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
		return repo.PlanRecord(detail)
	}
}

func persistReviewState(t *testing.T, planDir string, detail *plan.PlanDetail) {
	t.Helper()
	record, err := plan.NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceReviewReportsStandalonePhasesInOrderAndStopsOnFailure(t *testing.T) {
	newService := func(t *testing.T, runner CommandRunner, creator ReviewCreator, out *bytes.Buffer) (Service, Request) {
		t.Helper()
		plansRoot := t.TempDir()
		planDir := filepath.Join(plansRoot, "plan-a")
		if err := os.MkdirAll(planDir, 0o750); err != nil {
			t.Fatal(err)
		}
		repoRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/review\n\ngo 1.26\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
		detail.Dir = planDir
		detail.State.Repo.Root = repoRoot
		detail.State.Plan.LastRunCommitPolicy = CommitPolicySlice.String()
		detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
		persistReviewState(t, planDir, detail)
		encodedSlices, err := json.Marshal(detail.Slices)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(planDir, "slices.json"), encodedSlices, 0o600); err != nil {
			t.Fatal(err)
		}

		options := ResolvedRunOptions{Mode: ModeRun, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}
		service := NewService(plan.NewFileRepository(plansRoot), out, Options{
			ExecutionConfig: ExecutionConfig{ResolvedRunOptions: options},
			RunDependencies: RunDependencies{CommandRunner: runner, ReviewCreator: creator},
		})
		return service, Request{Input: "plan-a", ResolvedRunOptions: options}
	}

	t.Run("success", func(t *testing.T) {
		var out bytes.Buffer
		creatorCalled := false
		reporter := &recordingInvocationStatusReporter{}
		requirePhase := func(want runstatus.Phase) {
			t.Helper()
			if len(reporter.phases) == 0 || reporter.phases[len(reporter.phases)-1].phase != want {
				t.Fatalf("current phase = %+v, want %q", reporter.phases, want)
			}
		}
		runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
			if name == "git" && runGitKey(args) == "status --porcelain" {
				requirePhase(PhasePreparingExecution)
				return nil
			}
			if name == "sh" && strings.Join(args, " ") == "-c go build ./... && go test ./..." {
				requirePhase(PhaseFinalVerification)
				if !strings.Contains(out.String(), "Verifying completed branch: ") {
					t.Fatal("verification phase was not emitted before repository verification")
				}
				return nil
			}
			t.Fatalf("unexpected command %s %v", name, args)
			return nil
		}
		var reviewedDetail *plan.PlanDetail
		creator := reviewCreatorFunc(func(_ context.Context, run ReviewRun) (plan.PlanReview, error) {
			creatorCalled = true
			reviewedDetail = run.Detail
			requirePhase(PhaseReview)
			if !strings.Contains(out.String(), "Running agent review: pi\n") {
				t.Fatal("agent phase was not emitted before review creation")
			}
			return plan.PlanReview{Verdict: plan.ReviewVerdictApprove}, nil
		})
		service, request := newService(t, runner, creator, &out)
		startedAt := time.Date(2026, 8, 8, 18, 55, 0, 0, time.UTC)
		service.dependencies.StatusReporter = reporter
		service.dependencies.Now = func() time.Time { return startedAt }

		if _, err := service.Review(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if !creatorCalled {
			t.Fatal("review creator was not called")
		}
		if reporter.trackCalls != 0 || reporter.invocationCalls != 1 {
			t.Fatalf("reporter calls: Track=%d TrackInvocation=%d, want enhanced seam once", reporter.trackCalls, reporter.invocationCalls)
		}
		if reporter.invocation.PlanID != "plan-a" || reporter.invocation.RepoID != taodata.RepoID(reviewedDetail.State.Repo.Root) || !reporter.invocation.StartedAt.Equal(startedAt) {
			t.Fatalf("unexpected invocation: %+v", reporter.invocation)
		}
		wantPhases := []runstatus.Phase{PhaseWaitingForOwnership, PhasePreparingExecution, PhaseFinalVerification, PhaseReview}
		if len(reporter.phases) != len(wantPhases) {
			t.Fatalf("phases = %+v, want %q", reporter.phases, wantPhases)
		}
		for i, want := range wantPhases {
			if reporter.phases[i].phase != want {
				t.Fatalf("phase %d = %q, want %q", i, reporter.phases[i].phase, want)
			}
		}
		assertTextOrder(t, out.String(), "Preparing review: plan-a", "Verifying completed branch: ", "Running agent review: pi")
	})

	t.Run("preparation failure", func(t *testing.T) {
		var out bytes.Buffer
		creatorCalled := false
		runner := reviewGateGitRunner(" M uncommitted.go\n")
		creator := reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			creatorCalled = true
			return plan.PlanReview{}, nil
		})
		service, request := newService(t, runner, creator, &out)
		reporter := &recordingStatusReporter{}
		service.dependencies.StatusReporter = reporter

		if _, err := service.Review(context.Background(), request); err == nil {
			t.Fatal("expected preparation failure")
		}
		if len(reporter.calls) != 1 || reporter.calls[0].status != "run plan-a" || reporter.calls[0].settlement != "blocked" {
			t.Fatalf("unexpected status settlement: %#v", reporter.calls)
		}
		if creatorCalled || strings.Contains(out.String(), "Verifying completed branch:") || strings.Contains(out.String(), "Running agent review:") || strings.Contains(out.String(), "Review completed:") {
			t.Fatalf("preparation failure printed a later phase or completion: %q", out.String())
		}
	})

	t.Run("verification failure", func(t *testing.T) {
		var out bytes.Buffer
		creatorCalled := false
		runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
			if name == "git" && runGitKey(args) == "status --porcelain" {
				return nil
			}
			if name == "sh" {
				return fmt.Errorf("tests failed")
			}
			t.Fatalf("unexpected command %s %v", name, args)
			return nil
		}
		creator := reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			creatorCalled = true
			return plan.PlanReview{}, nil
		})
		service, request := newService(t, runner, creator, &out)

		if _, err := service.Review(context.Background(), request); err == nil {
			t.Fatal("expected verification failure")
		}
		if creatorCalled || strings.Contains(out.String(), "Running agent review:") || strings.Contains(out.String(), "Review completed:") {
			t.Fatalf("verification failure printed a later phase or completion: %q", out.String())
		}
		assertTextOrder(t, out.String(), "Preparing review: plan-a", "Verifying completed branch: ")
	})
}

func TestServiceReviewPublishesWaitingPhaseBeforeContendedLock(t *testing.T) {
	withPlanRunLockSettings(t, time.Hour, func(pid int) bool { return true })
	planDir := t.TempDir()
	held, err := acquirePlanRunLock(planDir, "plan-a", time.Date(2026, 8, 8, 18, 50, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })

	detail := runPlanDetail(plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	reporter := &recordingInvocationStatusReporter{}
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}},
		RunDependencies: RunDependencies{StatusReporter: reporter},
	})

	_, err = service.Review(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}})
	if err == nil || !errors.Is(err, errPlanRunLocked) {
		t.Fatalf("Review error = %v, want contended plan lock", err)
	}
	if reporter.invocationCalls != 1 || len(reporter.phases) != 1 || reporter.phases[0].phase != PhaseWaitingForOwnership {
		t.Fatalf("status before lock failure = invocation calls %d, phases %+v", reporter.invocationCalls, reporter.phases)
	}
}

func TestReviewLockReloadsAuthoritativePlanDetail(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	stale := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	stale.Dir = planDir
	stale.State.Repo.Root = repoRoot
	stale.State.Plan.Title = "stale"
	fresh := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	fresh.Dir = planDir
	fresh.State.Repo.Root = repoRoot
	fresh.State.Plan.Title = "fresh"
	repo := &memoryRunRepository{details: []*plan.PlanDetail{stale, fresh}}
	var reviewedDetail *plan.PlanDetail
	creator := reviewCreatorFunc(func(_ context.Context, run ReviewRun) (plan.PlanReview, error) {
		reviewedDetail = run.Detail
		return plan.PlanReview{Verdict: plan.ReviewVerdictComment}, nil
	})
	service := NewService(repo, io.Discard, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}},
		RunDependencies: RunDependencies{ReviewCreator: creator, CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return nil }},
	})

	if _, err := service.Review(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}}); err != nil {
		t.Fatal(err)
	}
	if reviewedDetail != fresh || reviewedDetail.State.Plan.Title != "fresh" {
		t.Fatalf("review used stale pre-lock detail: %#v", reviewedDetail)
	}
	if repo.calls != 2 {
		t.Fatalf("ResolvePlan calls = %d, want pre-lock selection plus post-lock reload", repo.calls)
	}
}

func TestReviewRejectsPendingSliceWorkBeforeVerificationOrAgent(t *testing.T) {
	current := "002-current"
	tests := []struct {
		name       string
		detail     *plan.PlanDetail
		wantSlice  string
		wantAbsent string
	}{
		{
			name: "current slice takes precedence",
			detail: &plan.PlanDetail{
				Dir: t.TempDir(),
				State: plan.State{Status: plan.StatusInProgress, Repo: plan.Repo{Root: t.TempDir()}, Plan: plan.PlanState{
					ID: "plan-a", CurrentSlice: &current, PendingSlices: []string{"001-pending", current},
				}},
				Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: current, Status: plan.StatusInProgress}, {ID: "001-pending", Status: plan.StatusPending}}},
			},
			wantSlice:  current,
			wantAbsent: "001-pending",
		},
		{
			name: "first pending slice",
			detail: &plan.PlanDetail{
				Dir: t.TempDir(),
				State: plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Root: t.TempDir()}, Plan: plan.PlanState{
					ID: "plan-a", PendingSlices: []string{"003-pending"},
				}},
				Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "003-pending", Status: plan.StatusPending}}},
			},
			wantSlice: "003-pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			commandCalled := false
			creatorCalled := false
			repo := &memoryRunRepository{details: []*plan.PlanDetail{tt.detail, tt.detail}}
			service := NewService(repo, &out, Options{
				ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}},
				RunDependencies: RunDependencies{
					CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
						commandCalled = true
						return nil
					},
					ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
						creatorCalled = true
						return plan.PlanReview{}, nil
					}),
				},
			})

			_, err := service.Review(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}})
			if err == nil || !strings.Contains(err.Error(), tt.wantSlice) || !strings.Contains(err.Error(), "tao run plan-a") {
				t.Fatalf("Review error = %v, want actionable refusal for %s", err, tt.wantSlice)
			}
			if tt.wantAbsent != "" && strings.Contains(err.Error(), tt.wantAbsent) {
				t.Fatalf("Review error chose pending slice instead of current slice: %v", err)
			}
			if commandCalled || creatorCalled {
				t.Fatalf("refused review performed expensive work: command=%v creator=%v", commandCalled, creatorCalled)
			}
			if out.Len() != 0 {
				t.Fatalf("refused review emitted preparation output: %q", out.String())
			}
		})
	}
}

func TestReviewSettledWorkRunsVerificationAndAgent(t *testing.T) {
	detail := runPlanDetail(plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = t.TempDir()
	detail.State.Plan.LastRunCommitPolicy = CommitPolicySlice.String()
	commandCalled := false
	creatorCalled := false
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, detail}}
	service := NewService(repo, io.Discard, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}},
		RunDependencies: RunDependencies{
			CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
				commandCalled = true
				return nil
			},
			ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
				creatorCalled = true
				return plan.PlanReview{Verdict: plan.ReviewVerdictApprove}, nil
			}),
		},
	})

	if _, err := service.Review(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi}}); err != nil {
		t.Fatal(err)
	}
	if !commandCalled || !creatorCalled {
		t.Fatalf("settled review did not run verification and agent: command=%v creator=%v", commandCalled, creatorCalled)
	}
}

func assertTextOrder(t *testing.T, text string, values ...string) {
	t.Helper()
	previous := -1
	for _, value := range values {
		index := strings.Index(text, value)
		if index < 0 {
			t.Fatalf("output missing %q: %q", value, text)
		}
		if index <= previous {
			t.Fatalf("output is not ordered at %q: %q", value, text)
		}
		previous = index
	}
}

// reviewGateDetail builds a plan detail whose single completed slice declares
// the given expected files. The review gate uses them only for plan shape; its
// cleanliness oracle does not filter leftovers by expected_files.
func reviewGateDetail(expected ...string) *plan.PlanDetail {
	return &plan.PlanDetail{
		State:  plan.State{Plan: plan.PlanState{ID: "plan-a", CompletedSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted, ExpectedFiles: expected}}},
	}
}

func reviewGateGitRunner(status string) CommandRunner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "status --porcelain" {
			_, _ = io.WriteString(stdout, status)
		}
		return nil
	}
}

// TestRequireCleanReviewWorktreeToleratesStartingDirtyPaths guards compatibility
// for historical runs that recorded preexisting dirty paths. The review gate
// tolerates those paths while still rejecting other uncommitted run-produced
// work.
func TestRequireCleanReviewWorktreeToleratesStartingDirtyPaths(t *testing.T) {
	t.Run("only starting-dirty remains", func(t *testing.T) {
		git := gitClient(agentOperationOptions{CommandRunner: reviewGateGitRunner(" M README.md\n?? .env.local\n")}, t.TempDir())
		detail := reviewGateDetail("README.md", ".env.local")
		if err := requireCleanReviewWorktree(context.Background(), git, detail, []string{"README.md", ".env.local"}); err != nil {
			t.Fatalf("expected starting-dirty paths tolerated, got: %v", err)
		}
	})

	t.Run("uncommitted plan work remains", func(t *testing.T) {
		git := gitClient(agentOperationOptions{CommandRunner: reviewGateGitRunner(" M README.md\n M internal/run/run.go\n")}, t.TempDir())
		detail := reviewGateDetail("README.md", "internal/run/run.go")
		err := requireCleanReviewWorktree(context.Background(), git, detail, []string{"README.md"})
		if err == nil {
			t.Fatal("expected uncommitted non-starting-dirty path to fail the gate")
		}
		if !strings.Contains(err.Error(), "internal/run/run.go") {
			t.Fatalf("error %q should name the offending path", err.Error())
		}
		if strings.Contains(err.Error(), "README.md") {
			t.Fatalf("error %q should not report the tolerated starting-dirty path", err.Error())
		}
	})
}

// TestRequireCleanReviewWorktreeMatchesStatusClassificationTolerance pins the
// shared status-classification rules: run-produced work outside .tao is blocked
// unless it was already dirty when the run started. A rename entirely inside
// .tao is tolerated, while any other rename stays a hard stop.
func TestRequireCleanReviewWorktreeMatchesStatusClassificationTolerance(t *testing.T) {
	t.Run("scratch file outside expected_files blocked", func(t *testing.T) {
		git := gitClient(agentOperationOptions{CommandRunner: reviewGateGitRunner("?? debug.sh\n")}, t.TempDir())
		err := requireCleanReviewWorktree(context.Background(), git, reviewGateDetail("internal/run/run.go"), nil)
		if err == nil || !strings.Contains(err.Error(), "debug.sh") {
			t.Fatalf("expected undeclared scratch file to fail the gate, got: %v", err)
		}
	})

	t.Run("tao metadata rename tolerated", func(t *testing.T) {
		git := gitClient(agentOperationOptions{CommandRunner: reviewGateGitRunner("R  .tao/plans/old.json -> .tao/plans/new.json\n")}, t.TempDir())
		if err := requireCleanReviewWorktree(context.Background(), git, reviewGateDetail("internal/run/run.go"), nil); err != nil {
			t.Fatalf("expected .tao rename tolerated, got: %v", err)
		}
	})

	t.Run("plan-work rename stays a hard stop", func(t *testing.T) {
		git := gitClient(agentOperationOptions{CommandRunner: reviewGateGitRunner("R  old.go -> new.go\n")}, t.TempDir())
		err := requireCleanReviewWorktree(context.Background(), git, reviewGateDetail("internal/run/run.go"), nil)
		if err == nil || !strings.Contains(err.Error(), "old.go -> new.go") {
			t.Fatalf("expected non-tao rename to fail the gate, got: %v", err)
		}
	})
}

// TestRequireCleanReviewWorktreeToleratesTaoMetadataPaths guards the review
// gate against aborting on .tao metadata. Automatic slice commits exclude .tao
// entries (classifyGitStatus identifies staged metadata for unstaging), so .tao
// dirt produced during the run must not read as unreviewed plan work.
func TestRequireCleanReviewWorktreeToleratesTaoMetadataPaths(t *testing.T) {
	t.Run("only tao metadata remains", func(t *testing.T) {
		git := gitClient(agentOperationOptions{CommandRunner: reviewGateGitRunner("?? .tao/\n M .tao/state.json\nA  .tao/plans/plan-a/events.jsonl\n")}, t.TempDir())
		if err := requireCleanReviewWorktree(context.Background(), git, reviewGateDetail("internal/run/run.go"), nil); err != nil {
			t.Fatalf("expected .tao metadata tolerated, got: %v", err)
		}
	})

	t.Run("tao metadata plus plan work", func(t *testing.T) {
		git := gitClient(agentOperationOptions{CommandRunner: reviewGateGitRunner("?? .tao/logs/run.log\n M internal/run/run.go\n")}, t.TempDir())
		err := requireCleanReviewWorktree(context.Background(), git, reviewGateDetail("internal/run/run.go"), nil)
		if err == nil {
			t.Fatal("expected uncommitted plan work to fail the gate despite .tao dirt")
		}
		if !strings.Contains(err.Error(), "internal/run/run.go") {
			t.Fatalf("error %q should name the offending path", err.Error())
		}
		if strings.Contains(err.Error(), ".tao") {
			t.Fatalf("error %q should not report tolerated .tao paths", err.Error())
		}
	})
}

// TestPrepareReviewExecutionCapturesNoStartingDirtyPaths guards the standalone
// `tao review --run` path: it must NOT snapshot review-invocation dirt into
// StartingDirtyPaths. A review-start snapshot would tolerate every dirty path —
// including uncommitted plan work left by a crashed run, the very thing
// requireCleanReviewWorktree exists to catch — making the gate vacuous where
// the in-run review (whose snapshot predates the agent's edits) hard-stops. It
// also means a rename entry, which the snapshot capture rejects as ambiguous,
// cannot abort a CommitPolicyNone review that skips the gate entirely.
func TestPrepareReviewExecutionCapturesNoStartingDirtyPaths(t *testing.T) {
	repoRoot := t.TempDir()
	repo := plan.NewFileRepository(t.TempDir())
	fakeGit := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "status --porcelain" {
			_, _ = io.WriteString(stdout, " M README.md\nR  old.go -> new.go\n")
		}
		return nil
	}
	s := Service{
		dependencies: RunDependencies{
			CommandRunner:     fakeGit,
			PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) { return repo.PlanRecord(detail) },
			EventAppender:     repo,
			LogAppender:       repo,
		},
	}
	detail := &plan.PlanDetail{
		State: plan.State{
			Repo:      plan.Repo{Root: repoRoot},
			Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent},
		},
	}
	config := ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi}}

	execution, err := s.prepareReviewExecution(detail, config)
	if err != nil {
		t.Fatalf("prepareReviewExecution: %v", err)
	}
	if len(execution.StartingDirtyPaths) != 0 {
		t.Fatalf("StartingDirtyPaths = %q, want empty: a review-start snapshot would tolerate uncommitted plan work", execution.StartingDirtyPaths)
	}
}

func TestStandaloneReviewDoesNotUsePersistedStartingDirtyTolerance(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Plan.LastRunCommitPolicy = CommitPolicyPlan.String()
	detail.State.Plan.LastRunStartingDirty = []string{"README.md"}
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	repo := plan.NewFileRepository("")
	execution, err := (Service{dependencies: RunDependencies{CommandRunner: reviewGateGitRunner(""), PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) { return repo.PlanRecord(detail) }, EventAppender: repo, LogAppender: repo}}).prepareReviewExecution(detail, ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi}})
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.StartingDirtyPaths) != 0 {
		t.Fatalf("historical tolerance should remain metadata-only, got %#v", execution.StartingDirtyPaths)
	}
	_, err = createReviewWithAgentSession(context.Background(), &recordingAgentSessionExecutor{result: AgentSessionResult{Output: "Looks good."}}, agentOperationOptions{Agent: "pi", CommitPolicy: execution.Config.CommitPolicy, CommandRunner: reviewGateGitRunner(" M README.md\n")}, ReviewRun{PlanDir: planDir, PlanID: "plan-a", Detail: detail, RepoRoot: repoRoot}, fileReviewRecordFactory(repo))
	if err == nil || !strings.Contains(err.Error(), "clean committed tree") {
		t.Fatalf("expected historical plan-policy review to require cleanliness, got %v", err)
	}
}

func TestReviewExecutionRootUsesRecordedWorkspacePathWithoutStrategy(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeRoot := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	detail := &plan.PlanDetail{
		State: plan.State{
			Plan:      plan.PlanState{ID: "plan-a"},
			Repo:      plan.Repo{Root: repoRoot},
			Workspace: &plan.Workspace{Path: worktreeRoot},
		},
	}

	got, err := reviewExecutionRoot(detail)
	if err != nil {
		t.Fatalf("reviewExecutionRoot: %v", err)
	}
	if got != worktreeRoot {
		t.Fatalf("reviewExecutionRoot() = %q, want recorded legacy workspace path %q", got, worktreeRoot)
	}
}

// TestPrepareReviewExecutionUsesRecordedRunCommitPolicy guards the standalone
// review's gate policy: a plan executed with --commit-policy none deliberately
// leaves its work uncommitted, and `tao review` has no --commit-policy flag,
// so the gate must key off the policy recorded by the plan's runs rather than
// this invocation's configured default — otherwise such a plan can never be
// reviewed standalone. State is authoritative over legacy run_context telemetry;
// plans predating the policy telemetry record nothing and a legacy none-policy
// plan is indistinguishable from a committing one, so an unrecorded policy
// resolves to none (skip the gate; merge still enforces a clean tree) rather
// than locking legacy none-policy plans out of review.
func TestPrepareReviewExecutionUsesRecordedRunCommitPolicy(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	s := Service{
		dependencies: RunDependencies{
			CommandRunner:     reviewGateGitRunner(""),
			PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) { return repo.PlanRecord(detail) },
			EventAppender:     repo,
			LogAppender:       repo,
		},
	}
	newDetail := func(statePolicy string, events []plan.Event) *plan.PlanDetail {
		return &plan.PlanDetail{
			State: plan.State{
				Plan:      plan.PlanState{LastRunCommitPolicy: statePolicy},
				Repo:      plan.Repo{Root: t.TempDir()},
				Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent},
			},
			Events: events,
		}
	}
	config := ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi, CommitPolicy: CommitPolicyPlan}}

	cases := []struct {
		name        string
		statePolicy string
		events      []plan.Event
		want        CommitPolicy
	}{
		{
			name:        "state policy wins over legacy events",
			statePolicy: CommitPolicySlice.String(),
			events:      []plan.Event{{Type: plan.EventTypeRunContext, CommitPolicy: CommitPolicyNone.String()}},
			want:        CommitPolicySlice,
		},
		{
			name:        "invalid state policy falls back to latest legacy event",
			statePolicy: "always",
			events: []plan.Event{
				{Type: plan.EventTypeRunContext, CommitPolicy: CommitPolicyPlan.String()},
				{Type: plan.EventTypeRunContext, CommitPolicy: CommitPolicyNone.String()},
			},
			want: CommitPolicyNone,
		},
		{
			name:        "empty state policy falls back to latest legacy event",
			statePolicy: " ",
			events:      []plan.Event{{Type: plan.EventTypeRunContext, CommitPolicy: CommitPolicySlice.String()}},
			want:        CommitPolicySlice,
		},
		{
			name: "legacy events use last valid run context policy",
			events: []plan.Event{
				{Type: plan.EventTypeRunContext, CommitPolicy: CommitPolicySlice.String()},
				{Type: plan.EventTypeRunContext, CommitPolicy: CommitPolicyNone.String()},
			},
			want: CommitPolicyNone,
		},
		{
			name:   "unrecorded policy resolves to none",
			events: []plan.Event{{Type: plan.EventTypeRunContext}},
			want:   CommitPolicyNone,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			execution, err := s.prepareReviewExecution(newDetail(tt.statePolicy, tt.events), config)
			if err != nil {
				t.Fatalf("prepareReviewExecution: %v", err)
			}
			if execution.Config.CommitPolicy != tt.want {
				t.Fatalf("CommitPolicy = %q, want %q", execution.Config.CommitPolicy, tt.want)
			}
		})
	}
}

func reviewGitFake(calls *[]string, responses map[string]string) CommandRunner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			return nil
		}
		key := runGitKey(args)
		*calls = append(*calls, key)
		if out, ok := responses[key]; ok {
			_, _ = io.WriteString(stdout, out+"\n")
			return nil
		}
		switch key {
		case "status --porcelain":
			return nil
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head123\n")
		case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
		default:
			return fmt.Errorf("unstubbed git command: %s", key)
		}
		return nil
	}
}

func TestCreateReviewWithAgentSessionFallsBackToBaseCommitWithoutWorkspace(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	reviewedAt := time.Date(2026, 6, 28, 12, 45, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, &reviewedAt)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.BaseCommit = "base123"
	detail.State.Workspace = nil
	persistReviewState(t, planDir, detail)

	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{Output: "Looks good."}}
	calls := []string{}
	store := plan.NewFileRepository("")

	review, err := createReviewWithAgentSession(context.Background(), executor, agentOperationOptions{Agent: "pi", CommandRunner: runGitFake(&calls, nil), Now: func() time.Time { return reviewedAt }}, ReviewRun{PlanDir: planDir, PlanID: "plan-a", Detail: detail, RepoRoot: repoRoot}, fileReviewRecordFactory(store))
	if err != nil {
		t.Fatal(err)
	}

	if review.Base != "base123" {
		t.Fatalf("expected review base to fall back to base_commit, got %+v", review)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("expected one review request, got %#v", executor.requests)
	}
	if prompt := executor.requests[0].Prompt; !strings.Contains(prompt, "Base: `base123`") {
		t.Fatalf("review prompt missing base_commit fallback:\n%s", prompt)
	}
	gotState, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotState.Plan.Review == nil || gotState.Plan.Review.Base != "base123" {
		t.Fatalf("persisted review did not use base_commit fallback: %+v", gotState.Plan.Review)
	}
}

func TestExtractApprovedReviewRequiresValidCommitMessage(t *testing.T) {
	output := "Looks good.\n```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"Ready to merge.\",\"commit_message\":{\"subject\":\"feat(review): persist approved commit proposals\",\"body\":\"What:\\nPersist the review agent's proposal for the exact diff.\\n\\nWhy:\\nAllow normal merge to reuse reviewed message context.\"}}\n```"

	got := extractReview(output)
	if got.Verdict != plan.ReviewVerdictApprove || got.Summary != "Ready to merge." || got.FindingsCount != 0 {
		t.Fatalf("unexpected review: %+v", got)
	}
	if got.Findings == nil || len(got.Findings) != 0 {
		t.Fatalf("expected empty findings slice, got %+v", got.Findings)
	}
	if got.CommitMessage == nil || got.CommitMessage.Subject != "feat(review): persist approved commit proposals" || !strings.Contains(got.CommitMessage.Body, "Why:") {
		t.Fatalf("unexpected commit message: %+v", got.CommitMessage)
	}
}

func TestParseReviewOutputPreservesAggregateApprovalWithoutCommitMessage(t *testing.T) {
	output := "Review.\n```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"Ready\",\"findings\":[]}\n```"
	got := ParseReviewOutput(output)
	if got.Verdict != plan.ReviewVerdictApprove || got.CommitMessage != nil {
		t.Fatalf("aggregate approval changed by plan commit-message contract: %+v", got)
	}
}

func TestExtractApprovedReviewInvalidCommitMessageFallsBackToComment(t *testing.T) {
	tests := map[string]string{
		"missing":  "",
		"invalid":  `,"commit_message":{"subject":"review work","body":"details"}`,
		"trailers": `,"commit_message":{"subject":"feat(review): persist approved commit proposals","body":"What:\nPersist the proposal.\n\nWhy:\nReuse review context.\n\nTao-Plan: forged"}`,
	}
	for name, commitMessage := range tests {
		t.Run(name, func(t *testing.T) {
			output := "Review.\n```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"Ready\"" + commitMessage + "}\n```"
			got := extractReview(output)
			if got.Verdict != plan.ReviewVerdictComment || got.CommitMessage != nil || got.FindingsCount != 0 {
				t.Fatalf("invalid approval must use comment fallback: %+v", got)
			}
		})
	}
}

func TestExtractApprovedReviewOversizedCommitMessageFallsBackToComment(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"verdict": "approve",
		"summary": "Ready",
		"commit_message": map[string]string{
			"subject": "feat(review): persist approved commit proposals",
			"body":    "What:\n" + strings.Repeat("x", maxReviewSummaryRunes) + "\n\nWhy:\nNeeded.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := extractReview("Review.\n```tao-review-json\n" + string(payload) + "\n```")
	if got.Verdict != plan.ReviewVerdictComment || got.CommitMessage != nil {
		t.Fatalf("oversized approval must use comment fallback: %+v", got)
	}
}

func TestExtractNonApprovedReviewClearsCommitMessage(t *testing.T) {
	output := "Review.\n```tao-review-json\n{\"verdict\":\"changes_requested\",\"summary\":\"Fix this\",\"commit_message\":{\"subject\":\"feat(review): ignore stale commit proposal\",\"body\":\"What:\\nIgnore this message.\\n\\nWhy:\\nThe review is not approved.\"},\"findings\":[]}\n```"
	got := extractReview(output)
	if got.Verdict != plan.ReviewVerdictChangesRequested || got.CommitMessage != nil {
		t.Fatalf("non-approved review retained commit message: %+v", got)
	}
}

func TestExtractReviewMalformedOutputDegradesGracefully(t *testing.T) {
	tests := []string{
		"Plain human review without a structured block.",
		"Review text.\n```tao-review-json\n{\"verdict\": \"approve\", \"findings\": \"not-an-array\"}\n```",
	}
	for _, output := range tests {
		t.Run(output[:12], func(t *testing.T) {
			got := extractReview(output)
			if got.Verdict != "comment" || got.Summary != strings.TrimSpace(output) || got.FindingsCount != 0 {
				t.Fatalf("expected graceful comment fallback, got %+v", got)
			}
		})
	}
}

func TestExtractReviewCapsStructuredMetadata(t *testing.T) {
	findings := make([]plan.ReviewFinding, maxReviewFindings+1)
	for i := range findings {
		findings[i] = plan.ReviewFinding{
			Severity:   "  " + strings.Repeat("s", maxReviewFindingSeverityRunes+3) + "  ",
			File:       "  " + strings.Repeat("f", maxReviewFindingFileRunes+3) + "  ",
			Line:       -1,
			Message:    "  " + strings.Repeat("m", maxReviewFindingTextRunes+3) + "  ",
			Suggestion: "  " + strings.Repeat("g", maxReviewFindingTextRunes+3) + "  ",
		}
	}
	payload, err := json.Marshal(map[string]any{
		"verdict":  plan.ReviewVerdictChangesRequested,
		"summary":  "  " + strings.Repeat("s", maxReviewSummaryRunes+3) + "  ",
		"findings": findings,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := extractReview("Review.\n```tao-review-json\n" + string(payload) + "\n```")
	if got.Verdict != plan.ReviewVerdictChangesRequested {
		t.Fatalf("verdict = %q", got.Verdict)
	}
	if got.FindingsCount != maxReviewFindings || len(got.Findings) != maxReviewFindings {
		t.Fatalf("findings = %d/%d", got.FindingsCount, len(got.Findings))
	}
	if len([]rune(got.Summary)) != maxReviewSummaryRunes {
		t.Fatalf("summary length = %d, want %d", len([]rune(got.Summary)), maxReviewSummaryRunes)
	}
	finding := got.Findings[0]
	if finding.Line != 0 {
		t.Fatalf("negative line should be clamped, got %d", finding.Line)
	}
	if len([]rune(finding.Severity)) != maxReviewFindingSeverityRunes || len([]rune(finding.File)) != maxReviewFindingFileRunes || len([]rune(finding.Message)) != maxReviewFindingTextRunes || len([]rune(finding.Suggestion)) != maxReviewFindingTextRunes {
		t.Fatalf("finding fields were not capped: %+v", finding)
	}
}

func TestExtractReviewOversizedJSONBlockFallsBackToCappedComment(t *testing.T) {
	output := "Review.\n```tao-review-json\n" + strings.Repeat("x", maxReviewJSONBlockBytes+1) + "\n```"
	got := extractReview(output)
	if got.Verdict != plan.ReviewVerdictComment || got.FindingsCount != 0 {
		t.Fatalf("expected capped comment fallback, got %+v", got)
	}
	if len([]rune(got.Summary)) != maxReviewSummaryRunes {
		t.Fatalf("fallback summary length = %d, want %d", len([]rune(got.Summary)), maxReviewSummaryRunes)
	}
}

func TestResumeReviewReloadsAuthoritativeStateAndRejectsReopenedWork(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	stale := runPlanDetail(plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	stale.Dir = planDir
	stale.State.Repo.Root = repoRoot
	fresh := runPlanDetail(plan.StatusInProgress, []string{"002-reopened"}, []string{"001-a"}, "002-reopened", plan.StatusPending, nil, nil)
	fresh.Dir = planDir
	fresh.State.Repo.Root = repoRoot
	repo := &memoryRunRepository{details: []*plan.PlanDetail{stale, fresh}}
	commandCalled := false
	creatorCalled := false
	service := NewService(repo, io.Discard, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi, ReviewEnabled: true}},
		RunDependencies: RunDependencies{
			CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
				commandCalled = true
				return nil
			},
			ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
				creatorCalled = true
				return plan.PlanReview{}, nil
			}),
		},
	})

	err := service.ResumeReview(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi, ReviewEnabled: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "002-reopened") {
		t.Fatalf("ResumeReview error = %v, want refreshed pending-work refusal", err)
	}
	if repo.calls != 2 {
		t.Fatalf("ResolvePlan calls = %d, want pre-lock selection plus post-lock reload", repo.calls)
	}
	if commandCalled || creatorCalled {
		t.Fatalf("stale recovery performed work: command=%t creator=%t", commandCalled, creatorCalled)
	}
}

func TestResumeReviewHonorsExistingPlanOwnershipAndPreservesBestEffortReviewFailure(t *testing.T) {
	plansRoot := t.TempDir()
	planDir := filepath.Join(plansRoot, "plan-a")
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 22, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		Dir: planDir,
		State: plan.State{
			Schema: "tao.plan.state.v1",
			Status: plan.StatusInReview,
			Repo:   plan.Repo{Root: t.TempDir()},
			Plan:   plan.PlanState{ID: "plan-a", CompletedSlices: []string{"001-work"}},
		},
		Slices: plan.SlicesFile{Schema: "tao.plan.slices.v1", PlanID: "plan-a", Slices: []plan.Slice{{ID: "001-work", Status: plan.StatusCompleted}}},
	}
	persistReviewState(t, planDir, detail)
	encodedSlices, err := json.Marshal(detail.Slices)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "slices.json"), encodedSlices, 0o600); err != nil {
		t.Fatal(err)
	}

	var warnings bytes.Buffer
	repo := plan.NewFileRepository(plansRoot)
	options := ResolvedRunOptions{Mode: ModeRun, CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentPi, ReviewEnabled: true}
	service := NewService(repo, &warnings, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: options},
		RunDependencies: RunDependencies{
			ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
				return plan.PlanReview{}, fmt.Errorf("review timed out")
			}),
			Now:              func() time.Time { return now },
			SessionLogWriter: &warnings,
		},
	})
	request := Request{Input: "plan-a", ResolvedRunOptions: options}
	if err := service.WithPlanRunLock(context.Background(), request, func(ownedCtx context.Context) error {
		return service.ResumeReview(ownedCtx, request)
	}); err != nil {
		t.Fatalf("ResumeReview returned best-effort review error under existing ownership: %v", err)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Status != plan.StatusInReview || reloaded.State.Plan.Review == nil || reloaded.State.Plan.Review.Status != plan.ReviewStatusError {
		t.Fatalf("best-effort review failure was not recorded: %+v", reloaded.State)
	}
	if !strings.Contains(warnings.String(), "Warning: plan review failed; continuing without failing the run: review timed out") {
		t.Fatalf("review warning = %q", warnings.String())
	}
}

func TestReviewCreatorDefaultsFromAgentCapabilities(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi}}, RunDependencies{PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) { return repo.PlanRecord(detail) }, EventAppender: repo, LogAppender: repo})
	resolveExecutorDefaults(&execution)
	creator, ok := execution.Dependencies.ReviewCreator.(agentExecutor)
	if !ok || creator.descriptor.Kind != AgentPi {
		t.Fatalf("expected pi review creator, got %T", execution.Dependencies.ReviewCreator)
	}
}
