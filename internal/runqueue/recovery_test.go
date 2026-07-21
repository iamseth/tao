package runqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
)

type recoveryRepositoryFunc func(context.Context, string) (*plan.PlanDetail, error)

func (f recoveryRepositoryFunc) ResolvePlan(ctx context.Context, planID string) (*plan.PlanDetail, error) {
	return f(ctx, planID)
}

func TestRecoveryInspectorClassifiesSliceCompleteReviewPhases(t *testing.T) {
	completedReview := func(status, verdict string) *plan.PlanReview {
		return &plan.PlanReview{Status: status, Verdict: verdict}
	}
	tests := []struct {
		name         string
		review       *plan.PlanReview
		events       []plan.Event
		wantPending  bool
		wantTerminal bool
	}{
		{name: "review not started", wantPending: true},
		{name: "review error was already attempted", review: completedReview(plan.ReviewStatusError, plan.ReviewStatusError)},
		{
			name:   "rework review error was already attempted",
			review: completedReview(plan.ReviewStatusError, plan.ReviewStatusError),
			events: []plan.Event{{Type: plan.EventTypePlanReopened}, {Type: plan.EventTypePlanReviewed, Review: completedReview(plan.ReviewStatusError, plan.ReviewStatusError)}},
		},
		{
			name:        "review error superseded by reopen",
			review:      completedReview(plan.ReviewStatusError, plan.ReviewStatusError),
			events:      []plan.Event{{Type: plan.EventTypePlanReviewed, Review: completedReview(plan.ReviewStatusError, plan.ReviewStatusError)}, {Type: plan.EventTypePlanReopened}},
			wantPending: true,
		},
		{name: "approved review is terminal", review: completedReview(plan.ReviewStatusCompleted, plan.ReviewVerdictApprove), wantTerminal: true},
		{name: "requested changes review is terminal", review: completedReview(plan.ReviewStatusCompleted, plan.ReviewVerdictChangesRequested), wantTerminal: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedRecoveryPlan(test.review, test.events)
			inspect := NewRecoveryInspector(recoveryRepositoryFunc(func(context.Context, string) (*plan.PlanDetail, error) {
				return detail, nil
			}))
			inspection, err := inspect(context.Background(), "plan-a")
			if err != nil {
				t.Fatal(err)
			}
			if !inspection.SlicesComplete || inspection.ReviewPending != test.wantPending || inspection.TerminalReview != test.wantTerminal {
				t.Fatalf("recovery inspection = %+v, want complete with pending=%v terminal=%v", inspection, test.wantPending, test.wantTerminal)
			}
		})
	}
}

func TestRecoveryInspectorRestoresSupersededReworkFingerprint(t *testing.T) {
	finding := plan.ReviewFinding{Severity: "major", File: "internal/runqueue/recovery.go", Message: "fix recovery"}
	review := &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Findings: []plan.ReviewFinding{finding}}
	detail := completedRecoveryPlan(review, []plan.Event{{Type: plan.EventTypePlanReviewed, Review: review}, {Type: plan.EventTypePlanReopened}})
	detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: "r101-fix-recovery", Status: plan.StatusPending})

	inspect := NewRecoveryInspector(recoveryRepositoryFunc(func(context.Context, string) (*plan.PlanDetail, error) { return detail, nil }))
	inspection, err := inspect(context.Background(), "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.ReworkRound != 1 || inspection.PreviousFindingFingerprint != reworkpkg.FindingsFingerprint([]plan.ReviewFinding{finding}) {
		t.Fatalf("recovery progress = %+v", inspection)
	}
}

func TestRecoveryInspectorReportsRepositoryErrors(t *testing.T) {
	wantErr := errors.New("plan store unavailable")
	inspect := NewRecoveryInspector(recoveryRepositoryFunc(func(context.Context, string) (*plan.PlanDetail, error) { return nil, wantErr }))
	if _, err := inspect(context.Background(), "plan-a"); !errors.Is(err, wantErr) {
		t.Fatalf("inspection error = %v, want %v", err, wantErr)
	}

	if _, err := NewRecoveryInspector(nil)(context.Background(), "plan-a"); err == nil {
		t.Fatal("nil repository inspection unexpectedly succeeded")
	}
}

func completedRecoveryPlan(review *plan.PlanReview, events []plan.Event) *plan.PlanDetail {
	return &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusInReview, Plan: plan.PlanState{ID: "plan-a", CompletedSlices: []string{"001-work"}, Review: review}},
		Slices: plan.SlicesFile{PlanID: "plan-a", Slices: []plan.Slice{{ID: "001-work", Status: plan.StatusCompleted}}},
		Events: events,
	}
}
