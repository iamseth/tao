package cli

import (
	"bytes"
	"context"
	"io"
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
func editPlanRepo(planID string) *plantest.Repository {
	completedAt := time.Date(2026, 5, 26, 12, 30, 0, 0, time.UTC)
	detail := plantest.NewPlanDetail(planID).
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
	repo := editPlanRepo(planID)
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
	if !strings.Contains(out.String(), "Removed pending slice: 004-d") || !strings.Contains(out.String(), "Next: tao run "+planID) {
		t.Fatalf("unexpected remove output %q", out.String())
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

func TestEditRejectsInvalidFlagsUnknownSubcommandsAndUnsafeEdits(t *testing.T) {
	const planID = "20260526-1200-edit"
	repo := editPlanRepo(planID)
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
