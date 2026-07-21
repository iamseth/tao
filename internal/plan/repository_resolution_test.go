package plan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/taodata"
)

func TestPlanSlugAcceptsMinuteAndSecondLevelIDs(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want string
		ok   bool
	}{
		{id: "20260427-1802-legacy-plan", want: "legacy-plan", ok: true},
		{id: "20260427-180245-second-plan", want: "second-plan", ok: true},
		{id: "2026042-1802-short-date"},
		{id: "2026042x-1802-invalid-date"},
		{id: "20260427-180-invalid-time"},
		{id: "20260427-18025-invalid-time"},
		{id: "20260427-1802457-invalid-time"},
		{id: "20260427-1802x5-invalid-time"},
		{id: "20260427-1802-"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, ok := PlanSlug(tc.id)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("PlanSlug(%q) = %q, %t; want %q, %t", tc.id, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestGetPlanResolvesMinuteAndSecondLevelIDs(t *testing.T) {
	dir := t.TempDir()
	legacyID := "20260427-1802-legacy-plan"
	secondLevelID := "20260427-181045-second-plan"
	writeMinimalPlan(t, dir, legacyID, "Legacy Plan")
	writeMinimalPlan(t, dir, secondLevelID, "Second-level Plan")

	repo := NewFileRepository(dir)
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "legacy exact", input: legacyID, want: legacyID},
		{name: "second-level exact", input: secondLevelID, want: secondLevelID},
		{name: "legacy ID prefix", input: "20260427-1802-l", want: legacyID},
		{name: "second-level ID prefix", input: "20260427-181045-s", want: secondLevelID},
		{name: "legacy slug", input: "legacy-plan", want: legacyID},
		{name: "second-level slug", input: "second-plan", want: secondLevelID},
		{name: "legacy slug prefix", input: "legacy", want: legacyID},
		{name: "second-level slug prefix", input: "second", want: secondLevelID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail, err := repo.GetPlan(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if detail.State.Plan.ID != tc.want {
				t.Fatalf("unexpected plan id %q; want %q", detail.State.Plan.ID, tc.want)
			}
		})
	}
}

func TestGetPlanAcceptsExactIDBeforePrefix(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "20260427-1802", "Exact Plan")
	writeMinimalPlan(t, dir, "20260427-1802-example-plan", "Example Plan")

	repo := NewFileRepository(dir)
	detail, err := repo.GetPlan(context.Background(), "20260427-1802")
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.ID != "20260427-1802" {
		t.Fatalf("unexpected plan id %q", detail.State.Plan.ID)
	}
}

func TestGetPlanAcceptsUniquePrefix(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "20260427-1802-example-plan", "Example Plan")

	repo := NewFileRepository(dir)
	detail, err := repo.GetPlan(context.Background(), "20260427-1802")
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.ID != "20260427-1802-example-plan" {
		t.Fatalf("unexpected plan id %q", detail.State.Plan.ID)
	}
}

func TestGetPlanAcceptsUniqueSlugAndSlugPrefix(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "20260427-1802-example-plan", "Example Plan")
	writeMinimalPlan(t, dir, "20260427-1810-other-plan", "Other Plan")

	repo := NewFileRepository(dir)
	for _, input := range []string{"example-plan", "exam"} {
		detail, err := repo.GetPlan(context.Background(), input)
		if err != nil {
			t.Fatalf("GetPlan(%q): %v", input, err)
		}
		if detail.State.Plan.ID != "20260427-1802-example-plan" {
			t.Fatalf("unexpected plan id %q", detail.State.Plan.ID)
		}
	}
}

func TestGetPlanRejectsAmbiguousSlugPrefix(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "20260427-1802-example-plan", "Example Plan")
	writeMinimalPlan(t, dir, "20260427-181045-example-two", "Example Two")

	repo := NewFileRepository(dir)
	_, err := repo.GetPlan(context.Background(), "example")
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "plan slug") || !strings.Contains(err.Error(), "20260427-1802-example-plan") || !strings.Contains(err.Error(), "20260427-181045-example-two") {
		t.Fatalf("expected ambiguous slug error listing plan ids, got %v", err)
	}
}

func TestGetPlanInputAcceptsPlanDirectoryAndFile(t *testing.T) {
	dir := t.TempDir()
	planID := "20260427-1802-example-plan"
	writeMinimalPlan(t, dir, planID, "Example Plan")
	repo := NewFileRepository(dir)

	for _, input := range []string{filepath.Join(dir, planID), filepath.Join(dir, planID, "slices.json")} {
		detail, err := repo.GetPlanInput(context.Background(), input)
		if err != nil {
			t.Fatalf("GetPlanInput(%q): %v", input, err)
		}
		if detail.State.Plan.ID != planID {
			t.Fatalf("unexpected plan id %q", detail.State.Plan.ID)
		}
	}

	_, err := repo.GetPlanInput(context.Background(), filepath.Join(dir, "missing", "state.json"))
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing path not-found error, got %v", err)
	}
}

func TestDeletePlanDeletesResolvedPlan(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input func(root, id string) string
	}{
		{name: "id", input: func(root, id string) string { return id }},
		{name: "prefix", input: func(root, id string) string { return "20260427-1802" }},
		{name: "slug", input: func(root, id string) string { return "example" }},
		{name: "path", input: func(root, id string) string { return filepath.Join(root, id, "state.json") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			planID := "20260427-1802-example-plan"
			writeMinimalPlan(t, dir, planID, "Example Plan")
			repo := NewFileRepository(dir)

			result, err := repo.DeletePlan(context.Background(), tc.input(dir, planID), DeletePlanOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if result.ID != planID || result.Invalid {
				t.Fatalf("unexpected delete result: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(dir, planID)); !os.IsNotExist(err) {
				t.Fatalf("expected plan directory to be removed, got %v", err)
			}
		})
	}
}

func TestDeletePlanRequiresInput(t *testing.T) {
	_, err := NewFileRepository(t.TempDir()).DeletePlan(context.Background(), "", DeletePlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "plan input is required") {
		t.Fatalf("expected missing input error, got %v", err)
	}
}

func TestDeletePlanPreservesAmbiguity(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "20260427-1802-example-plan", "Example Plan")
	writeMinimalPlan(t, dir, "20260427-1810-example-two", "Example Two")

	_, err := NewFileRepository(dir).DeletePlan(context.Background(), "example", DeletePlanOptions{})
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "plan slug") || !strings.Contains(err.Error(), "20260427-1802-example-plan") || !strings.Contains(err.Error(), "20260427-1810-example-two") {
		t.Fatalf("expected ambiguous slug error listing plan ids, got %v", err)
	}
	for _, id := range []string{"20260427-1802-example-plan", "20260427-1810-example-two"} {
		if _, err := os.Stat(filepath.Join(dir, id)); err != nil {
			t.Fatalf("expected %s to remain: %v", id, err)
		}
	}
}

func TestDeletePlanDeletesInvalidDirectoryOnlyWithConfirmation(t *testing.T) {
	dir := t.TempDir()
	planID := "20260427-1802-broken-plan"
	planDir := filepath.Join(dir, planID)
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "state.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := NewFileRepository(dir)

	_, err := repo.DeletePlan(context.Background(), planID, DeletePlanOptions{})
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "confirm invalid deletion") {
		t.Fatalf("expected invalid confirmation error, got %v", err)
	}
	if _, err := os.Stat(planDir); err != nil {
		t.Fatalf("expected invalid plan to remain before confirmation: %v", err)
	}

	result, err := repo.DeletePlan(context.Background(), planID, DeletePlanOptions{ConfirmInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Invalid || result.ID != planID {
		t.Fatalf("unexpected invalid delete result: %+v", result)
	}
	if _, err := os.Stat(planDir); !os.IsNotExist(err) {
		t.Fatalf("expected invalid plan directory to be removed, got %v", err)
	}
}

func TestDeletePlanRejectsActivePlans(t *testing.T) {
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

	_, err := NewFileRepository(dir).DeletePlan(context.Background(), "active", DeletePlanOptions{})
	if err == nil || !errors.Is(err, ErrActive) || !strings.Contains(err.Error(), "active") {
		t.Fatalf("expected active plan rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "active")); err != nil {
		t.Fatalf("expected active plan to remain: %v", err)
	}
}

func TestDeletePlanRejectsUnsafePathsBeforeRemoveAll(t *testing.T) {
	dir := t.TempDir()
	store := &recordingArtifactStore{}
	repo := NewFileRepository(dir)
	repo.store = store

	for _, input := range []string{dir, t.TempDir()} {
		_, err := repo.DeletePlan(context.Background(), input, DeletePlanOptions{ConfirmInvalid: true})
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "refusing to delete") {
			t.Fatalf("DeletePlan(%q) expected unsafe path error, got %v", input, err)
		}
	}
	if len(store.removed) != 0 {
		t.Fatalf("RemoveAll called for unsafe path: %v", store.removed)
	}
}

func TestGetPlanRejectsAmbiguousPrefix(t *testing.T) {
	dir := t.TempDir()
	writeMinimalPlan(t, dir, "20260427-1802-example-plan", "Example Plan")
	writeMinimalPlan(t, dir, "20260427-1810-example-plan", "Example Plan")

	repo := NewFileRepository(dir)
	_, err := repo.GetPlan(context.Background(), "20260427")
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ambiguous prefix invalid error, got %v", err)
	}
}

func TestGetPlanRejectsInvalidAndMissingIDs(t *testing.T) {
	repo := NewFileRepository(t.TempDir())
	for _, tc := range []struct {
		id   string
		want error
	}{
		{id: "", want: ErrInvalid},
		{id: "../bad", want: ErrInvalid},
		{id: "missing", want: ErrNotFound},
	} {
		_, err := repo.GetPlan(context.Background(), tc.id)
		if err == nil || !errors.Is(err, tc.want) {
			t.Fatalf("expected %v for id %q, got %v", tc.want, tc.id, err)
		}
	}
}

func TestDefaultDirAndNewRepositoryFallback(t *testing.T) {
	dir := t.TempDir()
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	t.Chdir(dir)

	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataHome, "repos", taodata.RepoID(root), "plans")
	if got := DefaultDir(); got != want {
		t.Fatalf("unexpected default dir %q", got)
	}
	if repo := NewFileRepository(""); repo.Dir != DefaultDir() {
		t.Fatalf("expected default repo dir, got %q", repo.Dir)
	}
}
