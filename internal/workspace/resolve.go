package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// RepositoryRootFromGitCommonDir returns the canonical control-checkout root
// represented by git rev-parse --git-common-dir. Linked worktrees share this
// identity even though --show-toplevel returns a different path.
func RepositoryRootFromGitCommonDir(worktreeRoot, commonDir string) (string, error) {
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return "", fmt.Errorf("git common directory is empty")
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreeRoot, commonDir)
	}
	canonical, err := PhysicalPath(commonDir)
	if err != nil {
		return "", fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	if filepath.Base(canonical) != ".git" {
		return "", fmt.Errorf("git common directory %q does not identify a non-bare repository root", canonical)
	}
	return filepath.Dir(canonical), nil
}

// ManagedWorktreeOwnership identifies the one active plan that canonically
// owns a standalone-commit target.
type ManagedWorktreeOwnership struct {
	PlanID  string
	Command string
}

// UnresolvedInvalidManagedWorktreeOwners returns invalid plans whose ownership
// of the target worktree cannot be safely disproved. A readable state artifact
// can disprove ownership through exact repository, path, and cleanup metadata.
// Once an active plan identifies the target physical worktree, live branch drift
// cannot authorize mutation. An unreadable state artifact remains unresolved
// because the rest of a corrupt plan must not authorize mutation.
func UnresolvedInvalidManagedWorktreeOwners(repoRoot, worktreeRoot, _ string, summaries []plan.PlanSummary) ([]string, error) {
	canonicalRepo, err := PhysicalPath(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize repository identity: %w", err)
	}
	canonicalWorktree, err := PhysicalPath(worktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize worktree path: %w", err)
	}
	var unresolved []string
	for _, summary := range summaries {
		if summary.Status != plan.StatusInvalid {
			continue
		}
		state, stateErr := readInvalidPlanState(summary.Dir)
		if stateErr != nil || invalidPlanMayOwnWorktree(state, canonicalRepo, canonicalWorktree) {
			unresolved = append(unresolved, summary.ID)
		}
	}
	sort.Strings(unresolved)
	return unresolved, nil
}

func readInvalidPlanState(dir string) (plan.State, error) {
	file, err := os.Open(filepath.Join(dir, "state.json")) // #nosec G304 -- dir comes from the repository's enumerated plan summaries.
	if err != nil {
		return plan.State{}, err
	}
	defer func() { _ = file.Close() }()
	var state plan.State
	if err := json.NewDecoder(file).Decode(&state); err != nil {
		return plan.State{}, err
	}
	return state, nil
}

func invalidPlanMayOwnWorktree(state plan.State, canonicalRepo, canonicalWorktree string) bool {
	detail := &plan.PlanDetail{State: state}
	if !managedPlanCanOwnWorktree(detail) {
		return false
	}
	repoRoot := strings.TrimSpace(state.Repo.Root)
	if repoRoot == "" {
		return true
	}
	recordedRepo, repoErr := PhysicalPath(repoRoot)
	if repoErr != nil {
		return true
	}
	if recordedRepo != canonicalRepo {
		return false
	}
	identity := ResolveRecordedWorktree(detail)
	if identity.Path == "" {
		return true
	}
	recordedWorktree, pathErr := PhysicalPath(identity.Path)
	if pathErr != nil {
		return true
	}
	return recordedWorktree == canonicalWorktree
}

// ResolveManagedWorktreeOwnership matches a Git worktree against durable plan
// metadata. Repository and physical path identify candidate owners. Their
// recorded branch must also match the live branch before ownership is resolved;
// branch drift on an otherwise exact active workspace fails closed rather than
// making the workspace appear unmanaged.
func ResolveManagedWorktreeOwnership(repoRoot, worktreeRoot, branch string, details []*plan.PlanDetail) (*ManagedWorktreeOwnership, error) {
	canonicalRepo, err := PhysicalPath(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize repository identity: %w", err)
	}
	canonicalWorktree, err := PhysicalPath(worktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize worktree path: %w", err)
	}
	branch = strings.TrimSpace(branch)
	var candidates []*plan.PlanDetail
	for _, detail := range details {
		if !managedPlanCanOwnWorktree(detail) {
			continue
		}
		recordedRepo, repoErr := PhysicalPath(strings.TrimSpace(detail.State.Repo.Root))
		identity := ResolveRecordedWorktree(detail)
		recordedWorktree, pathErr := PhysicalPath(identity.Path)
		if repoErr != nil || pathErr != nil || recordedRepo != canonicalRepo || recordedWorktree != canonicalWorktree {
			continue
		}
		candidates = append(candidates, detail)
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, detail := range candidates {
			ids = append(ids, planIDForError(detail))
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("managed worktree ownership is ambiguous across plans %s", strings.Join(ids, ", "))
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	detail := candidates[0]
	recordedBranch := strings.TrimSpace(detail.State.Workspace.Branch)
	if recordedBranch == "" || branch == "" || recordedBranch != branch {
		return nil, fmt.Errorf("managed worktree ownership cannot be safely resolved for plan %s: recorded branch %q does not match live branch %q", planIDForError(detail), recordedBranch, branch)
	}
	return &ManagedWorktreeOwnership{PlanID: detail.State.Plan.ID, Command: managedWorktreeRecoveryCommand(detail)}, nil
}

func managedPlanCanOwnWorktree(detail *plan.PlanDetail) bool {
	if detail == nil || detail.State.Workspace == nil || detail.State.Workspace.Strategy != plan.WorkspaceStrategyWorktree {
		return false
	}
	workspace := detail.State.Workspace
	return workspace.CleanupStatus != plan.WorkspaceCleanupStatusDone && workspace.LifecycleStatus != plan.WorkspaceStatusCleaned
}

func managedWorktreeRecoveryCommand(detail *plan.PlanDetail) string {
	id := strings.TrimSpace(detail.State.Plan.ID)
	for _, slice := range detail.Slices.Slices {
		if slice.VerificationRepair != nil && slice.Completion == nil {
			return "tao run " + id
		}
	}
	if plan.CurrentFailedFinalVerification(detail) != nil {
		return "tao run --repair-verification " + id
	}
	if plan.PlanLifecycleStatus(detail) == plan.StatusChangesRequested {
		return "tao rework " + id
	}
	if detail.State.Plan.CurrentSlice != nil {
		for i := range detail.Slices.Slices {
			slice := &detail.Slices.Slices[i]
			if slice.ID != *detail.State.Plan.CurrentSlice || slice.Status != plan.StatusBlocked {
				continue
			}
			if slice.CommitIntent != nil || slice.Completion != nil || blockedSliceRequiresManualCompletion(detail, slice) {
				return "tao slice-complete"
			}
			if blockedSliceHasRestartBoundary(slice) {
				return "tao run --restart " + id
			}
			return "tao run --continue " + id
		}
	}
	return "tao run " + id
}

func blockedSliceHasRestartBoundary(slice *plan.Slice) bool {
	return slice.ExecutionStart != nil &&
		strings.TrimSpace(slice.ExecutionRoot) != "" &&
		slice.ExecutionStart.CommitPolicy == "slice" &&
		slice.ExecutionStart.WorkspaceStrategy == plan.WorkspaceStrategyWorktree &&
		strings.TrimSpace(slice.ExecutionStart.Branch) != "" &&
		strings.TrimSpace(slice.ExecutionStart.Head) != "" &&
		slice.CommitIntent == nil && slice.Completion == nil
}

func blockedSliceRequiresManualCompletion(detail *plan.PlanDetail, slice *plan.Slice) bool {
	if slice.ExecutionStart != nil {
		return slice.ExecutionStart.CommitPolicy == "none" || slice.ExecutionStart.WorkspaceStrategy == plan.WorkspaceStrategyCurrent
	}
	return detail.State.Plan.LastRunCommitPolicy == "none" ||
		(detail.State.Workspace != nil && detail.State.Workspace.Strategy == plan.WorkspaceStrategyCurrent)
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
