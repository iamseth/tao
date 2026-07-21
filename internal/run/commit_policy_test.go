package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestRunExecutionModeCurrentSkipsCheckoutAfterCompletion(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &started, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &started, &completed)
	var out bytes.Buffer
	var calls []string

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeCurrent, CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no checkout-related git calls, got %#v", calls)
	}
	if strings.Contains(out.String(), "Merge instructions") {
		t.Fatalf("expected no merge instructions for current branch policy, got:\n%s", out.String())
	}
}

func TestRunWorktreeStrategySkipsFinalCheckoutAndMergeInstructions(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &started, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &started, &completed)
	completedDetail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Branch: "tao/plan-a"}
	var out bytes.Buffer
	var calls []string

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeIsolated}}, RunDependencies: RunDependencies{WorkspacePreparer: func(_ context.Context, _ *plan.PlanDetail, _ WorkspaceResolverInput) (string, error) {
		return detail.State.Repo.Root, nil
	}, SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, map[string]error{"checkout main": errors.New("should not checkout")})}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no final checkout git calls for worktree strategy, got %#v", calls)
	}
	output := out.String()
	if !strings.Contains(output, "Worktree run: leaving the plan worktree on its feature branch; control checkout was not changed.") {
		t.Fatalf("expected worktree checkout note, got:\n%s", output)
	}
	if strings.Contains(output, "Merge instructions") {
		t.Fatalf("expected no merge instructions for worktree strategy, got:\n%s", output)
	}
}

func TestRunWorkPromptExecutionModeText(t *testing.T) {
	currentPrompt, err := renderWorkPrompt(workPromptData{PlanDir: "/plans/plan-a", ExecutionMode: ExecutionModeCurrent.String(), CommitPolicy: CommitPolicySlice.String()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Use the current branch where `tao run` started", "Do not create or switch branches", "Direct commits to `main` or `master` are allowed only when that is the starting branch"} {
		if !strings.Contains(currentPrompt, want) {
			t.Fatalf("expected current policy prompt to contain %q:\n%s", want, currentPrompt)
		}
	}
	if strings.Contains(currentPrompt, "Create or reuse a single feature branch") {
		t.Fatalf("expected current policy prompt to omit feature branch instructions:\n%s", currentPrompt)
	}

	featurePrompt, err := renderWorkPrompt(workPromptData{PlanDir: "/plans/plan-a", ExecutionMode: ExecutionModeIsolated.String(), CommitPolicy: CommitPolicySlice.String()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Create or reuse a single feature branch", "Never commit directly to:", "- `main`", "- `master`"} {
		if !strings.Contains(featurePrompt, want) {
			t.Fatalf("expected feature policy prompt to contain %q:\n%s", want, featurePrompt)
		}
	}
	if strings.Contains(featurePrompt, "Use the current branch where `tao run` started") {
		t.Fatalf("expected feature policy prompt to omit current branch instructions:\n%s", featurePrompt)
	}
}

func TestRunReturnsCheckoutFailureAfterSummary(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &started, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &started, &completed)
	var out bytes.Buffer
	var calls []string

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, map[string]error{"checkout main": errors.New("dirty tree")})}})
	if err == nil || !strings.Contains(err.Error(), "checkout default branch main") || !strings.Contains(err.Error(), "dirty tree") {
		t.Fatalf("expected checkout failure, got %v", err)
	}
	if !strings.Contains(out.String(), "Plan slices complete: plan-a") || !strings.Contains(out.String(), "Summary: 1/1 slices completed") {
		t.Fatalf("expected summary before checkout failure, got %q", out.String())
	}
}

func TestRunReturnsDirtyWorktreeErrorBeforeCheckout(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &started, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &started, &completed)
	var out bytes.Buffer
	var calls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			return nil
		}
		key := runGitKey(args)
		calls = append(calls, key)
		switch key {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
		case "status --porcelain":
			_, _ = io.WriteString(stdout, " M internal/run/run.go\n")
		case "checkout main":
			t.Fatal("checkout should not run with a dirty worktree")
		}
		return nil
	}

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runner}})
	if err == nil || !strings.Contains(err.Error(), "worktree has uncommitted changes") || !strings.Contains(err.Error(), "refusing to checkout default branch main") {
		t.Fatalf("expected dirty worktree checkout safety error, got %v", err)
	}
	if runHasGitCall(calls, "checkout main") {
		t.Fatalf("expected no checkout call, got %#v", calls)
	}
	if !strings.Contains(out.String(), "Plan slices complete: plan-a") || !strings.Contains(out.String(), "Summary: 1/1 slices completed") {
		t.Fatalf("expected summary before dirty worktree error, got %q", out.String())
	}
}

func TestServiceExecuteUsesRequestCommitPolicy(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	settleRunTestSlice(completedDetail)
	detail.Dir = t.TempDir()
	completedDetail.Dir = detail.Dir
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, completedDetail}}
	executor := &packetCapturingExecutor{}

	err := NewService(repo, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&[]string{}, nil)}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(executor.packet, "- Commit Policy: slice") {
		t.Fatalf("expected request commit policy in run packet, got:\n%s", executor.packet)
	}
}

func TestServiceExecuteExecutionModeCurrentCapturesStartingBranchBeforeSliceStart(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.Branch = "planned-feature"
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = planDir
	completedDetail.State.Repo.Root = repoRoot
	persistRunArtifacts(t, planDir, detail)
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, completedDetail}}
	started := false
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "branch --show-current" {
			_, _ = io.WriteString(stdout, "main\n")
		}
		return ctx.Err()
	}

	err := NewService(repo, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeCurrent, CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
		record, err := plan.NewPlanRecord(planDir, detail)
		if err != nil {
			return nil, err
		}
		return startCallbackRecord{PlanMutationRecord: record, onStart: func(sliceID string, now time.Time) error {
			started = true
			state, err := plan.ReadState(detail.Dir)
			if err != nil {
				return err
			}
			if state.Repo.Branch != "main" {
				t.Fatalf("expected starting branch written before slice start, got %q", state.Repo.Branch)
			}
			return record.StartSlice(sliceID, now)
		}}, nil
	}, CommandRunner: runner}}).Execute(context.Background(), Request{Input: "plan-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("expected slice starter to run")
	}
}

func TestServiceExecuteExecutionModeCurrentFailsBeforeSliceStartWhenBranchEmpty(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.Branch = "planned-feature"
	persistRunArtifacts(t, planDir, detail)
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
	started := false
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		return ctx.Err()
	}

	err := NewService(repo, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeCurrent, CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: &countingSliceExecutor{}, PlanRecordFactory: callbackPlanRecordFactory(func(detail *plan.PlanDetail, sliceID string, now time.Time) error {
		started = true
		return nil
	}, nil), CommandRunner: runner}}).Execute(context.Background(), Request{Input: "plan-a"})
	if err == nil || !strings.Contains(err.Error(), "returned empty branch") {
		t.Fatalf("expected empty branch error, got %v", err)
	}
	if started {
		t.Fatal("expected slice starter not to run")
	}
	state, readErr := plan.ReadState(planDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state.Repo.Branch != "planned-feature" {
		t.Fatalf("expected state branch unchanged, got %q", state.Repo.Branch)
	}
}

type memoryRunRepository struct {
	details []*plan.PlanDetail
	calls   int
}
