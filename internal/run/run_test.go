package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestRunFinalizesPlanCompletedByCurrentInvocation(t *testing.T) {
	started := time.Now().UTC().Add(-2 * time.Minute)
	completed := time.Now().UTC()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &started, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &started, &completed)
	completedDetail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, PlanID: "plan-a", SliceID: "001-a", Metrics: &plan.AgentMetrics{SessionID: "session-a", Status: plan.StatusCompleted, TotalTokens: 12, Cost: 0.5}}}
	var out bytes.Buffer
	var calls []string

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Running slice 001-a", "Slice completed: 001-a", "Plan slices complete: plan-a", "Summary: 1/1 slices completed", "Agent: 1 session(s), 12 token(s), $0.5000 cost", "git merge feature"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
	if !runHasGitCall(calls, "checkout main") {
		t.Fatalf("expected checkout main call, got %#v", calls)
	}
}

func TestRunDoesNotFinalizeAlreadyCompletePlan(t *testing.T) {
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	var calls []string

	if err := executeDetail(context.Background(), detail, nil, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{CommandRunner: runGitFake(&calls, nil)}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Plan slices complete: plan-a\n" {
		t.Fatalf("unexpected output %q", got)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no git calls, got %#v", calls)
	}
}

func TestRunPrintsFreshReviewHintForAlreadyInReviewPlan(t *testing.T) {
	detail := runPlanDetail(plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer

	if err := executeDetail(context.Background(), detail, nil, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}}); err != nil {
		t.Fatal(err)
	}
	want := "Plan slices complete: plan-a\nNext: run `tao review --run plan-a` to request a fresh review.\n"
	if got := out.String(); got != want {
		t.Fatalf("unexpected output %q", got)
	}
}

func TestFinalizerDoesNotFinalizeAlreadyCompletePlan(t *testing.T) {
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	var calls []string
	finalizer := newFinalizer(&out, testRunExecutionWithOptions(Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyPlan, PullRequest: true}}, RunDependencies: RunDependencies{CommandRunner: runGitFake(&calls, nil)}}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 0, detail, plan.Derive(detail, time.Time{}).Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected completed plan to stop execution")
	}
	if got := out.String(); got != "Plan slices complete: plan-a\n" {
		t.Fatalf("unexpected output %q", got)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no git calls, got %#v", calls)
	}
}

func TestFinalizerPrintsFreshReviewHintForAlreadyInReviewPlan(t *testing.T) {
	detail := runPlanDetail(plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	finalizer := newFinalizer(&out, testRunExecutionWithOptions(Options{}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 0, detail, plan.Derive(detail, time.Time{}).Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected completed plan to stop execution")
	}
	want := "Plan slices complete: plan-a\nNext: run `tao review --run plan-a` to request a fresh review.\n"
	if got := out.String(); got != want {
		t.Fatalf("unexpected output %q", got)
	}
}

func TestRunDoesNotFinalizeWhenMaxSlicesStopsBeforeCompletion(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a", "002-b"}, nil, "001-a", plan.StatusPending, nil, nil)
	reloaded := runPlanDetail(plan.StatusInProgress, []string{"002-b"}, []string{"001-a"}, "002-b", plan.StatusPending, nil, nil)
	var out bytes.Buffer
	var calls []string

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return reloaded, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{MaxSlices: 1, CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stopped after 1 slice(s); next pending slice: 002-b") {
		t.Fatalf("expected max-slices stop output, got %q", out.String())
	}
	if len(calls) != 0 {
		t.Fatalf("expected no git calls, got %#v", calls)
	}
}

func TestAutomaticSliceRequiresCleanBoundaryWhileNoneIsExempt(t *testing.T) {
	runner := func(_ context.Context, _ string, name string, args []string, stdout io.Writer, _ io.Writer) error {
		if name == "git" && runGitKey(args) == "status --porcelain" {
			_, _ = io.WriteString(stdout, " M README.md\n")
		}
		return nil
	}
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent}}, RunDependencies{CommandRunner: runner})
	if err := requireCleanAutomaticSliceStart(context.Background(), execution, "."); err == nil || !strings.Contains(err.Error(), "commit or stash") || !strings.Contains(err.Error(), "commit policy none") {
		t.Fatalf("expected actionable dirty-start refusal, got %v", err)
	}
	execution.Config.CommitPolicy = CommitPolicyNone
	if err := requireCleanAutomaticSliceStart(context.Background(), execution, "."); err != nil {
		t.Fatalf("commit policy none should be exempt: %v", err)
	}
}

func TestCurrentModeAutomaticSliceRejectsProtectedBranchBeforeAgentWork(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = t.TempDir()
	started := false
	executor := &countingSliceExecutor{}
	runner := func(_ context.Context, _ string, name string, args []string, stdout io.Writer, _ io.Writer) error {
		if name == "git" && runGitKey(args) == "branch --show-current" {
			_, _ = io.WriteString(stdout, "master\n")
		}
		return nil
	}

	err := executeDetail(context.Background(), detail, nil, io.Discard, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent}},
		RunDependencies: RunDependencies{
			SliceExecutor: executor,
			PlanRecordFactory: callbackPlanRecordFactory(func(*plan.PlanDetail, string, time.Time) error {
				started = true
				return nil
			}, nil),
			CommandRunner: runner,
		},
	})
	if err == nil || !strings.Contains(err.Error(), `unsafe execution branch "master"`) {
		t.Fatalf("expected protected-branch preflight error, got %v", err)
	}
	if started || detail.Slices.Slices[0].Status != plan.StatusPending {
		t.Fatalf("slice started before branch preflight completed: started=%t status=%s", started, detail.Slices.Slices[0].Status)
	}
	if executor.calls != 0 {
		t.Fatalf("agent executor ran %d time(s) on protected branch", executor.calls)
	}
}

func TestAutomaticSliceBoundaryUsesPreparedBranchAndHead(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	runner := func(_ context.Context, _ string, name string, args []string, stdout io.Writer, _ io.Writer) error {
		if name != "git" {
			return nil
		}
		switch runGitKey(args) {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "prepared-feature\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "prepared-head\n")
		}
		return nil
	}
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice}}, RunDependencies{CommandRunner: runner, PlanRecordFactory: memoryPlanRecordFactory})
	execution.StartingBranch = "stale-branch"
	if err := startSlice(context.Background(), execution, detail, "001-a", time.Now().UTC(), "."); err != nil {
		t.Fatal(err)
	}
	got := detail.Slices.Slices[0].ExecutionStart
	if got == nil || got.Branch != "prepared-feature" || got.Head != "prepared-head" {
		t.Fatalf("execution boundary = %#v, want prepared branch/head", got)
	}
}

func TestAutomaticSliceCompletionBoundaryGuardsTransaction(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		head    string
		status  string
		mutate  func(*plan.PlanDetail)
		wantErr string
	}{
		{name: "stable despite unrelated ref movement", branch: "feature", head: "head123"},
		{name: "branch switch", branch: "other", head: "head123", wantErr: "changed execution branch"},
		{name: "execution branch advancement", branch: "feature", head: "other-head", wantErr: "advanced unexpectedly"},
		{name: "leftovers", branch: "feature", head: "head123", status: " M leftover.go\n", wantErr: "leftover worktree changes"},
		{name: "missing outcome", branch: "feature", head: "head123", mutate: func(detail *plan.PlanDetail) { detail.Slices.Slices[0].Completion = nil }, wantErr: "without a committed or no_changes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := runPlanDetail(plan.StatusInProgress, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
			settleRunTestSlice(detail)
			if tt.mutate != nil {
				tt.mutate(detail)
			}
			runner := func(_ context.Context, _ string, name string, args []string, stdout io.Writer, _ io.Writer) error {
				if name != "git" {
					return nil
				}
				switch runGitKey(args) {
				case "branch --show-current":
					_, _ = io.WriteString(stdout, tt.branch)
				case "rev-parse HEAD":
					_, _ = io.WriteString(stdout, tt.head)
				case "status --porcelain":
					_, _ = io.WriteString(stdout, tt.status)
				}
				return nil
			}
			execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice}}, RunDependencies{CommandRunner: runner})
			err := (SelectedSliceRunner{execution: execution}).validateAutomaticSliceBoundary(context.Background(), detail, "001-a", ".")
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunPreflightBlocksInvalidSelectedSliceBeforeExecutor(t *testing.T) {
	repo := t.TempDir()
	detail := &plan.PlanDetail{
		Dir:   "/plans/plan-a",
		State: plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Root: repo}, Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID:             "001-a",
			Status:         plan.StatusPending,
			RequiredInputs: []plan.RequiredInput{{Path: "missing.test.ts", Kind: plan.RequiredInputFile, Reason: "test fixture"}},
			Verification:   plan.Verification{Commands: []string{"go test ."}},
		}}},
	}
	var out bytes.Buffer
	started := false
	executor := &countingSliceExecutor{}

	err := executeDetail(context.Background(), detail, nil, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{
		SliceExecutor: executor,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			return repo, nil
		},
		PlanRecordFactory: callbackPlanRecordFactory(func(detail *plan.PlanDetail, sliceID string, now time.Time) error {
			started = true
			return nil
		}, nil),
	}})
	if err == nil || !strings.Contains(err.Error(), "slice 001-a failed verification preflight") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	if started {
		t.Fatal("expected slice starter not to run before preflight passes")
	}
	if executor.calls != 0 {
		t.Fatalf("expected executor not to run, got %d calls", executor.calls)
	}
	if text := out.String(); !strings.Contains(text, "Verification Findings:") || strings.Contains(text, "Running slice 001-a") {
		t.Fatalf("expected findings without running output, got %q", text)
	}
}

func TestRunPreflightUsesExecutionRootForPathChecks(t *testing.T) {
	repo := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "generated_test.go"), []byte("package generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = repo
	detail.Slices.Slices[0].Verification.Commands = []string{"gofmt -w generated_test.go"}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.State.Repo.Root = repo
	var out bytes.Buffer
	var calls []string
	executor := &countingSliceExecutor{}
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)})
	execution.ExecutionRoot = workspace

	err := executeDetailWithExecution(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, execution)
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("expected executor to run once, got %d", executor.calls)
	}
	if strings.Contains(out.String(), "Verification Findings:") {
		t.Fatalf("expected path check to use execution root without findings, got:\n%s", out.String())
	}
}

func TestRunPreflightChecksRequiredInputsAfterPreparingAuthoritativeRoot(t *testing.T) {
	for _, tt := range []struct {
		name             string
		prepareWorkspace func(*testing.T, string)
		wantFinding      string
	}{
		{name: "producer output absent", wantFinding: "does not exist"},
		{name: "producer output has wrong kind", prepareWorkspace: func(t *testing.T, root string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(root, "generated", "input.txt"), 0o750); err != nil {
				t.Fatal(err)
			}
		}, wantFinding: "is not a file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			controlRoot := t.TempDir()
			workspaceRoot := t.TempDir()
			if err := os.MkdirAll(filepath.Join(controlRoot, "generated"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(controlRoot, "generated", "input.txt"), []byte("control checkout only"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.prepareWorkspace != nil {
				tt.prepareWorkspace(t, workspaceRoot)
			}

			detail := runPlanDetail(plan.StatusPlanned, []string{"002-consumer"}, []string{"001-producer"}, "002-consumer", plan.StatusPending, nil, nil)
			detail.State.Repo.Root = controlRoot
			detail.Slices.Slices = append([]plan.Slice{{
				ID: "001-producer", Status: plan.StatusCompleted, ExpectedFiles: []string{"generated/input.txt"},
			}}, detail.Slices.Slices...)
			detail.Slices.Slices[1].DependsOn = []string{"001-producer"}
			detail.Slices.Slices[1].RequiredInputs = []plan.RequiredInput{{Path: "generated/input.txt", Kind: plan.RequiredInputFile, Reason: "producer output"}}

			prepared := 0
			started := 0
			events := 0
			executor := &countingSliceExecutor{}
			var out bytes.Buffer
			err := executeDetailWithExecution(context.Background(), detail, nil, &out, testRunExecution(
				ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeIsolated}},
				RunDependencies{
					SliceExecutor: executor,
					WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
						prepared++
						return workspaceRoot, nil
					},
					PlanRecordFactory: callbackPlanRecordFactory(func(*plan.PlanDetail, string, time.Time) error {
						started++
						return nil
					}, nil),
					EventAppender: eventAppenderFunc(func(string, plan.Event) error {
						events++
						return nil
					}),
				},
			))
			if err == nil || !strings.Contains(err.Error(), "failed verification preflight") {
				t.Fatalf("execute error = %v, want required-input refusal", err)
			}
			if prepared != 1 {
				t.Fatalf("workspace preparations = %d, want authoritative root prepared once", prepared)
			}
			if started != 0 || events != 0 || executor.calls != 0 {
				t.Fatalf("effects after required-input failure: starts=%d events=%d executor=%d, want none", started, events, executor.calls)
			}
			if !strings.Contains(out.String(), tt.wantFinding) {
				t.Fatalf("findings output = %q, want %q", out.String(), tt.wantFinding)
			}
		})
	}
}

func TestRunPreflightAllowsExpectedFutureFileWarningBeforeRunning(t *testing.T) {
	repo := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = repo
	detail.Slices.Slices[0].ExpectedFiles = []string{"missing.test.ts"}
	detail.Slices.Slices[0].Verification.Commands = []string{"pnpm exec vitest missing.test.ts"}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.State.Repo.Root = repo
	var out bytes.Buffer
	var calls []string
	executor := &countingSliceExecutor{}

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("expected executor to run once, got %d calls", executor.calls)
	}
	text := out.String()
	warningIndex := strings.Index(text, "warning 001-a")
	runningIndex := strings.Index(text, "Running slice 001-a")
	if warningIndex < 0 || runningIndex < 0 || warningIndex > runningIndex {
		t.Fatalf("expected future-file warning before running output, got:\n%s", text)
	}
	for _, want := range []string{"Verification Findings:", "future file declared in this plan", "Slice completed: 001-a"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestRunPreflightPrintsWarningsAndContinues(t *testing.T) {
	repo := t.TempDir()
	detail := &plan.PlanDetail{
		Dir:    "/plans/plan-a",
		State:  plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Root: repo}, Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"pnpm --filter @repo/missing test"}}}}},
	}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	var calls []string
	executor := &countingSliceExecutor{}

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("expected executor to run once, got %d calls", executor.calls)
	}
	text := out.String()
	for _, want := range []string{"Verification Findings:", "warning 001-a", "Running slice 001-a", "Slice completed: 001-a"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestRunPreflightPrintsGuardrailWarningsBeforeRunning(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = t.TempDir()
	detail.Slices.Slices[0].ExpectedFiles = []string{"internal/..."}
	detail.Slices.Slices[0].Verification.Commands = []string{"go test"}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	executor := &countingSliceExecutor{}

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	guardrailIndex := strings.Index(text, "slice expected file")
	runningIndex := strings.Index(text, "Running slice 001-a")
	if guardrailIndex < 0 || runningIndex < 0 || guardrailIndex > runningIndex {
		t.Fatalf("expected guardrail before running output, got:\n%s", text)
	}
	if executor.calls != 1 {
		t.Fatalf("expected warnings not to block executor, got %d calls", executor.calls)
	}
	if detail.Slices.Slices[0].Status == plan.StatusBlocked {
		t.Fatal("expected guardrail warning not to block slice")
	}
}

func TestRunPreflightPrintsBudgetWarningsBeforeRunning(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = t.TempDir()
	detail.Slices.Slices[0].Verification.Commands = []string{"go test"}
	detail.Events = []plan.Event{{
		Type:    plan.EventTypeAgentMetrics,
		PlanID:  "plan-a",
		SliceID: "001-a",
		Metrics: &plan.AgentMetrics{SessionID: "session-a", Status: plan.StatusCompleted, OutputTokens: plan.DefaultAgentBudgetThresholds().Slice.OutputTokens + 1},
	}}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: &countingSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	budgetIndex := strings.Index(text, "Agent Metrics Budget Warnings:")
	runningIndex := strings.Index(text, "Running slice 001-a")
	if budgetIndex < 0 || runningIndex < 0 || budgetIndex > runningIndex {
		t.Fatalf("expected budget warning before running output, got:\n%s", text)
	}
}

func TestRunPassesSelectedSlicePacketToExecutor(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Slices.Slices[0].Goal = "Use packet context"
	detail.Slices.Slices[0].RequiredInputs = []plan.RequiredInput{{Path: "run.go", Kind: plan.RequiredInputFile, Reason: "runtime contract"}}
	detail.Slices.Slices[0].Verification.Commands = []string{"pnpm test"}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	executor := &packetCapturingExecutor{}

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(executor.packet, "# Tao Run Packet") || !strings.Contains(executor.packet, "- Goal: Use packet context") || !strings.Contains(executor.packet, "## Required Inputs\n- run.go (file): runtime contract") || !strings.Contains(executor.packet, "- Commit Policy: none") || !strings.Contains(executor.packet, "- Execution Mode: current") {
		t.Fatalf("expected selected-slice packet, got:\n%s", executor.packet)
	}
	if got := strings.Join(executor.verificationCommands, "\n"); got != "pnpm test" {
		t.Fatalf("executor verification commands = %q", got)
	}
}

func TestRunPassesWorkspaceRootAndControlPlanDirToExecutor(t *testing.T) {
	controlDir := t.TempDir()
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = controlDir
	detail.State.Repo.Root = t.TempDir()
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = controlDir
	var out bytes.Buffer
	executor := &packetCapturingExecutor{}
	prepared := false

	err := executeDetailWithExecution(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, WorkspacePreparer: func(ctx context.Context, detail *plan.PlanDetail, input WorkspaceResolverInput) (string, error) {
		prepared = true
		return workspaceRoot, nil
	}, CommandRunner: runGitFake(&[]string{}, nil)}))
	if err != nil {
		t.Fatal(err)
	}
	if !prepared {
		t.Fatal("expected workspace to be prepared before execution")
	}
	if executor.repoRoot != workspaceRoot {
		t.Fatalf("expected executor repo root %q, got %q", workspaceRoot, executor.repoRoot)
	}
	if executor.planDir != controlDir {
		t.Fatalf("expected executor plan dir %q, got %q", controlDir, executor.planDir)
	}
}

func TestPrepareExecutionWorkspaceDefaultsToWorktree(t *testing.T) {
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = nil
	var calls []string

	root, err := prepareExecutionWorkspace(context.Background(), detail, WorkspaceResolverInput{CommandRunner: runWorkspaceGitFake(&calls)})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	if root != want {
		t.Fatalf("expected default worktree root %q, got %q", want, root)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.Strategy != plan.WorkspaceStrategyWorktree || detail.State.Workspace.Path != want {
		t.Fatalf("expected worktree metadata, got %#v", detail.State.Workspace)
	}
	if !runHasGitCallPrefix(calls, "worktree add ") {
		t.Fatalf("expected worktree preparation, got calls %#v", calls)
	}
}

func TestPrepareExecutionWorkspaceWorktreeRunOptionOverridesPlanCurrent(t *testing.T) {
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	var calls []string

	root, err := prepareExecutionWorkspace(context.Background(), detail, WorkspaceResolverInput{Config: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated}}, CommandRunner: runWorkspaceGitFake(&calls)})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	if root != want {
		t.Fatalf("expected explicit worktree root %q, got %q", want, root)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.Strategy != plan.WorkspaceStrategyWorktree {
		t.Fatalf("expected worktree override metadata, got %#v", detail.State.Workspace)
	}
	if !runHasGitCallPrefix(calls, "worktree add ") {
		t.Fatalf("expected worktree preparation, got calls %#v", calls)
	}
}

func TestServiceExecuteWorkspaceStrategyWorktreeOverridesPlanCurrent(t *testing.T) {
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = detail.Dir
	completedDetail.State.Repo.Root = repoRoot
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, completedDetail}}
	executor := &packetCapturingExecutor{}
	var calls []string

	err := NewService(repo, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runWorkspaceGitFake(&calls)}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated}})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	if executor.repoRoot != want {
		t.Fatalf("expected worktree root %q, got %q", want, executor.repoRoot)
	}
	if !runHasGitCallPrefix(calls, "worktree add ") {
		t.Fatalf("expected worktree preparation, got calls %#v", calls)
	}
}

func TestServiceExecuteRefusesDirtyReadyWorkspaceBeforePreparation(t *testing.T) {
	workspaceRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = t.TempDir()
	detail.State.Repo.Branch = "master"
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(workspaceRoot), Path: workspaceRoot,
		Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}

	preparerCalls := 0
	dependencyCalls := 0
	agentCalls := 0
	runner := interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string {
		return " M unattributed.go\n"
	}, "tao/plan-a", "base")
	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			preparerCalls++
			dependencyCalls++
			return workspaceRoot, nil
		},
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return nil
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if err == nil || !strings.Contains(err.Error(), "ready workspace contains unattributed changes") || !strings.Contains(err.Error(), "unattributed.go") {
		t.Fatalf("execute error = %v, want dirty ready-workspace refusal", err)
	}
	if preparerCalls != 0 || dependencyCalls != 0 || agentCalls != 0 {
		t.Fatalf("preparer=%d dependencies=%d agent=%d, want refusal before all calls", preparerCalls, dependencyCalls, agentCalls)
	}
}

func TestServiceExecuteRefusesActiveSequencerInReadyWorkspaceBeforePreparation(t *testing.T) {
	workspaceRoot := t.TempDir()
	writeLinkedWorktreeSequencer(t, workspaceRoot)
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = t.TempDir()
	detail.State.Repo.Branch = "master"
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(workspaceRoot), Path: workspaceRoot,
		Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}

	preparerCalls := 0
	agentCalls := 0
	runner := interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "base")
	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			preparerCalls++
			return workspaceRoot, nil
		},
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return nil
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if err == nil || !strings.Contains(err.Error(), `Git operation "cherry-pick/revert" is active in the ready workspace`) {
		t.Fatalf("execute error = %v, want sequencer refusal", err)
	}
	if preparerCalls != 0 || agentCalls != 0 {
		t.Fatalf("preparer=%d agent=%d, want refusal before both calls", preparerCalls, agentCalls)
	}
}

func TestServiceExecuteRefusesDirtyUnrecordedManagedWorkspaceBeforePreparation(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	if err := os.MkdirAll(workspaceRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.Branch = "master"
	detail.State.Workspace = nil

	preparerCalls := 0
	agentCalls := 0
	runner := interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string {
		return " M interrupted.go\n"
	}, "tao/plan-a", "base")
	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			preparerCalls++
			return workspaceRoot, nil
		},
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return nil
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if err == nil || !strings.Contains(err.Error(), "unrecorded managed workspace contains unattributed changes") || !strings.Contains(err.Error(), "interrupted.go") {
		t.Fatalf("execute error = %v, want dirty unrecorded-workspace refusal", err)
	}
	if preparerCalls != 0 || agentCalls != 0 {
		t.Fatalf("preparer=%d agent=%d, want refusal before all calls", preparerCalls, agentCalls)
	}
}

func TestServiceExecuteRefusesDirtyRecordedWorkspaceBeforeMutation(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		status   string
		label    string
	}{
		{name: plan.WorkspaceStatusPending, strategy: plan.WorkspaceStrategyWorktree, status: plan.WorkspaceStatusPending, label: "pending workspace"},
		{name: plan.WorkspaceStatusPreparing, strategy: plan.WorkspaceStrategyWorktree, status: plan.WorkspaceStatusPreparing, label: "preparing workspace"},
		{name: plan.WorkspaceStatusFailed, strategy: plan.WorkspaceStrategyWorktree, status: plan.WorkspaceStatusFailed, label: "failed workspace"},
		{name: "legacy lifecycle", strategy: plan.WorkspaceStrategyWorktree, label: "legacy workspace"},
		{name: "legacy empty strategy", status: plan.WorkspaceStatusReady, label: "legacy workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceRoot := t.TempDir()
			detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
			detail.Dir = t.TempDir()
			detail.State.Repo.Root = t.TempDir()
			detail.State.Repo.Branch = "master"
			detail.State.Workspace = &plan.Workspace{
				Strategy: tt.strategy, Root: filepath.Dir(workspaceRoot), Path: workspaceRoot,
				Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: tt.status,
			}

			preparerCalls := 0
			dependencyCalls := 0
			agentCalls := 0
			runner := interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string {
				return " M interrupted.go\n"
			}, "tao/plan-a", "base")
			err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner: runner,
				WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
					preparerCalls++
					dependencyCalls++
					return workspaceRoot, nil
				},
				SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
					agentCalls++
					return nil
				}),
			}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
				ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
			}})
			if err == nil || !strings.Contains(err.Error(), tt.label+" contains unattributed changes") || !strings.Contains(err.Error(), "interrupted.go") {
				t.Fatalf("execute error = %v, want dirty %s refusal", err, tt.label)
			}
			if preparerCalls != 0 || dependencyCalls != 0 || agentCalls != 0 {
				t.Fatalf("preparer=%d dependencies=%d agent=%d, want refusal before all calls", preparerCalls, dependencyCalls, agentCalls)
			}
		})
	}
}

func TestServiceExecuteStartsNextAutomaticSliceAfterMaxSlicesStop(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a", "002-b"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	detail.State.Repo.Branch = "master"
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(workspaceRoot), Path: workspaceRoot,
		Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}
	detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{
		ID: "002-b", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"go test ."}},
	})
	persistRunArtifacts(t, planDir, detail)

	liveHead := "base"
	baseRunner := interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "")
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "rev-parse HEAD" {
			_, _ = io.WriteString(stdout, liveHead+"\n")
			return nil
		}
		return baseRunner(ctx, cwd, name, args, stdout, stderr)
	}
	repo := plan.NewFileRepository(plansDir)
	var runs []string
	executor := sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
		loaded, err := repo.ResolvePlan(ctx, run.PlanDir)
		if err != nil {
			return err
		}
		record, err := repo.PlanRecord(loaded)
		if err != nil {
			return err
		}
		intent := plan.SliceCommitIntent{
			Hash: "intent-" + run.SliceID, Policy: CommitPolicySlice.String(),
			StartingBranch: "tao/plan-a", StartingHead: liveHead, CreatedAt: time.Now().UTC(),
		}
		if err := record.RecordSliceCommitIntent(run.SliceID, intent); err != nil {
			return err
		}
		runs = append(runs, run.SliceID)
		liveHead = "after-" + run.SliceID
		return record.CompleteSliceWithOutcome(run.SliceID, "done", nil, plan.SliceCompletionOutcome{
			Outcome: plan.SliceCompletionCommitted, CommitSHA: liveHead,
		}, time.Now().UTC())
	})
	newService := func() Service {
		return NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
			CommandRunner: runner,
			WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
				return workspaceRoot, nil
			},
			SliceExecutor: executor,
		}})
	}
	request := Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, MaxSlices: 1,
	}}
	if err := newService().Execute(context.Background(), request); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	afterFirst, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.State.Workspace == nil || afterFirst.State.Workspace.HeadSHA != "after-001-a" {
		t.Fatalf("workspace HEAD after first completion = %#v, want after-001-a", afterFirst.State.Workspace)
	}
	if err := newService().Execute(context.Background(), request); err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if !slices.Equal(runs, []string{"001-a", "002-b"}) {
		t.Fatalf("automatic slice runs = %v, want both slices across two executions", runs)
	}
}

func TestCompletedAutomaticSliceProvesBoundaryRequiresConsistentLatestEvidence(t *testing.T) {
	newDetail := func() *plan.PlanDetail {
		detail := runPlanDetail(plan.StatusInProgress, []string{"002-b"}, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
		detail.State.Workspace = &plan.Workspace{
			Strategy: plan.WorkspaceStrategyWorktree, Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
		}
		slice := &detail.Slices.Slices[0]
		slice.ExecutionStart = &plan.SliceExecutionStart{Branch: "tao/plan-a", Head: "base"}
		slice.CommitIntent = &plan.SliceCommitIntent{Policy: CommitPolicySlice.String(), StartingBranch: "tao/plan-a", StartingHead: "base"}
		slice.Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "after-001-a"}
		return detail
	}
	tests := []struct {
		name   string
		mutate func(*plan.PlanDetail)
		want   bool
	}{
		{name: "legacy evidence", mutate: func(*plan.PlanDetail) {}, want: true},
		{name: "workspace does not mirror execution start", mutate: func(detail *plan.PlanDetail) { detail.State.Workspace.HeadSHA = "other" }},
		{name: "commit intent disagrees", mutate: func(detail *plan.PlanDetail) { detail.Slices.Slices[0].CommitIntent.StartingHead = "other" }},
		{name: "matching completion is not latest", mutate: func(detail *plan.PlanDetail) {
			detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, "later")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := newDetail()
			tt.mutate(detail)
			if got := completedAutomaticSliceProvesBoundary(detail, "tao/plan-a", "after-001-a"); got != tt.want {
				t.Fatalf("completedAutomaticSliceProvesBoundary() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestServiceExecuteMigratesLegacyCompletedSliceWorkspaceHead(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusInProgress, []string{"002-b", "003-c"}, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	detail.State.Repo.Branch = "master"
	detail.State.Plan.LastRunCommitPolicy = CommitPolicySlice.String()
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(workspaceRoot), Path: workspaceRoot,
		Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}
	detail.Slices.Slices[0].ExecutionRoot = workspaceRoot
	// This is the execution_start/workspace shape written before completion
	// refreshed workspace.head_sha: the automatic boundary omitted its newer
	// policy/strategy fields and the workspace still mirrors the pre-slice HEAD.
	detail.Slices.Slices[0].ExecutionStart = &plan.SliceExecutionStart{Branch: "tao/plan-a", Head: "base"}
	detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Hash: "legacy-intent", Policy: CommitPolicySlice.String(), StartingBranch: "tao/plan-a", StartingHead: "base"}
	detail.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "after-001-a"}
	detail.Slices.Slices = append(detail.Slices.Slices,
		plan.Slice{ID: "002-b", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"go test ."}}},
		plan.Slice{ID: "003-c", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"go test ."}}},
	)
	persistRunArtifacts(t, planDir, detail)

	repo := plan.NewFileRepository(plansDir)
	runner := interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "after-001-a")
	prepared := 0
	var runs []string
	service := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		WorkspacePreparer: func(ctx context.Context, detail *plan.PlanDetail, _ WorkspaceResolverInput) (string, error) {
			prepared++
			if detail.State.Workspace.HeadSHA != "after-001-a" {
				t.Fatalf("workspace HEAD before preparation = %q, want migrated completion HEAD", detail.State.Workspace.HeadSHA)
			}
			persisted, err := repo.ResolvePlan(ctx, detail.Dir)
			if err != nil {
				return "", err
			}
			if persisted.State.Workspace == nil || persisted.State.Workspace.HeadSHA != "after-001-a" {
				t.Fatalf("persisted workspace boundary before preparation = %#v", persisted.State.Workspace)
			}
			return workspaceRoot, nil
		},
		SliceExecutor: sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
			runs = append(runs, run.SliceID)
			loaded, err := repo.ResolvePlan(ctx, run.PlanDir)
			if err != nil {
				return err
			}
			record, err := repo.PlanRecord(loaded)
			if err != nil {
				return err
			}
			if err := record.RecordSliceCommitIntent(run.SliceID, plan.SliceCommitIntent{
				Hash: "intent-" + run.SliceID, Policy: CommitPolicySlice.String(),
				StartingBranch: "tao/plan-a", StartingHead: "after-001-a", CreatedAt: time.Now().UTC(),
			}); err != nil {
				return err
			}
			return record.CompleteSliceWithOutcome(run.SliceID, "done", nil, plan.SliceCompletionOutcome{
				Outcome: plan.SliceCompletionNoChanges, CommitSHA: "after-001-a",
			}, time.Now().UTC())
		}),
	}})
	request := Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, MaxSlices: 1,
	}}
	if err := service.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if prepared != 1 || !slices.Equal(runs, []string{"002-b"}) {
		t.Fatalf("preparer calls=%d runs=%v, want one migrated next-slice run", prepared, runs)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Workspace == nil || reloaded.State.Workspace.HeadSHA != "after-001-a" {
		t.Fatalf("reloaded workspace boundary = %#v", reloaded.State.Workspace)
	}
	if start := reloaded.Slices.Slices[1].ExecutionStart; start == nil || start.Branch != "tao/plan-a" || start.Head != "after-001-a" {
		t.Fatalf("next slice execution boundary = %#v", start)
	}
}

func TestServiceExecuteResumesInterruptedAutomaticSliceBeforeWorkspaceMutation(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	completed := interruptedServiceRunDetail(t, root)
	completed.Dir = detail.Dir
	completed.State.Status = plan.StatusCompleted
	completed.State.Plan.CurrentSlice = nil
	completed.State.Plan.PendingSlices = nil
	completed.State.Plan.CompletedSlices = []string{"001-a"}
	completed.Slices.Slices[0].Status = plan.StatusCompleted
	completed.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: CommitPolicySlice.String()}
	completed.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "base"}
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, completed}}
	prepared := 0
	started := 0
	agentCalled := false
	var calls []string
	runner := interruptedServiceGitRunner(t, root, &calls, func() string {
		if agentCalled {
			return ""
		}
		return "M  staged.go\n?? new.go\n"
	}, "tao/plan-a", "base")
	executor := sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
		agentCalled = true
		if run.RepoRoot != root {
			t.Fatalf("resumed root = %q, want %q", run.RepoRoot, root)
		}
		if !run.Resuming || run.ResumeAttempt != 1 {
			t.Fatalf("resume context = resuming:%t attempt:%d, want true/1", run.Resuming, run.ResumeAttempt)
		}
		if !strings.Contains(run.RunPacket, "## Interrupted Slice Resume") || !strings.Contains(run.RunPacket, "- Resume Attempt: 1") {
			t.Fatalf("resume run packet missing context:\n%s", run.RunPacket)
		}
		return nil
	})

	var out bytes.Buffer
	err := NewService(repo, &out, Options{RunDependencies: RunDependencies{
		CommandRunner: runner, SliceExecutor: executor, EventAppender: eventAppenderFunc(func(string, plan.Event) error { return nil }),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return root, nil
		},
		PlanRecordFactory: callbackPlanRecordFactory(func(*plan.PlanDetail, string, time.Time) error {
			started++
			return nil
		}, nil),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, MaxSlices: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !agentCalled || prepared != 0 || started != 0 {
		t.Fatalf("agent=%t prepared=%d started=%d, want resumed agent without preparation or start", agentCalled, prepared, started)
	}
	if detail.Slices.Slices[0].ExecutionStart.Head != "base" || detail.State.Plan.LastRunStartingDirty == nil || len(detail.State.Plan.LastRunStartingDirty) != 0 {
		t.Fatalf("original clean boundary changed: slice=%#v dirty=%#v", detail.Slices.Slices[0].ExecutionStart, detail.State.Plan.LastRunStartingDirty)
	}
	if runHasGitCallPrefix(calls, "rebase ") || runHasGitCallPrefix(calls, "worktree ") {
		t.Fatalf("resume mutated workspace: calls=%#v", calls)
	}
	for _, want := range []string{"Resuming slice 001-a at recorded tao/plan-a@base", "preserving staged.go, new.go"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("resume diagnostic missing %q: %q", want, out.String())
		}
	}
}

func TestServiceExecuteResumeRevalidatesExactBoundaryBeforeAgentHandoff(t *testing.T) {
	tests := []struct {
		name   string
		runner func(*testing.T, string) CommandRunner
	}{
		{
			name: "branch drift",
			runner: func(t *testing.T, root string) CommandRunner {
				calls := 0
				base := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base")
				return func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if runGitKey(args) == "branch --show-current" {
						calls++
						branch := "tao/plan-a"
						if calls > 1 {
							branch = "other"
						}
						_, _ = io.WriteString(stdout, branch+"\n")
						return nil
					}
					return base(ctx, cwd, name, args, stdout, stderr)
				}
			},
		},
		{
			name: "HEAD drift",
			runner: func(t *testing.T, root string) CommandRunner {
				calls := 0
				base := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base")
				return func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if runGitKey(args) == "rev-parse HEAD" {
						calls++
						head := "base"
						if calls > 1 {
							head = "advanced"
						}
						_, _ = io.WriteString(stdout, head+"\n")
						return nil
					}
					return base(ctx, cwd, name, args, stdout, stderr)
				}
			},
		},
		{
			name: "status drift",
			runner: func(t *testing.T, root string) CommandRunner {
				calls := 0
				return interruptedServiceGitRunner(t, root, &[]string{}, func() string {
					calls++
					if calls > 1 {
						return " M other.go\n"
					}
					return " M partial.go\n"
				}, "tao/plan-a", "base")
			},
		},
		{
			name: "Git operation drift",
			runner: func(t *testing.T, root string) CommandRunner {
				calls := 0
				return interruptedServiceGitRunner(t, root, &[]string{}, func() string {
					calls++
					if calls > 1 {
						writeLinkedWorktreeSequencer(t, root)
					}
					return " M partial.go\n"
				}, "tao/plan-a", "base")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			agentCalls := 0
			var events []plan.Event
			var out bytes.Buffer
			err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, &out, Options{RunDependencies: RunDependencies{
				CommandRunner: tt.runner(t, root),
				EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
					events = append(events, event)
					return nil
				}),
				SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
					agentCalls++
					return nil
				}),
			}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
				ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
			}})
			if err == nil || !strings.Contains(err.Error(), "before agent handoff") {
				t.Fatalf("execute error = %v, want handoff boundary refusal", err)
			}
			if agentCalls != 0 || strings.Contains(out.String(), "Resuming slice") {
				t.Fatalf("agent calls=%d output=%q, want refusal before prompt handoff", agentCalls, out.String())
			}
			for _, event := range events {
				if event.Type == plan.EventTypeSliceResumeAttempted {
					t.Fatalf("resume-attempt event recorded before rejected handoff: %+v", event)
				}
			}
		})
	}
}

func TestServiceExecuteRefusesInterruptedAutomaticSliceWithActiveSequencer(t *testing.T) {
	root := t.TempDir()
	writeLinkedWorktreeSequencer(t, root)
	detail := interruptedServiceRunDetail(t, root)
	preparerCalls := 0
	agentCalls := 0
	runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base")

	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			preparerCalls++
			return root, nil
		},
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return nil
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if err == nil || !strings.Contains(err.Error(), `Git operation "cherry-pick/revert" is active`) || !strings.Contains(err.Error(), "changed_paths=partial.go") {
		t.Fatalf("execute error = %v, want interrupted sequencer refusal", err)
	}
	if preparerCalls != 0 || agentCalls != 0 {
		t.Fatalf("preparer=%d agent=%d, want refusal before both calls", preparerCalls, agentCalls)
	}
}

func TestServiceExecuteResumesInterruptedAutomaticSliceThenRunsNextSlice(t *testing.T) {
	root := t.TempDir()
	initial := interruptedServiceRunDetail(t, root)
	planDir := initial.Dir
	initial.State.Plan.PendingSlices = []string{"001-a", "002-b"}
	initial.Slices.Slices = append(initial.Slices.Slices, plan.Slice{
		ID: "002-b", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"go test ."}},
	})

	afterFirst := interruptedServiceRunDetail(t, root)
	afterFirst.Dir = planDir
	afterFirst.State.Plan.CurrentSlice = nil
	afterFirst.State.Plan.PendingSlices = []string{"002-b"}
	afterFirst.State.Plan.CompletedSlices = []string{"001-a"}
	afterFirst.Slices.Slices[0].Status = plan.StatusCompleted
	afterFirst.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "after-1"}
	afterFirst.Slices.Slices = append(afterFirst.Slices.Slices, plan.Slice{
		ID: "002-b", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"go test ."}},
	})

	completed := interruptedServiceRunDetail(t, root)
	completed.Dir = planDir
	completed.State.Status = plan.StatusCompleted
	completed.State.Plan.CurrentSlice = nil
	completed.State.Plan.PendingSlices = nil
	completed.State.Plan.CompletedSlices = []string{"001-a", "002-b"}
	completed.Slices.Slices[0].Status = plan.StatusCompleted
	completed.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "after-1"}
	completed.Slices.Slices = append(completed.Slices.Slices, plan.Slice{
		ID: "002-b", Status: plan.StatusCompleted, ExecutionRoot: root,
		ExecutionStart: &plan.SliceExecutionStart{Branch: "tao/plan-a", Head: "after-1", CommitPolicy: CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree},
		Completion:     &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "after-2"},
		Verification:   plan.Verification{Commands: []string{"go test ."}},
	})

	var runs []SliceRun
	runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string {
		if len(runs) == 0 {
			return " M partial.go\n"
		}
		return ""
	}, "tao/plan-a", "")
	runner = func(base CommandRunner) CommandRunner {
		return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
			if runGitKey(args) == "rev-parse HEAD" {
				switch len(runs) {
				case 0:
					_, _ = io.WriteString(stdout, "base\n")
				case 1:
					_, _ = io.WriteString(stdout, "after-1\n")
				default:
					_, _ = io.WriteString(stdout, "after-2\n")
				}
				return nil
			}
			return base(ctx, cwd, name, args, stdout, stderr)
		}
	}(runner)

	repo := &memoryRunRepository{details: []*plan.PlanDetail{initial, afterFirst, completed}}
	err := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner:     runner,
		EventAppender:     eventAppenderFunc(func(string, plan.Event) error { return nil }),
		PlanRecordFactory: memoryPlanRecordFactory,
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			runs = append(runs, run)
			return nil
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("agent runs = %d, want resumed slice and next slice", len(runs))
	}
	if runs[0].SliceID != "001-a" || !runs[0].Resuming {
		t.Fatalf("first run = %+v, want resumed 001-a", runs[0])
	}
	if runs[1].SliceID != "002-b" || runs[1].Resuming {
		t.Fatalf("second run = %+v, want normal 002-b", runs[1])
	}
	if afterFirst.Slices.Slices[1].ExecutionStart == nil || afterFirst.Slices.Slices[1].ExecutionStart.Head != "after-1" {
		t.Fatalf("next slice did not start at completed recovery head: %#v", afterFirst.Slices.Slices[1].ExecutionStart)
	}
}

func TestServiceExecuteResumesAgainAfterASecondProviderInterruption(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	var calls []string
	runner := interruptedServiceGitRunner(t, root, &calls, func() string { return " M partial.go\n" }, "tao/plan-a", "base")
	agentCalls := 0
	var events []plan.Event
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner, EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
			events = append(events, event)
			return nil
		}),
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return context.DeadlineExceeded
		}),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			t.Fatal("interrupted resume prepared workspace")
			return "", nil
		},
	}})
	request := Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}
	for attempt := 1; attempt <= 2; attempt++ {
		err := service.Execute(context.Background(), request)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d error = %v, want provider timeout", attempt, err)
		}
		if detail.Slices.Slices[0].ExecutionStart.Head != "base" || detail.Slices.Slices[0].CommitIntent != nil {
			t.Fatalf("attempt %d changed recovery evidence: %#v", attempt, detail.Slices.Slices[0])
		}
	}
	if agentCalls != 2 {
		t.Fatalf("agent calls = %d, want one exact-boundary resume per invocation", agentCalls)
	}
	var attempts, failures []plan.Event
	for _, event := range events {
		switch event.Type {
		case plan.EventTypeSliceResumeAttempted:
			attempts = append(attempts, event)
		case plan.EventTypeSliceResumeFailed:
			failures = append(failures, event)
		}
		if event.Type == plan.EventTypeSliceStarted || event.Type == plan.EventTypeSliceCompleted {
			t.Fatalf("resume duplicated lifecycle transition: %+v", event)
		}
	}
	if len(attempts) != 2 || attempts[0].Attempts != 1 || attempts[1].Attempts != 2 || len(failures) != 2 {
		t.Fatalf("resume evidence attempts=%+v failures=%+v", attempts, failures)
	}
}

func TestInterruptedResumeFailureEventWarningPreservesProviderError(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	providerErr := errors.New("provider connection lost")
	var out bytes.Buffer
	runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base")
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, &out, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
			if event.Type == plan.EventTypeSliceResumeFailed {
				return errors.New("resume journal unavailable")
			}
			return nil
		}),
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error { return providerErr }),
	}})

	err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want original provider error", err)
	}
	if !strings.Contains(out.String(), "Warning: record slice resume failure: resume journal unavailable") {
		t.Fatalf("output missing best-effort event warning: %q", out.String())
	}
}

func TestServiceExecuteRoutesCommitIntentWithoutAgent(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Hash: "intent", Policy: CommitPolicySlice.String()}
	agentCalls := 0
	gitCalls := 0
	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { gitCalls++; return nil },
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error { agentCalls++; return nil }),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if err == nil || !strings.Contains(err.Error(), "interrupted post-intent completion transaction") || !strings.Contains(err.Error(), "rerun tao slice-complete") || !strings.Contains(err.Error(), "do not rerun the implementation agent") {
		t.Fatalf("error = %v, want post-intent slice-complete-only recovery guidance", err)
	}
	if agentCalls != 0 || gitCalls != 0 {
		t.Fatalf("post-intent path called agent=%d git=%d", agentCalls, gitCalls)
	}
}

func TestServiceExecuteRepairsOnlyCleanTornAutomaticStart(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	detail.Slices.Slices[0].ExecutionStart = nil
	started := 0
	agentCalls := 0
	runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "tao/plan-a", "base")
	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner, EventAppender: eventAppenderFunc(func(string, plan.Event) error { return nil }),
		SliceExecutor:     sliceExecutorFunc(func(context.Context, SliceRun) error { agentCalls++; return errors.New("provider stopped") }),
		PlanRecordFactory: callbackPlanRecordFactory(func(*plan.PlanDetail, string, time.Time) error { started++; return nil }, nil),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			t.Fatal("torn start prepared workspace")
			return "", nil
		},
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if err == nil || !strings.Contains(err.Error(), "provider stopped") {
		t.Fatalf("error = %v, want provider error after repair", err)
	}
	if agentCalls != 1 || started != 0 || detail.Slices.Slices[0].ExecutionStart == nil {
		t.Fatalf("agent=%d started=%d boundary=%#v", agentCalls, started, detail.Slices.Slices[0].ExecutionStart)
	}

	dirty := interruptedServiceRunDetail(t, root)
	dirty.Slices.Slices[0].ExecutionStart = nil
	agentCalls = 0
	dirtyRunner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M unattributed.go\n" }, "tao/plan-a", "base")
	err = NewService(&memoryRunRepository{details: []*plan.PlanDetail{dirty}}, io.Discard, Options{RunDependencies: RunDependencies{CommandRunner: dirtyRunner, SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error { agentCalls++; return nil })}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if err == nil || !strings.Contains(err.Error(), "dirty worktree has no immutable execution boundary") || !strings.Contains(err.Error(), "changed_paths=unattributed.go") || !strings.Contains(err.Error(), "leave the automatic slice uncommitted") || agentCalls != 0 {
		t.Fatalf("dirty torn start error=%v agent=%d", err, agentCalls)
	}
}

func TestServiceExecuteRepairsMissingStartEventAfterReload(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 16, 18, 15, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	detail.State.Repo.Branch = "master"
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Path: workspaceRoot,
		Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}
	persistRunArtifacts(t, planDir, detail)

	failedStore := &stateOnlyStartStore{appendEventErr: errors.New("injected start event append failure")}
	failedExecution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}, RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return plan.NewPlanRecordWithStore(failedStore, detail.Dir, detail)
		},
	})
	if err := startSlice(context.Background(), failedExecution, detail, "001-a", startedAt, workspaceRoot); err == nil || !strings.Contains(err.Error(), "injected start event append failure") {
		t.Fatalf("start error = %v, want injected event append failure", err)
	}
	if failedStore.appended != 1 {
		t.Fatalf("failed start appended %d events, want one failed attempt", failedStore.appended)
	}

	repo := plan.NewFileRepository(plansDir)
	reloaded, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	slice := reloaded.Slices.Slices[0]
	if reloaded.State.Plan.CurrentSlice == nil || *reloaded.State.Plan.CurrentSlice != "001-a" || slice.Status != plan.StatusInProgress || slice.ExecutionStart == nil {
		t.Fatalf("reloaded event-gap start = state:%#v slice:%#v", reloaded.State.Plan, slice)
	}
	if len(reloaded.Events) != 0 {
		t.Fatalf("reloaded events = %#v, want missing start event", reloaded.Events)
	}

	providerErr := errors.New("provider stopped after start event repair")
	agentCalls := 0
	service := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			t.Fatal("event-gap recovery prepared workspace")
			return "", nil
		},
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			agentCalls++
			if run.RepoRoot != workspaceRoot || !run.Resuming {
				t.Fatalf("event-gap run = root:%q resuming:%t", run.RepoRoot, run.Resuming)
			}
			return providerErr
		}),
	}})
	request := Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := service.Execute(context.Background(), request); !errors.Is(err, providerErr) {
			t.Fatalf("repair attempt %d error = %v, want provider error", attempt, err)
		}
	}
	if agentCalls != 2 {
		t.Fatalf("agent calls = %d, want two resumed attempts", agentCalls)
	}

	repaired, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	repairedSlice := repaired.Slices.Slices[0]
	if repairedSlice.ExecutionStart == nil || repairedSlice.ExecutionStart.Branch != "tao/plan-a" || repairedSlice.ExecutionStart.Head != "base" || repairedSlice.Timing.StartedAt == nil || !repairedSlice.Timing.StartedAt.Equal(startedAt) {
		t.Fatalf("recovery changed execution boundary: %#v", repairedSlice)
	}
	startedEvents := 0
	for _, event := range repaired.Events {
		if event.Type == plan.EventTypeSliceStarted && event.SliceID == "001-a" {
			startedEvents++
			if !event.Timestamp.Equal(startedAt) {
				t.Fatalf("repaired start timestamp = %s, want %s", event.Timestamp, startedAt)
			}
		}
	}
	if startedEvents != 1 {
		t.Fatalf("slice_started events = %d, want exactly one; events=%#v", startedEvents, repaired.Events)
	}
}

func TestServiceExecuteRepairsStateAdvancedSliceStartAfterReload(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 16, 18, 30, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	detail.State.Repo.Branch = "master"
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Path: workspaceRoot,
		Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
	}
	persistRunArtifacts(t, planDir, detail)

	failedStore := &stateOnlyStartStore{writeSlicesErr: errors.New("injected slices write failure")}
	failedExecution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}, RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return plan.NewPlanRecordWithStore(failedStore, detail.Dir, detail)
		},
	})
	if err := startSlice(context.Background(), failedExecution, detail, "001-a", startedAt, workspaceRoot); err == nil || !strings.Contains(err.Error(), "injected slices write failure") {
		t.Fatalf("start error = %v, want injected slices write failure", err)
	}
	if failedStore.appended != 0 {
		t.Fatalf("failed start appended %d events, want none before slices persistence", failedStore.appended)
	}

	repo := plan.NewFileRepository(plansDir)
	reloaded, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.CurrentSlice == nil || *reloaded.State.Plan.CurrentSlice != "001-a" || reloaded.Slices.Slices[0].Status != plan.StatusPending {
		t.Fatalf("reloaded torn start = state:%#v slice:%#v", reloaded.State.Plan, reloaded.Slices.Slices[0])
	}

	providerErr := errors.New("provider stopped after repaired start")
	prepared := 0
	var gitCalls []string
	service := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, workspaceRoot, &gitCalls, func() string { return "" }, "tao/plan-a", "base"),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return "", errors.New("workspace preparation must not run during torn-start repair")
		},
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			if run.RepoRoot != workspaceRoot || !run.Resuming {
				t.Fatalf("repaired run = root:%q resuming:%t", run.RepoRoot, run.Resuming)
			}
			return providerErr
		}),
	}})
	err = service.Execute(context.Background(), Request{Input: planDir, ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	if !errors.Is(err, providerErr) {
		t.Fatalf("repair run error = %v, want provider error", err)
	}
	if prepared != 0 {
		t.Fatalf("workspace preparer calls = %d, want none", prepared)
	}
	for _, call := range gitCalls {
		if strings.HasPrefix(call, "worktree ") || strings.HasPrefix(call, "rebase ") || strings.HasPrefix(call, "checkout ") {
			t.Fatalf("repair mutated workspace with git %s", call)
		}
	}

	repaired, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	slice := repaired.Slices.Slices[0]
	if slice.Status != plan.StatusInProgress || slice.ExecutionRoot != workspaceRoot || slice.ExecutionStart == nil {
		t.Fatalf("repaired slice = %#v", slice)
	}
	if slice.ExecutionStart.Branch != "tao/plan-a" || slice.ExecutionStart.Head != "base" || slice.Timing.StartedAt == nil || !slice.Timing.StartedAt.Equal(startedAt) {
		t.Fatalf("repaired boundary/timing = start:%#v timing:%#v", slice.ExecutionStart, slice.Timing)
	}
	startedEvents := 0
	for _, event := range repaired.Events {
		if event.Type == plan.EventTypeSliceStarted && event.SliceID == "001-a" {
			startedEvents++
		}
	}
	if startedEvents != 1 {
		t.Fatalf("slice_started events = %d, want exactly one; events=%#v", startedEvents, repaired.Events)
	}
}

func TestServiceExecuteRefusesInterruptedBoundaryDriftAndManualOwnership(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		mode     ExecutionMode
		branch   string
		head     string
		want     string
	}{
		{name: "branch drift", strategy: plan.WorkspaceStrategyWorktree, mode: ExecutionModeIsolated, branch: "other", head: "base", want: "branch differs"},
		{name: "head drift", strategy: plan.WorkspaceStrategyWorktree, mode: ExecutionModeIsolated, branch: "tao/plan-a", head: "advanced", want: "HEAD advanced"},
		{name: "current checkout", strategy: plan.WorkspaceStrategyCurrent, mode: ExecutionModeCurrent, branch: "feature", head: "base", want: "manually owned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			detail.State.Workspace.Strategy = tt.strategy
			detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = tt.strategy
			if tt.strategy == plan.WorkspaceStrategyCurrent {
				detail.Slices.Slices[0].ExecutionStart.Branch = "feature"
			}
			agentCalls := 0
			prepared := 0
			runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M work.go\n" }, tt.branch, tt.head)
			err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner: runner, SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error { agentCalls++; return nil }),
				WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
					prepared++
					return root, nil
				},
			}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: tt.mode, CommitPolicy: CommitPolicySlice}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			for _, want := range []string{"root=", "branch=", "HEAD=", "changed_paths=work.go"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("diagnostic missing %q: %v", want, err)
				}
			}
			if tt.name == "current checkout" && !strings.Contains(err.Error(), "requires manual completion") {
				t.Fatalf("manual completion diagnostic = %v", err)
			}
			if tt.name != "current checkout" && !strings.Contains(err.Error(), "restore the recorded root, branch, and HEAD") {
				t.Fatalf("changed-boundary diagnostic = %v", err)
			}
			if agentCalls != 0 || prepared != 0 {
				t.Fatalf("agent=%d prepared=%d before refusal", agentCalls, prepared)
			}
			if strings.Contains(strings.ToLower(err.Error()), "manual commit") || strings.Contains(err.Error(), "commit the changes") {
				t.Fatalf("unsafe commit guidance: %v", err)
			}
		})
	}
}

func TestServiceExecuteRebasesStaleIsolatedWorktreeBeforeAgent(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	workspacePath := filepath.Join(workspaceRoot, "plan-a")
	if err := os.MkdirAll(workspacePath, 0o750); err != nil {
		t.Fatal(err)
	}
	planDir := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a", "002-b"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.Branch = "feature"
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: workspaceRoot, Path: workspacePath, Branch: "tao/plan-a", BaseBranch: "main", BaseSHA: "base-old"}
	reloaded := runPlanDetail(plan.StatusInProgress, []string{"002-b"}, []string{"001-a"}, "002-b", plan.StatusPending, nil, nil)
	reloaded.Dir = planDir
	reloaded.State.Repo.Root = repoRoot
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, reloaded}}
	var order []string
	rebased := false
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			return nil
		}
		actualCWD := cwd
		gitArgs := args
		if len(args) >= 2 && args[0] == "-C" {
			actualCWD = args[1]
			gitArgs = args[2:]
		}
		key := strings.Join(gitArgs, " ")
		order = append(order, key)
		switch key {
		case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
		case "branch --format=%(refname:short) --list main":
			_, _ = io.WriteString(stdout, "main\n")
		case "rev-parse main":
			_, _ = io.WriteString(stdout, "base-new\n")
		case "branch --show-current":
			if actualCWD != workspacePath {
				t.Fatalf("expected branch check in workspace %q, got %q", workspacePath, actualCWD)
			}
			_, _ = io.WriteString(stdout, "tao/plan-a\n")
		case "rev-parse HEAD":
			if actualCWD != workspacePath {
				t.Fatalf("expected HEAD check in workspace %q, got %q", workspacePath, actualCWD)
			}
			if rebased {
				_, _ = io.WriteString(stdout, "head-new\n")
			} else {
				_, _ = io.WriteString(stdout, "head-old\n")
			}
		case "status --porcelain":
			// The worktree is clean before and after the pre-run rebase.
		case "merge-base --is-ancestor base-new head-old":
			return errors.New("not an ancestor")
		case "rebase main":
			if actualCWD != workspacePath {
				t.Fatalf("expected rebase in workspace %q, got %q", workspacePath, actualCWD)
			}
			rebased = true
		default:
			t.Fatalf("unexpected git command %q in %s", key, actualCWD)
		}
		return nil
	}
	executor := sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
		order = append(order, "agent")
		if !rebased {
			t.Fatal("expected isolated worktree to be rebased before invoking the agent")
		}
		if run.RepoRoot != workspacePath {
			t.Fatalf("expected agent repo root %q, got %q", workspacePath, run.RepoRoot)
		}
		return ctx.Err()
	})

	err := NewService(repo, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runner}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, MaxSlices: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !runCallBefore(order, "rebase main", "agent") {
		t.Fatalf("expected pre-run rebase before agent, got order %#v", order)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.RebaseStatus != "not_needed" || detail.State.Workspace.BaseStatus != "current" {
		t.Fatalf("expected refreshed workspace metadata after rebase, got %#v", detail.State.Workspace)
	}
}

func TestServiceExecuteDoesNotRebaseCurrentMode(t *testing.T) {
	repoRoot := t.TempDir()
	planDir := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a", "002-b"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.Branch = "feature"
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, BaseBranch: "main", BaseSHA: "base-old"}
	reloaded := runPlanDetail(plan.StatusInProgress, []string{"002-b"}, []string{"001-a"}, "002-b", plan.StatusPending, nil, nil)
	reloaded.Dir = planDir
	reloaded.State.Repo.Root = repoRoot
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, reloaded}}
	var calls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			return nil
		}
		key := runGitKey(args)
		calls = append(calls, key)
		if strings.HasPrefix(key, "rebase ") || strings.HasPrefix(key, "merge-base --is-ancestor") {
			t.Fatalf("current-mode run must not prepare an automatic rebase, got git %s", key)
		}
		switch key {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "status --porcelain":
		default:
			t.Fatalf("unexpected git command %q", key)
		}
		return nil
	}
	executorCalls := 0
	executor := sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
		executorCalls++
		want, err := filepath.Abs(repoRoot)
		if err != nil {
			t.Fatal(err)
		}
		if run.RepoRoot != want {
			t.Fatalf("expected current-mode agent repo root %q, got %q", want, run.RepoRoot)
		}
		return ctx.Err()
	})

	err := NewService(repo, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runner}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeCurrent, MaxSlices: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if executorCalls != 1 {
		t.Fatalf("expected current-mode executor once, got %d", executorCalls)
	}
	if runHasGitCallPrefix(calls, "rebase ") || runHasGitCallPrefix(calls, "merge-base --is-ancestor") {
		t.Fatalf("expected no automatic rebase checks in current mode, got calls %#v", calls)
	}
}

func TestServiceExecuteWorkspaceStrategyCurrentOverridesPlanWorktree(t *testing.T) {
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = detail.Dir
	completedDetail.State.Repo.Root = repoRoot
	completedDetail.State.Workspace = detail.State.Workspace
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, completedDetail}}
	executor := &packetCapturingExecutor{}
	var calls []string

	err := NewService(repo, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeCurrent}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if executor.repoRoot != want {
		t.Fatalf("expected current repo root %q, got %q", want, executor.repoRoot)
	}
	if detail.State.Workspace.Strategy != plan.WorkspaceStrategyWorktree {
		t.Fatalf("expected run option not to rewrite plan workspace strategy, got %#v", detail.State.Workspace)
	}
	if runHasGitCallPrefix(calls, "worktree ") {
		t.Fatalf("expected current strategy not to prepare a worktree, got calls %#v", calls)
	}
}

func TestRenderWorkPromptIncludesRunPacket(t *testing.T) {
	prompt, err := renderWorkPrompt(workPromptData{PlanDir: "/plans/plan-a", RunPacket: "# Tao Run Packet\n\n- ID: plan-a", CommitPolicy: CommitPolicySlice.String()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Use this compact packet", "# Tao Run Packet", "Read a full fallback artifact only after naming a concrete reason", "tao slice-complete --plan-dir \"/plans/plan-a\""} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in prompt:\n%s", want, prompt)
		}
	}
}

func TestRenderWorkPromptIncludesInterruptedResumeInstructions(t *testing.T) {
	prompt, err := renderWorkPrompt(workPromptData{PlanDir: "/plans/plan-a", CommitPolicy: CommitPolicySlice.String(), Resuming: true, ResumeAttempt: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"This is resume attempt 3",
		"inspect all staged, unstaged, and untracked work",
		"Continue or correct that work rather than discarding it or restarting",
		"Rerun every verification command declared for the slice",
		"call `tao slice-complete`",
		"Never run `git commit` manually",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("resume prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRenderWorkPromptRejectsRemovedPlanPolicy(t *testing.T) {
	_, err := renderWorkPrompt(workPromptData{PlanDir: "/plans/plan-a", CommitPolicy: CommitPolicyPlan.String()})
	if err == nil || !strings.Contains(err.Error(), "plan was removed; use slice or none") {
		t.Fatalf("expected prompt migration error, got %v", err)
	}
}

func TestRunWorkPromptInstructsSliceComplete(t *testing.T) {
	prompt, err := renderWorkPrompt(workPromptData{PlanDir: "/plans/plan-a", CommitPolicy: CommitPolicyNone.String()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"write local files", "verification results JSON file", "Tao updates `state.json`, `slices.json`, duration", "tao slice-complete --plan-dir \"/plans/plan-a\""} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in prompt:\n%s", want, prompt)
		}
	}
}

func TestRunContinueRestartsBlockedPlanBeforeCapabilityGate(t *testing.T) {
	detail := runPlanDetail(plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	executor := &countingSliceExecutor{}
	continued := false
	fixedNow := time.Date(2026, 5, 24, 21, 0, 0, 0, time.FixedZone("test", -5*60*60))

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Continue: true, CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: callbackPlanRecordFactory(nil, func(detail *plan.PlanDetail, now time.Time) error {
		continued = true
		if !now.Equal(fixedNow.UTC()) || now.Location() != time.UTC {
			t.Fatalf("expected deterministic UTC continue timestamp, got %s (%s)", now, now.Location())
		}
		return plan.MarkBlockedContinued(detail, now)
	}), Now: func() time.Time { return fixedNow }, CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if !continued || executor.calls != 1 {
		t.Fatalf("expected continue and executor once, continued=%v calls=%d", continued, executor.calls)
	}
}

func TestRunContinueDoesNotSkipVerificationPreflight(t *testing.T) {
	repo := t.TempDir()
	detail := &plan.PlanDetail{
		Dir:   "/plans/plan-a",
		State: plan.State{Status: plan.StatusBlocked, Repo: plan.Repo{Root: repo}, Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID:             "001-a",
			Status:         plan.StatusPending,
			RequiredInputs: []plan.RequiredInput{{Path: "missing.test.ts", Kind: plan.RequiredInputFile, Reason: "test fixture"}},
			Verification:   plan.Verification{Commands: []string{"go test ."}},
		}}},
	}
	var out bytes.Buffer
	executor := &countingSliceExecutor{}

	err := executeDetail(context.Background(), detail, nil, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Continue: true, CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{
		SliceExecutor: executor,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			return repo, nil
		},
		PlanRecordFactory: memoryPlanRecordFactory,
	}})
	if err == nil || !strings.Contains(err.Error(), "slice 001-a failed verification preflight") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("expected executor not to run, got %d calls", executor.calls)
	}
}

func TestRunResumeChecksRequiredInputsBeforeRepairOrHandoff(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	detail.Events = nil
	detail.Slices.Slices[0].RequiredInputs = []plan.RequiredInput{{Path: "missing.txt", Kind: plan.RequiredInputFile, Reason: "resume contract"}}
	prepared := 0
	events := 0
	executor := &countingSliceExecutor{}

	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return root, nil
		},
		EventAppender: eventAppenderFunc(func(string, plan.Event) error {
			events++
			return nil
		}),
		SliceExecutor: executor,
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
		ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
	}})
	if err == nil || !strings.Contains(err.Error(), "failed verification preflight") {
		t.Fatalf("execute error = %v, want resume required-input refusal", err)
	}
	if prepared != 0 {
		t.Fatalf("resume prepared %d workspaces, want immutable root reuse", prepared)
	}
	if events != 0 || executor.calls != 0 {
		t.Fatalf("resume effects: events=%d executor=%d, want no start-event repair or handoff", events, executor.calls)
	}
}

func TestRunStartsSliceBeforeExecutor(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	executor := &packetCapturingExecutor{}
	started := false
	fixedNow := time.Date(2026, 5, 24, 22, 0, 0, 0, time.FixedZone("test", -5*60*60))

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor, PlanRecordFactory: callbackPlanRecordFactory(func(detail *plan.PlanDetail, sliceID string, now time.Time) error {
		started = true
		if !now.Equal(fixedNow.UTC()) || now.Location() != time.UTC {
			t.Fatalf("expected deterministic UTC slice start timestamp, got %s (%s)", now, now.Location())
		}
		_, _, err := plan.MarkSliceStarted(detail, sliceID, now)
		return err
	}, nil), Now: func() time.Time { return fixedNow }, CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("expected slice starter to run")
	}
	if !strings.Contains(executor.packet, "- Status: in_progress") || !strings.Contains(executor.packet, "- Current Slice: 001-a") {
		t.Fatalf("expected packet rendered after start bookkeeping, got:\n%s", executor.packet)
	}
}

func TestRunPersistsExecutionRootWhenStartingSlice(t *testing.T) {
	planDir := t.TempDir()
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	persistRunArtifacts(t, planDir, detail)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = planDir
	completedDetail.State.Repo.Root = detail.State.Repo.Root
	var out bytes.Buffer

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, ctx.Err()
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: fakeSliceExecutor{}, RootResolver: ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) {
		return workspaceRoot, nil
	}), CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(planDir, "slices.json")) //nolint:gosec // test reads a path from a t.TempDir-derived location
	if err != nil {
		t.Fatal(err)
	}
	var slicesFile plan.SlicesFile
	if err := json.Unmarshal(data, &slicesFile); err != nil {
		t.Fatal(err)
	}
	if got := slicesFile.Slices[0].ExecutionRoot; got != workspaceRoot {
		t.Fatalf("expected execution root %q persisted at slice start, got %q", workspaceRoot, got)
	}
}

func TestSelectedSliceRunnerPersistsLastRunCommitPolicyAtStart(t *testing.T) {
	planDir := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	detail.State.Plan.LastRunCommitPolicy = CommitPolicySlice.String()
	persistRunArtifacts(t, planDir, detail)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = planDir
	completedDetail.State.Repo.Root = detail.State.Repo.Root
	runner := SelectedSliceRunner{
		reload: func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
			return completedDetail, ctx.Err()
		},
		out:           io.Discard,
		execution:     testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyPlan}}, RunDependencies{}),
		sliceExecutor: fakeSliceExecutor{},
		rootResolver: ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) {
			return detail.State.Repo.Root, nil
		}),
	}

	if _, err := runner.Run(context.Background(), detail, plan.Derive(detail, time.Time{})); err != nil {
		t.Fatal(err)
	}
	state, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan.LastRunCommitPolicy != CommitPolicyPlan.String() {
		t.Fatalf("LastRunCommitPolicy = %q, want %q", state.Plan.LastRunCommitPolicy, CommitPolicyPlan)
	}
}

func TestStartSliceRecordsAutomaticRunBoundary(t *testing.T) {
	planDir := t.TempDir()
	executionRoot := t.TempDir()
	startedAt := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	persistRunArtifacts(t, planDir, detail)

	var gitCalls []string
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
		CommitPolicy:  CommitPolicySlice,
		ExecutionMode: ExecutionModeIsolated,
	}}, RunDependencies{CommandRunner: runGitFake(&gitCalls, nil)})
	execution.StartingDirtyPaths = []string{"README.md", "internal/run/run.go"}
	if err := startSlice(context.Background(), execution, detail, "001-a", startedAt, executionRoot); err != nil {
		t.Fatal(err)
	}

	reloaded, err := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.LastRunCommitPolicy != CommitPolicySlice.String() || !slices.Equal(reloaded.State.Plan.LastRunStartingDirty, execution.StartingDirtyPaths) {
		t.Fatalf("run metadata = policy:%q dirty:%#v", reloaded.State.Plan.LastRunCommitPolicy, reloaded.State.Plan.LastRunStartingDirty)
	}
	startedSlice := reloaded.Slices.Slices[0]
	if startedSlice.Status != plan.StatusInProgress || startedSlice.ExecutionRoot != executionRoot {
		t.Fatalf("started slice = %#v", startedSlice)
	}
	wantBoundary := plan.SliceExecutionStart{
		Branch: "feature", Head: "head123", CommitPolicy: CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree,
	}
	if startedSlice.ExecutionStart == nil || *startedSlice.ExecutionStart != wantBoundary {
		t.Fatalf("execution boundary = %#v, want %#v", startedSlice.ExecutionStart, wantBoundary)
	}
	var startedEvent *plan.Event
	for i := range reloaded.Events {
		if reloaded.Events[i].Type == plan.EventTypeSliceStarted && reloaded.Events[i].SliceID == "001-a" {
			startedEvent = &reloaded.Events[i]
			break
		}
	}
	if startedEvent == nil || !startedEvent.Timestamp.Equal(startedAt) {
		t.Fatalf("slice_started event = %#v, want timestamp %s", startedEvent, startedAt)
	}

	changedBoundaryExecution := execution
	changedBoundaryExecution.Dependencies.CommandRunner = interruptedServiceGitRunner(t, executionRoot, &[]string{}, func() string { return "" }, "feature", "changed-head")
	if err := startSlice(context.Background(), changedBoundaryExecution, detail, "001-a", startedAt.Add(time.Minute), executionRoot); err == nil || !strings.Contains(err.Error(), "refusing to overwrite branch or head") {
		t.Fatalf("changed boundary error = %v", err)
	}
	reloaded, err = plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Slices.Slices[0].ExecutionStart; got == nil || *got != wantBoundary {
		t.Fatalf("failed retry changed immutable boundary: %#v", got)
	}
}

func TestStartSlicePersistsLastRunStartingDirtyAndClearsItOnCleanStart(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a", "002-b"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Plan.LastRunStartingDirty = []string{"stale.txt"}
	detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: "002-b", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"go test ."}}})
	persistRunArtifacts(t, planDir, detail)

	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyPlan}}, RunDependencies{})
	execution.StartingDirtyPaths = []string{"README.md", "internal/run/run.go"}
	if err := startSlice(context.Background(), execution, detail, "001-a", time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC), repoRoot); err != nil {
		t.Fatal(err)
	}
	state, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(state.Plan.LastRunStartingDirty, []string{"README.md", "internal/run/run.go"}) {
		t.Fatalf("LastRunStartingDirty = %#v, want captured dirty paths", state.Plan.LastRunStartingDirty)
	}

	execution.StartingDirtyPaths = []string{}
	if err := startSlice(context.Background(), execution, detail, "002-b", time.Date(2026, 7, 10, 1, 1, 0, 0, time.UTC), repoRoot); err != nil {
		t.Fatal(err)
	}
	state, err = plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan.LastRunStartingDirty == nil || len(state.Plan.LastRunStartingDirty) != 0 {
		t.Fatalf("LastRunStartingDirty = %#v, want persisted empty slice after clean start", state.Plan.LastRunStartingDirty)
	}
}

func TestRunRecordsRunContextTelemetry(t *testing.T) {
	planDir := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	detail.Slices.Slices[0].ExpectedFiles = []string{"internal/..."}
	detail.Slices.Slices[0].Verification.Commands = []string{"go test"}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = planDir
	fixedNow := time.Date(2026, 5, 26, 16, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	repo := &memoryRunRepository{}

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: &countingSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, EventAppender: repo, Now: func() time.Time { return fixedNow }, CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(planDir, "events.jsonl")) //nolint:gosec // test reads a path from a t.TempDir-derived location
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("events = %d, want run context and final verification", len(lines))
	}
	var event plan.Event
	if err := json.Unmarshal(lines[0], &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != plan.EventTypeRunContext || event.SliceID != "001-a" || event.Agent != AgentPi.String() || event.CommitPolicy != CommitPolicyNone.String() || !event.RunPacketProvided || event.GuardrailWarnings != 1 {
		t.Fatalf("unexpected run context event: %+v", event)
	}
	if err := json.Unmarshal(lines[1], &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != plan.EventTypeFinalVerification || event.Result != finalVerificationSkipped {
		t.Fatalf("unexpected final verification event: %+v", event)
	}
}

func TestPrepareRunExecutionDefaultsRunDependencies(t *testing.T) {
	planDir := t.TempDir()
	repo := &memoryRunRepository{}
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	var out bytes.Buffer
	workspaceRoot := t.TempDir()

	execution, err := NewService(repo, &out, Options{RunDependencies: RunDependencies{CommandRunner: runGitFake(&[]string{}, nil), WorkspacePreparer: func(ctx context.Context, detail *plan.PlanDetail, input WorkspaceResolverInput) (string, error) {
		return workspaceRoot, nil
	}}}).prepareRunExecution(context.Background(), detail, ExecutionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	dependencies := execution.Dependencies
	if dependencies.CommandRunner == nil || dependencies.ProcessStarter == nil || dependencies.SliceExecutor == nil || dependencies.PullRequestCreator == nil || dependencies.ReviewCreator == nil || dependencies.RootResolver == nil {
		t.Fatalf("expected command, agent, pull request, review, and workspace dependencies to be defaulted: %+v", dependencies)
	}
	if dependencies.EventAppender != repo || dependencies.LogAppender != repo {
		t.Fatal("expected repository-backed event and log dependencies to default from service repo")
	}
	if dependencies.PlanRecordFactory == nil {
		t.Fatal("expected plan record factory to default from service repo")
	}
	record, err := dependencies.PlanRecordFactory(detail)
	if err != nil {
		t.Fatalf("default plan record factory returned error: %v", err)
	}
	if record == nil {
		t.Fatal("default plan record factory returned nil record")
	}
	if dependencies.WorkspacePreparer == nil || dependencies.OutputWriter != &out || execution.ExecutionRoot != workspaceRoot {
		t.Fatalf("expected workspace, output, and root defaults, got %+v", dependencies)
	}
}

func TestExecutionRootResolverUsesCachedRoot(t *testing.T) {
	cachedRoot := filepath.Join(t.TempDir(), "cached")
	resolver := executionRootResolver(runExecution{
		ExecutionRoot: cachedRoot,
		Dependencies: RunDependencies{
			RootResolver: ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) {
				t.Fatal("expected cached execution root to bypass injected root resolver")
				return "", nil
			}),
			WorkspacePreparer: func(ctx context.Context, detail *plan.PlanDetail, input WorkspaceResolverInput) (string, error) {
				t.Fatal("expected cached execution root to bypass workspace preparation")
				return "", nil
			},
		},
	})

	root, err := resolver.ResolveExecutionRoot(context.Background(), runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if root != cachedRoot {
		t.Fatalf("expected cached root %q, got %q", cachedRoot, root)
	}
}

func TestExecutionRootResolverUsesCurrentWorkspaceRoot(t *testing.T) {
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	resolver := executionRootResolver(runExecution{Dependencies: RunDependencies{
		CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
			t.Fatalf("current workspace strategy should not run %s %#v", name, args)
			return nil
		},
		PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) {
			t.Fatal("current workspace strategy should not write workspace metadata")
			return nil, nil
		},
	}})

	root, err := resolver.ResolveExecutionRoot(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if root != want {
		t.Fatalf("expected current workspace root %q, got %q", want, root)
	}
}

func TestExecutionRootResolverPreparesWorktreeStrategyMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	workspacePath := filepath.Join(workspaceRoot, "plan-a")
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.Branch = "feature"
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: workspaceRoot}
	createdAt := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	readyAt := createdAt.Add(time.Minute)
	var calls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %q", name)
		}
		actualCWD := cwd
		gitArgs := args
		if len(args) >= 2 && args[0] == "-C" {
			actualCWD = args[1]
			gitArgs = args[2:]
		}
		key := strings.Join(gitArgs, " ")
		calls = append(calls, key)
		switch key {
		case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
		case "branch --format=%(refname:short) --list main":
			_, _ = io.WriteString(stdout, "main\n")
		case "rev-parse main", "rev-parse feature":
			_, _ = io.WriteString(stdout, "base123\n")
		case "worktree list --porcelain":
			_, _ = io.WriteString(stdout, "worktree "+repoRoot+"\nHEAD base123\nbranch refs/heads/feature\n\n")
		case "rev-parse --verify tao/plan-a":
			return errors.New("missing branch")
		case "worktree add -b tao/plan-a " + workspacePath + " main":
		case "branch --show-current":
			if actualCWD != workspacePath {
				t.Fatalf("expected status cwd %q, got %q", workspacePath, actualCWD)
			}
			_, _ = io.WriteString(stdout, "tao/plan-a\n")
		case "rev-parse HEAD":
			if actualCWD != workspacePath {
				t.Fatalf("expected status cwd %q, got %q", workspacePath, actualCWD)
			}
			_, _ = io.WriteString(stdout, "head123\n")
		case "status --porcelain":
			if actualCWD != workspacePath {
				t.Fatalf("expected status cwd %q, got %q", workspacePath, actualCWD)
			}
		case "merge-base --is-ancestor base123 head123":
		default:
			t.Fatalf("unexpected git command %q in %s", key, actualCWD)
		}
		return ctx.Err()
	}
	var statuses []string
	recordFactory := func(recordDetail *plan.PlanDetail) (PlanMutationRecord, error) {
		if recordDetail.Dir != detail.Dir {
			t.Fatalf("expected plan dir %q, got %q", detail.Dir, recordDetail.Dir)
		}
		return persistOnlyRecord{persist: func() error {
			statuses = append(statuses, recordDetail.State.Workspace.LifecycleStatus)
			return nil
		}}, nil
	}
	resolver := executionRootResolver(runExecution{Dependencies: RunDependencies{CommandRunner: runner, PlanRecordFactory: recordFactory, Now: runClock(createdAt, readyAt)}})

	root, err := resolver.ResolveExecutionRoot(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	if root != workspacePath {
		t.Fatalf("expected worktree path %q, got %q", workspacePath, root)
	}
	if strings.Join(statuses, ",") != strings.Join([]string{plan.WorkspaceStatusPreparing, plan.WorkspaceStatusReady}, ",") {
		t.Fatalf("expected preparing and ready metadata writes, got %#v", statuses)
	}
	if detail.State.Workspace.Path != workspacePath || detail.State.Workspace.Branch != "tao/plan-a" || detail.State.Workspace.BaseSHA != "base123" || detail.State.Workspace.DependencyPreparation != "skipped" {
		t.Fatalf("unexpected workspace metadata: %#v", detail.State.Workspace)
	}
	if detail.State.Workspace.Timing.CreatedAt == nil || !detail.State.Workspace.Timing.CreatedAt.Equal(createdAt) {
		t.Fatalf("expected created timestamp %s, got %#v", createdAt, detail.State.Workspace.Timing.CreatedAt)
	}
	if detail.State.Workspace.Timing.PreparedAt == nil || !detail.State.Workspace.Timing.PreparedAt.Equal(readyAt) {
		t.Fatalf("expected ready timestamp %s, got %#v", readyAt, detail.State.Workspace.Timing.PreparedAt)
	}
	if !runCallBefore(calls, "worktree add -b tao/plan-a "+workspacePath+" main", "branch --show-current") {
		t.Fatalf("expected worktree add before status calls, got %#v", calls)
	}
}

func TestRunDoesNotStartSliceBeforePreflightPasses(t *testing.T) {
	repo := t.TempDir()
	detail := &plan.PlanDetail{
		Dir:   "/plans/plan-a",
		State: plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Root: repo}, Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID:             "001-a",
			Status:         plan.StatusPending,
			RequiredInputs: []plan.RequiredInput{{Path: "missing.test.ts", Kind: plan.RequiredInputFile, Reason: "test fixture"}},
			Verification:   plan.Verification{Commands: []string{"go test ."}},
		}}},
	}
	var out bytes.Buffer
	started := false
	executor := &countingSliceExecutor{}

	err := executeDetail(context.Background(), detail, nil, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{
		SliceExecutor: executor,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			return repo, nil
		},
		PlanRecordFactory: callbackPlanRecordFactory(func(detail *plan.PlanDetail, sliceID string, now time.Time) error {
			started = true
			return nil
		}, nil),
	}})
	if err == nil || !strings.Contains(err.Error(), "slice 001-a failed verification preflight") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	if started {
		t.Fatal("expected slice starter not to run before preflight passes")
	}
	if executor.calls != 0 {
		t.Fatalf("expected executor not to run, got %d calls", executor.calls)
	}
}

func (r *memoryRunRepository) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.calls >= len(r.details) {
		return r.details[len(r.details)-1], nil
	}
	detail := r.details[r.calls]
	r.calls++
	return detail, nil
}

func (r *memoryRunRepository) PlanRecord(detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	return plan.NewPlanRecord("", detail)
}

func (r *memoryRunRepository) OpenLogAppend(planDir string) (*os.File, error) { return nil, nil }

func (r *memoryRunRepository) AppendEvent(planDir string, event plan.Event) error {
	return plan.NewFileRepository("").AppendEvent(planDir, event)
}

func persistRunArtifacts(t *testing.T, planDir string, detail *plan.PlanDetail) {
	t.Helper()
	record, err := plan.NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
}

type sliceExecutorFunc func(ctx context.Context, run SliceRun) error

type stateOnlyStartStore struct {
	writeSlicesErr error
	appendEventErr error
	appended       int
}

func (s *stateOnlyStartStore) WriteState(planDir string, state plan.State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(planDir, "state.json"), data, 0o600)
}

func (s *stateOnlyStartStore) WriteSlices(planDir string, slices plan.SlicesFile) error {
	if s.writeSlicesErr != nil {
		return s.writeSlicesErr
	}
	data, err := json.MarshalIndent(slices, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(planDir, "slices.json"), data, 0o600)
}

func (s *stateOnlyStartStore) AppendEvent(string, plan.Event) error {
	s.appended++
	return s.appendEventErr
}

func (f sliceExecutorFunc) RunSlice(ctx context.Context, run SliceRun) error {
	return f(ctx, run)
}

type persistOnlyRecord struct {
	PlanMutationRecord
	persist func() error
}

func (r persistOnlyRecord) PersistState() error {
	return r.persist()
}

type startCallbackRecord struct {
	PlanMutationRecord
	onStart func(sliceID string, now time.Time) error
}

func (r startCallbackRecord) StartSliceWithRunCommitPolicy(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, now time.Time) error {
	if r.onStart != nil {
		return r.onStart(sliceID, now)
	}
	return r.PlanMutationRecord.StartSliceWithRunCommitPolicy(sliceID, executionRoot, commitPolicy, startingDirtyPaths, now)
}

func (r startCallbackRecord) StartSliceWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary plan.SliceExecutionStart, now time.Time) error {
	if r.onStart != nil {
		return r.onStart(sliceID, now)
	}
	return r.PlanMutationRecord.StartSliceWithRunBoundary(sliceID, executionRoot, commitPolicy, startingDirtyPaths, boundary, now)
}

type memoryPlanMutationRecord struct {
	detail     *plan.PlanDetail
	onStart    func(*plan.PlanDetail, string, time.Time) error
	onContinue func(*plan.PlanDetail, time.Time) error
}

func memoryPlanRecordFactory(detail *plan.PlanDetail) (PlanMutationRecord, error) {
	return memoryPlanMutationRecord{detail: detail}, nil
}

func callbackPlanRecordFactory(onStart func(*plan.PlanDetail, string, time.Time) error, onContinue func(*plan.PlanDetail, time.Time) error) PlanRecordFactory {
	return func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
		return memoryPlanMutationRecord{detail: detail, onStart: onStart, onContinue: onContinue}, nil
	}
}

func (r memoryPlanMutationRecord) StartSlice(sliceID string, now time.Time) error {
	if r.onStart != nil {
		return r.onStart(r.detail, sliceID, now)
	}
	_, _, err := plan.MarkSliceStarted(r.detail, sliceID, now)
	return err
}

func (r memoryPlanMutationRecord) StartSliceWithExecutionRoot(sliceID string, executionRoot string, now time.Time) error {
	if err := r.StartSlice(sliceID, now); err != nil {
		return err
	}
	return r.recordExecutionRoot(sliceID, executionRoot)
}

func (r memoryPlanMutationRecord) StartSliceWithRunCommitPolicy(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, now time.Time) error {
	if err := plan.MarkRunStartMetadata(r.detail, commitPolicy, startingDirtyPaths); err != nil {
		return err
	}
	return r.StartSliceWithExecutionRoot(sliceID, executionRoot, now)
}

func (r memoryPlanMutationRecord) StartSliceWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary plan.SliceExecutionStart, now time.Time) error {
	if err := plan.MarkRunStartMetadata(r.detail, commitPolicy, startingDirtyPaths); err != nil {
		return err
	}
	if err := plan.MarkSliceExecutionStart(r.detail, sliceID, boundary); err != nil {
		return err
	}
	return r.StartSliceWithExecutionRoot(sliceID, executionRoot, now)
}

func (r memoryPlanMutationRecord) RepairSliceStartWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary plan.SliceExecutionStart, startedAt time.Time) error {
	if err := plan.MarkRunStartMetadata(r.detail, commitPolicy, startingDirtyPaths); err != nil {
		return err
	}
	if err := plan.MarkSliceExecutionStart(r.detail, sliceID, boundary); err != nil {
		return err
	}
	if _, _, err := plan.MarkSliceStarted(r.detail, sliceID, startedAt); err != nil {
		return err
	}
	return r.recordExecutionRoot(sliceID, executionRoot)
}

func (r memoryPlanMutationRecord) BlockSliceForBudget(sliceID string, reason string, now time.Time) error {
	_, _, err := plan.MarkSliceBudgetBlocked(r.detail, sliceID, reason, now)
	return err
}

func (r memoryPlanMutationRecord) RepairMissingSliceStartedEvent(sliceID string, startedAt time.Time) error {
	for _, event := range r.detail.Events {
		if event.Type == plan.EventTypeSliceStarted && event.SliceID == sliceID {
			return nil
		}
	}
	r.detail.Events = append(r.detail.Events, plan.Event{
		Type: plan.EventTypeSliceStarted, Timestamp: startedAt.UTC(), PlanID: r.detail.State.Plan.ID,
		SliceID: sliceID, Message: "Work started on slice",
	})
	return nil
}

func (r memoryPlanMutationRecord) recordExecutionRoot(sliceID string, executionRoot string) error {
	for i := range r.detail.Slices.Slices {
		if r.detail.Slices.Slices[i].ID == sliceID {
			r.detail.Slices.Slices[i].ExecutionRoot = executionRoot
			return nil
		}
	}
	return nil
}

func (r memoryPlanMutationRecord) ContinueBlocked(now time.Time) error {
	if r.onContinue != nil {
		return r.onContinue(r.detail, now)
	}
	return plan.MarkBlockedContinued(r.detail, now)
}

func (r memoryPlanMutationRecord) PersistState() error { return nil }

func (r memoryPlanMutationRecord) RecordFinalVerification(verification plan.FinalVerification) error {
	return plan.MarkFinalVerification(r.detail, verification)
}

func (r memoryPlanMutationRecord) RecordStartingBranch(branch string) error {
	r.detail.State.Repo.Branch = branch
	return nil
}

func (r memoryPlanMutationRecord) RecordPullRequest(pr plan.PullRequest, branch, headSHA string) error {
	pr.Branch = branch
	pr.HeadSHA = headSHA
	r.detail.State.Plan.PullRequest = &pr
	if r.detail.State.Workspace == nil {
		r.detail.State.Workspace = &plan.Workspace{}
	}
	r.detail.State.Workspace.Branch = branch
	r.detail.State.Workspace.HeadSHA = headSHA
	r.detail.State.Workspace.PushedSHA = headSHA
	r.detail.State.UpdatedAt = pr.CreatedAt
	r.detail.State.Plan.Timing.LastActivityAt = &pr.CreatedAt
	return nil
}

func (r memoryPlanMutationRecord) RecordReviewError(review plan.PlanReview, _ string) error {
	reviewedAt := review.ReviewedAt
	r.detail.State.Plan.Review = &review
	r.detail.State.UpdatedAt = reviewedAt
	r.detail.State.Plan.Timing.LastActivityAt = &reviewedAt
	return nil
}

func (r memoryPlanMutationRecord) RecordReviewCompleted(review plan.PlanReview, agent string) error {
	return r.RecordReviewError(review, agent)
}

func testRunExecution(config ExecutionConfig, dependencies RunDependencies) runExecution {
	return newRunExecution(config, dependencies)
}

func testRunExecutionWithOptions(options Options) runExecution {
	return runExecutionFromOptions(options)
}

func settleRunTestSlice(detail *plan.PlanDetail) {
	const sliceID = "001-a"
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == sliceID {
			detail.Slices.Slices[i].ExecutionStart = &plan.SliceExecutionStart{Branch: "feature", Head: "head123"}
			detail.Slices.Slices[i].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionNoChanges, CommitSHA: "head123"}
			return
		}
	}
}

func runPlanDetail(status string, pending []string, completed []string, sliceID string, sliceStatus string, startedAt *time.Time, completedAt *time.Time) *plan.PlanDetail {
	return &plan.PlanDetail{
		Dir: "/plans/plan-a",
		State: plan.State{
			Status:    status,
			Repo:      plan.Repo{Root: ".", Branch: "feature"},
			Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent},
			Plan:      plan.PlanState{ID: "plan-a", Title: "Plan A", CompletedSlices: completed, PendingSlices: pending, Timing: plan.PlanTiming{StartedAt: startedAt, CompletedAt: completedAt}},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: sliceID, Status: sliceStatus, Verification: plan.Verification{Commands: []string{"go test ."}}}}},
	}
}

func runGitFake(calls *[]string, failures map[string]error) CommandRunner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			return nil
		}
		key := runGitKey(args)
		*calls = append(*calls, key)
		if err := failures[key]; err != nil {
			_, _ = io.WriteString(stderr, "checkout failed")
			return err
		}
		switch key {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head123\n")
		case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
		}
		return nil
	}
}

func runWorkspaceGitFake(calls *[]string) CommandRunner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			return nil
		}
		key := runGitKey(args)
		*calls = append(*calls, key)
		switch {
		case key == "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
		case key == "branch --format=%(refname:short) --list main":
			_, _ = io.WriteString(stdout, "main\n")
		case key == "rev-parse main", key == "rev-parse feature":
			_, _ = io.WriteString(stdout, "base123\n")
		case key == "worktree list --porcelain":
		case strings.HasPrefix(key, "worktree add "):
		case key == "branch --show-current":
			_, _ = io.WriteString(stdout, "tao/plan-a\n")
		case key == "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head123\n")
		case key == "status --porcelain":
		}
		return nil
	}
}

func interruptedServiceRunDetail(t *testing.T, root string) *plan.PlanDetail {
	t.Helper()
	current := "001-a"
	started := time.Date(2026, 7, 16, 17, 0, 0, 0, time.UTC)
	return &plan.PlanDetail{
		Dir: t.TempDir(),
		State: plan.State{
			Status: plan.StatusInProgress,
			Repo:   plan.Repo{Root: t.TempDir(), Branch: "master"},
			Workspace: &plan.Workspace{
				Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(root), Path: root,
				Branch: "tao/plan-a", HeadSHA: "base", LifecycleStatus: plan.WorkspaceStatusReady,
			},
			Plan: plan.PlanState{
				ID: "plan-a", CurrentSlice: &current, PendingSlices: []string{current},
				LastRunCommitPolicy: CommitPolicySlice.String(), LastRunStartingDirty: []string{},
				Timing: plan.PlanTiming{StartedAt: &started},
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID: current, Status: plan.StatusInProgress, ExecutionRoot: root,
			ExecutionStart: &plan.SliceExecutionStart{Branch: "tao/plan-a", Head: "base", CommitPolicy: CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree},
			Timing:         plan.SliceTiming{StartedAt: &started}, Verification: plan.Verification{Commands: []string{"go test ."}},
		}}},
		Events: []plan.Event{{Type: plan.EventTypeSliceStarted, Timestamp: started, PlanID: "plan-a", SliceID: current, Message: "Work started on slice"}},
	}
}

func writeLinkedWorktreeSequencer(t *testing.T, root string) {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), "repo.git", "worktrees", "plan-a")
	if err := os.MkdirAll(filepath.Join(gitDir, "sequencer"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func interruptedServiceGitRunner(t *testing.T, root string, calls *[]string, status func() string, branch string, head string) CommandRunner {
	t.Helper()
	commonDir := filepath.Join(t.TempDir(), "repo.git")
	gitDir := filepath.Join(commonDir, "worktrees", "plan-a")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			t.Fatalf("unexpected dependency command %s", name)
		}
		actualCWD := cwd
		if len(args) >= 2 && args[0] == "-C" {
			actualCWD = args[1]
		}
		key := runGitKey(args)
		*calls = append(*calls, key)
		switch key {
		case "rev-parse --show-toplevel":
			_, _ = io.WriteString(stdout, actualCWD+"\n")
		case "rev-parse --git-common-dir":
			_, _ = io.WriteString(stdout, commonDir+"\n")
		case "rev-parse --git-dir":
			if actualCWD != root {
				t.Fatalf("git-dir cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, gitDir+"\n")
		case "branch --show-current":
			if actualCWD != root {
				t.Fatalf("branch cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, branch+"\n")
		case "rev-parse HEAD":
			if actualCWD != root {
				t.Fatalf("HEAD cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, head+"\n")
		case "status --porcelain":
			if actualCWD != root {
				t.Fatalf("status cwd = %q, want immutable root %q", actualCWD, root)
			}
			_, _ = io.WriteString(stdout, status())
		case "ls-files --stage -z", "ls-files --others --exclude-standard -z":
		default:
			t.Fatalf("unexpected git command %q", key)
		}
		return nil
	}
}

func runClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(times) {
			return times[len(times)-1]
		}
		now := times[index]
		index++
		return now
	}
}

func runGitKey(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	return strings.Join(args, " ")
}

func runHasGitCall(calls []string, want string) bool {
	return slices.Contains(calls, want)
}

func runHasGitCallPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func runCallBefore(calls []string, before string, after string) bool {
	beforeIndex := -1
	afterIndex := -1
	for i, call := range calls {
		if call == before {
			beforeIndex = i
		}
		if call == after {
			afterIndex = i
		}
	}
	return beforeIndex >= 0 && afterIndex >= 0 && beforeIndex < afterIndex
}
