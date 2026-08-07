package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecordReviewRejectsUnsettledWorkWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*PlanRecord, PlanReview) error
	}{
		{
			name: "completed",
			apply: func(record *PlanRecord, review PlanReview) error {
				return record.RecordReviewCompleted(review, "pi")
			},
		},
		{
			name: "error",
			apply: func(record *PlanRecord, review PlanReview) error {
				return record.RecordReviewError(review, "pi")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			detail := startSliceDetail(dir)
			current := "001-a"
			detail.State.Status = StatusInProgress
			detail.State.Plan.CurrentSlice = &current
			detail.Slices.Slices[0].Status = StatusInProgress
			detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested, Summary: "keep this actionable review"}
			detail.State.Plan.MergeCommitIntent = &SingleMergeCommitIntent{PlanID: "plan-a", SourceHead: "old-head", DefaultBranch: "main", DefaultParent: "base", Message: "fix(plan): old\n\nWhat:\nKeep it.\n\nWhy:\nIt is reviewed.", CreatedAt: time.Date(2026, 8, 5, 3, 0, 0, 0, time.UTC)}
			writeStartSliceArtifacts(t, dir, detail)
			record := testRecord(dir, detail)
			beforeDetail := clonePlanDetail(detail)
			beforeState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}
			beforeSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}

			review := PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "replacement", Head: "new-head", ReviewedAt: time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC)}
			err = tt.apply(record, review)
			if err == nil || !strings.Contains(err.Error(), "001-a") || !strings.Contains(err.Error(), "tao run plan-a") {
				t.Fatalf("review record error = %v, want actionable unsettled-work refusal", err)
			}
			if !reflect.DeepEqual(detail, beforeDetail) {
				t.Fatalf("refused review changed in-memory detail:\n got: %#v\nwant: %#v", detail, beforeDetail)
			}
			afterState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}
			afterSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}
			if string(afterState) != string(beforeState) || string(afterSlices) != string(beforeSlices) {
				t.Fatal("refused review changed persisted plan artifacts")
			}
			if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("refused review created an event artifact: %v", err)
			}
		})
	}
}
