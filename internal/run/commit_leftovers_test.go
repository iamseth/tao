package run

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestCommitLeftoversDetectsRunProducedFilesSortedDeduped(t *testing.T) {
	detail := commitLeftoversPlanDetail([]string{"001-a"}, []plan.Slice{
		{ID: "001-a", ExpectedFiles: []string{"b.go"}},
	})
	status := " M b.go\n?? a.go\n M b.go\n"

	got, err := commitLeftovers(detail, status, nil)
	if err != nil {
		t.Fatalf("commitLeftovers returned error: %v", err)
	}
	want := []string{"a.go", "b.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("commitLeftovers() = %v, want %v", got, want)
	}
}

func TestCommitLeftoversCleanTreeReturnsNone(t *testing.T) {
	detail := commitLeftoversPlanDetail([]string{"001-a"}, []plan.Slice{
		{ID: "001-a", ExpectedFiles: []string{"internal/run/commit_leftovers.go"}},
	})

	got, err := commitLeftovers(detail, "", nil)
	if err != nil {
		t.Fatalf("commitLeftovers returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("commitLeftovers() = %v, want no leftovers", got)
	}
}

func TestCommitLeftoversExcludesTaoAndStartingDirtyPaths(t *testing.T) {
	detail := commitLeftoversPlanDetail([]string{"001-a"}, []plan.Slice{
		{ID: "001-a", ExpectedFiles: []string{"internal/run/commit_leftovers.go"}},
	})
	status := "?? .tao/state.json\n M ./.tao/workspaces/plan/file.go\n M README.md\n M internal/run/unrelated.go\n"

	got, err := commitLeftovers(detail, status, startingDirtyPredicate([]string{"./README.md"}))
	if err != nil {
		t.Fatalf("commitLeftovers returned error: %v", err)
	}
	want := []string{"internal/run/unrelated.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("commitLeftovers() = %v, want %v", got, want)
	}
}

func TestCommitLeftoversSurfacesAmbiguousStatusEntry(t *testing.T) {
	detail := commitLeftoversPlanDetail([]string{"001-a"}, []plan.Slice{
		{ID: "001-a", ExpectedFiles: []string{"new.go"}},
	})
	status := "R  old.go -> new.go\n"

	got, err := commitLeftovers(detail, status, nil)
	if err == nil {
		t.Fatalf("commitLeftovers() error = nil, want ambiguous entry error; leftovers %v", got)
	}
	var ambiguous *commitLeftoverAmbiguousStatusError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("commitLeftovers() error = %T %v, want *commitLeftoverAmbiguousStatusError", err, err)
	}
	if !slices.Equal(ambiguous.Lines, []string{"R  old.go -> new.go"}) {
		t.Fatalf("ambiguous lines = %v, want rename line", ambiguous.Lines)
	}
}

func commitLeftoversPlanDetail(completed []string, planSlices []plan.Slice) *plan.PlanDetail {
	return &plan.PlanDetail{
		Dir: "/plans/plan-a",
		State: plan.State{
			Repo: plan.Repo{Root: "/repo"},
			Plan: plan.PlanState{ID: "plan-a", Title: "Plan A", CompletedSlices: completed},
		},
		Slices: plan.SlicesFile{Slices: planSlices},
	}
}

func runCommitTestGitCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	_ = runCommitTestGitOutput(t, root, args...)
}

func runCommitTestGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- test helper invokes git with test-defined args.
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
