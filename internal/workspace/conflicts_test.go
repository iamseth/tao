package workspace

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestAnalyzeConflictOverlappingExpectedFilesWaits(t *testing.T) {
	result := AnalyzeConflict(conflictPlan("plan-a", []string{"internal/queue/run_manager.go"}), []ConflictPlan{conflictPlan("plan-b", []string{"internal/queue/run_manager.go"})}, ConflictOptions{})
	if !result.Wait || !strings.Contains(result.Reason, "expected files overlap") {
		t.Fatalf("expected overlapping files to wait, got %+v", result)
	}
}

func TestAnalyzeConflictSameBranchWaits(t *testing.T) {
	candidate := conflictPlan("plan-a", []string{"internal/queue/a.go"})
	candidate.Workspace.Branch = "tao/shared"
	peer := conflictPlan("plan-b", []string{"internal/plan/b.go"})
	peer.Workspace.Branch = "tao/shared"

	result := AnalyzeConflict(candidate, []ConflictPlan{peer}, ConflictOptions{})
	if !result.Wait || !strings.Contains(result.Reason, "branch") {
		t.Fatalf("expected same branch to wait, got %+v", result)
	}
}

func TestAnalyzeConflictMissingWorkspaceWaits(t *testing.T) {
	candidate := conflictPlan("plan-a", []string{"internal/queue/a.go"})
	candidate.Workspace.Path = ""

	result := AnalyzeConflict(candidate, nil, ConflictOptions{})
	if !result.Wait || !strings.Contains(result.Reason, "workspace is missing") {
		t.Fatalf("expected missing workspace to wait, got %+v", result)
	}
}

func TestAnalyzeConflictDirtyWorkspaceWaits(t *testing.T) {
	candidate := conflictPlan("plan-a", []string{"internal/queue/a.go"})
	candidate.WorkspaceDirty = true

	result := AnalyzeConflict(candidate, nil, ConflictOptions{})
	if !result.Wait || !strings.Contains(result.Reason, "uncommitted changes") {
		t.Fatalf("expected dirty workspace to wait, got %+v", result)
	}
}

func TestAnalyzeConflictDependencyInstallLimitWaits(t *testing.T) {
	candidate := conflictPlan("plan-a", []string{"internal/queue/a.go"})
	candidate.Workspace.DependencyPreparation = plan.DependencyPreparationStatusRunning
	peer := conflictPlan("plan-b", []string{"internal/plan/b.go"})
	peer.Workspace.DependencyPreparation = plan.DependencyPreparationStatusRunning

	result := AnalyzeConflict(candidate, []ConflictPlan{peer}, ConflictOptions{MaxParallelDependencyInstalls: 1})
	if !result.Wait || !strings.Contains(result.Reason, "dependency install limit") {
		t.Fatalf("expected dependency install limit to wait, got %+v", result)
	}
}

func TestAnalyzeConflictNonOverlappingPlansCanRun(t *testing.T) {
	candidate := conflictPlan("plan-a", []string{"internal/queue/a.go"})
	peer := conflictPlan("plan-b", []string{"internal/plan/b.go"})

	result := AnalyzeConflict(candidate, []ConflictPlan{peer}, ConflictOptions{MaxParallelDependencyInstalls: 1})
	if result.Wait {
		t.Fatalf("expected non-overlapping plans to run, got %+v", result)
	}
}

func conflictPlan(id string, expectedFiles []string) ConflictPlan {
	return ConflictPlan{
		ID:            id,
		Status:        plan.StatusPlanned,
		ExpectedFiles: expectedFiles,
		Workspace: &plan.Workspace{
			Strategy:        plan.WorkspaceStrategyWorktree,
			Path:            ".tao/workspaces/" + id,
			Branch:          "tao/" + id,
			BaseBranch:      "master",
			BaseSHA:         "abc123",
			LifecycleStatus: plan.WorkspaceStatusReady,
		},
	}
}
