package merge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

var ErrCleanupDeclined = errors.New("workspace cleanup declined")

// cleanupStatusMissing marks a cleanup decision where the plan branch is not in
// the managed-cleanup list at all — typically because a prior cleanup already
// removed it. Callers retrying cleanup treat this as already settled.
const cleanupStatusMissing = "missing"

// WorkspaceCleaner is the subset of workspace.Manager used after a successful merge.
type WorkspaceCleaner interface {
	PlanClean(ctx context.Context, planID string) (workspace.CleanPlan, error)
	PlanManagedCleanup(ctx context.Context, ownedBranches ...string) ([]workspace.ManagedCleanup, error)
	CleanManaged(ctx context.Context, item workspace.ManagedCleanup, options workspace.CleanOptions) error
}

// CleanupResult describes the post-merge cleanup decision and action.
type CleanupResult struct {
	Plan    workspace.CleanPlan
	Managed workspace.ManagedCleanup
	Removed bool
}

// CleanupDeclinedError reports a managed-cleanup decision that was not safe to remove.
type CleanupDeclinedError struct {
	PlanID string
	Branch string
	Status string
	Reason string
}

func (e *CleanupDeclinedError) Error() string {
	branch := strings.TrimSpace(e.Branch)
	if branch == "" {
		branch = "unknown branch"
	}
	message := fmt.Sprintf("%s for %s", ErrCleanupDeclined, branch)
	if status := strings.TrimSpace(e.Status); status != "" {
		message += ": " + status
	}
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		message += " (" + reason + ")"
	}
	return message
}

func (e *CleanupDeclinedError) Is(target error) bool {
	return target == ErrCleanupDeclined
}

// Cleanup removes the merged plan branch and worktree through workspace.Manager's
// managed cleanup path. Non-force cleanup only proceeds for a clean managed decision.
func (s Service) Cleanup(ctx context.Context, detail *plan.PlanDetail, options Options) (CleanupResult, error) {
	if detail == nil {
		return CleanupResult{}, fmt.Errorf("merge plan detail is nil")
	}
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return CleanupResult{}, err
	}
	planID := strings.TrimSpace(detail.State.Plan.ID)
	if planID == "" {
		return CleanupResult{}, fmt.Errorf("merge cleanup plan id is missing")
	}
	cleaner, err := s.workspaceCleaner(detail)
	if err != nil {
		return CleanupResult{}, err
	}
	cleanPlan, err := cleaner.PlanClean(ctx, planID)
	if err != nil {
		return CleanupResult{}, fmt.Errorf("plan cleanup decision for %s: %w", planID, err)
	}
	branch, branchErr := resolvePlanBranch(detail)
	if branchErr != nil {
		branch = strings.TrimSpace(cleanPlan.Branch)
		if branch == "" {
			return CleanupResult{Plan: cleanPlan}, branchErr
		}
	}
	managed, ok, err := managedCleanupForBranch(ctx, cleaner, branch)
	if err != nil {
		return CleanupResult{Plan: cleanPlan}, err
	}
	result := CleanupResult{Plan: cleanPlan, Managed: managed}
	if !ok {
		if options.Force {
			return result, nil
		}
		return result, &CleanupDeclinedError{PlanID: planID, Branch: branch, Status: cleanupStatusMissing, Reason: "managed cleanup did not include branch"}
	}
	squashUnmerged := options.allowNonAncestralCleanup && managed.Status == workspace.ManagedStatusUnmerged
	if managed.Status != workspace.ManagedStatusClean || !managed.CanRemove {
		if !options.Force && !squashUnmerged {
			return result, &CleanupDeclinedError{PlanID: planID, Branch: managed.Branch, Status: managed.Status, Reason: managed.Reason}
		}
	}
	// A verified squash intentionally leaves the source branch non-ancestral.
	// Allow only that managed unmerged decision; dirty/current/protected states
	// still require the user's explicit --force.
	cleanOptions := workspace.CleanOptions{Force: options.Force, AllowNonAncestralBranch: squashUnmerged}
	if err := cleaner.CleanManaged(ctx, managed, cleanOptions); err != nil {
		return result, fmt.Errorf("clean merged plan workspace %s: %w", planID, err)
	}
	result.Removed = true
	return result, nil
}

func managedCleanupForBranch(ctx context.Context, cleaner WorkspaceCleaner, branch string) (workspace.ManagedCleanup, bool, error) {
	items, err := cleaner.PlanManagedCleanup(ctx, branch)
	if err != nil {
		return workspace.ManagedCleanup{}, false, fmt.Errorf("plan managed cleanup: %w", err)
	}
	for _, item := range items {
		if item.Branch == branch {
			return item, true, nil
		}
	}
	return workspace.ManagedCleanup{}, false, nil
}

func (s Service) workspaceCleaner(detail *plan.PlanDetail) (WorkspaceCleaner, error) {
	if s.Cleaner != nil {
		return s.Cleaner, nil
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	if repoRoot == "" {
		return nil, fmt.Errorf("merge cleanup repo root is missing")
	}
	manager, err := workspace.NewManager(workspace.Options{RepoRoot: repoRoot, Runner: s.Runner})
	if err != nil {
		return nil, fmt.Errorf("create workspace manager: %w", err)
	}
	return manager, nil
}

// AppendPlanMergedEvent records the verified merge in the plan event log.
func (s Service) AppendPlanMergedEvent(detail *plan.PlanDetail, branch string, mergedDefaultSHA string) error {
	if detail == nil {
		return fmt.Errorf("merge plan detail is nil")
	}
	planID := strings.TrimSpace(detail.State.Plan.ID)
	if planID == "" {
		return fmt.Errorf("plan merged event plan id is missing")
	}
	planDir := strings.TrimSpace(detail.Dir)
	if planDir == "" {
		return fmt.Errorf("plan merged event plan dir is missing")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("plan merged event branch is missing")
	}
	mergedDefaultSHA = strings.TrimSpace(mergedDefaultSHA)
	if mergedDefaultSHA == "" {
		return fmt.Errorf("plan merged event default SHA is missing")
	}
	mergedAt := s.now().UTC()
	// RecordMerged persists the whole state snapshot, and the rebase/verify
	// window between plan load and this point can span minutes. Re-read the full
	// artifact bundle so concurrent updates and a recovered mutation journal's
	// event evidence are not clobbered or duplicated by the stale in-memory
	// copy. An unreadable artifact falls back to the loaded snapshot, which is no
	// worse than writing it unconditionally. The injected-store path is
	// in-memory and single-writer, so its snapshot is already current.
	if s.Events == nil {
		repo := plan.NewFileRepository(filepath.Dir(planDir))
		if reloaded, err := repo.ResolvePlan(context.Background(), planDir); err == nil {
			detail.State = reloaded.State
			detail.Slices = reloaded.Slices
			detail.Events = reloaded.Events
		} else if state, stateErr := plan.ReadState(planDir); stateErr == nil {
			// Preserve compatibility with state-only fixtures and historical
			// directories whose required artifact bundle is incomplete.
			detail.State = state
		}
	}
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	record, err := s.planMergeRecord(planDir, detail)
	if err != nil {
		return fmt.Errorf("record merged plan: %w", err)
	}
	if err := record.RecordMerged(branch, mergedDefaultSHA, mergedAt); err != nil {
		return err
	}
	return nil
}

// planMergeRecord builds the plan record used to persist a recorded merge. When
// a caller injects an Events store, the merge is persisted (state.json +
// events.jsonl) through it so in-memory and on-disk state stay consistent;
// otherwise the default file-backed record is used.
func (s Service) planMergeRecord(planDir string, detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	if s.Events != nil {
		return plan.NewPlanRecordWithStore(s.Events, planDir, detail)
	}
	return plan.NewPlanRecord(planDir, detail)
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
