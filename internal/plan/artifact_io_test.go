package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	slices0 "slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadEventsWarnsAndSkipsMalformedOrOversizedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	valid := `{"type":"plan_created","timestamp":"2026-05-03T23:31:31Z","plan_id":"plan-a","message":"created"}`
	oversized := strings.Repeat("x", maxEventJSONLLineBytes+1)
	content := "not json\n" + oversized + "\n" + valid
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, warnings, err := readEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "plan_created" {
		t.Fatalf("expected one valid event after skipped bad lines, got %#v", events)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"events.jsonl line 1", "events.jsonl line 2 exceeds"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected warning %q, got %v", want, warnings)
		}
	}
}

func TestStartSliceWritesStateSlicesAndEvent(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)

	if err := testRecord(dir, detail).StartSlice("001-a", now); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if state.Status != StatusInProgress || state.Plan.CurrentSlice == nil || *state.Plan.CurrentSlice != "001-a" {
		t.Fatalf("unexpected state after start: %#v", state)
	}
	if state.Plan.Timing.StartedAt == nil || !state.Plan.Timing.StartedAt.Equal(now) || state.Plan.Timing.LastActivityAt == nil || !state.Plan.Timing.LastActivityAt.Equal(now) {
		t.Fatalf("unexpected plan timing after start: %#v", state.Plan.Timing)
	}

	slices := readSlicesFile(t, dir)
	if got := slices.Slices[0]; got.Status != StatusInProgress || got.Timing.StartedAt == nil || !got.Timing.StartedAt.Equal(now) || got.Timing.LastActivityAt == nil || !got.Timing.LastActivityAt.Equal(now) {
		t.Fatalf("unexpected slice after start: %#v", got)
	}
	events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 || len(events) != 1 {
		t.Fatalf("read started event: events=%#v warnings=%v err=%v", events, warnings, err)
	}
	if event := events[0]; event.Type != EventTypeSliceStarted || event.SliceID != "001-a" || event.MutationID == "" {
		t.Fatalf("unexpected started event %#v", event)
	}
}

func TestStartSliceDoesNotDuplicateStartedEvent(t *testing.T) {
	dir := t.TempDir()
	first := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	second := first.Add(time.Minute)
	detail := startSliceDetail(dir)
	detail.Events = []Event{{Type: EventTypeSliceStarted, Timestamp: first, PlanID: "plan-a", SliceID: "001-a", Message: "Work started on slice"}}
	detail.State.Plan.CurrentSlice = &detail.Slices.Slices[0].ID
	detail.State.Plan.Timing.StartedAt = &first
	detail.Slices.Slices[0].Status = StatusInProgress
	detail.Slices.Slices[0].Timing.StartedAt = &first
	writeStartSliceArtifacts(t, dir, detail)
	if err := AppendEvent(dir, detail.Events[0]); err != nil {
		t.Fatal(err)
	}

	if err := testRecord(dir, detail).StartSlice("001-a", second); err != nil {
		t.Fatal(err)
	}

	if got := readEventsFile(t, dir); got != "{\"type\":\"slice_started\",\"timestamp\":\"2026-05-03T23:31:31Z\",\"plan_id\":\"plan-a\",\"slice_id\":\"001-a\",\"message\":\"Work started on slice\"}\n" {
		t.Fatalf("unexpected duplicate events.jsonl %q", got)
	}
	slices := readSlicesFile(t, dir)
	if !slices.Slices[0].Timing.StartedAt.Equal(first) || !slices.Slices[0].Timing.LastActivityAt.Equal(second) {
		t.Fatalf("unexpected retry timing: %#v", slices.Slices[0].Timing)
	}
}

func TestStartSlicePreservesUnknownJSONFields(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	detail := startSliceDetail(dir)
	stateJSON := `{"schema":"tao.plan.state.v1","status":"planned","created_at":"2026-05-03T23:00:00Z","updated_at":"2026-05-03T23:00:00Z","repo":{"name":"","root":"","branch":""},"plan":{"id":"plan-a","title":"Plan A","change_type":"feat","current_slice":null,"completed_slices":[],"pending_slices":["001-a"],"timing":{"started_at":null,"completed_at":null,"last_activity_at":null},"custom_plan_field":"keep"},"global_invariants":[],"open_questions":[],"custom_state_field":"keep"}`
	slicesJSON := `{"schema":"tao.plan.slices.v1","plan_id":"plan-a","execution":{"mode":"","parallel_safe":false},"slices":[{"id":"001-a","title":"A","status":"pending","depends_on":[],"timing":{"created_at":"2026-05-03T23:00:00Z","started_at":null,"completed_at":null,"updated_at":"2026-05-03T23:00:00Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]},"custom_slice_field":"keep"}],"custom_slices_field":"keep"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), []byte(slicesJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := testRecord(dir, detail).StartSlice("001-a", now); err != nil {
		t.Fatal(err)
	}

	var state map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &state)
	planState := state["plan"].(map[string]any)
	if state["custom_state_field"] != "keep" || planState["custom_plan_field"] != "keep" || planState["change_type"] != "feat" {
		t.Fatalf("expected change type and custom state fields to be preserved: %#v", state)
	}
	var slices map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &slices)
	if slices["custom_slices_field"] != "keep" || slices["slices"].([]any)[0].(map[string]any)["custom_slice_field"] != "keep" {
		t.Fatalf("expected custom slice fields to be preserved: %#v", slices)
	}
}

func TestPlanRecordLifecycleWritePreservesDecisionMetadataAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	detail := startSliceDetail(dir)
	detail.State.Plan.Decision = &Decision{
		Problem: "Plans cannot expose their rationale.", WhyNow: "Planning views need rationale.", ExpectedBenefit: "Plans become comparable.",
		Readiness: DecisionReadinessReady, SuccessCriteria: []string{"Rationale survives lifecycle writes."},
		Disposition: DecisionDispositionReady, DispositionReason: "The model is bounded.",
		Priority: Priority{Level: PriorityOverallLevelMust, Impact: PriorityLevelHigh, Urgency: PriorityLevelMedium, Effort: PriorityEffortSmall, Risk: PriorityLevelLow, Confidence: PriorityLevelHigh, Rationale: "High value for low effort."},
	}
	detail.State.Plan.Sequence = &Sequence{Position: 1, Total: 2, Relationships: []PlanRelation{{PlanID: "plan-b", Type: PlanRelationBefore, Reason: "Plan B consumes this model."}}}
	writeStartSliceArtifacts(t, dir, detail)

	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	planObject := raw["plan"].(map[string]any)
	decision := planObject["decision"].(map[string]any)
	decision["custom_decision_field"] = "keep"
	relationship := planObject["sequence"].(map[string]any)["relationships"].([]any)[0].(map[string]any)
	relationship["id"] = "extension-id"
	relationship["custom_relationship_field"] = "keep"
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded := detailFromFiles(files)
	record, err := NewPlanRecord(dir, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice("001-a", now); err != nil {
		t.Fatal(err)
	}

	persisted, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Plan.Decision == nil || persisted.Plan.Decision.Problem != detail.State.Plan.Decision.Problem || persisted.Plan.Decision.Priority.Effort != PriorityEffortSmall || persisted.Plan.Decision.Priority.Confidence != PriorityLevelHigh || persisted.Plan.Decision.ExpectedBenefit != detail.State.Plan.Decision.ExpectedBenefit || persisted.Plan.Sequence == nil || len(persisted.Plan.Sequence.Relationships) != 1 {
		t.Fatalf("decision metadata did not survive lifecycle write: %+v", persisted.Plan)
	}
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	planObject = raw["plan"].(map[string]any)
	decision = planObject["decision"].(map[string]any)
	if decision["custom_decision_field"] != "keep" {
		t.Fatalf("unknown decision field was lost: %#v", decision)
	}
	relationship = planObject["sequence"].(map[string]any)["relationships"].([]any)[0].(map[string]any)
	if relationship["id"] != "extension-id" || relationship["custom_relationship_field"] != "keep" {
		t.Fatalf("unknown relationship fields were lost: %#v", relationship)
	}
}

func TestAutomaticReworkOperationsPreserveUnknownArtifactFields(t *testing.T) {
	dir := t.TempDir()
	detail := completedReopenDetail()
	detail.Dir = dir
	writeStartSliceArtifacts(t, dir, detail)
	addUnknownPlanField(t, dir, "custom_rework_state", "keep")
	addUnknownSlicesField(t, dir, "custom_rework_slices", "keep")

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded := detailFromFiles(files)
	record, err := NewPlanRecord(dir, loaded)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 20, 0, 0, 0, time.UTC)
	if err := record.RecordAutomaticReworkStop(AutomaticReworkStop{Round: 0, Attempts: 0, Fingerprint: "finding-set", Reason: "automatic rework stopped", StoppedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := record.ReopenAutomatic(
		[]Slice{newReopenSlice("002-fix", "Fix finding", now)},
		AutomaticReworkRound{Round: 1, Attempts: 1, MaxAttempts: 5, Fingerprint: "finding-set", ReopenedAt: now},
	); err != nil {
		t.Fatal(err)
	}

	var state map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &state)
	if state["plan"].(map[string]any)["custom_rework_state"] != "keep" {
		t.Fatalf("unknown state field was lost: %#v", state)
	}
	var slicesArtifact map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &slicesArtifact)
	if slicesArtifact["custom_rework_slices"] != "keep" {
		t.Fatalf("unknown slices field was lost: %#v", slicesArtifact)
	}
}

func TestPlanRecordFinalVerificationPreservesUnknownStateFields(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	stateJSON := `{"schema":"tao.plan.state.v1","status":"planned","created_at":"2026-05-03T23:00:00Z","updated_at":"2026-05-03T23:00:00Z","repo":{"name":"","root":"","branch":""},"plan":{"id":"plan-a","title":"Plan A","current_slice":null,"completed_slices":[],"pending_slices":["001-a"],"last_run_commit_policy":"","last_run_starting_dirty":[],"timing":{"started_at":null,"completed_at":null,"last_activity_at":null},"custom_plan_field":"keep"},"global_invariants":[],"open_questions":[],"custom_state_field":"keep"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	verification := FinalVerification{
		Command: "make verify", CWD: "/repo", Result: "passed", VerifiedAt: time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC),
	}

	if err := testRecord(dir, detail).RecordFinalVerification(verification); err != nil {
		t.Fatal(err)
	}

	var state map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &state)
	planState := state["plan"].(map[string]any)
	if state["custom_state_field"] != "keep" || planState["custom_plan_field"] != "keep" {
		t.Fatalf("expected custom state fields to be preserved: %#v", state)
	}
	persisted := planState["final_verification"].(map[string]any)
	if persisted["command"] != verification.Command || persisted["result"] != verification.Result {
		t.Fatalf("unexpected final verification: %#v", persisted)
	}
}

func TestWriteStateAndSlicesUseSameDirectoryAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)

	if err := writeState(dir, detail.State); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(dir, detail.Slices); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"state.json", "slices.json"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected %s permissions 0600, got %o", name, info.Mode().Perm())
		}
		matches, err := filepath.Glob(filepath.Join(dir, "."+name+".tmp-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("expected no temporary files after writing %s, got %v", name, matches)
		}
	}
}

func TestReviewArtifactRoundTrip(t *testing.T) {
	dir := t.TempDir()
	content := "# Plan Review\n\nVerdict: pass\n\nNo findings.\n"

	if err := WriteReviewArtifact(dir, content); err != nil {
		t.Fatal(err)
	}

	artifact, warnings := readReviewArtifact(dir)
	if len(warnings) != 0 {
		t.Fatalf("unexpected review warnings: %v", warnings)
	}
	if artifact.Path != filepath.Join(dir, ReviewFile) || artifact.Content != content {
		t.Fatalf("unexpected review artifact: %+v", artifact)
	}
	info, err := os.Stat(filepath.Join(dir, ReviewFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected review artifact permissions 0600, got %o", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(dir, "."+ReviewFile+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no temporary review artifacts, got %v", matches)
	}

	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))
	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files.review.Path != filepath.Join(dir, ReviewFile) || files.review.Content != content {
		t.Fatalf("loadPlanFiles did not populate review artifact: %+v", files.review)
	}
	detail := detailFromFiles(files)
	if detail.Review.Path != filepath.Join(dir, ReviewFile) || detail.Review.Content != content {
		t.Fatalf("detailFromFiles did not populate review artifact: %+v", detail.Review)
	}

	missing, missingWarnings := readReviewArtifact(t.TempDir())
	if len(missingWarnings) != 0 || missing.Path != "" || missing.Content != "" {
		t.Fatalf("missing review artifact should be tolerated, got artifact=%+v warnings=%v", missing, missingWarnings)
	}
}

func TestPlanReviewStatePersistence(t *testing.T) {
	dir := t.TempDir()
	reviewedAt := time.Date(2026, 6, 28, 7, 1, 2, 0, time.UTC)
	state := startSliceDetail(dir).State
	state.Plan.Review = &PlanReview{Verdict: "pass", Summary: "ready to merge", FindingsCount: 2, CommitMessage: &ReviewCommitMessage{Subject: "feat(review): persist approved commit proposals", Body: "What:\nPersist the proposal.\n\nWhy:\nReuse reviewed context."}, Base: "base123", Head: "head456", Agent: "pi", ReviewedAt: reviewedAt}

	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}

	got, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan.Review == nil {
		t.Fatal("expected persisted plan review metadata")
	}
	if !reflect.DeepEqual(got.Plan.Review, state.Plan.Review) {
		t.Fatalf("unexpected persisted review:\n got: %+v\nwant: %+v", got.Plan.Review, state.Plan.Review)
	}

	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	planObject := raw["plan"].(map[string]any)
	reviewObject, ok := planObject["review"].(map[string]any)
	commitMessage, messageOK := reviewObject["commit_message"].(map[string]any)
	if !ok || !messageOK || commitMessage["subject"] != "feat(review): persist approved commit proposals" || reviewObject["verdict"] != "pass" || reviewObject["findings_count"] != float64(2) || reviewObject["reviewed_at"] != reviewedAt.Format(time.RFC3339) {
		t.Fatalf("state.json did not persist review metadata: %#v", planObject["review"])
	}
}

func TestWriteStateAndSlicesPreserveUnknownJSONFields(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	stateJSON := `{"schema":"tao.plan.state.v1","status":"planned","created_at":"2026-05-03T23:00:00Z","updated_at":"2026-05-03T23:00:00Z","repo":{"name":"","root":"","branch":""},"plan":{"id":"plan-a","title":"Plan A","current_slice":null,"completed_slices":[],"pending_slices":["001-a"],"timing":{"started_at":null,"completed_at":null,"last_activity_at":null},"custom_plan_field":"keep"},"global_invariants":[],"open_questions":[],"custom_state_field":"keep"}`
	slicesJSON := `{"schema":"tao.plan.slices.v1","plan_id":"plan-a","execution":{"mode":"","parallel_safe":false},"slices":[{"id":"001-a","title":"A","status":"pending","depends_on":[],"timing":{"created_at":"2026-05-03T23:00:00Z","started_at":null,"completed_at":null,"updated_at":"2026-05-03T23:00:00Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]},"custom_slice_field":"keep"}],"custom_slices_field":"keep"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), []byte(slicesJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	preparedState, err := prepareJSON(filepath.Join(dir, "state.json"), detail.State, artifactJSONChanges{})
	if err != nil {
		t.Fatal(err)
	}
	preparedSlices, err := prepareJSON(filepath.Join(dir, "slices.json"), detail.Slices, artifactJSONChanges{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeState(dir, detail.State); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(dir, detail.Slices); err != nil {
		t.Fatal(err)
	}
	persistedState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // Test path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	persistedSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // Test path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persistedState, preparedState) || !bytes.Equal(persistedSlices, preparedSlices) {
		t.Fatal("write path did not install the exact prepared artifact bytes")
	}

	var state map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &state)
	if state["custom_state_field"] != "keep" || state["plan"].(map[string]any)["custom_plan_field"] != "keep" {
		t.Fatalf("expected custom state fields to be preserved: %#v", state)
	}
	var slices map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &slices)
	if slices["custom_slices_field"] != "keep" || slices["slices"].([]any)[0].(map[string]any)["custom_slice_field"] != "keep" {
		t.Fatalf("expected custom slice fields to be preserved: %#v", slices)
	}
}

func TestArtifactChangeSetLowersDeclaredIntentAndPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	existing := `{"schema":"tao.plan.state.v1","status":"in_review","created_at":"2026-05-03T23:00:00Z","updated_at":"2026-05-03T23:00:00Z","repo":{"name":"","root":"","branch":""},"plan":{"id":"plan-a","title":"Plan A","current_slice":null,"completed_slices":[],"pending_slices":[],"timing":{"started_at":null,"completed_at":null,"last_activity_at":null},"review":{"status":"completed","verdict":"approve","summary":"old","findings_count":1,"findings":[{"message":"old"}],"commit_message":{"subject":"fix(plan): old","body":"old"},"base":"base","head":"head","agent":"pi","reviewed_at":"2026-05-03T23:00:00Z","unknown_review":"keep"},"unknown_plan":"keep"},"global_invariants":[],"open_questions":[],"unknown_state":"keep"}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	detail := startSliceDetail(dir)
	detail.State.Plan.Review = nil
	preserved, err := prepareJSON(path, detail.State, stateJSONChanges(NewArtifactChangeSet(detail)))
	if err != nil {
		t.Fatal(err)
	}
	var preservedRoot map[string]any
	if err := json.Unmarshal(preserved, &preservedRoot); err != nil {
		t.Fatal(err)
	}
	preservedPlan := preservedRoot["plan"].(map[string]any)
	if preservedPlan["review"].(map[string]any)["summary"] != "old" || preservedPlan["unknown_plan"] != "keep" || preservedRoot["unknown_state"] != "keep" {
		t.Fatalf("undeclared review or unknown siblings were not preserved: %#v", preservedRoot)
	}

	clearChanges := NewArtifactChangeSet(detail)
	clearChanges.ClearPlanReview()
	cleared, err := prepareJSON(path, detail.State, stateJSONChanges(clearChanges))
	if err != nil {
		t.Fatal(err)
	}
	var clearedRoot map[string]any
	if err := json.Unmarshal(cleared, &clearedRoot); err != nil {
		t.Fatal(err)
	}
	clearedPlan := clearedRoot["plan"].(map[string]any)
	if value, ok := clearedPlan["review"]; !ok || value != nil {
		t.Fatalf("declared review clear did not emit null: %#v", clearedPlan)
	}
	if clearedPlan["unknown_plan"] != "keep" || clearedRoot["unknown_state"] != "keep" {
		t.Fatalf("declared clear erased unknown siblings: %#v", clearedRoot)
	}

	replacement := PlanReview{ReviewedAt: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)}
	replaceChanges := NewArtifactChangeSet(detail)
	if err := replaceChanges.ReplacePlanReview(replacement); err != nil {
		t.Fatal(err)
	}
	replaced, err := prepareJSON(path, detail.State, stateJSONChanges(replaceChanges))
	if err != nil {
		t.Fatal(err)
	}
	var replacedRoot map[string]any
	if err := json.Unmarshal(replaced, &replacedRoot); err != nil {
		t.Fatal(err)
	}
	review := replacedRoot["plan"].(map[string]any)["review"].(map[string]any)
	if review["base"] != "" || review["commit_message"] != nil {
		t.Fatalf("review replacement did not lower explicit empty string/null: %#v", review)
	}
	if findings, ok := review["findings"].([]any); !ok || len(findings) != 0 {
		t.Fatalf("review replacement did not lower findings as []: %#v", review["findings"])
	}
	if review["unknown_review"] != "keep" || replacedRoot["plan"].(map[string]any)["unknown_plan"] != "keep" {
		t.Fatalf("review replacement erased unknown fields: %#v", replacedRoot)
	}
}

func TestPersistStateChangesRebasesReviewReplacementAndPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	state := startSliceDetail(dir).State
	state.Plan.Review = &PlanReview{
		Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "old summary", FindingsCount: 1,
		Findings:      []ReviewFinding{{Message: "old finding"}},
		CommitMessage: &ReviewCommitMessage{Subject: "fix(plan): old", Body: "old body"},
		Base:          "old-base", Head: "old-head", Agent: "old-agent",
		ReviewedAt: time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC),
	}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	slices := startSliceDetail(dir).Slices
	if err := writeSlices(dir, slices); err != nil {
		t.Fatal(err)
	}
	detail := &PlanDetail{Dir: dir, State: cloneState(state), Slices: slices}
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	planObject := raw["plan"].(map[string]any)
	planObject["title"] = "concurrent title"
	planObject["unknown_plan"] = "keep"
	planObject["review"].(map[string]any)["unknown_review"] = "keep"
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	replacement := PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment}
	changes := NewArtifactChangeSet(detail)
	if err := changes.ReplacePlanReview(replacement); err != nil {
		t.Fatal(err)
	}
	if err := record.PersistStateChanges(changes); err != nil {
		t.Fatal(err)
	}

	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	planObject = raw["plan"].(map[string]any)
	review := planObject["review"].(map[string]any)
	if planObject["title"] != "concurrent title" || planObject["unknown_plan"] != "keep" || review["unknown_review"] != "keep" {
		t.Fatalf("review replacement rebase erased concurrent or unknown fields: %#v", planObject)
	}
	if review["status"] != ReviewStatusCompleted || review["verdict"] != ReviewVerdictComment || review["summary"] != "" || review["findings_count"] != float64(0) || review["base"] != "" || review["head"] != "" || review["agent"] != "" {
		t.Fatalf("review replacement did not replace every known scalar field: %#v", review)
	}
	if findings, ok := review["findings"].([]any); !ok || len(findings) != 0 {
		t.Fatalf("review replacement did not persist findings: []: %#v", review)
	}
	if message, exists := review["commit_message"]; !exists || message != nil {
		t.Fatalf("review replacement did not persist commit_message: null: %#v", review)
	}
	if _, exists := review["reviewed_at"]; !exists {
		t.Fatalf("review replacement omitted reviewed_at: %#v", review)
	}
}

func TestArtifactChangeSetThreadsThroughArtifactMutation(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	detail.State.Plan.Review = &PlanReview{Summary: "old", Findings: []ReviewFinding{}, ReviewedAt: time.Date(2026, 5, 3, 23, 0, 0, 0, time.UTC)}
	if err := writeState(dir, detail.State); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(dir, detail.Slices); err != nil {
		t.Fatal(err)
	}

	err := applyArtifactMutation(fileArtifactStore{}, dir, detail, func(working *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(working, func(changes *ArtifactChangeSet) ([]Event, error) {
			changes.ClearPlanReview()
			return nil, changes.ClearSliceBlockerNote("001-a")
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &state)
	if value, ok := state["plan"].(map[string]any)["review"]; !ok || value != nil {
		t.Fatalf("artifact mutation did not lower state clear: %#v", state)
	}
	var slicesRoot map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &slicesRoot)
	slice := slicesRoot["slices"].([]any)[0].(map[string]any)
	if value, ok := slice["blocker_note"]; !ok || value != "" {
		t.Fatalf("artifact mutation did not lower slice clear: %#v", slice)
	}
}

func TestArtifactChangeSetThreadsThroughSlicesUpdate(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	detail.Slices.Slices[0].BlockerNote = "old blocker"
	if err := writeSlices(dir, detail.Slices); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
	raw["slices"].([]any)[0].(map[string]any)["unknown_slice"] = "keep"
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := applySlicesArtifactUpdate(fileArtifactStore{}, dir, detail, func(_ *PlanDetail, changes *ArtifactChangeSet) error {
		return changes.ClearSliceBlockerNote("001-a")
	}); err != nil {
		t.Fatal(err)
	}
	readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
	slice := raw["slices"].([]any)[0].(map[string]any)
	if slice["blocker_note"] != "" || slice["unknown_slice"] != "keep" {
		t.Fatalf("slices update did not clear blocker note and preserve unknown sibling: %#v", slice)
	}
}

func TestSliceBlockerClearAppliesAfterArtifactSliceRebase(t *testing.T) {
	dir := t.TempDir()
	baseline := startSliceDetail(dir)
	baseline.State.Status = StatusBlocked
	baseline.Slices.Slices[0].Status = StatusBlocked
	baseline.Slices.Slices[0].BlockerNote = "original blocker"
	writeStartSliceArtifacts(t, dir, baseline)

	stale := clonePlanDetail(baseline)
	stale.Slices.Slices[0].Title = "caller title"
	settled := clonePlanDetail(baseline)
	settled.Slices.Slices[0].BlockerNote = "newer blocker"
	settled.Slices.Slices[0].Notes = "concurrent note"
	if err := writeSlices(dir, settled.Slices); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
	raw["slices"].([]any)[0].(map[string]any)["unknown_slice"] = "keep"
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 3, 23, 45, 0, 0, time.UTC)
	if err := applyArtifactMutationPreservingDetail(fileArtifactStore{}, dir, stale, baseline, func(working *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(working, func(changes *ArtifactChangeSet) ([]Event, error) {
			return nil, markBlockedContinued(working, changes, now)
		})
	}); err != nil {
		t.Fatal(err)
	}

	readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
	sliceObject := raw["slices"].([]any)[0].(map[string]any)
	if sliceObject["title"] != "caller title" || sliceObject["notes"] != "concurrent note" {
		t.Fatalf("slice rebase lost caller or settled fields: %#v", sliceObject)
	}
	if value, exists := sliceObject["blocker_note"]; !exists || value != "" || sliceObject["unknown_slice"] != "keep" {
		t.Fatalf("rebased blocker clear or unknown sibling was lost: %#v", sliceObject)
	}
}

func TestArtifactChangeSetPreparedBytesMatchAdapterStore(t *testing.T) {
	seed := func(t *testing.T, dir string) *PlanDetail {
		t.Helper()
		detail := startSliceDetail(dir)
		detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "old", Findings: []ReviewFinding{}, ReviewedAt: time.Date(2026, 5, 3, 23, 0, 0, 0, time.UTC)}
		if err := writeState(dir, detail.State); err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
		raw["plan"].(map[string]any)["unknown_plan"] = "keep"
		payload, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
		return detail
	}

	fileDir := t.TempDir()
	fileDetail := seed(t, fileDir)
	fileRecord, err := NewPlanRecord(fileDir, fileDetail)
	if err != nil {
		t.Fatal(err)
	}
	fileChanges := NewArtifactChangeSet(fileDetail)
	fileChanges.ClearPlanReview()
	if err := fileRecord.PersistStateChanges(fileChanges); err != nil {
		t.Fatal(err)
	}
	filePayload, err := os.ReadFile(filepath.Join(fileDir, "state.json")) //nolint:gosec // Test path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	adapterDir := t.TempDir()
	adapterDetail := seed(t, adapterDir)
	adapter := &payloadArtifactStore{}
	adapterRecord, err := NewPlanRecordWithStore(adapter, adapterDir, adapterDetail)
	if err != nil {
		t.Fatal(err)
	}
	adapterChanges := NewArtifactChangeSet(adapterDetail)
	adapterChanges.ClearPlanReview()
	if err := adapterRecord.PersistStateChanges(adapterChanges); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(adapter.statePayload, filePayload) {
		t.Fatalf("adapter payload differs from file payload:\nadapter: %s\nfile: %s", adapter.statePayload, filePayload)
	}
}

type payloadArtifactStore struct {
	statePayload  []byte
	slicesPayload []byte
	events        []Event
}

func (s *payloadArtifactStore) WriteState(_ string, payload []byte) error {
	s.statePayload = append([]byte(nil), payload...)
	return nil
}

func (s *payloadArtifactStore) WriteSlices(_ string, payload []byte) error {
	s.slicesPayload = append([]byte(nil), payload...)
	return nil
}

func (s *payloadArtifactStore) AppendEvent(_ string, event Event) error {
	s.events = append(s.events, event)
	return nil
}

func TestCompleteSliceWritesStateSlicesAndEvent(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	completed := started.Add(2 * time.Minute)
	detail := startSliceDetail(dir)
	detail.State.Status = StatusInProgress
	detail.State.Plan.CurrentSlice = &detail.Slices.Slices[0].ID
	detail.State.Plan.Timing.StartedAt = &started
	detail.Slices.Slices[0].Status = StatusInProgress
	detail.Slices.Slices[0].Timing.StartedAt = &started
	writeStartSliceArtifacts(t, dir, detail)

	results := []VerificationRun{{Command: "go test ./internal/plan", CWD: "/repo", Result: "passed", Details: "ok"}}
	if err := testRecord(dir, detail).CompleteSlice("001-a", "implemented completion", results, completed); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if state.Status != StatusInReview || state.Plan.CurrentSlice != nil || len(state.Plan.PendingSlices) != 0 || len(state.Plan.CompletedSlices) != 1 || state.Plan.CompletedSlices[0] != "001-a" {
		t.Fatalf("unexpected state after complete: %#v", state)
	}
	if state.Plan.Timing.CompletedAt == nil || !state.Plan.Timing.CompletedAt.Equal(completed) {
		t.Fatalf("unexpected plan timing after complete: %#v", state.Plan.Timing)
	}
	slices := readSlicesFile(t, dir)
	got := slices.Slices[0]
	if got.Status != StatusCompleted || got.Timing.CompletedAt == nil || !got.Timing.CompletedAt.Equal(completed) || got.Timing.DurationSeconds == nil || *got.Timing.DurationSeconds != 120 {
		t.Fatalf("unexpected slice after complete: %#v", got)
	}
	if got.Notes != "implemented completion" || len(got.VerificationResults) != 1 || got.VerificationResults[0].Command != results[0].Command {
		t.Fatalf("unexpected notes/results after complete: %#v", got)
	}
	if got := readEventsFile(t, dir); !strings.Contains(got, `"type":"slice_completed"`) || !strings.Contains(got, `"duration_seconds":120`) {
		t.Fatalf("unexpected events.jsonl %q", got)
	}
}

func TestCurrentSliceLifecycleClearWritersPersistNullAndPreserveUnknownPlanFields(t *testing.T) {
	started := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	transitioned := started.Add(time.Minute)
	tests := []struct {
		name  string
		setup func(string) *PlanDetail
		apply func(*PlanRecord) error
	}{
		{
			name: "slice completion with pending continuation",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusInProgress
				detail.State.Plan.CurrentSlice = new("001-a")
				detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, "002-b")
				detail.State.Plan.Timing.StartedAt = &started
				detail.Slices.Slices[0].Status = StatusInProgress
				detail.Slices.Slices[0].Timing.StartedAt = &started
				detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusPending})
				return detail
			},
			apply: func(record *PlanRecord) error {
				return record.CompleteSlice("001-a", "done", nil, transitioned)
			},
		},
		{
			name: "slice completion with outcome",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusInProgress
				detail.State.Plan.CurrentSlice = new("001-a")
				detail.State.Plan.Timing.StartedAt = &started
				detail.Slices.Slices[0].Status = StatusInProgress
				detail.Slices.Slices[0].Timing.StartedAt = &started
				detail.Slices.Slices[0].CommitIntent = &SliceCommitIntent{Policy: "slice", CreatedAt: started}
				return detail
			},
			apply: func(record *PlanRecord) error {
				outcome := SliceCompletionOutcome{Outcome: SliceCompletionNoChanges}
				return record.CompleteSliceWithOutcome("001-a", "done", nil, outcome, transitioned)
			},
		},
		{
			name: "plan reopen",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusReviewed
				detail.State.Plan.CurrentSlice = new("001-a")
				detail.State.Plan.PendingSlices = nil
				detail.State.Plan.CompletedSlices = []string{"001-a"}
				detail.State.Plan.Timing.CompletedAt = &started
				detail.Slices.Slices[0].Status = StatusCompleted
				return detail
			},
			apply: func(record *PlanRecord) error {
				return record.Reopen([]Slice{{ID: "002-fix", Title: "Fix", Status: StatusPending}}, transitioned)
			},
		},
		{
			name: "forced plan reopen",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusInReview
				detail.State.Plan.CurrentSlice = new("001-a")
				detail.State.Plan.PendingSlices = nil
				detail.State.Plan.CompletedSlices = []string{"001-a"}
				detail.State.Plan.Timing.CompletedAt = &started
				detail.Slices.Slices[0].Status = StatusCompleted
				return detail
			},
			apply: func(record *PlanRecord) error {
				return record.ReopenForced([]Slice{{ID: "002-fix", Title: "Fix", Status: StatusPending}}, transitioned)
			},
		},
		{
			name: "plan edit removes stale current slice",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusInProgress
				detail.State.Plan.CurrentSlice = new("001-a")
				return detail
			},
			apply: func(record *PlanRecord) error {
				return record.RemoveSlice("001-a", transitioned)
			},
		},
		{
			name: "plan edit skips stale current slice",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusInProgress
				detail.State.Plan.CurrentSlice = new("001-a")
				return detail
			},
			apply: func(record *PlanRecord) error {
				return record.SkipSlice("001-a", transitioned)
			},
		},
		{
			name: "plan edit reorders pending slices with stale current slice",
			setup: func(dir string) *PlanDetail {
				detail := startSliceDetail(dir)
				detail.State.Status = StatusInProgress
				detail.State.Plan.CurrentSlice = new("stale")
				detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, "002-b")
				detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusPending})
				return detail
			},
			apply: func(record *PlanRecord) error {
				return record.ReorderPendingSlices([]string{"002-b", "001-a"}, transitioned)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			detail := tt.setup(dir)
			writeStartSliceArtifacts(t, dir, detail)
			addUnknownPlanField(t, dir, "unknown_current_slice_sibling", "keep")

			if err := tt.apply(testRecord(dir, detail)); err != nil {
				t.Fatal(err)
			}

			var raw map[string]any
			readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
			planObject := raw["plan"].(map[string]any)
			if value, exists := planObject["current_slice"]; !exists || value != nil {
				t.Fatalf("current_slice clear = %#v, exists=%t; want explicit null", value, exists)
			}
			if planObject["unknown_current_slice_sibling"] != "keep" {
				t.Fatalf("current_slice clear erased unknown plan sibling: %#v", planObject)
			}
		})
	}
}

func TestArtifactMutationWasRecoveredMatchesCurrentSliceClear(t *testing.T) {
	started := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	completed := started.Add(time.Minute)
	stale := startSliceDetail("")
	stale.State.Status = StatusInProgress
	stale.State.Plan.CurrentSlice = new("001-a")
	stale.Slices.Slices[0].Status = StatusInProgress
	stale.Slices.Slices[0].Timing.StartedAt = &started

	requested, err := completeSliceMutation("001-a", "done", nil, completed)(clonePlanDetail(stale))
	if err != nil {
		t.Fatal(err)
	}
	replayed := clonePlanDetail(stale)
	replayed.State = requested.State
	replayed.Slices = requested.Slices
	for _, event := range requested.Events {
		event.MutationID = "replayed-mutation"
		replayed.Events = append(replayed.Events, event)
	}
	if !artifactMutationWasRecovered(replayed, requested) {
		t.Fatal("replayed completion with current_slice clear did not match requested artifact mutation")
	}
	if replayed.State.Plan.CurrentSlice != nil {
		t.Fatalf("replayed completion retained current_slice %q", *replayed.State.Plan.CurrentSlice)
	}
}

func TestLoadPlanFilesAllowsLegacyStateWithoutWorkspaceMetadata(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "state.json"), `{
  "schema": "tao.plan.state.v1",
  "status": "planned",
  "created_at": "2026-05-03T23:00:00Z",
  "updated_at": "2026-05-03T23:00:00Z",
  "repo": {"name": "repo", "root": "/repo", "branch": "master"},
  "plan": {"id": "plan", "title": "Plan", "current_slice": null, "completed_slices": [], "pending_slices": ["001-a"], "timing": {}},
  "global_invariants": [],
  "open_questions": []
}`)
	writeFile(t, filepath.Join(dir, "slices.json"), `{
  "schema": "tao.plan.slices.v1",
  "plan_id": "plan",
  "execution": {"mode": "serial", "parallel_safe": false},
  "slices": [{"id": "001-a", "title": "A", "status": "pending", "depends_on": [], "timing": {"created_at": "2026-05-03T23:00:00Z", "updated_at": "2026-05-03T23:00:00Z"}, "goal": "", "context": "", "tasks": [], "expected_files": [], "verification": {"commands": ["go test ./internal/plan"], "manual_checks": []}}]
}`)
	writeFile(t, filepath.Join(dir, PlanningBriefFile), completePlanningBriefMarkdown())

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	if detail.State.Workspace != nil {
		t.Fatalf("expected legacy state to omit workspace metadata, got %#v", detail.State.Workspace)
	}
	if len(detail.Warnings) != 0 {
		t.Fatalf("expected no warnings for legacy state without workspace metadata, got %v", detail.Warnings)
	}
}

func TestCompleteSliceDoesNotDuplicateCompletedEvent(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	completed := started.Add(time.Minute)
	detail := startSliceDetail(dir)
	detail.State.Status = StatusInProgress
	detail.State.Plan.CurrentSlice = &detail.Slices.Slices[0].ID
	detail.Slices.Slices[0].Status = StatusInProgress
	detail.Slices.Slices[0].Timing.StartedAt = &started
	detail.Events = []Event{{Type: EventTypeSliceCompleted, Timestamp: completed, PlanID: "plan-a", SliceID: "001-a", Message: "Slice completed and verified"}}
	writeStartSliceArtifacts(t, dir, detail)
	if err := AppendEvent(dir, detail.Events[0]); err != nil {
		t.Fatal(err)
	}

	if err := testRecord(dir, detail).CompleteSlice("001-a", "retry", nil, completed.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(readEventsFile(t, dir), `"type":"slice_completed"`); got != 1 {
		t.Fatalf("expected one completion event, got %d", got)
	}
}

func TestStartSliceAndCompleteSliceUpdateInMemoryDetail(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	completed := started.Add(90 * time.Second)
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)

	if err := testRecord(dir, detail).StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusInProgress || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" {
		t.Fatalf("StartSlice did not update in-memory state: %#v", detail.State.Plan)
	}
	startedSlice := findSlice(detail, "001-a")
	if startedSlice == nil || startedSlice.Status != StatusInProgress || startedSlice.Timing.StartedAt == nil || !startedSlice.Timing.StartedAt.Equal(started) {
		t.Fatalf("StartSlice did not update in-memory slice: %#v", startedSlice)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != EventTypeSliceStarted {
		t.Fatalf("StartSlice did not append in-memory event: %#v", detail.Events)
	}

	results := []VerificationRun{{Command: "go test ./internal/plan", Result: "passed"}}
	if err := testRecord(dir, detail).CompleteSlice("001-a", "done", results, completed); err != nil {
		t.Fatal(err)
	}
	completedSlice := findSlice(detail, "001-a")
	if detail.State.Status != StatusInReview || detail.State.Plan.CurrentSlice != nil || len(detail.State.Plan.PendingSlices) != 0 || len(detail.State.Plan.CompletedSlices) != 1 {
		t.Fatalf("CompleteSlice did not update in-memory state: %#v", detail.State.Plan)
	}
	if completedSlice == nil || completedSlice.Status != StatusCompleted || completedSlice.Notes != "done" || len(completedSlice.VerificationResults) != 1 {
		t.Fatalf("CompleteSlice did not update in-memory slice: %#v", completedSlice)
	}
	if len(detail.Events) != 2 || detail.Events[0].Type != EventTypeSliceStarted || detail.Events[1].Type != EventTypeSliceCompleted {
		t.Fatalf("CompleteSlice did not preserve event order: %#v", detail.Events)
	}
}

func TestApproveSliceWritesApprovalAndEvent(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 24, 23, 1, 2, 0, time.UTC)
	detail := startSliceDetail(dir)
	detail.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "human approval"}
	writeStartSliceArtifacts(t, dir, detail)

	if err := testRecord(dir, detail).ApproveSlice("001-a", "Seth", now); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if !state.UpdatedAt.Equal(now) || state.Plan.Timing.LastActivityAt == nil || !state.Plan.Timing.LastActivityAt.Equal(now) {
		t.Fatalf("unexpected state timing after approve: %#v", state)
	}
	slices := readSlicesFile(t, dir)
	approval := slices.Slices[0].Approval
	if approval == nil || !approval.Approved || approval.ApprovedBy == nil || *approval.ApprovedBy != "Seth" || approval.ApprovedAt == nil || *approval.ApprovedAt != "2026-05-24T23:01:02Z" {
		t.Fatalf("unexpected approval after approve: %#v", approval)
	}
	if got := readEventsFile(t, dir); !strings.Contains(got, `"type":"slice_approved"`) || !strings.Contains(got, `"message":"Slice approved"`) {
		t.Fatalf("unexpected events.jsonl %q", got)
	}
}

func TestApproveSliceIsIdempotentAndPreservesMetadata(t *testing.T) {
	dir := t.TempDir()
	first := time.Date(2026, 5, 24, 23, 1, 2, 0, time.UTC)
	second := first.Add(time.Minute)
	approvedBy := "Original"
	approvedAt := "2026-05-24T23:01:02Z"
	detail := startSliceDetail(dir)
	detail.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "human approval", Approved: true, ApprovedBy: &approvedBy, ApprovedAt: &approvedAt}
	detail.Events = []Event{{Type: EventTypeSliceApproved, Timestamp: first, PlanID: "plan-a", SliceID: "001-a", Message: "Slice approved"}}
	writeStartSliceArtifacts(t, dir, detail)
	if err := AppendEvent(dir, detail.Events[0]); err != nil {
		t.Fatal(err)
	}

	if err := testRecord(dir, detail).ApproveSlice("001-a", "Second", second); err != nil {
		t.Fatal(err)
	}

	slices := readSlicesFile(t, dir)
	approval := slices.Slices[0].Approval
	if approval.ApprovedBy == nil || *approval.ApprovedBy != "Original" || approval.ApprovedAt == nil || *approval.ApprovedAt != approvedAt {
		t.Fatalf("expected existing metadata preserved, got %#v", approval)
	}
	if got := strings.Count(readEventsFile(t, dir), `"type":"slice_approved"`); got != 1 {
		t.Fatalf("expected one approval event, got %d", got)
	}
}

func TestBlockSliceWritesCanonicalStateAndPreservesExecutionMetadata(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 7, 19, 15, 44, 35, 0, time.UTC)
	blockedAt := started.Add(time.Minute)
	retriedAt := blockedAt.Add(time.Minute)
	detail := startSliceDetail(dir)
	slice := &detail.Slices.Slices[0]
	detail.State.Status = StatusInProgress
	detail.State.Plan.CurrentSlice = new(slice.ID)
	detail.State.Plan.Timing.StartedAt = new(started)
	slice.Status = StatusInProgress
	slice.Timing.StartedAt = new(started)
	slice.ExecutionRoot = "/workspace/plan-a"
	slice.ExecutionStart = &SliceExecutionStart{
		Branch: "tao/plan-a", Head: "base123", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree,
	}
	slice.CommitIntent = &SliceCommitIntent{
		Hash: "intent-hash", Policy: "slice", StartingBranch: "tao/plan-a", StartingHead: "base123", Message: "feat: preserve work", CreatedAt: started,
	}
	wantRoot := slice.ExecutionRoot
	wantStart := *slice.ExecutionStart
	wantIntent := *slice.CommitIntent
	writeStartSliceArtifacts(t, dir, detail)

	longReason := strings.Repeat("界", maxBlockerNoteRunes+10)
	record := testRecord(dir, detail)
	if err := record.BlockSlice("001-a", longReason, blockedAt); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	persisted := readSlicesFile(t, dir).Slices[0]
	if state.Status != StatusBlocked || state.Plan.CurrentSlice == nil || *state.Plan.CurrentSlice != "001-a" || !slices0.Contains(state.Plan.PendingSlices, "001-a") {
		t.Fatalf("unexpected canonical blocked state: %#v", state)
	}
	if persisted.Status != StatusBlocked || len([]rune(persisted.BlockerNote)) != maxBlockerNoteRunes {
		t.Fatalf("unexpected blocked slice shape: status=%q note runes=%d", persisted.Status, len([]rune(persisted.BlockerNote)))
	}
	if persisted.ExecutionRoot != wantRoot || persisted.ExecutionStart == nil || *persisted.ExecutionStart != wantStart || persisted.CommitIntent == nil || *persisted.CommitIntent != wantIntent {
		t.Fatalf("blocking changed execution metadata: %#v", persisted)
	}
	if !state.UpdatedAt.Equal(blockedAt) || state.Plan.Timing.LastActivityAt == nil || !state.Plan.Timing.LastActivityAt.Equal(blockedAt) || !persisted.Timing.UpdatedAt.Equal(blockedAt) || persisted.Timing.LastActivityAt == nil || !persisted.Timing.LastActivityAt.Equal(blockedAt) {
		t.Fatalf("unexpected blocked timing: state=%#v slice=%#v", state.Plan.Timing, persisted.Timing)
	}
	var blockedEvent *Event
	for i := range detail.Events {
		if detail.Events[i].Type == EventTypeSliceBlocked && detail.Events[i].SliceID == "001-a" {
			blockedEvent = &detail.Events[i]
			break
		}
	}
	if blockedEvent == nil || blockedEvent.Reason != persisted.BlockerNote || blockedEvent.Message != "Slice blocked" {
		t.Fatalf("unexpected slice_blocked event: %#v", blockedEvent)
	}

	if err := record.BlockSlice("001-a", "  updated blocker  ", retriedAt); err != nil {
		t.Fatal(err)
	}
	persisted = readSlicesFile(t, dir).Slices[0]
	if persisted.BlockerNote != "updated blocker" || !persisted.Timing.UpdatedAt.Equal(retriedAt) || persisted.Timing.LastActivityAt == nil || !persisted.Timing.LastActivityAt.Equal(retriedAt) {
		t.Fatalf("idempotent block did not update note and timing: %#v", persisted)
	}
	if persisted.ExecutionRoot != wantRoot || persisted.ExecutionStart == nil || *persisted.ExecutionStart != wantStart || persisted.CommitIntent == nil || *persisted.CommitIntent != wantIntent {
		t.Fatalf("idempotent block changed execution metadata: %#v", persisted)
	}
	if got := strings.Count(readEventsFile(t, dir), `"type":"slice_blocked"`); got != 1 {
		t.Fatalf("slice_blocked event count = %d, want 1", got)
	}
}

func TestBlockSliceRejectsInvalidSelectionsWithoutMutation(t *testing.T) {
	tests := []struct {
		name    string
		sliceID string
		mutate  func(*PlanDetail)
		want    string
	}{
		{name: "unknown slice", sliceID: "missing", want: "slice missing not found"},
		{name: "completed slice", sliceID: "001-a", mutate: func(detail *PlanDetail) {
			detail.Slices.Slices[0].Status = StatusCompleted
		}, want: "is completed and cannot be blocked"},
		{name: "skipped slice", sliceID: "001-a", mutate: func(detail *PlanDetail) {
			detail.Slices.Slices[0].Status = StatusSkipped
		}, want: "is skipped and cannot be blocked"},
		{name: "different blocked slice", sliceID: "002-b", mutate: func(detail *PlanDetail) {
			detail.State.Status = StatusBlocked
			detail.State.Plan.CurrentSlice = new("001-a")
			detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, "002-b")
			detail.Slices.Slices[0].Status = StatusBlocked
			detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusPending})
		}, want: "slice 001-a is already blocked"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := startSliceDetail("/plans/plan-a")
			if tt.mutate != nil {
				tt.mutate(detail)
			}
			original := clonePlanDetail(detail)
			store := &recordingArtifactMutationStore{}
			record, err := newPlanRecord(store, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}

			err = record.BlockSlice(tt.sliceID, "blocked", editTime())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BlockSlice error = %v, want text %q", err, tt.want)
			}
			if len(store.calls) != 0 {
				t.Fatalf("invalid block wrote artifacts: %v", store.calls)
			}
			if !reflect.DeepEqual(detail, original) {
				t.Fatalf("invalid block changed detail:\n got: %#v\nwant: %#v", detail, original)
			}
		})
	}
}

func TestContinueBlockedPlanWritesInProgressState(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 3, 23, 45, 0, 0, time.UTC)
	detail := startSliceDetail(dir)
	detail.State.Status = StatusBlocked
	detail.Slices.Slices[0].Status = StatusBlocked
	detail.Slices.Slices[0].BlockerNote = "resolved blocker"
	writeStartSliceArtifacts(t, dir, detail)
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
	raw["slices"].([]any)[0].(map[string]any)["unknown_slice"] = "keep"
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := testRecord(dir, detail).ContinueBlocked(now); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if state.Status != StatusInProgress || state.Plan.CurrentSlice == nil || *state.Plan.CurrentSlice != "001-a" {
		t.Fatalf("unexpected state after continue: %#v", state)
	}
	slices := readSlicesFile(t, dir)
	if slices.Slices[0].Status != StatusInProgress || slices.Slices[0].BlockerNote != "" {
		t.Fatalf("expected selected slice in progress with cleared blocker note, got %#v", slices.Slices[0])
	}
	readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
	sliceObject := raw["slices"].([]any)[0].(map[string]any)
	if value, exists := sliceObject["blocker_note"]; !exists || value != "" || sliceObject["unknown_slice"] != "keep" {
		t.Fatalf("continued slice did not persist an explicit clear and unknown sibling: %#v", sliceObject)
	}
}

func TestContinueBlockedRejectsOrdinarilyInProgressSlice(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)

	err := testRecord(dir, detail).ContinueBlocked(time.Date(2026, 5, 3, 23, 45, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "continue is not meaningful") {
		t.Fatalf("ContinueBlocked error = %v, want non-blocked plan error", err)
	}
	if got := readStateFile(t, dir); !reflect.DeepEqual(got, detail.State) {
		t.Fatalf("failed continuation changed state:\n got: %#v\nwant: %#v", got, detail.State)
	}
	if got := readSlicesFile(t, dir); !reflect.DeepEqual(got, detail.Slices) {
		t.Fatalf("failed continuation changed slices:\n got: %#v\nwant: %#v", got, detail.Slices)
	}
}

func TestApplyLifecycleMutationReturnsExplicitArtifacts(t *testing.T) {
	now := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	detail := startSliceDetail("")

	mutation, err := applyLifecycleMutation(detail, func(_ *ArtifactChangeSet) ([]Event, error) {
		event, appendEvent, err := MarkSliceStarted(detail, "001-a", now)
		if err != nil {
			return nil, err
		}
		if !appendEvent {
			return nil, nil
		}
		return []Event{event}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if mutation.State.Status != StatusInProgress || mutation.State.Plan.CurrentSlice == nil || *mutation.State.Plan.CurrentSlice != "001-a" {
		t.Fatalf("unexpected mutation state: %#v", mutation.State)
	}
	if mutation.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("unexpected mutation slices: %#v", mutation.Slices)
	}
	if len(mutation.Events) != 1 || mutation.Events[0].Type != EventTypeSliceStarted || mutation.Events[0].SliceID != "001-a" {
		t.Fatalf("unexpected mutation events: %#v", mutation.Events)
	}
}

func TestPlanRecordMutationWritesThroughBoundDirectory(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	store := &recordingArtifactMutationStore{}
	record, err := newPlanRecord(store, "/plans/plan-a", detail)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)

	if err := record.StartSlice("001-a", now); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(store.calls, ","); got != "state,slices,event" {
		t.Fatalf("unexpected write order %q", got)
	}
	if record.Dir() != "/plans/plan-a" || record.Detail() != detail {
		t.Fatalf("record did not keep its bound detail and directory: dir=%q detail=%p", record.Dir(), record.Detail())
	}
	if store.state.Status != StatusInProgress || store.slices.Slices[0].Status != StatusInProgress || len(store.events) != 1 || store.events[0].Type != EventTypeSliceStarted {
		t.Fatalf("unexpected stored mutation: state=%#v slices=%#v events=%#v", store.state, store.slices, store.events)
	}
	if detail.State.Status != StatusInProgress || len(detail.Events) != 1 {
		t.Fatalf("record mutation did not update bound detail: state=%#v events=%#v", detail.State, detail.Events)
	}
}

func TestPlanRecordMutationRejectsMismatchedDirectoryWithoutWrites(t *testing.T) {
	detail := startSliceDetail("/plans/right")
	original := clonePlanDetail(detail)
	store := &recordingArtifactMutationStore{}

	record, err := newPlanRecord(store, "/plans/wrong", detail)
	if err == nil || !strings.Contains(err.Error(), "does not match loaded detail directory") {
		t.Fatalf("expected directory mismatch error, got record=%#v err=%v", record, err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("mismatched record should not write artifacts, got calls %v", store.calls)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("mismatched record changed detail:\n got: %#v\nwant: %#v", detail, original)
	}
}

func TestApplyArtifactMutationWritesThroughStore(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	store := &recordingArtifactMutationStore{}
	events := []Event{
		{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a", Message: "started"},
		{Type: EventTypeSliceCompleted, PlanID: "plan-a", SliceID: "001-a", Message: "completed"},
	}

	err := applyArtifactMutation(store, detail.Dir, detail, func(mutated *PlanDetail) (lifecycleMutation, error) {
		return lifecycleMutation{State: mutated.State, Slices: mutated.Slices, Events: events}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(store.calls, ","); got != "state,slices,event,event" {
		t.Fatalf("unexpected write order %q", got)
	}
	if store.state.Plan.ID != "plan-a" || store.slices.PlanID != "plan-a" || len(store.events) != 2 || store.events[0].Type != EventTypeSliceStarted || store.events[1].Type != EventTypeSliceCompleted {
		t.Fatalf("unexpected stored artifacts: state=%#v slices=%#v events=%#v", store.state, store.slices, store.events)
	}
	mutationID := store.events[0].MutationID
	if mutationID == "" || store.events[1].MutationID != mutationID {
		t.Fatalf("transaction events do not share a mutation id: %#v", store.events)
	}
	if len(detail.Events) != 2 || detail.Events[0].Type != EventTypeSliceStarted || detail.Events[1].Type != EventTypeSliceCompleted || detail.Events[0].MutationID != mutationID || detail.Events[1].MutationID != mutationID {
		t.Fatalf("expected appended in-memory events in order, got %#v", detail.Events)
	}
}

func TestApplyArtifactMutationStopsOnWriteError(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	store := &recordingArtifactMutationStore{writeSlicesErr: errArtifactStore("write slices failed")}
	event := Event{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a"}

	err := applyArtifactMutation(store, detail.Dir, detail, func(mutated *PlanDetail) (lifecycleMutation, error) {
		return lifecycleMutation{State: mutated.State, Slices: mutated.Slices, Events: []Event{event}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "write slices failed") {
		t.Fatalf("expected write slices error, got %v", err)
	}
	if got := strings.Join(store.calls, ","); got != "state,slices" {
		t.Fatalf("unexpected write order after failure %q", got)
	}
	if len(store.events) != 0 || len(detail.Events) != 0 {
		t.Fatalf("event should not be appended after write failure: store=%#v detail=%#v", store.events, detail.Events)
	}
}

func TestApplyArtifactMutationDoesNotRecordFailedEventAppend(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	store := &recordingArtifactMutationStore{appendEventErr: errArtifactStore("append denied")}
	event := Event{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a"}

	err := applyArtifactMutation(store, detail.Dir, detail, func(mutated *PlanDetail) (lifecycleMutation, error) {
		return lifecycleMutation{State: mutated.State, Slices: mutated.Slices, Events: []Event{event}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "append events.jsonl: append denied") {
		t.Fatalf("expected wrapped append error, got %v", err)
	}
	if got := strings.Join(store.calls, ","); got != "state,slices,event" {
		t.Fatalf("unexpected write order after append failure %q", got)
	}
	if len(detail.Events) != 0 {
		t.Fatalf("failed event append should not update detail events: %#v", detail.Events)
	}
}

func TestApplyArtifactMutationLeavesDetailUnchangedOnFailures(t *testing.T) {
	event := Event{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a"}
	tests := []struct {
		name      string
		store     *recordingArtifactMutationStore
		wantCalls string
		wantError string
	}{
		{name: "state write", store: &recordingArtifactMutationStore{writeStateErr: errArtifactStore("write state failed")}, wantCalls: "state", wantError: "write state failed"},
		{name: "slices write", store: &recordingArtifactMutationStore{writeSlicesErr: errArtifactStore("write slices failed")}, wantCalls: "state,slices", wantError: "write slices failed"},
		{name: "event append", store: &recordingArtifactMutationStore{appendEventErr: errArtifactStore("append denied")}, wantCalls: "state,slices,event", wantError: "append events.jsonl: append denied"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := startSliceDetail("/plans/plan-a")
			original := clonePlanDetail(detail)

			err := applyArtifactMutation(tt.store, detail.Dir, detail, func(mutated *PlanDetail) (lifecycleMutation, error) {
				if _, _, err := MarkSliceStarted(mutated, "001-a", time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)); err != nil {
					return lifecycleMutation{}, err
				}
				return lifecycleMutation{State: mutated.State, Slices: mutated.Slices, Events: []Event{event}}, nil
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected %q error, got %v", tt.wantError, err)
			}
			if got := strings.Join(tt.store.calls, ","); got != tt.wantCalls {
				t.Fatalf("unexpected write order after failure %q", got)
			}
			if !reflect.DeepEqual(detail, original) {
				t.Fatalf("detail changed after failed mutation:\n got: %#v\nwant: %#v", detail, original)
			}
		})
	}
}

func TestLoadPlanFilesRestartCompatibilityMatrix(t *testing.T) {
	originalState := []byte(`{"schema":"tao.plan.state.v1","status":"planned","plan":{"id":"plan-a"},"fixture":"original-state"}`)
	originalSlices := []byte(`{"schema":"tao.plan.slices.v1","plan_id":"plan-a","slices":[],"fixture":"original-slices"}`)

	tests := []struct {
		name        string
		prepare     func(*testing.T, string, mutationJournal)
		wantError   string
		wantSettled bool
		wantState   []byte
		wantSlices  []byte
		wantEvents  int
	}{
		{name: "fresh", wantState: originalState, wantSlices: originalSlices},
		{name: "pending", prepare: func(t *testing.T, dir string, journal mutationJournal) {
			writeRestartJournal(t, dir, journal)
		}, wantSettled: true, wantEvents: 2},
		{name: "partially settled", prepare: func(t *testing.T, dir string, journal mutationJournal) {
			writeRestartJournal(t, dir, journal)
			assertInstallMutationTarget(t, filepath.Join(dir, "state.json"), journal.State.Payload)
			appendRestartEvent(t, dir, journal.Events[0].Payload)
		}, wantSettled: true, wantEvents: 2},
		{name: "already settled", prepare: func(t *testing.T, dir string, journal mutationJournal) {
			writeRestartJournal(t, dir, journal)
			assertInstallMutationTarget(t, filepath.Join(dir, "state.json"), journal.State.Payload)
			assertInstallMutationTarget(t, filepath.Join(dir, "slices.json"), journal.Slices.Payload)
			for _, event := range journal.Events {
				appendRestartEvent(t, dir, event.Payload)
			}
		}, wantSettled: true, wantEvents: 2},
		{name: "malformed", prepare: func(t *testing.T, dir string, _ mutationJournal) {
			if err := os.WriteFile(filepath.Join(dir, mutationJournalFile), []byte(`{"schema":`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantError: "invalid .mutation.json", wantState: originalState, wantSlices: originalSlices},
		{name: "legacy no journal", prepare: func(t *testing.T, dir string, journal mutationJournal) {
			assertInstallMutationTarget(t, filepath.Join(dir, "state.json"), journal.State.Payload)
		}, wantState: nil, wantSlices: originalSlices},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "plan-a")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			assertInstallMutationTarget(t, filepath.Join(dir, "state.json"), originalState)
			assertInstallMutationTarget(t, filepath.Join(dir, "slices.json"), originalSlices)
			journal := testMutationJournalWithTwoEvents(t)
			if tt.prepare != nil {
				tt.prepare(t, dir, journal)
			}

			files, err := loadPlanFiles(dir)
			switch {
			case tt.wantError != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("load error = %v, want %q", err, tt.wantError)
				}
			case err != nil:
				t.Fatal(err)
			case len(files.events) != tt.wantEvents:
				t.Fatalf("events after restart = %d, want %d: %#v", len(files.events), tt.wantEvents, files.events)
			}

			if tt.wantSettled {
				assertMutationFile(t, filepath.Join(dir, "state.json"), journal.State.Payload)
				assertMutationFile(t, filepath.Join(dir, "slices.json"), journal.Slices.Payload)
				if _, statErr := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("settled journal remains: %v", statErr)
				}
				return
			}
			if tt.wantState != nil {
				assertMutationFile(t, filepath.Join(dir, "state.json"), tt.wantState)
			} else {
				assertMutationFile(t, filepath.Join(dir, "state.json"), journal.State.Payload)
			}
			assertMutationFile(t, filepath.Join(dir, "slices.json"), tt.wantSlices)
		})
	}
}

func writeRestartJournal(t *testing.T, dir string, journal mutationJournal) {
	t.Helper()
	encoded, err := encodeMutationJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutationJournalFile), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertInstallMutationTarget(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendRestartEvent(t *testing.T, dir string, payload []byte) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- test path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(append([]byte(nil), payload...), '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanRecordStartSliceFailuresPreserveMemoryAndReplayJournal(t *testing.T) {
	startedAt := time.Date(2026, 7, 18, 1, 30, 0, 0, time.UTC)
	boundary := SliceExecutionStart{
		Branch: "tao/plan-a", Head: "base123", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree,
	}
	for _, operation := range []string{"state", "slices", "event-1", "remove"} {
		t.Run(operation, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "plan-a")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			detail := startSliceDetail(dir)
			original := clonePlanDetail(detail)
			writeStartSliceArtifacts(t, dir, detail)
			ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: operation}
			store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
			record, err := newPlanRecord(store, dir, detail)
			if err != nil {
				t.Fatal(err)
			}

			err = record.StartSliceWithRunBoundary("001-a", "/worktrees/plan-a", "slice", []string{"README.md"}, boundary, startedAt)
			if err == nil || !strings.Contains(err.Error(), "injected "+operation+" failure") {
				t.Fatalf("start error = %v, want injected %s failure", err, operation)
			}
			if !reflect.DeepEqual(detail, original) {
				t.Fatalf("failed start changed in-memory detail:\n got: %#v\nwant: %#v", detail, original)
			}

			files, err := loadPlanFiles(dir)
			if err != nil {
				t.Fatalf("replay failed start journal: %v", err)
			}
			replayed := detailFromFiles(files)
			if replayed.State.Plan.CurrentSlice == nil || *replayed.State.Plan.CurrentSlice != "001-a" || replayed.State.Plan.LastRunCommitPolicy != "slice" {
				t.Fatalf("replayed state = %#v", replayed.State.Plan)
			}
			persistedSlice := replayed.Slices.Slices[0]
			if persistedSlice.Status != StatusInProgress || persistedSlice.ExecutionStart == nil || *persistedSlice.ExecutionStart != boundary {
				t.Fatalf("replayed slice = %#v", persistedSlice)
			}
			if len(replayed.Events) != 1 || replayed.Events[0].Type != EventTypeSliceStarted || replayed.Events[0].MutationID == "" {
				t.Fatalf("replayed events = %#v", replayed.Events)
			}
			if _, statErr := os.Stat(filepath.Join(dir, mutationJournalFile)); !os.IsNotExist(statErr) {
				t.Fatalf("journal remains after replay: %v", statErr)
			}
		})
	}
}

func TestPlanRecordRetrySettlesBeforeReevaluatingMutation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 18, 1, 30, 0, 0, time.UTC)
	detail := startSliceDetail(dir)
	original := clonePlanDetail(detail)
	writeStartSliceArtifacts(t, dir, detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "event-1"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}

	if err := record.StartSlice("001-a", startedAt); err == nil || !strings.Contains(err.Error(), "injected event-1 failure") {
		t.Fatalf("first start error = %v, want injected event failure", err)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("failed start changed in-memory detail:\n got: %#v\nwant: %#v", detail, original)
	}

	// Retry through the same record and stale detail. The mutation entry point
	// must replay and publish the first intent before it evaluates this request.
	if err := record.StartSlice("001-a", startedAt); err != nil {
		t.Fatalf("retry start: %v", err)
	}
	if detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" || detail.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("retry did not publish recovered lifecycle state: state=%#v slices=%#v", detail.State, detail.Slices)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != EventTypeSliceStarted || detail.Events[0].MutationID == "" {
		t.Fatalf("retry duplicated or lost recovered event: %#v", detail.Events)
	}
	persisted, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 || len(persisted) != 1 || persisted[0].MutationID != detail.Events[0].MutationID {
		t.Fatalf("persisted retry events = %#v warnings=%v err=%v", persisted, warnings, err)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after retry: %v", err)
	}
}

func TestPlanRecordRetryRejectsConflictingRecoveredExecutionBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 18, 1, 30, 0, 0, time.UTC)
	originalBoundary := SliceExecutionStart{
		Branch: "tao/plan-a", Head: "base123", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree,
	}
	conflictingBoundary := originalBoundary
	conflictingBoundary.Head = "other-base"
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "event-1"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}

	if err := record.StartSliceWithRunBoundary("001-a", "/worktrees/plan-a", "slice", []string{"README.md"}, originalBoundary, startedAt); err == nil || !strings.Contains(err.Error(), "injected event-1 failure") {
		t.Fatalf("first start error = %v, want injected event failure", err)
	}
	if err := record.StartSliceWithRunBoundary("001-a", "/worktrees/plan-a", "slice", []string{"README.md"}, conflictingBoundary, startedAt); err == nil || !strings.Contains(err.Error(), "execution boundary is immutable") {
		t.Fatalf("conflicting retry error = %v, want immutable boundary error", err)
	}

	slice := detail.Slices.Slices[0]
	if slice.ExecutionStart == nil || *slice.ExecutionStart != originalBoundary {
		t.Fatalf("recovered execution boundary = %#v, want %#v", slice.ExecutionStart, originalBoundary)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != EventTypeSliceStarted || detail.Events[0].MutationID == "" {
		t.Fatalf("recovered start event = %#v", detail.Events)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after conflicting retry recovery: %v", err)
	}
}

func TestPlanRecordRetryRejectsConflictingRecoveredCompletionOutcome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 18, 1, 30, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Minute)
	detail := startSliceDetail(dir)
	detail.State.Status = StatusInProgress
	detail.State.Plan.CurrentSlice = &detail.Slices.Slices[0].ID
	detail.State.Plan.Timing.StartedAt = &startedAt
	detail.Slices.Slices[0].Status = StatusInProgress
	detail.Slices.Slices[0].Timing.StartedAt = &startedAt
	detail.Slices.Slices[0].CommitIntent = &SliceCommitIntent{
		Hash: "intent", Policy: "slice", StartingBranch: "tao/plan-a", StartingHead: "base123", Message: "message", CreatedAt: startedAt,
	}
	writeStartSliceArtifacts(t, dir, detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "event-1"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	originalOutcome := SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "commit-a"}
	conflictingOutcome := SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "commit-b"}

	if err := record.CompleteSliceWithOutcome("001-a", "first completion", nil, originalOutcome, completedAt); err == nil || !strings.Contains(err.Error(), "injected event-1 failure") {
		t.Fatalf("first completion error = %v, want injected event failure", err)
	}
	if err := record.CompleteSliceWithOutcome("001-a", "conflicting completion", nil, conflictingOutcome, completedAt); err == nil || !strings.Contains(err.Error(), "conflicting completion outcome") {
		t.Fatalf("conflicting retry error = %v, want completion outcome conflict", err)
	}

	slice := detail.Slices.Slices[0]
	if slice.Completion == nil || *slice.Completion != originalOutcome || slice.Notes != "first completion" {
		t.Fatalf("recovered completion = %#v", slice)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != EventTypeSliceCompleted || detail.Events[0].MutationID == "" {
		t.Fatalf("recovered completion event = %#v", detail.Events)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after conflicting retry recovery: %v", err)
	}
}

func TestPlanRecordPostUnlinkSyncFailurePublishesBeforeSameRecordRetry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 18, 1, 30, 0, 0, time.UTC)
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	ioStore := &postUnlinkSyncFailureMutationJournalIO{delegate: fileMutationJournalIO{}}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}

	if err := record.StartSlice("001-a", startedAt); err != nil {
		t.Fatalf("start with post-unlink sync failure: %v", err)
	}
	if !ioStore.failed {
		t.Fatal("directory sync failure was not injected")
	}
	if detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" || detail.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("committed settlement was not published: state=%#v slices=%#v", detail.State, detail.Slices)
	}
	if len(detail.Events) != 1 || detail.Events[0].Type != EventTypeSliceStarted || detail.Events[0].MutationID == "" {
		t.Fatalf("committed event was not published: %#v", detail.Events)
	}
	mutationID := detail.Events[0].MutationID
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after successful unlink: %v", err)
	}

	if err := record.StartSlice("001-a", startedAt.Add(time.Minute)); err != nil {
		t.Fatalf("same-record retry: %v", err)
	}
	if len(detail.Events) != 1 || detail.Events[0].MutationID != mutationID {
		t.Fatalf("same-record retry duplicated or replaced committed event: %#v", detail.Events)
	}
	persisted, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 || len(persisted) != 1 || persisted[0].MutationID != mutationID {
		t.Fatalf("persisted retry events = %#v warnings=%v err=%v", persisted, warnings, err)
	}
}

func TestSingleTargetWriterWaitsForJournalAndRebasesOntoSettledSlices(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := startSliceDetail(dir)
	initial.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "human approval"}
	writeStartSliceArtifacts(t, dir, initial)

	loadDetail := func() *PlanDetail {
		files, err := loadPlanFiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		return detailFromFiles(files)
	}
	approvalDetail := loadDetail()
	intentDetail := loadDetail()
	blockingIO := &blockingRemoveMutationJournalIO{
		delegate: fileMutationJournalIO{}, entered: make(chan struct{}), release: make(chan struct{}),
	}
	approvalRecord, err := newPlanRecord(journalArtifactMutationStore{journalIO: blockingIO}, dir, approvalDetail)
	if err != nil {
		t.Fatal(err)
	}
	intentLockEntered := make(chan struct{})
	intentRecord, err := newPlanRecord(&signalingArtifactMutationStore{entered: intentLockEntered}, dir, intentDetail)
	if err != nil {
		t.Fatal(err)
	}

	approvalErr := make(chan error, 1)
	go func() {
		approvalErr <- approvalRecord.ApproveSlice("001-a", "alice", time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC))
	}()
	<-blockingIO.entered

	intent := SliceCommitIntent{
		Hash: "intent-hash", Policy: "slice", StartingBranch: "tao/plan-a", StartingHead: "base123",
		Message: "feat: preserve approval", CreatedAt: time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC),
	}
	intentErr := make(chan error, 1)
	go func() {
		intentErr <- intentRecord.RecordSliceCommitIntent("001-a", intent)
	}()
	<-intentLockEntered
	select {
	case err := <-intentErr:
		t.Fatalf("single-target writer completed while journal lock was held: %v", err)
	default:
	}
	close(blockingIO.release)

	if err := <-approvalErr; err != nil {
		t.Fatalf("approve slice: %v", err)
	}
	if err := <-intentErr; err != nil {
		t.Fatalf("record commit intent: %v", err)
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	slice := findSlice(detailFromFiles(files), "001-a")
	if slice == nil || slice.Approval == nil || !slice.Approval.Approved || slice.CommitIntent == nil || *slice.CommitIntent != intent {
		t.Fatalf("settled approval or rebased commit intent was lost: %#v", slice)
	}
	var approvals int
	for _, event := range files.events {
		if event.Type == EventTypeSliceApproved && event.SliceID == "001-a" {
			approvals++
		}
	}
	if approvals != 1 {
		t.Fatalf("approval events = %d, want 1", approvals)
	}
}

func TestArtifactMutationConcurrentWritersPreserveNonOverlappingApprovals(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := startSliceDetail(dir)
	initial.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "first approval"}
	initial.Slices.Slices = append(initial.Slices.Slices, Slice{
		ID:       "002-b",
		Title:    "B",
		Status:   StatusPending,
		Approval: &Approval{Required: true, Reason: "second approval"},
		Timing:   initial.Slices.Slices[0].Timing,
	})
	initial.State.Plan.PendingSlices = append(initial.State.Plan.PendingSlices, "002-b")
	writeStartSliceArtifacts(t, dir, initial)

	loadRecord := func() *PlanRecord {
		files, err := loadPlanFiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		record, err := NewPlanRecord(dir, detailFromFiles(files))
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	first := loadRecord()
	second := loadRecord()
	started := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	go func() {
		ready.Done()
		<-started
		errs <- first.ApproveSlice("001-a", "alice", time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC))
	}()
	go func() {
		ready.Done()
		<-started
		errs <- second.ApproveSlice("002-b", "bob", time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC))
	}()
	ready.Wait()
	close(started)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent approval: %v", err)
		}
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, sliceID := range []string{"001-a", "002-b"} {
		slice := findSlice(detailFromFiles(files), sliceID)
		if slice == nil || slice.Approval == nil || !slice.Approval.Approved {
			t.Fatalf("approval for %s was lost: %#v", sliceID, slice)
		}
		matching := 0
		for _, event := range files.events {
			if event.Type == EventTypeSliceApproved && event.SliceID == sliceID {
				matching++
				if event.MutationID == "" {
					t.Fatalf("approval event for %s has no mutation id", sliceID)
				}
			}
		}
		if matching != 1 {
			t.Fatalf("approval events for %s = %d, want 1", sliceID, matching)
		}
	}
}

func TestPlanRecordConcurrentStaleStateWritersMergeNewWorkspaceFields(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	loadRecord := func() *PlanRecord {
		files, err := loadPlanFiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		record, err := NewPlanRecord(dir, detailFromFiles(files))
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	strategyRecord := loadRecord()
	pathRecord := loadRecord()
	strategyRecord.Detail().State.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree}
	pathRecord.Detail().State.Workspace = &Workspace{Path: "/worktrees/plan-a"}

	started := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, record := range []*PlanRecord{strategyRecord, pathRecord} {
		go func() {
			ready.Done()
			<-started
			errs <- record.PersistState()
		}()
	}
	ready.Wait()
	close(started)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("persist concurrent workspace metadata: %v", err)
		}
	}

	workspace := readStateFile(t, dir).Workspace
	if workspace == nil || workspace.Strategy != WorkspaceStrategyWorktree || workspace.Path != "/worktrees/plan-a" {
		t.Fatalf("persisted workspace lost a concurrent field: %#v", workspace)
	}
}

func TestPlanRecordPersistsPreBoundWorkspaceMetadataWithCompleteArtifacts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	preparedAt := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	want := &Workspace{
		Strategy:              WorkspaceStrategyWorktree,
		Path:                  "/worktrees/plan-a",
		Branch:                "tao/plan-a",
		HeadSHA:               "head123",
		LifecycleStatus:       WorkspaceStatusReady,
		DependencyPreparation: "completed",
		Timing:                WorkspaceTiming{PreparedAt: &preparedAt},
	}
	// ExecutionPreparer installs workspace and dependency metadata before it
	// constructs a PlanRecord to persist the updated detail.
	detail.State.Workspace = want
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err != nil {
		t.Fatal(err)
	}

	persisted := readStateFile(t, dir)
	if !reflect.DeepEqual(persisted.Workspace, want) {
		t.Fatalf("persisted workspace = %#v, want %#v", persisted.Workspace, want)
	}
}

func TestPlanRecordConstructorRecoveryPreservesPreBoundWorkspaceMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	preparedAt := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	want := &Workspace{
		Strategy:              WorkspaceStrategyWorktree,
		Path:                  "/worktrees/plan-a",
		Branch:                "tao/plan-a",
		HeadSHA:               "head123",
		LifecycleStatus:       WorkspaceStatusReady,
		DependencyPreparation: "completed",
		Timing:                WorkspaceTiming{PreparedAt: &preparedAt},
	}
	detail.State.Workspace = want

	// Leave a concurrent lifecycle mutation journal pending after the caller
	// loaded and prepared its workspace metadata but before it binds a record.
	concurrent := detailFromFiles(files)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	concurrentRecord, err := newPlanRecord(store, dir, concurrent)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC)
	if err := concurrentRecord.StartSlice("001-a", startedAt); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("concurrent start error = %v, want injected state failure", err)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); err != nil {
		t.Fatalf("pending journal missing: %v", err)
	}

	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusInProgress || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" {
		t.Fatalf("recovered lifecycle state was not published: %#v", detail.State.Plan)
	}
	if !reflect.DeepEqual(detail.State.Workspace, want) {
		t.Fatalf("constructor discarded workspace metadata: %#v", detail.State.Workspace)
	}
	if err := record.PersistState(); err != nil {
		t.Fatal(err)
	}

	persisted := readStateFile(t, dir)
	if persisted.Status != StatusInProgress || persisted.Plan.CurrentSlice == nil || *persisted.Plan.CurrentSlice != "001-a" {
		t.Fatalf("recovered lifecycle state was erased: status=%q current_slice=%v", persisted.Status, persisted.Plan.CurrentSlice)
	}
	if !reflect.DeepEqual(persisted.Workspace, want) {
		t.Fatalf("persisted workspace = %#v, want %#v", persisted.Workspace, want)
	}
	persistedEvents, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("read recovered events: warnings=%v err=%v", warnings, err)
	}
	var starts int
	for _, event := range persistedEvents {
		if event.Type == EventTypeSliceStarted && event.SliceID == "001-a" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("recovered start events = %d, want 1", starts)
	}
}

func TestPlanRecordConstructorRecoveryRejectsConcurrentStructuralChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	createdAt := detail.State.CreatedAt.Add(time.Minute)
	detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, "002-b")
	detail.Slices.Slices = append(detail.Slices.Slices, Slice{
		ID: "002-b", Title: "B", Status: StatusPending,
		Timing: SliceTiming{CreatedAt: createdAt, UpdatedAt: createdAt},
	})

	// Leave a conflicting removal journal pending after the caller added a
	// different slice but before it binds a record.
	concurrent := detailFromFiles(files)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	concurrentRecord, err := newPlanRecord(store, dir, concurrent)
	if err != nil {
		t.Fatal(err)
	}
	removedAt := createdAt.Add(time.Minute)
	if err := concurrentRecord.RemoveSlice("001-a", removedAt); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("concurrent removal error = %v, want injected state failure", err)
	}

	if _, err := NewPlanRecord(dir, detail); err == nil || !strings.Contains(err.Error(), "concurrent structural change") {
		t.Fatalf("NewPlanRecord error = %v, want concurrent structural change", err)
	}
	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.state.Plan.PendingSlices) != 0 || len(persisted.slices.Slices) != 0 {
		t.Fatalf("constructor conflict changed recovered structure: state=%#v slices=%#v", persisted.state.Plan.PendingSlices, persisted.slices.Slices)
	}
	if !reflect.DeepEqual(detail.State.Plan.PendingSlices, []string{"001-a", "002-b"}) || len(detail.Slices.Slices) != 2 {
		t.Fatalf("constructor conflict changed caller intent: state=%#v slices=%#v", detail.State.Plan.PendingSlices, detail.Slices.Slices)
	}
}

func TestPlanRecordConstructorRecoveryPreservesPreBoundSliceEdits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	detail.Slices.Slices[0].Title = "Caller slice title"

	// Leave a concurrent lifecycle mutation journal pending after the caller
	// loaded and edited slices but before it binds a record.
	concurrent := detailFromFiles(files)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	concurrentRecord, err := newPlanRecord(store, dir, concurrent)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC)
	if err := concurrentRecord.StartSlice("001-a", startedAt); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("concurrent start error = %v, want injected state failure", err)
	}

	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Slices.Slices[0].Title != "Caller slice title" || detail.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("constructor lost caller or recovered slice edits: %#v", detail.Slices.Slices[0])
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.slices.Slices[0].Title != "Caller slice title" || persisted.slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("persisted slice lost caller or recovered edits: %#v", persisted.slices.Slices[0])
	}
	starts := 0
	for _, event := range persisted.events {
		if event.Type == EventTypeSliceStarted && event.SliceID == "001-a" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("recovered start events = %d, want 1", starts)
	}
}

func TestPlanRecordPersistStatePreservesLifecycleSettledAfterDetailLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := detailFromFiles(files)
	lifecycleRecord, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	if err := lifecycleRecord.StartSlice("001-a", startedAt); err != nil {
		t.Fatal(err)
	}

	stale.State.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree, Path: "/worktrees/plan-a"}
	metadataRecord, err := NewPlanRecord(dir, stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := metadataRecord.PersistState(); err != nil {
		t.Fatal(err)
	}

	persisted := readStateFile(t, dir)
	if persisted.Status != StatusInProgress || persisted.Plan.CurrentSlice == nil || *persisted.Plan.CurrentSlice != "001-a" {
		t.Fatalf("concurrent lifecycle state was erased: status=%q current_slice=%v", persisted.Status, persisted.Plan.CurrentSlice)
	}
	if persisted.Workspace == nil || persisted.Workspace.Path != "/worktrees/plan-a" {
		t.Fatalf("workspace metadata was not persisted: %#v", persisted.Workspace)
	}
	files, err = loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var foundStart bool
	for _, event := range files.events {
		if event.Type == EventTypeSliceStarted && event.SliceID == "001-a" {
			foundStart = true
			break
		}
	}
	if !foundStart {
		t.Fatalf("slice-start evidence missing from %#v", files.events)
	}
}

func TestPlanRecordPersistStateRetryPreservesIntentAfterRecoveryAndJournalInstallFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	installFailure := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "journal"}
	metadataRecord, err := newPlanRecord(journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: installFailure}, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree, Path: "/worktrees/plan-a"}

	// Leave a lifecycle journal for PersistState to recover before it tries to
	// install the caller's state-only follow-on journal.
	concurrent := detailFromFiles(files)
	recoveryFailure := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	concurrentRecord, err := newPlanRecord(journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: recoveryFailure}, dir, concurrent)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	if err := concurrentRecord.StartSlice("001-a", startedAt); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("concurrent start error = %v, want injected state failure", err)
	}

	if err := metadataRecord.PersistState(); err == nil || !strings.Contains(err.Error(), "injected journal failure") {
		t.Fatalf("persist state error = %v, want injected journal failure", err)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.Path != "/worktrees/plan-a" || detail.State.Status != StatusInProgress {
		t.Fatalf("failed persist did not retain caller intent over recovered state: %#v", detail.State)
	}

	// Advance the recovered lifecycle before retrying. The retry must use that
	// recovered bundle as its baseline, so the pending workspace edit does not
	// turn the recovered in-progress status into caller-owned intent.
	files, err = loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleRecord, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	blockedAt := startedAt.Add(time.Minute)
	if err := lifecycleRecord.BlockSlice("001-a", "waiting on dependency", blockedAt); err != nil {
		t.Fatal(err)
	}

	if err := metadataRecord.PersistState(); err != nil {
		t.Fatalf("retry persist state: %v", err)
	}
	persisted := readStateFile(t, dir)
	if persisted.Status != StatusBlocked || persisted.Workspace == nil || persisted.Workspace.Path != "/worktrees/plan-a" {
		t.Fatalf("retry lost caller intent or newer lifecycle state: %#v", persisted)
	}
	if detail.State.Status != StatusBlocked || detail.Slices.Slices[0].Status != StatusBlocked {
		t.Fatalf("retry did not publish latest settled bundle: state=%#v slices=%#v", detail.State, detail.Slices)
	}
}

func TestPlanRecordBoundToStaleDetailRefreshesSettledLifecycleMutation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := startSliceDetail(dir)
	initial.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "first approval"}
	initial.Slices.Slices = append(initial.Slices.Slices, Slice{
		ID:       "002-b",
		Title:    "B",
		Status:   StatusPending,
		Approval: &Approval{Required: true, Reason: "second approval"},
		Timing:   initial.Slices.Slices[0].Timing,
	})
	initial.State.Plan.PendingSlices = append(initial.State.Plan.PendingSlices, "002-b")
	writeStartSliceArtifacts(t, dir, initial)

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := detailFromFiles(files)
	first, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ApproveSlice("001-a", "alice", time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	second, err := NewPlanRecord(dir, stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.ApproveSlice("002-b", "bob", time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	files, err = loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	settled := detailFromFiles(files)
	for _, sliceID := range []string{"001-a", "002-b"} {
		slice := findSlice(settled, sliceID)
		if slice == nil || slice.Approval == nil || !slice.Approval.Approved {
			t.Fatalf("approval for %s was lost after stale detail was bound: %#v", sliceID, slice)
		}
	}
	for _, sliceID := range []string{"001-a", "002-b"} {
		matching := 0
		for _, event := range files.events {
			if event.Type == EventTypeSliceApproved && event.SliceID == sliceID {
				matching++
			}
		}
		if matching != 1 {
			t.Fatalf("approval events for %s = %d, want 1", sliceID, matching)
		}
	}
}

func TestPlanRecordPersistArtifactsPreservesLifecycleSettledAfterBinding(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.Title = "Caller plan title"
	detail.Slices.Slices[0].Title = "Caller slice title"

	concurrentRecord, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC)
	if err := concurrentRecord.StartSlice("001-a", startedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled lifecycle mutation left journal: %v", err)
	}

	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.state.Plan.Title != "Caller plan title" || persisted.state.Status != StatusInProgress ||
		persisted.state.Plan.CurrentSlice == nil || *persisted.state.Plan.CurrentSlice != "001-a" {
		t.Fatalf("persisted state lost caller or concurrent edits: %#v", persisted.state)
	}
	if persisted.slices.Slices[0].Title != "Caller slice title" || persisted.slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("persisted slice lost caller or concurrent edits: %#v", persisted.slices.Slices[0])
	}
	starts := 0
	for _, event := range persisted.events {
		if event.Type == EventTypeSliceStarted && event.SliceID == "001-a" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("slice-start events = %d, want 1", starts)
	}
	if detail.State.Status != StatusInProgress || detail.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("published detail lost concurrent lifecycle mutation: %#v", detail)
	}
}

func TestPlanRecordPersistArtifactsPreservesConcurrentReopenSlices(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reopen")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := completedReopenDetail()
	initial.Dir = dir
	writeStartSliceArtifacts(t, dir, initial)

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := detailFromFiles(files)
	record, err := NewPlanRecord(dir, stale)
	if err != nil {
		t.Fatal(err)
	}
	stale.Slices.Slices[0].Title = "Caller-edited completed slice"

	concurrent, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	reopenedAt := time.Date(2026, 7, 20, 18, 2, 0, 0, time.UTC)
	reopened := Slice{
		ID:     "002-fix",
		Title:  "Fix review finding",
		Status: StatusPending,
		Timing: SliceTiming{CreatedAt: reopenedAt, UpdatedAt: reopenedAt},
	}
	if err := concurrent.Reopen([]Slice{reopened}, reopenedAt); err != nil {
		t.Fatal(err)
	}

	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.state.Status != StatusInProgress || !reflect.DeepEqual(persisted.state.Plan.PendingSlices, []string{"002-fix"}) {
		t.Fatalf("persisted state lost concurrent reopen: %#v", persisted.state)
	}
	if len(persisted.slices.Slices) != 2 || persisted.slices.Slices[0].Title != "Caller-edited completed slice" {
		t.Fatalf("persisted slices lost caller edit or concurrent addition: %#v", persisted.slices.Slices)
	}
	if added := findSlice(detailFromFiles(persisted), "002-fix"); added == nil || added.Status != StatusPending {
		t.Fatalf("persisted slices lost reopened slice: %#v", persisted.slices.Slices)
	}
	reopenEvents := 0
	for _, event := range persisted.events {
		if event.Type == EventTypePlanReopened {
			reopenEvents++
		}
	}
	if reopenEvents != 1 {
		t.Fatalf("plan-reopened events = %d, want 1", reopenEvents)
	}
	if len(stale.Slices.Slices) != 2 || stale.State.Status != StatusInProgress {
		t.Fatalf("published detail lost concurrent reopen: %#v", stale)
	}
}

func TestPlanRecordPersistArtifactsPreservesConcurrentSliceRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := startSliceDetail(dir)
	created := initial.State.CreatedAt
	initial.State.Plan.PendingSlices = append(initial.State.Plan.PendingSlices, "002-b")
	initial.Slices.Slices = append(initial.Slices.Slices, Slice{
		ID: "002-b", Title: "B", Status: StatusPending,
		Timing: SliceTiming{CreatedAt: created, UpdatedAt: created},
	})
	writeStartSliceArtifacts(t, dir, initial)

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := detailFromFiles(files)
	record, err := NewPlanRecord(dir, stale)
	if err != nil {
		t.Fatal(err)
	}
	stale.Slices.Slices[0].Title = "Caller-edited remaining slice"

	concurrent, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	removedAt := time.Date(2026, 7, 20, 18, 3, 0, 0, time.UTC)
	if err := concurrent.RemoveSlice("002-b", removedAt); err != nil {
		t.Fatal(err)
	}

	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted.state.Plan.PendingSlices, []string{"001-a"}) {
		t.Fatalf("persisted state lost concurrent removal: %#v", persisted.state.Plan.PendingSlices)
	}
	if len(persisted.slices.Slices) != 1 || persisted.slices.Slices[0].ID != "001-a" || persisted.slices.Slices[0].Title != "Caller-edited remaining slice" {
		t.Fatalf("persisted slices lost caller edit or concurrent removal: %#v", persisted.slices.Slices)
	}
	removedEvents := 0
	for _, event := range persisted.events {
		if event.Type == EventTypeSliceRemoved && event.SliceID == "002-b" {
			removedEvents++
		}
	}
	if removedEvents != 1 {
		t.Fatalf("slice-removed events = %d, want 1", removedEvents)
	}
	if len(stale.Slices.Slices) != 1 || stale.Slices.Slices[0].Title != "Caller-edited remaining slice" {
		t.Fatalf("published detail lost caller edit or concurrent removal: %#v", stale.Slices.Slices)
	}
}

func TestPlanRecordPersistArtifactsRejectsConcurrentStructuralChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, initial)

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := detailFromFiles(files)
	record, err := NewPlanRecord(dir, stale)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := initial.State.CreatedAt.Add(time.Minute)
	stale.State.Plan.PendingSlices = append(stale.State.Plan.PendingSlices, "002-b")
	stale.Slices.Slices = append(stale.Slices.Slices, Slice{
		ID: "002-b", Title: "B", Status: StatusPending,
		Timing: SliceTiming{CreatedAt: createdAt, UpdatedAt: createdAt},
	})

	concurrent, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	removedAt := createdAt.Add(time.Minute)
	if err := concurrent.RemoveSlice("001-a", removedAt); err != nil {
		t.Fatal(err)
	}

	err = record.PersistArtifacts()
	if err == nil || !strings.Contains(err.Error(), "concurrent structural change") {
		t.Fatalf("PersistArtifacts error = %v, want concurrent structural change", err)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.state.Plan.PendingSlices) != 0 || len(persisted.slices.Slices) != 0 {
		t.Fatalf("rejected merge changed settled structure: state=%#v slices=%#v", persisted.state.Plan.PendingSlices, persisted.slices.Slices)
	}
	if !reflect.DeepEqual(stale.State.Plan.PendingSlices, []string{"001-a", "002-b"}) || len(stale.Slices.Slices) != 2 {
		t.Fatalf("rejected merge changed caller intent: state=%#v slices=%#v", stale.State.Plan.PendingSlices, stale.Slices.Slices)
	}
	if _, statErr := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected merge installed a journal: %v", statErr)
	}
}

func TestPlanRecordPersistArtifactsRetryDoesNotRepublishStaleLifecycleState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "remove"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.Title = "Caller plan title"
	detail.Slices.Slices[0].Title = "Caller slice title"

	concurrentRecord, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC)
	if err := concurrentRecord.StartSlice("001-a", startedAt); err != nil {
		t.Fatal(err)
	}

	err = record.PersistArtifacts()
	if err == nil || !strings.Contains(err.Error(), "injected remove failure") {
		t.Fatalf("persist error = %v, want injected remove failure", err)
	}
	if detail.State.Status != StatusPlanned || detail.Slices.Slices[0].Status != StatusPending {
		t.Fatalf("failed persist published refreshed lifecycle state: %#v", detail)
	}

	files, err = loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleRecord, err := NewPlanRecord(dir, detailFromFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	blockedAt := startedAt.Add(time.Minute)
	if err := lifecycleRecord.BlockSlice("001-a", "waiting on dependency", blockedAt); err != nil {
		t.Fatal(err)
	}

	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.state.Plan.Title != "Caller plan title" || persisted.state.Status != StatusBlocked {
		t.Fatalf("retry lost caller edit or newer lifecycle state: %#v", persisted.state)
	}
	if persisted.slices.Slices[0].Title != "Caller slice title" || persisted.slices.Slices[0].Status != StatusBlocked {
		t.Fatalf("retry lost caller edit or newer slice state: %#v", persisted.slices.Slices[0])
	}
	blockedEvents := 0
	for _, event := range persisted.events {
		if event.Type == EventTypeSliceBlocked && event.SliceID == "001-a" {
			blockedEvents++
		}
	}
	if blockedEvents != 1 {
		t.Fatalf("slice-blocked events = %d, want 1", blockedEvents)
	}
	if detail.State.Status != StatusBlocked || detail.Slices.Slices[0].Status != StatusBlocked {
		t.Fatalf("successful retry did not publish settled lifecycle state: %#v", detail)
	}
}

func TestPlanRecordPersistArtifactsRecoversFailurePrefixes(t *testing.T) {
	for _, operation := range []string{"journal", "state", "slices", "remove"} {
		t.Run(operation, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "plan-a")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeStartSliceArtifacts(t, dir, startSliceDetail(dir))
			files, err := loadPlanFiles(dir)
			if err != nil {
				t.Fatal(err)
			}
			detail := detailFromFiles(files)
			detail.State.Plan.Title = "Updated plan"
			detail.Slices.Slices[0].Title = "Updated slice"

			ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: operation}
			store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
			record, err := newPlanRecord(store, dir, detail)
			if err != nil {
				t.Fatal(err)
			}

			err = record.PersistArtifacts()
			if err == nil || !strings.Contains(err.Error(), "injected "+operation+" failure") {
				t.Fatalf("persist error = %v, want injected %s failure", err, operation)
			}

			stateBeforeRecovery := readStateFile(t, dir)
			slicesBeforeRecovery := readSlicesFile(t, dir)
			switch operation {
			case "journal", "state":
				if stateBeforeRecovery.Plan.Title != "Plan A" || slicesBeforeRecovery.Slices[0].Title != "A" {
					t.Fatalf("targets changed before first successful install: state=%q slice=%q", stateBeforeRecovery.Plan.Title, slicesBeforeRecovery.Slices[0].Title)
				}
			case "slices":
				if stateBeforeRecovery.Plan.Title != "Updated plan" || slicesBeforeRecovery.Slices[0].Title != "A" {
					t.Fatalf("unexpected state-only prefix: state=%q slice=%q", stateBeforeRecovery.Plan.Title, slicesBeforeRecovery.Slices[0].Title)
				}
			case "remove":
				if stateBeforeRecovery.Plan.Title != "Updated plan" || slicesBeforeRecovery.Slices[0].Title != "Updated slice" {
					t.Fatalf("unexpected fully installed prefix: state=%q slice=%q", stateBeforeRecovery.Plan.Title, slicesBeforeRecovery.Slices[0].Title)
				}
			}

			_, journalErr := os.Stat(filepath.Join(dir, mutationJournalFile))
			if operation == "journal" {
				if !errors.Is(journalErr, os.ErrNotExist) {
					t.Fatalf("journal installation failure left journal: %v", journalErr)
				}
			} else if journalErr != nil {
				t.Fatalf("durable failure prefix has no journal: %v", journalErr)
			}

			files, err = loadPlanFiles(dir)
			if err != nil {
				t.Fatalf("load after failed persist: %v", err)
			}
			if operation == "journal" {
				if files.state.Plan.Title != "Plan A" || files.slices.Slices[0].Title != "A" {
					t.Fatalf("journal installation failure changed artifacts: state=%q slice=%q", files.state.Plan.Title, files.slices.Slices[0].Title)
				}
			} else if files.state.Plan.Title != "Updated plan" || files.slices.Slices[0].Title != "Updated slice" {
				t.Fatalf("recovered artifacts = state:%q slice:%q", files.state.Plan.Title, files.slices.Slices[0].Title)
			}
			if len(files.events) != 0 {
				t.Fatalf("PersistArtifacts journal appended events: %#v", files.events)
			}
			if _, statErr := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("journal remains after recovery: %v", statErr)
			}
		})
	}
}

func TestPlanRecordPersistArtifactsRebasesEditsOverJournalCreatedAfterBinding(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, dir, startSliceDetail(dir))

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	detail := detailFromFiles(files)
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.Title = "Caller plan title"
	detail.Slices.Slices[0].Title = "Caller slice title"

	// Install concurrent intent after the record captured its baseline, then
	// fail before any target changes so PersistArtifacts must recover it.
	concurrent := detailFromFiles(files)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	concurrentRecord, err := newPlanRecord(store, dir, concurrent)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 20, 18, 1, 0, 0, time.UTC)
	if err := concurrentRecord.StartSlice("001-a", startedAt); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("concurrent start error = %v, want injected state failure", err)
	}

	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}

	persisted, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.state.Plan.Title != "Caller plan title" || persisted.state.Status != StatusInProgress {
		t.Fatalf("persisted state lost caller or recovered edits: %#v", persisted.state)
	}
	if persisted.slices.Slices[0].Title != "Caller slice title" || persisted.slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("persisted slice lost caller or recovered edits: %#v", persisted.slices.Slices[0])
	}
	if detail.State.Plan.Title != "Caller plan title" || detail.State.Status != StatusInProgress ||
		detail.Slices.Slices[0].Title != "Caller slice title" || detail.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("published detail lost caller or recovered edits: %#v", detail)
	}
	starts := 0
	for _, event := range persisted.events {
		if event.Type == EventTypeSliceStarted && event.SliceID == "001-a" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("recovered start events = %d, want 1", starts)
	}
}

func TestArtifactMutationRejectsInvalidLifecycleMutationWithoutWrites(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	original := clonePlanDetail(detail)
	store := &recordingArtifactMutationStore{}

	err := applyArtifactMutation(store, detail.Dir, detail, completeSliceMutation("001-a", "done", nil, time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)))
	if err == nil || !strings.Contains(err.Error(), "has no started_at") {
		t.Fatalf("expected invalid completion error, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("invalid mutation should not write artifacts, got calls %v", store.calls)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("detail changed after invalid mutation:\n got: %#v\nwant: %#v", detail, original)
	}
}

// TestPlanRecordLoadsLegacyStateAdvancedTornWrite keeps the compatibility path
// explicit for plans torn before lifecycle mutations gained a journal.
func TestPlanRecordLoadsLegacyStateAdvancedTornWrite(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 5, 3, 23, 31, 31, 0, time.UTC)
	completed := started.Add(time.Minute)
	detail := startSliceDetail(dir)
	detail.State.Status = StatusInProgress
	detail.State.Plan.CurrentSlice = &detail.Slices.Slices[0].ID
	detail.State.Plan.Timing.StartedAt = &started
	detail.Slices.Slices[0].Status = StatusInProgress
	detail.Slices.Slices[0].Timing.StartedAt = &started
	writeStartSliceArtifacts(t, dir, detail)

	mutation, err := completeSliceMutation("001-a", "done", nil, completed)(clonePlanDetail(detail))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := prepareJSON(filepath.Join(dir, "state.json"), mutation.State, stateJSONChanges(mutation.Changes))
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(filepath.Join(dir, "state.json"), payload); err != nil {
		t.Fatal(err)
	}

	// The legacy fixture has advanced state.json and untouched slices.json, with
	// no journal available to replay the missing target.
	state := readStateFile(t, dir)
	if state.Status != StatusInReview || state.Plan.CurrentSlice != nil {
		t.Fatalf("expected state.json advanced to in_review with no current_slice, got %#v", state)
	}
	// slices.json on disk is untouched: it still shows the prior in_progress slice.
	slices := readSlicesFile(t, dir)
	if slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("expected slices.json to remain at in_progress, got %#v", slices.Slices[0])
	}

	// A subsequent load still succeeds and degrades to a validate warning.
	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatalf("expected plan to remain loadable after cross-file failure, got %v", err)
	}
	loaded := detailFromFiles(files)
	const wantWarning = "slice is in_progress but state.json current_slice is null"
	found := slices0.Contains(loaded.Warnings, wantWarning)
	if !found {
		t.Fatalf("expected cross-file inconsistency warning %q, got %v", wantWarning, loaded.Warnings)
	}
}

func TestLegacyAutomaticCompletionTornWriteRequiresOutcomeRecovery(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	completed := started.Add(time.Minute)
	detail := startSliceDetail(dir)
	detail.State.Status = StatusInProgress
	detail.State.Plan.CurrentSlice = &detail.Slices.Slices[0].ID
	detail.State.Plan.Timing.StartedAt = &started
	detail.Slices.Slices[0].Status = StatusInProgress
	detail.Slices.Slices[0].Timing.StartedAt = &started
	detail.Slices.Slices[0].CommitIntent = &SliceCommitIntent{
		Hash: "intent", Policy: "slice", StartingBranch: "tao/plan-a", StartingHead: "parent", Message: "message", CreatedAt: started,
	}
	writeStartSliceArtifacts(t, dir, detail)

	// Reproduce a pre-journal state-first torn write explicitly: state advanced,
	// slices (including the commit outcome) and event still at their old values.
	outcome := SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "recorded-commit"}
	mutation, err := completeSliceWithOutcomeMutation("001-a", "done", nil, &outcome, completed)(clonePlanDetail(detail))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := prepareJSON(filepath.Join(dir, "state.json"), mutation.State, stateJSONChanges(mutation.Changes))
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(filepath.Join(dir, "state.json"), payload); err != nil {
		t.Fatal(err)
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded := detailFromFiles(files)
	lifecycle := AnalyzeLifecycle(loaded)
	if lifecycle.Complete || lifecycle.Runnable || lifecycle.RunnableError == nil || !strings.Contains(lifecycle.RunnableError.Error(), "completion outcome is missing") {
		t.Fatalf("torn automatic completion must remain recovery-gated: %+v", lifecycle)
	}
	loadedRecord := testRecord(dir, loaded)
	if err := loadedRecord.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, ReviewedAt: completed}, "pi"); err == nil || !strings.Contains(err.Error(), "completion outcome is missing") {
		t.Fatalf("review should reject unsettled completion, got %v", err)
	}

	if err := loadedRecord.CompleteSliceWithOutcome("001-a", "done", nil, outcome, completed); err != nil {
		t.Fatalf("persist recovered commit outcome: %v", err)
	}
	if lifecycle := AnalyzeLifecycle(loaded); !lifecycle.Complete {
		t.Fatalf("recovered completion did not settle plan: %+v", lifecycle)
	}
}

func TestMarkBlockedContinuedRestartsBlockedSlice(t *testing.T) {
	now := time.Date(2026, 5, 3, 23, 45, 0, 0, time.UTC)
	detail := startSliceDetail("")
	detail.Slices.Slices[0].Status = StatusBlocked

	if err := MarkBlockedContinued(detail, now); err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != StatusInProgress || detail.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("expected blocked slice restart, got state=%q slice=%q", detail.State.Status, detail.Slices.Slices[0].Status)
	}
}

func TestMarkBlockedContinuedFallsBackToFirstPending(t *testing.T) {
	now := time.Date(2026, 5, 3, 23, 45, 0, 0, time.UTC)
	detail := startSliceDetail("")
	detail.State.Status = StatusBlocked
	detail.State.Plan.PendingSlices = []string{"002-b"}
	detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusPending})

	if err := MarkBlockedContinued(detail, now); err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "002-b" || detail.Slices.Slices[1].Status != StatusInProgress {
		t.Fatalf("expected first pending slice selected, got current=%v slice=%q", detail.State.Plan.CurrentSlice, detail.Slices.Slices[1].Status)
	}
}

func TestMarkBlockedContinuedRejectsSafeguards(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlanDetail)
		want   string
	}{
		{name: "not blocked", want: "continue is not meaningful"},
		{name: "complete", mutate: func(detail *PlanDetail) { detail.State.Status = StatusCompleted }, want: "is complete"},
		{name: "missing slice", mutate: func(detail *PlanDetail) { detail.State.Status = StatusBlocked; detail.Slices.Slices = nil }, want: "not found"},
		{name: "approval", mutate: func(detail *PlanDetail) {
			detail.State.Status = StatusBlocked
			detail.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "approval"}
		}, want: "requires approval"},
		{name: "dependency", mutate: func(detail *PlanDetail) {
			detail.State.Status = StatusBlocked
			detail.Slices.Slices[0].DependsOn = []string{"000-base"}
		}, want: "incomplete dependencies"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := startSliceDetail("")
			if tt.mutate != nil {
				tt.mutate(detail)
			}

			err := MarkBlockedContinued(detail, time.Date(2026, 5, 3, 23, 45, 0, 0, time.UTC))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func startSliceDetail(dir string) *PlanDetail {
	created := time.Date(2026, 5, 3, 23, 0, 0, 0, time.UTC)
	return &PlanDetail{
		Dir: dir,
		State: State{
			Schema:    "tao.plan.state.v1",
			Status:    StatusPlanned,
			CreatedAt: created,
			UpdatedAt: created,
			Plan: PlanState{
				ID:            "plan-a",
				Title:         "Plan A",
				PendingSlices: []string{"001-a"},
			},
		},
		Slices: SlicesFile{
			Schema: "tao.plan.slices.v1",
			PlanID: "plan-a",
			Slices: []Slice{{
				ID:     "001-a",
				Title:  "A",
				Status: StatusPending,
				Timing: SliceTiming{CreatedAt: created, UpdatedAt: created},
			}},
		},
	}
}

func writeStartSliceArtifacts(t *testing.T, dir string, detail *PlanDetail) {
	t.Helper()
	if err := writeState(dir, detail.State); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(dir, detail.Slices); err != nil {
		t.Fatal(err)
	}
}

func addUnknownPlanField(t *testing.T, dir, key string, value any) {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	var raw map[string]any
	readJSONFile(t, path, &raw)
	raw["plan"].(map[string]any)[key] = value
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func addUnknownSlicesField(t *testing.T, dir, key string, value any) {
	t.Helper()
	path := filepath.Join(dir, "slices.json")
	var raw map[string]any
	readJSONFile(t, path, &raw)
	raw[key] = value
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readStateFile(t *testing.T, dir string) State {
	t.Helper()
	var state State
	readJSONFile(t, filepath.Join(dir, "state.json"), &state)
	return state
}

func readSlicesFile(t *testing.T, dir string) SlicesFile {
	t.Helper()
	var slices SlicesFile
	readJSONFile(t, filepath.Join(dir, "slices.json"), &slices)
	return slices
}

func readJSONFile(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test path is internally constructed
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func readEventsFile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl")) // #nosec G304 -- test path is internally constructed
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type recordingArtifactMutationStore struct {
	calls          []string
	state          State
	slices         SlicesFile
	events         []Event
	writeStateErr  error
	writeSlicesErr error
	appendEventErr error
}

func (s *recordingArtifactMutationStore) writeState(planDir string, state State) error {
	s.calls = append(s.calls, "state")
	s.state = state
	return s.writeStateErr
}

func (s *recordingArtifactMutationStore) writeSlices(_ string, slices SlicesFile) error {
	s.calls = append(s.calls, "slices")
	s.slices = slices
	return s.writeSlicesErr
}

func (s *recordingArtifactMutationStore) appendEvent(_ string, event Event) error {
	s.calls = append(s.calls, "event")
	if s.appendEventErr != nil {
		return s.appendEventErr
	}
	s.events = append(s.events, event)
	return nil
}

func (s *recordingArtifactMutationStore) withMutationLock(_ string, operation func() error) error {
	return operation()
}

func (s *recordingArtifactMutationStore) refreshMutationDetailLocked(string, string, bool) (mutationDetailRefresh, error) {
	return mutationDetailRefresh{}, nil
}

func (s *recordingArtifactMutationStore) settleMutationLocked(planDir string, journal mutationJournal) error {
	if journal.State != nil {
		var state State
		if err := json.Unmarshal(journal.State.Payload, &state); err != nil {
			return err
		}
		if err := s.writeState(planDir, state); err != nil {
			return err
		}
	}
	if journal.Slices != nil {
		var slicesFile SlicesFile
		if err := json.Unmarshal(journal.Slices.Payload, &slicesFile); err != nil {
			return err
		}
		if err := s.writeSlices(planDir, slicesFile); err != nil {
			return err
		}
	}
	for _, entry := range journal.Events {
		var event Event
		if err := json.Unmarshal(entry.Payload, &event); err != nil {
			return err
		}
		if err := s.appendEvent(planDir, event); err != nil {
			return fmt.Errorf("append events.jsonl: %w", err)
		}
	}
	return nil
}

type signalingArtifactMutationStore struct {
	fileArtifactStore
	entered chan struct{}
	once    sync.Once
}

func (s *signalingArtifactMutationStore) withMutationLock(planDir string, operation func() error) error {
	s.once.Do(func() { close(s.entered) })
	return s.fileArtifactStore.withMutationLock(planDir, operation)
}

type blockingRemoveMutationJournalIO struct {
	delegate mutationJournalIO
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (s *blockingRemoveMutationJournalIO) readFile(path string) ([]byte, error) {
	return s.delegate.readFile(path)
}

func (s *blockingRemoveMutationJournalIO) installJournal(path string, data []byte) error {
	return s.delegate.installJournal(path, data)
}

func (s *blockingRemoveMutationJournalIO) installTarget(path string, data []byte) error {
	return s.delegate.installTarget(path, data)
}

func (s *blockingRemoveMutationJournalIO) appendEvent(path string, payload []byte) error {
	return s.delegate.appendEvent(path, payload)
}

func (s *blockingRemoveMutationJournalIO) syncEvents(path string) error {
	return s.delegate.syncEvents(path)
}

func (s *blockingRemoveMutationJournalIO) syncPlanDir(path string) error {
	return s.delegate.syncPlanDir(path)
}

func (s *blockingRemoveMutationJournalIO) removeJournal(path string) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.delegate.removeJournal(path)
}

type journalArtifactMutationStore struct {
	fileArtifactStore
	journalIO mutationJournalIO
}

func (s journalArtifactMutationStore) withMutationLock(planDir string, operation func() error) error {
	_, err := withMutationPersistenceLock(planDir, func() (struct{}, error) {
		return struct{}{}, operation()
	})
	return err
}

func (s journalArtifactMutationStore) settleMutationLocked(planDir string, journal mutationJournal) error {
	return installAndSettleMutationLocked(s.journalIO, planDir, journal)
}

func (s journalArtifactMutationStore) refreshMutationDetailLocked(planDir string, expectedPlanID string, force bool) (mutationDetailRefresh, error) {
	recovered, err := settlePendingMutationLocked(s.journalIO, planDir, expectedPlanID)
	if err != nil {
		return mutationDetailRefresh{}, fmt.Errorf("recover plan mutation: %w", err)
	}
	if !force && !recovered {
		return mutationDetailRefresh{}, nil
	}
	files, err := loadPlanFilesLocked(planDir)
	if err != nil {
		return mutationDetailRefresh{}, err
	}
	return mutationDetailRefresh{detail: detailFromFiles(files), refreshed: true, recovered: recovered}, nil
}

type errArtifactStore string

func (e errArtifactStore) Error() string { return string(e) }
