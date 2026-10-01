package runtimeconfig

import "testing"

func TestResolveReworkOptions(t *testing.T) {
	negative, zero, two, three, six, seven := -1, 0, 2, 3, 6, 7
	yes, no := true, false
	tests := []struct {
		name     string
		stages   []ReworkOptionsPatch
		noReview bool
		reverify bool
		want     ResolvedReworkOptions
	}{
		{name: "defaults", want: ResolvedReworkOptions{5, 4}},
		{name: "explicit zero", stages: []ReworkOptionsPatch{{MaxAttempts: &zero}}, want: ResolvedReworkOptions{0, 4}},
		{name: "environment", stages: []ReworkOptionsPatch{{MaxAttempts: &six, EscalationFromAttempt: &two}}, want: ResolvedReworkOptions{6, 2}},
		{name: "repository overrides environment", stages: []ReworkOptionsPatch{{MaxAttempts: &six, EscalationFromAttempt: &two}, {MaxAttempts: &three}}, want: ResolvedReworkOptions{3, 2}},
		{name: "invocation overrides repository", stages: []ReworkOptionsPatch{{MaxAttempts: &six, EscalationFromAttempt: &two}, {MaxAttempts: &three}, {MaxAttempts: &zero, EscalationFromAttempt: &seven}}, want: ResolvedReworkOptions{0, 7}},
		{name: "overridden values are not consumed", stages: []ReworkOptionsPatch{{MaxAttempts: &negative, EscalationFromAttempt: &zero}, {MaxAttempts: &three, EscalationFromAttempt: &two}}, want: ResolvedReworkOptions{3, 2}},
		{name: "empty stages inherit", stages: []ReworkOptionsPatch{{MaxAttempts: &three}, {}, {}}, want: ResolvedReworkOptions{3, 4}},
		{name: "legacy false", stages: []ReworkOptionsPatch{{AutoRework: &no}}, want: ResolvedReworkOptions{0, 4}},
		{name: "legacy true resets count", stages: []ReworkOptionsPatch{{MaxAttempts: &two}, {AutoRework: &yes}}, want: ResolvedReworkOptions{5, 4}},
		{name: "legacy false overrides count", stages: []ReworkOptionsPatch{{MaxAttempts: &two}, {AutoRework: &no}}, want: ResolvedReworkOptions{0, 4}},
		{name: "canonical beats false", stages: []ReworkOptionsPatch{{AutoRework: &no, MaxAttempts: &three}}, want: ResolvedReworkOptions{3, 4}},
		{name: "canonical zero beats true reverse field order", stages: []ReworkOptionsPatch{{MaxAttempts: &zero, AutoRework: &yes}}, want: ResolvedReworkOptions{0, 4}},
		{name: "later canonical beats legacy", stages: []ReworkOptionsPatch{{AutoRework: &no}, {MaxAttempts: &three}}, want: ResolvedReworkOptions{3, 4}},
		{name: "threshold above cap", stages: []ReworkOptionsPatch{{MaxAttempts: &two, EscalationFromAttempt: &seven}}, want: ResolvedReworkOptions{2, 7}},
		{name: "review disabled", noReview: true, want: ResolvedReworkOptions{0, 4}},
		{name: "reverify", reverify: true, want: ResolvedReworkOptions{0, 4}},
		{name: "normalization preserves threshold", noReview: true, reverify: true, stages: []ReworkOptionsPatch{{MaxAttempts: &six, EscalationFromAttempt: &seven}}, want: ResolvedReworkOptions{0, 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveReworkOptions(!tt.noReview, tt.reverify, tt.stages...)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveReworkOptionsRejectsInvalidConsumedValues(t *testing.T) {
	negative, zero := -1, 0
	patches := []ReworkOptionsPatch{
		{MaxAttempts: &negative},
		{EscalationFromAttempt: &zero},
		{EscalationFromAttempt: &negative},
	}
	for _, patch := range patches {
		for _, review := range []bool{false, true} {
			for _, reverify := range []bool{false, true} {
				got, err := ResolveReworkOptions(review, reverify, patch)
				if err == nil {
					t.Fatalf("expected error for %+v (review=%t, reverify=%t), got %+v", patch, review, reverify, got)
				}
			}
		}
	}
}
