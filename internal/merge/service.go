package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	commitpkg "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/workspace"
)

// NewService constructs a Service with both the repo-root git client and the
// worktree client factory derived from repoRoot and runner. Optional
// collaborators (Cleaner, Events, Logf, Now) may be set on the returned
// Service before use.
func NewService(repoRoot string, runner commandrunner.Runner) Service {
	return Service{
		Git: gitops.NewClient(repoRoot, runner),
		NewGit: func(dir string) GitClient {
			return gitops.NewClient(dir, runner)
		},
	}
}

var (
	ErrNotApproved                = errors.New("plan is not approved for merge")
	ErrReviewBaseMismatch         = errors.New("review base does not match merge base")
	ErrReviewHeadMismatch         = errors.New("review head does not match plan branch tip")
	ErrDirtyWorktree              = errors.New("worktree has uncommitted changes")
	ErrMergeConflict              = errors.New("merge conflict")
	ErrVerifyFailed               = errors.New("merge verification failed")
	ErrSingleResolutionRolledBack = errors.New("single-plan conflict resolution was rolled back; update the source and refresh its review before merging again")
)

type Options struct {
	Force         bool
	NoVerify      bool
	VerifyCommand string
	RecordOnly    bool
	NoSquash      bool

	allowNonAncestralCleanup bool
}

type GitClient interface {
	Root() string
	DefaultBranch(ctx context.Context) (string, error)
	CurrentBranch(ctx context.Context) (string, error)
	RevParse(ctx context.Context, rev string) (string, error)
	CommitMessage(ctx context.Context, rev string) (string, error)
	CommitPathStates(ctx context.Context, parent, commit string) ([]gitops.CommitPathState, error)
	MergeBase(ctx context.Context, a string, b string) (string, error)
	IsAncestor(ctx context.Context, ancestor string, descendant string) (bool, error)
	StatusPorcelain(ctx context.Context) (string, error)
	StatusPorcelainIgnoredV1Z(ctx context.Context) (string, error)
	ChangedFiles(ctx context.Context, revspec string) ([]string, error)
	ChangedFilesExact(ctx context.Context, revspec string) ([]string, error)
	Diff(ctx context.Context, revspec string) (string, error)
	DiffBounded(ctx context.Context, revspec string, maxBytes int) (string, bool, error)
	DiffStat(ctx context.Context, revspec string) (string, error)
	DirtyFingerprint(ctx context.Context) (gitops.DirtyFingerprint, error)
	Checkout(ctx context.Context, branch string) error
	MergeFFOnly(ctx context.Context, ref string) error
	MergeSquash(ctx context.Context, ref string) error
	HasStagedChanges(ctx context.Context) (bool, error)
	CleanUntracked(ctx context.Context) error
	Add(ctx context.Context, paths ...string) error
	Commit(ctx context.Context, message string) error
	CommitWithoutHooks(ctx context.Context, message string) error
	UpdateRefCAS(ctx context.Context, ref, newSHA, oldSHA string) error
	Rebase(ctx context.Context, onto string) error
	RebaseAbort(ctx context.Context) error
	ResetHard(ctx context.Context, ref string) error
}

var _ GitClient = gitops.Client{}

type Service struct {
	Git               GitClient
	NewGit            func(dir string) GitClient
	Runner            commandrunner.Runner
	Cleaner           WorkspaceCleaner
	Events            plan.ArtifactStore
	Logf              func(format string, args ...any)
	Now               func() time.Time
	ProposalGenerator commitpkg.MergeProposalGenerator
	SingleResolver    SingleConflictResolutionService
	SingleReviewer    SingleIntegrationReviewer
}

func mergeVerifyMayNeedSnapshot(options Options) bool {
	if options.NoVerify {
		return false
	}
	if options.VerifyCommand != "" {
		return strings.TrimSpace(options.VerifyCommand) != ""
	}
	envCommand, envSet := runtimeconfig.RuntimeMergeVerifyCommand()
	if envSet {
		return strings.TrimSpace(envCommand) != ""
	}
	return true
}

func (s Service) Merge(ctx context.Context, detail *plan.PlanDetail, options Options) error {
	if detail == nil {
		return fmt.Errorf("merge plan detail is nil")
	}
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	intent := detail.State.Plan.MergeCommitIntent
	activeResolution := intent != nil && intent.Resolution != nil && intent.Resolution.Phase != plan.SingleMergeResolutionPhaseRolledBack
	if activeResolution && options.NoSquash {
		return fmt.Errorf("%w: --no-squash cannot be used while non-rolled-back single-plan conflict-resolution evidence exists", ErrSingleResolutionRejected)
	}
	git, err := s.gitClient()
	if err != nil {
		return err
	}
	// Resolution evidence is classified before the ordinary clean-worktree gate:
	// requested/resolved phases legitimately own a dirty default worktree, while
	// committed/reviewed phases own one exact Tao commit. Never mistake either
	// for unrelated user dirt on an interrupted rerun.
	if activeResolution {
		return s.resumeSingleResolutionMerge(ctx, git, detail, options)
	}
	if recorded, err := s.tryRecordExternalMerge(ctx, git, detail, options); recorded || err != nil {
		return err
	}
	if options.RecordOnly {
		return fmt.Errorf("record external merge: plan is not already merged into default; use --force only if you intentionally record a squash/cherry-pick/manual merge without ancestry proof")
	}
	if err := s.CheckPreMergeGate(ctx, detail, options); err != nil {
		return err
	}
	planBranch, err := resolvePlanBranch(detail)
	if err != nil {
		return err
	}
	if mergeVerifyMayNeedSnapshot(options) {
		if _, err := mergeVerifyRepoRoot(detail); err != nil {
			return err
		}
	}
	var mergeSnapshot mergeVerifySnapshot
	if options.NoSquash {
		mergeSnapshot, err = captureMergeVerifySnapshot(ctx, git, detail)
	} else {
		var intent *plan.SingleMergeCommitIntent
		intent, err = s.prepareSingleMergeIntentForMerge(ctx, git, detail, options.Force)
		if err == nil {
			mergeSnapshot = mergeVerifySnapshot{defaultBranch: intent.DefaultBranch, preMergeSHA: intent.DefaultParent}
		}
	}
	if err != nil {
		return err
	}
	if options.NoSquash {
		if err = s.Integrate(ctx, detail); err != nil {
			return err
		}
		return s.finishIntegratedMerge(ctx, git, detail, planBranch, mergeSnapshot, options)
	}

	err = s.integrateSquash(ctx, detail, s.SingleResolver != nil)
	if err == nil {
		return s.finishIntegratedMerge(ctx, git, detail, planBranch, mergeSnapshot, options)
	}
	var prepared *preparedSquashConflict
	if !errors.As(err, &prepared) {
		return err
	}
	if prepared.preexistingBoundary != nil {
		defer prepared.preexistingBoundary.cleanup()
		cause := fmt.Errorf("%w: automatic conflict resolution refuses pre-existing non-ignored worktree changes: %s", ErrSingleResolutionRejected, strings.Join(prepared.preexistingPaths, ", "))
		return errors.Join(cause, restoreSingleResolutionBoundary(ctx, git, *detail.State.Plan.MergeCommitIntent, *prepared.preexistingBoundary))
	}
	return s.resolveAndFinishSingleMerge(ctx, git, detail, options, prepared)
}

func (s Service) resumeSingleResolutionMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, options Options) error {
	if options.RecordOnly {
		return errors.New("record-only merge cannot resume a single-plan conflict-resolution transaction")
	}
	intent := detail.State.Plan.MergeCommitIntent
	if intent != nil && intent.Resolution != nil && (intent.Resolution.Phase == plan.SingleMergeResolutionPhaseCommitted || intent.Resolution.Phase == plan.SingleMergeResolutionPhaseReviewed) {
		planBranch, err := resolvePlanBranch(detail)
		if err != nil {
			return err
		}
		if inspectSingleResolutionRollbackBoundary(ctx, git, *intent, planBranch) == nil {
			return s.rollbackAndSettleSingleResolution(ctx, git, detail, *intent, planBranch, plan.SingleMergeResolutionRollbackRecoveredInterruption, ErrSingleResolutionRolledBack)
		}
	}
	if s.SingleResolver == nil {
		return errors.New("single-plan conflict-resolution evidence exists, but no guarded resolver is configured")
	}
	return s.resolveAndFinishSingleMerge(ctx, git, detail, options, nil)
}

func (s Service) resolveAndFinishSingleMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, options Options, prepared *preparedSquashConflict) error {
	intent := detail.State.Plan.MergeCommitIntent
	if intent == nil {
		return errors.New("single-plan resolution requires durable merge intent")
	}
	planBranch, err := resolvePlanBranch(detail)
	if err != nil {
		return err
	}
	if prepared == nil && intent.Resolution != nil {
		if err := classifySingleResolutionState(ctx, git, *intent, planBranch); err != nil {
			return err
		}
	}
	verify, err := resolveMergeVerifyCommandForDetail(detail, options)
	if err != nil {
		return rollbackPreparedSingleMerge(ctx, git, *intent, err)
	}
	request := SingleResolutionRequest{
		Intent: *intent, SourceBranch: planBranch, IntegrationRoot: git.Root(),
		PlanTitle: detail.State.Plan.Title, SourceReview: detail.Review.Content,
		VerifyCommand: verify.command,
	}
	sourceBase, err := git.MergeBase(ctx, intent.DefaultParent, intent.SourceHead)
	if err != nil {
		return rollbackPreparedSingleMerge(ctx, git, *intent, fmt.Errorf("read conflict-resolution source merge base: %w", err))
	}
	sourceBase = strings.TrimSpace(sourceBase)
	if sourceBase == "" {
		return rollbackPreparedSingleMerge(ctx, git, *intent, errors.New("conflict-resolution source merge base is empty"))
	}
	request.ChangedFiles, err = git.ChangedFilesExact(ctx, sourceBase+".."+intent.SourceHead)
	if err != nil {
		return rollbackPreparedSingleMerge(ctx, git, *intent, fmt.Errorf("read conflict-resolution source paths: %w", err))
	}
	if prepared != nil {
		request.ConflictFiles = append([]string(nil), prepared.files...)
		request.ConflictStatus = prepared.status
	} else if intent.Resolution != nil {
		request.ConflictFiles = append([]string(nil), intent.Resolution.ConflictFiles...)
		request.ConflictStatus, err = git.StatusPorcelain(ctx)
		if err != nil {
			return fmt.Errorf("classify durable conflict-resolution worktree: %w", err)
		}
	}

	resolved, err := s.SingleResolver.ResolveConflict(ctx, request)
	if err != nil {
		if errors.Is(err, ErrSingleResolutionPreflight) {
			return rollbackPreparedSingleMerge(ctx, git, *intent, err)
		}
		return err
	}
	request.Intent = resolved.Intent
	settled, err := s.SingleResolver.SettleResolved(ctx, request)
	if err != nil {
		return err
	}
	if settled.Intent.Resolution == nil || settled.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseCommitted && settled.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseReviewed {
		return rollbackPreparedSingleMerge(ctx, git, settled.Intent, errors.New("guarded resolver did not settle an exact integration commit"))
	}
	if settled.Head != settled.Intent.Resolution.IntegrationHead {
		return rollbackPreparedSingleMerge(ctx, git, settled.Intent, errors.New("guarded resolver settlement head does not match durable evidence"))
	}
	return s.finishResolvedSingleMerge(ctx, git, detail, planBranch, settled.Intent, verify, options)
}

func (s Service) finishResolvedSingleMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, planBranch string, intent plan.SingleMergeCommitIntent, verify mergeVerifyCommandResolution, options Options) error {
	snapshot := mergeVerifySnapshot{defaultBranch: intent.DefaultBranch, preMergeSHA: intent.DefaultParent}
	verificationEvidence, err := s.verifyIntegratedMerge(ctx, detail, snapshot, verify)
	if err != nil {
		return s.rollbackAndSettleSingleResolution(ctx, git, detail, intent, planBranch, plan.SingleMergeResolutionRollbackVerificationFailed, err)
	}
	if s.SingleReviewer == nil {
		return s.rollbackAndSettleSingleResolution(ctx, git, detail, intent, planBranch, plan.SingleMergeResolutionRollbackReviewUnavailable, errors.New("resolved squash requires a separately configured independent reviewer"))
	}
	result, err := s.SingleReviewer.ReviewResolvedIntegration(ctx, SingleReviewRequest{
		Intent: intent, SourceBranch: planBranch, IntegrationRoot: git.Root(),
		PlanTitle: detail.State.Plan.Title, SourceReview: detail.Review.Content,
		VerifyCommand: verify.command, VerificationHead: intent.Resolution.IntegrationHead,
		VerificationEvidence: verificationEvidence,
	})
	if err != nil {
		reason := plan.SingleMergeResolutionRollbackReviewUnavailable
		if errors.Is(err, ErrSingleReviewNotApproved) {
			reason = plan.SingleMergeResolutionRollbackReviewNotApproved
		}
		return s.rollbackAndSettleSingleResolution(ctx, git, detail, result.Intent, planBranch, reason, err)
	}
	if !result.Authorized || result.Intent.Resolution == nil || result.Intent.Resolution.Review == nil || !result.Intent.Resolution.Review.IsApproved() {
		return s.rollbackAndSettleSingleResolution(ctx, git, detail, result.Intent, planBranch, plan.SingleMergeResolutionRollbackReviewNotApproved, ErrSingleReviewNotApproved)
	}
	// Keep the caller's projection current even when an injected recorder owns
	// persistence; RecordMerged independently reloads and validates authority.
	detail.State.Plan.MergeCommitIntent = &result.Intent
	recorded, err := s.recordIntegratedMerge(ctx, git, detail, planBranch, snapshot, options)
	if err != nil && !recorded {
		return s.rollbackAndSettleSingleResolution(ctx, git, detail, result.Intent, planBranch, plan.SingleMergeResolutionRollbackMergeRecordingFailed, err)
	}
	return err
}

func (s Service) finishIntegratedMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, planBranch string, snapshot mergeVerifySnapshot, options Options) error {
	verify, err := resolveMergeVerifyCommandForDetail(detail, options)
	if err != nil {
		return rollbackIntegratedMerge(ctx, git, snapshot, err)
	}
	if _, err := s.verifyIntegratedMerge(ctx, detail, snapshot, verify); err != nil {
		return err
	}
	recorded, err := s.recordIntegratedMerge(ctx, git, detail, planBranch, snapshot, options)
	if err != nil && !recorded {
		return rollbackIntegratedMerge(ctx, git, snapshot, err)
	}
	return err
}

func (s Service) verifyIntegratedMerge(ctx context.Context, detail *plan.PlanDetail, snapshot mergeVerifySnapshot, verify mergeVerifyCommandResolution) (string, error) {
	if verify.command != "" {
		output, err := s.runMergeVerifyCapture(ctx, detail, snapshot, verify.command)
		if err != nil {
			s.appendMergeVerificationEvent(detail, plan.Event{Command: verify.command, Result: "failed", Message: "Merge verification failed; the merge was rolled back"})
			return output, err
		}
		s.appendMergeVerificationEvent(detail, plan.Event{Command: verify.command, Result: "passed", Message: "Merge verification passed"})
		if strings.TrimSpace(output) == "" {
			return "passed with no output", nil
		}
		return output, nil
	}
	reason := mergeVerifySkippedReason(verify)
	s.logMergeVerifySkipped(verify)
	s.appendMergeVerificationEvent(detail, plan.Event{Result: "skipped", Reason: reason, Message: "Merge verification skipped"})
	return "skipped: " + reason, nil
}

func (s Service) recordIntegratedMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, planBranch string, snapshot mergeVerifySnapshot, options Options) (bool, error) {
	mergedDefaultSHA, err := captureMergedDefaultSHA(ctx, git, snapshot.defaultBranch)
	if err != nil {
		return false, err
	}
	if err := s.AppendPlanMergedEvent(detail, planBranch, mergedDefaultSHA); err != nil {
		return false, err
	}
	options.allowNonAncestralCleanup = !options.NoSquash
	if _, err := s.Cleanup(ctx, detail, options); err != nil && !cleanupAlreadySettled(err) {
		return true, fmt.Errorf("plan %s merged and recorded, but cleanup failed: %w", detail.State.Plan.ID, err)
	}
	return true, nil
}

func classifySingleResolutionState(ctx context.Context, git GitClient, intent plan.SingleMergeCommitIntent, sourceBranch string) error {
	if err := intent.Validate(); err != nil {
		return fmt.Errorf("classify single-plan resolution evidence: %w", err)
	}
	liveSource, sourceErr := git.RevParse(ctx, "refs/heads/"+strings.TrimSpace(sourceBranch))
	liveDefault, defaultErr := git.RevParse(ctx, "refs/heads/"+intent.DefaultBranch)
	head, headErr := git.RevParse(ctx, "HEAD")
	branch, branchErr := git.CurrentBranch(ctx)
	if sourceErr != nil || defaultErr != nil || headErr != nil || branchErr != nil {
		return fmt.Errorf("classify single-plan resolution refs: %w", errors.Join(sourceErr, defaultErr, headErr, branchErr))
	}
	if strings.TrimSpace(liveSource) != intent.SourceHead {
		return fmt.Errorf("%w: source ref changed", ErrSingleResolutionDrift)
	}
	if strings.TrimSpace(branch) != intent.DefaultBranch || strings.TrimSpace(head) != strings.TrimSpace(liveDefault) {
		return fmt.Errorf("%w: integration checkout is not the exact default ref", ErrSingleResolutionDrift)
	}
	phase := intent.Resolution.Phase
	if phase == plan.SingleMergeResolutionPhaseRequested {
		if strings.TrimSpace(head) != intent.DefaultParent {
			return fmt.Errorf("%w: pre-commit resolution no longer has the durable parent checked out", ErrSingleResolutionDrift)
		}
		return nil
	}
	if phase == plan.SingleMergeResolutionPhaseResolved {
		if strings.TrimSpace(head) == intent.DefaultParent {
			return nil
		}
		exact, inspectErr := inspectExactResolutionCommit(ctx, git, intent, strings.TrimSpace(head))
		if inspectErr != nil {
			return fmt.Errorf("%w: inspect possible resolution commit: %w", ErrSingleResolutionDrift, inspectErr)
		}
		if !exact {
			return fmt.Errorf("%w: resolved HEAD is neither the durable parent nor exact resolution commit", ErrSingleResolutionDrift)
		}
		status, err := git.StatusPorcelain(ctx)
		if err != nil {
			return fmt.Errorf("classify exact resolved commit worktree: %w", err)
		}
		if strings.TrimSpace(status) != "" {
			return &DirtyWorktreeError{Status: status}
		}
		return nil
	}
	if strings.TrimSpace(head) == intent.DefaultParent {
		return fmt.Errorf("%w: committed resolution was already rolled back and cannot be re-authorized automatically", ErrSingleResolutionDrift)
	}
	if strings.TrimSpace(head) != intent.Resolution.IntegrationHead {
		return fmt.Errorf("%w: default ref does not match the durable integration head", ErrSingleResolutionDrift)
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("classify committed resolution worktree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return &DirtyWorktreeError{Status: status}
	}
	return nil
}

func rollbackPreparedSingleMerge(ctx context.Context, git GitClient, intent plan.SingleMergeCommitIntent, cause error) error {
	return rollbackIntegratedMerge(ctx, git, mergeVerifySnapshot{defaultBranch: intent.DefaultBranch, preMergeSHA: intent.DefaultParent}, cause)
}

// rollbackSingleResolutionCommit refuses to overwrite ambiguous or unrelated
// state. Only a clean exact integration head (or its already-restored parent)
// may be moved back to the durable parent.
func rollbackSingleResolutionCommit(ctx context.Context, git GitClient, intent plan.SingleMergeCommitIntent, sourceBranch string, cause error) (bool, error) {
	if intent.Resolution == nil || strings.TrimSpace(intent.Resolution.IntegrationHead) == "" {
		return false, errors.Join(cause, errors.New("automatic rollback refused: committed resolution evidence is incomplete"))
	}
	liveSource, sourceErr := git.RevParse(ctx, "refs/heads/"+strings.TrimSpace(sourceBranch))
	liveDefault, defaultErr := git.RevParse(ctx, "refs/heads/"+intent.DefaultBranch)
	status, statusErr := git.StatusPorcelain(ctx)
	branch, branchErr := git.CurrentBranch(ctx)
	if sourceErr != nil || defaultErr != nil || statusErr != nil || branchErr != nil || strings.TrimSpace(liveSource) != intent.SourceHead || (strings.TrimSpace(liveDefault) != intent.Resolution.IntegrationHead && strings.TrimSpace(liveDefault) != intent.DefaultParent) || strings.TrimSpace(status) != "" || strings.TrimSpace(branch) != intent.DefaultBranch {
		return false, errors.Join(cause, fmt.Errorf("automatic rollback refused because Git no longer matches the exact resolved transaction: %w", errors.Join(sourceErr, defaultErr, statusErr, branchErr)))
	}
	if strings.TrimSpace(liveDefault) == intent.DefaultParent {
		return true, cause
	}
	if err := git.ResetHard(ctx, intent.DefaultParent); err != nil {
		return false, errors.Join(cause, fmt.Errorf("automatic rollback reset %s to %s: %w", intent.DefaultBranch, intent.DefaultParent, err))
	}
	if err := git.Checkout(ctx, intent.DefaultBranch); err != nil {
		return false, errors.Join(cause, fmt.Errorf("automatic rollback re-checkout default branch %s: %w", intent.DefaultBranch, err))
	}
	return true, cause
}

func (s Service) rollbackAndSettleSingleResolution(ctx context.Context, git GitClient, detail *plan.PlanDetail, intent plan.SingleMergeCommitIntent, sourceBranch string, reason plan.SingleMergeResolutionRollbackReason, cause error) error {
	cleanupCtx, cancelCleanup := singleAgentCleanupContext(ctx)
	defer cancelCleanup()
	rolledBack, rollbackErr := rollbackSingleResolutionCommit(cleanupCtx, git, intent, sourceBranch, cause)
	if !rolledBack {
		return rollbackErr
	}
	if err := inspectSingleResolutionRollbackBoundary(cleanupCtx, git, intent, sourceBranch); err != nil {
		return errors.Join(rollbackErr, fmt.Errorf("settle single-plan resolution rollback: %w", err))
	}
	settled := *intent.Resolution
	settled.Phase = plan.SingleMergeResolutionPhaseRolledBack
	settled.RollbackReason = reason
	settled.RolledBackAt = s.now().UTC()
	if settled.RolledBackAt.Before(settled.CommittedAt) {
		settled.RolledBackAt = settled.CommittedAt
	}
	record, err := s.planMergeRecord(detail.Dir, detail)
	if err == nil {
		err = record.SettleSingleMergeResolutionRollback(intent, settled)
	}
	if err != nil {
		return errors.Join(rollbackErr, fmt.Errorf("persist single-plan resolution rollback settlement: %w", err))
	}
	return rollbackErr
}

func inspectSingleResolutionRollbackBoundary(ctx context.Context, git GitClient, intent plan.SingleMergeCommitIntent, sourceBranch string) error {
	liveSource, sourceErr := git.RevParse(ctx, "refs/heads/"+strings.TrimSpace(sourceBranch))
	liveDefault, defaultErr := git.RevParse(ctx, "refs/heads/"+intent.DefaultBranch)
	head, headErr := git.RevParse(ctx, "HEAD")
	status, statusErr := git.StatusPorcelain(ctx)
	branch, branchErr := git.CurrentBranch(ctx)
	if sourceErr != nil || defaultErr != nil || headErr != nil || statusErr != nil || branchErr != nil {
		return fmt.Errorf("inspect restored refs and worktree: %w", errors.Join(sourceErr, defaultErr, headErr, statusErr, branchErr))
	}
	if strings.TrimSpace(liveSource) != intent.SourceHead || strings.TrimSpace(liveDefault) != intent.DefaultParent || strings.TrimSpace(head) != intent.DefaultParent || strings.TrimSpace(status) != "" || strings.TrimSpace(branch) != intent.DefaultBranch {
		return ErrSingleResolutionDrift
	}
	return nil
}

func (s Service) prepareSingleMergeIntent(ctx context.Context, git GitClient, detail *plan.PlanDetail) (*plan.SingleMergeCommitIntent, error) {
	return s.prepareSingleMergeIntentForMerge(ctx, git, detail, false)
}

func (s Service) prepareSingleMergeIntentForMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, force bool) (*plan.SingleMergeCommitIntent, error) {
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return nil, err
	}
	planID := strings.TrimSpace(detail.State.Plan.ID)
	planBranch, err := resolvePlanBranch(detail)
	if err != nil {
		return nil, err
	}
	sourceHead, err := git.RevParse(ctx, planBranch)
	if err != nil {
		return nil, fmt.Errorf("capture source head for %s: %w", planBranch, err)
	}
	sourceHead = strings.TrimSpace(sourceHead)
	defaultBranch, err := resolveDefaultBranch(ctx, git, detail)
	if err != nil {
		return nil, err
	}
	defaultParent, err := git.RevParse(ctx, defaultBranch)
	if err != nil {
		return nil, fmt.Errorf("capture single-merge default parent for %s: %w", defaultBranch, err)
	}
	defaultParent = strings.TrimSpace(defaultParent)
	if sourceHead == "" || defaultParent == "" {
		return nil, fmt.Errorf("single-merge intent requires non-empty source and default revisions")
	}

	var superseded *plan.SingleMergeCommitIntent
	if existing := detail.State.Plan.MergeCommitIntent; existing != nil {
		if existing.SourceHead == sourceHead {
			if existing.PlanID != planID || existing.DefaultBranch != defaultBranch {
				return nil, fmt.Errorf("plan %s has a conflicting single-merge commit intent", planID)
			}
			if _, inspectErr := inspectSingleMergeIntent(ctx, git, detail, *existing); inspectErr != nil {
				return nil, inspectErr
			}
			return existing, nil
		}
		// A fresh source may supersede an unmutated old intent. Preserve the old
		// recovery record until the replacement proposal is valid and ready to
		// persist, and refuse any movement of default across that boundary.
		if existing.DefaultBranch != defaultBranch || defaultParent != existing.DefaultParent {
			return nil, fmt.Errorf("plan %s source changed while default drifted from the prior single-merge intent", planID)
		}
		superseded = existing
	}

	message, reviewMergeBase, exactReview, err := exactReviewMergeMessage(ctx, git, detail, planID, defaultParent, sourceHead, force)
	if err != nil {
		return nil, err
	}
	if !exactReview {
		message, err = s.generateSingleMergeMessage(ctx, git, commitpkg.MergeProposalContext{
			RepoRoot: git.Root(), PlanID: planID, DefaultBranch: defaultBranch,
			DefaultParent: defaultParent, MergeBase: reviewMergeBase, SourceBranch: planBranch, SourceHead: sourceHead,
		})
		if err != nil {
			return nil, err
		}
	}

	if superseded != nil {
		record, recordErr := s.planMergeRecord(detail.Dir, detail)
		if recordErr != nil {
			return nil, fmt.Errorf("clear superseded single-merge intent: %w", recordErr)
		}
		if recordErr = record.ClearSingleMergeCommitIntent(*superseded); recordErr != nil {
			return nil, fmt.Errorf("clear superseded single-merge intent: %w", recordErr)
		}
	}
	intent := plan.SingleMergeCommitIntent{
		Message: message, PlanID: planID, SourceHead: sourceHead,
		DefaultBranch: defaultBranch, DefaultParent: defaultParent, CreatedAt: s.now().UTC(),
	}
	record, err := s.planMergeRecord(detail.Dir, detail)
	if err != nil {
		return nil, fmt.Errorf("prepare single-merge commit intent: %w", err)
	}
	if err := record.RecordSingleMergeCommitIntent(intent); err != nil {
		return nil, fmt.Errorf("persist single-merge commit intent: %w", err)
	}
	return detail.State.Plan.MergeCommitIntent, nil
}

func exactReviewMergeMessage(ctx context.Context, git GitClient, detail *plan.PlanDetail, planID, defaultParent, sourceHead string, force bool) (string, string, bool, error) {
	review := plan.PersistedReview(detail)
	if review == nil || !review.IsApproved() || strings.TrimSpace(review.Head) != sourceHead || review.CommitMessage == nil {
		return "", "", false, nil
	}
	mergeBase, err := git.MergeBase(ctx, defaultParent, sourceHead)
	if err != nil {
		return "", "", false, fmt.Errorf("compute exact review proposal base for %s: %w", planID, err)
	}
	mergeBase = strings.TrimSpace(mergeBase)
	if mergeBase == "" {
		return "", "", false, fmt.Errorf("compute exact review proposal base for %s: empty revision", planID)
	}
	if strings.TrimSpace(review.Base) != mergeBase {
		return "", mergeBase, false, nil
	}
	message, err := singleMergeCommitMessage(*review.CommitMessage, planID, sourceHead)
	if err != nil {
		if force {
			return "", mergeBase, false, nil
		}
		return "", "", false, fmt.Errorf("plan %s review commit proposal is invalid: %w", planID, err)
	}
	return message, mergeBase, true, nil
}

func (s Service) generateSingleMergeMessage(ctx context.Context, git GitClient, exact commitpkg.MergeProposalContext) (string, error) {
	if s.ProposalGenerator == nil {
		return "", fmt.Errorf("plan %s requires exceptional merge commit proposal generation, but no provider-neutral generator is configured", exact.PlanID)
	}
	var err error
	if exact.MergeBase == "" {
		exact.MergeBase, err = git.MergeBase(ctx, exact.DefaultParent, exact.SourceHead)
		if err != nil {
			return "", fmt.Errorf("compute exact merge proposal base for %s: %w", exact.PlanID, err)
		}
		exact.MergeBase = strings.TrimSpace(exact.MergeBase)
		if exact.MergeBase == "" {
			return "", fmt.Errorf("compute exact merge proposal base for %s: empty revision", exact.PlanID)
		}
	}
	exact.Diff, err = git.Diff(ctx, exact.MergeBase+".."+exact.SourceHead)
	if err != nil {
		return "", fmt.Errorf("read exact merge diff for %s: %w", exact.PlanID, err)
	}
	before, err := git.DirtyFingerprint(ctx)
	if err != nil {
		return "", fmt.Errorf("capture Git state before merge proposal generation: %w", err)
	}
	proposal, generateErr := s.ProposalGenerator.GenerateMergeProposal(ctx, exact)
	if generateErr != nil && ctx.Err() != nil {
		return "", generateErr
	}
	after, stateErr := git.DirtyFingerprint(ctx)
	if stateErr != nil {
		return "", fmt.Errorf("verify Git state after merge proposal generation: %w", stateErr)
	}
	sourceAfter, sourceErr := git.RevParse(ctx, exact.SourceBranch)
	defaultAfter, defaultErr := git.RevParse(ctx, exact.DefaultBranch)
	if sourceErr != nil || defaultErr != nil {
		return "", fmt.Errorf("verify refs after merge proposal generation: %w", errors.Join(sourceErr, defaultErr))
	}
	if before.Hash != after.Hash || strings.TrimSpace(sourceAfter) != exact.SourceHead || strings.TrimSpace(defaultAfter) != exact.DefaultParent {
		return "", fmt.Errorf("merge proposal agent mutated Git state; restore the worktree, index, and refs before retrying plan %s", exact.PlanID)
	}
	if generateErr != nil {
		return "", generateErr
	}
	message, err := singleMergeProposalMessage(proposal, exact.PlanID, exact.SourceHead)
	if err != nil {
		return "", fmt.Errorf("plan %s generated merge commit proposal is invalid: %w", exact.PlanID, err)
	}
	return message, nil
}

func singleMergeCommitMessage(proposal plan.ReviewCommitMessage, planID, sourceHead string) (string, error) {
	trailers, err := singleMergeTrailers(planID, sourceHead)
	if err != nil {
		return "", err
	}
	return commitpkg.FormatProposalMessage(proposal.Subject, proposal.Body, trailers...)
}

func singleMergeProposalMessage(proposal commitpkg.Proposal, planID, sourceHead string) (string, error) {
	trailers, err := singleMergeTrailers(planID, sourceHead)
	if err != nil {
		return "", err
	}
	return commitpkg.Format(proposal, trailers...)
}

func singleMergeTrailers(planID, sourceHead string) ([]commitpkg.TrustedTrailer, error) {
	planTrailer, err := commitpkg.NewTrustedTrailer("Tao-Plan", strings.TrimSpace(planID))
	if err != nil {
		return nil, err
	}
	sourceTrailer, err := commitpkg.NewTrustedTrailer("Tao-Source-Head", strings.TrimSpace(sourceHead))
	if err != nil {
		return nil, err
	}
	return []commitpkg.TrustedTrailer{planTrailer, sourceTrailer}, nil
}

func rollbackIntegratedMerge(ctx context.Context, git GitClient, snapshot mergeVerifySnapshot, cause error) error {
	var rollbackErrors []string
	if err := git.ResetHard(ctx, snapshot.preMergeSHA); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Sprintf("reset %s to %s: %v", snapshot.defaultBranch, snapshot.preMergeSHA, err))
	}
	if err := git.Checkout(ctx, snapshot.defaultBranch); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Sprintf("re-checkout default branch %s: %v", snapshot.defaultBranch, err))
	}
	if len(rollbackErrors) == 0 {
		return cause
	}
	return fmt.Errorf("%w; rollback failed: %s", cause, strings.Join(rollbackErrors, "; "))
}

// recordMergeAndCleanup persists the plan_merged event and then removes the
// plan branch and worktree. The order is load-bearing: if recording failed
// after cleanup, the plan would be merged with no plan_merged event while
// every recovery ref (branch tip, rebased review/PR heads) is already gone,
// so no retry could ever complete it. A cleanup decline that reports nothing
// left to clean (the branch is not managed — already removed, or the plan ran
// in current mode with no separate branch) is success, not failure: the merge
// is durably recorded and there is nothing to remove, and reporting failure
// would make unattended queues mark a completed plan as failed.
func (s Service) recordMergeAndCleanup(ctx context.Context, detail *plan.PlanDetail, ref string, mergedDefaultSHA string, options Options, recordedAs string) error {
	if err := s.AppendPlanMergedEvent(detail, ref, mergedDefaultSHA); err != nil {
		return err
	}
	if _, err := s.Cleanup(ctx, detail, options); err != nil && !cleanupAlreadySettled(err) {
		return fmt.Errorf("plan %s %s, but cleanup failed: %w", detail.State.Plan.ID, recordedAs, err)
	}
	return nil
}

type externalMerge struct {
	Ref              string
	DefaultBranch    string
	MergedDefaultSHA string
	AncestryVerified bool
}

func (s Service) tryRecordExternalMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, options Options) (bool, error) {
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return false, err
	}
	if planMergeRecorded(detail) {
		s.logf("Plan merge already recorded for %s.", detail.State.Plan.ID)
		return true, s.retryRecordedMergeCleanup(ctx, git, detail, options)
	}
	if err := s.requireReworkSinceRecordedMerge(ctx, git, detail, options); err != nil {
		return false, err
	}
	if !options.Force {
		if err := requireApproved(detail); err != nil {
			return false, err
		}
	}
	merged, ok, err := s.detectExternalMerge(ctx, git, detail, options)
	if err != nil || !ok {
		return false, err
	}
	if !options.Force {
		if err := s.requireCleanMergeWorktrees(ctx, git, detail); err != nil {
			return false, err
		}
	}
	if merged.AncestryVerified {
		s.logf("Plan already merged into %s at %s via %s; recording completion.", merged.DefaultBranch, merged.MergedDefaultSHA, merged.Ref)
	} else {
		s.logf("Recording external merge for %s at %s without ancestry proof (--force).", merged.DefaultBranch, merged.MergedDefaultSHA)
	}
	return true, s.recordMergeAndCleanup(ctx, detail, merged.Ref, merged.MergedDefaultSHA, options, "external merge recorded")
}

// retryRecordedMergeCleanup finishes cleanup for a plan whose merge is already
// recorded. A prior invocation may have recorded the merge and then failed
// cleanup (e.g. run from inside the plan worktree), which previously leaked the
// branch and worktree forever: the recorded short-circuit reported success
// without retrying. Cleanup that finds nothing left to clean is success.
func (s Service) retryRecordedMergeCleanup(ctx context.Context, git GitClient, detail *plan.PlanDetail, options Options) error {
	// A squash intentionally leaves the source branch non-ancestral, but the
	// current invocation's mode does not prove how a prior merge was recorded.
	// Only a Tao squash commit tied to this plan and its reviewed source head may
	// bypass the ordinary unmerged-branch safeguard on a cleanup retry.
	options.allowNonAncestralCleanup = !options.NoSquash && recordedTaoSquash(ctx, git, detail)
	_, err := s.Cleanup(ctx, detail, options)
	if err == nil || cleanupAlreadySettled(err) {
		return nil
	}
	// A merge recorded without ancestry proof (--record-only --force after a
	// squash or cherry-pick) leaves a branch git still reports as unmerged, so
	// a plain retry declines on every invocation; only --force can remove it.
	// Say so, or the error's implicit "rerun tao merge" guidance loops forever.
	if declined, ok := errors.AsType[*CleanupDeclinedError](err); ok && declined.Status == workspace.ManagedStatusUnmerged && !options.Force {
		return fmt.Errorf("plan %s merge already recorded, but cleanup failed: %w; ancestry cannot prove this merge (squash/cherry-pick), so rerun with --force to remove the branch and worktree", detail.State.Plan.ID, err)
	}
	return fmt.Errorf("plan %s merge already recorded, but cleanup failed: %w", detail.State.Plan.ID, err)
}

func recordedTaoSquash(ctx context.Context, git GitClient, detail *plan.PlanDetail) bool {
	review := plan.PersistedReview(detail)
	if review == nil {
		return false
	}
	planID := strings.TrimSpace(detail.State.Plan.ID)
	sourceHead := strings.TrimSpace(review.Head)
	mergedSHA := supersededMergedDefaultSHA(detail)
	if planID == "" || sourceHead == "" || mergedSHA == "" {
		return false
	}
	message, err := git.CommitMessage(ctx, mergedSHA)
	if err != nil {
		return false
	}
	return taoSquashMessageMatches(message, planID, sourceHead)
}

func taoSquashMessageMatches(message, planID, sourceHead string) bool {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n")), "\n")
	if len(lines) < 2 {
		return false
	}
	return lines[len(lines)-2] == "Tao-Plan: "+strings.TrimSpace(planID) && lines[len(lines)-1] == "Tao-Source-Head: "+strings.TrimSpace(sourceHead)
}

// cleanupAlreadySettled reports whether a cleanup error means there is nothing
// left to clean up — the plan branch is no longer managed — rather than a
// failure to remove something that still exists.
func cleanupAlreadySettled(err error) bool {
	declined, ok := errors.AsType[*CleanupDeclinedError](err)
	return ok && declined.Status == cleanupStatusMissing
}

// requireReworkSinceRecordedMerge refuses to merge or record a reopened plan
// whose branch carries nothing beyond its previously recorded merge. After a
// merge-then-reopen, the branch tip still satisfies both ancestry against the
// default branch and refCarriesPlanWork (it is ahead of the plan base), so
// external-merge detection would re-record the superseded merge — and under
// --force so would the full merge flow, since every gate is skipped and merging
// zero new commits succeeds trivially. Either way the reopened plan is marked
// completed and cleanup deletes the branch and worktree carrying the pending
// rework. Recording deliberately anyway (abandoning the rework) remains
// available via --record-only --force, the same no-proof escape hatch used for
// squash merges.
func (s Service) requireReworkSinceRecordedMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, options Options) error {
	if options.RecordOnly && options.Force {
		return nil
	}
	priorMergedSHA := supersededMergedDefaultSHA(detail)
	if priorMergedSHA == "" {
		return nil
	}
	branch, err := resolvePlanBranch(detail)
	if err != nil {
		return nil //nolint:nilerr // Missing branch metadata leaves nothing live to protect; normal merge gates decide.
	}
	tip, err := git.RevParse(ctx, branch)
	if err != nil {
		// Branch is gone; there is no rework worktree left to protect and
		// detection has nothing live to trust, so let the normal flow decide.
		return nil //nolint:nilerr
	}
	alreadyMerged, err := git.IsAncestor(ctx, strings.TrimSpace(tip), priorMergedSHA)
	if err != nil {
		return fmt.Errorf("check whether %s carries rework beyond recorded merge %s: %w", branch, priorMergedSHA, err)
	}
	if alreadyMerged {
		return fmt.Errorf("plan %s was reopened for rework, but branch %s has no commits beyond the previously recorded merge %s; run the rework first (tao run), or use --record-only --force to intentionally re-record the merge and abandon the rework", detail.State.Plan.ID, branch, priorMergedSHA)
	}
	return nil
}

// supersededMergedDefaultSHA returns the merged default SHA of the most recent
// plan_merged event. Callers invoke it only after planMergeRecorded reported
// false, so any merge found here has been superseded by a later reopen.
func supersededMergedDefaultSHA(detail *plan.PlanDetail) string {
	sha := ""
	for _, event := range detail.Events {
		if event.Type == plan.EventTypePlanMerged {
			sha = strings.TrimSpace(event.MergedDefaultSHA)
		}
	}
	return sha
}

func (s Service) detectExternalMerge(ctx context.Context, git GitClient, detail *plan.PlanDetail, options Options) (externalMerge, bool, error) {
	defaultBranch, err := resolveDefaultBranch(ctx, git, detail)
	if err != nil {
		return externalMerge{}, false, err
	}
	planBranch := ""
	planBranchTip := ""
	if branch, branchErr := resolvePlanBranch(detail); branchErr == nil {
		planBranch = branch
		if tip, tipErr := git.RevParse(ctx, branch); tipErr == nil {
			planBranchTip = strings.TrimSpace(tip)
		}
	}
	// A plan whose branch IS the default branch (execution-mode current) can
	// never be proven merged by ancestry: every candidate ref is a commit of
	// the default branch's own history, trivially an ancestor of default, and
	// refCarriesPlanWork only proves that default advanced past the plan base —
	// unrelated commits landing on default would satisfy it while the plan's
	// actual work sits uncommitted or stashed. Skip detection entirely; the
	// explicit --record-only --force override below remains available.
	if planBranch != defaultBranch {
		for _, ref := range externalMergeRefs(detail) {
			if staleExternalMergeRef(ctx, git, ref, planBranch, planBranchTip) {
				continue
			}
			ok, err := git.IsAncestor(ctx, ref, defaultBranch)
			if err != nil {
				return externalMerge{}, false, fmt.Errorf("check whether %s is already merged into %s: %w", ref, defaultBranch, err)
			}
			if !ok {
				continue
			}
			// Being an ancestor of the default branch is not proof of a merge: a
			// recorded head that never advanced past the plan base (a zero-diff run,
			// or a review taken with no new commits) trivially satisfies ancestry.
			// Require the ref to actually carry plan commits so we never record an
			// unmerged plan as completed and delete its branch.
			provesWork, err := s.refCarriesPlanWork(ctx, git, detail, ref)
			if err != nil {
				return externalMerge{}, false, err
			}
			if !provesWork {
				continue
			}
			mergedDefaultSHA, err := captureMergedDefaultSHA(ctx, git, defaultBranch)
			if err != nil {
				return externalMerge{}, false, err
			}
			return externalMerge{Ref: ref, DefaultBranch: defaultBranch, MergedDefaultSHA: mergedDefaultSHA, AncestryVerified: true}, true, nil
		}
	}
	if options.RecordOnly && options.Force {
		mergedDefaultSHA, err := captureMergedDefaultSHA(ctx, git, defaultBranch)
		if err != nil {
			return externalMerge{}, false, err
		}
		ref := firstExternalMergeRef(detail)
		if ref == "" {
			ref = "external"
		}
		return externalMerge{Ref: ref, DefaultBranch: defaultBranch, MergedDefaultSHA: mergedDefaultSHA}, true, nil
	}
	return externalMerge{}, false, nil
}

// staleExternalMergeRef reports whether ref is a recorded head snapshot that no
// longer matches the live plan branch tip. A snapshot (review head, PR head,
// workspace head) proves an external merge only while it IS the branch tip:
// once follow-up commits advance the branch past the snapshot, the snapshot
// being an ancestor of default proves only that the old tip was merged, and
// recording it would complete the plan while the newer commits stay unmerged
// (and let cleanup delete the branch carrying them). When the plan branch no
// longer exists the snapshots are the only remaining evidence and stay trusted;
// a snapshot that cannot be resolved at all proves nothing and is skipped.
func staleExternalMergeRef(ctx context.Context, git GitClient, ref string, planBranch string, planBranchTip string) bool {
	if ref == planBranch || planBranchTip == "" {
		return false
	}
	refSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		return true
	}
	return strings.TrimSpace(refSHA) != planBranchTip
}

// refCarriesPlanWork reports whether ref is strictly ahead of the plan's base,
// i.e. it contains at least one plan commit rather than merely equalling or
// trailing the base. A ref that carries no plan work is an ancestor of the
// default branch without representing any merged work, so treating it as an
// external merge would falsely complete the plan. When the plan base cannot be
// resolved the check is conservative and reports no proof of work.
func (s Service) refCarriesPlanWork(ctx context.Context, git GitClient, detail *plan.PlanDetail, ref string) (bool, error) {
	base := planBaseRef(detail)
	if base == "" || base == ref {
		return false, nil
	}
	baseLeadsRef, err := git.IsAncestor(ctx, base, ref)
	if err != nil {
		return false, fmt.Errorf("check whether plan base %s precedes %s: %w", base, ref, err)
	}
	if !baseLeadsRef {
		return false, nil
	}
	refTrailsBase, err := git.IsAncestor(ctx, ref, base)
	if err != nil {
		return false, fmt.Errorf("check whether %s trails plan base %s: %w", ref, base, err)
	}
	return !refTrailsBase, nil
}

// planBaseRef resolves the best-available commit the plan branched from, used to
// prove that an external-merge candidate carries plan commits.
func planBaseRef(detail *plan.PlanDetail) string {
	if detail == nil {
		return ""
	}
	if workspace := detail.State.Workspace; workspace != nil {
		if base := strings.TrimSpace(workspace.BaseSHA); base != "" {
			return base
		}
	}
	if review := plan.PersistedReview(detail); review != nil {
		if base := strings.TrimSpace(review.Base); base != "" {
			return base
		}
	}
	return strings.TrimSpace(detail.State.Repo.BaseCommit)
}

func captureMergedDefaultSHA(ctx context.Context, git GitClient, defaultBranch string) (string, error) {
	mergedDefaultSHA, err := git.RevParse(ctx, defaultBranch)
	if err != nil {
		return "", fmt.Errorf("capture merged default SHA for %s: %w", defaultBranch, err)
	}
	mergedDefaultSHA = strings.TrimSpace(mergedDefaultSHA)
	if mergedDefaultSHA == "" {
		return "", fmt.Errorf("capture merged default SHA for %s: empty revision", defaultBranch)
	}
	return mergedDefaultSHA, nil
}

// planMergeRecorded reports whether the plan is currently in its terminal merged
// state. A plan reopened for rework after a merge is not treated as recorded, so
// the reworked plan can be re-merged rather than being skipped as already done.
func planMergeRecorded(detail *plan.PlanDetail) bool {
	return detail != nil && plan.PlanIsMerged(detail.Events)
}

func externalMergeRefs(detail *plan.PlanDetail) []string {
	if detail == nil {
		return nil
	}
	var refs []string
	if branch, err := resolvePlanBranch(detail); err == nil {
		refs = append(refs, branch)
	}
	// A plan reopened for rework after its last review carries recorded head
	// snapshots (review head, PR head, workspace head) that describe the
	// pre-reopen cycle and still point at the previously-merged commit. Treating
	// those stale snapshots as external-merge candidates would re-detect the old
	// merge and delete the branch holding the unmerged rework commits, so until
	// the reworked plan is re-reviewed we trust only the live plan branch.
	if !plan.ReviewSupersededByReopen(detail.Events) {
		if review := plan.PersistedReview(detail); review != nil {
			refs = append(refs, review.Head)
		}
		if pr := detail.State.Plan.PullRequest; pr != nil {
			refs = append(refs, pr.HeadSHA)
		}
		if workspace := detail.State.Workspace; workspace != nil {
			refs = append(refs, workspace.HeadSHA)
		}
	}
	return uniqueNonEmpty(refs)
}

func firstExternalMergeRef(detail *plan.PlanDetail) string {
	refs := externalMergeRefs(detail)
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	var unique []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

type NotApprovedError struct {
	PlanID string
	Reason string
}

func (e *NotApprovedError) Error() string {
	if e.PlanID == "" {
		return e.Reason
	}
	return fmt.Sprintf("plan %s is not approved for merge: %s", e.PlanID, e.Reason)
}

func (e *NotApprovedError) Unwrap() error { return ErrNotApproved }

type ReviewBaseMismatchError struct {
	ReviewBase    string
	MergeBase     string
	DefaultBranch string
	PlanBranch    string
}

func (e *ReviewBaseMismatchError) Error() string {
	return fmt.Sprintf("review base %q does not match merge-base(%s, %s) %q", e.ReviewBase, e.DefaultBranch, e.PlanBranch, e.MergeBase)
}

func (e *ReviewBaseMismatchError) Unwrap() error { return ErrReviewBaseMismatch }

type ReviewHeadMismatchError struct {
	ReviewHead string
	BranchTip  string
	PlanBranch string
}

func (e *ReviewHeadMismatchError) Error() string {
	return fmt.Sprintf("review head %q does not match plan branch %s tip %q", e.ReviewHead, e.PlanBranch, e.BranchTip)
}

func (e *ReviewHeadMismatchError) Unwrap() error { return ErrReviewHeadMismatch }

type DirtyWorktreeError struct {
	Status string
}

func (e *DirtyWorktreeError) Error() string {
	status := strings.TrimSpace(e.Status)
	if status == "" {
		return ErrDirtyWorktree.Error()
	}
	firstLine, _, _ := strings.Cut(status, "\n")
	return fmt.Sprintf("%s: %s", ErrDirtyWorktree.Error(), firstLine)
}

func (e *DirtyWorktreeError) Unwrap() error { return ErrDirtyWorktree }

func (s Service) CheckPreMergeGate(ctx context.Context, detail *plan.PlanDetail, options Options) error {
	if detail == nil {
		return fmt.Errorf("merge plan detail is nil")
	}
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	if options.Force {
		return nil
	}
	if err := requireApproved(detail); err != nil {
		return err
	}
	git, err := s.gitClient()
	if err != nil {
		return err
	}
	if err := s.requireCleanMergeWorktrees(ctx, git, detail); err != nil {
		return err
	}
	defaultBranch, err := resolveDefaultBranch(ctx, git, detail)
	if err != nil {
		return err
	}
	branch, err := resolvePlanBranch(detail)
	if err != nil {
		return err
	}
	mergeBase, err := git.MergeBase(ctx, defaultBranch, branch)
	if err != nil {
		return fmt.Errorf("compute merge base for %s and %s: %w", defaultBranch, branch, err)
	}
	review := plan.PersistedReview(detail)
	reviewBase := strings.TrimSpace(review.Base)
	mergeBase = strings.TrimSpace(mergeBase)
	if reviewBase != mergeBase {
		return &ReviewBaseMismatchError{ReviewBase: reviewBase, MergeBase: mergeBase, DefaultBranch: defaultBranch, PlanBranch: branch}
	}
	// The review verdict covers base..head, so the branch tip must still be
	// the reviewed head: commits added after the review — leftover worktree
	// changes committed later, or new work — are unreviewed and must not merge
	// on the strength of the old approval. Legacy reviews that never recorded
	// a head keep the base-only gate.
	if reviewHead := strings.TrimSpace(review.Head); reviewHead != "" {
		tip, err := git.RevParse(ctx, branch)
		if err != nil {
			return fmt.Errorf("resolve plan branch tip %s: %w", branch, err)
		}
		if tip = strings.TrimSpace(tip); tip != reviewHead {
			return &ReviewHeadMismatchError{ReviewHead: reviewHead, BranchTip: tip, PlanBranch: branch}
		}
	}
	return nil
}

func (s Service) gitClient() (GitClient, error) {
	if s.Git == nil {
		return nil, fmt.Errorf("merge git client is nil")
	}
	return s.Git, nil
}

func (s Service) gitClientForRoot(root string) (GitClient, error) {
	if s.NewGit != nil {
		return s.NewGit(root), nil
	}
	if s.Git != nil && s.Git.Root() == root {
		return s.Git, nil
	}
	return nil, fmt.Errorf("merge git client for root %q is unavailable", root)
}

func (s Service) logMergeVerifySkipped(resolution mergeVerifyCommandResolution) {
	if !resolution.skippedNoDetection() {
		return
	}
	s.logf("merge verification skipped: %s", mergeVerifySkippedReason(resolution))
}

func mergeVerifySkippedReason(resolution mergeVerifyCommandResolution) string {
	switch resolution.source {
	case mergeVerifySourceNoVerify:
		return "verification disabled"
	case mergeVerifySourceOption:
		return "configured verification command is empty"
	case mergeVerifySourceEnv:
		return "TAO_MERGE_VERIFY_COMMAND is empty"
	case mergeVerifySourceNoDetection:
		if repoRoot := strings.TrimSpace(resolution.repoRoot); repoRoot != "" {
			return "no supported build system detected in " + repoRoot
		}
		return "no supported build system detected"
	default:
		return "no verification command resolved"
	}
}

func (s Service) appendMergeVerificationEvent(detail *plan.PlanDetail, event plan.Event) {
	if s.Events == nil || detail == nil {
		return
	}
	event.Type = plan.EventTypeMergeVerification
	event.Timestamp = s.now().UTC()
	event.PlanID = strings.TrimSpace(detail.State.Plan.ID)
	if err := s.Events.AppendEvent(detail.Dir, event); err != nil {
		s.logf("append merge_verification event: %v", err)
	}
}

func (s Service) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// usesPlanWorktreeClient reports whether worktreeGit will return a git client
// bound to a separate plan worktree (rather than falling back to the repo-root
// client). The plan branch is already checked out in a separate worktree, so
// callers must not check it out again in the main checkout; in the fallback case
// they must.
func (s Service) usesPlanWorktreeClient(detail *plan.PlanDetail) bool {
	return hasSeparatePlanWorktree(detail) && s.NewGit != nil
}

func (s Service) worktreeGit(detail *plan.PlanDetail) (GitClient, error) {
	if s.usesPlanWorktreeClient(detail) {
		worktreePath := mergePlanWorktreeIdentity(detail).Path
		if worktreePath == "" {
			return nil, fmt.Errorf("merge worktree path could not be resolved for plan %s", detail.State.Plan.ID)
		}
		git := s.NewGit(worktreePath)
		if git == nil {
			return nil, fmt.Errorf("merge worktree git client is nil for %s", worktreePath)
		}
		if root := git.Root(); root != worktreePath {
			return nil, fmt.Errorf("worktree git client root mismatch: NewGit(%q) returned client bound to %q", worktreePath, root)
		}
		return git, nil
	}
	return s.gitClient()
}

// hasSeparatePlanWorktree reports whether the plan has a separate worktree
// that still exists on disk. A recorded worktree already removed (manually, or
// by a disk cleanup) has nothing to keep clean and nothing to rebase in, so
// callers fall back to the repo-root client — consistent with Cleanup, which
// treats a missing worktree as already settled rather than as a failure.
func hasSeparatePlanWorktree(detail *plan.PlanDetail) bool {
	identity := mergePlanWorktreeIdentity(detail)
	if !identity.Separate {
		return false
	}
	info, err := os.Stat(identity.Path)
	return err == nil && info.IsDir()
}

func mergePlanWorktreeIdentity(detail *plan.PlanDetail) workspace.PlanWorktreeIdentity {
	identity, err := workspace.ResolveExecutionRoot(detail, workspace.DefaultConfig())
	if err != nil || identity.Strategy != plan.WorkspaceStrategyWorktree {
		return workspace.PlanWorktreeIdentity{}
	}
	return workspace.PlanWorktreeIdentity{Path: identity.Root, Separate: identity.Separate}
}

func requireApproved(detail *plan.PlanDetail) error {
	derived := plan.AnalyzeRunCapabilities(detail)
	planID := detail.State.Plan.ID
	if !derived.Complete {
		return &NotApprovedError{PlanID: planID, Reason: fmt.Sprintf("plan status is %q", detail.State.Status)}
	}
	if !derived.Reviewed {
		return &NotApprovedError{PlanID: planID, Reason: "plan has not been reviewed"}
	}
	review := plan.PersistedReview(detail)
	if !review.IsApproved() {
		return &NotApprovedError{PlanID: planID, Reason: fmt.Sprintf("review status %q verdict %q", review.Status, review.Verdict)}
	}
	return nil
}

// requireCleanMergeWorktrees enforces a clean repo-root worktree and, when the
// plan runs in a separate worktree, a clean plan worktree too. The external-merge
// record path and the pre-merge gate must apply the identical cleanliness check,
// so both call this helper rather than open-coding it and drifting apart.
func (s Service) requireCleanMergeWorktrees(ctx context.Context, git GitClient, detail *plan.PlanDetail) error {
	if err := requireCleanWorktree(ctx, git); err != nil {
		return err
	}
	if hasSeparatePlanWorktree(detail) {
		worktreeGit, err := s.worktreeGit(detail)
		if err != nil {
			return err
		}
		if err := requireCleanWorktree(ctx, worktreeGit); err != nil {
			return err
		}
	}
	return nil
}

func requireCleanWorktree(ctx context.Context, git GitClient) error {
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("check worktree status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return &DirtyWorktreeError{Status: status}
	}
	return nil
}

func resolveDefaultBranch(ctx context.Context, git GitClient, detail *plan.PlanDetail) (string, error) {
	branch, err := git.DefaultBranch(ctx)
	if strings.TrimSpace(branch) != "" && err == nil {
		return strings.TrimSpace(branch), nil
	}
	if fallback := workspaceBaseBranch(detail); fallback != "" {
		return fallback, nil
	}
	if err != nil {
		return "", fmt.Errorf("detect default branch: %w", err)
	}
	return "", fmt.Errorf("detect default branch: no default branch found")
}

func workspaceBaseBranch(detail *plan.PlanDetail) string {
	if detail.State.Workspace == nil {
		return ""
	}
	return strings.TrimSpace(detail.State.Workspace.BaseBranch)
}

func resolvePlanBranch(detail *plan.PlanDetail) (string, error) {
	if detail.State.Workspace == nil {
		return "", fmt.Errorf("plan workspace metadata is missing")
	}
	branch := strings.TrimSpace(detail.State.Workspace.Branch)
	if branch == "" {
		return "", fmt.Errorf("plan workspace branch is missing")
	}
	return branch, nil
}
