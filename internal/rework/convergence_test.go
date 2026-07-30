package rework

import (
	"fmt"
	"slices"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestRecurringReworkFilesIntersectsThreeReviewObservations(t *testing.T) {
	detail := &plan.PlanDetail{Slices: plan.SlicesFile{Slices: append(
		convergenceRound(1,
			[]string{"zeta.go", "associated-only.go"},
			[]string{"alpha.go"},
			[]string{"first-only.go"},
		),
		convergenceRound(2,
			[]string{"alpha.go"},
			[]string{"zeta.go"},
			[]string{"second-only.go"},
		)...,
	)}}
	current := []plan.ReviewFinding{
		{File: "./zeta.go", Message: "a third, distinct zeta finding"},
		{File: "alpha.go", Message: "a third, distinct alpha finding"},
		{File: "current-only.go", Message: "new finding"},
	}

	got := recurringReworkFiles(detail, 0, current)
	if want := []string{"alpha.go", "zeta.go"}; !slices.Equal(got, want) {
		t.Fatalf("recurring files = %#v, want %#v", got, want)
	}
}

func TestRecurringReworkFilesRequiresCompleteContiguousBudgetHistory(t *testing.T) {
	tests := []struct {
		name     string
		baseline int
		rounds   []plan.Slice
		current  []plan.ReviewFinding
	}{
		{
			name: "associated expected files are not observations",
			rounds: append(
				convergenceRound(1, []string{"first.go", "shared.go"}),
				convergenceRound(2, []string{"second.go", "shared.go"})...,
			),
			current: []plan.ReviewFinding{{File: "shared.go"}},
		},
		{
			name: "unsafe primary paths are incomplete evidence",
			rounds: append(
				convergenceRound(1, []string{"../shared.go"}),
				convergenceRound(2, []string{"../shared.go"})...,
			),
			current: []plan.ReviewFinding{{File: "../shared.go"}},
		},
		{
			name: "missing primary paths are incomplete evidence",
			rounds: append(
				convergenceRound(1, nil),
				convergenceRound(2, []string{"shared.go"})...,
			),
			current: []plan.ReviewFinding{{File: "shared.go"}},
		},
		{
			name: "round gaps break the review sequence",
			rounds: append(
				convergenceRound(1, []string{"shared.go"}),
				convergenceRound(3, []string{"shared.go"})...,
			),
			current: []plan.ReviewFinding{{File: "shared.go"}},
		},
		{
			name:     "rounds at or before the baseline are ignored",
			baseline: 2,
			rounds: append(
				convergenceRound(1, []string{"shared.go"}),
				convergenceRound(2, []string{"shared.go"})...,
			),
			current: []plan.ReviewFinding{{File: "shared.go"}},
		},
		{
			name: "a file absent from the middle review is not recurring",
			rounds: append(
				convergenceRound(1, []string{"shared.go"}),
				convergenceRound(2, []string{"other.go"})...,
			),
			current: []plan.ReviewFinding{{File: "shared.go"}},
		},
		{
			name: "a file absent from the current review is not recurring",
			rounds: append(
				convergenceRound(1, []string{"shared.go"}),
				convergenceRound(2, []string{"shared.go"})...,
			),
			current: []plan.ReviewFinding{{File: "other.go"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := &plan.PlanDetail{Slices: plan.SlicesFile{Slices: test.rounds}}
			if got := recurringReworkFiles(detail, test.baseline, test.current); len(got) != 0 {
				t.Fatalf("recurring files = %#v, want none", got)
			}
		})
	}
}

func TestRecurringReworkFilesUsesLatestTwoRoundsInContiguousBudget(t *testing.T) {
	rounds := append(convergenceRound(3, []string{"old.go"}), convergenceRound(4, []string{"shared.go"})...)
	rounds = append(rounds, convergenceRound(5, []string{"shared.go"})...)
	detail := &plan.PlanDetail{Slices: plan.SlicesFile{Slices: rounds}}

	got := recurringReworkFiles(detail, 2, []plan.ReviewFinding{{File: "shared.go"}})
	if want := []string{"shared.go"}; !slices.Equal(got, want) {
		t.Fatalf("recurring files = %#v, want %#v", got, want)
	}
}

func convergenceRound(round int, expectedFiles ...[]string) []plan.Slice {
	result := make([]plan.Slice, len(expectedFiles))
	for index, expected := range expectedFiles {
		result[index] = plan.Slice{
			ID:            fmt.Sprintf("r%d%02d-finding", round, index+1),
			ExpectedFiles: expected,
		}
	}
	return result
}
