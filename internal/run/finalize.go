package run

import (
	"context"
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
	if !capabilities.Complete {
		return false, nil
	}
	defer refreshHeader(ctx, detail, f.execution.Config)
	if runCount <= 0 {
		return true, f.writeAlreadyCompleteRun(detail)
	}
	return true, f.finalizeCompletedRun(ctx, runCount, detail)
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
	execution := f.execution
	out := f.outputWriter()
	executionRoot, err := f.executionRoot(ctx, detail)
	if err != nil {
		return err
	}
	if execution.Config.CommitPolicy != CommitPolicyNone {
		if err := requireCleanReviewWorktree(ctx, gitClient(execution, executionRoot), detail, nil); err != nil {
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
		branch, headSHA, err := currentBranchHead(ctx, execution, executionRoot)
		if err != nil {
			return err
		}
		pr, err := f.pullRequestCreator().CreatePullRequest(ctx, PullRequestRun{PlanDir: absolutePlanDir(detail.Dir), PlanID: detail.State.Plan.ID, LogPath: plan.LogPath(detail.Dir), Detail: detail, RepoRoot: executionRoot, Branch: branch, HeadSHA: headSHA})
		if err != nil {
			return fmt.Errorf("create pull request: %w", err)
		}
		record, err := planMutationRecord(execution, detail)
		if err != nil {
			return err
		}
		if err := record.RecordPullRequest(pr, branch, headSHA); err != nil {
			return err
		}
		if err := writef(out, "Pull request: #%d %s\n", pr.Number, pr.URL); err != nil {
			return err
		}
		if plan.PlanIsPullRequestComplete(detail) {
			if err := writef(out, "Plan complete in Tao: %s (approved review and pull request recorded for the same head).\n", detail.State.Plan.ID); err != nil {
				return err
			}
			if err := writef(out, "Next: use the host's Squash and merge action. Tao does not merge the PR. After the merged change is present on your local default branch, optionally run `tao cleanup --dry-run`, then `tao cleanup`.\n"); err != nil {
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

func (f Finalizer) executionRoot(ctx context.Context, detail *plan.PlanDetail) (string, error) {
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
