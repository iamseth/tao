package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestAppendVerificationRepairRefusesNonCodeRecoveryBeforeGit(t *testing.T) {
	tests := []struct {
		name string
		kind plan.FinalVerificationFailureKind
	}{
		{name: "tool missing", kind: plan.FinalVerificationFailureKindToolMissing},
		{name: "timeout", kind: plan.FinalVerificationFailureKindTimeout},
		{name: "cancelled", kind: plan.FinalVerificationFailureKindCancelled},
		{name: "invalid command", kind: plan.FinalVerificationFailureKindInvalidCommand},
		{name: "legacy unclassified"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: test.kind, Fingerprint: "failure"}
			gitCalled := false
			execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated}}, RunDependencies{
				CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
					gitCalled = true
					return nil
				},
			})
			execution.ExecutionRoot = t.TempDir()

			err := appendVerificationRepair(context.Background(), detail, execution)
			if err == nil || !strings.Contains(err.Error(), "does not authorize code repair") {
				t.Fatalf("append error = %v", err)
			}
			if gitCalled {
				t.Fatal("git inspected before recovery decision refused repair")
			}
		})
	}
}

func TestAppendVerificationRepairRefusesExactHeadDriftBeforeMutation(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "failure"}
	factoryCalled := false
	runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
		if command != "git" {
			return nil
		}
		switch runGitKey(args) {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "advanced-head\n")
		}
		return nil
	}
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated}}, RunDependencies{
		CommandRunner: runner,
		PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) {
			factoryCalled = true
			return nil, nil
		},
	})
	execution.ExecutionRoot = t.TempDir()

	err := appendVerificationRepair(context.Background(), detail, execution)
	if err == nil || !strings.Contains(err.Error(), "worktree boundary is stale") {
		t.Fatalf("head drift error = %v", err)
	}
	if factoryCalled || len(detail.State.Plan.PendingSlices) != 0 {
		t.Fatalf("mutation began after drift: factory=%t pending=%v", factoryCalled, detail.State.Plan.PendingSlices)
	}
}

func TestAppendedVerificationRepairResumesAsOrdinaryRun(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "failure"}
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	request := plan.VerificationRepairRequest{Binding: plan.VerificationRepairBinding{Command: "make verify", HeadSHA: "failed-head", Fingerprint: "failure"}, CreatedAt: time.Now().UTC()}
	if err := record.AppendVerificationRepair(request); err != nil {
		t.Fatal(err)
	}

	if err := CheckRequestCanStart(detail, Request{}); err != nil {
		t.Fatalf("ordinary crash recovery run refused: %v", err)
	}
	if got := plan.VerificationRepairAttemptCount(detail); got != 1 {
		t.Fatalf("ordinary resume consumed another attempt: %d", got)
	}
}

func TestRequireCurrentFailedFinalVerificationBoundaryRefusesStaleBranchOrHead(t *testing.T) {
	tests := []struct {
		name       string
		liveBranch string
		liveHead   string
	}{
		{name: "branch", liveBranch: "other", liveHead: "failed-head"},
		{name: "head", liveBranch: "feature", liveHead: "other-head"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, Fingerprint: "failure"}
			runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
				if command != "git" {
					return nil
				}
				switch strings.Join(args, " ") {
				case "branch --show-current":
					_, _ = io.WriteString(stdout, test.liveBranch+"\n")
				case "rev-parse HEAD":
					_, _ = io.WriteString(stdout, test.liveHead+"\n")
				}
				return nil
			}
			execution := testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})
			execution.ExecutionRoot = t.TempDir()

			_, err := requireCurrentFailedFinalVerificationBoundary(context.Background(), detail, execution, "reverification")
			if err == nil || !strings.Contains(err.Error(), "reverification worktree boundary is stale") {
				t.Fatalf("boundary error = %v", err)
			}
		})
	}
}

func TestRequireReverifyFinalVerificationBoundary(t *testing.T) {
	tests := []struct {
		name       string
		liveBranch string
		liveHead   string
		status     string
		diverged   bool
		unsettled  bool
		wantErr    string
	}{
		{name: "exact head", liveBranch: "feature", liveHead: "failed-head"},
		{name: "advanced clean head", liveBranch: "feature", liveHead: "advanced-head"},
		{name: "diverged head", liveBranch: "feature", liveHead: "diverged-head", diverged: true, wantErr: "or its descendant"},
		{name: "dirty worktree", liveBranch: "feature", liveHead: "failed-head", status: " M repaired.go\n", wantErr: "clean worktree"},
		{name: "different branch", liveBranch: "other", liveHead: "failed-head", wantErr: "does not match recorded branch"},
		{name: "unsettled slice work", liveBranch: "feature", liveHead: "failed-head", unsettled: true, wantErr: "settled slice work"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, Fingerprint: "failure"}
			if test.unsettled {
				detail.State.Status = plan.StatusInProgress
				detail.State.Plan.CompletedSlices = nil
				detail.State.Plan.PendingSlices = []string{"001-a"}
				detail.Slices.Slices[0].Status = plan.StatusPending
			}
			runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
				if command != "git" {
					return nil
				}
				switch runGitKey(args) {
				case "status --porcelain":
					_, _ = io.WriteString(stdout, test.status)
				case "branch --show-current":
					_, _ = io.WriteString(stdout, test.liveBranch+"\n")
				case "rev-parse HEAD":
					_, _ = io.WriteString(stdout, test.liveHead+"\n")
				case "merge-base --is-ancestor failed-head diverged-head":
					if test.diverged {
						return fmt.Errorf("exit status 1")
					}
				}
				return nil
			}
			execution := testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})
			execution.ExecutionRoot = t.TempDir()

			failure, err := requireReverifyFinalVerificationBoundary(context.Background(), detail, execution)
			if test.wantErr == "" {
				if err != nil || failure != detail.State.Plan.FinalVerification {
					t.Fatalf("boundary = %+v, %v", failure, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("boundary error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestExecuteAutomaticVerificationRepair(t *testing.T) {
	tests := []struct {
		name         string
		failures     int
		explicit     bool
		resume       bool
		maxSlices    int
		failureKind  plan.FinalVerificationFailureKind
		wantAttempts int
		wantRuns     int
		wantStops    int
		wantError    bool
	}{
		{name: "first attempt succeeds", failures: 1, wantAttempts: 1, wantRuns: 2},
		{name: "second attempt succeeds", failures: 2, wantAttempts: 2, wantRuns: 3},
		{name: "exhaustion", failures: 3, wantAttempts: 2, wantRuns: 3, wantStops: 1, wantError: true},
		{name: "max slices consumed by ordinary work", failures: 1, maxSlices: 1, wantRuns: 1, wantError: true},
		{name: "max slices permits only one repair", failures: 2, maxSlices: 2, wantAttempts: 1, wantRuns: 2, wantError: true},
		{name: "ordinary service resumes after append", resume: true, wantAttempts: 1, wantRuns: 1},
		{name: "tool missing", failures: 1, failureKind: plan.FinalVerificationFailureKindToolMissing, wantRuns: 1, wantError: true},
		{name: "timeout", failures: 1, failureKind: plan.FinalVerificationFailureKindTimeout, wantRuns: 1, wantError: true},
		{name: "cancelled", failures: 1, failureKind: plan.FinalVerificationFailureKindCancelled, wantRuns: 1, wantError: true},
		{name: "invalid command", failures: 1, failureKind: plan.FinalVerificationFailureKindInvalidCommand, wantRuns: 1, wantError: true},
		{name: "explicit repair is single shot", failures: 1, explicit: true, wantAttempts: 1, wantRuns: 1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			plansDir := t.TempDir()
			planDir := filepath.Join(plansDir, "plan-a")
			if err := os.MkdirAll(planDir, 0o700); err != nil {
				t.Fatal(err)
			}
			detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
			detail.Dir = planDir
			detail.State.Repo.Root = root
			detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: root, Branch: "feature", HeadSHA: "head123"}
			if test.explicit || test.resume {
				detail = completedReviewPlanDetail(planDir)
				detail.State.Repo.Root = root
				detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: root, Branch: "feature", HeadSHA: "head123"}
				detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "head123", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "initial-failure"}
			}
			if test.resume {
				detail.State.Repo.Root = t.TempDir()
			}
			persistRunArtifacts(t, planDir, detail)
			repo := plan.NewFileRepository(plansDir)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.failureKind == plan.FinalVerificationFailureKindTimeout {
				var timeoutCancel context.CancelFunc
				ctx, timeoutCancel = context.WithTimeout(ctx, time.Second)
				defer timeoutCancel()
			}
			commandErr := errors.New("tests failed")
			var runErr = commandErr
			switch test.failureKind {
			case plan.FinalVerificationFailureKindToolMissing:
				runErr = finalVerificationProcessError(t, exec.Command("sh", "-c", "exit 127"))
			case plan.FinalVerificationFailureKindInvalidCommand:
				runErr = finalVerificationProcessError(t, exec.Command("sh", "-c", "exit 126"))
			}
			git := newScriptedGitRunner("")
			var identityRunner CommandRunner
			if test.resume {
				identityRunner = interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "feature", "head123")
			}
			gateCalls, reviewCalls, reloads := 0, 0, 0
			var runs []string
			var out bytes.Buffer
			execution := testRunExecution(ExecutionConfig{
				ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, ReviewEnabled: true, MaxSlices: test.maxSlices},
				RepairVerification: test.explicit,
			}, RunDependencies{
				EventAppender: repo,
				RootResolver:  ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) { return root, nil }),
				CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if name != "sh" {
						if test.resume && name == "git" {
							switch runGitKey(args) {
							case "rev-parse --show-toplevel", "rev-parse --git-common-dir", "rev-parse --git-dir":
								return identityRunner(ctx, cwd, name, args, stdout, stderr)
							}
						}
						return git.Run(ctx, cwd, name, args, stdout, stderr)
					}
					if cwd != root {
						t.Fatalf("verification ran outside execution root: %s", cwd)
					}
					gateCalls++
					if strings.Join(args, " ") != "-c make verify" {
						t.Fatalf("unexpected gate: %v", args)
					}
					if gateCalls <= test.failures {
						switch test.failureKind {
						case plan.FinalVerificationFailureKindCancelled:
							cancel()
						case plan.FinalVerificationFailureKindTimeout:
							<-ctx.Done()
						}
						_, _ = io.WriteString(stderr, "tests failed")
						return runErr
					}
					return nil
				},
				SliceExecutor: sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
					loaded, err := repo.ResolvePlan(ctx, run.PlanDir)
					if err != nil {
						return err
					}
					record, err := repo.PlanRecord(loaded)
					if err != nil {
						return err
					}
					intent := plan.SliceCommitIntent{Hash: "intent-" + run.SliceID, Policy: CommitPolicySlice.String(), StartingBranch: "feature", StartingHead: git.Head, CreatedAt: time.Now().UTC()}
					if err := record.RecordSliceCommitIntent(run.SliceID, intent); err != nil {
						return err
					}
					runs = append(runs, run.SliceID)
					git.Head = "after-" + run.SliceID
					return record.CompleteSliceWithOutcome(run.SliceID, "done", nil, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: git.Head}, time.Now().UTC())
				}),
				ReviewCreator: reviewCreatorFunc(func(_ context.Context, run ReviewRun) (plan.PlanReview, error) {
					reviewCalls++
					if run.Detail.State.Plan.FinalVerification.Result != finalVerificationPassed {
						t.Fatal("review reached before verification passed")
					}
					return plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}, nil
				}),
			})
			var pendingRepairID string
			if test.explicit || test.resume {
				execution.ExecutionRoot = root
				resolveExecutorDefaults(&execution)
				if err := appendVerificationRepair(ctx, detail, execution); err != nil {
					t.Fatal(err)
				}
				pendingRepairID = plan.Derive(detail, time.Time{}).NextSliceID
				if !strings.HasPrefix(pendingRepairID, plan.VerificationRepairSlicePrefix) {
					t.Fatalf("append did not leave a pending repair: %q", pendingRepairID)
				}
			}
			var err error
			if test.resume {
				// Discard the append's in-memory detail, as a new invocation would
				// after a crash between the journaled append and the next handoff.
				service := NewService(repo, &out, Options{ExecutionConfig: execution.Config, RunDependencies: execution.Dependencies})
				err = service.Execute(ctx, Request{Input: planDir, ResolvedRunOptions: execution.Config.ResolvedRunOptions})
			} else {
				err = executeDetailWithExecution(ctx, detail, func(ctx context.Context, prior *plan.PlanDetail) (*plan.PlanDetail, error) {
					reloads++
					return repo.ResolvePlan(ctx, prior.Dir)
				}, &out, execution)
			}
			var verificationErr *FinalVerificationError
			if test.wantError {
				if !errors.As(err, &verificationErr) || !errors.Is(err, runErr) || !strings.Contains(err.Error(), "finalize completed run:") {
					t.Fatalf("execute error = %v, want original wrapped verification failure", err)
				}
				wantKind := test.failureKind
				if wantKind == "" {
					wantKind = plan.FinalVerificationFailureKindCode
				}
				if verificationErr.Verification.FailureKind != wantKind {
					t.Fatalf("failure kind = %q, want %q", verificationErr.Verification.FailureKind, wantKind)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			loaded, err := repo.ResolvePlan(context.Background(), planDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := plan.VerificationRepairAttemptCount(loaded); got != test.wantAttempts {
				t.Fatalf("repair attempts = %d, want %d", got, test.wantAttempts)
			}
			if got := countPlanEvents(loaded.Events, plan.EventTypeVerificationRepairCreated); got != test.wantAttempts {
				t.Fatalf("repair created events = %d, want %d", got, test.wantAttempts)
			}
			if got := countPlanEvents(loaded.Events, plan.EventTypeVerificationRepairStopped); got != test.wantStops {
				t.Fatalf("repair stopped events = %d, want %d", got, test.wantStops)
			}
			if len(runs) != test.wantRuns || gateCalls != test.wantRuns {
				t.Fatalf("runs = %v, gate calls = %d, want %d each", runs, gateCalls, test.wantRuns)
			}
			automaticAttempts := test.wantAttempts
			if test.explicit || test.resume {
				automaticAttempts = 0
				if len(runs) != 1 || runs[0] != pendingRepairID {
					t.Fatalf("pending repair handoffs = %v", runs)
				}
			}
			if !test.resume && reloads != test.wantRuns+automaticAttempts {
				t.Fatalf("reloads = %d, want one per completed slice and automatic append", reloads)
			}
			for attempt := 1; attempt <= automaticAttempts; attempt++ {
				if !strings.Contains(out.String(), fmt.Sprintf("scheduled attempt %d of %d for %q", attempt, plan.VerificationRepairAttemptCap, "make verify")) {
					t.Fatalf("missing scheduling message: %s", out.String())
				}
			}
			wantReviews := 1
			if test.wantError {
				wantReviews = 0
			}
			if reviewCalls != wantReviews || len(loaded.State.Plan.PendingSlices) != 0 {
				t.Fatalf("review calls = %d, pending = %v", reviewCalls, loaded.State.Plan.PendingSlices)
			}
			for _, event := range loaded.Events {
				if event.Type == plan.EventTypeVerificationRepairStopped && (event.Attempts != plan.VerificationRepairAttemptCap || event.Command != "make verify" || event.HeadSHA != git.Head || !strings.Contains(event.Reason, "--reverify")) {
					t.Fatalf("unexpected stop evidence: %+v", event)
				}
			}
		})
	}
}

func TestRecoveryFinalVerificationDoesNotScheduleRepair(t *testing.T) {
	for _, operation := range []string{"zero-slice PR recovery", "resume review"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newPullRequestOrchestrationFixture(t)
			if err := os.WriteFile(filepath.Join(fixture.worktreeRoot, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runCommitTestGitCommand(t, fixture.worktreeRoot, "add", "Makefile")
			runCommitTestGitCommand(t, fixture.worktreeRoot, "commit", "-m", "test: add verification gate")
			head := strings.TrimSpace(runCommitTestGitOutput(t, fixture.worktreeRoot, "rev-parse", "HEAD"))
			repo := plan.NewFileRepository(fixture.plansRoot)
			ctx := context.Background()
			detail, err := repo.ResolvePlan(ctx, fixture.planDir)
			if err != nil {
				t.Fatal(err)
			}
			detail.State.Plan.Review = nil
			detail.State.Status = plan.StatusInReview
			detail.State.Workspace.HeadSHA = head
			state, err := json.Marshal(detail.State)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.planDir, "state.json"), state, 0o600); err != nil {
				t.Fatal(err)
			}

			gateErr := errors.New("repository tests failed")
			gateCalls, handoffs, reviews, pullRequests := 0, 0, 0, 0
			options := ResolvedRunOptions{Mode: ModeRun, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, Agent: AgentPi, ReviewEnabled: true, PullRequest: true}
			service := NewService(repo, io.Discard, Options{
				ExecutionConfig: ExecutionConfig{ResolvedRunOptions: options},
				RunDependencies: RunDependencies{
					CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
						if name != "sh" {
							return defaultCommandRunner(ctx, cwd, name, args, stdout, stderr)
						}
						gateCalls++
						if cwd != fixture.worktreeRoot || strings.Join(args, " ") != "-c make verify" {
							t.Fatalf("unexpected verification: %s %s %v", cwd, name, args)
						}
						return gateErr
					},
					SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
						handoffs++
						return errors.New("unexpected implementation handoff")
					}),
					ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
						reviews++
						return plan.PlanReview{}, errors.New("unexpected review")
					}),
					PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
						pullRequests++
						return plan.PullRequest{}, errors.New("unexpected pull request")
					}),
				},
			})
			request := Request{Input: fixture.planDir, ResolvedRunOptions: options}
			if operation == "resume review" {
				err = service.ResumeReview(ctx, request)
			} else {
				err = service.Execute(ctx, request)
			}
			var failure *FinalVerificationError
			if !errors.As(err, &failure) || !errors.Is(err, gateErr) || errors.Is(err, errVerificationRepairScheduled) {
				t.Errorf("recovery error = %v, want original FinalVerificationError", err)
			} else if failure.Verification.FailureKind != plan.FinalVerificationFailureKindCode || failure.Verification.HeadSHA != head || failure.Verification.Command != "make verify" {
				t.Errorf("verification failure = %+v", failure.Verification)
			}
			if gateCalls != 1 || handoffs != 0 || reviews != 0 || pullRequests != 0 {
				t.Errorf("gate/handoff/review/PR calls = %d/%d/%d/%d, want 1/0/0/0", gateCalls, handoffs, reviews, pullRequests)
			}
			loaded, err := repo.ResolvePlan(ctx, fixture.planDir)
			if err != nil {
				t.Fatal(err)
			}
			if plan.VerificationRepairAttemptCount(loaded) != 0 || len(loaded.State.Plan.PendingSlices) != 0 {
				t.Errorf("recovery generated repair work: %+v", loaded.Slices.Slices)
			}
			if got := countPlanEvents(loaded.Events, plan.EventTypeVerificationRepairCreated); got != 0 {
				t.Errorf("repair creation events = %d, want none", got)
			}
			if decision := plan.DeriveVerificationRecovery(loaded); decision.Kind != plan.PlanActionRepairVerification {
				t.Errorf("expected eligible failure left for explicit repair, got %+v", decision)
			}
		})
	}
}

// detachedRepairRecord persists appends without updating the caller's detail,
// so the execution loop cannot accidentally rely on mutation side effects.
type detachedRepairRecord struct {
	*plan.PlanRecord
	repo *plan.FileRepository
	dir  string
}

func (r detachedRepairRecord) AppendVerificationRepair(request plan.VerificationRepairRequest) error {
	detail, err := r.repo.ResolvePlan(context.Background(), r.dir)
	if err != nil {
		return err
	}
	record, err := r.repo.PlanRecord(detail)
	if err != nil {
		return err
	}
	return record.AppendVerificationRepair(request)
}

func TestExecuteVerificationRepairRequiresFreshDetail(t *testing.T) {
	for _, failReload := range []bool{false, true} {
		t.Run(fmt.Sprintf("reload error=%t", failReload), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			plansDir := t.TempDir()
			planDir := filepath.Join(plansDir, "plan-a")
			if err := os.MkdirAll(planDir, 0o700); err != nil {
				t.Fatal(err)
			}
			detail := completedReviewPlanDetail(planDir)
			detail.State.Repo.Root = root
			detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: root, Branch: "feature", HeadSHA: "head123"}
			persistRunArtifacts(t, planDir, detail)
			repo := plan.NewFileRepository(plansDir)
			git := newScriptedGitRunner("")
			gateCalls, reloads, handoffs := 0, 0, 0
			reloadErr := errors.New("reload unavailable")
			handoffErr := errors.New("stop at fresh repair handoff")
			var repairID string
			execution := testRunExecution(ExecutionConfig{
				ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated},
			}, RunDependencies{
				EventAppender: repo,
				RootResolver:  ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) { return root, nil }),
				PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
					record, err := repo.PlanRecord(detail)
					return detachedRepairRecord{PlanRecord: record, repo: repo, dir: detail.Dir}, err
				},
				CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if name != "sh" {
						return git.Run(ctx, cwd, name, args, stdout, stderr)
					}
					gateCalls++
					return errors.New("tests failed")
				},
				SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
					handoffs++
					if run.SliceID != repairID {
						t.Fatalf("handoff slice = %q, want reloaded %q", run.SliceID, repairID)
					}
					return handoffErr
				}),
			})
			resolveExecutorDefaults(&execution)
			// Enter the real loop at the boundary just after ordinary work completed.
			executor := detailExecutor{
				runs: 1, out: io.Discard, execution: execution,
				sliceExecutor: execution.Dependencies.SliceExecutor,
				finalizer:     newFinalizer(io.Discard, execution),
				reload: func(ctx context.Context, prior *plan.PlanDetail) (*plan.PlanDetail, error) {
					reloads++
					fresh, err := repo.ResolvePlan(ctx, prior.Dir)
					if err != nil {
						return nil, err
					}
					if plan.VerificationRepairAttemptCount(prior) != 0 || plan.Derive(prior, time.Time{}).NextSliceID != "" {
						t.Fatal("fixture did not retain stale pre-append detail")
					}
					repairID = plan.Derive(fresh, time.Time{}).NextSliceID
					if !strings.HasPrefix(repairID, plan.VerificationRepairSlicePrefix) || plan.VerificationRepairAttemptCount(fresh) != 1 {
						t.Fatal("reload did not find persisted pending repair")
					}
					if failReload {
						return nil, reloadErr
					}
					return fresh, nil
				},
			}
			err := executor.execute(context.Background(), detail)
			if failReload {
				if !errors.Is(err, reloadErr) || !strings.Contains(err.Error(), "reload plan after scheduling verification repair") || handoffs != 0 {
					t.Fatalf("reload failure = %v, handoffs = %d", err, handoffs)
				}
			} else if !errors.Is(err, handoffErr) || handoffs != 1 {
				t.Fatalf("fresh handoff = %v, calls = %d", err, handoffs)
			}
			if gateCalls != 1 || reloads != 1 {
				t.Fatalf("gate calls = %d, reloads = %d; must not re-enter finalization with stale detail", gateCalls, reloads)
			}
			loaded, err := repo.ResolvePlan(context.Background(), planDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := countPlanEvents(loaded.Events, plan.EventTypeVerificationRepairCreated); got != 1 {
				t.Fatalf("repair created events = %d, want one durable append", got)
			}
		})
	}
}

func TestRequireNoCurrentFinalVerificationFailureAcceptsPassingEvidence(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Result: finalVerificationPassed}
	if err := requireNoCurrentFinalVerificationFailure(context.Background(), detail, testRunExecution(ExecutionConfig{}, RunDependencies{})); err != nil {
		t.Fatalf("passing verification refused review: %v", err)
	}
}
