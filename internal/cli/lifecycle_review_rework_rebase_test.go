package cli

import (
	"bytes"
	"context"
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
	runpkg "github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/workspace"
)

// TestLifecycleReviewReworkRebase exercises the complete incident lifecycle
// against real Git and persisted plan artifacts.
func TestLifecycleReviewReworkRebase(t *testing.T) {
	fixture := newLifecycleGitFixture(t, "lifecycle-incident")
	fixture.completeOriginalSlice(t)
	oldFeatureHead := lifecycleGitOutput(t, fixture.worktree, "rev-parse", "HEAD")
	lifecycleCommit(t, fixture.repoRoot, "default.txt", "default advanced\n", "advance default")
	newBase := lifecycleGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")

	// The same preparation boundary used by a run must migrate the proven
	// completion HEAD, write intent before rebase, and settle it immediately.
	if err := fixture.prepare(t, nil); err != nil {
		t.Fatal(err)
	}
	rebasedHead := lifecycleGitOutput(t, fixture.worktree, "rev-parse", "HEAD")
	if rebasedHead == oldFeatureHead || !lifecycleIsAncestor(t, fixture.worktree, newBase, rebasedHead) {
		t.Fatalf("workspace was not rebased onto %s: old=%s live=%s", newBase, oldFeatureHead, rebasedHead)
	}
	detail := fixture.reload(t)
	if detail.State.Workspace.RebaseIntent != nil || detail.State.Workspace.BaseSHA != newBase || detail.State.Workspace.HeadSHA != rebasedHead {
		t.Fatalf("rebase was not durably settled at the live boundary: %#v", detail.State.Workspace)
	}

	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/review.go", Message: "reload lifecycle state", Suggestion: "serialize review and rework"}
	reviewSerialized := false
	reviewer := &lifecycleReviewCreator{
		fixture:    fixture,
		review:     plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Summary: "rework", FindingsCount: 1, Findings: []plan.ReviewFinding{finding}, Base: newBase, Head: rebasedHead, ReviewedAt: fixture.now.Add(3 * time.Minute)},
		serialized: &reviewSerialized,
	}
	reviewService := runpkg.NewService(plan.NewFileRepository(fixture.plansRoot), io.Discard, runpkg.Options{
		ExecutionConfig: runpkg.ExecutionConfig{ResolvedRunOptions: runpkg.ResolvedRunOptions{CommitPolicy: runpkg.CommitPolicyNone, ExecutionMode: runpkg.ExecutionModeIsolated, ReviewEnabled: true}},
		RunDependencies: runpkg.RunDependencies{
			ReviewCreator: reviewer,
			CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return nil },
			WorkspacePreparer: func(context.Context, *plan.PlanDetail, runpkg.WorkspaceResolverInput) (string, error) {
				return fixture.worktree, nil
			},
		},
	})
	if _, err := reviewService.Review(context.Background(), runpkg.Request{Input: fixture.planID, ResolvedRunOptions: runpkg.ResolvedRunOptions{CommitPolicy: runpkg.CommitPolicyNone, ExecutionMode: runpkg.ExecutionModeIsolated, ReviewEnabled: true}}); err != nil {
		t.Fatal(err)
	}
	if !reviewSerialized {
		t.Fatal("review did not exclude a competing lifecycle driver")
	}

	// Exercise the CLI rework --run handoff. A competing lifecycle driver must
	// remain excluded while the nested run re-enters the lock and completes the
	// generated slice on the existing rebased branch.
	competitorEntered := make(chan struct{})
	competitorDone := make(chan error, 1)
	executed := false
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(service runpkg.Service, ctx context.Context, request runpkg.Request) error {
		go func() { //nolint:gosec // G118: an independent context is the contention condition under test.
			locked := fixture.reload(t)
			competitorDone <- runpkg.WithPlanRunLock(context.Background(), locked, fixture.now, func(context.Context) error {
				close(competitorEntered)
				return nil
			})
		}()
		select {
		case <-competitorEntered:
			return errors.New("competing lifecycle driver entered during rework --run")
		default:
		}
		return service.WithPlanRunLock(ctx, request, func(context.Context) error {
			current := fixture.reload(t)
			if current.State.Status != plan.StatusInProgress || len(current.State.Plan.PendingSlices) != 1 {
				return fmt.Errorf("rework was not reopened before execution: status=%s pending=%v", current.State.Status, current.State.Plan.PendingSlices)
			}
			sliceID := current.State.Plan.PendingSlices[0]
			startHead := lifecycleGitOutput(t, fixture.worktree, "rev-parse", "HEAD")
			currentRecord := fixture.record(t, current)
			if err := currentRecord.StartSliceWithRunBoundary(sliceID, fixture.worktree, "slice", nil, plan.SliceExecutionStart{Branch: fixture.branch, Head: startHead, CommitPolicy: "slice", WorkspaceStrategy: plan.WorkspaceStrategyWorktree}, fixture.now.Add(4*time.Minute)); err != nil {
				return err
			}
			intent := plan.SliceCommitIntent{Hash: "rework-intent", Policy: "slice", StartingBranch: fixture.branch, StartingHead: startHead, Message: "fix(cli): address review finding", CreatedAt: fixture.now.Add(5 * time.Minute)}
			if err := currentRecord.RecordSliceCommitIntent(sliceID, intent); err != nil {
				return err
			}
			lifecycleCommit(t, fixture.worktree, "review-fix.txt", "fixed\n", "fix review finding")
			commit := lifecycleGitOutput(t, fixture.worktree, "rev-parse", "HEAD")
			executed = true
			return currentRecord.CompleteSliceWithOutcome(sliceID, "fixed review finding", nil, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: commit}, fixture.now.Add(5*time.Minute))
		})
	}
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out, Now: func() time.Time { return fixture.now }}).Run(context.Background(), []string{"--plans-dir", fixture.plansRoot, "rework", "--run", fixture.planID}); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("rework --run did not execute the generated slice")
	}
	select {
	case err := <-competitorDone:
		if !errors.Is(err, runpkg.ErrCannotStart) {
			t.Fatalf("competing lifecycle driver error = %v, want lock refusal", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("competing lifecycle driver did not observe lock ownership")
	}
	releasedRan := false
	if err := runpkg.WithPlanRunLock(context.Background(), fixture.reload(t), fixture.now, func(context.Context) error {
		releasedRan = true
		return nil
	}); err != nil || !releasedRan {
		t.Fatalf("rework --run did not release lifecycle lock: ran=%t err=%v", releasedRan, err)
	}
	final := fixture.reload(t)
	liveHead := lifecycleGitOutput(t, fixture.worktree, "rev-parse", "HEAD")
	completedID := final.State.Plan.CompletedSlices[len(final.State.Plan.CompletedSlices)-1]
	var completed *plan.Slice
	for i := range final.Slices.Slices {
		if final.Slices.Slices[i].ID == completedID {
			completed = &final.Slices.Slices[i]
			break
		}
	}
	if final.State.Status != plan.StatusInReview || completed == nil || completed.Completion == nil || completed.Completion.CommitSHA != liveHead {
		t.Fatalf("rework completion does not match live HEAD %s: status=%s slice=%#v", liveHead, final.State.Status, completed)
	}
}

func TestLifecycleReviewReworkRebaseRecoveryVariants(t *testing.T) {
	for _, test := range []struct {
		name        string
		extraCommit bool
		wantRecover bool
	}{
		{name: "interrupted after Git rebase recovers exact series", wantRecover: true},
		{name: "extra commit refuses recovery", extraCommit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, "rebase-recovery-"+strings.ReplaceAll(test.name, " ", "-"))
			fixture.completeOriginalSlice(t)
			fixture.reopenForRecovery(t)
			lifecycleCommit(t, fixture.repoRoot, "default.txt", "new base\n", "advance default")

			// Inject the crash window after Git has rewritten HEAD but before the
			// atomic settlement persists workspace metadata and clears the intent.
			settlementErr := errors.New("injected settlement crash")
			factory := func(detail *plan.PlanDetail) (workspace.PlanRecord, error) {
				record := fixture.record(t, detail)
				return failingLifecycleRebaseRecord{PlanRecord: record, settlementErr: settlementErr}, nil
			}
			err := fixture.prepare(t, factory)
			if err == nil || !strings.Contains(err.Error(), settlementErr.Error()) {
				t.Fatalf("prepare error = %v, want injected settlement crash", err)
			}
			interrupted := fixture.reload(t)
			if interrupted.State.Workspace.RebaseIntent == nil {
				t.Fatal("settlement crash did not retain durable rebase intent")
			}
			if interrupted.State.Workspace.HeadSHA == lifecycleGitOutput(t, fixture.worktree, "rev-parse", "HEAD") {
				t.Fatal("crash fixture must distinguish durable and live HEADs")
			}
			if test.extraCommit {
				lifecycleCommit(t, fixture.worktree, "unexpected.txt", "drift\n", "unexpected extra commit")
			}

			executor := &lifecycleRecoveryExecutor{err: errors.New("stop after recovery")}
			service := runpkg.NewService(plan.NewFileRepository(fixture.plansRoot), io.Discard, runpkg.Options{
				ExecutionConfig: runpkg.ExecutionConfig{ResolvedRunOptions: runpkg.ResolvedRunOptions{CommitPolicy: runpkg.CommitPolicySlice, ExecutionMode: runpkg.ExecutionModeIsolated, MaxSlices: 1, ReviewEnabled: false}},
				RunDependencies: runpkg.RunDependencies{SliceExecutor: executor},
			})
			err = service.Execute(context.Background(), runpkg.Request{Input: fixture.planID, ResolvedRunOptions: runpkg.ResolvedRunOptions{CommitPolicy: runpkg.CommitPolicySlice, ExecutionMode: runpkg.ExecutionModeIsolated, MaxSlices: 1, ReviewEnabled: false}})
			if test.wantRecover {
				if !errors.Is(err, executor.err) || executor.calls != 1 {
					t.Fatalf("exact recovery error=%v calls=%d, want executor handoff", err, executor.calls)
				}
				if recovered := fixture.reload(t); recovered.State.Workspace.RebaseIntent != nil {
					t.Fatalf("exact recovery did not clear intent: %#v", recovered.State.Workspace.RebaseIntent)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "commit-series proof differs") || executor.calls != 0 {
					t.Fatalf("extra-commit recovery error=%v calls=%d", err, executor.calls)
				}
				if refused := fixture.reload(t); refused.State.Workspace.RebaseIntent == nil {
					t.Fatal("refused recovery cleared durable intent")
				}
			}
		})
	}
}

type lifecycleGitFixture struct {
	plansRoot string
	planID    string
	repoRoot  string
	worktree  string
	branch    string
	now       time.Time
}

func newLifecycleGitFixture(t *testing.T, planID string) *lifecycleGitFixture {
	t.Helper()
	fixture := &lifecycleGitFixture{
		plansRoot: t.TempDir(),
		planID:    planID,
		repoRoot:  t.TempDir(),
		branch:    "tao/" + planID,
		now:       time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	}
	lifecycleGitRun(t, fixture.repoRoot, "init", "-b", "main")
	lifecycleGitRun(t, fixture.repoRoot, "config", "user.name", "Tao Test")
	lifecycleGitRun(t, fixture.repoRoot, "config", "user.email", "tao-test@example.com")
	lifecycleCommit(t, fixture.repoRoot, "README.md", "initial\n", "initial")
	base := lifecycleGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")

	planDir := filepath.Join(fixture.plansRoot, fixture.planID)
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	state := plan.State{
		Schema:    "tao.plan.state.v1",
		Status:    plan.StatusInProgress,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
		Repo:      plan.Repo{Name: "lifecycle", Root: fixture.repoRoot, Branch: "main", BaseCommit: base},
		Plan: plan.PlanState{
			ID:            fixture.planID,
			Title:         "Lifecycle incident regression",
			PendingSlices: []string{"001-original"},
			Timing:        plan.PlanTiming{StartedAt: &fixture.now, LastActivityAt: &fixture.now},
		},
		GlobalInvariants: []string{},
		OpenQuestions:    []string{},
	}
	slices := plan.SlicesFile{
		Schema: "tao.plan.slices.v1", PlanID: fixture.planID,
		Execution: plan.Execution{Mode: "serial", ParallelSafe: false},
		Slices: []plan.Slice{{
			ID: "001-original", Title: "Original work", Status: plan.StatusPending,
			DependsOn: []string{}, Timing: plan.SliceTiming{CreatedAt: fixture.now, UpdatedAt: fixture.now},
			Goal: "complete original work", Context: "incident setup", Tasks: []string{"change review lifecycle"},
			ExpectedFiles: []string{"internal/cli/review.go"},
			Verification:  plan.Verification{Commands: []string{"go test ./internal/cli -run TestReview"}, ManualChecks: []string{}},
		}},
	}
	record, err := plan.NewPlanRecord(planDir, &plan.PlanDetail{Dir: planDir, State: state, Slices: slices})
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *lifecycleGitFixture) reload(t *testing.T) *plan.PlanDetail {
	t.Helper()
	detail, err := plan.NewFileRepository(f.plansRoot).ResolvePlan(context.Background(), f.planID)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func (f *lifecycleGitFixture) record(t *testing.T, detail *plan.PlanDetail) *plan.PlanRecord {
	t.Helper()
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func (f *lifecycleGitFixture) prepare(t *testing.T, factory workspace.PlanRecordFactory) error {
	t.Helper()
	if factory == nil {
		factory = func(detail *plan.PlanDetail) (workspace.PlanRecord, error) {
			return plan.NewPlanRecord(detail.Dir, detail)
		}
	}
	config := workspace.DefaultConfig()
	config.DependencyInstallBehavior = workspace.DependencyInstallNever
	root, err := (workspace.ExecutionPreparer{PlanRecordFactory: factory, Config: config, Now: func() time.Time { return f.now }}).Prepare(context.Background(), f.reload(t), workspace.ExecutionPrepareOptions{ExecutionMode: "isolated"})
	if root != "" {
		f.worktree = root
	}
	return err
}

func (f *lifecycleGitFixture) completeOriginalSlice(t *testing.T) {
	t.Helper()
	if err := f.prepare(t, nil); err != nil {
		t.Fatal(err)
	}
	detail := f.reload(t)
	record := f.record(t, detail)
	startHead := lifecycleGitOutput(t, f.worktree, "rev-parse", "HEAD")
	boundary := plan.SliceExecutionStart{Branch: f.branch, Head: startHead, CommitPolicy: "slice", WorkspaceStrategy: plan.WorkspaceStrategyWorktree}
	if err := record.StartSliceWithRunBoundary("001-original", f.worktree, "slice", nil, boundary, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	intent := plan.SliceCommitIntent{Hash: "original-intent", Policy: "slice", StartingBranch: f.branch, StartingHead: startHead, Message: "test(cli): complete original work", CreatedAt: f.now.Add(2 * time.Minute)}
	if err := record.RecordSliceCommitIntent("001-original", intent); err != nil {
		t.Fatal(err)
	}
	lifecycleCommit(t, f.worktree, "review-change.txt", "original\n", "complete original work")
	head := lifecycleGitOutput(t, f.worktree, "rev-parse", "HEAD")
	if err := record.CompleteSliceWithOutcome("001-original", "original complete", nil, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: head}, f.now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func (f *lifecycleGitFixture) reopenForRecovery(t *testing.T) {
	t.Helper()
	detail := f.reload(t)
	record := f.record(t, detail)
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/review.go", Message: "recover interrupted rebase"}
	head := lifecycleGitOutput(t, f.worktree, "rev-parse", "HEAD")
	if err := record.RecordReviewCompleted(plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, FindingsCount: 1, Findings: []plan.ReviewFinding{finding}, Base: detail.State.Workspace.BaseSHA, Head: head, ReviewedAt: f.now.Add(4 * time.Minute)}, "pi"); err != nil {
		t.Fatal(err)
	}
	detail = f.reload(t)
	reworkSlice := plan.Slice{
		ID: "r101-recovery", Title: "Recovery work", Status: plan.StatusPending,
		DependsOn: []string{}, Timing: plan.SliceTiming{CreatedAt: f.now.Add(5 * time.Minute), UpdatedAt: f.now.Add(5 * time.Minute)},
		Goal: "recover rebase", Context: "recovery fixture", Tasks: []string{"run after recovery"},
		ExpectedFiles: []string{"internal/cli/review.go"}, Verification: plan.Verification{Commands: []string{"true"}, ManualChecks: []string{}},
	}
	if err := f.record(t, detail).Reopen([]plan.Slice{reworkSlice}, f.now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

type failingLifecycleRebaseRecord struct {
	*plan.PlanRecord
	settlementErr error
}

func (r failingLifecycleRebaseRecord) SettleWorkspaceRebase(plan.WorkspaceRebaseIntent, plan.WorkspaceRebaseSettlement) error {
	return r.settlementErr
}

type lifecycleReviewCreator struct {
	fixture    *lifecycleGitFixture
	review     plan.PlanReview
	serialized *bool
}

func (c *lifecycleReviewCreator) CreateReview(_ context.Context, run runpkg.ReviewRun) (plan.PlanReview, error) {
	competitorErr := runpkg.WithPlanRunLock(context.Background(), run.Detail, c.fixture.now, func(context.Context) error {
		return errors.New("competing lifecycle driver entered during review")
	})
	if !errors.Is(competitorErr, runpkg.ErrCannotStart) {
		return plan.PlanReview{}, fmt.Errorf("competing review lock error (want lock refusal): %w", competitorErr)
	}
	*c.serialized = true
	record, err := plan.NewPlanRecord(run.PlanDir, run.Detail)
	if err != nil {
		return plan.PlanReview{}, err
	}
	if err := record.RecordReviewCompleted(c.review, "pi"); err != nil {
		return plan.PlanReview{}, err
	}
	return c.review, nil
}

type lifecycleRecoveryExecutor struct {
	calls int
	err   error
}

func (e *lifecycleRecoveryExecutor) RunSlice(context.Context, runpkg.SliceRun) error {
	e.calls++
	return e.err
}

func lifecycleCommit(t *testing.T, root string, name string, content string, message string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	lifecycleGitRun(t, root, "add", "--", name)
	lifecycleGitRun(t, root, "commit", "-m", message)
}

func lifecycleGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: test-only Git arguments are supplied by this file.
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func lifecycleGitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	_ = lifecycleGitOutput(t, root, args...)
}

func lifecycleIsAncestor(t *testing.T, root string, ancestor string, descendant string) bool {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant) //nolint:gosec // G204: SHAs come from this test repository.
	cmd.Dir = root
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git merge-base --is-ancestor: %v", err)
	return false
}

func TestLifecycleReviewReworkRebaseGuidance(t *testing.T) {
	changesRequested := &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested}
	approved := &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}
	tests := []struct {
		name    string
		detail  *plan.PlanDetail
		want    string
		forbid  []string
		current bool
	}{
		{
			name: "reopened work runs before another review",
			detail: &plan.PlanDetail{
				State:  plan.State{Status: plan.StatusInProgress, Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"r101-fix"}, Review: changesRequested}},
				Events: []plan.Event{{Type: plan.EventTypePlanReviewed}, {Type: plan.EventTypePlanReopened}},
			},
			want:   "Next: tao run plan-a",
			forbid: []string{"Next: tao review --run", "Next: tao rework", "Next: tao merge"},
		},
		{
			name:    "executed rework makes stale review non-current",
			detail:  &plan.PlanDetail{State: plan.State{Status: plan.StatusInReview, Plan: plan.PlanState{ID: "plan-a", Review: changesRequested}}, Events: []plan.Event{{Type: plan.EventTypePlanReviewed}, {Type: plan.EventTypePlanReopened}}},
			want:    "Next: tao review --run plan-a",
			forbid:  []string{"Next: tao rework", "Next: tao merge"},
			current: false,
		},
		{
			name:    "actionable findings rework",
			detail:  &plan.PlanDetail{State: plan.State{Status: plan.StatusChangesRequested, Plan: plan.PlanState{ID: "plan-a", Review: changesRequested}}, Events: []plan.Event{{Type: plan.EventTypePlanReviewed}}},
			want:    "Next: tao rework plan-a",
			current: true,
		},
		{
			name:    "exact current approval merges",
			detail:  &plan.PlanDetail{State: plan.State{Status: plan.StatusReviewed, Plan: plan.PlanState{ID: "plan-a", Review: approved}}, Events: []plan.Event{{Type: plan.EventTypePlanReviewed}}},
			want:    "Next: tao merge plan-a",
			current: true,
		},
		{
			name:    "merged plan has no action",
			detail:  &plan.PlanDetail{State: plan.State{Status: plan.StatusCompleted, Plan: plan.PlanState{ID: "plan-a", Review: approved}}, Events: []plan.Event{{Type: plan.EventTypePlanReviewed}, {Type: plan.EventTypePlanMerged}}},
			want:    "no further action needed",
			forbid:  []string{"Next:"},
			current: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := renderReviewGuidance(&out, test.detail); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), test.want) {
				t.Fatalf("guidance %q does not contain %q", out.String(), test.want)
			}
			for _, forbidden := range test.forbid {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("guidance %q contains stale action %q", out.String(), forbidden)
				}
			}
			if got := plan.CurrentReview(test.detail) != nil; got != test.current {
				t.Fatalf("current review = %t, want %t", got, test.current)
			}
		})
	}
}
