package merge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

type MergeConflictError struct {
	Phase         string
	Files         []string
	Cause         error
	CleanupErrors []error
}

func (e *MergeConflictError) Error() string {
	phase := strings.TrimSpace(e.Phase)
	if phase == "" {
		phase = "integration"
	}
	message := fmt.Sprintf("%s during %s", ErrMergeConflict, phase)
	if len(e.Files) > 0 {
		message += ": " + strings.Join(e.Files, ", ")
	}
	if len(e.CleanupErrors) > 0 {
		parts := make([]string, 0, len(e.CleanupErrors))
		for _, err := range e.CleanupErrors {
			if err != nil {
				parts = append(parts, err.Error())
			}
		}
		if len(parts) > 0 {
			message += "; cleanup failed: " + strings.Join(parts, "; ")
		}
	}
	return message
}

func (e *MergeConflictError) Is(target error) bool {
	return target == ErrMergeConflict
}

func (e *MergeConflictError) Unwrap() error {
	return e.Cause
}

// IntegrateSquash applies the reviewed plan branch to default and creates one
// deterministic integration commit without rewriting the plan branch.
func (s Service) IntegrateSquash(ctx context.Context, detail *plan.PlanDetail) error {
	if detail == nil {
		return fmt.Errorf("merge plan detail is nil")
	}
	git, err := s.gitClient()
	if err != nil {
		return err
	}
	defaultBranch, err := resolveDefaultBranch(ctx, git, detail)
	if err != nil {
		return err
	}
	planBranch, err := resolvePlanBranch(detail)
	if err != nil {
		return err
	}
	preMergeSHA, err := git.RevParse(ctx, defaultBranch)
	if err != nil {
		return fmt.Errorf("capture pre-merge SHA for %s: %w", defaultBranch, err)
	}
	preMergeSHA = strings.TrimSpace(preMergeSHA)
	if preMergeSHA == "" {
		return fmt.Errorf("capture pre-merge SHA for %s: empty revision", defaultBranch)
	}
	sourceHead, err := git.RevParse(ctx, planBranch)
	if err != nil {
		return fmt.Errorf("capture source head for %s: %w", planBranch, err)
	}
	sourceHead = strings.TrimSpace(sourceHead)
	if sourceHead == "" {
		return fmt.Errorf("capture source head for %s: empty revision", planBranch)
	}
	failure := integrationFailure{
		defaultBranch:       defaultBranch,
		planBranch:          planBranch,
		preMergeSHA:         preMergeSHA,
		resetBeforeCheckout: true,
	}
	if err := git.Checkout(ctx, defaultBranch); err != nil {
		return fmt.Errorf("checkout default branch %s: %w", defaultBranch, err)
	}
	if err := git.MergeSquash(ctx, planBranch); err != nil {
		failure.phase = "squash merge"
		failure.cause = err
		return recoverIntegrationFailure(ctx, git, git, failure)
	}
	if err := git.Commit(ctx, squashCommitMessage(detail, sourceHead)); err != nil {
		failure.phase = "squash commit"
		failure.cause = err
		return recoverIntegrationFailure(ctx, git, git, failure)
	}
	return nil
}

func squashCommitMessage(detail *plan.PlanDetail, sourceHead string) string {
	title := strings.TrimSpace(detail.State.Plan.Title)
	if title == "" {
		title = "Tao plan " + strings.TrimSpace(detail.State.Plan.ID)
	}
	return fmt.Sprintf("%s\n\nTao-Plan: %s\nTao-Source-Head: %s", title, strings.TrimSpace(detail.State.Plan.ID), strings.TrimSpace(sourceHead))
}

// Integrate preserves plan-branch commits by rebasing and fast-forwarding.
func (s Service) Integrate(ctx context.Context, detail *plan.PlanDetail) error {
	if detail == nil {
		return fmt.Errorf("merge plan detail is nil")
	}
	git, err := s.gitClient()
	if err != nil {
		return err
	}
	defaultBranch, err := resolveDefaultBranch(ctx, git, detail)
	if err != nil {
		return err
	}
	planBranch, err := resolvePlanBranch(detail)
	if err != nil {
		return err
	}
	preMergeSHA, err := git.RevParse(ctx, defaultBranch)
	if err != nil {
		return fmt.Errorf("capture pre-merge SHA for %s: %w", defaultBranch, err)
	}
	preMergeSHA = strings.TrimSpace(preMergeSHA)
	if preMergeSHA == "" {
		return fmt.Errorf("capture pre-merge SHA for %s: empty revision", defaultBranch)
	}
	containsDefault, err := git.IsAncestor(ctx, defaultBranch, planBranch)
	if err != nil {
		return fmt.Errorf("check whether %s contains %s: %w", planBranch, defaultBranch, err)
	}
	failure := integrationFailure{defaultBranch: defaultBranch, planBranch: planBranch, preMergeSHA: preMergeSHA}
	if containsDefault {
		if err := git.Checkout(ctx, defaultBranch); err != nil {
			return fmt.Errorf("checkout default branch %s: %w", defaultBranch, err)
		}
		if err := git.MergeFFOnly(ctx, planBranch); err != nil {
			failure.phase = "fast-forward merge"
			failure.cause = err
			return recoverIntegrationFailure(ctx, git, git, failure)
		}
		return nil
	}
	worktreeGit, err := s.worktreeGit(detail)
	if err != nil {
		return err
	}
	// In a separate plan worktree the plan branch is already checked out, so we
	// rebase it in place. When worktreeGit falls back to the repo-root client
	// (current/no-worktree plans), the plan branch is NOT checked out there, so
	// check it out first or we would rebase whatever branch is currently checked
	// out (typically default).
	if !s.usesPlanWorktreeClient(detail) {
		if err := worktreeGit.Checkout(ctx, planBranch); err != nil {
			return fmt.Errorf("checkout plan branch %s for rebase: %w", planBranch, err)
		}
	}
	if err := worktreeGit.Rebase(ctx, defaultBranch); err != nil {
		failure.phase = "rebase"
		failure.rebasing = true
		failure.cause = err
		return recoverIntegrationFailure(ctx, worktreeGit, git, failure)
	}
	if err := git.Checkout(ctx, defaultBranch); err != nil {
		return fmt.Errorf("checkout default branch %s after rebase: %w", defaultBranch, err)
	}
	if err := git.MergeFFOnly(ctx, planBranch); err != nil {
		failure.phase = "fast-forward merge"
		failure.cause = err
		return recoverIntegrationFailure(ctx, git, git, failure)
	}
	return nil
}

type integrationFailure struct {
	phase               string
	defaultBranch       string
	planBranch          string
	preMergeSHA         string
	rebasing            bool
	resetBeforeCheckout bool
	cause               error
}

func recoverIntegrationFailure(ctx context.Context, conflictGit GitClient, restoreGit GitClient, failure integrationFailure) error {
	cleanupErrs := make([]error, 0)
	files := collectConflictFiles(ctx, conflictGit)
	if failure.rebasing {
		if err := conflictGit.RebaseAbort(ctx); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("abort rebase: %w", err))
		}
	}
	if failure.resetBeforeCheckout {
		// A failed squash leaves unresolved index entries on the already-current
		// default branch. Clear them before checkout, which Git otherwise rejects.
		if err := restoreGit.ResetHard(ctx, failure.preMergeSHA); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("reset %s to %s: %w", failure.defaultBranch, failure.preMergeSHA, err))
		}
		if err := restoreGit.Checkout(ctx, failure.defaultBranch); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("checkout default branch %s: %w", failure.defaultBranch, err))
		}
	} else if err := restoreGit.Checkout(ctx, failure.defaultBranch); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("checkout default branch %s: %w", failure.defaultBranch, err))
	} else if err := restoreGit.ResetHard(ctx, failure.preMergeSHA); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("reset %s to %s: %w", failure.defaultBranch, failure.preMergeSHA, err))
	} else if err := restoreGit.Checkout(ctx, failure.defaultBranch); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("re-checkout default branch %s: %w", failure.defaultBranch, err))
	}
	return &MergeConflictError{Phase: failure.phase, Files: files, Cause: failure.cause, CleanupErrors: cleanupErrs}
}

// collectConflictFiles returns only the paths left unmerged by the failed
// merge or rebase; auto-merged staged files are deliberately excluded.
func collectConflictFiles(ctx context.Context, git GitClient) []string {
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	for line := range strings.SplitSeq(status, "\n") {
		if len(line) < 4 || !unmergedStatusCode(line[:2]) {
			continue
		}
		if path := strings.TrimSpace(line[3:]); path != "" {
			seen[path] = struct{}{}
		}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

func unmergedStatusCode(code string) bool {
	return strings.Contains(code, "U") || code == "AA" || code == "DD"
}
