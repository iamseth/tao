package runqueue

import (
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestSummarizeCountsStatusesAndReviewedSucceeded(t *testing.T) {
	reviewedPlans := map[string]bool{
		"succeeded-reviewed": true,
		"pending":            true,
		"running":            true,
		"failed":             true,
		"skipped":            true,
	}
	var reviewedCalls []string
	summary := Summarize(QueueSnapshot{Entries: []QueueEntry{
		{PlanID: "pending", Status: QueueStatusPending},
		{PlanID: "running", Status: QueueStatusRunning},
		{PlanID: "succeeded-reviewed", Status: QueueStatusSucceeded},
		{PlanID: "succeeded-unreviewed", Status: QueueStatusSucceeded},
		{PlanID: "failed", Status: QueueStatusFailed},
		{PlanID: "skipped", Status: QueueStatusSkipped},
	}}, func(planID string) bool {
		reviewedCalls = append(reviewedCalls, planID)
		return reviewedPlans[planID]
	})

	if summary.Total != 6 {
		t.Fatalf("Total = %d, want 6", summary.Total)
	}
	if summary.Statuses.Pending != 1 || summary.Statuses.Running != 1 || summary.Statuses.Succeeded != 2 || summary.Statuses.Failed != 1 || summary.Statuses.Skipped != 1 {
		t.Fatalf("unexpected status counts: %#v", summary.Statuses)
	}
	if summary.SucceededReviewed != 1 {
		t.Fatalf("SucceededReviewed = %d, want 1", summary.SucceededReviewed)
	}
	if len(reviewedCalls) != 2 || reviewedCalls[0] != "succeeded-reviewed" || reviewedCalls[1] != "succeeded-unreviewed" {
		t.Fatalf("reviewed lookup calls = %#v, want succeeded plan IDs only", reviewedCalls)
	}
}

func TestSummarizeTreatsNilReviewedFuncAsUnreviewed(t *testing.T) {
	summary := Summarize(QueueSnapshot{Entries: []QueueEntry{
		{PlanID: "succeeded", Status: QueueStatusSucceeded},
	}}, nil)

	if summary.Total != 1 || summary.Statuses.Succeeded != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if summary.SucceededReviewed != 0 {
		t.Fatalf("SucceededReviewed = %d, want 0", summary.SucceededReviewed)
	}
}

func TestReviewedPlanLookupMatchesReviewedPlanIDs(t *testing.T) {
	lookup := ReviewedPlanLookup([]plan.PlanSummary{
		{ID: "reviewed-id", Reviewed: true},
		{ID: "unreviewed-id"},
	})

	if !lookup("reviewed-id") {
		t.Fatal("reviewed plan was not found")
	}
	for _, planID := range []string{"unreviewed-id", "missing"} {
		if lookup(planID) {
			t.Fatalf("lookup(%q) = true, want false", planID)
		}
	}
}

func TestReviewedPlanLookupEmptySummariesAlwaysFalse(t *testing.T) {
	lookup := ReviewedPlanLookup(nil)
	for _, planID := range []string{"", "plan-a"} {
		if lookup(planID) {
			t.Fatalf("lookup(%q) = true, want false", planID)
		}
	}
}
