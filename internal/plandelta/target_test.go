package plandelta

import (
	"path/filepath"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

func TestResolveTarget(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	custom := workspace.DefaultConfig()
	custom.Root = other
	for _, tt := range []struct {
		name           string
		ws             *plan.Workspace
		config         workspace.Config
		path, strategy string
		separate       bool
	}{
		{"relative path", &plan.Workspace{Path: "trees/a"}, workspace.DefaultConfig(), filepath.Join(root, "trees/a"), plan.WorkspaceStrategyWorktree, true},
		{"absolute path", &plan.Workspace{Path: other}, workspace.DefaultConfig(), other, plan.WorkspaceStrategyWorktree, true},
		{"relative root", &plan.Workspace{Root: "trees"}, workspace.DefaultConfig(), filepath.Join(root, "trees/plan-a"), plan.WorkspaceStrategyWorktree, true},
		{"absolute root", &plan.Workspace{Root: other}, workspace.DefaultConfig(), filepath.Join(other, "plan-a"), plan.WorkspaceStrategyWorktree, true},
		{"config root", nil, custom, filepath.Join(other, "plan-a"), plan.WorkspaceStrategyWorktree, true},
		{"defaults", nil, workspace.DefaultConfig(), filepath.Join(root, ".tao/workspaces/plan-a"), plan.WorkspaceStrategyWorktree, true},
		{"current", &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Path: other}, workspace.DefaultConfig(), root, plan.WorkspaceStrategyCurrent, false},
		{"same root", &plan.Workspace{Path: root}, workspace.DefaultConfig(), root, plan.WorkspaceStrategyWorktree, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := &plan.PlanDetail{}
			detail.State.Plan.ID = "plan-a"
			detail.State.Repo.Root = root
			detail.State.Workspace = tt.ws
			got, err := ResolveTarget(detail, tt.config)
			want := Target{PlanID: "plan-a", RepoRoot: root, WorktreePath: tt.path, Branch: "tao/plan-a", Strategy: tt.strategy, Separate: tt.separate}
			if err != nil || got != want {
				t.Fatalf("got %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestResolveTargetTypedAndInvalid(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name, id, root, strategy string
		typed, recorded, wantErr bool
	}{
		{"typed requires later branch verification", "20261001-140136-plan-changes", root, workspace.StrategyWorktree, true, false, false},
		{"recorded derived branch", "20261001-140136-plan-changes", root, workspace.StrategyWorktree, true, true, false},
		{"invalid typed id", "plan-a", root, workspace.StrategyWorktree, true, false, true},
		{"missing id", "", root, workspace.StrategyWorktree, false, false, true},
		{"missing root", "plan-a", "", workspace.StrategyWorktree, false, false, true},
		{"relative root", "plan-a", "relative", workspace.StrategyWorktree, false, false, true},
		{"invalid strategy", "plan-a", root, "invalid", false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := &plan.PlanDetail{}
			detail.State.Plan.ID = tt.id
			detail.State.Repo.Root = tt.root
			if tt.typed {
				detail.State.Plan.ChangeType = "feat"
			}
			detail.State.Workspace = &plan.Workspace{Strategy: tt.strategy}
			if tt.recorded {
				detail.State.Workspace.Branch = "feature/plan-changes"
			}
			got, err := ResolveTarget(detail, workspace.DefaultConfig())
			if (err != nil) != tt.wantErr {
				t.Fatalf("target %+v error %v", got, err)
			}
			if !tt.wantErr && (got.Branch != "feature/plan-changes" || got.BranchIsNew != !tt.recorded) {
				t.Fatalf("target %+v", got)
			}
		})
	}
	if _, err := ResolveTarget(nil, workspace.DefaultConfig()); err == nil {
		t.Fatal("nil detail accepted")
	}
}
