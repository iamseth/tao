package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
)

const (
	CleanupPolicyManual        = "manual"
	CleanupPolicyCompletedOnly = "completed-only"
	CleanupPolicyBranchMerged  = "branch-merged"
	CleanupPolicyPRMerged      = "pr-merged"
	CleanupPolicyForce         = "force"
)

// PlanClean returns a cleanup decision without deleting files.
func (m *Manager) PlanClean(ctx context.Context, planID string) (CleanPlan, error) {
	status, err := m.Status(ctx, planID)
	if err != nil {
		return CleanPlan{}, err
	}
	plan := CleanPlan{PlanID: status.PlanID, Path: status.Path, Branch: status.Branch, Dirty: status.Dirty, Missing: status.Missing}
	branchExists, err := m.git.cleanup.BranchExists(ctx, status.Branch)
	if err != nil {
		return CleanPlan{}, err
	}
	plan.BranchExists = branchExists
	plan.ProtectedBranch = gitops.ProtectedBranch(status.Branch)
	merged := false
	if branchExists {
		merged, err = m.git.cleanup.BranchMerged(ctx, status.Branch)
		if err != nil {
			return CleanPlan{}, err
		}
	}

	switch {
	case status.Missing && branchExists:
		plan.Status = "missing-ambiguous"
		plan.Reason = "workspace is missing but branch exists"
		return plan, nil
	case status.Missing:
		plan.Status = "missing"
		plan.Reason = "workspace is missing"
		return plan, nil
	case plan.ProtectedBranch:
		plan.Status = "protected-branch"
		plan.Reason = "workspace branch is protected"
		return plan, nil
	case status.Dirty:
		plan.Status = "dirty"
		plan.Reason = "workspace has uncommitted changes"
		plan.Actions = worktreeRemoveActions(status.Path, true)
		return plan, nil
	case branchExists && !merged:
		plan.Status = "unmerged"
		plan.Reason = "workspace branch is not merged"
		plan.Actions = worktreeRemoveActions(status.Path, false)
		return plan, nil
	default:
		plan.Status = "clean"
		plan.CanRemove = true
		plan.Reason = "workspace is clean"
		plan.Actions = worktreeRemoveActions(status.Path, false)
		return plan, nil
	}
}

// Clean removes a prepared workspace after callers have accepted PlanClean.
func (m *Manager) Clean(ctx context.Context, planID string, options CleanOptions) (CleanPlan, error) {
	plan, err := m.PlanClean(ctx, planID)
	if err != nil {
		return CleanPlan{}, err
	}
	if plan.Missing || plan.ProtectedBranch {
		return plan, fmt.Errorf("cannot clean workspace %s: %s", planID, plan.Reason)
	}
	if plan.Dirty && !options.ForceDirty {
		return plan, fmt.Errorf("cannot clean workspace %s: %s", planID, plan.Reason)
	}
	if !plan.CanRemove && !plan.Dirty && !options.Force {
		return plan, fmt.Errorf("cannot clean workspace %s: %s", planID, plan.Reason)
	}
	if err := m.git.mutation.RemoveWorktree(ctx, plan.Path, plan.Dirty && options.ForceDirty); err != nil {
		return plan, err
	}
	return plan, nil
}

// Managed cleanup decision statuses.
const (
	ManagedStatusClean     = "clean"
	ManagedStatusDirty     = "dirty"
	ManagedStatusUnmerged  = "unmerged"
	ManagedStatusCurrent   = "current"
	ManagedStatusProtected = "protected"
)

// ManagedCleanup describes the cleanup decision for one Tao-managed branch and its
// optional worktree.
type ManagedCleanup struct {
	Branch             string
	WorktreePath       string // empty when the branch has no worktree
	Status             string
	CanRemove          bool
	MergedNonAncestral bool
	Reason             string
}

// ManagedBranchPrefix returns the static prefix of branches Tao creates, derived
// from the branch name template (the text before "{plan_id}"). It returns the whole
// template when there is no placeholder, so callers can detect a missing prefix.
func (m *Manager) ManagedBranchPrefix() string {
	prefix, _, found := strings.Cut(m.config.BranchNameTemplate, "{plan_id}")
	if !found {
		return m.config.BranchNameTemplate
	}
	return prefix
}

// PlanManagedCleanup enumerates Tao-managed branches and decides, for each,
// whether it is safe to remove. Legacy branches are discovered through Tao's
// static branch prefix. Callers may additionally supply exact branch names
// whose ownership is established by durable plan or workspace metadata; those
// names are looked up exactly and never widened into category-prefix scans.
// A branch is removable when it is not protected, not the current branch, its
// worktree (if any) is clean, and its changes are already merged into the
// default branch.
func (m *Manager) PlanManagedCleanup(ctx context.Context, ownedBranches ...string) ([]ManagedCleanup, error) {
	branches, err := m.managedCleanupCandidates(ctx, ownedBranches)
	if err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		return nil, nil
	}

	current, err := m.git.branches.CurrentBranch(ctx)
	if err != nil {
		return nil, err
	}
	defaultBranch, err := m.git.branches.DefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	worktrees, err := m.git.status.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	worktreeByBranch := map[string]string{}
	for _, worktree := range worktrees {
		if worktree.Branch != "" {
			worktreeByBranch[worktree.Branch] = worktree.Path
		}
	}

	plans := make([]ManagedCleanup, 0, len(branches))
	for _, branch := range branches {
		// Integration branches have their own transactional recovery and restart
		// protocol; ordinary plan cleanup must never classify or remove them.
		if strings.HasPrefix(branch, integrationBranchPrefix) {
			continue
		}
		decision, err := m.decideManagedCleanup(ctx, branch, current, defaultBranch, worktreeByBranch[branch])
		if err != nil {
			return nil, err
		}
		plans = append(plans, decision)
	}
	return plans, nil
}

func (m *Manager) managedCleanupCandidates(ctx context.Context, ownedBranches []string) ([]string, error) {
	prefix := m.ManagedBranchPrefix()
	if strings.TrimSpace(prefix) == "" {
		return nil, fmt.Errorf("branch name template %q has no static prefix; cannot scope cleanup safely", m.config.BranchNameTemplate)
	}

	branches, err := m.git.cleanup.ListBranches(ctx, prefix+"*")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(branches)+len(ownedBranches))
	candidates := make([]string, 0, len(branches)+len(ownedBranches))
	add := func(branch string) {
		if branch == "" || strings.HasPrefix(branch, integrationBranchPrefix) {
			return
		}
		if _, ok := seen[branch]; ok {
			return
		}
		seen[branch] = struct{}{}
		candidates = append(candidates, branch)
	}
	for _, branch := range branches {
		add(branch)
	}
	for _, owned := range ownedBranches {
		owned = strings.TrimSpace(owned)
		if owned == "" || strings.HasPrefix(owned, integrationBranchPrefix) {
			continue
		}
		matches, err := m.git.cleanup.ListBranches(ctx, owned)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if match == owned {
				add(match)
			}
		}
	}
	return candidates, nil
}

func (m *Manager) decideManagedCleanup(ctx context.Context, branch string, current string, defaultBranch string, worktreePath string) (ManagedCleanup, error) {
	item := ManagedCleanup{Branch: branch, WorktreePath: worktreePath}
	switch {
	case gitops.ProtectedBranch(branch):
		item.Status = ManagedStatusProtected
		item.Reason = "branch is protected"
		return item, nil
	case branch == current:
		item.Status = ManagedStatusCurrent
		item.Reason = "branch is currently checked out"
		return item, nil
	}

	if worktreePath != "" {
		status, err := m.git.status.WorktreeStatus(ctx, worktreePath)
		if err != nil {
			return ManagedCleanup{}, err
		}
		if status.Dirty {
			item.Status = ManagedStatusDirty
			item.Reason = "worktree has uncommitted changes"
			return item, nil
		}
	}

	mergeMechanism, err := m.git.cleanup.MergedIntoMechanism(ctx, branch, defaultBranch)
	if err != nil {
		return ManagedCleanup{}, err
	}
	if mergeMechanism == gitops.MergeMechanismNone {
		item.Status = ManagedStatusUnmerged
		item.Reason = fmt.Sprintf("not merged into %s", defaultBranch)
		return item, nil
	}

	item.Status = ManagedStatusClean
	item.CanRemove = true
	item.MergedNonAncestral = mergeMechanism == gitops.MergeMechanismSquash
	item.Reason = fmt.Sprintf("merged into %s", defaultBranch)
	return item, nil
}

// CleanManaged removes a Tao-managed branch and its worktree. It removes the
// worktree first because Git refuses to delete a branch checked out in a worktree.
// Protected and current branches are never removed. Unless explicitly allowed by
// the options, only items that PlanManagedCleanup marked removable are acted on.
func (m *Manager) CleanManaged(ctx context.Context, item ManagedCleanup, options CleanOptions) error {
	if item.Status == ManagedStatusProtected || gitops.ProtectedBranch(item.Branch) {
		return fmt.Errorf("refusing to remove protected branch %s", item.Branch)
	}
	if item.Status == ManagedStatusCurrent {
		return fmt.Errorf("refusing to remove current branch %s", item.Branch)
	}
	if !item.CanRemove && !options.Force && !options.AllowNonAncestralBranch {
		return fmt.Errorf("refusing to remove %s: %s", item.Branch, item.Reason)
	}
	if item.WorktreePath != "" {
		status, err := m.git.status.WorktreeStatus(ctx, item.WorktreePath)
		if err != nil {
			return fmt.Errorf("re-check worktree %s before cleanup: %w", item.WorktreePath, err)
		}
		if status.Dirty && !options.Force {
			return fmt.Errorf("refusing to remove %s: worktree %s has uncommitted changes", item.Branch, item.WorktreePath)
		}
		if err := m.git.mutation.RemoveWorktree(ctx, item.WorktreePath, true); err != nil {
			return err
		}
	}
	forceBranch := options.Force || options.AllowNonAncestralBranch || item.MergedNonAncestral
	return m.git.mutation.DeleteBranch(ctx, item.Branch, forceBranch)
}

func worktreeRemoveActions(path string, force bool) []string {
	command := "git worktree remove " + path
	if force {
		command = "git worktree remove --force " + path
	}
	return []string{command}
}
