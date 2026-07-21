package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestInterruptedSliceClassifiesRecoveryBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*InterruptedSliceInput)
		want   InterruptedSliceDisposition
	}{
		{name: "tracked resume", mutate: statusMutation(" M tracked.go"), want: InterruptedSliceResume},
		{name: "untracked resume", mutate: statusMutation("?? new.go"), want: InterruptedSliceResume},
		{name: "staged resume", mutate: statusMutation("M  staged.go"), want: InterruptedSliceResume},
		{name: "clean resume without telemetry", want: InterruptedSliceResume},
		{name: "different slice", mutate: func(in *InterruptedSliceInput) { other := "002-b"; in.Detail.State.Plan.CurrentSlice = &other }, want: InterruptedSliceRefuse},
		{name: "changed branch", mutate: func(in *InterruptedSliceInput) { in.Branch = "other" }, want: InterruptedSliceRefuse},
		{name: "advanced head", mutate: func(in *InterruptedSliceInput) { in.Head = "advanced" }, want: InterruptedSliceRefuse},
		{name: "workspace path differs", mutate: func(in *InterruptedSliceInput) { in.Detail.State.Workspace.Path = "/workspace/other" }, want: InterruptedSliceRefuse},
		{name: "worktree is control checkout", mutate: func(in *InterruptedSliceInput) { in.Detail.State.Repo.Root = in.ExecutionRoot }, want: InterruptedSliceRefuse},
		{name: "workspace is not ready", mutate: func(in *InterruptedSliceInput) {
			in.Detail.State.Workspace.LifecycleStatus = plan.WorkspaceStatusCleaned
		}, want: InterruptedSliceRefuse},
		{name: "workspace cleanup is active", mutate: func(in *InterruptedSliceInput) {
			in.Detail.State.Workspace.CleanupStatus = plan.WorkspaceCleanupStatusRunning
		}, want: InterruptedSliceRefuse},
		{name: "intent present", mutate: func(in *InterruptedSliceInput) {
			in.Detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: "slice"}
			in.Head = "commit"
		}, want: InterruptedSliceCompletionRecovery},
		{name: "completion present", mutate: func(in *InterruptedSliceInput) {
			in.Detail.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "commit"}
			in.Head = "commit"
		}, want: InterruptedSliceCompletionRecovery},
		{name: "current mode", mutate: func(in *InterruptedSliceInput) {
			in.Detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
			in.Detail.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
			in.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
		}, want: InterruptedSliceManualCompletion},
		{name: "conflict", mutate: statusMutation("UU conflict.go"), want: InterruptedSliceRefuse},
		{name: "active operation", mutate: func(in *InterruptedSliceInput) { in.ActiveGitOperation = "rebase" }, want: InterruptedSliceRefuse},
		{name: "ambiguous status", mutate: statusMutation("R  old.go -> new.go"), want: InterruptedSliceRefuse},
		{name: "protected branch", mutate: func(in *InterruptedSliceInput) {
			in.Branch = "main"
			in.Detail.Slices.Slices[0].ExecutionStart.Branch = "main"
		}, want: InterruptedSliceRefuse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := interruptedInput()
			if tt.mutate != nil {
				tt.mutate(&input)
			}
			got := ClassifyInterruptedSlice(input)
			if got.Disposition != tt.want {
				t.Fatalf("disposition = %q (%s), want %q; facts=%#v", got.Disposition, got.Reason, tt.want, got.Facts)
			}
		})
	}
}

func TestInterruptedSliceClassifiesNewAndTornStarts(t *testing.T) {
	input := interruptedInput()
	input.Detail.State.Plan.CurrentSlice = nil
	input.Detail.Slices.Slices[0].Status = plan.StatusPending
	input.Detail.Slices.Slices[0].ExecutionStart = nil
	if got := ClassifyInterruptedSlice(input); got.Disposition != InterruptedSliceNewStart {
		t.Fatalf("new start = %#v", got)
	}

	input = interruptedInput()
	input.Detail.Slices.Slices[0].ExecutionStart = nil
	if got := ClassifyInterruptedSlice(input); got.Disposition != InterruptedSliceCleanStartRepair {
		t.Fatalf("clean torn start = %#v", got)
	}
	input.PorcelainStatus = " M unattributed.go"
	if got := ClassifyInterruptedSlice(input); got.Disposition != InterruptedSliceRefuse {
		t.Fatalf("dirty torn start = %#v", got)
	}
}

func TestInterruptedSliceClassifiesPolicyNoneWithoutExecutionStart(t *testing.T) {
	manualInput := func() InterruptedSliceInput {
		input := interruptedInput()
		input.Detail.State.Plan.CurrentSlice = nil
		input.Detail.Slices.Slices[0].Status = plan.StatusPending
		input.Detail.Slices.Slices[0].ExecutionRoot = ""
		input.Detail.Slices.Slices[0].ExecutionStart = nil
		input.Detail.State.Repo.Root = input.ExecutionRoot
		input.Detail.State.Workspace = nil
		execution := runExecution{
			Config:       ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent}},
			Dependencies: RunDependencies{PlanRecordFactory: memoryPlanRecordFactory},
		}
		if err := startSlice(context.Background(), execution, input.Detail, input.SliceID, time.Now(), input.ExecutionRoot); err != nil {
			t.Fatalf("start policy-none slice: %v", err)
		}
		if input.Detail.Slices.Slices[0].ExecutionStart != nil {
			t.Fatalf("policy-none run unexpectedly recorded execution_start: %#v", input.Detail.Slices.Slices[0].ExecutionStart)
		}
		input.CommitPolicy = CommitPolicyNone.String()
		input.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
		input.PorcelainStatus = " M manual.go"
		return input
	}

	if got := ClassifyInterruptedSlice(manualInput()); got.Disposition != InterruptedSliceManualCompletion {
		t.Fatalf("policy-none interrupted work = %#v, want manual completion", got)
	}

	tests := []struct {
		name   string
		mutate func(*InterruptedSliceInput)
	}{
		{name: "changed root", mutate: func(input *InterruptedSliceInput) { input.ExecutionRoot = "/other" }},
		{name: "conflict", mutate: func(input *InterruptedSliceInput) { input.PorcelainStatus = "UU conflict.go" }},
		{name: "active operation", mutate: func(input *InterruptedSliceInput) { input.ActiveGitOperation = "rebase" }},
		{name: "changed policy", mutate: func(input *InterruptedSliceInput) { input.CommitPolicy = CommitPolicySlice.String() }},
		{name: "changed strategy", mutate: func(input *InterruptedSliceInput) { input.WorkspaceStrategy = plan.WorkspaceStrategyWorktree }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := manualInput()
			tt.mutate(&input)
			if got := ClassifyInterruptedSlice(input); got.Disposition != InterruptedSliceRefuse {
				t.Fatalf("disposition = %q (%s), want refusal", got.Disposition, got.Reason)
			}
		})
	}
}

func TestServiceExecuteClassifiesReloadedPolicyNoneCurrentRunWithoutWorkspaceMetadata(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = nil
	persistRunArtifacts(t, planDir, detail)

	repo := plan.NewFileRepository(plansDir)
	providerErr := errors.New("provider stopped")
	agentCalls := 0
	service := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runGitFake(&[]string{}, nil),
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return providerErr
		}),
	}})
	request := Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeCurrent, CommitPolicy: CommitPolicyNone,
	}}
	if err := service.Execute(context.Background(), request); !errors.Is(err, providerErr) {
		t.Fatalf("first execute error = %v, want provider error", err)
	}

	reloaded, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	slice := reloaded.Slices.Slices[0]
	if reloaded.State.Workspace != nil || slice.ExecutionStart != nil || slice.ExecutionRoot != repoRoot {
		t.Fatalf("reloaded policy-none artifacts = workspace:%#v slice:%#v", reloaded.State.Workspace, slice)
	}

	err = service.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "requires manual completion") || !strings.Contains(err.Error(), `workspace="current"`) {
		t.Fatalf("second execute error = %v, want current-checkout manual completion", err)
	}
	if agentCalls != 1 {
		t.Fatalf("agent calls = %d, want no retry after classifying manual completion", agentCalls)
	}
}

func TestInterruptedSliceClassifiesBlockedContinuation(t *testing.T) {
	withBoundary := interruptedInput()
	withBoundary.Detail.State.Status = plan.StatusBlocked
	withBoundary.Detail.Slices.Slices[0].Status = plan.StatusBlocked
	if got := ClassifyInterruptedSlice(withBoundary); got.Disposition != InterruptedSliceRefuse {
		t.Fatalf("blocked slice without continue = %#v, want refusal", got)
	}
	withBoundary.ContinueBlocked = true
	if got := ClassifyInterruptedSlice(withBoundary); got.Disposition != InterruptedSliceBlockedContinue || got.ContinuationDisposition != InterruptedSliceResume {
		t.Fatalf("blocked slice with boundary = %#v, want validated resume continuation", got)
	}
	withBoundary.Branch = "other"
	if got := ClassifyInterruptedSlice(withBoundary); got.Disposition != InterruptedSliceRefuse {
		t.Fatalf("blocked slice with boundary drift = %#v, want refusal before continuation", got)
	}

	withoutBoundary := interruptedInput()
	withoutBoundary.Detail.State.Status = plan.StatusBlocked
	withoutBoundary.Detail.Slices.Slices[0].Status = plan.StatusBlocked
	withoutBoundary.Detail.Slices.Slices[0].ExecutionRoot = ""
	withoutBoundary.Detail.Slices.Slices[0].ExecutionStart = nil
	withoutBoundary.ContinueBlocked = true
	if got := ClassifyInterruptedSlice(withoutBoundary); got.Disposition != InterruptedSliceBlockedContinue || got.ContinuationDisposition != InterruptedSliceNewStart {
		t.Fatalf("blocked slice without boundary = %#v, want fresh-start continuation", got)
	}
}

func TestInterruptedSliceCanonicalBlockMatchesHandEditedState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*InterruptedSliceInput)
		want   InterruptedSliceDisposition
	}{
		{
			name: "blockedFreshStart",
			mutate: func(input *InterruptedSliceInput) {
				slice := &input.Detail.Slices.Slices[0]
				slice.ExecutionRoot = ""
				slice.ExecutionStart = nil
				slice.CommitIntent = nil
				slice.Completion = nil
			},
			want: InterruptedSliceNewStart,
		},
		{name: "preservedWork", want: InterruptedSliceResume},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical := interruptedInput()
			handEdited := interruptedInput()
			if tt.mutate != nil {
				tt.mutate(&canonical)
				tt.mutate(&handEdited)
			}

			handEdited.Detail.State.Status = plan.StatusBlocked
			handEdited.Detail.Slices.Slices[0].Status = plan.StatusBlocked
			handEdited.ContinueBlocked = true

			record, err := plan.NewPlanRecordWithStore(interruptedArtifactStore{}, "/plans/plan-a", canonical.Detail)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.BlockSlice(canonical.SliceID, "agent stopped", time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			canonical.ContinueBlocked = true

			canonicalResult := ClassifyInterruptedSlice(canonical)
			handEditedResult := ClassifyInterruptedSlice(handEdited)
			if canonicalResult.Disposition != InterruptedSliceBlockedContinue || canonicalResult.ContinuationDisposition != tt.want {
				t.Fatalf("canonical block classification = %#v, want continuation %q", canonicalResult, tt.want)
			}
			if canonicalResult.Disposition != handEditedResult.Disposition || canonicalResult.ContinuationDisposition != handEditedResult.ContinuationDisposition || canonicalResult.Reason != handEditedResult.Reason {
				t.Fatalf("canonical classification %#v differs from hand-edited classification %#v", canonicalResult, handEditedResult)
			}
		})
	}
}

func TestServiceExecuteRefusesClaimedWorktreeAtControlCheckout(t *testing.T) {
	controlRoot := t.TempDir()
	detail := interruptedServiceRunDetail(t, controlRoot)
	detail.State.Repo.Root = controlRoot
	agentCalls := 0
	gitCalls := 0

	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			gitCalls++
			return errors.New("Git must not inspect a workspace with invalid durable identity")
		},
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return nil
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if err == nil || !strings.Contains(err.Error(), "worktree is not separate from the repository checkout") {
		t.Fatalf("execute error = %v, want unsafe worktree identity refusal", err)
	}
	if gitCalls != 0 || agentCalls != 0 {
		t.Fatalf("git calls = %d, agent calls = %d; want refusal before inspecting or resuming control checkout", gitCalls, agentCalls)
	}
}

func TestServiceExecuteRefusesWorktreePathResolvingIntoControlCheckout(t *testing.T) {
	controlRoot := t.TempDir()
	runCommitTestGitCommand(t, controlRoot, "init")

	tests := []struct {
		name       string
		workspace  func(*testing.T) string
		wantReason string
	}{
		{
			name: "symlink to control checkout",
			workspace: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "claimed-worktree")
				if err := os.Symlink(controlRoot, path); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantReason: "physically resolves to the repository checkout",
		},
		{
			name: "stale nested workspace path",
			workspace: func(t *testing.T) string {
				path := filepath.Join(controlRoot, "stale", "nested-workspace")
				if err := os.MkdirAll(path, 0o750); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantReason: "Git top-level does not match the recorded execution root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceRoot := tt.workspace(t)
			detail := interruptedServiceRunDetail(t, workspaceRoot)
			detail.State.Repo.Root = controlRoot
			agentCalls := 0

			err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner: defaultCommandRunner,
				SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
					agentCalls++
					return nil
				}),
			}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
				ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
			}})
			if err == nil || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("execute error = %v, want identity refusal containing %q", err, tt.wantReason)
			}
			if agentCalls != 0 {
				t.Fatalf("agent calls = %d, want refusal before resume", agentCalls)
			}
		})
	}
}

func TestServiceExecuteContinuesBlockedAutomaticSliceWithBoundary(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	detail.State.Status = plan.StatusBlocked
	detail.Slices.Slices[0].Status = plan.StatusBlocked
	providerErr := errors.New("provider stopped")
	prepared := 0
	continued := false
	var gotRun SliceRun

	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base"),
		EventAppender: eventAppenderFunc(func(string, plan.Event) error { return nil }),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return root, nil
		},
		PlanRecordFactory: callbackPlanRecordFactory(nil, func(detail *plan.PlanDetail, now time.Time) error {
			continued = true
			return plan.MarkBlockedContinued(detail, now)
		}),
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			gotRun = run
			return providerErr
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		Continue: true, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("execute error = %v, want provider error", err)
	}
	if !continued || prepared != 0 || !gotRun.Resuming || gotRun.RepoRoot != root {
		t.Fatalf("continued=%t prepared=%d run=%+v, want in-place blocked resume", continued, prepared, gotRun)
	}
	if detail.Slices.Slices[0].ExecutionStart == nil || detail.Slices.Slices[0].ExecutionStart.Head != "base" {
		t.Fatalf("continuation changed execution boundary: %#v", detail.Slices.Slices[0].ExecutionStart)
	}
}

func TestServiceExecuteContinuesBlockedAutomaticSliceWithoutBoundary(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	detail.State.Status = plan.StatusBlocked
	detail.Slices.Slices[0].Status = plan.StatusBlocked
	detail.Slices.Slices[0].ExecutionRoot = ""
	detail.Slices.Slices[0].ExecutionStart = nil
	providerErr := errors.New("provider stopped")
	prepared := 0
	var gotRun SliceRun

	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		EventAppender: eventAppenderFunc(func(string, plan.Event) error { return nil }),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return root, nil
		},
		PlanRecordFactory: memoryPlanRecordFactory,
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			gotRun = run
			return providerErr
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		Continue: true, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("execute error = %v, want provider error", err)
	}
	if prepared != 1 || gotRun.Resuming || gotRun.RepoRoot != root {
		t.Fatalf("prepared=%d run=%+v, want ordinary fresh start", prepared, gotRun)
	}
	slice := detail.Slices.Slices[0]
	if slice.Status != plan.StatusInProgress || slice.ExecutionRoot != root || slice.ExecutionStart == nil {
		t.Fatalf("continued fresh slice = %#v", slice)
	}
	if slice.ExecutionStart.Branch != "tao/plan-a" || slice.ExecutionStart.Head != "base" {
		t.Fatalf("fresh execution boundary = %#v", slice.ExecutionStart)
	}
}

func TestServiceExecuteRepairsLaterStateAdvancedSliceStartAfterReload(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	firstStarted := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	firstCompleted := firstStarted.Add(time.Minute)
	secondStarted := firstCompleted.Add(time.Minute)
	detail := runPlanDetail(plan.StatusInProgress, []string{"002-b"}, []string{"001-a"}, "002-b", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	detail.State.Repo.Branch = "master"
	detail.State.Plan.Timing.StartedAt = &firstStarted
	detail.State.Plan.LastRunCommitPolicy = CommitPolicySlice.String()
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Path: workspaceRoot,
		Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}
	first := plan.Slice{
		ID: "001-a", Status: plan.StatusCompleted, ExecutionRoot: workspaceRoot,
		ExecutionStart: &plan.SliceExecutionStart{Branch: "tao/plan-a", Head: "base", CommitPolicy: CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree},
		CommitIntent:   &plan.SliceCommitIntent{Hash: "first-intent", Policy: CommitPolicySlice.String(), StartingBranch: "tao/plan-a", StartingHead: "base", CreatedAt: firstCompleted},
		Completion:     &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "first-commit"},
		Timing:         plan.SliceTiming{StartedAt: &firstStarted, CompletedAt: &firstCompleted},
		Verification:   plan.Verification{Commands: []string{"go test ."}},
	}
	detail.Slices.Slices = append([]plan.Slice{first}, detail.Slices.Slices...)
	persistRunArtifacts(t, planDir, detail)

	failedStore := &stateOnlyStartStore{writeSlicesErr: errors.New("injected later-slice write failure")}
	failedExecution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}, RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "first-commit"),
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return plan.NewPlanRecordWithStore(failedStore, detail.Dir, detail)
		},
	})
	if err := startSlice(context.Background(), failedExecution, detail, "002-b", secondStarted, workspaceRoot); err == nil || !strings.Contains(err.Error(), "injected later-slice write failure") {
		t.Fatalf("start error = %v, want injected later-slice write failure", err)
	}

	repo := plan.NewFileRepository(plansDir)
	reloaded, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.CurrentSlice == nil || *reloaded.State.Plan.CurrentSlice != "002-b" || reloaded.Slices.Slices[1].Status != plan.StatusPending {
		t.Fatalf("reloaded torn later start = state:%#v slice:%#v", reloaded.State.Plan, reloaded.Slices.Slices[1])
	}
	if reloaded.State.Workspace == nil || reloaded.State.Workspace.HeadSHA != "first-commit" {
		t.Fatalf("persisted workspace boundary = %#v, want first-commit", reloaded.State.Workspace)
	}

	providerErr := errors.New("provider stopped after later start repair")
	prepared := 0
	err = NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "first-commit"),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return "", errors.New("workspace preparation must not run during later torn-start repair")
		},
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			if run.RepoRoot != workspaceRoot || !run.Resuming {
				t.Fatalf("repaired later run = root:%q resuming:%t", run.RepoRoot, run.Resuming)
			}
			return providerErr
		}),
	}}).Execute(context.Background(), Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("repair run error = %v, want provider error", err)
	}
	if prepared != 0 {
		t.Fatalf("workspace preparer calls = %d, want none", prepared)
	}

	repaired, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	second := repaired.Slices.Slices[1]
	if second.Status != plan.StatusInProgress || second.ExecutionStart == nil || second.ExecutionStart.Head != "first-commit" {
		t.Fatalf("repaired later slice = %#v", second)
	}
}

func TestServiceExecuteRepairsSlicesAdvancedStartAndMissingEventAfterReload(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	detail := interruptedServiceRunDetail(t, workspaceRoot)
	detail.Dir = planDir
	detail.Slices.Slices[0].ExecutionStart = nil
	detail.Events = nil
	startedAt := *detail.Slices.Slices[0].Timing.StartedAt
	persistRunArtifacts(t, planDir, detail)

	repo := plan.NewFileRepository(plansDir)
	providerErr := errors.New("provider stopped after slices-advanced repair")
	prepared := 0
	err := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return "", errors.New("workspace preparation must not run during slices-advanced repair")
		},
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			if run.RepoRoot != workspaceRoot || !run.Resuming {
				t.Fatalf("repaired run = root:%q resuming:%t", run.RepoRoot, run.Resuming)
			}
			return providerErr
		}),
	}}).Execute(context.Background(), Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("repair run error = %v, want provider error", err)
	}
	if prepared != 0 {
		t.Fatalf("workspace preparer calls = %d, want none", prepared)
	}

	repaired, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	slice := repaired.Slices.Slices[0]
	wantBoundary := plan.SliceExecutionStart{
		Branch: "tao/plan-a", Head: "base", CommitPolicy: CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree,
	}
	if slice.Status != plan.StatusInProgress || slice.ExecutionRoot != workspaceRoot || slice.ExecutionStart == nil || *slice.ExecutionStart != wantBoundary {
		t.Fatalf("repaired slices-advanced start = %#v", slice)
	}
	if slice.Timing.StartedAt == nil || !slice.Timing.StartedAt.Equal(startedAt) {
		t.Fatalf("repair changed started_at: %#v", slice.Timing.StartedAt)
	}
	var startedEvent *plan.Event
	for i := range repaired.Events {
		if repaired.Events[i].Type == plan.EventTypeSliceStarted && repaired.Events[i].SliceID == "001-a" {
			if startedEvent != nil {
				t.Fatalf("repair duplicated slice_started event: %#v", repaired.Events[i])
			}
			startedEvent = &repaired.Events[i]
		}
	}
	if startedEvent == nil || !startedEvent.Timestamp.Equal(startedAt) {
		t.Fatalf("repaired slice_started event = %#v, want timestamp %s", startedEvent, startedAt)
	}
}

func TestServiceExecuteResumesStartSettledFromJournalOnReload(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}

	settled := interruptedServiceRunDetail(t, workspaceRoot)
	settled.Dir = planDir
	settled.State.Schema = "tao.plan.state.v1"
	settled.Slices.Schema = "tao.plan.slices.v1"
	settled.Slices.PlanID = "plan-a"
	settled.Events[0].MutationID = "restart-start"
	base := cloneRunRestartDetail(t, settled)
	base.State.Status = plan.StatusPlanned
	base.State.Plan.CurrentSlice = nil
	base.State.Plan.LastRunCommitPolicy = ""
	base.State.Plan.Timing.StartedAt = nil
	base.Slices.Slices[0].Status = plan.StatusPending
	base.Slices.Slices[0].ExecutionRoot = ""
	base.Slices.Slices[0].ExecutionStart = nil
	base.Slices.Slices[0].Timing.StartedAt = nil
	base.Events = nil
	persistRunArtifacts(t, planDir, base)
	writeRunRestartJournal(t, planDir, "restart-start", &settled.State, &settled.Slices, settled.Events)

	providerErr := errors.New("provider stopped after recovered start")
	prepared := 0
	err := NewService(plan.NewFileRepository(plansDir), io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return "", errors.New("recovered start must not prepare another workspace")
		},
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			if !run.Resuming || run.RepoRoot != workspaceRoot {
				t.Fatalf("recovered run = root:%q resuming:%t", run.RepoRoot, run.Resuming)
			}
			return providerErr
		}),
	}}).Execute(context.Background(), Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("execute error = %v, want provider interruption", err)
	}
	if prepared != 0 {
		t.Fatalf("workspace preparer calls = %d, want none", prepared)
	}

	reloaded, err := plan.NewFileRepository(plansDir).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.CurrentSlice == nil || *reloaded.State.Plan.CurrentSlice != "001-a" || reloaded.Slices.Slices[0].ExecutionStart == nil {
		t.Fatalf("recovered start was not retained: state=%#v slice=%#v", reloaded.State.Plan, reloaded.Slices.Slices[0])
	}
	if got := countPlanEvents(reloaded.Events, plan.EventTypeSliceStarted); got != 1 {
		t.Fatalf("slice_started events = %d, want one recovered event", got)
	}
	if _, statErr := os.Stat(filepath.Join(planDir, ".mutation.json")); !os.IsNotExist(statErr) {
		t.Fatalf("settled start journal remains: %v", statErr)
	}
}

func cloneRunRestartDetail(t *testing.T, detail *plan.PlanDetail) *plan.PlanDetail {
	t.Helper()
	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var clone plan.PlanDetail
	if err := json.Unmarshal(payload, &clone); err != nil {
		t.Fatal(err)
	}
	clone.Dir = detail.Dir
	return &clone
}

type runRestartPayload struct {
	Payload []byte `json:"payload"`
	SHA256  string `json:"sha256"`
}

type runRestartJournal struct {
	Schema     string              `json:"schema"`
	MutationID string              `json:"mutation_id"`
	PlanID     string              `json:"plan_id"`
	CreatedAt  time.Time           `json:"created_at"`
	State      *runRestartPayload  `json:"state,omitempty"`
	Slices     *runRestartPayload  `json:"slices,omitempty"`
	Events     []runRestartPayload `json:"events,omitempty"`
}

func writeRunRestartJournal(t *testing.T, planDir, mutationID string, state *plan.State, slicesFile *plan.SlicesFile, events []plan.Event) {
	t.Helper()
	journal := runRestartJournal{Schema: "tao.plan.mutation.v1", MutationID: mutationID, PlanID: "plan-a", CreatedAt: time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)}
	if state != nil {
		journal.State = runRestartJSONPayload(t, state, true)
	}
	if slicesFile != nil {
		journal.Slices = runRestartJSONPayload(t, slicesFile, true)
	}
	for i := range events {
		events[i].MutationID = mutationID
		journal.Events = append(journal.Events, *runRestartJSONPayload(t, events[i], false))
	}
	payload, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(filepath.Join(planDir, ".mutation.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRestartJSONPayload(t *testing.T, value any, indent bool) *runRestartPayload {
	t.Helper()
	var payload []byte
	var err error
	if indent {
		payload, err = json.MarshalIndent(value, "", "  ")
		payload = append(payload, '\n')
	} else {
		payload, err = json.Marshal(value)
	}
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	return &runRestartPayload{Payload: payload, SHA256: hex.EncodeToString(sum[:])}
}

func TestInterruptedSliceStrictlyInfersLegacyBoundary(t *testing.T) {
	input := interruptedInput()
	input.Detail.Slices.Slices[0].ExecutionStart.CommitPolicy = ""
	input.Detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = ""
	if got := ClassifyInterruptedSlice(input); got.Disposition != InterruptedSliceResume {
		t.Fatalf("legacy inference = %#v", got)
	}
	input.Detail.State.Plan.LastRunCommitPolicy = ""
	if got := ClassifyInterruptedSlice(input); got.Disposition != InterruptedSliceResume {
		t.Fatalf("legacy execution boundary policy inference = %#v", got)
	}
	input.Detail.State.Workspace = nil
	if got := ClassifyInterruptedSlice(input); got.Disposition != InterruptedSliceRefuse {
		t.Fatalf("missing legacy strategy = %#v", got)
	}
}

type interruptedArtifactStore struct{}

func (interruptedArtifactStore) WriteState(string, plan.State) error       { return nil }
func (interruptedArtifactStore) WriteSlices(string, plan.SlicesFile) error { return nil }
func (interruptedArtifactStore) AppendEvent(string, plan.Event) error      { return nil }

func statusMutation(status string) func(*InterruptedSliceInput) {
	return func(input *InterruptedSliceInput) { input.PorcelainStatus = status }
}

func interruptedInput() InterruptedSliceInput {
	current := "001-a"
	root := "/workspace/plan"
	return InterruptedSliceInput{
		Detail: &plan.PlanDetail{
			State: plan.State{
				Repo:      plan.Repo{Root: "/repo/control"},
				Plan:      plan.PlanState{CurrentSlice: &current, PendingSlices: []string{current}, LastRunCommitPolicy: CommitPolicySlice.String()},
				Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: root, Branch: "tao/plan", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady},
			},
			Slices: plan.SlicesFile{Slices: []plan.Slice{{
				ID: current, Status: plan.StatusInProgress, ExecutionRoot: root,
				ExecutionStart: &plan.SliceExecutionStart{Branch: "tao/plan", Head: "base", CommitPolicy: CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree},
			}}},
		},
		SliceID: current, ExecutionRoot: root, WorkspaceStrategy: plan.WorkspaceStrategyWorktree,
		CommitPolicy: CommitPolicySlice.String(), Branch: "tao/plan", Head: "base",
	}
}
