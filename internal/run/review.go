package run

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

type reviewProposalRepairError struct {
	category string
	cause    error
}

func (e *reviewProposalRepairError) Error() string {
	return fmt.Sprintf("review proposal repair failed: %v", e.cause)
}

func (e *reviewProposalRepairError) Unwrap() error { return e.cause }

type reviewGit interface {
	StatusPorcelain(context.Context) (string, error)
	RevParse(context.Context, string) (string, error)
	CurrentBranch(context.Context) (string, error)
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
			if err := plan.RequireNotAbandoned(detail); err != nil {
				return err
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
		if err := plan.RequireNotAbandoned(detail); err != nil {
			return err
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
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	reviewAttempted := completedRunReviewAttempted(detail)
	pullRequestCreated := completedRunPullRequestCreated(detail)

	// finalizeCompletedRun owns the ordinary cleanliness gate, summary, PR, and workspace
	// completion sequence. Disable only phases already durably completed so a
	// restart neither reruns an LLM review nor recreates a recorded PR.
	f.execution.Config.ReviewEnabled = f.execution.Config.ReviewEnabled && !reviewAttempted
	f.execution.Config.PullRequest = f.execution.Config.PullRequest && !pullRequestCreated
	return f.finalizeCompletedRun(ctx, 1, detail)
}

// ensureApprovedReviewProposal validates the exact durable approval before any
// push or forge operation. Historical approvals with an unusable proposal get
// one proposal-only correction session; substantive review is never rerun.
func (f Finalizer) ensureApprovedReviewProposal(ctx context.Context, detail *plan.PlanDetail, repoRoot, branch, headSHA string) error {
	review := plan.CurrentReview(detail)
	if review == nil {
		return f.failPullRequestFinalization(detail, branch, headSHA, "review_not_approved", fmt.Errorf("pull request preflight requires a current approved review"))
	}
	reviewBase := strings.TrimSpace(review.Base)
	if reviewBase == "" {
		// Legacy approvals predate persisted live review bases. Their immutable
		// plan/workspace base remains the compatibility boundary.
		reviewBase = reviewDetailBase(detail)
	}
	evidenceReview := *review
	evidenceReview.Base = reviewBase
	// A fresh unusable approval is deliberately projected as a comment while its
	// correction attempt is in flight. Recognize that exact consumed marker before
	// the approval gate so interruption keeps one recovery action without replacing
	// the stronger proposal-repair evidence.
	if failure := matchingProposalRepairFailure(detail, reviewBase, review.Head); failure != nil {
		err := consumedProposalCorrectionError(detail.State.Plan.ID, reviewBase, review.Head, failure)
		recoveryAction := plan.ProposalRepairRecoveryAction(failure.Category)
		if failure.RecoveryAction == recoveryAction {
			return err
		}
		return f.failProposalRepairWithAction(detail, evidenceReview, failure.Category, recoveryAction, err)
	}
	if !review.IsApproved() {
		return f.failPullRequestFinalization(detail, branch, headSHA, "review_not_approved", fmt.Errorf("pull request preflight requires a current approved review"))
	}
	if reviewBase == "" || strings.TrimSpace(review.Head) != strings.TrimSpace(headSHA) {
		err := fmt.Errorf("pull request preflight requires an exact approved review for head %q; recorded review is %q..%q", headSHA, reviewBase, review.Head)
		return f.failPullRequestFinalization(detail, branch, headSHA, "review_head_mismatch", err)
	}
	if _, _, err := pullRequestPreflight(PullRequestRun{Detail: detail, Branch: branch, HeadSHA: headSHA}); err == nil {
		return nil
	}
	if category, err := f.inspectCleanPullRequestWorktree(ctx, repoRoot, "proposal repair"); err != nil {
		return f.failPullRequestFinalization(detail, branch, headSHA, category, err)
	}

	session, ok := f.reviewer().(AgentSessionExecutor)
	if !ok {
		return f.failProposalRepair(detail, evidenceReview, "proposal_correction_unavailable", fmt.Errorf("approved review commit proposal is unusable and the proposal-only correction session is unavailable"))
	}
	prompt, err := renderReviewPrompt(reviewPromptData{
		PlanID: detail.State.Plan.ID, Base: reviewBase, Head: review.Head,
		ChangeType: detail.State.Plan.ChangeType, ProposalOnly: true,
	})
	if err != nil {
		return f.failProposalRepair(detail, evidenceReview, "proposal_prompt_failed", err)
	}
	record, err := planMutationRecord(f.execution, detail)
	if err != nil {
		return fmt.Errorf("record consumed proposal correction attempt: %w", err)
	}
	repairRecorder, ok := record.(ReviewProposalCorrectionRecorder)
	if !ok {
		return fmt.Errorf("approved review commit proposal is unusable and the plan record cannot durably consume a proposal-only correction attempt")
	}
	consumedAttempt := plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhaseProposalRepair, Category: "proposal_correction_started",
		ReviewBase: reviewBase, ReviewHead: strings.TrimSpace(review.Head),
		FailedAt: now(f.execution).UTC(), RecoveryAction: plan.FinalizationRecoveryRerunReview,
	}
	// A previous pre-correction cleanliness failure is obsolete only after the
	// live cleanliness check above succeeds. Compare and supersede that exact
	// evidence in the same mutation that consumes the one correction attempt;
	// clearing it first would leave an interruption window for a second attempt.
	repairedWorkspaceFailure := matchingPreCorrectionWorkspaceFailure(detail, branch, headSHA)
	if err := repairRecorder.ConsumeReviewProposalCorrection(repairedWorkspaceFailure, consumedAttempt); err != nil {
		return fmt.Errorf("record consumed proposal correction attempt: %w", err)
	}

	result, sessionErr := session.RunAgentSession(ctx, AgentSessionRequest{
		PlanDir: absolutePlanDir(detail.Dir), RepoRoot: repoRoot,
		LogAction: "correcting review proposal for plan " + detail.State.Plan.ID,
		Prompt:    prompt, CaptureOutput: true,
	})
	if category, err := f.reinspectProposalCorrectionWorktree(ctx, detail, repoRoot, branch, headSHA); err != nil {
		return f.failProposalRepairWithAction(detail, evidenceReview, category, plan.ProposalRepairRecoveryAction(category), err)
	}
	if sessionErr != nil {
		return f.failProposalRepair(detail, evidenceReview, "proposal_correction_failed", sessionErr)
	}
	proposal := reviewcontract.ParseCommitProposal(result.Output, detail.State.Plan.ChangeType)
	if proposal == nil {
		return f.failProposalRepair(detail, evidenceReview, "proposal_invalid", fmt.Errorf("proposal-only correction did not return a valid typed commit proposal"))
	}
	corrected := evidenceReview
	corrected.CommitMessage = proposal
	agent := corrected.Agent
	if strings.TrimSpace(agent) == "" {
		agent = f.execution.Config.Agent.String()
	}
	if err := repairRecorder.RecordReviewProposalCorrection(consumedAttempt, corrected, agent); err != nil {
		return f.failProposalRepair(detail, evidenceReview, "proposal_recording_failed", err)
	}
	if _, _, err := pullRequestPreflight(PullRequestRun{Detail: detail, Branch: branch, HeadSHA: headSHA}); err != nil {
		return f.failProposalRepair(detail, corrected, "proposal_invalid", err)
	}
	return nil
}

// reinspectProposalCorrectionWorktree ensures the untrusted correction session
// did not alter the exact linked-worktree boundary that was reviewed. This must
// run before the corrected approval is persisted or any remote operation starts.
func (f Finalizer) reinspectProposalCorrectionWorktree(ctx context.Context, detail *plan.PlanDetail, repoRoot, branch, headSHA string) (string, error) {
	fallbackBranch, fallbackHead := recordedWorkspaceBoundary(detail)
	if reason := interruptedWorktreeIdentityError(detail, repoRoot); reason != "" {
		return "workspace_mismatch", fmt.Errorf("reinspect pull request correction worktree: %s", reason)
	}
	if err := inspectLinkedWorktreeIdentity(ctx, detail, repoRoot, f.execution.Dependencies.CommandRunner); err != nil {
		return pullRequestWorkspaceFailureCategory(err), fmt.Errorf("reinspect pull request correction worktree ownership: %w", err)
	}
	liveBranch, liveHead, err := currentBranchHead(ctx, f.execution, repoRoot)
	if err != nil {
		return "workspace_preflight_failed", err
	}
	status, err := gitClient(f.execution, repoRoot).StatusPorcelain(ctx)
	if err != nil {
		return "workspace_preflight_failed", fmt.Errorf("reinspect pull request correction worktree status: %w", err)
	}
	if fallbackBranch == "" || fallbackHead == "" || liveBranch != branch || liveHead != headSHA || liveBranch != fallbackBranch || liveHead != fallbackHead {
		return "head_drift", fmt.Errorf("proposal correction changed the reviewed worktree boundary: recorded branch %q HEAD %s; captured branch %q HEAD %s; live branch %q HEAD %s", fallbackBranch, diagnosticSHA(fallbackHead), branch, diagnosticSHA(headSHA), liveBranch, diagnosticSHA(liveHead))
	}
	if strings.TrimSpace(status) != "" {
		return "workspace_dirty", fmt.Errorf("proposal correction left the reviewed worktree dirty; refusing pull request finalization")
	}
	if intent := detail.State.Plan.PullRequestIntent; intent != nil && (liveBranch != intent.Branch || liveHead != intent.HeadSHA) {
		return "intent_mismatch", fmt.Errorf("proposal correction worktree branch %q HEAD %s does not match recorded intent branch %q HEAD %s", liveBranch, diagnosticSHA(liveHead), intent.Branch, diagnosticSHA(intent.HeadSHA))
	}
	return "", nil
}

func consumedProposalCorrectionError(planID, reviewBase, reviewHead string, failure *plan.FinalizationFailure) error {
	prefix := fmt.Sprintf("proposal-only correction was already attempted for exact review range %q..%q", reviewBase, reviewHead)
	switch plan.ProposalRepairRecoveryAction(failure.Category) {
	case plan.FinalizationRecoveryRestoreBoundary:
		if failure.Category == "workspace_mismatch" {
			return fmt.Errorf("%s; repair or restore the recorded linked worktree before recording a fresh review", prefix)
		}
		if failure.Category == "workspace_dirty" || failure.Category == "workspace_preflight_failed" {
			return fmt.Errorf("%s; restore a clean worktree at the recorded branch and HEAD before recording a fresh review", prefix)
		}
		return fmt.Errorf("%s; restore the worktree to its recorded branch and HEAD before recording a fresh review", prefix)
	case plan.FinalizationRecoveryRepairIntent:
		return fmt.Errorf("%s; repair the conflicting durable pull-request intent before recording a fresh review", prefix)
	default:
		return fmt.Errorf("%s; run `tao review --run %s` to record a replacement substantive review", prefix, planID)
	}
}

func matchingProposalRepairFailure(detail *plan.PlanDetail, reviewBase, reviewHead string) *plan.FinalizationFailure {
	if detail == nil {
		return nil
	}
	failure := detail.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhaseProposalRepair {
		return nil
	}
	if failure.ReviewBase != strings.TrimSpace(reviewBase) || failure.ReviewHead != strings.TrimSpace(reviewHead) {
		return nil
	}
	return failure
}

func matchingPreCorrectionWorkspaceFailure(detail *plan.PlanDetail, branch, headSHA string) *plan.FinalizationFailure {
	if detail == nil {
		return nil
	}
	failure := detail.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhasePullRequest {
		return nil
	}
	if failure.Category != "workspace_dirty" && failure.Category != "workspace_preflight_failed" {
		return nil
	}
	if failure.Branch != strings.TrimSpace(branch) || failure.HeadSHA != strings.TrimSpace(headSHA) {
		return nil
	}
	copy := *failure
	return &copy
}

func (f Finalizer) failProposalRepair(detail *plan.PlanDetail, review plan.PlanReview, category string, localErr error) error {
	return f.failProposalRepairWithAction(detail, review, category, plan.FinalizationRecoveryRerunReview, localErr)
}

func (f Finalizer) failProposalRepairWithAction(detail *plan.PlanDetail, review plan.PlanReview, category, recoveryAction string, localErr error) error {
	failure := plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhaseProposalRepair, Category: category,
		ReviewBase: strings.TrimSpace(review.Base), ReviewHead: strings.TrimSpace(review.Head),
		FailedAt: now(f.execution).UTC(), RecoveryAction: recoveryAction,
	}
	if failure.ReviewBase == "" || failure.ReviewHead == "" {
		return localErr
	}
	if err := f.recordFinalizationFailure(detail, failure); err != nil {
		return fmt.Errorf("%w; record proposal repair failure: %w", localErr, err)
	}
	return localErr
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
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return execution, err
	}
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
	PlanDir      string
	PlanID       string
	Base         string
	Head         string
	ChangeType   plan.ChangeType
	ProposalOnly bool
}

type extractedReview = reviewcontract.Review

const (
	maxReviewBudgetWarnings    = 20
	maxReviewContextBytes      = 8 * 1024
	maxReviewStopReasonBytes   = 512
	maxReviewWarningScopeBytes = 256
)

func renderReviewPrompt(data reviewPromptData) (string, error) {
	return prompts.Render(prompts.PromptReview, prompts.Data{
		PlanDir: data.PlanDir, PlanID: data.PlanID, Base: data.Base, Head: data.Head,
		ChangeType: string(data.ChangeType), ProposalOnly: data.ProposalOnly,
	})
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
	changeType := state.Plan.ChangeType
	prompt, err := renderReviewPrompt(reviewPromptData{PlanDir: planDir, PlanID: planID, Base: base, Head: head, ChangeType: changeType})
	if err != nil {
		return plan.PlanReview{}, err
	}
	prompt = appendPriorReworkAndBudgetContext(prompt, detail, runtimeconfig.RuntimeAgentBudgetThresholds())
	result, err := executor.RunAgentSession(ctx, AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, LogAction: "reviewing plan " + planID, Prompt: prompt, CaptureOutput: true})
	if err != nil {
		return plan.PlanReview{}, err
	}
	extracted := extractTypedReview(result.Output, changeType)
	reviewedAt := now(options).UTC()
	review := plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: extracted.Verdict, Summary: extracted.Summary, FindingsCount: extracted.FindingsCount, Findings: extracted.Findings, CommitMessage: extracted.CommitMessage, Base: base, Head: head, Agent: options.Agent, ReviewedAt: reviewedAt}
	record, err := reviewPlanRecord(recordFactory, planDir, detail)
	if err != nil {
		return plan.PlanReview{}, err
	}
	persistedReview := review
	if extracted.Verdict == plan.ReviewVerdictApprove && !extracted.ProposalUsable {
		// The raw artifact retains the substantive exact-range approval, but the
		// lifecycle projection must remain a non-approval until Tao can atomically
		// persist a centrally validated proposal with it. Otherwise interruption or
		// correction failure would leave an approval that ordinary merge accepts.
		persistedReview.Verdict = plan.ReviewVerdictComment
		persistedReview.CommitMessage = nil
	}
	if err := record.RecordReviewCompletedWithArtifact(persistedReview, options.Agent, result.Output); err != nil {
		return plan.PlanReview{}, err
	}
	if extracted.Verdict != plan.ReviewVerdictApprove || extracted.ProposalUsable {
		return persistedReview, nil
	}

	correctionPrompt, err := renderReviewPrompt(reviewPromptData{PlanID: planID, Base: base, Head: head, ChangeType: changeType, ProposalOnly: true})
	if err != nil {
		return persistedReview, &reviewProposalRepairError{category: "proposal_prompt_failed", cause: err}
	}
	repairRecorder, ok := record.(ReviewProposalCorrectionRecorder)
	if !ok {
		return persistedReview, &reviewProposalRepairError{category: "proposal_correction_unavailable", cause: fmt.Errorf("plan record cannot durably consume a proposal-only correction attempt")}
	}
	consumedAttempt := plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhaseProposalRepair, Category: "proposal_correction_started",
		ReviewBase: strings.TrimSpace(base), ReviewHead: strings.TrimSpace(head),
		FailedAt: now(options).UTC(), RecoveryAction: plan.FinalizationRecoveryRerunReview,
	}
	if err := repairRecorder.ConsumeReviewProposalCorrection(nil, consumedAttempt); err != nil {
		return persistedReview, fmt.Errorf("record consumed proposal correction attempt: %w", err)
	}

	correctionResult, correctionErr := executor.RunAgentSession(ctx, AgentSessionRequest{
		PlanDir: planDir, RepoRoot: repoRoot, LogAction: "correcting review proposal for plan " + planID,
		Prompt: correctionPrompt, CaptureOutput: true,
	})
	if category, inspectErr := reinspectFreshReviewProposalCorrectionWorktree(ctx, options, detail, repoRoot, git, head); inspectErr != nil {
		return persistedReview, settleFreshReviewProposalCorrectionFailure(record, consumedAttempt, category, now(options).UTC(), inspectErr)
	}
	if correctionErr != nil {
		return persistedReview, settleFreshReviewProposalCorrectionFailure(record, consumedAttempt, "proposal_correction_failed", now(options).UTC(), correctionErr)
	}
	proposal := reviewcontract.ParseCommitProposal(correctionResult.Output, changeType)
	if proposal == nil {
		return persistedReview, settleFreshReviewProposalCorrectionFailure(record, consumedAttempt, "proposal_invalid", now(options).UTC(), fmt.Errorf("proposal-only correction did not return a valid typed commit proposal"))
	}
	corrected := review
	corrected.CommitMessage = proposal
	if err := repairRecorder.RecordReviewProposalCorrection(consumedAttempt, corrected, options.Agent); err != nil {
		return persistedReview, settleFreshReviewProposalCorrectionFailure(record, consumedAttempt, "proposal_recording_failed", now(options).UTC(), err)
	}
	return corrected, nil
}

// reinspectFreshReviewProposalCorrectionWorktree classifies any mutation made
// by the untrusted correction session before its error or output is handled.
// The captured review head and durable workspace/intent records remain the only
// authority for choosing a recovery action.
func reinspectFreshReviewProposalCorrectionWorktree(ctx context.Context, options agentOperationOptions, detail *plan.PlanDetail, repoRoot string, git reviewGit, reviewedHead string) (string, error) {
	if detail != nil && recordedAutomaticWorktree(detail.State.Workspace) {
		if reason := interruptedWorktreeIdentityError(detail, repoRoot); reason != "" {
			return "workspace_mismatch", fmt.Errorf("reinspect review proposal correction worktree: %s", reason)
		}
		if err := inspectLinkedWorktreeIdentity(ctx, detail, repoRoot, options.CommandRunner); err != nil {
			return pullRequestWorkspaceFailureCategory(err), fmt.Errorf("reinspect review proposal correction worktree ownership: %w", err)
		}
	}

	liveBranch, err := git.CurrentBranch(ctx)
	if err != nil {
		return "workspace_preflight_failed", fmt.Errorf("reinspect review proposal correction worktree branch: %w", err)
	}
	liveBranch = strings.TrimSpace(liveBranch)
	if liveBranch == "" {
		return "workspace_preflight_failed", fmt.Errorf("reinspect review proposal correction worktree branch: git branch --show-current returned empty branch")
	}
	liveHead, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return "workspace_preflight_failed", fmt.Errorf("reinspect review proposal correction worktree HEAD: %w", err)
	}
	liveHead = strings.TrimSpace(liveHead)
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return "workspace_preflight_failed", fmt.Errorf("reinspect review proposal correction worktree status: %w", err)
	}

	recordedBranch, recordedHead := recordedWorkspaceBoundary(detail)
	capturedBranch := strings.TrimSpace(options.StartingBranch)
	if capturedBranch == "" {
		capturedBranch = recordedBranch
	}
	if liveHead != strings.TrimSpace(reviewedHead) || (recordedHead != "" && liveHead != recordedHead) || (capturedBranch != "" && liveBranch != capturedBranch) || (recordedBranch != "" && liveBranch != recordedBranch) {
		return "head_drift", fmt.Errorf("proposal correction changed the reviewed worktree boundary: recorded branch %q HEAD %s; captured branch %q HEAD %s; live branch %q HEAD %s", recordedBranch, diagnosticSHA(recordedHead), capturedBranch, diagnosticSHA(reviewedHead), liveBranch, diagnosticSHA(liveHead))
	}
	if strings.TrimSpace(status) != "" {
		return "workspace_dirty", fmt.Errorf("proposal correction left the reviewed worktree dirty; refusing to persist the corrected review")
	}
	if intent := detail.State.Plan.PullRequestIntent; intent != nil && (liveBranch != strings.TrimSpace(intent.Branch) || liveHead != strings.TrimSpace(intent.HeadSHA)) {
		return "intent_mismatch", fmt.Errorf("proposal correction worktree branch %q HEAD %s does not match recorded intent branch %q HEAD %s", liveBranch, diagnosticSHA(liveHead), intent.Branch, diagnosticSHA(intent.HeadSHA))
	}
	return "", nil
}

// settleFreshReviewProposalCorrectionFailure compare-and-swaps the consumed
// marker to its terminal failure. The substantive approval remains in the raw
// review artifact while the lifecycle projection stays a safe non-approval, so
// failure cannot authorize integration or a second correction session.
func settleFreshReviewProposalCorrectionFailure(record PlanMutationRecord, consumed plan.FinalizationFailure, category string, failedAt time.Time, cause error) error {
	replacer, ok := record.(FinalizationFailureReplacer)
	if !ok {
		return &reviewProposalRepairError{category: category, cause: fmt.Errorf("%w; plan record cannot settle the consumed proposal correction attempt", cause)}
	}
	replacement := consumed
	replacement.Category = category
	replacement.FailedAt = failedAt
	replacement.RecoveryAction = plan.ProposalRepairRecoveryAction(category)
	if err := replacer.ReplaceFinalizationFailure(consumed, replacement); err != nil {
		return &reviewProposalRepairError{category: category, cause: fmt.Errorf("%w; settle consumed proposal correction attempt: %w", cause, err)}
	}
	return &reviewProposalRepairError{category: category, cause: cause}
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
	return extractTypedReview(output, "")
}

func extractTypedReview(output string, changeType plan.ChangeType) extractedReview {
	return reviewcontract.ParseTyped(output, reviewcontract.CommitProposalRequired, changeType)
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
