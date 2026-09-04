package merge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
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

// preparedSquashConflict is returned only to the ordinary single-plan merge
// coordinator. It deliberately leaves Git's conflicted index and worktree at
// the durable default parent so the guarded resolver can inspect them.
type preparedSquashConflict struct {
	files               []string
	status              string
	cause               error
	preexistingPaths    []string
	preexistingBoundary *worktreePathSnapshot
}

func (e *preparedSquashConflict) Error() string {
	return "squash conflict prepared for guarded resolution"
}
func (e *preparedSquashConflict) Unwrap() error { return e.cause }

// IntegrateSquash consumes the durable single-merge intent and either creates
// its exact squash commit or recognizes that exact commit after interruption.
// Its public behavior remains rollback-on-conflict; Merge uses the private
// prepared variant when a guarded resolver is configured.
func (s Service) IntegrateSquash(ctx context.Context, detail *plan.PlanDetail) error {
	return s.integrateSquash(ctx, detail, false)
}

func (s Service) integrateSquash(ctx context.Context, detail *plan.PlanDetail, preserveConflict bool) error {
	if detail == nil {
		return fmt.Errorf("merge plan detail is nil")
	}
	intent := detail.State.Plan.MergeCommitIntent
	if intent == nil {
		return fmt.Errorf("single-plan squash commit intent is missing")
	}
	git, err := s.gitClient()
	if err != nil {
		return err
	}
	planBranch, err := resolvePlanBranch(detail)
	if err != nil {
		return err
	}
	integrated, err := inspectSingleMergeIntent(ctx, git, detail, *intent)
	if err != nil {
		return err
	}
	if integrated {
		return nil
	}
	failure := integrationFailure{
		defaultBranch:       intent.DefaultBranch,
		planBranch:          planBranch,
		preMergeSHA:         intent.DefaultParent,
		resetBeforeCheckout: true,
	}
	var preexistingPaths []string
	var preexistingBoundary *worktreePathSnapshot
	if preserveConflict {
		preexistingPaths, preexistingBoundary, err = snapshotPreexistingWorktreeBoundary(ctx, git)
		if err != nil {
			return fmt.Errorf("snapshot worktree before possible conflict resolution: %w", err)
		}
		defer func() {
			if preexistingBoundary != nil {
				preexistingBoundary.cleanup()
			}
		}()
	}
	if err := git.Checkout(ctx, intent.DefaultBranch); err != nil {
		return fmt.Errorf("checkout default branch %s: %w", intent.DefaultBranch, err)
	}
	if err := git.MergeSquash(ctx, planBranch); err != nil {
		failure.phase = "squash merge"
		failure.cause = err
		files := collectConflictFiles(ctx, git)
		if preserveConflict && len(files) > 0 {
			status, statusErr := git.StatusPorcelain(ctx)
			if statusErr != nil {
				failure.cause = errors.Join(err, fmt.Errorf("inspect prepared squash conflict: %w", statusErr))
				return recoverIntegrationFailure(ctx, git, git, failure)
			}
			prepared := &preparedSquashConflict{
				files: files, status: status, cause: err,
				preexistingPaths: preexistingPaths, preexistingBoundary: preexistingBoundary,
			}
			preexistingBoundary = nil
			return prepared
		}
		return recoverIntegrationFailure(ctx, git, git, failure)
	}
	if err := git.Commit(ctx, intent.Message); err != nil {
		failure.phase = "squash commit"
		failure.cause = err
		return recoverIntegrationFailure(ctx, git, git, failure)
	}
	return nil
}

func inspectSingleMergeIntent(ctx context.Context, git GitClient, detail *plan.PlanDetail, intent plan.SingleMergeCommitIntent) (bool, error) {
	planID := strings.TrimSpace(detail.State.Plan.ID)
	if intent.PlanID != planID {
		return false, fmt.Errorf("single-merge intent plan %q does not match plan %q", intent.PlanID, planID)
	}
	defaultBranch, err := resolveDefaultBranch(ctx, git, detail)
	if err != nil {
		return false, err
	}
	if defaultBranch != intent.DefaultBranch {
		return false, fmt.Errorf("single-merge intent default branch %q does not match live default branch %q", intent.DefaultBranch, defaultBranch)
	}
	planBranch, err := resolvePlanBranch(detail)
	if err != nil {
		return false, err
	}
	sourceHead, err := git.RevParse(ctx, planBranch)
	if err != nil {
		return false, fmt.Errorf("capture source head for %s: %w", planBranch, err)
	}
	if sourceHead = strings.TrimSpace(sourceHead); sourceHead != intent.SourceHead {
		return false, fmt.Errorf("single-merge intent source head %s does not match live source head %s", intent.SourceHead, sourceHead)
	}
	defaultHead, err := git.RevParse(ctx, defaultBranch)
	if err != nil {
		return false, fmt.Errorf("capture default head for %s: %w", defaultBranch, err)
	}
	defaultHead = strings.TrimSpace(defaultHead)
	if defaultHead == intent.DefaultParent {
		return false, nil
	}
	parent, parentErr := git.RevParse(ctx, defaultHead+"^")
	message, messageErr := git.CommitMessage(ctx, defaultHead)
	exactLanded := parentErr == nil && messageErr == nil && strings.TrimSpace(parent) == intent.DefaultParent && strings.TrimSpace(message) == intent.Message
	if exactLanded {
		return true, nil
	}

	advanced, advancedErr := git.IsAncestor(ctx, intent.DefaultParent, defaultHead)
	rewound, rewoundErr := git.IsAncestor(ctx, defaultHead, intent.DefaultParent)
	status, statusErr := git.StatusPorcelain(ctx)
	operation := ""
	if strings.TrimSpace(git.Root()) != "" {
		operation, _ = gitops.ActiveOperation(git.Root())
	}
	classification := ClassifySingleMergeIntentRecovery(intent, SingleMergeIntentLiveState{
		PlanID: detail.State.Plan.ID, DefaultBranch: defaultBranch,
		SourceHead: sourceHead, LiveDefault: defaultHead,
		SourceBranchExists: true, DefaultBranchExists: true,
		DefaultAdvanced: advancedErr == nil && advanced,
		DefaultRewound:  rewoundErr == nil && rewound,
		Dirty:           statusErr != nil || strings.TrimSpace(status) != "",
		ActiveOperation: operation,
		// Once requested evidence is loaded from disk, provider invocation is
		// conservatively ambiguous; only invocation-local code can prove the
		// narrower pre-usable-provider category.
		ProviderWasUsable: intent.Resolution != nil && intent.Resolution.Phase == plan.SingleMergeResolutionPhaseRequested,
	})
	return false, &SingleMergeIntentDriftError{
		PlanID: intent.PlanID, DefaultBranch: defaultBranch,
		DefaultParent: intent.DefaultParent, LiveDefault: defaultHead,
		SourceHead: sourceHead, Phase: classification.Phase,
		Verdict: classification.Verdict, Reason: classification.Reason,
	}
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
	if zeroStatus, ok := git.(porcelainV1ZClient); ok {
		status, err := zeroStatus.StatusPorcelainV1Z(ctx)
		if err != nil {
			return nil
		}
		changes, err := parsePorcelainV1Z(status)
		if err != nil {
			return nil
		}
		return append([]string(nil), changes.unmergedPaths...)
	}

	// Retain the line-oriented fallback for lightweight GitClient test doubles.
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
