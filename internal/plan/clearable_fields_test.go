// Package plan: clearable_fields_test.go pins the mergeJSON clear contract with
// table-driven round-trips through the real persistence paths.
//
// # Clearable fields
//
// Unmigrated clearable fields are declared WITHOUT omitempty so marshaling an
// explicit zero/nil/empty value produces a JSON key that writeJSON merges over
// the prior value. Migrated fields use omitempty to preserve by default and are
// cleared only by a typed ArtifactChangeSet declaration.
//
// Tag-driven clearable fields (removing omitempty from an existing field is a
// schema-aware decision that must also add a test case below):
//
//   - PlanState.LastRunCommitPolicy (string, "last_run_commit_policy"): cleared
//     to "" by writing explicit empty — lets later run-start writes replace stale policy.
//   - PlanState.LastRunStartingDirty ([]string, "last_run_starting_dirty"): cleared
//     to [] by writing an empty slice — lets clean run starts replace stale path tolerances.
//   - PlanState.PullRequestIntent (*PullRequest, "pull_request_intent"):
//     cleared to null after the partial PR is successfully recorded.
//   - PlanState.MergeCommitIntent (*SingleMergeCommitIntent, "merge_commit_intent"):
//     cleared to null after durable merge evidence or safe source supersession.

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
//   - State.Plan.CurrentSlice preserves by default but is explicitly clearable
//     through ArtifactChangeSet.
//   - Workspace sub-fields: Branch, BaseSHA, HeadSHA, etc.
//   - Workspace.DependencyFailure, DependencyFingerprint, and RebaseIntent
//     preserve by default but are explicitly clearable through ArtifactChangeSet.
//   - Repo.BaseCommit ("base_commit,omitempty")
//   - PlanState.ChangeType (ChangeType, "change_type,omitempty")
//   - PlanState.PullRequest (*PullRequest, "pull_request,omitempty")
//   - PlanState.Review and all PlanReview fields preserve by default but the
//     block is explicitly replaceable or clearable through ArtifactChangeSet.
//
// Known merge-only fields in slices.json:
//
//   - Slice.ExecutionRoot, Tags, Approval, Notes, VerificationResults (all omitempty)
//   - Slice.BlockerNote preserves by default but is explicitly clearable through
//     ArtifactChangeSet.
//   - ReviewFinding sub-fields: Severity, File, Message, Suggestion (all omitempty)
package plan

import (
	"path/filepath"
	"reflect"
	"strings"
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
			name: "PlanState.MergeCommitIntent clears to nil",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				state.Plan.MergeCommitIntent = &SingleMergeCommitIntent{
					Message: "feat(merge): use review proposal\n\nWhat:\nUse it.\n\nWhy:\nKeep recovery exact.\n\nTao-Plan: plan-a\nTao-Source-Head: source123",
					PlanID:  "plan-a", SourceHead: "source123", DefaultBranch: "main", DefaultParent: "base123", CreatedAt: time.Date(2026, 7, 23, 20, 0, 0, 0, time.UTC),
				}
				if err := writeState(dir, state); err != nil {
					t.Fatal(err)
				}
			},
			clear: func(t *testing.T, dir string) {
				t.Helper()
				state := clearableContractBaseState()
				state.Plan.MergeCommitIntent = nil
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
				if got.Plan.MergeCommitIntent != nil {
					t.Fatalf("expected MergeCommitIntent nil after clear, got %+v", got.Plan.MergeCommitIntent)
				}
				var raw map[string]any
				readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
				planObj, _ := raw["plan"].(map[string]any)
				if value, exists := planObj["merge_commit_intent"]; !exists || value != nil {
					t.Errorf("expected merge_commit_intent: null in state.json, got %#v", planObj["merge_commit_intent"])
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

}

func TestMigratedSliceBlockerNoteRequiresDeclaredClear(t *testing.T) {
	field, ok := reflect.TypeOf(Slice{}).FieldByName("BlockerNote")
	if !ok || !strings.Contains(field.Tag.Get("json"), "omitempty") {
		t.Fatalf("Slice.BlockerNote must preserve by default with omitempty, tag=%q", field.Tag.Get("json"))
	}

	dir := t.TempDir()
	created := time.Date(2026, 5, 3, 23, 0, 0, 0, time.UTC)
	seeded := SlicesFile{
		Schema: "tao.plan.slices.v1",
		PlanID: "plan-a",
		Slices: []Slice{{
			ID: "001-a", Status: StatusBlocked, BlockerNote: "waiting for approval",
			Timing: SliceTiming{CreatedAt: created, UpdatedAt: created},
		}},
	}
	state := clearableContractBaseState()
	state.Status = StatusBlocked
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(dir, seeded); err != nil {
		t.Fatal(err)
	}
	intended := cloneSlicesFile(seeded)
	intended.Slices[0].BlockerNote = ""
	if err := writeSlices(dir, intended); err != nil {
		t.Fatal(err)
	}
	if got := readSlicesFile(t, dir).Slices[0].BlockerNote; got != seeded.Slices[0].BlockerNote {
		t.Fatalf("preserve-only write changed blocker note to %q", got)
	}

	detail := &PlanDetail{Dir: dir, State: state, Slices: cloneSlicesFile(seeded)}
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	detail.Slices.Slices[0].BlockerNote = ""
	if err := record.PersistArtifacts(); err == nil || !strings.Contains(err.Error(), "Slice.BlockerNote") {
		t.Fatalf("undeclared clear error = %v, want field-specific rejection", err)
	}
	if err := applySlicesArtifactUpdate(fileArtifactStore{}, dir, detail, func(_ *PlanDetail, changes *ArtifactChangeSet) error {
		return changes.ClearSliceBlockerNote("001-a")
	}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "slices.json"), &raw)
	sliceObject := raw["slices"].([]any)[0].(map[string]any)
	if value, exists := sliceObject["blocker_note"]; !exists || value != "" {
		t.Fatalf("declared clear did not persist blocker_note as an explicit empty string: %#v", value)
	}
}

func TestMigratedCurrentSliceRequiresDeclaredClear(t *testing.T) {
	field, ok := reflect.TypeOf(PlanState{}).FieldByName("CurrentSlice")
	if !ok || !strings.Contains(field.Tag.Get("json"), "omitempty") {
		t.Fatalf("PlanState.CurrentSlice must preserve by default with omitempty, tag=%q", field.Tag.Get("json"))
	}

	dir := t.TempDir()
	state := clearableContractBaseState()
	state.Status = StatusInProgress
	state.Plan.PendingSlices = []string{"001-a"}
	state.Plan.CurrentSlice = new("001-a")
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}

	intended := cloneState(state)
	intended.Plan.CurrentSlice = nil
	if err := writeState(dir, intended); err != nil {
		t.Fatal(err)
	}
	if got := readStateFile(t, dir).Plan.CurrentSlice; got == nil || *got != "001-a" {
		t.Fatalf("preserve-only write changed current_slice to %v", got)
	}

	detail := &PlanDetail{Dir: dir, State: intended}
	loadedBaseline := cloneState(state)
	detail.loadedStateBaseline = &loadedBaseline
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err == nil || !strings.Contains(err.Error(), "CurrentSlice") {
		t.Fatalf("undeclared clear error = %v, want field-specific rejection", err)
	}

	changes := NewArtifactChangeSet(detail)
	changes.ClearPlanCurrentSlice()
	if err := record.PersistStateChanges(changes); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	planObject := raw["plan"].(map[string]any)
	if value, exists := planObject["current_slice"]; !exists || value != nil {
		t.Fatalf("declared clear did not persist current_slice as explicit null: %#v", value)
	}
}

func TestMigratedPlanReviewRequiresDeclaredReplacement(t *testing.T) {
	reviewType := reflect.TypeOf(PlanReview{})
	for _, fieldName := range []string{"Verdict", "Summary", "FindingsCount", "Findings", "CommitMessage", "ReviewedAt"} {
		field, ok := reviewType.FieldByName(fieldName)
		if !ok || !strings.Contains(field.Tag.Get("json"), "omitempty") {
			t.Fatalf("PlanReview.%s must preserve by default with omitempty, tag=%q", fieldName, field.Tag.Get("json"))
		}
	}

	dir := t.TempDir()
	state := clearableContractBaseState()
	state.Plan.Review = &PlanReview{
		Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "ready", FindingsCount: 1,
		Findings:      []ReviewFinding{{Message: "old finding"}},
		CommitMessage: &ReviewCommitMessage{Subject: "fix(plan): old", Body: "old body"},
		ReviewedAt:    time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC),
	}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}

	intended := cloneState(state)
	intended.Plan.Review = &PlanReview{Status: ReviewStatusError}
	if err := writeState(dir, intended); err != nil {
		t.Fatal(err)
	}
	preserved := readStateFile(t, dir).Plan.Review
	if preserved == nil || preserved.Verdict == "" || preserved.Summary == "" || preserved.FindingsCount == 0 || len(preserved.Findings) == 0 || preserved.CommitMessage == nil || preserved.ReviewedAt.IsZero() {
		t.Fatalf("preserve-only write erased review fields: %+v", preserved)
	}

	detail := &PlanDetail{Dir: dir, State: intended}
	loadedBaseline := cloneState(state)
	detail.loadedStateBaseline = &loadedBaseline
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err == nil || !strings.Contains(err.Error(), "PlanReview") {
		t.Fatalf("undeclared review replacement error = %v, want field-specific rejection", err)
	}

	changes := NewArtifactChangeSet(detail)
	if err := changes.ReplacePlanReview(*intended.Plan.Review); err != nil {
		t.Fatal(err)
	}
	if err := record.PersistStateChanges(changes); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	review := raw["plan"].(map[string]any)["review"].(map[string]any)
	if review["verdict"] != "" || review["summary"] != "" || review["findings_count"] != float64(0) {
		t.Fatalf("declared replacement did not lower scalar zeros: %#v", review)
	}
	if findings, ok := review["findings"].([]any); !ok || len(findings) != 0 {
		t.Fatalf("declared replacement did not persist findings: []: %#v", review)
	}
	if value, exists := review["commit_message"]; !exists || value != nil {
		t.Fatalf("declared replacement did not persist commit_message: null: %#v", review)
	}
	if reviewedAt, exists := review["reviewed_at"]; !exists || reviewedAt == nil {
		t.Fatalf("declared replacement did not persist reviewed_at zero: %#v", review)
	}
}

func TestRecordWorkspaceReadyExplicitlyClearsPersistedDependencyEvidence(t *testing.T) {
	dir := t.TempDir()
	state := clearableContractBaseState()
	state.Workspace = &Workspace{
		Strategy: WorkspaceStrategyWorktree, LifecycleStatus: WorkspaceStatusPreparing,
		DependencyFailure: "npm install failed", DependencyFingerprint: "old-fingerprint",
	}
	detail := &PlanDetail{Dir: dir, State: state, Slices: SlicesFile{Schema: "tao.plan.slices.v1", PlanID: state.Plan.ID}}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := writeSlices(dir, detail.Slices); err != nil {
		t.Fatal(err)
	}
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.RecordWorkspaceReady(WorkspaceReadyRequest{
		DependencyStatus: DependencyPreparationStatusReady, ClearDependencyFailure: true,
		ClearDependencyFingerprint: true, PreparedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	workspace := raw["workspace"].(map[string]any)
	for _, key := range []string{"dependency_preparation_failure", "dependency_fingerprint"} {
		value, exists := workspace[key]
		if !exists || value != "" {
			t.Fatalf("typed ready clear did not lower %s as an explicit empty string: %#v", key, value)
		}
	}
}

func TestMigratedWorkspaceFieldsRequireDeclaredClear(t *testing.T) {
	tests := []struct {
		fieldName string
		jsonKey   string
		seed      func(*Workspace)
		zero      func(*Workspace)
		declare   func(*ArtifactChangeSet)
	}{
		{
			fieldName: "DependencyFailure",
			jsonKey:   "dependency_preparation_failure",
			seed:      func(workspace *Workspace) { workspace.DependencyFailure = "npm install failed" },
			zero:      func(workspace *Workspace) { workspace.DependencyFailure = "" },
			declare:   (*ArtifactChangeSet).ClearWorkspaceDependencyFailure,
		},
		{
			fieldName: "DependencyFingerprint",
			jsonKey:   "dependency_fingerprint",
			seed:      func(workspace *Workspace) { workspace.DependencyFingerprint = "old-fingerprint" },
			zero:      func(workspace *Workspace) { workspace.DependencyFingerprint = "" },
			declare:   (*ArtifactChangeSet).ClearWorkspaceDependencyFingerprint,
		},
	}

	workspaceType := reflect.TypeOf(Workspace{})
	for _, tt := range tests {
		t.Run(tt.fieldName, func(t *testing.T) {
			field, ok := workspaceType.FieldByName(tt.fieldName)
			if !ok || !strings.Contains(field.Tag.Get("json"), "omitempty") {
				t.Fatalf("Workspace.%s must preserve by default with omitempty, tag=%q", tt.fieldName, field.Tag.Get("json"))
			}

			dir := t.TempDir()
			state := clearableContractBaseState()
			state.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree}
			tt.seed(state.Workspace)
			if err := writeState(dir, state); err != nil {
				t.Fatal(err)
			}

			intended := state
			workspace := *state.Workspace
			intended.Workspace = &workspace
			tt.zero(intended.Workspace)
			if err := writeState(dir, intended); err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
			workspaceObject := raw["workspace"].(map[string]any)
			if workspaceObject[tt.jsonKey] == "" {
				t.Fatalf("preserve-only write unexpectedly cleared %s", tt.jsonKey)
			}

			detail := &PlanDetail{Dir: dir, State: intended}
			loadedBaseline := cloneState(state)
			detail.loadedStateBaseline = &loadedBaseline
			record, err := NewPlanRecord(dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.PersistState(); err == nil || !strings.Contains(err.Error(), tt.fieldName) {
				t.Fatalf("undeclared clear error = %v, want field-specific rejection", err)
			}

			changes := NewArtifactChangeSet(detail)
			tt.declare(changes)
			if err := record.PersistStateChanges(changes); err != nil {
				t.Fatal(err)
			}
			readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
			workspaceObject = raw["workspace"].(map[string]any)
			value, exists := workspaceObject[tt.jsonKey]
			if !exists || value != "" {
				t.Fatalf("declared clear did not persist %s as an explicit empty string: %#v", tt.jsonKey, value)
			}
		})
	}
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
