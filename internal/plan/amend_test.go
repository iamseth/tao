package plan

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func amendTime() time.Time {
	return time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)
}

func amendRequest() SliceAmendmentRequest {
	return SliceAmendmentRequest{
		Reason:          "  reviewer needs a helper file  ",
		Goal:            " Amended goal ",
		AddTasks:        []string{" Add the helper ", ""},
		AllowFiles:      []string{"internal/x/helper.go"},
		AddManualChecks: []string{"Confirm the helper is exercised"},
	}
}

func blockedAmendDetail() *PlanDetail {
	detail := editPlanDetail()
	detail.State.Status = StatusBlocked
	detail.State.Plan.CurrentSlice = new("001-a")
	slice := &detail.Slices.Slices[0]
	slice.Status = StatusBlocked
	slice.BlockerNote = "expected_files is too narrow"
	slice.ExecutionRoot = "/work/edit"
	slice.ExecutionStart = &SliceExecutionStart{Branch: "feature/edit", Head: "abc123"}
	slice.Goal = "Original goal"
	slice.Tasks = []string{"Existing task"}
	slice.ExpectedFiles = []string{"internal/x/x.go"}
	slice.Verification.ManualChecks = []string{"Existing check"}
	return detail
}

func findEventByType(events []Event, eventType string) *Event {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

func TestAmendBlockedSliceMutatesOnlyRequestedFields(t *testing.T) {
	detail := blockedAmendDetail()
	original := clonePlanDetail(detail)
	now := amendTime()

	event, err := markSliceAmendedWithChanges(detail, newArtifactChangeSet(detail), "001-a", amendRequest(), now)
	if err != nil {
		t.Fatal(err)
	}
	slice := findSlice(detail, "001-a")
	if slice.Goal != "Amended goal" {
		t.Fatalf("goal = %q, want replaced goal", slice.Goal)
	}
	if !slices.Equal(slice.Tasks, []string{"Existing task", "Add the helper"}) {
		t.Fatalf("tasks = %v", slice.Tasks)
	}
	if !slices.Equal(slice.ExpectedFiles, []string{"internal/x/x.go", "internal/x/helper.go"}) {
		t.Fatalf("expected files = %v", slice.ExpectedFiles)
	}
	if !slices.Equal(slice.Verification.ManualChecks, []string{"Existing check", "Confirm the helper is exercised"}) {
		t.Fatalf("manual checks = %v", slice.Verification.ManualChecks)
	}
	before := findSlice(original, "001-a")
	if slice.Status != StatusBlocked || slice.BlockerNote != before.BlockerNote || slice.ExecutionRoot != before.ExecutionRoot || !reflect.DeepEqual(slice.ExecutionStart, before.ExecutionStart) {
		t.Fatalf("amend touched blocker or execution fields: %#v", slice)
	}
	if !slices.Equal(slice.Verification.Commands, before.Verification.Commands) || !slices.Equal(slice.DependsOn, before.DependsOn) {
		t.Fatalf("amend touched verification commands or dependencies: %#v", slice)
	}
	if detail.State.Status != StatusBlocked || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != "001-a" || !slices.Equal(detail.State.Plan.PendingSlices, original.State.Plan.PendingSlices) {
		t.Fatalf("amend changed plan state: %#v", detail.State)
	}
	if !slice.Timing.UpdatedAt.Equal(now) || !detail.State.UpdatedAt.Equal(now) {
		t.Fatalf("amend did not record activity: slice=%v state=%v", slice.Timing.UpdatedAt, detail.State.UpdatedAt)
	}
	wantFields := []string{"goal", "tasks", "expected_files", "manual_checks"}
	if len(slice.Amendments) != 1 {
		t.Fatalf("amendments = %#v, want exactly one", slice.Amendments)
	}
	amendment := slice.Amendments[0]
	if amendment.Reason != "reviewer needs a helper file" || !amendment.AmendedAt.Equal(now) || !slices.Equal(amendment.Fields, wantFields) {
		t.Fatalf("unexpected amendment: %#v", amendment)
	}
	if event.Type != EventTypeSliceAmended || event.SliceID != "001-a" || event.PlanID != "edit" || event.Reason != "reviewer needs a helper file" || !slices.Equal(event.AmendedFields, wantFields) || event.Message != "Slice contract amended by plan edit" || !event.Timestamp.Equal(now) {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestAmendRepairSliceAppendsOnly(t *testing.T) {
	for _, status := range []string{StatusPending, StatusBlocked} {
		t.Run(status, func(t *testing.T) {
			detail := blockedAmendDetail()
			slice := findSlice(detail, "001-a")
			slice.Status = status
			slice.VerificationRepair = &VerificationRepairBinding{Command: "make verify", HeadSHA: "failed-head", Fingerprint: "failure"}
			before := clonePlanDetail(detail)
			request := amendRequest()
			request.Goal = ""
			event, err := markSliceAmendedWithChanges(detail, newArtifactChangeSet(detail), slice.ID, request, amendTime())
			if err != nil {
				t.Fatal(err)
			}
			original := findSlice(before, slice.ID)
			if slice.Goal != original.Goal || slice.Status != status || !reflect.DeepEqual(slice.VerificationRepair, original.VerificationRepair) || !slices.Equal(slice.Verification.Commands, original.Verification.Commands) {
				t.Fatalf("amend changed frozen fields: %+v", slice)
			}
			if !slices.Equal(slice.ExpectedFiles, []string{"internal/x/x.go", "internal/x/helper.go"}) || !slices.Equal(slice.Tasks, []string{"Existing task", "Add the helper"}) || !slices.Equal(slice.Verification.ManualChecks, []string{"Existing check", "Confirm the helper is exercised"}) {
				t.Fatalf("missing appended contract fields: %+v", slice)
			}
			fields := []string{"tasks", "expected_files", "manual_checks"}
			if len(slice.Amendments) != 1 || slice.Amendments[0].Reason != "reviewer needs a helper file" || !slices.Equal(slice.Amendments[0].Fields, fields) || event.Type != EventTypeSliceAmended || !slices.Equal(event.AmendedFields, fields) {
				t.Fatalf("missing amendment evidence: %+v event=%+v", slice.Amendments, event)
			}
		})
	}
}

func TestAmendPendingSliceRecordsOnlyChangedFields(t *testing.T) {
	detail := editPlanDetail()
	detail.Slices.Slices[2].Goal = "Original goal"

	event, err := markSliceAmendedWithChanges(detail, newArtifactChangeSet(detail), "003-c", SliceAmendmentRequest{Reason: "clarify", Goal: "Clearer goal"}, amendTime())
	if err != nil {
		t.Fatal(err)
	}
	slice := findSlice(detail, "003-c")
	if slice.Status != StatusPending || slice.Goal != "Clearer goal" || len(slice.Tasks) != 0 || len(slice.ExpectedFiles) != 0 {
		t.Fatalf("unexpected pending slice after amend: %#v", slice)
	}
	if len(slice.Amendments) != 1 || !slices.Equal(slice.Amendments[0].Fields, []string{"goal"}) {
		t.Fatalf("unexpected amendments: %#v", slice.Amendments)
	}
	if event.Type != EventTypeSliceAmended || !slices.Equal(event.AmendedFields, []string{"goal"}) {
		t.Fatalf("unexpected event: %#v", event)
	}
	if !slices.Equal(detail.State.Plan.PendingSlices, []string{"001-a", "002-b", "003-c"}) {
		t.Fatalf("pending queue changed: %v", detail.State.Plan.PendingSlices)
	}
}

func TestAmendSliceDedupsExpectedFilesAndRecordsOnlyChangedFields(t *testing.T) {
	detail := blockedAmendDetail()

	request := SliceAmendmentRequest{Reason: "widen", AllowFiles: []string{" internal/x/x.go ", "internal/x/new.go", "internal/x/new.go"}, AddTasks: []string{"Existing task"}}
	event, err := markSliceAmendedWithChanges(detail, newArtifactChangeSet(detail), "001-a", request, amendTime())
	if err != nil {
		t.Fatal(err)
	}
	slice := findSlice(detail, "001-a")
	if !slices.Equal(slice.ExpectedFiles, []string{"internal/x/x.go", "internal/x/new.go"}) {
		t.Fatalf("expected files = %v, want dedup of existing entry", slice.ExpectedFiles)
	}
	if !slices.Equal(slice.Tasks, []string{"Existing task"}) {
		t.Fatalf("tasks = %v, want duplicate task skipped", slice.Tasks)
	}
	if len(slice.Amendments) != 1 || !slices.Equal(slice.Amendments[0].Fields, []string{"expected_files"}) || !slices.Equal(event.AmendedFields, []string{"expected_files"}) {
		t.Fatalf("unexpected amendment fields: %#v event=%#v", slice.Amendments, event)
	}
}

func TestAmendSliceRefusals(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*PlanDetail)
		sliceID  string
		request  SliceAmendmentRequest
		want     string
		notFound bool
	}{
		{name: "missing slice", sliceID: "999-z", request: amendRequest(), want: "slice 999-z not found", notFound: true},
		{name: "in_progress", setup: func(d *PlanDetail) { d.Slices.Slices[0].Status = StatusInProgress }, sliceID: "001-a", request: amendRequest(), want: "slice 001-a is in_progress; only pending or blocked slices can be amended"},
		{name: "completed", setup: func(d *PlanDetail) { d.Slices.Slices[0].Status = StatusCompleted }, sliceID: "001-a", request: amendRequest(), want: "slice 001-a is completed; only pending or blocked slices can be amended"},
		{name: "skipped", setup: func(d *PlanDetail) { d.Slices.Slices[0].Status = StatusSkipped }, sliceID: "001-a", request: amendRequest(), want: "slice 001-a is skipped; only pending or blocked slices can be amended"},
		{name: "verification repair", setup: func(d *PlanDetail) {
			d.Slices.Slices[2].VerificationRepair = &VerificationRepairBinding{Command: "make verify", HeadSHA: "failed-head", Fingerprint: "failure"}
		}, sliceID: "003-c", request: amendRequest(), want: "cannot replace the goal of generated verification-repair slice 003-c; amend may only append expected files, tasks, or manual checks with a recorded reason"},
		{name: "abandoned plan", setup: func(d *PlanDetail) { d.State.Status = StatusAbandoned }, sliceID: "001-a", request: amendRequest(), want: "plan edit is abandoned"},
		{name: "empty reason", sliceID: "001-a", request: SliceAmendmentRequest{Reason: "   ", Goal: "x"}, want: "amendment reason is required"},
		{name: "no change requested", sliceID: "001-a", request: SliceAmendmentRequest{Reason: "why", AddTasks: []string{"  "}}, want: "amendment must supply a goal, task, allowed file, or manual check"},
		{name: "unsafe allow-file", sliceID: "001-a", request: SliceAmendmentRequest{Reason: "why", AllowFiles: []string{"../outside.go"}}, want: "parent traversal"},
		{name: "absolute allow-file", sliceID: "001-a", request: SliceAmendmentRequest{Reason: "why", AllowFiles: []string{"/etc/passwd"}}, want: "absolute path"},
		{name: "all duplicates", setup: func(d *PlanDetail) {
			d.Slices.Slices[0].ExpectedFiles = []string{"internal/x/x.go"}
			d.Slices.Slices[0].Goal = "same"
		}, sliceID: "001-a", request: SliceAmendmentRequest{Reason: "why", Goal: "same", AllowFiles: []string{"internal/x/x.go"}}, want: "does not change slice 001-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := editPlanDetail()
			if test.setup != nil {
				test.setup(detail)
			}
			original := clonePlanDetail(detail)

			_, err := amendSliceMutation(test.sliceID, test.request, amendTime())(detail)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if test.notFound && !errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
			if !reflect.DeepEqual(detail, original) {
				t.Fatalf("refused amend mutated plan artifacts:\n got: %#v\nwant: %#v", detail, original)
			}
		})
	}
}

func TestAmendSliceBoundsReason(t *testing.T) {
	detail := editPlanDetail()
	long := strings.Repeat("r", maxBlockerNoteRunes+10)

	event, err := markSliceAmendedWithChanges(detail, newArtifactChangeSet(detail), "001-a", SliceAmendmentRequest{Reason: long, Goal: "g"}, amendTime())
	if err != nil {
		t.Fatal(err)
	}
	slice := findSlice(detail, "001-a")
	if got := len([]rune(slice.Amendments[0].Reason)); got != maxBlockerNoteRunes {
		t.Fatalf("amendment reason runes = %d, want %d", got, maxBlockerNoteRunes)
	}
	if got := len([]rune(event.Reason)); got != maxBlockerNoteRunes {
		t.Fatalf("event reason runes = %d, want %d", got, maxBlockerNoteRunes)
	}
}

func TestPlanRecordAmendSlicePersistsReloadsAndReplaysIdempotently(t *testing.T) {
	dir := t.TempDir()
	writeEditPlan(t, dir)
	repo := NewFileRepository(dir)
	detail, err := repo.GetPlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	now := amendTime()

	if err := testRepoRecord(repo, detail).AmendSlice("003-c", amendRequest(), now); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repo.GetPlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	slice := findSlice(reloaded, "003-c")
	if slice == nil || slice.Status != StatusPending || slice.Goal != "Amended goal" || !slices.Equal(slice.ExpectedFiles, []string{"internal/x/helper.go"}) {
		t.Fatalf("amend was not persisted: %#v", slice)
	}
	if len(slice.Amendments) != 1 || slice.Amendments[0].Reason != "reviewer needs a helper file" || !slice.Amendments[0].AmendedAt.Equal(now) {
		t.Fatalf("amendments were not persisted: %#v", slice.Amendments)
	}
	event := findEventByType(reloaded.Events, EventTypeSliceAmended)
	if event == nil || event.SliceID != "003-c" || event.Reason != "reviewer needs a helper file" || !slices.Equal(event.AmendedFields, []string{"goal", "tasks", "expected_files", "manual_checks"}) {
		t.Fatalf("slice_amended event missing or wrong: %#v", reloaded.Events)
	}
	if !slices.Equal(reloaded.State.Plan.PendingSlices, []string{"001-a", "002-b", "003-c"}) {
		t.Fatalf("pending queue changed: %v", reloaded.State.Plan.PendingSlices)
	}

	replayed := clonePlanDetail(reloaded)
	mutation, err := amendSliceMutation("003-c", amendRequest(), now.Add(time.Minute))(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutation.Events) != 0 || !reflect.DeepEqual(mutation.Slices, reloaded.Slices) || !reflect.DeepEqual(replayed, reloaded) {
		t.Fatalf("replay was not idempotent: events=%#v slices=%#v", mutation.Events, mutation.Slices)
	}

	if err := testRepoRecord(repo, reloaded).AmendSlice("003-c", SliceAmendmentRequest{Reason: "second", AllowFiles: []string{"internal/x/helper.go", "internal/x/second.go"}}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	final, err := repo.GetPlan(context.Background(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	slice = findSlice(final, "003-c")
	if len(slice.Amendments) != 2 || slice.Amendments[1].Reason != "second" || !slices.Equal(slice.ExpectedFiles, []string{"internal/x/helper.go", "internal/x/second.go"}) {
		t.Fatalf("second amendment not appended in order: %#v files=%v", slice.Amendments, slice.ExpectedFiles)
	}
}
