package rework

import (
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestTrippedPlanBudgetWarningSelectsOnlyValidPlanScopeWarnings(t *testing.T) {
	warnings := []plan.AgentBudgetWarning{
		{Scope: "slice", Metric: "tool_calls", Observed: 900, Threshold: 120},
		{Scope: "plan", Metric: "cost", Observed: math.Inf(1), Threshold: 20},
		{Scope: "plan", Metric: "tool_calls", Observed: 516, Threshold: 400},
	}
	want := planBudgetWarning{Metric: "tool_calls", Observed: 516, Threshold: 400}
	if got, ok := trippedPlanBudgetWarning(warnings); !ok || got != want {
		t.Fatalf("tripped warning = %#v, %t, want %#v", got, ok, want)
	}
}

func TestPlanBudgetStopReasonRoundTripsAndRejectsMalformedEvidence(t *testing.T) {
	want := planBudgetWarning{Metric: "assistant_messages", Observed: 320, Threshold: 300}
	reason := planBudgetStopReason(want)
	if got, ok := planBudgetWarningFromStopReason(reason); !ok || got != want {
		t.Fatalf("parsed warning = %#v, %t, want %#v", got, ok, want)
	}
	for _, malformed := range []string{
		planBudgetStopReasonPrefix + `not-json`,
		planBudgetStopReasonPrefix + `{"metric":"","observed":516,"threshold":400}`,
		planBudgetStopReasonPrefix + `{"metric":"tool_calls","observed":399,"threshold":400}`,
	} {
		if got, ok := planBudgetWarningFromStopReason(malformed); ok || got != (planBudgetWarning{}) {
			t.Fatalf("parsed malformed reason %q as %#v", malformed, got)
		}
	}
}

func TestAnchorReversalsInChurnRequiresTwoDistinctPositiveLineRounds(t *testing.T) {
	churn := plan.ReworkChurn{AnchorRounds: map[string][]int{
		"internal/plan/verification_repair.go:47": {1, 8},
		"first.go:0":   {2, 3},
		"second.go:19": {4},
	}}
	want := []anchorReversalRounds{{Anchor: "internal/plan/verification_repair.go:47", Rounds: []int{1, 8}}}
	if got := anchorReversalsInChurn(churn); !reflect.DeepEqual(got, want) {
		t.Fatalf("anchor reversals = %#v, want %#v", got, want)
	}
}

func TestAnchorReversalStopReasonRoundTrips(t *testing.T) {
	reason := anchorReversalStopReason([]anchorReversalRounds{{Anchor: "./internal/plan/verification_repair.go:47", Rounds: []int{8, 1}}})
	want := []anchorReversalRounds{{Anchor: "internal/plan/verification_repair.go:47", Rounds: []int{1, 8}}}
	if got, ok := anchorReversalsFromStopReason(reason); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed anchor reversals = %#v, %t, want %#v", got, ok, want)
	}
	for _, malformed := range []string{
		anchorReversalStopReasonPrefix + `not-json`,
		anchorReversalStopReasonPrefix + `[{"anchor":"a.go:0","rounds":[1,2]}]`,
		anchorReversalStopReasonPrefix + `[{"anchor":"a.go:4","rounds":[1]}]`,
	} {
		if got, ok := anchorReversalsFromStopReason(malformed); ok || got != nil {
			t.Fatalf("parsed malformed reason %q as %#v", malformed, got)
		}
	}
}

func TestRecurringFilesInChurnCaseStudies(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]int
		want  []recurringFileRounds
	}{
		{
			name: "workflow recovery stops at round seven despite interleaving",
			files: map[string][]int{
				"internal/workspace/resolve.go": {2, 4, 7},
				"internal/run/run.go":           {1, 3, 5, 6},
			},
			want: []recurringFileRounds{
				{File: "internal/run/run.go", Rounds: []int{1, 3, 5, 6}},
				{File: "internal/workspace/resolve.go", Rounds: []int{2, 4, 7}},
			},
		},
		{
			name: "verification repair stops at round four without current match",
			files: map[string][]int{
				"internal/plan/derive.go": {1, 2, 3},
				"internal/run/run.go":     {4},
			},
			want: []recurringFileRounds{{File: "internal/plan/derive.go", Rounds: []int{1, 2, 3}}},
		},
		{
			name:  "two unrelated rounds do not stop",
			files: map[string][]int{"first.go": {1}, "second.go": {2}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := recurringFilesInChurn(plan.ReworkChurn{FileRounds: test.files})
			if len(got) != len(test.want) || len(got) > 0 && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("recurring files = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestFileRecurrenceStopReasonRoundTripsAndPreservesLegacyReasons(t *testing.T) {
	recurring := []recurringFileRounds{
		{File: "z.go", Rounds: []int{7, 2, 4}},
		{File: "./a.go", Rounds: []int{3, 1, 2}},
	}
	reason := fileRecurrenceStopReason(recurring)
	got, ok := recurringFileRoundsFromStopReason(reason)
	want := []recurringFileRounds{
		{File: "a.go", Rounds: []int{1, 2, 3}},
		{File: "z.go", Rounds: []int{2, 4, 7}},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed recurrence = %#v, %t, want %#v", got, ok, want)
	}
	files, ok := recurringFilesFromStopReason(reason)
	if !ok || !slices.Equal(files, []string{"a.go", "z.go"}) {
		t.Fatalf("parsed files = %#v, %t", files, ok)
	}

	legacy := recurringFilesStopReason([]string{"z.go", "a.go"})
	files, ok = recurringFilesFromStopReason(legacy)
	if !ok || !slices.Equal(files, []string{"a.go", "z.go"}) {
		t.Fatalf("parsed legacy files = %#v, %t", files, ok)
	}
}

func TestFileRecurrenceStopReasonRejectsIncompleteEvidence(t *testing.T) {
	for _, reason := range []string{
		fileRecurrenceStopReasonPrefix + `not-json`,
		fileRecurrenceStopReasonPrefix + `[{"file":"a.go","rounds":[1,2]}]`,
		fileRecurrenceStopReasonPrefix + `[{"file":"../a.go","rounds":[1,2,3]}]`,
	} {
		if got, ok := recurringFileRoundsFromStopReason(reason); ok || got != nil {
			t.Fatalf("parsed malformed reason %q as %#v", reason, got)
		}
	}
}

func TestAnchorFindingEvidenceUsesProjectedRoundsForDurableReviewOrdering(t *testing.T) {
	const anchorFile = "internal/rework/driver.go"
	review := func(file string, line int, message string) *plan.PlanReview {
		return &plan.PlanReview{
			Status:        plan.ReviewStatusCompleted,
			Verdict:       plan.ReviewVerdictChangesRequested,
			FindingsCount: 1,
			Findings:      []plan.ReviewFinding{{File: file, Line: line, Message: message}},
		}
	}
	// Real plans emit plan_reviewed events without encoded rework slice IDs and
	// interleave them with durable rework_round events.
	events := []plan.Event{
		{Type: plan.EventTypePlanReviewed, Review: review(anchorFile, 47, "round one blocking finding")},
		{Type: plan.EventTypeReworkRound, Round: 1},
		{Type: plan.EventTypePlanReviewed, Review: review("internal/rework/other.go", 9, "unrelated finding")},
		{Type: plan.EventTypeReworkRound, Round: 2},
		{Type: plan.EventTypePlanReviewed, Review: review(anchorFile, 47, "round three blocking finding")},
	}

	churn := plan.ProjectReworkChurn(events, 0)
	anchor := anchorFile + ":47"
	if got, want := churn.AnchorRounds[anchor], []int{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("churn anchor rounds = %#v, want %#v", got, want)
	}

	evidence := anchorFindingEvidence(events, anchorReversalsInChurn(churn))
	want := []AnchorFindingEvidence{
		{Anchor: anchor, Round: 1, Message: "round one blocking finding"},
		{Anchor: anchor, Round: 3, Message: "round three blocking finding"},
	}
	if !reflect.DeepEqual(evidence, want) {
		t.Fatalf("anchor finding evidence = %#v, want %#v", evidence, want)
	}
}
