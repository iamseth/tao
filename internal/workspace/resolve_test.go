package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestResolveExecutionRoot(t *testing.T) {
	const repoRoot = "/repo/root"
	const planID = "plan-a"
	const worktreePath = "/repo/worktrees/plan-a"

	currentConfig := DefaultConfig()
	currentConfig.Strategy = StrategyCurrent
	customConfig := DefaultConfig()
	customConfig.Root = "/srv/worktrees"
	blankStrategyConfig := DefaultConfig()
	blankStrategyConfig.Strategy = ""

	tests := []struct {
		name    string
		detail  *plan.PlanDetail
		config  Config
		want    ExecutionRootIdentity
		wantErr string
	}{
		{
			name:    "nil detail errors like review and prepare",
			config:  DefaultConfig(),
			wantErr: "plan detail is nil",
		},
		{
			name:   "nil workspace uses caller-derived current strategy",
			detail: executionRootDetail(repoRoot, planID, nil),
			config: currentConfig,
			want:   ExecutionRootIdentity{Root: repoRoot, Strategy: plan.WorkspaceStrategyCurrent},
		},
		{
			name:   "nil workspace uses caller-derived worktree strategy",
			detail: executionRootDetail(repoRoot, planID, nil),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, ".tao", "workspaces", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "current strategy returns trimmed repo root and wins over config",
			detail: executionRootDetail("  "+repoRoot+"  ", planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Path: worktreePath}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: repoRoot, Strategy: plan.WorkspaceStrategyCurrent},
		},
		{
			name:   "empty persisted strategy uses caller-derived worktree default",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Path: worktreePath}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: worktreePath, Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:    "empty persisted strategy requires caller default",
			detail:  executionRootDetail(repoRoot, planID, &plan.Workspace{Path: worktreePath}),
			config:  blankStrategyConfig,
			wantErr: "workspace strategy is required",
		},
		{
			name:   "worktree recorded path wins over root and config",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: worktreePath + "/", Root: "custom/worktrees"}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: worktreePath, Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree recorded relative path resolves under repo root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: ".tao/workspaces/" + planID}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, ".tao", "workspaces", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree recorded absolute root wins over config",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: "/opt/tao/worktrees"}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join("/opt/tao/worktrees", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree recorded relative root resolves under repo root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: "custom/worktrees"}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, "custom", "worktrees", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree without path or root falls back to default config root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, ".tao", "workspaces", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			// Legacy review resolution has no runtime config and would fall back to
			// the repo root for this shape. The unified resolver intentionally uses
			// the caller-supplied config root so custom-root worktree plans resolve to
			// the path Manager would create.
			name:   "worktree without path or root honors custom config root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join("/srv/worktrees", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:    "empty plan id without recorded path or root errors",
			detail:  executionRootDetail(repoRoot, "", &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}),
			config:  DefaultConfig(),
			wantErr: "worktree root could not be resolved",
		},
		{
			name:   "repo root equals worktree path is not separate",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: repoRoot}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: repoRoot, Strategy: plan.WorkspaceStrategyWorktree, Separate: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveExecutionRoot(tt.detail, tt.config)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveExecutionRoot error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveExecutionRoot returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveExecutionRoot() = %#v, want %#v", got, tt.want)
			}
			if tt.detail != nil && tt.detail.State.Workspace != nil && tt.detail.State.Workspace.Strategy == plan.WorkspaceStrategyWorktree {
				worktree := ResolvePlanWorktree(tt.detail, tt.config)
				if got.Root != worktree.Path || got.Separate != worktree.Separate {
					t.Fatalf("ResolveExecutionRoot() = %#v, ResolvePlanWorktree() = %#v", got, worktree)
				}
			}
		})
	}
}

func TestPhysicalPathResolvesSymlinkedRoot(t *testing.T) {
	physicalRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(physicalRoot, alias); err != nil {
		t.Fatal(err)
	}

	got, err := PhysicalPath(alias)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(physicalRoot) {
		t.Fatalf("PhysicalPath() = %q, want %q", got, physicalRoot)
	}
}

func TestResolveRecordedWorktreeSupportsLegacyMetadata(t *testing.T) {
	detail := executionRootDetail("/repo", "plan-a", &plan.Workspace{Root: ".tao/workspaces"})
	got := ResolveRecordedWorktree(detail)
	want := filepath.Join("/repo", ".tao/workspaces", "plan-a")
	if got.Path != want || !got.Separate {
		t.Fatalf("ResolveRecordedWorktree() = %#v, want path %q separate", got, want)
	}
}

func executionRootDetail(repoRoot string, planID string, workspace *plan.Workspace) *plan.PlanDetail {
	return &plan.PlanDetail{
		State: plan.State{
			Repo:      plan.Repo{Root: repoRoot},
			Plan:      plan.PlanState{ID: planID},
			Workspace: workspace,
		},
	}
}
