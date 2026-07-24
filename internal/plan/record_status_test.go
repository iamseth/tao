package plan

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPlanRecordReviewStatusesBeforeMerge(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	started := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	completed := started.Add(time.Minute)
	record := testRecord(dir, detail)
	if err := record.StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	if err := record.CompleteSlice("001-a", "done", nil, completed); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusInReview {
		t.Fatalf("final slice should move plan to in_review, got %q", detail.State.Status)
	}

	reviewed := completed.Add(time.Minute)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested, Summary: "fix", FindingsCount: 1, Findings: []ReviewFinding{{Message: "fix"}}, ReviewedAt: reviewed}, "pi"); err != nil {
		t.Fatal(err)
	}
	state := readStateFile(t, dir)
	if state.Status != StatusChangesRequested {
		t.Fatalf("changes-requested review should set status %q, got %q", StatusChangesRequested, state.Status)
	}

	approvedAt := reviewed.Add(time.Minute)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "ok", Findings: []ReviewFinding{}, ReviewedAt: approvedAt}, "pi"); err != nil {
		t.Fatal(err)
	}
	state = readStateFile(t, dir)
	if state.Status != StatusReviewed {
		t.Fatalf("approved review should set status %q before merge, got %q", StatusReviewed, state.Status)
	}
}

func TestPlanRecordClearsCommitMessageFromNonApprovedReplacement(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	now := time.Date(2026, 7, 23, 19, 30, 0, 0, time.UTC)
	record := testRecord(dir, detail)
	if err := record.StartSlice("001-a", now); err != nil {
		t.Fatal(err)
	}
	if err := record.CompleteSlice("001-a", "done", nil, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	proposal := &ReviewCommitMessage{Subject: "feat(review): persist approved commit proposals", Body: "What:\nPersist the proposal.\n\nWhy:\nReuse reviewed context."}
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "ready", Findings: []ReviewFinding{}, CommitMessage: proposal, Base: "base123", Head: "head123", ReviewedAt: now.Add(2 * time.Minute)}, "pi"); err != nil {
		t.Fatal(err)
	}
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment, Summary: "note", Findings: []ReviewFinding{}, CommitMessage: proposal, Base: "base123", Head: "head456", ReviewedAt: now.Add(3 * time.Minute)}, "pi"); err != nil {
		t.Fatal(err)
	}
	state := readStateFile(t, dir)
	if state.Plan.Review == nil || state.Plan.Review.CommitMessage != nil {
		t.Fatalf("non-approved replacement retained commit message: %+v", state.Plan.Review)
	}
	events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected event warnings: %v", warnings)
	}
	var approved, replacement *Event
	for i := range events {
		if events[i].Type != EventTypePlanReviewed || events[i].Review == nil {
			continue
		}
		switch events[i].Review.Summary {
		case "ready":
			approved = &events[i]
		case "note":
			replacement = &events[i]
		}
	}
	if approved == nil || approved.Review.CommitMessage == nil || approved.Review.Base != "base123" || approved.Review.Head != "head123" {
		t.Fatalf("approved review event lost bound commit message: %+v", approved)
	}
	if replacement == nil || replacement.Review.CommitMessage != nil {
		t.Fatalf("replacement review event retained commit message: %+v", replacement)
	}
}

// TestTornFinalSliceWriteStillSettles guards the recovery invariant for a torn
// final-slice completion: state.json is written before slices.json, so a crash
// between the two persists an in_review status with a drained queue while the
// slice stays in_progress in slices.json. state.json owns the queue, so the
// plan must still derive Complete and accept a review stamp — otherwise the
// review refuses to stamp reviewed, merge refuses approval, rework refuses to
// reopen a non-reviewed status, and run sees no pending slices: a permanent
// wedge with no re-establishment path.
func TestTornFinalSliceWriteStillSettles(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	detail.State.Status = StatusInReview
	detail.State.Plan.PendingSlices = nil
	detail.State.Plan.CurrentSlice = nil
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices[0].Status = StatusInProgress // slices.json never flipped

	lifecycle := AnalyzeLifecycle(detail)
	if !lifecycle.Complete {
		t.Fatal("torn final-slice write must still derive Complete from the persisted post-slice status")
	}
	if lifecycle.Active {
		t.Fatal("settled plan must not read as active despite the torn in_progress slice")
	}

	record := testRecord(dir, detail)
	reviewed := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "ok", Findings: []ReviewFinding{}, ReviewedAt: reviewed}, "pi"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusReviewed {
		t.Fatalf("approved review must stamp %q on the torn plan, got %q", StatusReviewed, detail.State.Status)
	}
	if got := PlanLifecycleStatus(detail); got != StatusReviewed {
		t.Fatalf("status projection = %q, want %q", got, StatusReviewed)
	}
}

func TestPlanRecordMergedStatus(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	started := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	completed := started.Add(time.Minute)
	reviewed := completed.Add(time.Minute)
	merged := reviewed.Add(time.Minute)
	record := testRecord(dir, detail)
	if err := record.StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	if err := record.CompleteSlice("001-a", "done", nil, completed); err != nil {
		t.Fatal(err)
	}
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "ok", Findings: []ReviewFinding{}, ReviewedAt: reviewed}, "pi"); err != nil {
		t.Fatal(err)
	}
	if err := record.RecordMerged("tao/plan-a", "abc123", merged); err != nil {
		t.Fatal(err)
	}
	state := readStateFile(t, dir)
	if state.Status != StatusCompleted {
		t.Fatalf("merge should set final status %q, got %q", StatusCompleted, state.Status)
	}
	if len(detail.Events) == 0 || detail.Events[len(detail.Events)-1].Type != EventTypePlanMerged {
		t.Fatalf("expected plan_merged event, got %#v", detail.Events)
	}
}

// TestRecordMergedPreservesSliceCompletionTime guards duration accuracy:
// CompletedAt is stamped when the final slice completes; a merge days later
// must not overwrite it, or elapsed times inflate by the review/merge gap.
// Legacy plans without the stamp record the merge instant instead.
func TestPlanRecordMergedIsIdempotentForExactEvidence(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	record := testRecord(dir, detail)
	merged := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	if err := record.RecordMerged("tao/plan-a", "squash123", merged); err != nil {
		t.Fatal(err)
	}
	if err := record.RecordMerged("tao/plan-a", "squash123", merged.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(detail.Events) != 1 {
		t.Fatalf("exact retry appended duplicate events: %#v", detail.Events)
	}
	if err := record.RecordMerged("tao/plan-a", "other", merged); err == nil {
		t.Fatal("different evidence must not overwrite an existing merge record")
	}
}

func TestRecordMergedPreservesSliceCompletionTime(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	record := testRecord(dir, detail)
	started := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	completed := started.Add(30 * time.Minute)
	merged := completed.Add(96 * time.Hour)
	if err := record.StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	if err := record.CompleteSlice("001-a", "done", nil, completed); err != nil {
		t.Fatal(err)
	}
	if got := detail.State.Plan.Timing.CompletedAt; got == nil || !got.Equal(completed) {
		t.Fatalf("fixture expects slice completion to stamp CompletedAt, got %v", got)
	}
	if err := record.RecordMerged("tao/plan-a", "abc123", merged); err != nil {
		t.Fatal(err)
	}
	if got := detail.State.Plan.Timing.CompletedAt; got == nil || !got.Equal(completed) {
		t.Fatalf("CompletedAt = %v, want slice completion %v preserved across merge", got, completed)
	}
	if got := detail.State.Plan.Timing.LastActivityAt; got == nil || !got.Equal(merged) {
		t.Fatalf("LastActivityAt = %v, want merge instant %v", got, merged)
	}

	// A legacy plan without the slice-completion stamp records the merge instant.
	legacyDir := t.TempDir()
	legacy := startSliceDetail(legacyDir)
	legacy.State.Plan.Timing.CompletedAt = nil
	legacyRecord := testRecord(legacyDir, legacy)
	if err := legacyRecord.RecordMerged("tao/plan-a", "abc123", merged); err != nil {
		t.Fatal(err)
	}
	if got := legacy.State.Plan.Timing.CompletedAt; got == nil || !got.Equal(merged) {
		t.Fatalf("legacy CompletedAt = %v, want merge instant %v", got, merged)
	}
}
