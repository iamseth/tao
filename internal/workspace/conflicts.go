package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

// ConflictPlan is the scheduling-relevant part of one plan.
type ConflictPlan struct {
	ID             string
	Status         string
	ExpectedFiles  []string
	Workspace      *plan.Workspace
	WorkspaceDirty bool
}

// ConflictOptions controls conservative resource checks.
type ConflictOptions struct {
	MaxParallelDependencyInstalls int
}

// ConflictResult describes why a candidate should wait.
type ConflictResult struct {
	Wait   bool
	Reason string
}

// AnalyzeConflict returns a wait reason when running candidate alongside peers is unsafe.
func AnalyzeConflict(candidate ConflictPlan, peers []ConflictPlan, options ConflictOptions) ConflictResult {
	if options.MaxParallelDependencyInstalls < 1 {
		options.MaxParallelDependencyInstalls = 1
	}
	if reason := workspaceWaitReason(candidate); reason != "" {
		return ConflictResult{Wait: true, Reason: reason}
	}
	installing := 0
	if dependencyRunning(candidate.Workspace) {
		installing++
	}
	for _, peer := range peers {
		if peer.ID == candidate.ID {
			continue
		}
		if reason := workspaceWaitReason(peer); reason != "" {
			return ConflictResult{Wait: true, Reason: fmt.Sprintf("plan %s: %s", peer.ID, reason)}
		}
		if overlapReason(candidate.ExpectedFiles, peer.ExpectedFiles) != "" {
			return ConflictResult{Wait: true, Reason: fmt.Sprintf("expected files overlap with plan %s", peer.ID)}
		}
		if sameNonEmpty(candidate.WorkspaceBranch(), peer.WorkspaceBranch()) {
			return ConflictResult{Wait: true, Reason: fmt.Sprintf("branch %q is already used by plan %s", candidate.WorkspaceBranch(), peer.ID)}
		}
		if sameNonEmpty(candidate.WorkspaceBaseBranch(), peer.WorkspaceBaseBranch()) && candidate.WorkspaceBaseSHA() != "" && peer.WorkspaceBaseSHA() != "" && candidate.WorkspaceBaseSHA() != peer.WorkspaceBaseSHA() {
			return ConflictResult{Wait: true, Reason: fmt.Sprintf("base %s differs from plan %s", candidate.WorkspaceBaseBranch(), peer.ID)}
		}
		if dependencyRunning(peer.Workspace) {
			installing++
		}
	}
	if installing > options.MaxParallelDependencyInstalls {
		return ConflictResult{Wait: true, Reason: "parallel dependency install limit reached"}
	}
	return ConflictResult{}
}

func workspaceWaitReason(p ConflictPlan) string {
	if p.Status != "" && p.Status != plan.StatusPlanned && p.Status != plan.StatusPending && p.Status != plan.StatusInProgress {
		return fmt.Sprintf("plan status is %s", p.Status)
	}
	if p.Workspace == nil {
		return ""
	}
	if p.WorkspaceDirty {
		return "workspace has uncommitted changes"
	}
	if p.Workspace.Path == "" && p.Workspace.Strategy == plan.WorkspaceStrategyWorktree {
		return "workspace is missing"
	}
	switch p.Workspace.LifecycleStatus {
	case "", plan.WorkspaceStatusReady:
	case plan.WorkspaceStatusPending, plan.WorkspaceStatusPreparing:
		return "workspace is not ready"
	default:
		return fmt.Sprintf("workspace status is %s", p.Workspace.LifecycleStatus)
	}
	switch p.Workspace.CleanupStatus {
	case "", plan.WorkspaceCleanupStatusHeld, plan.WorkspaceCleanupStatusPending:
		return ""
	default:
		return fmt.Sprintf("workspace cleanup status is %s", p.Workspace.CleanupStatus)
	}
}

func overlapReason(a, b []string) string {
	for _, left := range normalizeExpectedFiles(a) {
		for _, right := range normalizeExpectedFiles(b) {
			if left == right || pathContains(left, right) || pathContains(right, left) || uncertainPattern(left) || uncertainPattern(right) {
				return "overlap"
			}
		}
	}
	return ""
}

func normalizeExpectedFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Clean(path)))
	}
	return out
}

func pathContains(parent, child string) bool {
	return child != parent && strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

func uncertainPattern(path string) bool {
	return strings.ContainsAny(path, "*?[") || strings.Contains(path, "...")
}

func sameNonEmpty(a, b string) bool {
	return a != "" && a == b
}

func dependencyRunning(workspace *plan.Workspace) bool {
	return workspace != nil && workspace.DependencyPreparation == plan.DependencyPreparationStatusRunning
}

func (p ConflictPlan) WorkspaceBranch() string {
	if p.Workspace == nil {
		return ""
	}
	return p.Workspace.Branch
}

func (p ConflictPlan) WorkspaceBaseBranch() string {
	if p.Workspace == nil {
		return ""
	}
	return p.Workspace.BaseBranch
}

func (p ConflictPlan) WorkspaceBaseSHA() string {
	if p.Workspace == nil {
		return ""
	}
	return p.Workspace.BaseSHA
}
