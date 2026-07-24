package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventFailureModeFieldsJSONLRoundTripAndOmitZero(t *testing.T) {
	planDir := t.TempDir()
	timestamp := time.Date(2026, 7, 14, 1, 30, 0, 0, time.UTC)
	event := Event{
		Type:        EventTypeReworkStopped,
		Timestamp:   timestamp,
		PlanID:      "failure-events",
		Result:      "failed",
		Round:       2,
		Attempts:    3,
		Fingerprint: "finding-set",
		Message:     "Rework stopped",
	}
	if err := AppendEvent(planDir, event); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(planDir, Event{Type: EventTypeSessionTimeout, Timestamp: timestamp, PlanID: "failure-events", Message: "Timed out"}); err != nil {
		t.Fatal(err)
	}

	events, warnings, err := readEvents(filepath.Join(planDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected read warnings: %v", warnings)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	got := events[0]
	if got.Result != event.Result || got.Round != event.Round || got.Attempts != event.Attempts || got.Fingerprint != event.Fingerprint {
		t.Fatalf("unexpected failure-mode fields after JSONL round trip: %+v", got)
	}

	data, err := os.ReadFile(filepath.Join(planDir, "events.jsonl")) //nolint:gosec // Test path is internally constructed.
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, field := range []string{`"result"`, `"round"`, `"attempts"`, `"fingerprint"`} {
		if strings.Contains(lines[1], field) {
			t.Errorf("zero-value field %s was not omitted from JSON: %s", field, lines[1])
		}
	}
}

func TestSliceRequiredInputsJSONAndLegacyCompatibility(t *testing.T) {
	slice := Slice{
		ID: "001-a",
		RequiredInputs: []RequiredInput{{
			Path:   "internal/plan",
			Kind:   RequiredInputDirectory,
			Reason: "validation package",
		}},
	}
	data, err := json.Marshal(slice)
	if err != nil {
		t.Fatal(err)
	}
	var got Slice
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.RequiredInputs) != 1 || got.RequiredInputs[0] != slice.RequiredInputs[0] {
		t.Fatalf("required inputs after round trip = %+v", got.RequiredInputs)
	}

	var legacy Slice
	if err := json.Unmarshal([]byte(`{"id":"legacy","expected_files":[],"verification":{"commands":["go test ./internal/plan"],"manual_checks":[]}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.RequiredInputs != nil {
		t.Fatalf("legacy required inputs = %+v, want nil", legacy.RequiredInputs)
	}
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyData), "required_inputs") {
		t.Fatalf("legacy JSON unexpectedly added required_inputs: %s", legacyData)
	}
}

func TestPlanReviewFindingsJSON(t *testing.T) {
	review := PlanReview{
		Verdict:       ReviewVerdictChangesRequested,
		Summary:       "Needs work.",
		FindingsCount: 1,
		CommitMessage: &ReviewCommitMessage{Subject: "feat(review): persist approved commit proposals", Body: "What:\nPersist the proposal.\n\nWhy:\nReuse reviewed context."},
		Findings: []ReviewFinding{
			{Severity: "major", File: "internal/run/review.go", Line: 42, Message: "Fix this.", Suggestion: "Adjust the code."},
		},
	}

	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	var got PlanReview
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.FindingsCount != len(got.Findings) {
		t.Fatalf("findings count = %d, findings = %+v", got.FindingsCount, got.Findings)
	}
	want := review.Findings[0]
	if len(got.Findings) != 1 || got.Findings[0] != want {
		t.Fatalf("unexpected findings after JSON round trip: %+v", got.Findings)
	}
	if got.CommitMessage == nil || *got.CommitMessage != *review.CommitMessage {
		t.Fatalf("unexpected commit message after JSON round trip: %+v", got.CommitMessage)
	}

	var older PlanReview
	if err := json.Unmarshal([]byte(`{"verdict":"approve","summary":"ready","findings_count":0}`), &older); err != nil {
		t.Fatal(err)
	}
	if len(older.Findings) != 0 {
		t.Fatalf("older review should have no findings, got %+v", older.Findings)
	}
	if older.CommitMessage != nil {
		t.Fatalf("older review should have no commit message, got %+v", older.CommitMessage)
	}
}

func TestPlanReviewIsApproved(t *testing.T) {
	tests := []struct {
		name   string
		review *PlanReview
		want   bool
	}{
		{
			name:   "approve",
			review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove},
			want:   true,
		},
		{
			name:   "changes requested",
			review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested},
		},
		{
			name:   "empty verdict",
			review: &PlanReview{Status: ReviewStatusCompleted},
		},
		{
			name:   "not completed",
			review: &PlanReview{Status: ReviewStatusError, Verdict: ReviewVerdictApprove},
		},
		{
			name: "nil review",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.review.IsApproved(); got != tt.want {
				t.Fatalf("IsApproved() = %v, want %v", got, tt.want)
			}
		})
	}
}
