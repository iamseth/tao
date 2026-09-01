package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type Finalizer struct {
	out           io.Writer
	execution     runExecution
	rootResolver  ExecutionRootResolver
	prCreator     PullRequestCreator
	reviewCreator ReviewCreator
}

type pullRequestIdentityProvenance uint8

const (
	pullRequestIdentityUnknown pullRequestIdentityProvenance = iota
	pullRequestIdentityUnowned
	pullRequestIdentityOwned
)

type provenancePullRequestCreator interface {
	createPullRequestWithProvenance(context.Context, PullRequestRun) (plan.PullRequest, pullRequestIdentityProvenance, error)
}

func newFinalizer(out io.Writer, execution runExecution) Finalizer {
	resolveExecutorDefaults(&execution)
	return Finalizer{
		out:           out,
		execution:     execution,
		rootResolver:  executionRootResolver(execution),
		prCreator:     execution.Dependencies.PullRequestCreator,
		reviewCreator: execution.Dependencies.ReviewCreator,
	}
}

func (f Finalizer) FinalizeIfComplete(ctx context.Context, runCount int, detail *plan.PlanDetail, capabilities plan.RunCapabilities) (bool, error) {
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return false, err
	}
	if !capabilities.Complete {
		return false, nil
	}
	defer refreshHeader(ctx, detail, f.execution.Config)
	if runCount <= 0 {
		if f.execution.Config.Reverify {
			return true, f.reverifyCompletedRun(ctx, detail)
		}
		if f.pullRequestRecoveryEnabled(detail) {
			// A pending intent is an unsettled transaction even when a later
			// substantive review replaced the approval that authorized it. Route
			// it through the ordinary exact-boundary preflight so a non-approval
			// becomes durable, actionable recovery evidence before any push or
			// forge mutation can occur.
			if detail.State.Plan.PullRequestIntent != nil {
				return true, f.resumePullRequestFinalization(ctx, detail)
			}
			if review := plan.CurrentReview(detail); review != nil && review.IsApproved() {
				return true, f.resumePullRequestFinalization(ctx, detail)
			}
			if !completedRunReviewAttempted(detail) {
				if _, _, _, err := f.pullRequestRecoveryBoundary(ctx, detail); err != nil {
					return true, err
				}
				return true, f.resumeCompletedRun(ctx, detail)
			}
		}
		return true, f.writeAlreadyCompleteRun(detail)
	}
	return true, f.finalizeCompletedRun(ctx, runCount, detail)
}

func (f Finalizer) pullRequestRecoveryEnabled(detail *plan.PlanDetail) bool {
	config := f.execution.Config
	if !config.PullRequest || (config.Mode != "" && config.Mode != ModeRun) || config.CommitPolicy != CommitPolicySlice || (config.ExecutionMode != "" && config.ExecutionMode != ExecutionModeIsolated) {
		return false
	}
	return detail != nil && !plan.PlanIsMerged(detail.Events)
}

func (f Finalizer) reverifyCompletedRun(ctx context.Context, detail *plan.PlanDetail) error {
	executionRoot, err := f.executionRoot(ctx, detail)
	if err != nil {
		return fmt.Errorf("reverify completed run: %w", err)
	}
	execution := f.execution
	execution.ExecutionRoot = executionRoot
	if _, err := requireCurrentFailedFinalVerificationBoundary(ctx, detail, execution, "reverification"); err != nil {
		return err
	}
	if execution.Config.CommitPolicy != CommitPolicyNone {
		if err := requireCleanReviewWorktree(ctx, execution.Dependencies.reviewGitFactory(executionRoot), detail, nil); err != nil {
			return fmt.Errorf("reverify completed run: %w", err)
		}
	}
	ReportPhase(ctx, PhaseFinalVerification, nil)
	if err := f.verifyCompletedBranch(ctx, detail, executionRoot); err != nil {
		return fmt.Errorf("reverify completed run: %w", err)
	}
	return nil
}

func (f Finalizer) writeAlreadyCompleteRun(detail *plan.PlanDetail) error {
	out := f.outputWriter()
	if err := writef(out, "Plan slices complete: %s\n", detail.State.Plan.ID); err != nil {
		return err
	}
	if detail.State.Status != plan.StatusInReview {
		return nil
	}
	return writef(out, "Next: run `tao review --run %s` to request a fresh review.\n", detail.State.Plan.ID)
}

func (f Finalizer) finalizeCompletedRun(ctx context.Context, runCount int, detail *plan.PlanDetail) error {
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	execution := f.execution
	out := f.outputWriter()
	executionRoot, err := f.executionRoot(ctx, detail)
	if err != nil {
		return err
	}
	if execution.Config.CommitPolicy != CommitPolicyNone {
		if err := requireCleanReviewWorktree(ctx, execution.Dependencies.reviewGitFactory(executionRoot), detail, nil); err != nil {
			return fmt.Errorf("finalize completed run: %w", err)
		}
	}
	ReportPhase(ctx, PhaseFinalVerification, nil)
	if err := f.verifyCompletedBranch(ctx, detail, executionRoot); err != nil {
		return fmt.Errorf("finalize completed run: %w", err)
	}
	if err := writeSessionSummary(out, detail, now(execution).UTC()); err != nil {
		return err
	}
	if execution.Config.ReviewEnabled {
		ReportPhase(ctx, PhaseReview, nil)
	}
	if err := f.reviewCompletedRun(ctx, runCount, detail, executionRoot); err != nil {
		return err
	}
	if execution.Config.PullRequest {
		// A non-approval is a review outcome, not a failed PR handoff. Leave it
		// for the ordinary (possibly automatic) rework driver; only an approved
		// exact-head review makes pull-request finalization eligible.
		review := plan.CurrentReview(detail)
		if review != nil && review.IsApproved() {
			if err := f.createAndRecordPullRequest(ctx, detail, executionRoot); err != nil {
				return err
			}
		}
	}
	if execution.Config.ExecutionMode == ExecutionModeCurrent {
		return nil
	}
	if effectiveWorkspaceStrategy(detail, execution.Config) == plan.WorkspaceStrategyWorktree {
		return writef(out, "Worktree run: leaving the plan worktree on its feature branch; control checkout was not changed.\n")
	}
	git := gitClient(execution, executionRoot)
	featureBranch, err := git.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("detect current branch: %w", err)
	}
	defaultBranch, err := git.DefaultBranch(ctx)
	if err != nil {
		return err
	}
	if featureBranch == defaultBranch {
		return nil
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect worktree status before checkout: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("worktree has uncommitted changes after completed run; refusing to checkout default branch %s", defaultBranch)
	}
	if err := git.Checkout(ctx, defaultBranch); err != nil {
		return fmt.Errorf("checkout default branch %s: %w", defaultBranch, err)
	}
	return writef(out, "\nMerge instructions:\n  git merge %s\n", featureBranch)
}

func (f Finalizer) resumePullRequestFinalization(ctx context.Context, detail *plan.PlanDetail) error {
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	if plan.PlanIsPullRequestComplete(detail) && detail.State.Plan.PullRequest != nil {
		return f.writePullRequestCompletion(detail, *detail.State.Plan.PullRequest)
	}

	executionRoot, branch, headSHA, err := f.pullRequestRecoveryBoundary(ctx, detail)
	if err != nil {
		return err
	}
	if err := f.ensureApprovedReviewProposal(ctx, detail, executionRoot, branch, headSHA); err != nil {
		return err
	}
	return f.createAndRecordPullRequestAtHead(ctx, detail, executionRoot, branch, headSHA)
}

func (f Finalizer) pullRequestRecoveryBoundary(ctx context.Context, detail *plan.PlanDetail) (string, string, string, error) {
	executionRoot := workspace.ResolveRecordedWorktree(detail).Path
	fallbackBranch, fallbackHead := recordedWorkspaceBoundary(detail)
	if reason := interruptedWorktreeIdentityError(detail, executionRoot); reason != "" {
		err := fmt.Errorf("resolve pull request recovery worktree: %s", reason)
		return "", "", "", f.failPullRequestFinalization(detail, fallbackBranch, fallbackHead, "workspace_mismatch", err)
	}
	if err := inspectLinkedWorktreeIdentity(ctx, detail, executionRoot, f.execution.Dependencies.CommandRunner); err != nil {
		category := pullRequestWorkspaceFailureCategory(err)
		err = fmt.Errorf("inspect pull request recovery worktree: %w", err)
		return "", "", "", f.failPullRequestFinalization(detail, fallbackBranch, fallbackHead, category, err)
	}
	branch, headSHA, err := currentBranchHead(ctx, f.execution, executionRoot)
	if err != nil {
		return "", "", "", f.failPullRequestFinalization(detail, fallbackBranch, fallbackHead, "workspace_preflight_failed", err)
	}
	if fallbackBranch == "" || fallbackHead == "" || branch != fallbackBranch || headSHA != fallbackHead {
		err := fmt.Errorf("recover pull request: recorded workspace branch %q HEAD %s does not match recovery worktree branch %q HEAD %s", fallbackBranch, diagnosticSHA(fallbackHead), branch, diagnosticSHA(headSHA))
		return "", "", "", f.failPullRequestFinalization(detail, fallbackBranch, fallbackHead, "head_drift", err)
	}
	if intent := detail.State.Plan.PullRequestIntent; intent != nil && (branch != intent.Branch || headSHA != intent.HeadSHA) {
		err := fmt.Errorf("recover pull request: recorded intent branch %q HEAD %s does not match recovery worktree branch %q HEAD %s", intent.Branch, diagnosticSHA(intent.HeadSHA), branch, diagnosticSHA(headSHA))
		return "", "", "", f.failPullRequestFinalization(detail, branch, headSHA, "intent_mismatch", err)
	}
	return executionRoot, branch, headSHA, nil
}

func (f Finalizer) createAndRecordPullRequest(ctx context.Context, detail *plan.PlanDetail, executionRoot string) error {
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	branch, headSHA, err := currentBranchHead(ctx, f.execution, executionRoot)
	if err != nil {
		fallbackBranch, fallbackHead := recordedWorkspaceBoundary(detail)
		return f.failPullRequestFinalization(detail, fallbackBranch, fallbackHead, "workspace_preflight_failed", err)
	}
	if err := f.requireRecordedPullRequestBoundary(detail, branch, headSHA); err != nil {
		return err
	}
	if err := f.ensureApprovedReviewProposal(ctx, detail, executionRoot, branch, headSHA); err != nil {
		return err
	}
	branch, headSHA, err = currentBranchHead(ctx, f.execution, executionRoot)
	if err != nil {
		fallbackBranch, fallbackHead := recordedWorkspaceBoundary(detail)
		return f.failPullRequestFinalization(detail, fallbackBranch, fallbackHead, "workspace_preflight_failed", err)
	}
	if err := f.requireRecordedPullRequestBoundary(detail, branch, headSHA); err != nil {
		return err
	}
	return f.createAndRecordPullRequestAtHead(ctx, detail, executionRoot, branch, headSHA)
}

func (f Finalizer) createAndRecordPullRequestAtHead(ctx context.Context, detail *plan.PlanDetail, executionRoot, branch, headSHA string) error {
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	record, err := planMutationRecord(f.execution, detail)
	if err != nil {
		return fmt.Errorf("bind plan mutation record before pull request creation: %w", err)
	}
	creationIntent := plan.PullRequest{}
	if existing := detail.State.Plan.PullRequestIntent; existing != nil {
		if existing.Branch != branch || existing.HeadSHA != headSHA {
			err := fmt.Errorf("recover pull request intent for #%d: recorded branch and head do not match requested branch and head", existing.Number)
			return f.failPullRequestFinalizationWithRecord(record, detail, branch, headSHA, "intent_mismatch", err)
		}
		creationIntent = *existing
	}
	if err := record.RecordPullRequestIntent(creationIntent, branch, headSHA); err != nil {
		return fmt.Errorf("record pull request creation intent: %w", err)
	}

	run := PullRequestRun{PlanDir: absolutePlanDir(detail.Dir), PlanID: detail.State.Plan.ID, LogPath: plan.LogPath(detail.Dir), Detail: detail, RepoRoot: executionRoot, Branch: branch, HeadSHA: headSHA, mutationRecord: record}
	if category, err := f.inspectCleanPullRequestWorktree(ctx, executionRoot, "pull request creation"); err != nil {
		return f.failPullRequestFinalizationWithRecord(record, detail, branch, headSHA, category, err)
	}
	creator := f.pullRequestCreator()
	provenance := pullRequestIdentityUnknown
	var pr plan.PullRequest
	if provenanceCreator, ok := creator.(provenancePullRequestCreator); ok {
		pr, provenance, err = provenanceCreator.createPullRequestWithProvenance(ctx, run)
	} else {
		pr, err = creator.CreatePullRequest(ctx, run)
	}
	if err != nil {
		category := "pull_request_failed"
		var classified *pullRequestFinalizationError
		if errors.As(err, &classified) {
			category = classified.category
		}
		return f.failPullRequestFinalizationWithRecord(record, detail, branch, headSHA, category, fmt.Errorf("create pull request: %w", err))
	}
	// Only an identity emitted by Tao or already recorded as Tao-owned may
	// authorize identity-based recovery. A branch discovery can be a human PR;
	// recording it directly is safe, but an interrupted recording retains only
	// the branch/head creation intent so the next attempt repeats discovery.
	if provenance == pullRequestIdentityOwned || recordedPullRequestIdentityMatches(detail, pr, branch, headSHA) {
		if err := record.RecordPullRequestIntent(pr, branch, headSHA); err != nil {
			return f.failPullRequestFinalizationWithRecord(record, detail, branch, headSHA, "intent_settlement_failed", err)
		}
	}
	if err := record.RecordPullRequest(pr, branch, headSHA); err != nil {
		return f.failPullRequestFinalizationWithRecord(record, detail, branch, headSHA, "final_recording_failed", err)
	}
	return f.writePullRequestCompletion(detail, pr)
}

func (f Finalizer) inspectCleanPullRequestWorktree(ctx context.Context, executionRoot, action string) (string, error) {
	status, err := gitClient(f.execution, executionRoot).StatusPorcelain(ctx)
	if err != nil {
		return "workspace_preflight_failed", fmt.Errorf("inspect pull request worktree status before %s: %w", action, err)
	}
	if strings.TrimSpace(status) != "" {
		return "workspace_dirty", fmt.Errorf("pull request worktree is dirty before %s; restore a clean worktree at the recorded branch and HEAD", action)
	}
	return "", nil
}

func recordedPullRequestIdentityMatches(detail *plan.PlanDetail, pr plan.PullRequest, branch, headSHA string) bool {
	if detail == nil || detail.State.Plan.PullRequestIntent == nil {
		return false
	}
	intent := *detail.State.Plan.PullRequestIntent
	return pullRequestIntentHasIdentity(intent) && intent.Branch == branch && intent.HeadSHA == headSHA && pullRequestMatchesIntent(pr, intent)
}

func (f Finalizer) writePullRequestCompletion(detail *plan.PlanDetail, pr plan.PullRequest) error {
	out := f.outputWriter()
	if err := writef(out, "Pull request: #%d %s\n", pr.Number, pr.URL); err != nil {
		return err
	}
	if !plan.PlanIsPullRequestComplete(detail) {
		return nil
	}
	if err := writef(out, "Plan complete in Tao: %s (approved review and pull request recorded for the same head).\n", detail.State.Plan.ID); err != nil {
		return err
	}
	return writef(out, "Next: use the host's Squash and merge action. Tao does not merge the PR. After the merged change is present on your local default branch, optionally run `tao cleanup --dry-run`, then `tao cleanup`.\n")
}

func pullRequestWorkspaceFailureCategory(err error) string {
	var structural *structuralWorktreeIdentityError
	if errors.As(err, &structural) {
		return "workspace_mismatch"
	}
	return "workspace_preflight_failed"
}

func recordedWorkspaceBoundary(detail *plan.PlanDetail) (string, string) {
	if detail == nil || detail.State.Workspace == nil {
		return "", ""
	}
	return strings.TrimSpace(detail.State.Workspace.Branch), strings.TrimSpace(detail.State.Workspace.HeadSHA)
}

func (f Finalizer) requireRecordedPullRequestBoundary(detail *plan.PlanDetail, liveBranch, liveHead string) error {
	recordedBranch, recordedHead := recordedWorkspaceBoundary(detail)
	if recordedBranch == "" && recordedHead == "" {
		return nil
	}
	if recordedBranch != "" && recordedHead != "" && liveBranch == recordedBranch && liveHead == recordedHead {
		return nil
	}
	err := fmt.Errorf("pull request finalization: recorded workspace branch %q HEAD %s does not match live branch %q HEAD %s", recordedBranch, diagnosticSHA(recordedHead), liveBranch, diagnosticSHA(liveHead))
	return f.failPullRequestFinalization(detail, recordedBranch, recordedHead, "head_drift", err)
}

func (f Finalizer) failPullRequestFinalization(detail *plan.PlanDetail, branch, headSHA, category string, localErr error) error {
	if detail == nil || strings.TrimSpace(branch) == "" || strings.TrimSpace(headSHA) == "" {
		return localErr
	}
	record, err := planMutationRecord(f.execution, detail)
	if err != nil {
		return fmt.Errorf("%w; record pull request finalization failure: %w", localErr, err)
	}
	return f.failPullRequestFinalizationWithRecord(record, detail, branch, headSHA, category, localErr)
}

func (f Finalizer) failPullRequestFinalizationWithRecord(record PlanMutationRecord, detail *plan.PlanDetail, branch, headSHA, category string, localErr error) error {
	branch, headSHA = strings.TrimSpace(branch), strings.TrimSpace(headSHA)
	if detail == nil || branch == "" || headSHA == "" {
		return localErr
	}
	failure := plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhasePullRequest, Category: category, Branch: branch, HeadSHA: headSHA,
		FailedAt: now(f.execution).UTC(), RecoveryAction: plan.PullRequestFinalizationRecoveryAction(category),
	}
	if err := recordFinalizationFailure(record, detail, failure); err != nil {
		return fmt.Errorf("%w; record pull request finalization failure: %w", localErr, err)
	}
	return localErr
}

func (f Finalizer) recordFinalizationFailure(detail *plan.PlanDetail, failure plan.FinalizationFailure) error {
	record, err := planMutationRecord(f.execution, detail)
	if err != nil {
		return err
	}
	return recordFinalizationFailure(record, detail, failure)
}

func recordFinalizationFailure(record PlanMutationRecord, detail *plan.PlanDetail, failure plan.FinalizationFailure) error {
	recorder, ok := record.(FinalizationFailureRecorder)
	if !ok {
		return nil
	}
	if existing := detail.State.Plan.FinalizationFailure; existing != nil {
		if *existing == failure {
			return recorder.RecordFinalizationFailure(failure)
		}
		if existing.Phase != failure.Phase || existing.Branch != failure.Branch || existing.HeadSHA != failure.HeadSHA || existing.ReviewBase != failure.ReviewBase || existing.ReviewHead != failure.ReviewHead {
			return fmt.Errorf("current finalization failure is bound to different durable evidence")
		}
		replacer, ok := record.(FinalizationFailureReplacer)
		if !ok {
			return fmt.Errorf("plan mutation record does not support atomic finalization failure replacement")
		}
		return replacer.ReplaceFinalizationFailure(*existing, failure)
	}
	return recorder.RecordFinalizationFailure(failure)
}

func (f Finalizer) executionRoot(ctx context.Context, detail *plan.PlanDetail) (string, error) {
	if root := strings.TrimSpace(f.execution.ExecutionRoot); root != "" {
		return root, nil
	}
	resolver := f.rootResolver
	if resolver == nil {
		resolver = executionRootResolver(f.execution)
	}
	return resolver.ResolveExecutionRoot(ctx, detail)
}

func (f Finalizer) pullRequestCreator() PullRequestCreator {
	if f.prCreator != nil {
		return f.prCreator
	}
	return f.execution.Dependencies.PullRequestCreator
}

func (f Finalizer) reviewer() ReviewCreator {
	if f.reviewCreator != nil {
		return f.reviewCreator
	}
	return f.execution.Dependencies.ReviewCreator
}

func (f Finalizer) outputWriter() io.Writer {
	if f.out != nil {
		return f.out
	}
	if f.execution.Dependencies.OutputWriter != nil {
		return f.execution.Dependencies.OutputWriter
	}
	return io.Discard
}

func (f Finalizer) reviewCompletedRun(ctx context.Context, runCount int, detail *plan.PlanDetail, executionRoot string) error {
	if runCount <= 0 || !f.execution.Config.ReviewEnabled {
		return nil
	}
	reviewer := f.reviewer()
	if reviewer == nil {
		f.warnReviewFailure(fmt.Errorf("review creator unavailable"))
		f.recordReviewError(detail, fmt.Errorf("review creator unavailable"))
		return nil
	}
	review, err := reviewer.CreateReview(ctx, ReviewRun{PlanDir: absolutePlanDir(detail.Dir), PlanID: detail.State.Plan.ID, LogPath: plan.LogPath(detail.Dir), Detail: detail, RepoRoot: executionRoot, Base: detail.State.Repo.BaseCommit})
	if err != nil {
		if repairErr, ok := errors.AsType[*reviewProposalRepairError](err); ok {
			// The creator has already persisted the bounded substantive result. Publish
			// that exact range in memory before attaching failure evidence to it.
			plan.SetPersistedReview(detail, review)
			if f.pullRequestRecoveryEnabled(detail) {
				// Fresh-review proposal repair settles a launched correction by
				// compare-and-swapping its consumed marker. Do not overwrite that
				// terminal evidence with a second failure record.
				if failure := matchingProposalRepairFailure(detail, strings.TrimSpace(review.Base), strings.TrimSpace(review.Head)); failure != nil && failure.Category == repairErr.category {
					return err
				}
				return f.failProposalRepairWithAction(detail, review, repairErr.category, "rerun_review", err)
			}
			return writeReviewCompletion(f.outputWriter(), review)
		}
		f.warnReviewFailure(err)
		f.recordReviewError(detail, err)
		return nil
	}
	plan.SetPersistedReview(detail, review)
	return writeReviewCompletion(f.outputWriter(), review)
}

func (f Finalizer) warnReviewFailure(err error) {
	_ = writef(f.reviewWarningWriter(), "Warning: plan review failed; continuing without failing the run: %v\n", err)
}

func (f Finalizer) warnReviewRecording(action string, err error) {
	_ = writef(f.reviewWarningWriter(), "Warning: %s: %v\n", action, err)
}

func (f Finalizer) reviewWarningWriter() io.Writer {
	if f.execution.Dependencies.SessionLogWriter != nil {
		return f.execution.Dependencies.SessionLogWriter
	}
	return f.outputWriter()
}

func (f Finalizer) recordReviewError(detail *plan.PlanDetail, reviewErr error) {
	if detail == nil || reviewErr == nil {
		return
	}
	reviewedAt := now(f.execution).UTC()
	review := plan.PlanReview{Status: plan.ReviewStatusError, Verdict: plan.ReviewStatusError, Summary: fmt.Sprintf("Review failed: %v", reviewErr), Base: detail.State.Repo.BaseCommit, Agent: f.execution.Config.Agent.String(), ReviewedAt: reviewedAt}
	if detail.State.Workspace != nil {
		review.Head = detail.State.Workspace.HeadSHA
	}
	record, err := planMutationRecord(f.execution, detail)
	if err != nil {
		f.warnReviewRecording("record review error metadata", err)
		return
	}
	if err := record.RecordReviewError(review, f.execution.Config.Agent.String()); err != nil {
		f.warnReviewRecording("record review error metadata", err)
	}
}

func writeReviewCompletion(out io.Writer, review plan.PlanReview) error {
	label := review.Verdict
	if label == "" {
		label = review.Status
	}
	if label == "" {
		label = plan.ReviewStatusCompleted
	}
	findingLabel := "findings"
	if review.FindingsCount == 1 {
		findingLabel = "finding"
	}
	return writef(out, "Review: %s (%d %s)\n", label, review.FindingsCount, findingLabel)
}

func currentBranchHead(ctx context.Context, execution runExecution, repoRoot string) (string, string, error) {
	git := gitClient(execution, repoRoot)
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return "", "", fmt.Errorf("detect pull request branch: %w", err)
	}
	if branch == "" {
		return "", "", fmt.Errorf("detect pull request branch: git branch --show-current returned empty branch")
	}
	headSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("detect pull request head: %w", err)
	}
	return branch, headSHA, nil
}

// effectiveWorkspaceStrategy reports the physical workspace strategy for a run.
// The shared workspace resolver owns persisted strategy precedence; for legacy
// plans with no persisted strategy, keep deriving the strategy from execution
// mode so finalization stays independent of filesystem root availability.
func effectiveWorkspaceStrategy(detail *plan.PlanDetail, config ExecutionConfig) string {
	workspaceConfig := workspaceConfigForExecutionMode(config.ExecutionMode)
	if identity, err := workspace.ResolveExecutionRoot(detail, workspaceConfig); err == nil {
		return identity.Strategy
	}
	if detail != nil && detail.State.Workspace != nil && strings.TrimSpace(detail.State.Workspace.Strategy) != "" {
		return detail.State.Workspace.Strategy
	}
	return workspaceStrategyForExecutionMode(config.ExecutionMode)
}

func workspaceConfigForExecutionMode(mode ExecutionMode) workspace.Config {
	config := workspace.DefaultConfig()
	config.Strategy = workspaceStrategyForExecutionMode(mode)
	return config
}

func workspaceStrategyForExecutionMode(mode ExecutionMode) string {
	if mode == ExecutionModeCurrent {
		return plan.WorkspaceStrategyCurrent
	}
	return plan.WorkspaceStrategyWorktree
}

func writeSessionSummary(out io.Writer, detail *plan.PlanDetail, now time.Time) error {
	derived := plan.Derive(detail, now)
	metrics := plan.SummarizeAgentTelemetry(detail).Totals
	if err := writef(out, "Plan slices complete: %s\n", detail.State.Plan.ID); err != nil {
		return err
	}
	if err := writef(out, "Summary: %d/%d slices completed in %s\n", derived.CompletedCount, derived.TotalCount, plan.FormatDuration(derived.Elapsed)); err != nil {
		return err
	}
	if metrics.Attempts == 0 {
		return nil
	}
	return writef(out, "Agent: %d session(s), %d token(s), $%.4f cost\n", metrics.Sessions, metrics.TotalTokens, metrics.Cost)
}

func gitClient(options commandRunnerConfig, repoRoot string) gitops.Client {
	runner := options.commandRunner()
	if runner == nil {
		runner = defaultCommandRunner
	}
	return gitops.NewClient(repoRoot, runner)
}
