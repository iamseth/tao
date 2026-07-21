// Package plan: clearable_fields_test.go pins the mergeJSON clearable-field
// contract with table-driven round-trips through the real WriteState write path.
//
// # Clearable fields
//
// A clearable field is declared WITHOUT omitempty so that marshalling an
// explicit zero/nil/empty value produces a JSON key in the encoded output.
// When writeJSON deep-merges the update over the existing file, that explicit
// zero overwrites the prior stored value.
//
// Intended-clearable fields (add entries here when a field is made clearable;
// removing omitempty from an existing field is a schema-aware decision that
// must also add a test case below):
//
//   - State.Plan.CurrentSlice (*string, "current_slice"): cleared to nil by
//     writing null — signals no slice is in progress.
//   - PlanState.LastRunCommitPolicy (string, "last_run_commit_policy"): cleared
//     to "" by writing explicit empty — lets later run-start writes replace stale policy.
//   - PlanState.LastRunStartingDirty ([]string, "last_run_starting_dirty"): cleared
//     to [] by writing an empty slice — lets clean run starts replace stale path tolerances.
//   - PlanReview.Findings ([]ReviewFinding, "findings"): cleared to [] by
//     writing an empty slice — prevents stale findings surviving review rewrites.
//   - PlanReview.Verdict, Summary, FindingsCount, ReviewedAt: also non-omitempty;
//     writing explicit zero values clears them (tested indirectly via Findings).
//   - Workspace.DependencyFailure (string, "dependency_preparation_failure"): cleared
//     to "" by writing explicit empty — fixes the stale-display bug in view/view.go
//     where a prior dependency failure remained visible after a retry success.
//   - Workspace.DependencyFingerprint (string, "dependency_fingerprint"): cleared
//     to "" by writing explicit empty when successful-install evidence is unknown.
//   - Slice.BlockerNote (string, "blocker_note"): cleared to "" by ContinueBlocked
//     so resolved blocker text does not survive the slices.json merge-write.
//
// # Merge-only fields (omitempty)
//
// A merge-only field has `omitempty` in its struct tag.  When the Go value is
// zero/nil it is absent from the encoded update JSON, so a later write with a
// zero value cannot overwrite a previously stored non-zero value.
//
// Known merge-only fields in state.json (adding omitempty to a clearable field
// is a breaking schema change; removing omitempty from a merge-only field is a
// deliberate schema extension):
//
//   - State.Workspace (*Workspace, "workspace,omitempty") — the whole block
//   - Workspace sub-fields (except DependencyFailure and DependencyFingerprint):
//     Branch, BaseSHA, HeadSHA, etc.
//   - Repo.BaseCommit ("base_commit,omitempty")
//   - PlanState.PullRequest (*PullRequest, "pull_request,omitempty")
//   - PlanState.Review (*PlanReview, "review,omitempty") — the whole struct pointer;
//     individual non-omitempty sub-fields inside the block ARE clearable once the
//     block is present on disk.
//
// Known merge-only fields in slices.json:
//
//   - Slice.ExecutionRoot, Tags, Approval, Notes, VerificationResults (all omitempty)
//   - ReviewFinding sub-fields: Severity, File, Message, Suggestion (all omitempty)
package plan

import (
	"path/filepath"
	"testing"
	"time"
)

// TestClearableFieldsRoundTrip verifies that each intended-clearable field can
// be zeroed through the real WriteState write path and reloads as the zero value.
func TestClearableFieldsRoundTrip(t *testing.T) {
	type roundTripCase struct {
		name  string
		setup func(t *testing.T, dir string)
		clear func(t *testing.T, dir string)
		check func(t *testing.T, dir string)
	}

	tests := []roundTripCase{
		{
			name: "State.Plan.CurrentSlice clears to nil",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				sliceID := "001-a"
				state := clearableContractBaseState()
				state.Plan.CurrentSlice = &sliceID
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				// No omitempty on current_slice: nil marshals as null,
				// which writeJSON deep-merges over the stored string.
				state.Plan.CurrentSlice = nil
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				t.Helper()
				got, err := ReadState(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got.Plan.CurrentSlice != nil {
					t.Errorf("expected CurrentSlice nil after clear, got %q", *got.Plan.CurrentSlice)
				}
				// Confirm the raw JSON contains an explicit null (key present, not absent).
				var raw map[string]any
				readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
				planObj, _ := raw["plan"].(map[string]any)
				if _, exists := planObj["current_slice"]; !exists {
					t.Error("expected current_slice key present in state.json (as null), key is absent")
				}
			},
		},
		{
			name: "PlanState.LastRunCommitPolicy clears to empty string",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				state.Plan.LastRunCommitPolicy = "plan"
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				// No omitempty on last_run_commit_policy: "" marshals as "",
				// which writeJSON deep-merges over the stored policy.
				state.Plan.LastRunCommitPolicy = ""
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				t.Helper()
				got, err := ReadState(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got.Plan.LastRunCommitPolicy != "" {
					t.Errorf("expected LastRunCommitPolicy cleared to empty, got %q", got.Plan.LastRunCommitPolicy)
				}
				// Confirm raw JSON has the field present with an explicit empty string (not absent).
				var raw map[string]any
				readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
				planObj, _ := raw["plan"].(map[string]any)
				val, exists := planObj["last_run_commit_policy"]
				if !exists {
					t.Error("expected last_run_commit_policy key present in state.json (as \"\"), key is absent")
				}
				if val != "" {
					t.Errorf("expected last_run_commit_policy: \"\" in state.json, got %#v", val)
				}
			},
		},
		{
			name: "PlanState.LastRunStartingDirty clears to empty slice",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				state.Plan.LastRunStartingDirty = []string{"README.md", "internal/run/run.go"}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				// No omitempty on last_run_starting_dirty: []string{} marshals as [],
				// which writeJSON deep-merges via mergeJSONArray(existing, []) → [].
				state.Plan.LastRunStartingDirty = []string{}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				t.Helper()
				got, err := ReadState(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got.Plan.LastRunStartingDirty == nil || len(got.Plan.LastRunStartingDirty) != 0 {
					t.Errorf("expected LastRunStartingDirty cleared to empty slice, got %+v", got.Plan.LastRunStartingDirty)
				}
				// Confirm state.json persists an explicit empty array rather than a null or missing key.
				var raw map[string]any
				readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
				planObj, _ := raw["plan"].(map[string]any)
				lastRunStartingDirty, ok := planObj["last_run_starting_dirty"].([]any)
				if !ok || len(lastRunStartingDirty) != 0 {
					t.Errorf("expected last_run_starting_dirty: [] in state.json, got %#v", planObj["last_run_starting_dirty"])
				}
			},
		},
		{
			name: "PlanReview.Findings clears to empty slice",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				reviewedAt := time.Date(2026, 6, 28, 7, 0, 0, 0, time.UTC)
				state := clearableContractBaseState()
				state.Plan.Review = &PlanReview{
					Status:        ReviewStatusCompleted,
					Verdict:       ReviewVerdictChangesRequested,
					Summary:       "Needs work.",
					FindingsCount: 1,
					Findings:      []ReviewFinding{{Severity: "major", Message: "fix this"}},
					ReviewedAt:    reviewedAt,
				}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, dir string) {
				t.Helper()
				reviewedAt := time.Date(2026, 6, 28, 7, 1, 0, 0, time.UTC)
				state := clearableContractBaseState()
				// No omitempty on Findings: []ReviewFinding{} marshals as [],
				// which writeJSON deep-merges via mergeJSONArray(existing, []) → [].
				state.Plan.Review = &PlanReview{
					Status:        ReviewStatusCompleted,
					Verdict:       ReviewVerdictApprove,
					Summary:       "Ready to merge.",
					FindingsCount: 0,
					Findings:      []ReviewFinding{},
					ReviewedAt:    reviewedAt,
				}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				t.Helper()
				got, err := ReadState(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got.Plan.Review == nil {
					t.Fatal("expected Plan.Review non-nil after clear")
				}
				if len(got.Plan.Review.Findings) != 0 {
					t.Errorf("expected Findings cleared to empty, got %+v", got.Plan.Review.Findings)
				}
				// Confirm state.json persists an explicit empty array rather than a null or missing key.
				var raw map[string]any
				readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
				planObj, _ := raw["plan"].(map[string]any)
				reviewObj, _ := planObj["review"].(map[string]any)
				findings, ok := reviewObj["findings"].([]any)
				if !ok || len(findings) != 0 {
					t.Errorf("expected findings: [] in state.json, got %#v", reviewObj["findings"])
				}
			},
		},
		{
			name: "Workspace.DependencyFailure clears to empty on retry success",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				state.Workspace = &Workspace{
					Strategy:              WorkspaceStrategyWorktree,
					DependencyPreparation: "failed",
					DependencyFailure:     "npm install failed: network error",
				}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				// No omitempty on dependency_preparation_failure: "" marshals as "",
				// which writeJSON deep-merges over the stored failure string.
				state.Workspace = &Workspace{
					Strategy:              WorkspaceStrategyWorktree,
					DependencyPreparation: "ready",
					DependencyFailure:     "",
				}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				t.Helper()
				got, err := ReadState(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got.Workspace == nil {
					t.Fatal("expected State.Workspace non-nil after clear")
				}
				if got.Workspace.DependencyFailure != "" {
					t.Errorf("expected DependencyFailure cleared to empty, got %q", got.Workspace.DependencyFailure)
				}
				// Confirm raw JSON has the field present with an explicit empty string (not absent).
				var raw map[string]any
				readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
				wsObj, _ := raw["workspace"].(map[string]any)
				val, exists := wsObj["dependency_preparation_failure"]
				if !exists {
					t.Error("expected dependency_preparation_failure key present in state.json (as \"\"), key is absent")
				}
				if val != "" {
					t.Errorf("expected dependency_preparation_failure: \"\" in state.json, got %#v", val)
				}
			},
		},
		{
			name: "Workspace.DependencyFingerprint clears to empty when unknown",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				state.Workspace = &Workspace{
					Strategy:              WorkspaceStrategyWorktree,
					DependencyFingerprint: "6f5902ac237024bdd0c176cb93063dc4e3592dc78f70c1a4406058c3b7d46655",
				}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				state.Workspace = &Workspace{
					Strategy:              WorkspaceStrategyWorktree,
					DependencyFingerprint: "",
				}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				t.Helper()
				got, err := ReadState(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got.Workspace == nil {
					t.Fatal("expected State.Workspace non-nil after clear")
				}
				if got.Workspace.DependencyFingerprint != "" {
					t.Errorf("expected DependencyFingerprint cleared to empty, got %q", got.Workspace.DependencyFingerprint)
				}
				var raw map[string]any
				readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
				wsObj, _ := raw["workspace"].(map[string]any)
				val, exists := wsObj["dependency_fingerprint"]
				if !exists {
					t.Error("expected dependency_fingerprint key present in state.json (as \"\"), key is absent")
				}
				if val != "" {
					t.Errorf("expected dependency_fingerprint: \"\" in state.json, got %#v", val)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			tt.clear(t, dir)
			tt.check(t, dir)
		})
	}

	t.Run("Slice.BlockerNote clears to empty string", func(t *testing.T) {
		dir := t.TempDir()
		created := time.Date(2026, 5, 3, 23, 0, 0, 0, time.UTC)
		slices := SlicesFile{
			Schema: "tao.plan.slices.v1",
			PlanID: "plan-a",
			Slices: []Slice{{
				ID: "001-a", Status: StatusBlocked, BlockerNote: "waiting for approval",
				Timing: SliceTiming{CreatedAt: created, UpdatedAt: created},
			}},
		}
		if err := writeSlices(dir, slices); err != nil {
			t.Fatal(err)
		}
		slices.Slices[0].BlockerNote = ""
		if err := writeSlices(dir, slices); err != nil {
			t.Fatal(err)
		}

		var got SlicesFile
		readJSONFile(t, filepath.Join(dir, "slices.json"), &got)
		if got.Slices[0].BlockerNote != "" {
			t.Fatalf("expected BlockerNote cleared to empty, got %q", got.Slices[0].BlockerNote)
		}
		var raw map[string]any
		readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
		rawSlices, ok := raw["slices"].([]any)
		if !ok || len(rawSlices) != 1 {
			t.Fatalf("unexpected raw slices: %#v", raw["slices"])
		}
		sliceObj, _ := rawSlices[0].(map[string]any)
		value, exists := sliceObj["blocker_note"]
		if !exists || value != "" {
			t.Errorf("expected blocker_note: \"\" in slices.json, got %#v", value)
		}
	})
}

// clearableContractBaseState returns a minimal State used by clearable-field
// round-trip tests.  It must not be shared with other test helpers that mutate
// common fields, to avoid cross-test ordering surprises.
func clearableContractBaseState() State {
	created := time.Date(2026, 5, 3, 23, 0, 0, 0, time.UTC)
	return State{
		Schema:    "tao.plan.state.v1",
		Status:    StatusPlanned,
		CreatedAt: created,
		UpdatedAt: created,
		Repo:      Repo{Name: "tao", Root: "/repo", Branch: "main"},
		Plan: PlanState{
			ID:    "plan-a",
			Title: "Plan A",
		},
	}
}
