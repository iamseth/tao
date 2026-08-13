package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecordPRFeedbackTriageWritesStateAndEvent(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	triagedAt := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	result := PRFeedbackTriageResult{
		"PRRT_change":   {Kind: "change", Rationale: "Requests a concrete correction."},
		"PRRT_question": {Kind: "question", Rationale: "Asks about the chosen behavior."},
	}

	if err := record.RecordPRFeedbackTriage(result, triagedAt); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackTriage, result) {
		t.Fatalf("persisted triage = %#v, want %#v", state.Plan.PRFeedbackTriage, result)
	}
	if !state.UpdatedAt.Equal(triagedAt) || state.Plan.Timing.LastActivityAt == nil || !state.Plan.Timing.LastActivityAt.Equal(triagedAt) {
		t.Fatalf("triage activity timestamps = updated %v, last activity %v", state.UpdatedAt, state.Plan.Timing.LastActivityAt)
	}
	events := readRecordTestEvents(t, dir)
	event := requireRecordTestTriageEvents(t, events, 1)[0]
	if event.MutationID == "" || !event.Timestamp.Equal(triagedAt) || !reflect.DeepEqual(event.PRFeedbackTriage, result) {
		t.Fatalf("triage event = %#v", event)
	}
}

func TestRecordPRFeedbackTriageIsIdempotentForUnchangedThreadSet(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	firstAt := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	first := PRFeedbackTriageResult{"PRRT_1": {Kind: "change", Rationale: "Keep the binding."}}
	if err := record.RecordPRFeedbackTriage(first, firstAt); err != nil {
		t.Fatal(err)
	}

	reclassified := PRFeedbackTriageResult{"PRRT_1": {Kind: "question", Rationale: "A later agent answered differently."}}
	if err := record.RecordPRFeedbackTriage(reclassified, firstAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackTriage, first) {
		t.Fatalf("idempotent triage = %#v, want original %#v", state.Plan.PRFeedbackTriage, first)
	}
	if !state.UpdatedAt.Equal(firstAt) {
		t.Fatalf("idempotent repeat updated timestamp to %v, want %v", state.UpdatedAt, firstAt)
	}
	requireRecordTestTriageEvents(t, readRecordTestEvents(t, dir), 1)
}

func TestRecordPRFeedbackTriageSupersedesChangedThreadSet(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	firstAt := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	if err := record.RecordPRFeedbackTriage(PRFeedbackTriageResult{
		"PRRT_1": {Kind: "change", Rationale: "First request."},
	}, firstAt); err != nil {
		t.Fatal(err)
	}
	secondAt := firstAt.Add(time.Hour)
	superseding := PRFeedbackTriageResult{
		"PRRT_1": {Kind: "change", Rationale: "First request."},
		"PRRT_2": {Kind: "scope", Rationale: "New unrelated request."},
	}
	if err := record.RecordPRFeedbackTriage(superseding, secondAt); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackTriage, superseding) {
		t.Fatalf("superseding triage = %#v, want %#v", state.Plan.PRFeedbackTriage, superseding)
	}
	events := requireRecordTestTriageEvents(t, readRecordTestEvents(t, dir), 2)
	latest := events[len(events)-1]
	if !latest.Timestamp.Equal(secondAt) || !reflect.DeepEqual(latest.PRFeedbackTriage, superseding) {
		t.Fatalf("superseding triage event = %#v", latest)
	}
}

func TestReopenFromPullRequestConsumesThreadAcrossCompletedPRCycle(t *testing.T) {
	dir := t.TempDir()
	detail := completedReopenDetail()
	detail.State.Plan.PullRequest = &PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17", CreatedAt: detail.State.UpdatedAt, Branch: "feature", HeadSHA: "old-head"}
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "old-head", ReviewedAt: detail.State.UpdatedAt}
	detail.State.Plan.PRFeedbackTriage = PRFeedbackTriageResult{
		"PRRT_change":   {Kind: "change", Rationale: "Requests a lifecycle fix."},
		"PRRT_question": {Kind: "question", Rationale: "Asks why this is needed."},
	}
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	reopenedAt := time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC)
	newSlices := []Slice{newReopenSlice("002-pr-fix", "Fix pull request feedback", reopenedAt)}

	if err := record.ReopenFromPullRequest(newSlices, []string{"PRRT_change"}, reopenedAt); err != nil {
		t.Fatal(err)
	}
	// Retrying the same atomic transaction is a no-op rather than a duplicate
	// consumption or reopen.
	if err := record.ReopenFromPullRequest(newSlices, []string{"PRRT_change"}, reopenedAt); err != nil {
		t.Fatalf("retry atomic reopen: %v", err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackConsumedThreadIDs, []string{"PRRT_change"}) {
		t.Fatalf("consumed thread IDs = %#v, want PRRT_change", state.Plan.PRFeedbackConsumedThreadIDs)
	}
	if state.Status != StatusInProgress || !reflect.DeepEqual(state.Plan.PendingSlices, []string{"002-pr-fix"}) {
		t.Fatalf("reopened state = status %q pending %v", state.Status, state.Plan.PendingSlices)
	}
	var reopenEvents []Event
	for _, event := range readRecordTestEvents(t, dir) {
		if event.Type == EventTypePlanReopened {
			reopenEvents = append(reopenEvents, event)
		}
	}
	if len(reopenEvents) != 1 || reopenEvents[0].MutationID == "" || !reopenEvents[0].Timestamp.Equal(reopenedAt) {
		t.Fatalf("reopen events = %#v", reopenEvents)
	}

	// Complete the generated work, approve its new head, and refresh the
	// recorded pull request just as a full pull-request rework cycle does.
	startedAt := reopenedAt.Add(time.Minute)
	if err := record.StartSlice("002-pr-fix", startedAt); err != nil {
		t.Fatal(err)
	}
	completedAt := startedAt.Add(time.Minute)
	if err := record.CompleteSlice("002-pr-fix", "fixed", nil, completedAt); err != nil {
		t.Fatal(err)
	}
	reviewedAt := completedAt.Add(time.Minute)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "new-head", ReviewedAt: reviewedAt}, "pi"); err != nil {
		t.Fatal(err)
	}
	prAt := reviewedAt.Add(time.Minute)
	if err := record.RecordPullRequest(PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17", CreatedAt: prAt}, "feature", "new-head"); err != nil {
		t.Fatal(err)
	}
	if record.Detail().State.Status != StatusCompleted {
		t.Fatalf("completed pull-request cycle status = %q, want completed", record.Detail().State.Status)
	}

	// The same unresolved thread remains consumed while a newly arrived thread
	// can be triaged and converted in a later invocation.
	refreshed := PRFeedbackTriageResult{
		"PRRT_change": {Kind: "change", Rationale: "Requests a lifecycle fix."},
		"PRRT_new":    {Kind: "change", Rationale: "Requests a newly arrived fix."},
	}
	if err := record.RecordPRFeedbackTriage(refreshed, prAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	beforeSlices := len(record.Detail().Slices.Slices)
	err := record.ReopenFromPullRequest([]Slice{newReopenSlice("003-duplicate", "Duplicate pull request feedback", prAt.Add(2*time.Minute))}, []string{"PRRT_change"}, prAt.Add(2*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("second conversion error = %v, want consumed-thread refusal", err)
	}
	if len(record.Detail().Slices.Slices) != beforeSlices || record.Detail().State.Status != StatusCompleted {
		t.Fatal("consumed-thread refusal mutated the completed plan")
	}

	if err := record.ReopenFromPullRequest([]Slice{newReopenSlice("003-new", "Fix new pull request feedback", prAt.Add(3*time.Minute))}, []string{"PRRT_new"}, prAt.Add(3*time.Minute)); err != nil {
		t.Fatalf("reopen for newly arrived thread: %v", err)
	}
	state = readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackConsumedThreadIDs, []string{"PRRT_change", "PRRT_new"}) {
		t.Fatalf("consumed thread IDs after new thread = %#v", state.Plan.PRFeedbackConsumedThreadIDs)
	}
}

func TestReopenFromPullRequestRejectsUntriagedThreadWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	detail := completedReopenDetail()
	detail.State.Plan.PRFeedbackTriage = PRFeedbackTriageResult{
		"PRRT_change": {Kind: "change", Rationale: "Requests a lifecycle fix."},
	}
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	beforeState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	beforeSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	reopenedAt := time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC)

	err = record.ReopenFromPullRequest(
		[]Slice{newReopenSlice("002-pr-fix", "Fix pull request feedback", reopenedAt)},
		[]string{"PRRT_change", "PRRT_missing"},
		reopenedAt,
	)
	if err == nil || !strings.Contains(err.Error(), "PRRT_missing") {
		t.Fatalf("reopen error = %v, want missing triage refusal", err)
	}
	afterState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	afterSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	if string(afterState) != string(beforeState) || string(afterSlices) != string(beforeSlices) {
		t.Fatal("refused pull-request reopen changed persisted artifacts")
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("refused pull-request reopen created events: %v", err)
	}
}

func TestLegacyPlanStateOmitsAbsentPRFeedbackTriage(t *testing.T) {
	var state State
	if err := json.Unmarshal([]byte(`{"schema":"tao.plan.state.v1","plan":{"id":"legacy"}}`), &state); err != nil {
		t.Fatal(err)
	}
	if state.Plan.PRFeedbackTriage != nil || state.Plan.PRFeedbackConsumedThreadIDs != nil {
		t.Fatalf("legacy pull-request feedback state = triage %#v consumed %#v, want nil", state.Plan.PRFeedbackTriage, state.Plan.PRFeedbackConsumedThreadIDs)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "pr_feedback_triage") || strings.Contains(string(encoded), "pr_feedback_consumed_thread_ids") {
		t.Fatalf("legacy state unexpectedly added pull-request feedback fields: %s", encoded)
	}
}

func readRecordTestEvents(t *testing.T, dir string) []Event {
	t.Helper()
	events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("read event warnings: %v", warnings)
	}
	return events
}

func requireRecordTestTriageEvents(t *testing.T, events []Event, want int) []Event {
	t.Helper()
	var triageEvents []Event
	for _, event := range events {
		if event.Type == EventTypePRFeedbackTriaged {
			triageEvents = append(triageEvents, event)
		}
	}
	if len(triageEvents) != want {
		t.Fatalf("pr_feedback_triaged events = %d, want %d: %#v", len(triageEvents), want, events)
	}
	return triageEvents
}

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
