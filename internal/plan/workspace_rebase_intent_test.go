package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceRebaseIntentRecordIsValidatedIdempotentAndExactClear(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	intent := testWorkspaceRebaseIntent()

	if err := record.RecordWorkspaceRebaseIntent(intent); err != nil {
		t.Fatal(err)
	}
	if got := detail.State.Workspace.RebaseIntent; got == nil || *got != intent {
		t.Fatalf("recorded intent = %#v, want %#v", got, intent)
	}
	if err := record.RecordWorkspaceRebaseIntent(intent); err != nil {
		t.Fatalf("exact retry: %v", err)
	}

	conflict := intent
	conflict.NewBaseSHA = "dddddddddddddddddddddddddddddddddddddddd"
	if err := record.RecordWorkspaceRebaseIntent(conflict); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting record error = %v", err)
	}
	if err := record.ClearWorkspaceRebaseIntent(conflict); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mismatched clear error = %v", err)
	}
	if got := readStateFile(t, dir).Workspace.RebaseIntent; got == nil || *got != intent {
		t.Fatalf("mismatched operations changed intent to %#v", got)
	}

	if err := record.ClearWorkspaceRebaseIntent(intent); err != nil {
		t.Fatal(err)
	}
	if detail.State.Workspace.RebaseIntent != nil {
		t.Fatalf("in-memory intent was not cleared: %#v", detail.State.Workspace.RebaseIntent)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	workspace := raw["workspace"].(map[string]any)
	if value, exists := workspace["rebase_intent"]; !exists || value != nil {
		t.Fatalf("clear did not persist explicit null: %#v", workspace)
	}
	if err := record.ClearWorkspaceRebaseIntent(intent); err != nil {
		t.Fatalf("settled retry: %v", err)
	}
}

func TestWorkspaceRebaseSettlementPersistsBoundaryAndClearAtomically(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	intent := testWorkspaceRebaseIntent()
	if err := record.RecordWorkspaceRebaseIntent(intent); err != nil {
		t.Fatal(err)
	}
	settlement := WorkspaceRebaseSettlement{
		Branch: intent.Branch, BaseSHA: intent.NewBaseSHA, BaseCurrentSHA: intent.NewBaseSHA,
		HeadSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", BaseStatus: "current",
		RefreshStatus: "not_needed", RebaseStatus: "not_needed", LifecycleStatus: WorkspaceStatusPreparing,
	}
	mismatched := intent
	mismatched.OldHeadSHA = "ffffffffffffffffffffffffffffffffffffffff"
	if err := record.SettleWorkspaceRebase(mismatched, settlement); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mismatched settlement error = %v", err)
	}
	if persisted := readStateFile(t, dir).Workspace; persisted.RebaseIntent == nil || *persisted.RebaseIntent != intent || persisted.HeadSHA == settlement.HeadSHA {
		t.Fatalf("mismatched settlement partially persisted: %#v", persisted)
	}
	if err := record.SettleWorkspaceRebase(intent, settlement); err != nil {
		t.Fatal(err)
	}

	persisted := readStateFile(t, dir).Workspace
	if persisted == nil || persisted.RebaseIntent != nil {
		t.Fatalf("persisted settlement retained intent: %#v", persisted)
	}
	if persisted.Branch != settlement.Branch || persisted.BaseSHA != settlement.BaseSHA || persisted.BaseCurrentSHA != settlement.BaseCurrentSHA || persisted.HeadSHA != settlement.HeadSHA {
		t.Fatalf("persisted settlement boundary = %#v, want %#v", persisted, settlement)
	}
	if persisted.BaseStatus != settlement.BaseStatus || persisted.RefreshStatus != settlement.RefreshStatus || persisted.RebaseStatus != settlement.RebaseStatus || persisted.LifecycleStatus != settlement.LifecycleStatus {
		t.Fatalf("persisted settlement statuses = %#v, want %#v", persisted, settlement)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	workspace := raw["workspace"].(map[string]any)
	if value, exists := workspace["rebase_intent"]; !exists || value != nil {
		t.Fatalf("settlement did not persist explicit intent clear: %#v", workspace)
	}
}

func TestWorkspaceRebaseIntentValidation(t *testing.T) {
	valid := testWorkspaceRebaseIntent()
	legacy := valid
	legacy.CommitSeriesFingerprint = "v1:sha256:" + strings.Repeat("d", 64)
	if err := validateWorkspaceRebaseIntent(legacy); err != nil {
		t.Fatalf("legacy v1 fingerprint rejected: %v", err)
	}
	current := valid
	current.CommitSeriesFingerprint = "v5:sha256:" + strings.Repeat("d", 64)
	if err := validateWorkspaceRebaseIntent(current); err != nil {
		t.Fatalf("current v5 fingerprint rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*WorkspaceRebaseIntent)
	}{
		{name: "blank branch", mutate: func(i *WorkspaceRebaseIntent) { i.Branch = " " }},
		{name: "multiline base branch", mutate: func(i *WorkspaceRebaseIntent) { i.BaseBranch = "main\nother" }},
		{name: "invalid old head", mutate: func(i *WorkspaceRebaseIntent) { i.OldHeadSHA = "not-a-sha" }},
		{name: "empty old base", mutate: func(i *WorkspaceRebaseIntent) { i.OldBaseSHA = "" }},
		{name: "spaced new base", mutate: func(i *WorkspaceRebaseIntent) { i.NewBaseSHA = " aaaaaaa" }},
		{name: "negative count", mutate: func(i *WorkspaceRebaseIntent) { i.CommitCount = -1 }},
		{name: "unversioned fingerprint", mutate: func(i *WorkspaceRebaseIntent) { i.CommitSeriesFingerprint = strings.Repeat("a", 64) }},
		{name: "unsupported fingerprint version", mutate: func(i *WorkspaceRebaseIntent) { i.CommitSeriesFingerprint = "v6:sha256:" + strings.Repeat("a", 64) }},
		{name: "uppercase fingerprint", mutate: func(i *WorkspaceRebaseIntent) { i.CommitSeriesFingerprint = "v2:sha256:" + strings.Repeat("A", 64) }},
		{name: "zero time", mutate: func(i *WorkspaceRebaseIntent) { i.CreatedAt = time.Time{} }},
		{name: "non UTC time", mutate: func(i *WorkspaceRebaseIntent) { i.CreatedAt = i.CreatedAt.In(time.FixedZone("offset", 3600)) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := valid
			tt.mutate(&intent)
			if err := validateWorkspaceRebaseIntent(intent); err == nil {
				t.Fatalf("invalid intent accepted: %#v", intent)
			}
		})
	}
}

func TestWorkspaceRebaseIntentClearRequiresDeclarationAndPreservesUnknownFields(t *testing.T) {
	field, ok := reflect.TypeOf(Workspace{}).FieldByName("RebaseIntent")
	if !ok || !strings.Contains(field.Tag.Get("json"), "omitempty") {
		t.Fatalf("Workspace.RebaseIntent must preserve by default, tag=%q", field.Tag.Get("json"))
	}

	dir := t.TempDir()
	state := clearableContractBaseState()
	state.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree, RebaseIntent: ptrWorkspaceRebaseIntent(testWorkspaceRebaseIntent())}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	workspace := raw["workspace"].(map[string]any)
	workspace["unknown_workspace_field"] = "keep"
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	intended := cloneState(state)
	intended.Workspace.RebaseIntent = nil
	if err := writeState(dir, intended); err != nil {
		t.Fatal(err)
	}
	if got := readStateFile(t, dir).Workspace.RebaseIntent; got == nil {
		t.Fatal("undeclared nil did not preserve existing intent")
	}

	detail := &PlanDetail{Dir: dir, State: intended}
	baseline := cloneState(state)
	detail.loadedStateBaseline = &baseline
	record, err := NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err == nil || !strings.Contains(err.Error(), "RebaseIntent") {
		t.Fatalf("undeclared clear error = %v", err)
	}
	changes := NewArtifactChangeSet(detail)
	changes.ClearWorkspaceRebaseIntent()
	if err := record.PersistStateChanges(changes); err != nil {
		t.Fatal(err)
	}
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	workspace = raw["workspace"].(map[string]any)
	if value, exists := workspace["rebase_intent"]; !exists || value != nil {
		t.Fatalf("declared clear did not lower null: %#v", workspace)
	}
	if workspace["unknown_workspace_field"] != "keep" {
		t.Fatalf("declared clear erased unknown workspace field: %#v", workspace)
	}
}

func testWorkspaceRebaseIntent() WorkspaceRebaseIntent {
	return WorkspaceRebaseIntent{
		Branch:                  "tao/plan-a",
		BaseBranch:              "main",
		OldHeadSHA:              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OldBaseSHA:              "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		NewBaseSHA:              "cccccccccccccccccccccccccccccccccccccccc",
		CommitCount:             2,
		CommitSeriesFingerprint: "v2:sha256:" + strings.Repeat("d", 64),
		CreatedAt:               time.Date(2026, 8, 5, 3, 0, 0, 0, time.UTC),
	}
}

func ptrWorkspaceRebaseIntent(intent WorkspaceRebaseIntent) *WorkspaceRebaseIntent {
	return &intent
}
