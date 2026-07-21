package rework

import "testing"

func TestBudgetAttemptsAtRound(t *testing.T) {
	tests := []struct {
		name   string
		budget Budget
		round  int
		want   int
	}{
		{name: "fresh plan", budget: Budget{}, round: 0, want: 0},
		{name: "fresh first round", budget: Budget{}, round: 1, want: 1},
		{name: "mid-round recovery", budget: Budget{BaselineRound: 4}, round: 5, want: 1},
		{name: "persisted progress never decreases", budget: Budget{BaselineRound: 4, Attempts: 2}, round: 5, want: 2},
		{name: "round before baseline", budget: Budget{BaselineRound: 4}, round: 3, want: 0},
		{name: "fingerprint stall inputs", budget: Budget{Attempts: 1, PreviousFindingFingerprint: "finding-1"}, round: 1, want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.budget.AttemptsAtRound(test.round); got != test.want {
				t.Fatalf("AttemptsAtRound(%d) = %d, want %d", test.round, got, test.want)
			}
		})
	}
}

func TestBudgetRecover(t *testing.T) {
	tests := []struct {
		name             string
		budget           Budget
		round            int
		baselineRecorded bool
		fingerprint      string
		want             Budget
	}{
		{
			name:   "legacy interrupted round",
			budget: Budget{}, round: 5, fingerprint: "finding-1",
			want: Budget{BaselineRound: 4, Attempts: 1, PreviousFindingFingerprint: "finding-1"},
		},
		{
			name:   "explicit baseline",
			budget: Budget{BaselineRound: 3}, round: 5, baselineRecorded: true, fingerprint: "finding-2",
			want: Budget{BaselineRound: 3, Attempts: 2, PreviousFindingFingerprint: "finding-2"},
		},
		{
			name:   "stale inspection preserves progress",
			budget: Budget{BaselineRound: 3, Attempts: 2, PreviousFindingFingerprint: "finding-old"}, round: 4, baselineRecorded: true, fingerprint: "finding-stale",
			want: Budget{BaselineRound: 3, Attempts: 2, PreviousFindingFingerprint: "finding-old"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.budget.Recover(test.round, test.baselineRecorded, test.fingerprint); got != test.want {
				t.Fatalf("Recover() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestBudgetLegacySnapshotBaseline(t *testing.T) {
	tests := []struct {
		name   string
		budget Budget
		round  int
		want   int
	}{
		{name: "fresh snapshot after manual rounds", budget: Budget{}, round: 4, want: 4},
		{name: "durable attempts reconstruct baseline", budget: Budget{Attempts: 1}, round: 5, want: 4},
		{name: "created round before progress persistence", budget: Budget{PreviousFindingFingerprint: "finding-1"}, round: 1, want: 0},
		{name: "inference clamps below zero", budget: Budget{Attempts: 2}, round: 1, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.budget.LegacySnapshotBaseline(test.round); got != test.want {
				t.Fatalf("LegacySnapshotBaseline(%d) = %d, want %d", test.round, got, test.want)
			}
		})
	}
}
