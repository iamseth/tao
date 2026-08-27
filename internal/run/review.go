package run

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/reviewcontract"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/workspace"
	"github.com/iamseth/tao/prompts"
)

type ReviewRun struct {
	PlanDir  string
	PlanID   string
	LogPath  string
	Detail   *plan.PlanDetail
	RepoRoot string
	Base     string
	HeadSHA  string
}

type ReviewCreator interface {
	CreateReview(ctx context.Context, run ReviewRun) (plan.PlanReview, error)
}

type reviewGit interface {
	StatusPorcelain(context.Context) (string, error)
	RevParse(context.Context, string) (string, error)
	DefaultBranch(context.Context) (string, error)
	MergeBase(context.Context, string, string) (string, error)
}

type reviewGitFactory func(repoRoot string) reviewGit

var _ reviewGit = gitops.Client{}

func newReviewGitFactory(runner CommandRunner) reviewGitFactory {
	if runner == nil {
		runner = defaultCommandRunner
	}
	return func(repoRoot string) reviewGit {
		return gitops.NewClient(repoRoot, runner)
	}
}

// Review runs a fresh persisted plan review without executing pending slices.
func (s Service) Review(ctx context.Context, request Request) (review plan.PlanReview, err error) {
	config, err := prepareRequestConfig(s.config, request)
	if err != nil {
		return plan.PlanReview{}, err
	}
	lockDetail, err := s.repo.ResolvePlan(ctx, request.Input)
	if err != nil {
		return plan.PlanReview{}, err
	}
	if lockDetail == nil {
		return plan.PlanReview{}, fmt.Errorf("plan %q not found", request.Input)
	}
	startedAt := now(s.dependencies).UTC()
	lockErr := trackRunStatus(ctx, s.dependencies.StatusReporter, lockDetail, startedAt, func(statusCtx context.Context) error {
		return WithPlanRunLock(statusCtx, lockDetail, startedAt, func(ownedCtx context.Context) error {
			// Resolve by the exact directory after acquisition. The pre-lock detail is
			// identity only and may be stale after another lifecycle driver releases.
			detail, err := s.repo.ResolvePlan(ownedCtx, lockDetail.Dir)
			if err != nil {
				return err
			}
			if detail == nil {
				return fmt.Errorf("plan %q not found", lockDetail.Dir)
			}
			if err := plan.RequireSliceWorkSettled(detail); err != nil {
				return err
			}
			ReportPhase(ownedCtx, PhasePreparingExecution, nil)
			if err := writef(s.out, "Preparing review: %s\n", detail.State.Plan.ID); err != nil {
				return err
			}
			execution, err := s.prepareReviewExecution(detail, config)
			if err != nil {
				return err
			}
			if execution.Config.CommitPolicy != CommitPolicyNone {
				if err := requireCleanReviewWorktree(ownedCtx, execution.Dependencies.reviewGitFactory(execution.ExecutionRoot), detail, nil); err != nil {
					return fmt.Errorf("prepare review: %w", err)
				}
			}
			if err := requireNoCurrentFinalVerificationFailure(ownedCtx, detail, execution); err != nil {
				return fmt.Errorf("prepare review: %w", err)
			}
			ReportPhase(ownedCtx, PhaseFinalVerification, nil)
			if err := writef(s.out, "Verifying completed branch: %s\n", execution.ExecutionRoot); err != nil {
				return err
			}
			if err := newFinalizer(s.out, execution).verifyCompletedBranch(ownedCtx, detail, execution.ExecutionRoot); err != nil {
				return fmt.Errorf("prepare review: %w", err)
			}
			ReportPhase(ownedCtx, PhaseReview, nil)
			if err := writef(s.out, "Running agent review: %s\n", execution.Config.Agent); err != nil {
				return err
			}
			review, err = execution.Dependencies.ReviewCreator.CreateReview(ownedCtx, ReviewRun{PlanDir: absolutePlanDir(detail.Dir), PlanID: detail.State.Plan.ID, LogPath: plan.LogPath(detail.Dir), Detail: detail, RepoRoot: execution.ExecutionRoot, Base: reviewDetailBase(detail)})
			return err
		})
	})
	return review, lockErr
}

// ResumeReview completes the remaining finalization phases of an interrupted
// slice-complete run. Agent review failures retain the ordinary run path's
// best-effort warning and error-recording behavior instead of failing the
// recovered queue entry.
func (s Service) ResumeReview(ctx context.Context, request Request) error {
	config, err := prepareRequestConfig(s.config, request)
	if err != nil {
		return err
	}
	lockDetail, err := s.repo.ResolvePlan(ctx, request.Input)
	if err != nil {
		return err
	}
	if lockDetail == nil {
		return fmt.Errorf("plan %q not found", request.Input)
	}
	planDir := lockDetail.Dir
	return withPlanRunLock(ctx, lockDetail, now(s.dependencies).UTC(), func(ownedCtx context.Context) error {
		detail, err := s.repo.ResolvePlan(ownedCtx, planDir)
		if err != nil {
			return err
		}
		if detail == nil {
			return fmt.Errorf("plan %q not found", planDir)
		}
		if err := plan.RequireSliceWorkSettled(detail); err != nil {
			return err
		}
		execution, err := s.prepareReviewExecution(detail, config)
		if err != nil {
			return err
		}
		return newFinalizer(s.out, execution).resumeCompletedRun(ownedCtx, detail)
	})
}

// resumeCompletedRun re-enters normal finalization at the earliest phase not
// proven complete by durable metadata. A persisted review proves the review
// phase completed; a persisted pull request proves all phases through PR
// creation completed.
func (f Finalizer) resumeCompletedRun(ctx context.Context, detail *plan.PlanDetail) error {
	reviewAttempted := completedRunReviewAttempted(detail)
	pullRequestCreated := completedRunPullRequestCreated(detail)

	// finalizeCompletedRun owns the ordinary cleanliness gate, summary, PR, and workspace
	// completion sequence. Disable only phases already durably completed so a
	// restart neither reruns an LLM review nor recreates a recorded PR.
	f.execution.Config.ReviewEnabled = f.execution.Config.ReviewEnabled && !reviewAttempted
	f.execution.Config.PullRequest = f.execution.Config.PullRequest && !pullRequestCreated
	return f.finalizeCompletedRun(ctx, 1, detail)
}

func completedRunReviewAttempted(detail *plan.PlanDetail) bool {
	attempted := plan.CurrentReview(detail) != nil
	if detail == nil {
		return attempted
	}
	for _, event := range detail.Events {
		switch event.Type {
		case plan.EventTypePlanReopened:
			attempted = false
		case plan.EventTypePlanReviewed:
			attempted = true
		}
	}
	return attempted
}

func completedRunPullRequestCreated(detail *plan.PlanDetail) bool {
	if detail == nil {
		return false
	}
	created := detail.State.Plan.PullRequest != nil
	for _, event := range detail.Events {
		switch event.Type {
		case plan.EventTypePlanReopened:
			created = false
		case plan.EventTypePullRequestCreated:
			created = true
		}
	}
	return created
}

func (s Service) prepareReviewExecution(detail *plan.PlanDetail, config ExecutionConfig) (runExecution, error) {
	execution := newRunExecution(config, s.dependencies)
	root, err := reviewExecutionRoot(detail)
	if err != nil {
		return execution, err
	}
	execution.ExecutionRoot = root
	dependencies := &execution.Dependencies
	if dependencies.CommandRunner == nil {
		dependencies.CommandRunner = defaultCommandRunner
	}
	if dependencies.reviewGitFactory == nil {
		dependencies.reviewGitFactory = newReviewGitFactory(dependencies.CommandRunner)
	}
	if dependencies.ProcessStarter == nil {
		dependencies.ProcessStarter = defaultProcessStarter
	}
	if dependencies.EventAppender == nil {
		dependencies.EventAppender = s.repo
	}
	if dependencies.PlanRecordFactory == nil {
		dependencies.PlanRecordFactory = func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return s.repo.PlanRecord(detail)
		}
	}
	if dependencies.LogAppender == nil {
		dependencies.LogAppender = s.repo
	}
	if dependencies.OutputWriter == nil {
		dependencies.OutputWriter = s.out
	}
	// Automatic slice runs and their standalone reviews require a clean tree.
	// Historical starting-dirty tolerances are retained as readable metadata but
	// are no longer applied to an automatic execution.

	// The review gate uses the latest persisted run policy: state.json first,
	// legacy run_context events second. If neither records a valid policy, the
	// legacy fallback is none so old none-policy plans are not locked out of
	// review; merge still enforces a clean tree before shipping.
	policy := reviewCommitPolicy(detail)
	execution.Config.CommitPolicy = policy
	if execution.Config.ExecutionMode == ExecutionModeCurrent {
		execution.StartingBranch = strings.TrimSpace(detail.State.Repo.Branch)
	}
	resolveExecutorDefaults(&execution)
	if execution.Dependencies.ReviewCreator == nil {
		return execution, fmt.Errorf("run dependencies incompletely resolved after setup: ReviewCreator still nil")
	}
	return execution, nil
}

func reviewCommitPolicy(detail *plan.PlanDetail) CommitPolicy {
	if policy := parseRecordedCommitPolicy(detail.State.Plan.LastRunCommitPolicy); policy != "" {
		return policy
	}
	if policy := lastRunCommitPolicy(detail.Events); policy != "" {
		return policy
	}
	return CommitPolicyNone
}

func parseRecordedCommitPolicy(value string) CommitPolicy {
	switch CommitPolicy(strings.TrimSpace(value)) {
	case CommitPolicyPlan, CommitPolicySlice, CommitPolicyNone:
		return CommitPolicy(strings.TrimSpace(value))
	default:
		return ""
	}
}

// lastRunCommitPolicy returns the commit policy recorded by the most recent
// legacy run_context event, or "" when no event recorded a valid policy.
func lastRunCommitPolicy(events []plan.Event) CommitPolicy {
	var policy CommitPolicy
	for _, event := range events {
		if event.Type != plan.EventTypeRunContext {
			continue
		}
		parsed := parseRecordedCommitPolicy(event.CommitPolicy)
		if parsed == "" {
			continue
		}
		policy = parsed
	}
	return policy
}

func reviewExecutionRoot(detail *plan.PlanDetail) (string, error) {
	if detail == nil {
		return "", fmt.Errorf("plan detail is nil")
	}
	identity, err := workspace.ResolveExecutionRoot(detail, reviewWorkspaceConfig(detail))
	if err == nil {
		return absoluteReviewPath(identity.Root)
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	if repoRoot == "" {
		return "", fmt.Errorf("plan %s does not record a repo root", detail.State.Plan.ID)
	}
	return absoluteReviewPath(repoRoot)
}

func reviewWorkspaceConfig(detail *plan.PlanDetail) workspace.Config {
	config := workspaceConfigForExecutionMode(ExecutionModeCurrent)
	if detail != nil && detail.State.Workspace != nil && strings.TrimSpace(detail.State.Workspace.Strategy) == "" && strings.TrimSpace(detail.State.Workspace.Path) != "" {
		config.Strategy = plan.WorkspaceStrategyWorktree
	}
	return config
}

func absoluteReviewPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("review workspace root is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

type reviewPromptData struct {
	PlanDir string
	PlanID  string
	Base    string
	Head    string
}

type extractedReview = reviewcontract.Review

const (
	maxReviewBudgetWarnings    = 20
	maxReviewContextBytes      = 8 * 1024
	maxReviewStopReasonBytes   = 512
	maxReviewWarningScopeBytes = 256
)

func renderReviewPrompt(data reviewPromptData) (string, error) {
	return prompts.Render(prompts.PromptReview, prompts.Data{PlanDir: data.PlanDir, PlanID: data.PlanID, Base: data.Base, Head: data.Head})
}

func appendPriorReworkAndBudgetContext(prompt string, detail *plan.PlanDetail, thresholds plan.AgentBudgetThresholds) string {
	if detail == nil {
		return prompt
	}
	summary := plan.SummarizeRework(detail.Events)
	warnings := plan.AgentBudgetWarnings(detail, thresholds)
	if summary == (plan.ReworkSummary{}) && len(warnings) == 0 {
		return prompt
	}

	var context strings.Builder
	context.WriteString("\n\n## Prior Rework and Budget Context\n\n")
	context.WriteString("The following is advisory history for evaluating the current changes.\n")
	if summary.Rounds > 0 {
		fmt.Fprintf(&context, "- Rework rounds: %d\n", summary.Rounds)
	}
	if summary.LatestStoppedReason != "" {
		context.WriteString("- Latest rework stop: " + boundedReviewContextText(summary.LatestStoppedReason, maxReviewStopReasonBytes) + "\n")
	}
	if summary.DistinctFindingFingerprints > 0 {
		fmt.Fprintf(&context, "- Distinct finding fingerprints: %d\n", summary.DistinctFindingFingerprints)
	}
	warningCount := min(len(warnings), maxReviewBudgetWarnings)
	for _, warning := range warnings[:warningCount] {
		scope := warning.Scope
		if warning.SliceID != "" {
			scope += " " + warning.SliceID
		}
		fmt.Fprintf(&context, "- Budget warning (%s): %s observed %g > threshold %g\n",
			boundedReviewContextText(scope, maxReviewWarningScopeBytes),
			warning.Metric,
			warning.Observed,
			warning.Threshold,
		)
	}
	if omitted := len(warnings) - warningCount; omitted > 0 {
		fmt.Fprintf(&context, "- Additional budget warnings omitted: %d\n", omitted)
	}
	return strings.TrimRight(prompt, "\n") + boundedReviewContextText(context.String(), maxReviewContextBytes)
}

func boundedReviewContextText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "…"
	limit := maxBytes - len(suffix)
	for limit > 0 && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit] + suffix
}

func createReviewWithAgentSession(ctx context.Context, executor AgentSessionExecutor, options agentOperationOptions, run ReviewRun, recordFactory PlanRecordFactory) (plan.PlanReview, error) {
	planDir := reviewPlanDir(run)
	state, err := reviewState(planDir, run.Detail)
	if err != nil {
		return plan.PlanReview{}, err
	}
	planID := reviewPlanID(run, state)
	repoRoot := run.RepoRoot
	if repoRoot == "" {
		repoRoot = state.Repo.Root
	}
	gitFactory := options.reviewGitFactory
	if gitFactory == nil {
		gitFactory = newReviewGitFactory(options.CommandRunner)
	}
	git := gitFactory(repoRoot)
	detail := run.Detail
	if detail == nil {
		detail = &plan.PlanDetail{Dir: planDir, State: state}
	} else {
		detail.State = state
	}
	// A clean committed tree is required only when the commit policy actually
	// commits slice work; under CommitPolicyNone the changes are intentionally
	// left uncommitted, so demanding a clean worktree would stall every review.
	if options.CommitPolicy != CommitPolicyNone {
		if err := requireCleanReviewWorktree(ctx, git, detail, options.StartingDirtyPaths); err != nil {
			return plan.PlanReview{}, err
		}
	}
	base := reviewRunBase(ctx, git, run, state)
	head := run.HeadSHA
	if head == "" {
		head, err = git.RevParse(ctx, "HEAD")
		if err != nil {
			return plan.PlanReview{}, fmt.Errorf("detect review head: %w", err)
		}
	}
	prompt, err := renderReviewPrompt(reviewPromptData{PlanDir: planDir, PlanID: planID, Base: base, Head: head})
	if err != nil {
		return plan.PlanReview{}, err
	}
	prompt = appendPriorReworkAndBudgetContext(prompt, detail, runtimeconfig.RuntimeAgentBudgetThresholds())
	result, err := executor.RunAgentSession(ctx, AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, LogAction: "reviewing plan " + planID, Prompt: prompt, CaptureOutput: true})
	if err != nil {
		return plan.PlanReview{}, err
	}
	extracted := extractReview(result.Output)
	reviewedAt := now(options).UTC()
	review := plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: extracted.Verdict, Summary: extracted.Summary, FindingsCount: extracted.FindingsCount, Findings: extracted.Findings, CommitMessage: extracted.CommitMessage, Base: base, Head: head, Agent: options.Agent, ReviewedAt: reviewedAt}
	record, err := reviewPlanRecord(recordFactory, planDir, detail)
	if err != nil {
		return plan.PlanReview{}, err
	}
	if err := record.RecordReviewCompletedWithArtifact(review, options.Agent, result.Output); err != nil {
		return plan.PlanReview{}, err
	}
	return review, nil
}

// requireCleanReviewWorktree blocks any run-produced uncommitted work before
// review. commitLeftovers shares git-status classification with slice completion:
// .tao metadata is skipped, starting-dirty paths are tolerated, and any ambiguous
// rename or copy outside .tao stays a hard stop.
func requireCleanReviewWorktree(ctx context.Context, git reviewGit, detail *plan.PlanDetail, startingDirty []string) error {
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("check worktree status before review: %w", err)
	}
	leftovers, err := commitLeftovers(detail, status, startingDirtyPredicate(startingDirty))
	if err != nil {
		if ambiguous, ok := errors.AsType[*commitLeftoverAmbiguousStatusError](err); ok {
			return fmt.Errorf("review requires a clean committed tree; uncommitted changes remain:\n%s", strings.Join(ambiguous.Lines, "\n"))
		}
		return fmt.Errorf("check worktree status before review: %w", err)
	}
	if len(leftovers) > 0 {
		return fmt.Errorf("review requires a clean committed tree; uncommitted changes remain:\n%s", strings.Join(leftovers, "\n"))
	}
	return nil
}

func extractReview(output string) extractedReview {
	return reviewcontract.Parse(output, reviewcontract.CommitProposalRequired)
}

func reviewPlanRecord(factory PlanRecordFactory, planDir string, detail *plan.PlanDetail) (PlanMutationRecord, error) {
	if factory != nil {
		record, err := factory(detail)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, fmt.Errorf("plan record is nil")
		}
		return record, nil
	}
	return plan.NewPlanRecord(planDir, detail)
}

func reviewPlanDir(run ReviewRun) string {
	if run.PlanDir != "" {
		return run.PlanDir
	}
	if run.Detail != nil {
		return run.Detail.Dir
	}
	return ""
}

func reviewState(planDir string, detail *plan.PlanDetail) (plan.State, error) {
	if detail != nil {
		return detail.State, nil
	}
	state, err := plan.ReadState(planDir)
	if err != nil {
		return plan.State{}, fmt.Errorf("read review state: %w", err)
	}
	return state, nil
}

func reviewPlanID(run ReviewRun, state plan.State) string {
	if run.PlanID != "" {
		return run.PlanID
	}
	return state.Plan.ID
}

func reviewDetailBase(detail *plan.PlanDetail) string {
	if detail == nil {
		return ""
	}
	if base := reviewWorkspaceBase(detail.State); base != "" {
		return base
	}
	return strings.TrimSpace(detail.State.Repo.BaseCommit)
}

// reviewRunBase prefers the live merge-base so the persisted review matches
// what the merge gate will compute, then falls back to bases recorded at plan
// creation for plans without branch metadata or when git is unavailable.
func reviewRunBase(ctx context.Context, git reviewGit, run ReviewRun, state plan.State) string {
	if base := reviewLiveMergeBase(ctx, git, state); base != "" {
		return base
	}
	if base := reviewWorkspaceBase(state); base != "" {
		return base
	}
	if base := strings.TrimSpace(run.Base); base != "" {
		return base
	}
	return strings.TrimSpace(state.Repo.BaseCommit)
}

// reviewLiveMergeBase computes merge-base(default, plan branch) with the same
// inputs `tao merge` uses for its review-base gate, so a review rerun after a
// manual rebase records a base the merge gate accepts. An empty result means
// the live base is not computable and callers must fall back to recorded bases.
func reviewLiveMergeBase(ctx context.Context, git reviewGit, state plan.State) string {
	if state.Workspace == nil {
		return ""
	}
	branch := strings.TrimSpace(state.Workspace.Branch)
	if branch == "" {
		return ""
	}
	defaultBranch, err := git.DefaultBranch(ctx)
	defaultBranch = strings.TrimSpace(defaultBranch)
	if err != nil || defaultBranch == "" {
		defaultBranch = strings.TrimSpace(state.Workspace.BaseBranch)
	}
	if defaultBranch == "" || defaultBranch == branch {
		return ""
	}
	base, err := git.MergeBase(ctx, defaultBranch, branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(base)
}

func reviewWorkspaceBase(state plan.State) string {
	if state.Workspace == nil {
		return ""
	}
	return strings.TrimSpace(state.Workspace.BaseSHA)
}
