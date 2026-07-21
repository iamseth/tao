package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListPlansActiveUsesStateAndSlices(t *testing.T) {
	dir := t.TempDir()
	writePlan(t, dir, "active", `{
  "schema":"tao.plan.state.v1",
  "status":"in_progress",
  "created_at":"2026-04-27T18:10:50Z",
  "updated_at":"2026-04-27T18:17:41Z",
  "repo":{"name":"rollcall","root":"/repo","branch":"main"},
  "plan":{"id":"active","title":"Active Plan","current_slice":"001-a","completed_slices":[],"pending_slices":["001-a"],"timing":{"started_at":"2026-04-27T18:12:25Z","completed_at":null,"last_activity_at":"2026-04-27T18:17:41Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"active","execution":{"mode":"serial","parallel_safe":false},
  "slices":[{"id":"001-a","title":"Do work","status":"in_progress","depends_on":[],"timing":{"created_at":"2026-04-27T18:10:50Z","started_at":"2026-04-27T18:12:25Z","completed_at":null,"updated_at":"2026-04-27T18:17:41Z","last_activity_at":"2026-04-27T18:17:41Z","duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}}]
}`)
	writePlan(t, dir, "done", `{
  "schema":"tao.plan.state.v1",
  "status":"completed",
  "created_at":"2026-04-27T18:00:00Z",
  "updated_at":"2026-04-27T18:03:00Z",
  "repo":{"name":"rollcall","root":"/repo","branch":"main"},
  "plan":{"id":"done","title":"Done Plan","current_slice":null,"completed_slices":["001-a"],"pending_slices":[],"timing":{"started_at":"2026-04-27T18:00:00Z","completed_at":"2026-04-27T18:03:00Z","last_activity_at":"2026-04-27T18:03:00Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"done","execution":{"mode":"serial","parallel_safe":false},"slices":[]
}`)

	repo := NewFileRepository(dir)
	repo.Now = func() time.Time { return time.Date(2026, 4, 27, 18, 20, 0, 0, time.UTC) }

	plans, err := repo.ListPlans(context.Background(), PlanFilter{ActiveOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 active plan, got %d", len(plans))
	}
	if plans[0].ID != "active" || plans[0].CurrentSliceID != "001-a" {
		t.Fatalf("unexpected active summary: %+v", plans[0])
	}
}

func TestListPlansMissingDirReturnsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plans-that-do-not-exist")
	repo := NewFileRepository(dir)
	summaries, err := repo.ListPlans(context.Background(), PlanFilter{})
	if err != nil {
		t.Fatalf("expected missing plans dir to be treated as empty, got error: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("expected no summaries, got %d", len(summaries))
	}
}

func TestSliceDurationFallsBackToTimestamps(t *testing.T) {
	started := time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC)
	completed := started.Add(90 * time.Second)
	slice := Slice{Timing: SliceTiming{StartedAt: &started, CompletedAt: &completed}}
	if got := SliceDuration(slice, time.Now()); got != 90*time.Second {
		t.Fatalf("expected 90s, got %s", got)
	}
}

func TestPlanStateHelpers(t *testing.T) {
	started := time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC)
	completed := started.Add(2 * time.Minute)
	detail := &PlanDetail{
		State:  State{Status: StatusCompleted, Plan: PlanState{ID: "plan", Timing: PlanTiming{StartedAt: &started}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusCompleted, Timing: SliceTiming{CompletedAt: &completed}}}},
	}
	derived := Derive(detail, completed.Add(time.Hour))
	if !derived.Complete {
		t.Fatal("expected completed plan")
	}
	if got := derived.CompletedAt; got == nil || !got.Equal(completed) {
		t.Fatalf("expected completed time fallback, got %v", got)
	}
	if got := derived.Elapsed; got != 2*time.Minute {
		t.Fatalf("expected elapsed to stop at completion, got %s", got)
	}
	if err := derived.RunnableError; err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("expected complete runnable error, got %v", err)
	}

	detail.State.Status = StatusInProgress
	detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusInProgress})
	derived = Derive(detail, time.Time{})
	if got := derived.CurrentSliceID; got != "002-b" {
		t.Fatalf("expected current slice from slice status, got %q", got)
	}
	if got := derived.CurrentSlice; got == nil || got.ID != "002-b" {
		t.Fatalf("expected current slice pointer, got %+v", got)
	}

	detail.Slices.Slices[1].Status = StatusPlanned
	detail.State.Plan.PendingSlices = []string{"002-b"}
	derived = Derive(detail, time.Time{})
	if got := derived.CurrentSliceID; got != "" {
		t.Fatalf("expected no current slice for planned pending work, got %q", got)
	}
	if got := derived.NextSliceID; got != "002-b" {
		t.Fatalf("expected next slice from pending state, got %q", got)
	}
}

func TestNextRunnableSlice(t *testing.T) {
	detail := &PlanDetail{
		State: State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"002-b"}, CompletedSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Status: StatusCompleted},
			{ID: "002-b", Status: StatusPending, DependsOn: []string{"001-a"}},
		}},
	}
	derived := Derive(detail, time.Time{})
	if derived.RunnableError != nil {
		t.Fatal(derived.RunnableError)
	}
	if derived.NextSlice.ID != "002-b" {
		t.Fatalf("expected next runnable slice 002-b, got %q", derived.NextSlice.ID)
	}

	detail.State.Plan.CompletedSlices = nil
	derived = Derive(detail, time.Time{})
	if err := derived.RunnableError; err == nil || !strings.Contains(err.Error(), "incomplete dependencies: 001-a") {
		t.Fatalf("expected dependency error, got %v", err)
	}

	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices[1].Status = StatusBlocked
	derived = Derive(detail, time.Time{})
	if err := derived.RunnableError; err == nil || !strings.Contains(err.Error(), "slice 002-b is blocked") {
		t.Fatalf("expected blocked slice error, got %v", err)
	}

	detail.Slices.Slices[1].Status = StatusPending
	detail.Slices.Slices[1].Approval = &Approval{Required: true, Reason: "external approval", Approved: false}
	derived = Derive(detail, time.Time{})
	if err := derived.RunnableError; err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected approval error, got %v", err)
	}
	detail.Slices.Slices[1].Approval.Approved = true
	derived = Derive(detail, time.Time{})
	if derived.RunnableError != nil {
		t.Fatalf("expected approved slice to be runnable, got %v", derived.RunnableError)
	}
	detail.State.Status = StatusBlocked
	detail.Slices.Slices[1].Status = StatusBlocked
	if err := MarkBlockedContinued(detail, time.Time{}); err != nil {
		t.Fatalf("expected approved slice to enable continue checks, got %v", err)
	}
	detail.State.Status = StatusPlanned
	detail.Slices.Slices[1].Status = StatusPending

	detail.Slices.Slices = detail.Slices.Slices[:1]
	derived = Derive(detail, time.Time{})
	if err := derived.RunnableError; err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing slice error, got %v", err)
	}
}

func TestDeriveIgnoresStaleCompletedCurrentSliceWithPendingWork(t *testing.T) {
	current := "001-a"
	detail := &PlanDetail{
		State: State{Status: StatusInProgress, Plan: PlanState{ID: "plan", CurrentSlice: &current, PendingSlices: []string{"002-b"}, CompletedSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Status: StatusCompleted},
			{ID: "002-b", Status: StatusPending},
		}},
	}

	derived := Derive(detail, time.Time{})
	if derived.CurrentSliceID != "" || derived.CurrentSlice != nil {
		t.Fatalf("expected stale completed current slice to be ignored, got %q", derived.CurrentSliceID)
	}
	if derived.NextSliceID != "002-b" || derived.NextSlice == nil || derived.NextSlice.ID != "002-b" {
		t.Fatalf("expected first pending slice to be selected, got %+v", derived.Lifecycle)
	}
	if !derived.Runnable || derived.RunnableError != nil || derived.Complete {
		t.Fatalf("expected recovered pending slice to be runnable and incomplete, got %+v", derived.Lifecycle)
	}
}

func TestDeriveKeepsActiveEmptyPendingPlansIncompleteAndNotRunnable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{name: "in-progress", status: StatusInProgress},
		{name: "current", status: StatusPlanned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := "001-a"
			detail := &PlanDetail{
				State:  State{Status: tc.status, Plan: PlanState{ID: "plan", CurrentSlice: &current}},
				Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
			}

			derived := Derive(detail, time.Time{})
			if derived.Complete {
				t.Fatalf("expected active empty-pending plan to remain incomplete: %+v", derived.Lifecycle)
			}
			if !derived.Active {
				t.Fatalf("expected active empty-pending plan to remain active: %+v", derived.Lifecycle)
			}
			if derived.Runnable || derived.RunnableError == nil || !strings.Contains(derived.RunnableError.Error(), "no pending slices") {
				t.Fatalf("expected no-pending runnable error, got %+v", derived.Lifecycle)
			}
		})
	}
}

func TestRunCapabilitiesFromLifecycle(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
	}
	capabilities := AnalyzeRunCapabilities(detail)
	if !capabilities.CanRun || capabilities.DisabledReason != "" || capabilities.CanContinue || capabilities.Complete || capabilities.Active {
		t.Fatalf("expected runnable capabilities, got %+v", capabilities)
	}

	detail.State.Status = StatusCompleted
	detail.State.Plan.PendingSlices = nil
	capabilities = AnalyzeRunCapabilities(detail)
	if capabilities.CanRun || !capabilities.Complete || capabilities.DisabledReason == "" {
		t.Fatalf("expected disabled complete capabilities, got %+v", capabilities)
	}
}

func TestRunCapabilitiesContinueMatchesBlockedLifecycle(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*PlanDetail)
		wantEnabled bool
		wantReason  string
	}{
		{name: "blocked plan", mutate: func(detail *PlanDetail) { detail.State.Status = StatusBlocked }, wantEnabled: true},
		{name: "blocked selected slice", mutate: func(detail *PlanDetail) { detail.Slices.Slices[0].Status = StatusBlocked }, wantEnabled: true},
		{name: "complete", mutate: func(detail *PlanDetail) { detail.State.Status = StatusCompleted }, wantReason: "is complete"},
		{name: "missing slice", mutate: func(detail *PlanDetail) { detail.State.Status = StatusBlocked; detail.Slices.Slices = nil }, wantReason: "not found"},
		{name: "approval", mutate: func(detail *PlanDetail) {
			detail.State.Status = StatusBlocked
			detail.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "approval"}
		}, wantReason: "requires approval"},
		{name: "dependency", mutate: func(detail *PlanDetail) {
			detail.State.Status = StatusBlocked
			detail.Slices.Slices[0].DependsOn = []string{"000-base"}
		}, wantReason: "incomplete dependencies"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := startSliceDetail("")
			if tt.mutate != nil {
				tt.mutate(detail)
			}

			capabilities := AnalyzeRunCapabilities(detail)
			if capabilities.CanContinue != tt.wantEnabled {
				t.Fatalf("expected can_continue=%v, got %+v", tt.wantEnabled, capabilities)
			}
			if tt.wantReason != "" && !strings.Contains(capabilities.ContinueDisabledReason, tt.wantReason) {
				t.Fatalf("expected continue disabled reason %q, got %+v", tt.wantReason, capabilities)
			}
			if tt.wantEnabled && capabilities.ContinueDisabledReason != "" {
				t.Fatalf("expected no continue disabled reason, got %+v", capabilities)
			}
		})
	}
}

func TestListPlansUsesSliceStatusForStaleCompletedState(t *testing.T) {
	dir := t.TempDir()
	writePlan(t, dir, "stale", `{
  "schema":"tao.plan.state.v1",
  "status":"completed",
  "created_at":"2026-04-27T18:10:50Z",
  "updated_at":"2026-04-27T18:31:23Z",
  "repo":{"name":"rollcall","root":"/repo","branch":"main"},
  "plan":{"id":"stale","title":"Stale Plan","current_slice":"002-b","completed_slices":["001-a"],"pending_slices":["002-b"],"timing":{"started_at":"2026-04-27T18:00:00Z","completed_at":null,"last_activity_at":"2026-04-27T18:31:23Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"stale","execution":{"mode":"serial","parallel_safe":false},
  "slices":[
    {"id":"001-a","title":"One","status":"completed","depends_on":[],"timing":{"created_at":"2026-04-27T18:00:00Z","started_at":"2026-04-27T18:00:00Z","completed_at":"2026-04-27T18:01:00Z","updated_at":"2026-04-27T18:01:00Z","last_activity_at":"2026-04-27T18:01:00Z","duration_seconds":60},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}},
    {"id":"002-b","title":"Two","status":"completed","depends_on":[],"timing":{"created_at":"2026-04-27T18:00:00Z","started_at":"2026-04-27T18:02:00Z","completed_at":"2026-04-27T18:05:00Z","updated_at":"2026-04-27T18:05:00Z","last_activity_at":"2026-04-27T18:05:00Z","duration_seconds":180},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}}
  ]
}`)

	repo := NewFileRepository(dir)
	repo.Now = func() time.Time { return time.Date(2026, 4, 27, 19, 0, 0, 0, time.UTC) }
	summaries, err := repo.ListPlans(context.Background(), PlanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	summary := summaries[0]
	if summary.CompletedCount != 2 || summary.PendingCount != 0 || summary.CurrentSliceID != "" {
		t.Fatalf("summary used stale state metadata: %+v", summary)
	}
	if summary.Active() {
		t.Fatalf("completed summary should not be active: %+v", summary)
	}
	if summary.CompletedAt == nil || !summary.CompletedAt.Equal(time.Date(2026, 4, 27, 18, 5, 0, 0, time.UTC)) {
		t.Fatalf("expected completed_at fallback from latest completed slice, got %v", summary.CompletedAt)
	}
}

func TestListPlansIncludesInvalidPlanWarningsAndSortFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "invalid"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeMinimalPlan(t, dir, "valid", "Valid Plan")

	repo := NewFileRepository(dir)
	summaries, err := repo.ListPlans(context.Background(), PlanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected valid and invalid summaries, got %d", len(summaries))
	}
	foundInvalid := false
	for _, summary := range summaries {
		if summary.ID == "invalid" {
			foundInvalid = true
			if summary.Status != StatusInvalid || len(summary.Warnings) == 0 {
				t.Fatalf("expected invalid warning summary, got %+v", summary)
			}
		}
	}
	if !foundInvalid {
		t.Fatal("expected invalid directory summary")
	}
}

func TestPlanValidationAndEventWarnings(t *testing.T) {
	dir := t.TempDir()
	writePlan(t, dir, "warn", `{
  "schema":"tao.plan.state.v1",
  "status":"in_progress",
  "created_at":"2026-04-27T18:10:50Z",
  "updated_at":"2026-04-27T18:10:50Z",
  "repo":{"name":"rollcall","root":"/repo","branch":"main"},
  "plan":{"id":"warn","title":"Warn Plan","current_slice":"missing","completed_slices":[],"pending_slices":[],"timing":{"started_at":null,"completed_at":null,"last_activity_at":"2026-04-27T18:10:50Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"other","execution":{"mode":"serial","parallel_safe":false},
  "slices":[{"id":"001-a","title":"A","status":"in_progress","depends_on":[],"timing":{"created_at":"2026-04-27T18:10:50Z","started_at":null,"completed_at":null,"updated_at":"2026-04-27T18:10:50Z","last_activity_at":null,"duration_seconds":null},"goal":"","context":"","tasks":[],"expected_files":[],"verification":{"commands":[],"manual_checks":[]}}]
}`)
	if err := os.WriteFile(filepath.Join(dir, "warn", "events.jsonl"), []byte("not json\n{\"type\":\"plan_created\",\"timestamp\":\"2026-04-27T18:10:50Z\",\"plan_id\":\"warn\",\"message\":\"ok\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := NewFileRepository(dir).GetPlan(context.Background(), "warn")
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(detail.Warnings, "\n")
	for _, want := range []string{"events.jsonl line 1", "does not match", "current_slice does not exist"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("expected warning %q in %q", want, warnings)
		}
	}
	if len(detail.Events) != 1 {
		t.Fatalf("expected one valid event, got %d", len(detail.Events))
	}
}

func writePlan(t *testing.T, root, id, state, slices string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), []byte(slices), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeMinimalPlan(t *testing.T, root, id, title string) {
	t.Helper()
	writePlan(t, root, id, `{
  "schema":"tao.plan.state.v1",
  "status":"planned",
  "created_at":"2026-04-27T18:10:50Z",
  "updated_at":"2026-04-27T18:10:50Z",
  "repo":{"name":"rollcall","root":"/repo","branch":"main"},
  "plan":{"id":"`+id+`","title":"`+title+`","current_slice":null,"completed_slices":[],"pending_slices":[],"timing":{"started_at":null,"completed_at":null,"last_activity_at":"2026-04-27T18:10:50Z"}},
  "global_invariants":[],"open_questions":[]
}`, `{
  "schema":"tao.plan.slices.v1","plan_id":"`+id+`","execution":{"mode":"serial","parallel_safe":false},"slices":[]
}`)
}

type recordingArtifactStore struct {
	fileArtifactStore
	removed []string
}

func (s *recordingArtifactStore) removeAll(path string) error {
	s.removed = append(s.removed, path)
	return nil
}
