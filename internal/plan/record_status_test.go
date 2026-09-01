package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestPlanRecordPullRequestCompletionEvidenceOrders(t *testing.T) {
	tests := []struct {
		name        string
		reviewFirst bool
	}{
		{name: "review then pull request", reviewFirst: true},
		{name: "pull request then review"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			seed := startSliceDetail(dir)
			started := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
			completed := started.Add(time.Minute)
			seedRecord := testRecord(dir, seed)
			if err := seedRecord.StartSlice("001-a", started); err != nil {
				t.Fatal(err)
			}
			if err := seedRecord.CompleteSlice("001-a", "done", nil, completed); err != nil {
				t.Fatal(err)
			}

			// Bind both records before writing the first fact. The second writer
			// must refresh under the mutation lock and complete from the latest
			// evidence rather than its stale in-memory detail.
			firstDetail := clonePlanDetail(seed)
			secondDetail := clonePlanDetail(seed)
			firstRecord := testRecord(dir, firstDetail)
			secondRecord := testRecord(dir, secondDetail)
			firstAt := completed.Add(time.Minute)
			secondAt := firstAt.Add(time.Minute)
			firstStatus := StatusInReview
			secondEventType := EventTypePlanReviewed

			if tt.reviewFirst {
				if err := firstRecord.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123", ReviewedAt: firstAt}, "pi"); err != nil {
					t.Fatal(err)
				}
				firstStatus = StatusReviewed
				if err := secondRecord.RecordPullRequest(PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: secondAt}, "tao/plan-a", "head123"); err != nil {
					t.Fatal(err)
				}
				if err := secondRecord.RecordPullRequest(PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: secondAt}, "tao/plan-a", "head123"); err != nil {
					t.Fatalf("idempotent pull request retry: %v", err)
				}
				secondEventType = EventTypePullRequestCreated
			} else {
				if err := firstRecord.RecordPullRequest(PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: firstAt}, "tao/plan-a", "head123"); err != nil {
					t.Fatal(err)
				}
				if err := secondRecord.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123", ReviewedAt: secondAt}, "pi"); err != nil {
					t.Fatal(err)
				}
				if err := secondRecord.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123", ReviewedAt: secondAt}, "pi"); err != nil {
					t.Fatalf("idempotent review retry: %v", err)
				}
			}

			if firstDetail.State.Status != firstStatus {
				t.Fatalf("first evidence status = %q, want %q", firstDetail.State.Status, firstStatus)
			}
			if secondDetail.State.Status != StatusCompleted || !PlanIsPullRequestComplete(secondDetail) {
				t.Fatalf("second matching evidence did not complete plan: status=%q detail=%#v", secondDetail.State.Status, secondDetail.State.Plan)
			}
			if PlanIsMerged(secondDetail.Events) {
				t.Fatal("PR completion must not append plan_merged evidence")
			}
			state := readStateFile(t, dir)
			if state.Status != StatusCompleted {
				t.Fatalf("persisted status = %q, want %q", state.Status, StatusCompleted)
			}
			ownedEvents := 0
			for _, event := range secondDetail.Events {
				if event.Type == secondEventType {
					ownedEvents++
				}
			}
			if ownedEvents != 1 {
				t.Fatalf("idempotent second evidence wrote %d %s events, want 1", ownedEvents, secondEventType)
			}
		})
	}
}

func TestPlanRecordPullRequestCompletionClearedByReplacementReview(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*PlanRecord, time.Time) error
		want  string
	}{
		{
			name: "review error",
			apply: func(record *PlanRecord, at time.Time) error {
				return record.RecordReviewError(PlanReview{Status: ReviewStatusError, Verdict: ReviewVerdictApprove, Head: "head123", ReviewedAt: at}, "pi")
			},
			want: StatusInReview,
		},
		{
			name: "changes requested",
			apply: func(record *PlanRecord, at time.Time) error {
				return record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested, Head: "head123", ReviewedAt: at}, "pi")
			},
			want: StatusChangesRequested,
		},
		{
			name: "comment",
			apply: func(record *PlanRecord, at time.Time) error {
				return record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment, Head: "head123", ReviewedAt: at}, "pi")
			},
			want: StatusReviewed,
		},
		{
			name: "approved mismatched head",
			apply: func(record *PlanRecord, at time.Time) error {
				return record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head456", ReviewedAt: at}, "pi")
			},
			want: StatusReviewed,
		},
		{
			name: "approved missing head",
			apply: func(record *PlanRecord, at time.Time) error {
				return record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, ReviewedAt: at}, "pi")
			},
			want: StatusReviewed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, detail, record, nextAt := newPullRequestCompletedRecord(t)
			if err := tt.apply(record, nextAt); err != nil {
				t.Fatal(err)
			}
			if detail.State.Status != tt.want {
				t.Fatalf("replacement review status = %q, want %q", detail.State.Status, tt.want)
			}
			if PlanIsPullRequestComplete(detail) {
				t.Fatal("replacement review retained stale PR completion")
			}
			if got := PlanLifecycleStatus(detail); got != tt.want {
				t.Fatalf("replacement review projection = %q, want %q", got, tt.want)
			}
			if state := readStateFile(t, dir); state.Status != tt.want {
				t.Fatalf("persisted replacement status = %q, want %q", state.Status, tt.want)
			}
		})
	}
}

func TestPlanRecordPullRequestRefreshRecomputesCompletion(t *testing.T) {
	dir, detail, record, nextAt := newPullRequestCompletedRecord(t)
	if err := record.RecordPullRequest(PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: nextAt}, "tao/plan-a", "head456"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusReviewed || PlanIsPullRequestComplete(detail) {
		t.Fatalf("mismatched refreshed PR retained completion: status=%q PR=%#v", detail.State.Status, detail.State.Plan.PullRequest)
	}

	matchingAt := nextAt.Add(time.Minute)
	if err := record.RecordPullRequest(PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: matchingAt}, "tao/plan-a", "head123"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusCompleted || !PlanIsPullRequestComplete(detail) {
		t.Fatalf("matching refreshed PR did not restore completion: status=%q PR=%#v", detail.State.Status, detail.State.Plan.PullRequest)
	}
	if state := readStateFile(t, dir); state.Status != StatusCompleted || state.Plan.PullRequest == nil || state.Plan.PullRequest.HeadSHA != "head123" {
		t.Fatalf("unexpected persisted refreshed PR completion: %#v", state)
	}
}

func newPullRequestCompletedRecord(t *testing.T) (string, *PlanDetail, *PlanRecord, time.Time) {
	t.Helper()
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	record := testRecord(dir, detail)
	started := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
	completed := started.Add(time.Minute)
	if err := record.StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	if err := record.CompleteSlice("001-a", "done", nil, completed); err != nil {
		t.Fatal(err)
	}
	reviewed := completed.Add(time.Minute)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123", ReviewedAt: reviewed}, "pi"); err != nil {
		t.Fatal(err)
	}
	pullRequestAt := reviewed.Add(time.Minute)
	if err := record.RecordPullRequest(PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: pullRequestAt}, "tao/plan-a", "head123"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusCompleted || !PlanIsPullRequestComplete(detail) {
		t.Fatalf("fixture did not reach PR completion: status=%q", detail.State.Status)
	}
	return dir, detail, record, pullRequestAt.Add(time.Minute)
}

func TestPlanRecordReviewWritersReplaceKnownFieldsAndPreserveUnknownFields(t *testing.T) {
	tests := []struct {
		name        string
		replacement PlanReview
		apply       func(*PlanRecord, PlanReview) error
	}{
		{
			name:        "completed",
			replacement: PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment, ReviewedAt: time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)},
			apply:       func(record *PlanRecord, review PlanReview) error { return record.RecordReviewCompleted(review, "pi") },
		},
		{
			name:        "error",
			replacement: PlanReview{Status: ReviewStatusError, ReviewedAt: time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)},
			apply:       func(record *PlanRecord, review PlanReview) error { return record.RecordReviewError(review, "pi") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			detail := startSliceDetail(dir)
			started := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
			record := testRecord(dir, detail)
			if err := record.StartSlice("001-a", started); err != nil {
				t.Fatal(err)
			}
			if err := record.CompleteSlice("001-a", "done", nil, started.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			approval := PlanReview{
				Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "old summary", FindingsCount: 1,
				Findings:      []ReviewFinding{{Message: "old finding"}},
				CommitMessage: &ReviewCommitMessage{Subject: "fix(plan): old", Body: "old body"},
				Base:          "old-base", Head: "old-head", Agent: "old-agent", ReviewedAt: started.Add(2 * time.Minute),
			}
			if err := record.RecordReviewCompleted(approval, "pi"); err != nil {
				t.Fatal(err)
			}

			var raw map[string]any
			readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
			planObject := raw["plan"].(map[string]any)
			planObject["unknown_plan"] = "keep"
			planObject["review"].(map[string]any)["unknown_review"] = "keep"
			payload, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state.json"), payload, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := tt.apply(record, tt.replacement); err != nil {
				t.Fatal(err)
			}
			readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
			planObject = raw["plan"].(map[string]any)
			review := planObject["review"].(map[string]any)
			if planObject["unknown_plan"] != "keep" || review["unknown_review"] != "keep" {
				t.Fatalf("review writer erased unknown fields: %#v", planObject)
			}
			if review["summary"] != "" || review["findings_count"] != float64(0) || review["base"] != "" || review["head"] != "" || review["agent"] != "" {
				t.Fatalf("review writer retained stale known fields: %#v", review)
			}
			if findings, ok := review["findings"].([]any); !ok || len(findings) != 0 {
				t.Fatalf("review writer did not persist findings: []: %#v", review)
			}
			if message, exists := review["commit_message"]; !exists || message != nil {
				t.Fatalf("review writer did not persist commit_message: null: %#v", review)
			}
		})
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

func TestRecordReviewProposalCorrectionRejectsChangedConsumedMarker(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	record := testRecord(dir, detail)
	started := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if err := record.StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	if err := record.CompleteSlice("001-a", "done", nil, started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	wrong := &ReviewCommitMessage{Subject: "feat(pr): propose wrong type", Body: "What:\nPropose a message.\n\nWhy:\nExercise correction."}
	approval := PlanReview{
		Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "approved", Findings: []ReviewFinding{},
		Base: "base123", Head: "head123", CommitMessage: wrong, ReviewedAt: started.Add(2 * time.Minute),
	}
	if err := record.RecordReviewCompleted(approval, "pi"); err != nil {
		t.Fatal(err)
	}
	attempt := FinalizationFailure{
		Phase: FinalizationFailurePhaseProposalRepair, Category: "proposal_correction_started",
		ReviewBase: approval.Base, ReviewHead: approval.Head, FailedAt: started.Add(3 * time.Minute), RecoveryAction: FinalizationRecoveryRerunReview,
	}
	if err := record.ConsumeReviewProposalCorrection(nil, attempt); err != nil {
		t.Fatal(err)
	}
	replacement := attempt
	replacement.Category = "proposal_invalid"
	replacement.FailedAt = attempt.FailedAt.Add(time.Minute)
	if err := record.ReplaceFinalizationFailure(attempt, replacement); err != nil {
		t.Fatal(err)
	}
	corrected := approval
	corrected.CommitMessage = &ReviewCommitMessage{Subject: "fix(pr): propose correct type", Body: wrong.Body}
	if err := record.RecordReviewProposalCorrection(attempt, corrected, "pi"); err == nil || !strings.Contains(err.Error(), "marker changed") {
		t.Fatalf("stale correction error = %v", err)
	}
	if got := detail.State.Plan.Review; got == nil || got.CommitMessage == nil || got.CommitMessage.Subject != wrong.Subject {
		t.Fatalf("stale correction replaced review: %#v", got)
	}
	if got := detail.State.Plan.FinalizationFailure; got == nil || *got != replacement {
		t.Fatalf("stale correction cleared replacement marker: %#v", got)
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
	if !PlanIsMerged(detail.Events) {
		t.Fatal("RecordMerged must retain exclusive actual-merge evidence")
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
