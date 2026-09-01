package plan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReadStateReadsStateJSON(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "state", "State")

	state, err := ReadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan.ID != "state" || state.Plan.Title != "State" {
		t.Fatalf("unexpected state: %+v", state.Plan)
	}
}

func TestRepositoryLoadsHistoricalPlanCommitPolicy(t *testing.T) {
	root := t.TempDir()
	writeMinimalPlan(t, root, "historical", "Historical")
	planDir := filepath.Join(root, "historical")
	state, err := ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	state.Plan.LastRunCommitPolicy = "plan"
	if err := writeState(planDir, state); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(root).GetPlan(context.Background(), "historical")
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.LastRunCommitPolicy != "plan" {
		t.Fatalf("historical commit policy = %q, want plan", detail.State.Plan.LastRunCommitPolicy)
	}
}

func TestAppendEventAppendsJSONL(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "events")
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(planDir, "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"plan_created","timestamp":"2026-04-27T18:10:50Z","plan_id":"events","message":"created"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	timestamp := time.Date(2026, 4, 27, 18, 12, 0, 0, time.UTC)
	if err := AppendEvent(planDir, Event{Type: "slice_started", Timestamp: timestamp, PlanID: "events", SliceID: "001-a", Message: "started"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path) //nolint:gosec // G304: test path is internally constructed
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two JSONL events, got %d: %q", len(lines), content)
	}
	if !strings.Contains(lines[1], `"type":"slice_started"`) || !strings.Contains(lines[1], `"slice_id":"001-a"`) {
		t.Fatalf("unexpected appended event: %s", lines[1])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected events.jsonl permissions 0600, got %o", info.Mode().Perm())
	}
}

func TestPlanRecordSingleMergeCommitIntentRoundTripsAndClears(t *testing.T) {
	root := t.TempDir()
	writeMinimalPlan(t, root, "merge-intent", "Merge Intent")
	planDir := filepath.Join(root, "merge-intent")
	repo := NewFileRepository(root)
	detail, err := repo.GetPlan(context.Background(), "merge-intent")
	if err != nil {
		t.Fatal(err)
	}
	intent := SingleMergeCommitIntent{
		Message: "feat(merge): use review proposal\n\nWhat:\nUse it.\n\nWhy:\nKeep recovery exact.\n\nTao-Plan: merge-intent\nTao-Source-Head: source123",
		PlanID:  "merge-intent", SourceHead: "source123", DefaultBranch: "main", DefaultParent: "base123",
		CreatedAt: time.Date(2026, 7, 23, 20, 15, 0, 0, time.UTC),
	}
	record, err := NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.RecordSingleMergeCommitIntent(intent); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repo.GetPlan(context.Background(), "merge-intent")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.MergeCommitIntent == nil || *reloaded.State.Plan.MergeCommitIntent != intent {
		t.Fatalf("single-merge intent did not round-trip: %#v", reloaded.State.Plan.MergeCommitIntent)
	}
	if err := record.ClearSingleMergeCommitIntent(intent); err != nil {
		t.Fatal(err)
	}
	reloaded, err = repo.GetPlan(context.Background(), "merge-intent")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.MergeCommitIntent != nil {
		t.Fatalf("single-merge intent was not cleared: %#v", reloaded.State.Plan.MergeCommitIntent)
	}
}

func TestPlanRecordFinalizationFailureLifecycle(t *testing.T) {
	root := t.TempDir()
	writeMinimalPlan(t, root, "failure", "Failure")
	planDir := filepath.Join(root, "failure")
	repo := NewFileRepository(root)
	detail, err := repo.GetPlan(context.Background(), "failure")
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	failedAt := time.Date(2026, 8, 29, 12, 0, 0, 0, time.FixedZone("offset", -7*60*60))
	failure := FinalizationFailure{
		Phase: FinalizationFailurePhasePullRequest, Category: "push_failed", Branch: "fix/failure", HeadSHA: "head123",
		FailedAt: failedAt, RecoveryAction: "resume_pull_request",
	}
	if err := record.RecordFinalizationFailure(failure); err != nil {
		t.Fatal(err)
	}
	failure.FailedAt = failure.FailedAt.UTC()
	if err := record.RecordFinalizationFailure(failure); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if got := len(detail.Events); got != 1 {
		t.Fatalf("failure event count = %d, want 1", got)
	}
	conflict := failure
	conflict.Category = "create_failed"
	if err := record.RecordFinalizationFailure(conflict); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting failure error = %v", err)
	}

	reloaded, err := repo.GetPlan(context.Background(), "failure")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.FinalizationFailure == nil || *reloaded.State.Plan.FinalizationFailure != failure {
		t.Fatalf("failure did not round-trip: %#v", reloaded.State.Plan.FinalizationFailure)
	}
	if eventFailure := reloaded.Events[0].FinalizationFailure; eventFailure == nil || *eventFailure != failure {
		t.Fatalf("failure event did not replay: %#v", reloaded.Events[0])
	}
	summary := Summarize(reloaded, failure.FailedAt.Add(time.Minute))
	if summary.FinalizationFailure == nil || *summary.FinalizationFailure != failure {
		t.Fatalf("summary failure = %#v", summary.FinalizationFailure)
	}
	summary.FinalizationFailure.Category = "mutated"
	if reloaded.State.Plan.FinalizationFailure.Category != "push_failed" {
		t.Fatal("summary shared finalization failure storage")
	}

	clearedAt := failure.FailedAt.Add(time.Minute)
	if err := record.ClearFinalizationFailure(failure, clearedAt); err != nil {
		t.Fatal(err)
	}
	if err := record.ClearFinalizationFailure(failure, clearedAt); err != nil {
		t.Fatalf("idempotent clear retry: %v", err)
	}
	reloaded, err = repo.GetPlan(context.Background(), "failure")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.FinalizationFailure != nil {
		t.Fatalf("failure was not cleared: %#v", reloaded.State.Plan.FinalizationFailure)
	}
	clearEvents := 0
	for _, event := range reloaded.Events {
		if event.Type == EventTypeFinalizationFailureCleared {
			clearEvents++
		}
	}
	if clearEvents != 1 {
		t.Fatalf("clear event count = %d, want 1", clearEvents)
	}
}

func TestPlanRecordReplaceFinalizationFailurePersistenceFailuresPreserveEvidence(t *testing.T) {
	for _, operation := range []string{"journal", "state", "event-1", "event-2", "remove"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			writeMinimalPlan(t, root, "replace-failure", "Replace Failure")
			planDir := filepath.Join(root, "replace-failure")
			repo := NewFileRepository(root)
			detail, err := repo.GetPlan(context.Background(), "replace-failure")
			if err != nil {
				t.Fatal(err)
			}
			initialRecord, err := NewPlanRecord(planDir, detail)
			if err != nil {
				t.Fatal(err)
			}
			original := FinalizationFailure{
				Phase: FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "fix/failure", HeadSHA: "head123",
				FailedAt: time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC), RecoveryAction: FinalizationRecoveryResumePullRequest,
			}
			if err := initialRecord.RecordFinalizationFailure(original); err != nil {
				t.Fatal(err)
			}
			replacement := original
			replacement.FailedAt = original.FailedAt.Add(time.Hour)

			ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: operation}
			store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
			record, err := newPlanRecord(store, planDir, detail)
			if err != nil {
				t.Fatal(err)
			}
			err = record.ReplaceFinalizationFailure(original, replacement)
			if err == nil || !strings.Contains(err.Error(), "injected "+operation+" failure") {
				t.Fatalf("replacement error = %v, want injected %s failure", err, operation)
			}
			if current := detail.State.Plan.FinalizationFailure; current == nil || *current != original {
				t.Fatalf("failed replacement changed in-memory evidence: %#v", current)
			}

			reloaded, err := repo.GetPlan(context.Background(), "replace-failure")
			if err != nil {
				t.Fatalf("recover replacement: %v", err)
			}
			current := reloaded.State.Plan.FinalizationFailure
			if current == nil || (*current != original && *current != replacement) {
				t.Fatalf("recovered evidence = %#v, want old or replacement", current)
			}
			if operation == "journal" && *current != original {
				t.Fatalf("pre-journal failure evidence = %#v, want original", current)
			}
			if operation != "journal" && *current != replacement {
				t.Fatalf("journaled replacement evidence = %#v, want replacement", current)
			}
			if *current == replacement {
				var cleared, recorded bool
				for _, event := range reloaded.Events {
					if event.Timestamp != replacement.FailedAt || event.FinalizationFailure == nil {
						continue
					}
					switch event.Type {
					case EventTypeFinalizationFailureCleared:
						cleared = *event.FinalizationFailure == original
					case EventTypeFinalizationFailed:
						recorded = *event.FinalizationFailure == replacement
					}
				}
				if !cleared || !recorded {
					t.Fatalf("replacement lifecycle events missing: %#v", reloaded.Events)
				}
			}
		})
	}
}

func TestPlanRecordReplaceFinalizationFailureRejectsBoundaryDrift(t *testing.T) {
	root := t.TempDir()
	writeMinimalPlan(t, root, "replace-boundary", "Replace Boundary")
	planDir := filepath.Join(root, "replace-boundary")
	detail, err := NewFileRepository(root).GetPlan(context.Background(), "replace-boundary")
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	original := FinalizationFailure{
		Phase: FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "fix/failure", HeadSHA: "head123",
		FailedAt: time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC), RecoveryAction: FinalizationRecoveryResumePullRequest,
	}
	if err := record.RecordFinalizationFailure(original); err != nil {
		t.Fatal(err)
	}
	replacement := original
	replacement.HeadSHA = "head456"
	replacement.FailedAt = original.FailedAt.Add(time.Hour)
	if err := record.ReplaceFinalizationFailure(original, replacement); err == nil || !strings.Contains(err.Error(), "preserve the durable boundary") {
		t.Fatalf("boundary replacement error = %v", err)
	}
	if current := detail.State.Plan.FinalizationFailure; current == nil || *current != original {
		t.Fatalf("boundary rejection changed evidence: %#v", current)
	}
}

func TestPlanRecordFinalizationFailureClearsOnHeadReplacementAndSuccess(t *testing.T) {
	root := t.TempDir()
	writeMinimalPlan(t, root, "settle-failure", "Settle Failure")
	planDir := filepath.Join(root, "settle-failure")
	state, err := ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	state.Workspace = &Workspace{Branch: "fix/failure", HeadSHA: "head123"}
	if err := writeState(planDir, state); err != nil {
		t.Fatal(err)
	}
	repo := NewFileRepository(root)
	detail, err := repo.GetPlan(context.Background(), "settle-failure")
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	failure := FinalizationFailure{
		Phase: FinalizationFailurePhasePullRequest, Category: "push_failed", Branch: "fix/failure", HeadSHA: "head123",
		FailedAt: time.Date(2026, 8, 29, 19, 0, 0, 0, time.UTC), RecoveryAction: "resume_pull_request",
	}
	if err := record.RecordFinalizationFailure(failure); err != nil {
		t.Fatal(err)
	}
	if err := record.AdvanceWorkspaceHead("fix/failure", "head123", "head456"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.FinalizationFailure != nil {
		t.Fatal("head replacement retained stale failure")
	}

	failure.HeadSHA = "head456"
	failure.FailedAt = failure.FailedAt.Add(time.Minute)
	if err := record.RecordFinalizationFailure(failure); err != nil {
		t.Fatal(err)
	}
	createdAt := failure.FailedAt.Add(time.Minute)
	if err := record.RecordPullRequest(PullRequest{Number: 7, URL: "https://example.test/pull/7", CreatedAt: createdAt}, "fix/failure", "head456"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.FinalizationFailure != nil {
		t.Fatal("successful pull request retained failure")
	}
}

func TestPlanRecordPullRequestIntentRoundTripsAndClearsOnSuccess(t *testing.T) {
	root := t.TempDir()
	writeMinimalPlan(t, root, "pr-intent", "PR Intent")
	planDir := filepath.Join(root, "pr-intent")
	repo := NewFileRepository(root)
	detail, err := repo.GetPlan(context.Background(), "pr-intent")
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	pr := PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: time.Date(2026, 8, 12, 20, 0, 0, 0, time.UTC)}
	if err := record.RecordPullRequestIntent(PullRequest{}, "feature/pr-intent", "head123"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repo.GetPlan(context.Background(), "pr-intent")
	if err != nil {
		t.Fatal(err)
	}
	intent := reloaded.State.Plan.PullRequestIntent
	if intent == nil || intent.Number != 0 || intent.URL != "" || intent.Branch != "feature/pr-intent" || intent.HeadSHA != "head123" {
		t.Fatalf("unidentified pull request intent did not round-trip: %#v", intent)
	}
	if err := record.RecordPullRequestIntent(pr, "feature/pr-intent", "head123"); err != nil {
		t.Fatal(err)
	}
	reloaded, err = repo.GetPlan(context.Background(), "pr-intent")
	if err != nil {
		t.Fatal(err)
	}
	intent = reloaded.State.Plan.PullRequestIntent
	if intent == nil || intent.Number != 42 || intent.URL != pr.URL || intent.Branch != "feature/pr-intent" || intent.HeadSHA != "head123" {
		t.Fatalf("identified pull request intent did not refine prior evidence: %#v", intent)
	}
	if err := record.RecordPullRequest(pr, "feature/pr-intent", "head123"); err != nil {
		t.Fatal(err)
	}
	reloaded, err = repo.GetPlan(context.Background(), "pr-intent")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.PullRequestIntent != nil || reloaded.State.Plan.PullRequest == nil {
		t.Fatalf("successful pull request did not settle intent: intent=%#v pr=%#v", reloaded.State.Plan.PullRequestIntent, reloaded.State.Plan.PullRequest)
	}
}

func TestAppendEventCreatesEventsJSONL(t *testing.T) {
	planDir := t.TempDir()
	timestamp := time.Date(2026, 4, 27, 18, 12, 0, 0, time.UTC)

	if err := AppendEvent(planDir, Event{Type: "slice_completed", Timestamp: timestamp, PlanID: "events", Message: "completed"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(planDir, "events.jsonl")
	content, err := os.ReadFile(path) //nolint:gosec // G304: test path is internally constructed
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); !strings.HasSuffix(got, "\n") || !strings.Contains(got, `"type":"slice_completed"`) {
		t.Fatalf("unexpected created event log: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected events.jsonl permissions 0600, got %o", info.Mode().Perm())
	}
}

func TestRepositoryOwnsPlanMutations(t *testing.T) {
	dir := t.TempDir()
	writePlan(t, dir, "mutate", `{
  "schema":"tao.plan.state.v1",
  "status":"planned",
  "created_at":"2026-04-27T18:10:50Z",
  "updated_at":"2026-04-27T18:10:50Z",
  "repo":{"name":"rollcall","root":"/repo","branch":"main"},
  "plan":{"id":"mutate","title":"Mutate Plan","current_slice":null,"completed_slices":[],"pending_slices":["001-a"],"timing":{"started_at":null,"completed_at":null,"last_activity_at":"2026-04-27T18:10:50Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"mutate","execution":{"mode":"serial","parallel_safe":false},
  "slices":[{"id":"001-a","title":"A","status":"pending","depends_on":[],"timing":{"created_at":"2026-04-27T18:10:50Z","started_at":null,"completed_at":null,"updated_at":"2026-04-27T18:10:50Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}}]
}`)
	repo := NewFileRepository(dir)
	planDir := filepath.Join(dir, "mutate")
	detail, err := repo.GetPlan(context.Background(), "mutate")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 5, 3, 23, 0, 0, 0, time.UTC)

	if err := testRepoRecord(repo, detail).StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusInProgress || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" || len(detail.Events) != 1 {
		t.Fatalf("StartSlice did not update in-memory detail: state=%+v events=%#v", detail.State.Plan, detail.Events)
	}
	state, err := ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusInProgress || state.Plan.CurrentSlice == nil || *state.Plan.CurrentSlice != "001-a" {
		t.Fatalf("unexpected started state: %+v", state.Plan)
	}
	if err := testRepoRecord(repo, detail).StartSlice("001-a", started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != EventTypeSliceStarted {
		t.Fatalf("StartSlice retry duplicated in-memory events: %#v", detail.Events)
	}

	completed := started.Add(2 * time.Minute)
	if err := testRepoRecord(repo, detail).CompleteSlice("001-a", "done", nil, completed); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusInReview || len(detail.State.Plan.CompletedSlices) != 1 || len(detail.Events) != 2 || detail.Events[1].Type != EventTypeSliceCompleted {
		t.Fatalf("CompleteSlice did not update in-memory detail: state=%+v events=%#v", detail.State.Plan, detail.Events)
	}
	reloaded, err := repo.GetPlan(context.Background(), "mutate")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Status != StatusInReview || len(reloaded.State.Plan.CompletedSlices) != 1 || reloaded.Slices.Slices[0].Notes != "done" {
		t.Fatalf("unexpected completed plan: %+v %+v", reloaded.State.Plan, reloaded.Slices.Slices[0])
	}
	if len(reloaded.Events) != 2 || reloaded.Events[0].Type != EventTypeSliceStarted || reloaded.Events[1].Type != EventTypeSliceCompleted {
		t.Fatalf("unexpected lifecycle events: %+v", reloaded.Events)
	}
}

func TestPlanRecordStateEventMutationsRecoverInstalledPrefixes(t *testing.T) {
	mutatedAt := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		apply     func(*PlanRecord) error
		eventType string
		assert    func(*testing.T, *PlanDetail)
	}{
		{
			name: "finalization failure",
			apply: func(record *PlanRecord) error {
				return record.RecordFinalizationFailure(FinalizationFailure{
					Phase: FinalizationFailurePhaseProposalRepair, Category: "proposal_invalid", ReviewBase: "base123", ReviewHead: "head123",
					FailedAt: mutatedAt, RecoveryAction: "rerun_review",
				})
			},
			eventType: EventTypeFinalizationFailed,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if failure := detail.State.Plan.FinalizationFailure; failure == nil || failure.Category != "proposal_invalid" || failure.ReviewHead != "head123" {
					t.Fatalf("replayed finalization failure = %#v", failure)
				}
			},
		},
		{
			name: "pull request",
			apply: func(record *PlanRecord) error {
				return record.RecordPullRequest(PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: mutatedAt}, "tao/plan-a", "head123")
			},
			eventType: EventTypePullRequestCreated,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if pr := detail.State.Plan.PullRequest; pr == nil || pr.Number != 42 || pr.Branch != "tao/plan-a" || pr.HeadSHA != "head123" {
					t.Fatalf("replayed pull request = %#v", pr)
				}
			},
		},
		{
			name: "review error",
			apply: func(record *PlanRecord) error {
				return record.RecordReviewError(PlanReview{Status: ReviewStatusError, Summary: "review failed", ReviewedAt: mutatedAt}, "pi")
			},
			eventType: EventTypePlanReviewed,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if review := detail.State.Plan.Review; review == nil || review.Status != ReviewStatusError || review.Summary != "review failed" {
					t.Fatalf("replayed review error = %#v", review)
				}
			},
		},
		{
			name: "review completed",
			apply: func(record *PlanRecord) error {
				return record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "approved", Findings: []ReviewFinding{}, ReviewedAt: mutatedAt}, "pi")
			},
			eventType: EventTypePlanReviewed,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if review := detail.State.Plan.Review; review == nil || !review.IsApproved() || review.Summary != "approved" {
					t.Fatalf("replayed completed review = %#v", review)
				}
			},
		},
		{
			name: "merge",
			apply: func(record *PlanRecord) error {
				return record.RecordMerged("tao/plan-a", "merged123", mutatedAt)
			},
			eventType: EventTypePlanMerged,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if detail.State.Status != StatusCompleted {
					t.Fatalf("replayed merge status = %q", detail.State.Status)
				}
			},
		},
	}

	for _, test := range tests {
		for _, operation := range []string{"event-1", "remove"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "plan-a")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				detail := startSliceDetail(dir)
				if test.eventType == EventTypePlanReviewed {
					detail.State.Status = StatusInReview
					detail.State.Plan.PendingSlices = nil
					detail.State.Plan.CompletedSlices = []string{"001-a"}
					detail.Slices.Slices[0].Status = StatusCompleted
				}
				original := clonePlanDetail(detail)
				writeStartSliceArtifacts(t, dir, detail)
				slicesBefore, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // Test path is rooted in t.TempDir.
				if err != nil {
					t.Fatal(err)
				}
				ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: operation}
				store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
				record, err := newPlanRecord(store, dir, detail)
				if err != nil {
					t.Fatal(err)
				}

				err = test.apply(record)
				if err == nil || !strings.Contains(err.Error(), "injected "+operation+" failure") {
					t.Fatalf("record error = %v, want injected %s failure", err, operation)
				}
				if !reflect.DeepEqual(detail, original) {
					t.Fatalf("failed record changed in-memory detail:\n got: %#v\nwant: %#v", detail, original)
				}

				if err := test.apply(record); err != nil {
					t.Fatalf("retry through same record: %v", err)
				}
				test.assert(t, detail)
				if len(detail.Events) != 1 || detail.Events[0].Type != test.eventType || detail.Events[0].MutationID == "" {
					t.Fatalf("retry duplicated or lost recovered events = %#v", detail.Events)
				}
				persisted, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
				if err != nil || len(warnings) != 0 || len(persisted) != 1 || persisted[0].MutationID != detail.Events[0].MutationID {
					t.Fatalf("persisted retry events = %#v warnings=%v err=%v", persisted, warnings, err)
				}
				slicesAfter, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // Test path is rooted in t.TempDir.
				if err != nil {
					t.Fatal(err)
				}
				if string(slicesAfter) != string(slicesBefore) {
					t.Fatal("state/event mutation rewrote slices.json")
				}
				if _, statErr := os.Stat(filepath.Join(dir, mutationJournalFile)); !os.IsNotExist(statErr) {
					t.Fatalf("journal remains after replay: %v", statErr)
				}
			})
		}
	}
}

func TestPlanRecordArtifactMutationRetriesRecognizeRecoveredOperation(t *testing.T) {
	mutatedAt := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		planID         string
		setup          func(string) *PlanDetail
		apply          func(*PlanRecord, time.Time) error
		eventType      string
		freshRetryWant string
		assert         func(*testing.T, *PlanDetail)
	}{
		{
			name:   "remove",
			planID: "edit",
			setup: func(dir string) *PlanDetail {
				detail := editPlanDetail()
				detail.Dir = dir
				return detail
			},
			apply:     func(record *PlanRecord, now time.Time) error { return record.RemoveSlice("003-c", now) },
			eventType: EventTypeSliceRemoved,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if findSlice(detail, "003-c") != nil || slices.Contains(detail.State.Plan.PendingSlices, "003-c") {
					t.Fatalf("remove postcondition missing: pending=%v slices=%#v", detail.State.Plan.PendingSlices, detail.Slices.Slices)
				}
			},
		},
		{
			name:   "skip",
			planID: "edit",
			setup: func(dir string) *PlanDetail {
				detail := editPlanDetail()
				detail.Dir = dir
				return detail
			},
			apply:     func(record *PlanRecord, now time.Time) error { return record.SkipSlice("003-c", now) },
			eventType: EventTypeSliceSkipped,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				slice := findSlice(detail, "003-c")
				if slice == nil || slice.Status != StatusSkipped || slices.Contains(detail.State.Plan.PendingSlices, "003-c") {
					t.Fatalf("skip postcondition missing: pending=%v slice=%#v", detail.State.Plan.PendingSlices, slice)
				}
			},
		},
		{
			name:   "continue",
			planID: "plan-a",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusBlocked
				detail.Slices.Slices[0].Status = StatusBlocked
				detail.Slices.Slices[0].BlockerNote = "resolved blocker"
				return detail
			},
			apply:          func(record *PlanRecord, now time.Time) error { return record.ContinueBlocked(now) },
			freshRetryWant: "continue is not meaningful",
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				slice := findSlice(detail, "001-a")
				if detail.State.Status != StatusInProgress || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" || slice == nil || slice.Status != StatusInProgress || slice.BlockerNote != "" {
					t.Fatalf("continue postcondition missing: state=%#v slice=%#v", detail.State, slice)
				}
			},
		},
		{
			name:   "reorder",
			planID: "edit",
			setup: func(dir string) *PlanDetail {
				detail := editPlanDetail()
				detail.Dir = dir
				return detail
			},
			apply: func(record *PlanRecord, now time.Time) error {
				return record.ReorderPendingSlices([]string{"001-a", "003-c", "002-b"}, now)
			},
			eventType: EventTypeSlicesReordered,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if !slices.Equal(detail.State.Plan.PendingSlices, []string{"001-a", "003-c", "002-b"}) {
					t.Fatalf("reorder postcondition missing: pending=%v", detail.State.Plan.PendingSlices)
				}
			},
		},
		{
			name:   "reopen",
			planID: "reopen",
			setup: func(dir string) *PlanDetail {
				detail := completedReopenDetail()
				detail.Dir = dir
				return detail
			},
			apply: func(record *PlanRecord, now time.Time) error {
				return record.Reopen([]Slice{newReopenSlice("002-fix", "Fix review finding", now)}, now)
			},
			eventType: EventTypePlanReopened,
			assert: func(t *testing.T, detail *PlanDetail) {
				t.Helper()
				if detail.State.Status != StatusInProgress || !slices.Equal(detail.State.Plan.PendingSlices, []string{"002-fix"}) || findSlice(detail, "002-fix") == nil {
					t.Fatalf("reopen postcondition missing: state=%#v slices=%#v", detail.State, detail.Slices.Slices)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), test.planID)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			detail := test.setup(dir)
			original := clonePlanDetail(detail)
			writeStartSliceArtifacts(t, dir, detail)
			ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "remove"}
			store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
			record, err := newPlanRecord(store, dir, detail)
			if err != nil {
				t.Fatal(err)
			}

			if err := test.apply(record, mutatedAt); err == nil || !strings.Contains(err.Error(), "injected remove failure") {
				t.Fatalf("first mutation error = %v, want injected journal removal failure", err)
			}
			if !reflect.DeepEqual(detail, original) {
				t.Fatalf("failed mutation changed in-memory detail:\n got: %#v\nwant: %#v", detail, original)
			}
			files, err := readPlanFiles(dir)
			if err != nil {
				t.Fatalf("read installed mutation targets: %v", err)
			}
			installed := detailFromFiles(files)
			test.assert(t, installed)
			mutationID := requireSingleEventType(t, installed.Events, test.eventType)

			retriedAt := mutatedAt.Add(time.Minute)
			if err := test.apply(record, retriedAt); err != nil {
				t.Fatalf("retry through same record at a later timestamp: %v", err)
			}
			if !reflect.DeepEqual(detail.State, installed.State) || !reflect.DeepEqual(detail.Slices, installed.Slices) {
				t.Fatalf("later retry changed recovered postimage:\n got: state=%#v slices=%#v\nwant: state=%#v slices=%#v", detail.State, detail.Slices, installed.State, installed.Slices)
			}
			test.assert(t, detail)
			if got := requireSingleEventType(t, detail.Events, test.eventType); got != mutationID {
				t.Fatalf("retry event mutation_id = %q, want recovered %q", got, mutationID)
			}

			// A process restart loads the already-settled artifacts before it can
			// reconstruct the request. The operation-level postcondition must make
			// that retry idempotent too, not only a retry that performs recovery.
			settledFiles, err := loadPlanFiles(dir)
			if err != nil {
				t.Fatalf("load settled retry: %v", err)
			}
			settledDetail := detailFromFiles(settledFiles)
			freshRecord, err := NewPlanRecord(dir, settledDetail)
			if err != nil {
				t.Fatal(err)
			}
			freshRetryErr := test.apply(freshRecord, retriedAt.Add(time.Minute))
			if test.freshRetryWant != "" {
				if freshRetryErr == nil || !strings.Contains(freshRetryErr.Error(), test.freshRetryWant) {
					t.Fatalf("fresh retry error = %v, want text %q", freshRetryErr, test.freshRetryWant)
				}
			} else if freshRetryErr != nil {
				t.Fatalf("retry through fresh record at a later timestamp: %v", freshRetryErr)
			}
			if !reflect.DeepEqual(settledDetail.State, installed.State) || !reflect.DeepEqual(settledDetail.Slices, installed.Slices) {
				t.Fatalf("fresh later retry changed settled postimage:\n got: state=%#v slices=%#v\nwant: state=%#v slices=%#v", settledDetail.State, settledDetail.Slices, installed.State, installed.Slices)
			}
			test.assert(t, settledDetail)
			if got := requireSingleEventType(t, settledDetail.Events, test.eventType); got != mutationID {
				t.Fatalf("fresh retry event mutation_id = %q, want recovered %q", got, mutationID)
			}
			if _, statErr := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("journal remains after retry: %v", statErr)
			}
		})
	}
}

func requireSingleEventType(t *testing.T, events []Event, eventType string) string {
	t.Helper()
	if eventType == "" {
		return ""
	}
	mutationID := ""
	count := 0
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		count++
		mutationID = event.MutationID
	}
	if count != 1 || mutationID == "" {
		t.Fatalf("events of type %s = %d with mutation_id %q: %#v", eventType, count, mutationID, events)
	}
	return mutationID
}

func TestPlanRecordStateEventRetryPreservesNewerStateAfterRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	detail := startSliceDetail(dir)
	detail.State.Status = StatusInReview
	detail.State.Plan.PendingSlices = nil
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices[0].Status = StatusCompleted
	writeStartSliceArtifacts(t, dir, detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "event-1"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	staleRecord, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	pullRequestAt := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	pullRequest := PullRequest{Number: 42, URL: "https://example.test/pull/42", CreatedAt: pullRequestAt}

	if err := staleRecord.RecordPullRequest(pullRequest, "tao/plan-a", "head123"); err == nil || !strings.Contains(err.Error(), "injected event-1 failure") {
		t.Fatalf("first pull request error = %v, want injected event failure", err)
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatalf("recover pull request mutation: %v", err)
	}
	newerDetail := detailFromFiles(files)
	pullRequestMutationID := ""
	for _, event := range newerDetail.Events {
		if event.Type == EventTypePullRequestCreated {
			pullRequestMutationID = event.MutationID
			break
		}
	}
	if pullRequestMutationID == "" {
		t.Fatalf("recovered pull request event has no mutation id: %#v", newerDetail.Events)
	}
	newerRecord, err := NewPlanRecord(dir, newerDetail)
	if err != nil {
		t.Fatal(err)
	}
	reviewedAt := pullRequestAt.Add(time.Minute)
	review := PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "approved", Findings: []ReviewFinding{}, ReviewedAt: reviewedAt}
	if err := newerRecord.RecordReviewCompleted(review, "pi"); err != nil {
		t.Fatalf("record later review: %v", err)
	}
	wantState := cloneState(newerDetail.State)

	if err := staleRecord.RecordPullRequest(pullRequest, "tao/plan-a", "head123"); err != nil {
		t.Fatalf("retry recovered pull request: %v", err)
	}
	if !reflect.DeepEqual(detail.State, wantState) {
		t.Fatalf("retry replaced newer state:\n got: %#v\nwant: %#v", detail.State, wantState)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted.state, wantState) {
		t.Fatalf("retry persisted stale state:\n got: %#v\nwant: %#v", persisted.state, wantState)
	}
	pullRequestEvents := 0
	for _, event := range persisted.events {
		if event.Type != EventTypePullRequestCreated {
			continue
		}
		pullRequestEvents++
		if event.MutationID != pullRequestMutationID {
			t.Fatalf("retry replaced pull request event evidence: %#v", event)
		}
	}
	if pullRequestEvents != 1 {
		t.Fatalf("pull request events after retry = %d, want 1", pullRequestEvents)
	}
}

func TestPlanRecordMergedRetryRejectsDifferentEvidenceAfterRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "event-1"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	mergedAt := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)

	if err := record.RecordMerged("tao/plan-a", "merged123", mergedAt); err == nil || !strings.Contains(err.Error(), "injected event-1 failure") {
		t.Fatalf("first merge error = %v, want injected event failure", err)
	}
	if err := record.RecordMerged("tao/plan-a", "different456", mergedAt.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "different evidence") {
		t.Fatalf("retry merge error = %v, want different evidence", err)
	}
	if detail.State.Status != StatusCompleted || len(detail.Events) != 1 || detail.Events[0].MergedDefaultSHA != "merged123" {
		t.Fatalf("retry did not publish original merge: state=%q events=%#v", detail.State.Status, detail.Events)
	}
	persisted, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 || len(persisted) != 1 || persisted[0].MergedDefaultSHA != "merged123" {
		t.Fatalf("persisted merge events = %#v warnings=%v err=%v", persisted, warnings, err)
	}
}

func TestPlanRecordStartSliceWithRunBoundaryPersistsOneMutation(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	store := &recordingArtifactMutationStore{}
	record, err := newPlanRecord(store, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 18, 1, 15, 0, 0, time.UTC)
	boundary := SliceExecutionStart{
		Branch: "tao/plan-a", Head: "base123", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree,
	}

	if err := record.StartSliceWithRunBoundary("001-a", "/worktrees/plan-a", "slice", []string{"README.md"}, boundary, startedAt); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(store.calls, ","); got != "state,slices,event" {
		t.Fatalf("persistence order = %q, want state,slices,event", got)
	}
	if store.state.Plan.LastRunCommitPolicy != "slice" || !slices.Equal(store.state.Plan.LastRunStartingDirty, []string{"README.md"}) {
		t.Fatalf("persisted run metadata = policy:%q dirty:%#v", store.state.Plan.LastRunCommitPolicy, store.state.Plan.LastRunStartingDirty)
	}
	persistedSlice := store.slices.Slices[0]
	if persistedSlice.ExecutionRoot != "/worktrees/plan-a" || persistedSlice.ExecutionStart == nil || *persistedSlice.ExecutionStart != boundary {
		t.Fatalf("persisted execution boundary = %#v", persistedSlice)
	}
	var startedEvent *Event
	for i := range store.events {
		if store.events[i].Type == EventTypeSliceStarted && store.events[i].SliceID == "001-a" {
			startedEvent = &store.events[i]
			break
		}
	}
	if startedEvent == nil || !startedEvent.Timestamp.Equal(startedAt) {
		t.Fatalf("persisted slice_started event = %#v", startedEvent)
	}
	if detail.Slices.Slices[0].ExecutionStart == nil || *detail.Slices.Slices[0].ExecutionStart != boundary {
		t.Fatalf("in-memory execution boundary = %#v", detail.Slices.Slices[0].ExecutionStart)
	}
}

func TestPlanRecordRepairSliceStartWithRunBoundaryCompletesPersistedPrefixes(t *testing.T) {
	startedAt := time.Date(2026, 7, 18, 1, 45, 0, 0, time.UTC)
	boundary := SliceExecutionStart{
		Branch: "tao/plan-a", Head: "base123", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree,
	}
	tests := []struct {
		name   string
		mutate func(*PlanDetail)
	}{
		{name: "state advanced", mutate: func(detail *PlanDetail) {
			detail.State.Status = StatusInProgress
			detail.State.UpdatedAt = startedAt
			detail.State.Plan.CurrentSlice = new("001-a")
			detail.State.Plan.Timing.StartedAt = new(startedAt)
			detail.State.Plan.Timing.LastActivityAt = new(startedAt)
		}},
		{name: "slices advanced and event missing", mutate: func(detail *PlanDetail) {
			if _, _, err := MarkSliceStarted(detail, "001-a", startedAt); err != nil {
				t.Fatal(err)
			}
			detail.Events = nil
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := startSliceDetail("/plans/plan-a")
			tt.mutate(detail)
			store := &recordingArtifactMutationStore{}
			record, err := newPlanRecord(store, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}

			if err := record.RepairSliceStartWithRunBoundary("001-a", "/worktrees/plan-a", "slice", nil, boundary, startedAt); err != nil {
				t.Fatal(err)
			}

			if got := strings.Join(store.calls, ","); got != "state,slices,event" {
				t.Fatalf("persistence order = %q, want state,slices,event", got)
			}
			persistedSlice := store.slices.Slices[0]
			if persistedSlice.Status != StatusInProgress || persistedSlice.ExecutionRoot != "/worktrees/plan-a" || persistedSlice.ExecutionStart == nil || *persistedSlice.ExecutionStart != boundary {
				t.Fatalf("repaired slice = %#v", persistedSlice)
			}
			if persistedSlice.Timing.StartedAt == nil || !persistedSlice.Timing.StartedAt.Equal(startedAt) {
				t.Fatalf("repaired started_at = %v, want %s", persistedSlice.Timing.StartedAt, startedAt)
			}
			if len(store.events) != 1 || store.events[0].Type != EventTypeSliceStarted || !store.events[0].Timestamp.Equal(startedAt) {
				t.Fatalf("repaired events = %#v", store.events)
			}
		})
	}
}

func TestPlanRecordRecordFinalVerificationPersistsStateMetadata(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	store := &recordingArtifactMutationStore{}
	record, err := newPlanRecord(store, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	verifiedAt := time.Date(2026, 7, 18, 2, 0, 0, 123, time.FixedZone("offset", 3600))
	verification := FinalVerification{Command: "make verify", CWD: "/repo", Result: "passed", VerifiedAt: verifiedAt}

	if err := record.RecordFinalVerification(verification); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(store.calls, ","); got != "state" {
		t.Fatalf("persistence calls = %q, want state", got)
	}
	wantTime := verifiedAt.UTC()
	if store.state.Plan.FinalVerification == nil || store.state.Plan.FinalVerification.VerifiedAt != wantTime || store.state.UpdatedAt != wantTime {
		t.Fatalf("persisted final verification = %#v updated_at=%s", store.state.Plan.FinalVerification, store.state.UpdatedAt)
	}
	if store.state.Plan.Timing.LastActivityAt == nil || *store.state.Plan.Timing.LastActivityAt != wantTime {
		t.Fatalf("persisted last_activity_at = %v, want %s", store.state.Plan.Timing.LastActivityAt, wantTime)
	}
	if detail.State.Plan.FinalVerification == nil || *detail.State.Plan.FinalVerification != *store.state.Plan.FinalVerification {
		t.Fatalf("in-memory final verification = %#v", detail.State.Plan.FinalVerification)
	}
}

func TestPlanRecordFinalVerificationWriteFailureRetainsInMemoryMetadata(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	persistErr := errors.New("state unavailable")
	store := &recordingArtifactMutationStore{writeStateErr: persistErr}
	record, err := newPlanRecord(store, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	verification := FinalVerification{
		CWD: "/repo", Result: "passed", VerifiedAt: time.Date(2026, 7, 18, 2, 15, 0, 0, time.UTC),
	}

	if err := record.RecordFinalVerification(verification); !errors.Is(err, persistErr) {
		t.Fatalf("record error = %v, want %v", err, persistErr)
	}
	if detail.State.Plan.FinalVerification == nil || *detail.State.Plan.FinalVerification != verification {
		t.Fatalf("in-memory final verification = %#v, want %#v", detail.State.Plan.FinalVerification, verification)
	}
	if detail.State.UpdatedAt != verification.VerifiedAt || detail.State.Plan.Timing.LastActivityAt == nil || *detail.State.Plan.Timing.LastActivityAt != verification.VerifiedAt {
		t.Fatalf("in-memory timestamps = updated:%s activity:%v", detail.State.UpdatedAt, detail.State.Plan.Timing.LastActivityAt)
	}
}

func TestPlanRecordAutomaticReworkStopIsDurableAndIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	detail := startSliceDetail(dir)
	detail.State.Status = StatusChangesRequested
	original := clonePlanDetail(detail)
	writeStartSliceArtifacts(t, dir, detail)
	evidence := AutomaticReworkStop{
		Round: 2, Attempts: 2, Fingerprint: "finding-set", Reason: "automatic rework stalled", StoppedAt: editTime(),
	}
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "remove"}
	record, err := newPlanRecord(journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.RecordAutomaticReworkStop(evidence); err == nil || !strings.Contains(err.Error(), "injected remove failure") {
		t.Fatalf("first stop error = %v, want journal removal failure", err)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("failed stop published in-memory changes: got %#v want %#v", detail, original)
	}
	if err := record.RecordAutomaticReworkStop(evidence); err != nil {
		t.Fatalf("same-record retry: %v", err)
	}
	mutationID := requireSingleEventType(t, detail.Events, EventTypeReworkStopped)
	if !reflect.DeepEqual(detail.State, original.State) || !reflect.DeepEqual(detail.Slices, original.Slices) {
		t.Fatalf("stop changed lifecycle artifacts: state=%#v slices=%#v", detail.State, detail.Slices)
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	freshDetail := detailFromFiles(files)
	fresh, err := NewPlanRecord(dir, freshDetail)
	if err != nil {
		t.Fatal(err)
	}
	evidence.StoppedAt = evidence.StoppedAt.Add(time.Minute)
	if err := fresh.RecordAutomaticReworkStop(evidence); err != nil {
		t.Fatalf("fresh-record semantic retry: %v", err)
	}
	if got := requireSingleEventType(t, freshDetail.Events, EventTypeReworkStopped); got != mutationID {
		t.Fatalf("fresh retry mutation_id = %q, want %q", got, mutationID)
	}
}

func TestPlanRecordAutomaticReworkStopRefreshesStaleDetail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reopen")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	detail := completedReopenDetail()
	detail.Dir = dir
	writeStartSliceArtifacts(t, dir, detail)
	stale := clonePlanDetail(detail)
	staleRecord, err := NewPlanRecord(dir, stale)
	if err != nil {
		t.Fatal(err)
	}
	current := clonePlanDetail(detail)
	currentRecord, err := NewPlanRecord(dir, current)
	if err != nil {
		t.Fatal(err)
	}
	verification := FinalVerification{Command: "make verify", CWD: "/repo", Result: "passed", VerifiedAt: editTime()}
	if err := currentRecord.RecordFinalVerification(verification); err != nil {
		t.Fatal(err)
	}
	if err := staleRecord.RecordAutomaticReworkStop(AutomaticReworkStop{Round: 1, Attempts: 1, Fingerprint: "finding-set", Reason: "automatic rework stalled", StoppedAt: editTime()}); err != nil {
		t.Fatal(err)
	}
	if stale.State.Plan.FinalVerification == nil || stale.State.Plan.FinalVerification.Command != "make verify" {
		t.Fatalf("stop erased authoritative state: %#v", stale.State.Plan.FinalVerification)
	}
}

func TestPlanRecordAutomaticReopenJournalsOrderedRoundEvidenceOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reopen")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	detail := completedReopenDetail()
	detail.Dir = dir
	original := clonePlanDetail(detail)
	writeStartSliceArtifacts(t, dir, detail)
	reopenedAt := editTime()
	newSlices := []Slice{newReopenSlice("002-fix", "Fix review finding", reopenedAt)}
	evidence := AutomaticReworkRound{Round: 1, Attempts: 1, MaxAttempts: 5, Fingerprint: "finding-set", ReopenedAt: reopenedAt}
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "remove"}
	record, err := newPlanRecord(journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.ReopenAutomatic(newSlices, evidence); err == nil || !strings.Contains(err.Error(), "injected remove failure") {
		t.Fatalf("first reopen error = %v, want journal removal failure", err)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("failed reopen published in-memory changes: got %#v want %#v", detail, original)
	}
	if err := record.ReopenAutomatic(newSlices, evidence); err != nil {
		t.Fatalf("same-record retry: %v", err)
	}
	assertAutomaticReopenEvents(t, detail.Events)

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	freshDetail := detailFromFiles(files)
	fresh, err := NewPlanRecord(dir, freshDetail)
	if err != nil {
		t.Fatal(err)
	}
	evidence.ReopenedAt = evidence.ReopenedAt.Add(time.Minute)
	if err := fresh.ReopenAutomatic(newSlices, evidence); err != nil {
		t.Fatalf("fresh-record semantic retry: %v", err)
	}
	assertAutomaticReopenEvents(t, freshDetail.Events)
}

func assertAutomaticReopenEvents(t *testing.T, events []Event) {
	t.Helper()
	owned := make([]Event, 0, 2)
	for _, event := range events {
		if event.Type == EventTypePlanReopened || event.Type == EventTypeReworkRound {
			owned = append(owned, event)
		}
	}
	if len(owned) != 2 || owned[0].Type != EventTypePlanReopened || owned[1].Type != EventTypeReworkRound {
		t.Fatalf("automatic reopen events = %#v", owned)
	}
	if owned[0].MutationID == "" || owned[1].MutationID != owned[0].MutationID {
		t.Fatalf("automatic reopen mutation IDs = %q, %q", owned[0].MutationID, owned[1].MutationID)
	}
	if owned[1].Round != 1 || owned[1].Attempts != 1 || owned[1].Fingerprint != "finding-set" || owned[1].Message != "Automatic rework round 1 (attempt 1 of 5)" {
		t.Fatalf("automatic round evidence = %#v", owned[1])
	}
}

func TestReopenCompletedPlanAddsPendingSlicesAndEvent(t *testing.T) {
	dir := t.TempDir()
	writeCompletedReopenPlan(t, dir)
	planDir := filepath.Join(dir, "reopen")
	completed := time.Date(2026, 5, 3, 23, 2, 0, 0, time.UTC)
	if err := AppendEvent(planDir, Event{Type: EventTypeSliceCompleted, Timestamp: completed, PlanID: "reopen", SliceID: "001-done", Message: "done"}); err != nil {
		t.Fatal(err)
	}
	repo := NewFileRepository(dir)
	detail, err := repo.GetPlan(context.Background(), "reopen")
	if err != nil {
		t.Fatal(err)
	}
	reopened := time.Date(2026, 5, 4, 1, 0, 0, 0, time.UTC)
	detail.State.Plan.FinalizationFailure = &FinalizationFailure{
		Phase: FinalizationFailurePhaseProposalRepair, Category: "proposal_invalid", ReviewBase: "base123", ReviewHead: "head123",
		FailedAt: reopened.Add(-time.Minute), RecoveryAction: "rerun_review",
	}
	if err := testRepoRecord(repo, detail).PersistState(); err != nil {
		t.Fatal(err)
	}
	newSlices := []Slice{
		newReopenSlice("002-fix", "Fix review finding", reopened),
		newReopenSlice("003-cover", "Cover review finding", reopened),
	}

	if err := testRepoRecord(repo, detail).Reopen(newSlices, reopened); err != nil {
		t.Fatal(err)
	}

	assertReopenedPlan(t, detail, reopened)
	if detail.State.Plan.FinalizationFailure != nil {
		t.Fatal("plan reopen retained finalization failure")
	}
	reloaded, err := repo.GetPlan(context.Background(), "reopen")
	if err != nil {
		t.Fatal(err)
	}
	assertReopenedPlan(t, reloaded, reopened)
}

func TestReopenNonCompletedPlanErrors(t *testing.T) {
	detail := editPlanDetail()
	original := clonePlanDetail(detail)

	_, err := Reopen(detail, []Slice{newReopenSlice("004-d", "D", editTime())}, editTime())
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "only reviewed plans can be reopened") {
		t.Fatalf("expected typed not-completed error, got %v", err)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("failed reopen changed detail:\n got: %#v\nwant: %#v", detail, original)
	}
}

func TestReopenRejectsDuplicateSliceIDs(t *testing.T) {
	detail := completedReopenDetail()

	_, err := Reopen(detail, []Slice{newReopenSlice("001-done", "Duplicate", editTime())}, editTime())
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "duplicate slice id 001-done") {
		t.Fatalf("expected duplicate slice error, got %v", err)
	}

	_, err = Reopen(detail, []Slice{newReopenSlice("002-a", "A", editTime()), newReopenSlice("002-a", "A again", editTime())}, editTime())
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "duplicate slice id 002-a") {
		t.Fatalf("expected duplicate new slice error, got %v", err)
	}
}

func TestEditRemoveSliceRejectsPendingDependents(t *testing.T) {
	detail := editPlanDetail()

	_, err := MarkSliceRemoved(detail, "001-a", editTime())
	if err == nil || !strings.Contains(err.Error(), "pending slices depend on it: 002-b") {
		t.Fatalf("expected dependency error, got %v", err)
	}
}

func TestEditRemoveSliceDeletesPendingSlice(t *testing.T) {
	detail := editPlanDetail()
	detail.Slices.Slices[1].DependsOn = nil

	event, err := MarkSliceRemoved(detail, "001-a", editTime())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(detail.State.Plan.PendingSlices, ",") != "002-b,003-c" {
		t.Fatalf("unexpected pending queue: %v", detail.State.Plan.PendingSlices)
	}
	if findSlice(detail, "001-a") != nil {
		t.Fatalf("removed slice still present: %#v", detail.Slices.Slices)
	}
	if event.Type != EventTypeSliceRemoved || event.SliceID != "001-a" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestEditSkipSlicePreservesAuditableRecord(t *testing.T) {
	detail := editPlanDetail()
	detail.Slices.Slices[1].DependsOn = nil

	event, err := MarkSliceSkipped(detail, "001-a", editTime())
	if err != nil {
		t.Fatal(err)
	}
	slice := findSlice(detail, "001-a")
	if slice == nil || slice.Status != StatusSkipped || slice.Timing.LastActivityAt == nil {
		t.Fatalf("unexpected skipped slice: %#v", slice)
	}
	if slices.Contains(detail.State.Plan.PendingSlices, "001-a") {
		t.Fatalf("skipped slice remains pending: %v", detail.State.Plan.PendingSlices)
	}
	if event.Type != EventTypeSliceSkipped || event.SliceID != "001-a" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestEditRejectsCompletedOrInProgressSlices(t *testing.T) {
	for _, status := range []string{StatusCompleted, StatusInProgress} {
		t.Run(status, func(t *testing.T) {
			detail := editPlanDetail()
			detail.Slices.Slices[0].Status = status

			_, err := MarkSliceSkipped(detail, "001-a", editTime())
			if err == nil || !strings.Contains(err.Error(), "only pending slices can be edited") {
				t.Fatalf("expected pending-only error, got %v", err)
			}
		})
	}
}

func TestEditReorderRejectsDependencyInvalidOrder(t *testing.T) {
	detail := editPlanDetail()

	_, err := MarkPendingSlicesReordered(detail, []string{"002-b", "001-a", "003-c"}, editTime())
	if err == nil || !strings.Contains(err.Error(), "before pending dependency 001-a") {
		t.Fatalf("expected dependency order error, got %v", err)
	}
}

func TestEditReorderPendingSlices(t *testing.T) {
	detail := editPlanDetail()

	event, err := MarkPendingSlicesReordered(detail, []string{"001-a", "003-c", "002-b"}, editTime())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(detail.State.Plan.PendingSlices, ",") != "001-a,003-c,002-b" {
		t.Fatalf("unexpected pending queue: %v", detail.State.Plan.PendingSlices)
	}
	if event.Type != EventTypeSlicesReordered {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestRepositoryWritesPlanEdits(t *testing.T) {
	dir := t.TempDir()
	writeEditPlan(t, dir)
	repo := NewFileRepository(dir)
	detail, err := repo.GetPlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}

	if err := testRepoRecord(repo, detail).SkipSlice("003-c", editTime()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repo.GetPlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	if findSlice(reloaded, "003-c").Status != StatusSkipped || slices.Contains(reloaded.State.Plan.PendingSlices, "003-c") {
		t.Fatalf("skip was not persisted: state=%v slice=%#v", reloaded.State.Plan.PendingSlices, findSlice(reloaded, "003-c"))
	}
	if len(reloaded.Events) != 1 || reloaded.Events[0].Type != EventTypeSliceSkipped {
		t.Fatalf("unexpected events: %#v", reloaded.Events)
	}
}

func assertReopenedPlan(t *testing.T, detail *PlanDetail, reopened time.Time) {
	t.Helper()
	if detail.State.Status != StatusInProgress || detail.State.Plan.CurrentSlice != nil || detail.State.Plan.Timing.CompletedAt != nil || !detail.State.UpdatedAt.Equal(reopened) {
		t.Fatalf("unexpected reopened state: %#v", detail.State)
	}
	if got := strings.Join(detail.State.Plan.CompletedSlices, ","); got != "001-done" {
		t.Fatalf("completed slices changed: %v", detail.State.Plan.CompletedSlices)
	}
	if got := strings.Join(detail.State.Plan.PendingSlices, ","); got != "002-fix,003-cover" {
		t.Fatalf("unexpected pending slices: %v", detail.State.Plan.PendingSlices)
	}
	completed := findSlice(detail, "001-done")
	if completed == nil || completed.Status != StatusCompleted || completed.Notes != "done before rework" {
		t.Fatalf("completed slice was not preserved: %#v", completed)
	}
	if findSlice(detail, "002-fix") == nil || findSlice(detail, "002-fix").Status != StatusPending || findSlice(detail, "003-cover") == nil || findSlice(detail, "003-cover").Status != StatusPending {
		t.Fatalf("new pending slices not appended: %#v", detail.Slices.Slices)
	}
	if len(detail.Events) != 2 || detail.Events[0].Type != EventTypeSliceCompleted || detail.Events[1].Type != EventTypePlanReopened || detail.Events[1].PlanID != "reopen" {
		t.Fatalf("unexpected reopen events: %#v", detail.Events)
	}
}

func completedReopenDetail() *PlanDetail {
	completed := time.Date(2026, 5, 3, 23, 2, 0, 0, time.UTC)
	return &PlanDetail{
		State: State{
			Schema:    "tao.plan.state.v1",
			Status:    StatusCompleted,
			CreatedAt: completed.Add(-2 * time.Minute),
			UpdatedAt: completed,
			Plan: PlanState{
				ID:              "reopen",
				Title:           "Reopen Plan",
				CompletedSlices: []string{"001-done"},
				Timing:          PlanTiming{StartedAt: new(completed), CompletedAt: new(completed), LastActivityAt: new(completed)},
			},
		},
		Slices: SlicesFile{
			Schema: "tao.plan.slices.v1",
			PlanID: "reopen",
			Execution: Execution{
				Mode: "serial",
			},
			Slices: []Slice{{
				ID:     "001-done",
				Title:  "Done",
				Status: StatusCompleted,
				Timing: SliceTiming{
					CreatedAt:       completed.Add(-2 * time.Minute),
					StartedAt:       new(completed),
					CompletedAt:     new(completed),
					UpdatedAt:       completed,
					LastActivityAt:  new(completed),
					DurationSeconds: new(int64),
				},
				Notes: "done before rework",
			}},
		},
	}
}

func newReopenSlice(id string, title string, now time.Time) Slice {
	return Slice{
		ID:            id,
		Title:         title,
		Status:        StatusPending,
		DependsOn:     []string{"001-done"},
		Timing:        SliceTiming{CreatedAt: now, UpdatedAt: now},
		Goal:          "Fix review finding",
		Context:       "Generated by rework",
		Tasks:         []string{"Apply the review finding"},
		ExpectedFiles: []string{"internal/plan/lifecycle.go"},
		Verification:  Verification{Commands: []string{"go test ./internal/plan"}, ManualChecks: []string{}},
	}
}

func writeCompletedReopenPlan(t *testing.T, root string) {
	t.Helper()
	writePlan(t, root, "reopen", `{
  "schema":"tao.plan.state.v1",
  "status":"completed",
  "created_at":"2026-05-03T23:00:00Z",
  "updated_at":"2026-05-03T23:02:00Z",
  "repo":{"name":"tao","root":"/repo","branch":"main"},
  "plan":{"id":"reopen","title":"Reopen Plan","current_slice":null,"completed_slices":["001-done"],"pending_slices":[],"timing":{"started_at":"2026-05-03T23:00:00Z","completed_at":"2026-05-03T23:02:00Z","last_activity_at":"2026-05-03T23:02:00Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"reopen","execution":{"mode":"serial","parallel_safe":false},
  "slices":[{"id":"001-done","title":"Done","status":"completed","depends_on":[],"timing":{"created_at":"2026-05-03T23:00:00Z","started_at":"2026-05-03T23:00:00Z","completed_at":"2026-05-03T23:02:00Z","updated_at":"2026-05-03T23:02:00Z","last_activity_at":"2026-05-03T23:02:00Z","duration_seconds":120},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]},"notes":"done before rework"}]
}`)
}

func editTime() time.Time {
	return time.Date(2026, 5, 26, 19, 30, 0, 0, time.UTC)
}

func editPlanDetail() *PlanDetail {
	created := time.Date(2026, 5, 26, 18, 0, 0, 0, time.UTC)
	return &PlanDetail{
		State: State{
			Schema:    "tao.plan.state.v1",
			Status:    StatusPlanned,
			CreatedAt: created,
			UpdatedAt: created,
			Plan: PlanState{
				ID:            "edit",
				Title:         "Edit Plan",
				PendingSlices: []string{"001-a", "002-b", "003-c"},
			},
		},
		Slices: SlicesFile{
			Schema: "tao.plan.slices.v1",
			PlanID: "edit",
			Slices: []Slice{
				{ID: "001-a", Title: "A", Status: StatusPending, Timing: SliceTiming{CreatedAt: created, UpdatedAt: created}},
				{ID: "002-b", Title: "B", Status: StatusPending, DependsOn: []string{"001-a"}, Timing: SliceTiming{CreatedAt: created, UpdatedAt: created}},
				{ID: "003-c", Title: "C", Status: StatusPending, Timing: SliceTiming{CreatedAt: created, UpdatedAt: created}},
			},
		},
	}
}

func writeEditPlan(t *testing.T, root string) {
	t.Helper()
	writePlan(t, root, "edit", `{
  "schema":"tao.plan.state.v1",
  "status":"planned",
  "created_at":"2026-05-26T18:00:00Z",
  "updated_at":"2026-05-26T18:00:00Z",
  "repo":{"name":"tao","root":"/repo","branch":"main"},
  "plan":{"id":"edit","title":"Edit Plan","current_slice":null,"completed_slices":[],"pending_slices":["001-a","002-b","003-c"],"timing":{"started_at":null,"completed_at":null,"last_activity_at":"2026-05-26T18:00:00Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"edit","execution":{"mode":"serial","parallel_safe":false},
  "slices":[
    {"id":"001-a","title":"A","status":"pending","depends_on":[],"timing":{"created_at":"2026-05-26T18:00:00Z","started_at":null,"completed_at":null,"updated_at":"2026-05-26T18:00:00Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}},
    {"id":"002-b","title":"B","status":"pending","depends_on":["001-a"],"timing":{"created_at":"2026-05-26T18:00:00Z","started_at":null,"completed_at":null,"updated_at":"2026-05-26T18:00:00Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}},
    {"id":"003-c","title":"C","status":"pending","depends_on":[],"timing":{"created_at":"2026-05-26T18:00:00Z","started_at":null,"completed_at":null,"updated_at":"2026-05-26T18:00:00Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}}
  ]
}`)
}
