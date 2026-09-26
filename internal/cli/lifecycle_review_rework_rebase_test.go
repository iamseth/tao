package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	runpkg "github.com/iamseth/tao/internal/run"
)

func TestLifecycleReworkRunLockHandoff(t *testing.T) {
	fixture := newLifecycleHandoffFixture(t)

	// A competing lifecycle driver must remain excluded while the nested run
	// re-enters the lock and completes the generated slice on the existing branch.
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
			if err := currentRecord.StartSlice(sliceID, plan.SliceStartRequest{ExecutionRoot: fixture.worktree, Run: &plan.SliceRunStart{CommitPolicy: "slice"}, Boundary: &plan.SliceExecutionStart{Branch: fixture.branch, Head: startHead, CommitPolicy: "slice", WorkspaceStrategy: plan.WorkspaceStrategyWorktree}, StartedAt: fixture.now.Add(4 * time.Minute)}); err != nil {
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

// This fixture starts at the persisted review boundary; rebase and review
// execution belong to the lifecycle tests in internal/run.
type lifecycleHandoffFixture struct {
	plansRoot string
	planID    string
	worktree  string
	branch    string
	now       time.Time
}

func newLifecycleHandoffFixture(t *testing.T) *lifecycleHandoffFixture {
	t.Helper()
	fixture := &lifecycleHandoffFixture{
		plansRoot: t.TempDir(),
		planID:    "lifecycle-handoff",
		worktree:  filepath.Join(t.TempDir(), "worktree"),
		branch:    "tao/lifecycle-handoff",
		now:       time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	}
	repoRoot := newCLICommitRepo(t)
	base := lifecycleGitOutput(t, repoRoot, "rev-parse", "HEAD")
	runCLICommitGit(t, repoRoot, "worktree", "add", "-b", fixture.branch, fixture.worktree)
	lifecycleCommit(t, fixture.worktree, "review-change.txt", "original\n", "complete original work")
	head := lifecycleGitOutput(t, fixture.worktree, "rev-parse", "HEAD")
	writeCLIReworkPlan(t, fixture.plansRoot, fixture.planID, plan.StatusInReview, nil)
	detail := fixture.reload(t)
	detail.State.Repo = plan.Repo{Name: "lifecycle", Root: repoRoot, Branch: "main", BaseCommit: base}
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: repoRoot, Path: fixture.worktree,
		Branch: fixture.branch, BaseBranch: "main", BaseSHA: base, HeadSHA: head,
	}
	record := fixture.record(t, detail)
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/review.go", Message: "reload lifecycle state", Suggestion: "serialize review and rework"}
	review := plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Summary: "rework", FindingsCount: 1, Findings: []plan.ReviewFinding{finding}, Base: base, Head: head, ReviewedAt: fixture.now.Add(3 * time.Minute)}
	if err := record.RecordReviewCompleted(review, "pi"); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *lifecycleHandoffFixture) reload(t *testing.T) *plan.PlanDetail {
	t.Helper()
	detail, err := plan.NewFileRepository(f.plansRoot).ResolvePlan(context.Background(), f.planID)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func (f *lifecycleHandoffFixture) record(t *testing.T, detail *plan.PlanDetail) *plan.PlanRecord {
	t.Helper()
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func lifecycleCommit(t *testing.T, root string, name string, content string, message string) {
	t.Helper()
	writeCLICommitFile(t, root, name, content)
	runCLICommitGit(t, root, "add", "--", name)
	runCLICommitGit(t, root, "commit", "-m", message)
}

func lifecycleGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runCLICommitGit(t, root, args...))
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
