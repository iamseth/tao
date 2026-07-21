package merge

import (
	"encoding/json"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestBatchAttemptsReviewHistoryPersistsAndClearsExplicitly(t *testing.T) {
	store := newTestBatchStore(t)
	state := testBatchState()
	state.AggregateReviewSequence = 7
	state.Attempts.ReviewHistory = []BatchReviewRound{{HeadSHA: "review-head", Fingerprint: "findings-v1", FindingFiles: []string{"internal/workspace/cleanup.go"}, FindingCount: 2, AllFindingsHaveFiles: true}}
	persisted, err := store.Transition(state, "2026-07-17T14:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Attempts.ReviewHistory) != 1 || loaded.Attempts.ReviewHistory[0].HeadSHA != "review-head" || loaded.Attempts.ReviewHistory[0].Fingerprint != "findings-v1" || loaded.Attempts.ReviewHistory[0].FindingCount != 2 || !loaded.Attempts.ReviewHistory[0].AllFindingsHaveFiles {
		t.Fatalf("review history did not round-trip: %+v", loaded.Attempts.ReviewHistory)
	}

	persisted.Attempts.ReviewHistory = nil
	persisted.Attempts.ReviewFingerprint = ""
	if _, err := store.Transition(persisted, "2026-07-17T14:01:00Z"); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Attempts.ReviewHistory != nil || loaded.Attempts.ReviewFingerprint != "" {
		t.Fatalf("review convergence state did not clear: %+v", loaded.Attempts)
	}
	if loaded.AggregateReviewSequence != 7 {
		t.Fatalf("aggregate review artifact sequence did not survive convergence reset: %+v", loaded)
	}

	encoded, err := json.Marshal(loaded.Attempts)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if value, ok := raw["review_history"]; !ok || value != nil {
		t.Fatalf("review_history must be explicitly emitted as null, got %s", encoded)
	}
	if value, ok := raw["review_fingerprint"]; !ok || value != "" {
		t.Fatalf("review_fingerprint must be explicitly emitted as empty, got %s", encoded)
	}
	encodedState, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var rawState map[string]any
	if err := json.Unmarshal(encodedState, &rawState); err != nil {
		t.Fatal(err)
	}
	if value, ok := rawState["aggregate_review_sequence"]; !ok || value != float64(7) {
		t.Fatalf("aggregate_review_sequence must be explicitly emitted, got %s", encodedState)
	}
}

func TestBatchEjectionStatePersistsAndResettablePointersAreExplicit(t *testing.T) {
	store := newTestBatchStore(t)
	state := testBatchState()
	reason := "aggregate review not converging on a.go (plan plan-a)"
	state.ChosenOrder = nil
	state.Candidates[0].Deferred = &BatchDeferral{PlanID: "plan-a", Reason: reason}
	state.NonConvergence = &BatchNonConvergence{Files: []string{"a.go"}, PlanID: "plan-a", Reason: reason}
	state.Ejection = &BatchEjection{PlanID: "plan-a", Reason: reason, Status: batchEjectionPending}
	if _, err := store.Transition(state, "2026-07-17T14:00:00Z"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Ejection == nil || loaded.Ejection.PlanID != "plan-a" || loaded.NonConvergence == nil || loaded.Candidates[0].Deferred == nil {
		t.Fatalf("ejection attribution did not round-trip: %+v", loaded)
	}

	loaded.Ejection, loaded.NonConvergence = nil, nil
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ejection", "non_convergence"} {
		if value, ok := raw[key]; !ok || value != nil {
			t.Fatalf("%s must be explicitly emitted as null, got %s", key, encoded)
		}
	}
}

func TestResumeBlockedBatch(t *testing.T) {
	tests := []struct {
		name  string
		state BatchState
		want  BatchStatus
		ok    bool
	}{
		{
			name:  "recorded resolver phase",
			state: BatchState{Status: BatchStatusBlocked, BlockedReason: "agent resolution failed", ResumeStatus: BatchStatusResolving},
			want:  BatchStatusResolving,
			ok:    true,
		},
		{
			name:  "legacy verification failure",
			state: BatchState{Status: BatchStatusBlocked, BlockedReason: "aggregate verification failed", Verification: &BatchVerification{}},
			want:  BatchStatusReviewing,
			ok:    true,
		},
		{
			name:  "legacy approved landing",
			state: BatchState{Status: BatchStatusBlocked, BlockedReason: "default branch temporarily unavailable", Review: &BatchReview{Verdict: plan.ReviewVerdictApprove}},
			want:  BatchStatusReadyToLand,
			ok:    true,
		},
		{
			name: "explicit terminal classification ignores reworded reason",
			state: BatchState{
				Status: BatchStatusBlocked, BlockedReason: "operator decision needed before continuing",
				BlockKind: BatchBlockKindTerminal, ResumeStatus: BatchStatusResolving,
			},
			want: BatchStatusBlocked,
			ok:   false,
		},
		{
			name: "explicit resumable classification overrides legacy phrase",
			state: BatchState{
				Status: BatchStatusBlocked, BlockedReason: "temporary outage after cap exhausted",
				BlockKind: BatchBlockKindResumable, ResumeStatus: BatchStatusResolving,
			},
			want: BatchStatusResolving,
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResumeBlockedBatch(tt.state)
			if ok != tt.ok || got.Status != tt.want {
				t.Fatalf("ResumeBlockedBatch() = status %q, %t; want %q, %t", got.Status, ok, tt.want, tt.ok)
			}
			if ok && (got.BlockedReason != "" || got.BlockKind != "" || got.ResumeStatus != "") {
				t.Fatalf("resumed state retained block metadata: %#v", got)
			}
		})
	}
}

func TestResumeBlockedBatchLegacyTerminalReasons(t *testing.T) {
	tests := []struct {
		name   string
		reason string
	}{
		{name: "attempt cap", reason: "resolution attempt cap exhausted for plan-a"},
		{name: "equivalent findings", reason: "aggregate review stalled on equivalent findings"},
		{name: "non-convergence", reason: "aggregate review not converging on cleanup.go (plan plan-a)"},
		{name: "explicit approval", reason: "aggregate review returned comment; explicit approval required"},
		{name: "no actionable findings", reason: "aggregate review requested changes without actionable findings"},
		{name: "unsupported verdict", reason: "aggregate review returned an unsupported verdict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := BatchState{Status: BatchStatusBlocked, BlockedReason: tt.reason, ResumeStatus: BatchStatusReviewing}
			got, ok := ResumeBlockedBatch(state)
			if ok || got.Status != BatchStatusBlocked {
				t.Fatalf("legacy ResumeBlockedBatch() = status %q, %t; want blocked, false", got.Status, ok)
			}
		})
	}
}

func TestBatchStateBlockKindPersistsAndLegacyRecordRemainsValid(t *testing.T) {
	store := newTestBatchStore(t)
	legacy := testBatchState()
	legacy.Status = BatchStatusBlocked
	legacy.BlockedReason = "agent session interrupted"
	legacy.ResumeStatus = BatchStatusResolving
	persisted, err := store.Transition(legacy, "2026-07-19T00:00:00Z")
	if err != nil {
		t.Fatalf("persist legacy blocked state: %v", err)
	}
	loaded, err := store.Load(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BlockKind != "" || loaded.Status != BatchStatusBlocked || loaded.ResumeStatus != BatchStatusResolving {
		t.Fatalf("legacy blocked state changed during load: %#v", loaded)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["block_kind"]; ok {
		t.Fatalf("legacy empty block kind must be omitted, got %s", encoded)
	}

	persisted.BlockKind = BatchBlockKindTerminal
	persisted.BlockedReason = "operator decision needed"
	persisted.ResumeStatus = ""
	if _, err := store.Transition(persisted, "2026-07-19T00:01:00Z"); err != nil {
		t.Fatalf("persist typed blocked state: %v", err)
	}
	loaded, err = store.Load(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BlockKind != BatchBlockKindTerminal {
		t.Fatalf("typed block kind did not round-trip: %#v", loaded)
	}
}

func TestBlockBatchRecordsExplicitClassification(t *testing.T) {
	transient := BatchState{Status: BatchStatusReviewing}
	BlockBatch(&transient, BatchBlockKindResumable, "aggregate review failed: timeout")
	if transient.Status != BatchStatusBlocked || transient.BlockKind != BatchBlockKindResumable || transient.ResumeStatus != BatchStatusReviewing {
		t.Fatalf("transient block = %#v", transient)
	}

	terminal := BatchState{Status: BatchStatusResolving}
	BlockBatch(&terminal, BatchBlockKindTerminal, "operator decision needed")
	if terminal.Status != BatchStatusBlocked || terminal.BlockKind != BatchBlockKindTerminal || terminal.ResumeStatus != "" {
		t.Fatalf("terminal block = %#v", terminal)
	}
}
