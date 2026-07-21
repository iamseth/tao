package workspace

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

// PhysicalPath resolves symlinks and returns an absolute, clean path. Boundary
// inspection uses this seam so path identity follows workspace ownership rather
// than being reimplemented by run orchestration.
func PhysicalPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

// ExecutionRootIdentity holds the resolved execution root for a plan together
// with the physical workspace strategy that selected it.
type ExecutionRootIdentity struct {
	// Root is the filesystem root where plan commands should execute.
	Root string
	// Strategy is the physical workspace strategy: "current" or "worktree".
	Strategy string
	// Separate is true when Root is a distinct filesystem path from the plan's
	// repo root. Current-mode roots are never separate.
	Separate bool
}

// ResolveExecutionRoot derives the filesystem root where a plan should run.
// A non-empty plan-recorded workspace.strategy wins over config.Strategy. When
// no strategy is recorded, config.Strategy must already contain the caller's
// chosen default (for example, one derived from execution mode); this resolver
// intentionally does not inspect execution mode itself.
//
// Current strategy returns the trimmed state.repo.root. Worktree strategy uses
// recorded workspace.path first, resolving relative paths under state.repo.root,
// then recorded workspace.root joined with the plan ID, and finally config.Root
// joined with the plan ID.
func ResolveExecutionRoot(detail *plan.PlanDetail, config Config) (ExecutionRootIdentity, error) {
	if detail == nil {
		return ExecutionRootIdentity{}, fmt.Errorf("plan detail is nil")
	}

	strategy := strings.TrimSpace(config.Strategy)
	if detail.State.Workspace != nil {
		if recorded := strings.TrimSpace(detail.State.Workspace.Strategy); recorded != "" {
			strategy = recorded
		}
	}

	switch strategy {
	case plan.WorkspaceStrategyCurrent:
		root := strings.TrimSpace(detail.State.Repo.Root)
		if root == "" {
			return ExecutionRootIdentity{}, fmt.Errorf("plan %s does not record a repo root", planIDForError(detail))
		}
		return ExecutionRootIdentity{Root: root, Strategy: plan.WorkspaceStrategyCurrent}, nil
	case plan.WorkspaceStrategyWorktree:
		identity := resolvePlanWorktreeIdentity(detail, config)
		if identity.Path == "" {
			return ExecutionRootIdentity{}, fmt.Errorf("worktree root could not be resolved for plan %s", planIDForError(detail))
		}
		return ExecutionRootIdentity{Root: identity.Path, Strategy: plan.WorkspaceStrategyWorktree, Separate: identity.Separate}, nil
	case "":
		return ExecutionRootIdentity{}, fmt.Errorf("workspace strategy is required")
	default:
		return ExecutionRootIdentity{}, fmt.Errorf("unsupported workspace strategy %q", strategy)
	}
}

// PlanWorktreeIdentity holds the resolved worktree path and whether it forms
// a separate working copy from the plan's repo root.
type PlanWorktreeIdentity struct {
	// Path is the canonical (filepath.Clean) worktree path derived from the
	// plan's recorded workspace metadata, or empty when not determinable. Relative
	// recorded paths are resolved under the plan's repo root when available.
	Path string
	// Separate is true when Path is a distinct filesystem path from the
	// plan's repo root.
	Separate bool
}

// ResolveRecordedWorktree derives a worktree identity only from recorded path
// or root metadata. It accepts legacy workspace records whose strategy is empty
// so recovery can inspect them without guessing from current configuration.
func ResolveRecordedWorktree(detail *plan.PlanDetail) PlanWorktreeIdentity {
	if detail == nil || detail.State.Workspace == nil {
		return PlanWorktreeIdentity{}
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	path := recordedWorkspacePath(detail, repoRoot)
	if path == "" {
		path = recordedWorkspaceRootPath(detail)
	}
	if path == "" {
		return PlanWorktreeIdentity{}
	}
	path = filepath.Clean(path)
	return PlanWorktreeIdentity{Path: path, Separate: repoRoot == "" || filepath.Clean(repoRoot) != path}
}

// ResolvePlanWorktree derives the worktree identity for a plan from its
// recorded workspace metadata. The path normalization uses filepath.Clean,
// matching what Manager uses at worktree-creation time.
//
// When the plan records a worktree strategy but no path, the plan-recorded
// workspace root wins over runtime config: the plan may have been created
// under an older or custom root, and deriving the path from today's config
// would point at a directory that never existed (candidateWorktreeRoots
// defends the same drift). Only a plan with neither path nor root falls back
// to what Manager.workspacePath would create under the given config.
func ResolvePlanWorktree(detail *plan.PlanDetail, config Config) PlanWorktreeIdentity {
	if detail == nil || detail.State.Workspace == nil || detail.State.Workspace.Strategy != plan.WorkspaceStrategyWorktree {
		return PlanWorktreeIdentity{}
	}
	return resolvePlanWorktreeIdentity(detail, config)
}

func resolvePlanWorktreeIdentity(detail *plan.PlanDetail, config Config) PlanWorktreeIdentity {
	if detail == nil {
		return PlanWorktreeIdentity{}
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	path := ""
	if detail.State.Workspace != nil {
		path = recordedWorkspacePath(detail, repoRoot)
		if path == "" {
			path = recordedWorkspaceRootPath(detail)
		}
	}
	if path == "" {
		path = resolvedWorktreePath(repoRoot, detail.State.Plan.ID, config)
	}
	if path == "" {
		return PlanWorktreeIdentity{}
	}
	path = filepath.Clean(path)
	separate := repoRoot == "" || filepath.Clean(repoRoot) != path
	return PlanWorktreeIdentity{Path: path, Separate: separate}
}

// recordedWorkspacePath returns the plan-recorded workspace path, resolving a
// relative path under the plan's repo root so callers do not accidentally bind
// it to their process working directory.
func recordedWorkspacePath(detail *plan.PlanDetail, repoRoot string) string {
	path := strings.TrimSpace(detail.State.Workspace.Path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && repoRoot != "" {
		path = filepath.Join(repoRoot, path)
	}
	return path
}

// recordedWorkspaceRootPath derives the worktree path from the plan-recorded
// workspace root joined with the plan ID, resolving a relative root under the
// plan's repo root. Empty when the plan records no root or the inputs cannot
// form an absolute path.
func recordedWorkspaceRootPath(detail *plan.PlanDetail) string {
	root := strings.TrimSpace(detail.State.Workspace.Root)
	planID := strings.TrimSpace(detail.State.Plan.ID)
	if root == "" || planID == "" {
		return ""
	}
	if !filepath.IsAbs(root) {
		repoRoot := strings.TrimSpace(detail.State.Repo.Root)
		if repoRoot == "" {
			return ""
		}
		root = filepath.Join(repoRoot, root)
	}
	return filepath.Join(root, planID)
}

// resolvedWorktreePath returns the worktree path the Manager would create for
// planID using the same normalization as Manager.workspacePath (filepath.Clean
// applied to config root joined with planID). Returns an empty string when the
// inputs are insufficient to derive an absolute path.
func resolvedWorktreePath(repoRoot, planID string, config Config) string {
	if planID == "" || config.Root == "" {
		return ""
	}
	root := config.Root
	if !filepath.IsAbs(root) {
		if repoRoot == "" {
			return ""
		}
		root = filepath.Join(repoRoot, root)
	}
	return filepath.Clean(filepath.Join(root, planID))
}

func planIDForError(detail *plan.PlanDetail) string {
	planID := strings.TrimSpace(detail.State.Plan.ID)
	if planID == "" {
		return "<unknown>"
	}
	return planID
}
