package plan

import (
	"errors"
	"testing"
	"time"
)

func TestDeriveReviewedReflectsPlanReview(t *testing.T) {
	detail := reviewedCapabilityDetail()

	derived := Derive(detail, time.Time{})
	if derived.Capabilities.Reviewed {
		t.Fatalf("expected missing review to keep derived capabilities unreviewed: %+v", derived.Capabilities)
	}
	if capabilities := AnalyzeRunCapabilities(detail); capabilities.Reviewed {
		t.Fatalf("expected missing review to keep analyzed capabilities unreviewed: %+v", capabilities)
	}

	reviewedAt := time.Date(2026, 6, 28, 6, 45, 0, 0, time.UTC)
	detail.State.Plan.Review = &PlanReview{Verdict: "pass", Summary: "ready", ReviewedAt: reviewedAt}

	derived = Derive(detail, time.Time{})
	if !derived.Capabilities.Reviewed {
		t.Fatalf("expected persisted review to mark derived capabilities reviewed: %+v", derived.Capabilities)
	}
	if capabilities := AnalyzeRunCapabilities(detail); !capabilities.Reviewed {
		t.Fatalf("expected persisted review to mark analyzed capabilities reviewed: %+v", capabilities)
	}

	summary := Summarize(detail, time.Time{})
	if !summary.Reviewed || summary.ReviewVerdict != "pass" {
		t.Fatalf("expected review metadata in summary, got %+v", summary)
	}
}

func TestDeriveCompletionDoesNotRequireReview(t *testing.T) {
	detail := reviewedCapabilityDetail()

	derived := Derive(detail, time.Time{})
	if !derived.Complete || !derived.Capabilities.Complete {
		t.Fatalf("expected slice-complete plan without review to remain complete: %+v", derived)
	}
	if derived.Capabilities.Reviewed {
		t.Fatalf("expected missing review to remain informational, got %+v", derived.Capabilities)
	}

	summary := Summarize(detail, time.Time{})
	if !summary.Complete || summary.Status != StatusInReview || summary.Reviewed || summary.ReviewVerdict != "" {
		t.Fatalf("expected slice-complete unreviewed summary to be in_review, got %+v", summary)
	}
}

func TestPlanLifecycleStatusReflectsReviewAndMergeStages(t *testing.T) {
	detail := reviewedCapabilityDetail()
	if got := PlanLifecycleStatus(detail); got != StatusInReview {
		t.Fatalf("unreviewed slice-complete status = %q, want %q", got, StatusInReview)
	}

	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested}
	if got := PlanLifecycleStatus(detail); got != StatusChangesRequested {
		t.Fatalf("changes-requested status = %q, want %q", got, StatusChangesRequested)
	}

	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove}
	if got := PlanLifecycleStatus(detail); got != StatusReviewed {
		t.Fatalf("approved review status = %q, want %q", got, StatusReviewed)
	}

	detail.Events = append(detail.Events, Event{Type: EventTypePlanMerged})
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("merged status = %q, want %q", got, StatusCompleted)
	}
}

// TestPlanLifecycleStatusLegacyCompletedWithoutMergeEvent covers plans whose
// state.Status is completed but whose event log has no plan_merged event:
// pre-upgrade releases wrote completed at final slice completion, so the plan
// finished under the old semantics (and was typically merged manually or
// before merge events existed). The persisted status is trusted — demoting
// such plans would mass-revert historical completed plans to in_review on
// upgrade, with no reliable recovery once branches and head snapshots are
// gone. The current write path only stores completed through RecordMerged, so
// new plans always carry the event and never take the legacy arm.
func TestPlanLifecycleStatusLegacyCompletedWithoutMergeEvent(t *testing.T) {
	detail := reviewedCapabilityDetail()
	detail.State.Status = StatusCompleted
	detail.Events = nil
	if PlanIsMerged(detail.Events) {
		t.Fatal("fixture must have no plan_merged event")
	}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("legacy completed status = %q, want %q", got, StatusCompleted)
	}

	// A non-merge event (e.g. plan_reviewed) does not disqualify the legacy arm.
	detail.Events = []Event{{Type: EventTypePlanReviewed}}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("legacy completed status with review event = %q, want %q", got, StatusCompleted)
	}

	detail.Events = []Event{{Type: EventTypePlanMerged}}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("recorded merge status = %q, want %q", got, StatusCompleted)
	}
}

// TestPlanLifecycleStatusReopenedAfterMergeIsNotCompleted covers the case where a
// merged plan is reopened for rework: the stale plan_merged event must not keep
// projecting completed while the reopened plan has pending work.
func TestPlanLifecycleStatusReopenedAfterMergeIsNotCompleted(t *testing.T) {
	detail := reviewedCapabilityDetail()
	detail.Events = []Event{{Type: EventTypePlanMerged}}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("merged status = %q, want %q", got, StatusCompleted)
	}

	// Reopen for rework: pending slice added, status back to in_progress, and a
	// plan_reopened event appended after the merge.
	detail.State.Status = StatusInProgress
	detail.State.Plan.PendingSlices = []string{"002-b"}
	detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusPending})
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := PlanLifecycleStatus(detail); got != StatusInProgress {
		t.Fatalf("reopened-after-merge status = %q, want %q", got, StatusInProgress)
	}
	if PlanIsMerged(detail.Events) {
		t.Fatal("reopened plan must not report as merged")
	}
}

// TestPlanLifecycleStatusReworkClearsStaleReviewVerdict covers a plan reviewed
// changes_requested, reopened, then reworked to slice-complete: it must project
// in_review (awaiting a fresh verdict), not the stale changes_requested verdict.
func TestPlanLifecycleStatusReworkClearsStaleReviewVerdict(t *testing.T) {
	detail := reviewedCapabilityDetail()
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested}
	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: detail.State.Plan.Review}}
	if got := PlanLifecycleStatus(detail); got != StatusChangesRequested {
		t.Fatalf("pre-rework status = %q, want %q", got, StatusChangesRequested)
	}

	// Reopen, then complete the rework slice so the queue drains again.
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := PlanLifecycleStatus(detail); got != StatusInReview {
		t.Fatalf("reworked slice-complete status = %q, want %q (stale verdict must be ignored)", got, StatusInReview)
	}
	if AnalyzeRunCapabilities(detail).Reviewed {
		t.Fatal("reopened plan must not report a current review until re-reviewed")
	}

	// A fresh review after the reopen is honored again.
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove}
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReviewed, Review: detail.State.Plan.Review})
	if got := PlanLifecycleStatus(detail); got != StatusReviewed {
		t.Fatalf("post-rework fresh-review status = %q, want %q", got, StatusReviewed)
	}
	if !AnalyzeRunCapabilities(detail).Reviewed {
		t.Fatal("fresh review after reopen must mark the plan reviewed")
	}
}

// TestLifecycleCompleteAllowsSkippedFinalSlice guards against the regression
// where a plan that skipped its final pending slice was never Complete because
// lifecycleComplete gated on a raw pending count that treats skipped slices as
// pending. Such a plan could neither finalize nor merge — permanently stuck.
func TestLifecycleCompleteAllowsSkippedFinalSlice(t *testing.T) {
	completedAt := time.Date(2026, 6, 28, 6, 40, 0, 0, time.UTC)
	detail := &PlanDetail{
		State: State{
			// in_review (not completed) so the completion decision exercises the
			// slicesComplete path rather than the status==Completed escape hatch.
			Status: StatusInReview,
			Plan: PlanState{
				ID:              "plan",
				CompletedSlices: []string{"001-a"},
			},
		},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Status: StatusCompleted, Timing: SliceTiming{CompletedAt: &completedAt}},
			{ID: "002-b", Status: StatusSkipped},
		}},
	}
	if !AnalyzeRunCapabilities(detail).Complete {
		t.Fatal("plan whose final slice is skipped (queue drained) must be Complete")
	}
	if got := PlanLifecycleStatus(detail); got != StatusInReview {
		t.Fatalf("skipped-final-slice status = %q, want %q", got, StatusInReview)
	}
}

// TestSummarizeClearsStaleReviewVerdictAfterReopen guards the PlanSummary
// projection: after a reopen supersedes the last review, ReviewVerdict must not
// leak the stale persisted verdict (Reviewed already flips false via CurrentReview).
func TestSummarizeClearsStaleReviewVerdictAfterReopen(t *testing.T) {
	now := time.Date(2026, 6, 28, 7, 0, 0, 0, time.UTC)
	detail := reviewedCapabilityDetail()
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested}
	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: detail.State.Plan.Review}}
	if got := Summarize(detail, now).ReviewVerdict; got != ReviewVerdictChangesRequested {
		t.Fatalf("pre-reopen ReviewVerdict = %q, want %q", got, ReviewVerdictChangesRequested)
	}

	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := Summarize(detail, now).ReviewVerdict; got != "" {
		t.Fatalf("post-reopen ReviewVerdict = %q, want empty (superseded by reopen)", got)
	}
}

func TestReviewAccessorsSeparateCurrentFromPersisted(t *testing.T) {
	review := PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "ready"}
	detail := reviewedCapabilityDetail()
	SetPersistedReview(detail, review)
	persisted := PersistedReview(detail)
	if persisted == nil {
		t.Fatal("expected persisted review after SetPersistedReview")
	}

	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: persisted}}
	if got := CurrentReview(detail); got != persisted {
		t.Fatalf("CurrentReview without reopen = %p, want persisted review %p", got, persisted)
	}
	if got := PersistedReview(detail); got != persisted {
		t.Fatalf("PersistedReview without reopen = %p, want persisted review %p", got, persisted)
	}

	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := CurrentReview(detail); got != nil {
		t.Fatalf("CurrentReview after reopen = %#v, want nil", got)
	}
	if got := PersistedReview(detail); got != persisted {
		t.Fatalf("PersistedReview after reopen = %p, want persisted review %p", got, persisted)
	}

	if got := CurrentReview(nil); got != nil {
		t.Fatalf("CurrentReview(nil) = %#v, want nil", got)
	}
	if got := PersistedReview(nil); got != nil {
		t.Fatalf("PersistedReview(nil) = %#v, want nil", got)
	}
	SetPersistedReview(nil, review)
}

// TestReviewSupersededByReopenIgnoresFailedReviewEvents guards the reopen
// guard against being reset by a failed-review event: RecordReviewError copies
// its head snapshot from pre-reopen state, so letting it supersede the reopen
// would restore merge trust in stale heads while the rework is unreviewed —
// external-merge detection could then re-record the old merge and delete the
// branch holding the rework commits.
func TestReviewSupersededByReopenIgnoresFailedReviewEvents(t *testing.T) {
	failed := []Event{
		{Type: EventTypePlanMerged},
		{Type: EventTypePlanReopened},
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusError}},
	}
	if !ReviewSupersededByReopen(failed) {
		t.Fatal("failed review after reopen must not supersede the reopen")
	}

	completed := []Event{
		{Type: EventTypePlanMerged},
		{Type: EventTypePlanReopened},
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusError}},
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusCompleted}},
	}
	if ReviewSupersededByReopen(completed) {
		t.Fatal("completed review after reopen must supersede the reopen")
	}

	legacy := []Event{
		{Type: EventTypePlanMerged},
		{Type: EventTypePlanReopened},
		{Type: EventTypePlanReviewed},
	}
	if ReviewSupersededByReopen(legacy) {
		t.Fatal("legacy review event without payload keeps the historical reset")
	}
}

func TestRunCapabilitiesNeedsApprovalFromRunnableError(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Approval: &Approval{Required: true, Reason: "human sign-off"}}}},
	}
	capabilities := AnalyzeRunCapabilities(detail)
	if capabilities.CanRun {
		t.Fatalf("expected approval-gated slice to not be runnable: %+v", capabilities)
	}
	if !capabilities.NeedsApproval {
		t.Fatalf("expected NeedsApproval=true, got %+v", capabilities)
	}
	if capabilities.ApprovalSliceID != "001-a" {
		t.Fatalf("expected ApprovalSliceID=001-a, got %+v", capabilities)
	}
	if capabilities.DisabledReason != "slice 001-a requires approval: human sign-off" {
		t.Fatalf("expected DisabledReason to match legacy prose, got %q", capabilities.DisabledReason)
	}
}

func TestRunCapabilitiesNeedsApprovalFromContinueError(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusBlocked, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusBlocked, Approval: &Approval{Required: true, Reason: "team review"}}}},
	}
	capabilities := AnalyzeRunCapabilities(detail)
	if !capabilities.NeedsApproval {
		t.Fatalf("expected NeedsApproval=true from ContinueError path, got %+v", capabilities)
	}
	if capabilities.ApprovalSliceID != "001-a" {
		t.Fatalf("expected ApprovalSliceID=001-a, got %+v", capabilities)
	}
}

func TestRunCapabilitiesNoApprovalWhenRunnable(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
	}
	capabilities := AnalyzeRunCapabilities(detail)
	if capabilities.NeedsApproval || capabilities.ApprovalSliceID != "" {
		t.Fatalf("expected no approval gate for runnable plan, got %+v", capabilities)
	}
}

func TestApprovalRequiredErrorCarriesTypedFields(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Approval: &Approval{Required: true, Reason: "security audit"}}}},
	}
	lifecycle := AnalyzeLifecycle(detail)
	var approvalErr *ApprovalRequiredError
	if !errors.As(lifecycle.RunnableError, &approvalErr) {
		t.Fatalf("expected ApprovalRequiredError from RunnableError, got %T: %v", lifecycle.RunnableError, lifecycle.RunnableError)
	}
	if approvalErr.SliceID != "001-a" {
		t.Fatalf("expected SliceID=001-a, got %q", approvalErr.SliceID)
	}
	if approvalErr.Reason != "security audit" {
		t.Fatalf("expected Reason=security audit, got %q", approvalErr.Reason)
	}
	want := "slice 001-a requires approval: security audit"
	if got := approvalErr.Error(); got != want {
		t.Fatalf("expected Error()=%q, got %q", want, got)
	}
}

func reviewedCapabilityDetail() *PlanDetail {
	completedAt := time.Date(2026, 6, 28, 6, 40, 0, 0, time.UTC)
	return &PlanDetail{
		State: State{
			Status: StatusPlanned,
			Plan: PlanState{
				ID:              "plan",
				CompletedSlices: []string{"001-a"},
			},
		},
		Slices: SlicesFile{Slices: []Slice{{
			ID:     "001-a",
			Status: StatusCompleted,
			Timing: SliceTiming{CompletedAt: &completedAt},
		}}},
	}
}
