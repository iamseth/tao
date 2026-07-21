package plan

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceMetadataRoundTrip(t *testing.T) {
	state := State{
		Plan: PlanState{ID: "plan"},
		Workspace: &Workspace{
			Strategy:              WorkspaceStrategyWorktree,
			Root:                  ".tao/workspaces",
			Path:                  ".tao/workspaces/plan",
			Branch:                "tao/plan",
			BaseBranch:            "master",
			BaseSHA:               "abc123",
			BaseCurrentSHA:        "def456",
			BaseStatus:            WorkspaceBaseStatusStale,
			HeadSHA:               "head123",
			PushedSHA:             "head123",
			RefreshStatus:         WorkspaceRefreshStatusNeeded,
			RebaseStatus:          WorkspaceRebaseStatusNeeded,
			LifecycleStatus:       WorkspaceStatusReady,
			DependencyPreparation: DependencyPreparationStatusReady,
			CleanupStatus:         WorkspaceCleanupStatusHeld,
		},
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded State
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Workspace == nil {
		t.Fatal("expected workspace metadata")
	}
	if decoded.Workspace.Strategy != WorkspaceStrategyWorktree || decoded.Workspace.CleanupStatus != WorkspaceCleanupStatusHeld || decoded.Workspace.BaseStatus != WorkspaceBaseStatusStale || decoded.Workspace.HeadSHA != "head123" {
		t.Fatalf("unexpected workspace metadata: %#v", decoded.Workspace)
	}
}

func TestWorkspaceMetadataRemainsOptional(t *testing.T) {
	var state State
	if err := json.Unmarshal([]byte(`{"plan":{"id":"legacy"}}`), &state); err != nil {
		t.Fatal(err)
	}

	if state.Workspace != nil {
		t.Fatalf("expected omitted workspace metadata to remain nil, got %#v", state.Workspace)
	}
}
