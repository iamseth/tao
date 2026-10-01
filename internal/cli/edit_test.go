package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

// editPlanRepo builds a plantest.Repository that mirrors the fixture used
// by the edit tests: four pending slices (001-a through 004-d, with 002-b
// depending on 001-a) and one completed slice 005-e.
func editPlanRepo() *plantest.Repository {
	completedAt := time.Date(2026, 5, 26, 12, 30, 0, 0, time.UTC)
	detail := plantest.NewPlanDetail("20260526-1200-edit").
		WithStatus(plan.StatusPlanned).
		WithPendingSlices("001-a", "002-b", "003-c", "004-d").
		WithCompletedSlices("005-e").
		WithRepoRoot("/repo").
		AddSlice(plantest.NewSlice("001-a").WithTitle("A").Build()).
		AddSlice(plantest.NewSlice("002-b").WithTitle("B").WithDependsOn("001-a").Build()).
		AddSlice(plantest.NewSlice("003-c").WithTitle("C").Build()).
		AddSlice(plantest.NewSlice("004-d").WithTitle("D").Build()).
		AddSlice(plantest.NewSlice("005-e").WithTitle("E").
			WithStatus(plan.StatusCompleted).
			WithCompletedAt(completedAt).Build()).
		Build()
	repo := plantest.NewRepository()
	repo.AddDetail(detail)
	return repo
}

func TestEditRemoveSkipsAndMovesPendingSlices(t *testing.T) {
	const planID = "20260526-1200-edit"
	repo := editPlanRepo()
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(_ string) Repository { return repo }}

	// Remove 004-d.
	if err := app.Run(context.Background(), []string{"edit", "remove", planID, "004-d"}); err != nil {
		t.Fatal(err)
	}
	updated, _ := repo.GetPlan(context.Background(), planID)
	if containsID(updated.State.Plan.PendingSlices, "004-d") {
		t.Fatalf("expected 004-d removed from pending queue, got %v", updated.State.Plan.PendingSlices)
	}
	if sliceByID(updated, "004-d") != nil {
		t.Fatalf("expected 004-d removed from slices list, found: %v", updated.Slices.Slices)
	}
	for _, want := range []string{
		"Removed pending slice: 004-d",
		"Next: tao run " + planID,
		"Reason: the next pending slice is runnable",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("remove output %q does not contain %q", out.String(), want)
		}
	}

	// Skip 003-c (reuse same app/repo; state already modified).
	out.Reset()
	if err := app.Run(context.Background(), []string{"e", "skip", planID, "003-c"}); err != nil {
		t.Fatal(err)
	}
	updated, _ = repo.GetPlan(context.Background(), planID)
	if containsID(updated.State.Plan.PendingSlices, "003-c") {
		t.Fatalf("expected 003-c removed from pending queue, got %v", updated.State.Plan.PendingSlices)
	}
	skipped := sliceByID(updated, "003-c")
	if skipped == nil || skipped.Status != plan.StatusSkipped {
		t.Fatalf("expected 003-c skipped, got %+v", skipped)
	}
	if !strings.Contains(out.String(), "Skipped pending slice: 003-c") {
		t.Fatalf("unexpected skip output %q", out.String())
	}

	// Move 002-b after 001-a.
	out.Reset()
	if err := app.Run(context.Background(), []string{"edit", "move", planID, "002-b", "--after", "001-a"}); err != nil {
		t.Fatal(err)
	}
	updated, _ = repo.GetPlan(context.Background(), planID)
	wantOrder := []string{"001-a", "002-b"}
	if !pendingSlicesEqual(updated.State.Plan.PendingSlices, wantOrder) {
		t.Fatalf("expected pending order %v, got %v", wantOrder, updated.State.Plan.PendingSlices)
	}
	if !strings.Contains(out.String(), "Moved pending slice: 002-b") {
		t.Fatalf("unexpected move output %q", out.String())
	}

	// Verify all three event types were appended.
	types := eventTypes(updated.Events)
	for _, want := range []string{plan.EventTypeSliceRemoved, plan.EventTypeSliceSkipped, plan.EventTypeSlicesReordered} {
		if !containsID(types, want) {
			t.Fatalf("expected event %q, got %v", want, types)
		}
	}
}

func TestEditSurfacesGeneratedVerificationRepairRefusalWithoutPersistence(t *testing.T) {
	const planID = "20260526-1200-edit"
	for _, test := range []struct {
		name    string
		action  string
		sliceID string
	}{
		{name: "remove", action: "remove", sliceID: "004-d"},
		{name: "skip", action: "skip", sliceID: "003-c"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := editPlanRepo()
			detail, err := repo.GetPlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			sliceByID(detail, test.sliceID).VerificationRepair = &plan.VerificationRepairBinding{
				Command: "make verify", HeadSHA: "failed-head", Fingerprint: "failure",
			}
			before, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			app := App{Out: &out, Err: &out, Repository: func(_ string) Repository { return repo }}

			err = app.Run(context.Background(), []string{"edit", test.action, planID, test.sliceID})
			want := "cannot " + test.action + " generated verification-repair slice " + test.sliceID +
				"; run `tao run " + planID + "` to complete it, or use `tao abandon --reason TEXT " + planID + "` before recovering manually"
			if err == nil || err.Error() != want {
				t.Fatalf("edit error = %v, want %q", err, want)
			}
			afterDetail, resolveErr := repo.GetPlan(context.Background(), planID)
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			after, err := json.Marshal(afterDetail)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("refused edit persisted artifact mutation:\n got: %s\nwant: %s", after, before)
			}
			if out.Len() != 0 {
				t.Fatalf("edit emitted success output: %q", out.String())
			}
		})
	}
}

func TestEditRejectsAbandonedPlanWithoutChangingPreservedWork(t *testing.T) {
	const planID = "20260526-1200-edit"
	tests := []struct {
		name string
		args []string
	}{
		{name: "remove", args: []string{"edit", "remove", planID, "004-d"}},
		{name: "skip", args: []string{"edit", "skip", planID, "003-c"}},
		{name: "move", args: []string{"edit", "move", planID, "004-d", "--before", "003-c"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := editPlanRepo()
			detail, err := repo.GetPlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			detail.State.Status = plan.StatusAbandoned
			detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanAbandoned, PlanID: planID, Reason: "superseded by safer work"})
			before, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			app := App{Out: &out, Err: &out, Repository: func(_ string) Repository { return repo }}

			err = app.Run(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), "plan "+planID+" is abandoned: superseded by safer work") {
				t.Fatalf("edit error = %v", err)
			}
			afterDetail, resolveErr := repo.GetPlan(context.Background(), planID)
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			after, err := json.Marshal(afterDetail)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("edit changed abandoned status, slices, queue, or events:\n got: %s\nwant: %s", after, before)
			}
			if out.Len() != 0 {
				t.Fatalf("edit emitted success output: %q", out.String())
			}
		})
	}
}

func TestEditRejectsInvalidFlagsUnknownSubcommandsAndUnsafeEdits(t *testing.T) {
	const planID = "20260526-1200-edit"
	repo := editPlanRepo()
	app := App{Out: io.Discard, Err: io.Discard, Repository: func(_ string) Repository { return repo }}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown subcommand", args: []string{"edit", "rename", planID, "001-a"}, want: "unknown edit subcommand"},
		{name: "remove invalid flag", args: []string{"edit", "remove", planID, "001-a", "--force"}, want: "unknown flag"},
		{name: "move invalid flag", args: []string{"edit", "move", planID, "002-b", "--near", "001-a"}, want: "flag provided but not defined"},
		{name: "missing move relation", args: []string{"edit", "move", planID, "002-b"}, want: "requires --before or --after"},
		{name: "unsafe completed edit", args: []string{"edit", "skip", planID, "005-e"}, want: "only pending slices can be edited"},
		{name: "dependency invalid move", args: []string{"edit", "move", planID, "002-b", "--before", "001-a"}, want: "before pending dependency"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := app.Run(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

// containsID reports whether values contains s.
func containsID(values []string, s string) bool {
	return slices.Contains(values, s)
}

// sliceByID returns the slice with the given ID, or nil.
func sliceByID(detail *plan.PlanDetail, id string) *plan.Slice {
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == id {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}

// pendingSlicesEqual reports whether the pending slice lists are identical.
func pendingSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// eventTypes extracts event type strings from a list of events.
func eventTypes(events []plan.Event) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

// amendPlanRepo builds a plantest.Repository whose plan directory is a real
// temporary directory so tao edit amend can take the plan run lock. Slice
// 002-b is blocked with an existing goal, task, expected file, and manual
// check; 001-a is pending; 003-c is in progress; 005-e is completed.
func amendPlanRepo(t *testing.T) (*plantest.Repository, *plan.PlanDetail) {
	t.Helper()
	detail := plantest.NewPlanDetail("20260526-1200-amend").
		WithStatus(plan.StatusInProgress).
		WithCurrentSlice("003-c").
		WithPendingSlices("001-a", "002-b").
		WithCompletedSlices("005-e").
		WithRepoRoot(t.TempDir()).
		AddSlice(plantest.NewSlice("001-a").WithTitle("A").WithVerificationCommands("go test ./...").Build()).
		AddSlice(plantest.NewSlice("002-b").WithTitle("B").
			WithGoal("Original goal").
			WithTasks("Existing task").
			WithExpectedFiles("internal/cli/existing.go").
			WithManualChecks("Existing check").
			WithVerificationCommands("go test ./internal/cli").
			WithBlockerNote("expected_files does not allow internal/cli/new.go").Build()).
		AddSlice(plantest.NewSlice("003-c").WithTitle("C").WithStatus(plan.StatusInProgress).
			WithStartedAt(time.Date(2026, 5, 26, 12, 15, 0, 0, time.UTC)).
			WithVerificationCommands("go test ./...").Build()).
		AddSlice(plantest.NewSlice("005-e").WithTitle("E").WithStatus(plan.StatusCompleted).
			WithCompletedAt(time.Date(2026, 5, 26, 12, 30, 0, 0, time.UTC)).Build()).
		Build()
	detail.Dir = t.TempDir()
	repo := plantest.NewRepository()
	repo.AddDetail(detail)
	return repo, detail
}

func writeAmendInput(t *testing.T, name string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEditAmendAppendsContractChangesToBlockedSlice(t *testing.T) {
	const planID = "20260526-1200-amend"
	repo, _ := amendPlanRepo(t)
	reasonFile := writeAmendInput(t, "reason.txt", "  blocker fix needs a new helper file and a check\n")
	goalFile := writeAmendInput(t, "goal.txt", "Amended goal\n")
	now := time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC)
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut, Now: func() time.Time { return now }, Repository: func(_ string) Repository { return repo }}

	err := app.Run(context.Background(), []string{"edit", "amend", planID, "002-b",
		"--reason-file", reasonFile,
		"--goal-file", goalFile,
		"--add-task", "Add the helper",
		"--allow-file", "internal/cli/new.go",
		"--allow-file", "internal/cli/existing.go",
		"--add-manual-check", "Run the helper once",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Amended slice 002-b: goal, tasks, expected_files, manual_checks", "Operator Amendments", "Next: tao run"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("amend output %q does not contain %q", out.String(), want)
		}
	}

	updated, err := repo.GetPlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	amended := sliceByID(updated, "002-b")
	if amended == nil {
		t.Fatal("slice 002-b missing after amend")
	}
	if amended.Status != plan.StatusBlocked || amended.BlockerNote != "expected_files does not allow internal/cli/new.go" {
		t.Fatalf("amend changed status or blocker note: %+v", amended)
	}
	if amended.Goal != "Amended goal" {
		t.Fatalf("goal = %q", amended.Goal)
	}
	if !slices.Equal(amended.Tasks, []string{"Existing task", "Add the helper"}) {
		t.Fatalf("tasks = %v", amended.Tasks)
	}
	if !slices.Equal(amended.ExpectedFiles, []string{"internal/cli/existing.go", "internal/cli/new.go"}) {
		t.Fatalf("expected files = %v", amended.ExpectedFiles)
	}
	if !slices.Equal(amended.Verification.ManualChecks, []string{"Existing check", "Run the helper once"}) {
		t.Fatalf("manual checks = %v", amended.Verification.ManualChecks)
	}
	if len(amended.Amendments) != 1 {
		t.Fatalf("amendments = %+v", amended.Amendments)
	}
	entry := amended.Amendments[0]
	if entry.Reason != "blocker fix needs a new helper file and a check" || !entry.AmendedAt.Equal(now) || !slices.Equal(entry.Fields, []string{"goal", "tasks", "expected_files", "manual_checks"}) {
		t.Fatalf("amendment entry = %+v", entry)
	}
	found := false
	for _, event := range updated.Events {
		if event.Type != plan.EventTypeSliceAmended || event.SliceID != "002-b" {
			continue
		}
		found = true
		if event.Reason != entry.Reason || !slices.Equal(event.AmendedFields, entry.Fields) {
			t.Fatalf("slice_amended event = %+v", event)
		}
	}
	if !found {
		t.Fatalf("no slice_amended event for 002-b in %v", eventTypes(updated.Events))
	}
}

func TestEditAmendApprovalContracts(t *testing.T) {
	const planID = "20260526-1200-amend"
	const assertion = "Observations are supplied through approval."
	const facts = "Observed emulator v2 on 2026-09-30: export includes the header."
	for _, test := range []struct {
		name        string
		field       string
		replaceGoal bool
		approved    bool
		blocked     bool
		wantField   string
	}{
		{name: "reason facts do not repair goal", field: "goal", wantField: "goal"},
		{name: "approved pending still rejected", field: "goal", approved: true, wantField: "goal"},
		{name: "replace sole assertion", field: "goal", replaceGoal: true},
		{name: "appended facts leave context", field: "context", replaceGoal: true, wantField: "context"},
		{name: "appended facts leave task", field: "tasks", replaceGoal: true, wantField: "tasks[0]"},
		{name: "appended facts leave approval reason", field: "approval.reason", replaceGoal: true, wantField: "approval.reason"},
		{name: "another pending slice invalidates preview", field: "other", replaceGoal: true, wantField: "context"},
		{name: "blocked remains outside detector", field: "context", blocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, detail := amendPlanRepo(t)
			selected := sliceByID(detail, "001-a")
			selected.Approval = &plan.Approval{Required: true, Approved: test.approved, Reason: "Authorize export implementation"}
			switch test.field {
			case "goal":
				selected.Goal = assertion
			case "context":
				selected.Context = assertion
			case "tasks":
				selected.Tasks = []string{assertion}
			case "approval.reason":
				selected.Approval.Reason = assertion
			case "other":
				other := sliceByID(detail, "002-b")
				other.Status = plan.StatusPending
				other.Approval = &plan.Approval{Required: true, Reason: "Authorize export implementation"}
				other.Context = assertion
			}
			if test.blocked {
				selected.Status = plan.StatusBlocked
				selected.BlockerNote = "Missing export observation"
			}
			before, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			app := App{Out: &out, Err: io.Discard, Repository: func(string) Repository { return repo }}
			args := []string{"edit", "amend", planID, "001-a", "--reason-file", writeAmendInput(t, "reason.txt", facts), "--add-task", facts}
			if test.replaceGoal {
				args = append(args, "--goal-file", writeAmendInput(t, "goal.txt", facts))
			}
			err = app.Run(context.Background(), args)
			if test.wantField != "" {
				if err == nil || !strings.Contains(err.Error(), "amended plan verification is invalid") || !strings.Contains(err.Error(), test.wantField+" explicitly treats approval as factual evidence") {
					t.Fatalf("amend error = %v, want contradiction in %s", err, test.wantField)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			updated, err := repo.GetPlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantField != "" {
				after, err := json.Marshal(updated)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) || out.Len() != 0 {
					t.Fatalf("refused preview mutated plan or emitted success: %s; output=%q", after, out.String())
				}
				return
			}
			amended := sliceByID(updated, "001-a")
			if amended.Status != selected.Status || amended.BlockerNote != selected.BlockerNote || *amended.Approval != *selected.Approval {
				t.Fatalf("amend changed lifecycle or authorization: %+v", amended)
			}
			if test.replaceGoal && amended.Goal != facts {
				t.Fatalf("goal = %q", amended.Goal)
			}
			if !slices.Contains(amended.Tasks, facts) {
				t.Fatalf("facts missing from amended tasks: %v", amended.Tasks)
			}
			found := false
			for _, event := range updated.Events {
				if event.Type == plan.EventTypeSliceAmended && event.SliceID == "001-a" {
					found = true
					fields := []string{"tasks"}
					if test.replaceGoal {
						fields = []string{"goal", "tasks"}
					}
					if event.Reason != facts || !slices.Equal(event.AmendedFields, fields) {
						t.Fatalf("amend event = %+v", event)
					}
				}
			}
			if !found {
				t.Fatal("missing slice_amended event")
			}
		})
	}
}

func TestEditAmendRefusesWithoutPersisting(t *testing.T) {
	const planID = "20260526-1200-amend"
	for _, test := range []struct {
		name    string
		args    func(t *testing.T) []string
		prepare func(t *testing.T, detail *plan.PlanDetail)
		want    string
	}{
		{
			name: "missing reason flag",
			args: func(*testing.T) []string { return []string{"edit", "amend", planID, "002-b", "--add-task", "x"} },
			want: "requires --reason-file",
		},
		{
			name: "missing reason file",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "002-b", "--reason-file", filepath.Join(t.TempDir(), "absent.txt"), "--add-task", "x"}
			},
			want: "read amendment reason file",
		},
		{
			name: "empty reason file",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "002-b", "--reason-file", writeAmendInput(t, "reason.txt", " \n"), "--add-task", "x"}
			},
			want: "amendment reason file is empty",
		},
		{
			name: "no change flags",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "002-b", "--reason-file", writeAmendInput(t, "reason.txt", "why")}
			},
			want: "requires at least one contract change",
		},
		{
			name: "blank change values",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "002-b", "--reason-file", writeAmendInput(t, "reason.txt", "why"), "--add-task", " ", "--allow-file", ""}
			},
			want: "requires at least one contract change",
		},
		{
			name: "unknown flag",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "002-b", "--reason-file", writeAmendInput(t, "reason.txt", "why"), "--force"}
			},
			want: "flag provided but not defined",
		},
		{
			name: "in progress slice",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "003-c", "--reason-file", writeAmendInput(t, "reason.txt", "why"), "--add-task", "x"}
			},
			want: "slice 003-c is in_progress; only pending or blocked slices can be amended",
		},
		{
			name: "verification repair slice",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "001-a", "--reason-file", writeAmendInput(t, "reason.txt", "why"), "--add-task", "x"}
			},
			prepare: func(_ *testing.T, detail *plan.PlanDetail) {
				sliceByID(detail, "001-a").VerificationRepair = &plan.VerificationRepairBinding{Command: "make verify", HeadSHA: "failed-head", Fingerprint: "failure"}
			},
			want: "cannot amend generated verification-repair slice 001-a; run `tao run " + planID + "` to complete it",
		},
		{
			name: "run lock held",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "002-b", "--reason-file", writeAmendInput(t, "reason.txt", "why"), "--add-task", "x"}
			},
			prepare: func(t *testing.T, detail *plan.PlanDetail) {
				lock, err := plan.AcquireRunLock(detail.Dir, planID, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = lock.Release() })
			},
			want: "plan " + planID + " is already running",
		},
		{
			name: "amended plan fails verification",
			args: func(t *testing.T) []string {
				return []string{"edit", "amend", planID, "002-b", "--reason-file", writeAmendInput(t, "reason.txt", "why"), "--allow-file", "internal/cli/new.go"}
			},
			prepare: func(_ *testing.T, detail *plan.PlanDetail) {
				sliceByID(detail, "002-b").RequiredInputs = []plan.RequiredInput{{Path: "fixtures/missing.json", Kind: plan.RequiredInputFile, Reason: "seed data"}}
			},
			want: "amended plan verification is invalid: required file input \"fixtures/missing.json\" does not exist",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, detail := amendPlanRepo(t)
			if test.prepare != nil {
				test.prepare(t, detail)
			}
			before, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			app := App{Out: &out, Err: io.Discard, Repository: func(_ string) Repository { return repo }}

			err = app.Run(context.Background(), test.args(t))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("edit amend error = %v, want %q", err, test.want)
			}
			afterDetail, err := repo.GetPlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(afterDetail)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("refused amend persisted artifact mutation:\n got: %s\nwant: %s", after, before)
			}
			if out.Len() != 0 {
				t.Fatalf("edit amend emitted success output: %q", out.String())
			}
		})
	}
}
