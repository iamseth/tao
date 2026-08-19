package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

// blockedApproveRepo builds a plantest.Repository with a single blocked plan
// that has slice sliceID gated by approval.
func blockedApproveRepo(planID, sliceID, planStatus, sliceStatus string, approved bool) *plantest.Repository {
	detail := plantest.NewPlanDetail(planID).
		WithStatus(planStatus).
		WithCurrentSlice(sliceID).
		WithPendingSlices(sliceID).
		WithRepoRoot("/repo").
		AddSlice(plantest.NewSlice(sliceID).
			WithStatus(sliceStatus).
			WithVerificationCommands("go test ./internal/cli").
			WithApproval(plantest.Approval(true, "human approval", approved)).
			Build()).
		Build()
	repo := plantest.NewRepository()
	repo.AddDetail(detail)
	return repo
}

func TestApproveCommandApprovesCurrentGatedSlice(t *testing.T) {
	const planID = "20260430-1200-run-plan"
	const sliceID = "001-a"
	repo := blockedApproveRepo(planID, sliceID, plan.StatusBlocked, plan.StatusBlocked, false)

	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(_ string) Repository { return repo }}
	if err := app.Run(context.Background(), []string{"approve", "--by", "Seth", planID, "--slice", sliceID}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Slice approved: " + sliceID,
		"Next: tao run --continue " + planID,
		"Reason: the recorded blocker must be resolved before explicitly continuing",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected approve output %q, got %q", want, out.String())
		}
	}

	updated, err := repo.GetPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	sl := findSlice(updated, sliceID)
	if sl == nil || sl.Approval == nil || !sl.Approval.Approved {
		t.Fatalf("expected slice to be approved, got %+v", sl)
	}
	if sl.Approval.ApprovedBy == nil || *sl.Approval.ApprovedBy != "Seth" {
		t.Fatalf("expected approved_by Seth, got %v", sl.Approval.ApprovedBy)
	}
	if sl.Approval.ApprovedAt == nil || *sl.Approval.ApprovedAt == "" {
		t.Fatalf("expected non-empty approved_at")
	}
	foundEvent := false
	for _, e := range updated.Events {
		if e.Type == plan.EventTypeSliceApproved {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("expected slice_approved event, got %v", updated.Events)
	}

	// Confirm the approved blocked slice is now continuable.
	if err := plan.MarkBlockedContinued(updated, time.Now().UTC()); err != nil {
		t.Fatalf("expected approved blocked slice to be continuable, got %v", err)
	}
}

func TestApproveCommandStampsInjectedClock(t *testing.T) {
	const planID = "20260430-1200-run-plan"
	const sliceID = "001-a"
	repo := blockedApproveRepo(planID, sliceID, plan.StatusBlocked, plan.StatusBlocked, false)

	fixed := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Now: func() time.Time { return fixed }, Repository: func(_ string) Repository { return repo }}
	if err := app.Run(context.Background(), []string{"approve", "--by", "Seth", planID, "--slice", sliceID}); err != nil {
		t.Fatal(err)
	}

	updated, err := repo.GetPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	sl := findSlice(updated, sliceID)
	if sl == nil || sl.Approval == nil || sl.Approval.ApprovedAt == nil {
		t.Fatalf("expected approval with timestamp, got %+v", sl)
	}
	if want := "2031-02-03T04:05:06Z"; *sl.Approval.ApprovedAt != want {
		t.Fatalf("expected injected clock %q, got %q", want, *sl.Approval.ApprovedAt)
	}
}

func TestApproveCommandRejectsNonGatedSlice(t *testing.T) {
	const planID = "20260430-1200-run-plan"
	const sliceID = "001-a"
	detail := plantest.NewPlanDetail(planID).
		WithStatus(plan.StatusPlanned).
		WithPendingSlices(sliceID).
		AddSlice(plantest.NewSlice(sliceID).WithStatus(plan.StatusPending).Build()).
		Build()
	repo := plantest.NewRepository()
	repo.AddDetail(detail)

	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(_ string) Repository { return repo }}
	err := app.Run(context.Background(), []string{"approve", "--by", "Seth", planID})
	if err == nil || !strings.Contains(err.Error(), "does not require approval") {
		t.Fatalf("expected non-gated rejection, got %v", err)
	}
}
