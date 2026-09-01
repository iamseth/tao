package plan

import "testing"

func TestDecisionOverviewClonePreservesUnrankedAndCopiesNestedValues(t *testing.T) {
	legacy := cloneDecisionOverview(DecisionOverview{Source: DecisionOverviewSourcePlanningBrief, Problem: "Legacy goal"})
	if legacy.Priority != nil || legacy.Sequence != nil || legacy.Disposition != "" {
		t.Fatalf("legacy clone inferred rank: %+v", legacy)
	}

	source := DecisionOverview{
		SuccessCriteria: []string{"one"},
		Priority:        &Priority{Impact: PriorityLevelHigh},
		Sequence:        &Sequence{Position: 1, Total: 2, Relationships: []PlanRelation{{PlanID: "other"}}},
	}
	clone := cloneDecisionOverview(source)
	clone.SuccessCriteria[0] = "changed"
	clone.Priority.Impact = PriorityLevelLow
	clone.Sequence.Relationships[0].PlanID = "changed"
	if source.SuccessCriteria[0] != "one" || source.Priority.Impact != PriorityLevelHigh || source.Sequence.Relationships[0].PlanID != "other" {
		t.Fatalf("overview clone aliases source: source=%+v clone=%+v", source, clone)
	}
}

func TestSummarizePlansMixedStatuses(t *testing.T) {
	rollup := SummarizePlans([]PlanSummary{
		{Status: StatusPlanned},
		{Status: StatusInProgress},
		{Status: StatusCompleted, Complete: true, Reviewed: true, ReviewVerdict: "approve"},
		{Status: StatusBlocked},
		{Status: StatusInvalid},
	})

	if rollup.Total != 5 {
		t.Fatalf("Total = %d, want 5", rollup.Total)
	}
	if rollup.Statuses.Planned != 1 || rollup.Statuses.InProgress != 1 || rollup.Statuses.Completed != 1 || rollup.Statuses.Blocked != 1 {
		t.Fatalf("unexpected status counts: %#v", rollup.Statuses)
	}
	if rollup.Completed != 1 || rollup.Reviewed != 1 {
		t.Fatalf("Completed/Reviewed = %d/%d, want 1/1", rollup.Completed, rollup.Reviewed)
	}
	if got := rollup.Verdicts["approve"]; got != 1 {
		t.Fatalf("approve verdict count = %d, want 1", got)
	}
}

func TestSummarizePlansCountsVerificationFailedProjection(t *testing.T) {
	rollup := SummarizePlans([]PlanSummary{
		{Status: StatusVerificationFailed},
		{Status: StatusInReview},
	})

	if rollup.Total != 2 || rollup.Statuses.VerificationFailed != 1 || rollup.Statuses.InReview != 1 {
		t.Fatalf("unexpected verification-failed rollup: %#v", rollup)
	}
}

func TestSummarizePlansReviewedVersusUnreviewedCompleted(t *testing.T) {
	rollup := SummarizePlans([]PlanSummary{
		{Status: StatusCompleted, Complete: true, Reviewed: true, ReviewVerdict: "approve"},
		{Status: StatusCompleted, Complete: true},
		{Status: StatusCompleted, Complete: true, Reviewed: true, ReviewVerdict: "changes"},
		{Status: StatusCompleted, Complete: true, ReviewVerdict: "ignored"},
	})

	if rollup.Statuses.Completed != 4 || rollup.Completed != 4 {
		t.Fatalf("completed status/lifecycle counts = %d/%d, want 4/4", rollup.Statuses.Completed, rollup.Completed)
	}
	if rollup.Reviewed != 2 {
		t.Fatalf("Reviewed = %d, want 2", rollup.Reviewed)
	}
	if got := rollup.Verdicts["approve"]; got != 1 {
		t.Fatalf("approve verdict count = %d, want 1", got)
	}
	if got := rollup.Verdicts["changes"]; got != 1 {
		t.Fatalf("changes verdict count = %d, want 1", got)
	}
	if got := rollup.Verdicts["ignored"]; got != 0 {
		t.Fatalf("unreviewed verdict count = %d, want 0", got)
	}
}

func TestSummarizePlansEmpty(t *testing.T) {
	rollup := SummarizePlans(nil)

	if rollup.Total != 0 || rollup.Completed != 0 || rollup.Reviewed != 0 {
		t.Fatalf("unexpected empty totals: %#v", rollup)
	}
	if rollup.Statuses != (PlanStatusRollup{}) {
		t.Fatalf("unexpected empty status counts: %#v", rollup.Statuses)
	}
	if len(rollup.Verdicts) != 0 {
		t.Fatalf("unexpected empty verdict counts: %#v", rollup.Verdicts)
	}
}
