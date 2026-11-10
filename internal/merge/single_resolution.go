package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/prompts"
)

var (
	ErrSingleResolutionRejected  = errors.New("single-plan conflict resolution rejected")
	ErrSingleResolutionDrift     = errors.New("single-plan conflict resolution drifted from durable intent")
	ErrSingleResolutionPreflight = errors.New("single-plan conflict resolution could not start safely")
)

// Authority labels describe the remaining resolver allowance in startup failure diagnostics.
const (
	SingleResolutionAuthorityPreserved = "preserved"
	SingleResolutionAuthorityRearmed   = "rearmed"
	SingleResolutionAuthorityConsumed  = "consumed"
)

// SingleResolutionStartupError is safe rendering metadata for one launch
// failure. Cause may contain bounded provider diagnostics; Capability and
// Authority are the stable user-facing classifications.
type SingleResolutionStartupError struct {
	Capability       plan.SingleMergeStartupCapability
	PromptAcceptance string
	Authority        string
	Cause            error
}

func (e *SingleResolutionStartupError) Error() string {
	return fmt.Sprintf("%s failed (%s one-shot authority): %v", e.Capability, e.Authority, e.Cause)
}

func (e *SingleResolutionStartupError) Unwrap() error { return e.Cause }

func startupCapability(err error) plan.SingleMergeStartupCapability {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "resolve provider executable"), strings.Contains(message, "executable") && strings.Contains(message, "not found"):
		return plan.SingleMergeStartupExecutable
	case strings.Contains(message, "private pi configuration"), strings.Contains(message, "configuration projection"), strings.Contains(message, "prepare private pi"):
		return plan.SingleMergeStartupConfigProjection
	case strings.Contains(message, "no selected model"), strings.Contains(message, "selected model is incomplete"):
		return plan.SingleMergeStartupSelectedModel
	case strings.Contains(message, "no local credentials"):
		return plan.SingleMergeStartupLocalCredentials
	case strings.Contains(message, "confinement"), strings.Contains(message, "sandbox"), strings.Contains(message, "bubblewrap"), strings.Contains(message, "bwrap"):
		return plan.SingleMergeStartupConfinement
	default:
		return plan.SingleMergeStartupRPCInitialization
	}
}

func safePreAcceptanceFailure(acceptance string, err error) bool {
	if acceptance != "not_transmitted" && acceptance != "rejected" {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, unsafe := range []string{"authentication", "unauthorized", "forbidden", "remote credential", "api key"} {
		if strings.Contains(message, unsafe) {
			return false
		}
	}
	return true
}

// SingleResolutionRecorder is the plan-scoped durable authority used by the
// resolver. PlanRecord implements this interface with compare-and-set writes.
type SingleResolutionRecorder interface {
	RecordSingleMergeResolution(plan.SingleMergeCommitIntent, plan.SingleMergeResolution) error
	AdvanceSingleMergeResolution(plan.SingleMergeCommitIntent, plan.SingleMergeResolution) error
	RearmSingleMergeResolution(plan.SingleMergeCommitIntent, plan.SingleMergeStartupFailure) error
}

// SingleConflictResolver is the editing seam used by ordinary squash merges.
// Implementations must not stage, commit, or move refs before returning exact
// resolved evidence.
type SingleConflictResolver interface {
	ResolveConflict(context.Context, SingleResolutionRequest) (SingleResolutionResult, error)
}

// SingleConflictResolutionService includes the Tao-owned settlement step used
// by the single-plan merge transaction after the editing session returns.
type SingleConflictResolutionService interface {
	SingleConflictResolver
	SettleResolved(context.Context, SingleResolutionRequest) (SingleResolutionSettlement, error)
}

// SingleResolutionRequest describes an already-prepared squash conflict. All
// descriptive fields are rendered as JSON-delimited untrusted packets.
type SingleResolutionRequest struct {
	Intent          plan.SingleMergeCommitIntent
	SourceBranch    string
	IntegrationRoot string
	PlanTitle       string
	SourceReview    string
	ChangedFiles    []string
	ConflictFiles   []string
	ConflictStatus  string
	VerifyCommand   string
}

// SingleResolutionResult exposes the provider result for best-effort generic
// plan telemetry while keeping it non-authoritative. Intent is the latest exact
// durable transaction known to the resolver.
type SingleResolutionResult struct {
	Intent    plan.SingleMergeCommitIntent
	Provider  BatchAgentSessionResult
	Recovered bool
}

// SingleResolutionSettlement describes an exact Tao-owned commit recovered or
// created from durable resolved evidence.
type SingleResolutionSettlement struct {
	Intent    plan.SingleMergeCommitIntent
	Head      string
	Recovered bool
}

// GuardedSingleConflictResolver performs one untrusted editing session and
// provides a separate idempotent Tao-owned settlement step.
type GuardedSingleConflictResolver struct {
	Git      GitClient
	Recorder SingleResolutionRecorder
	Agent    SingleMergeAgent
	Now      func() time.Time
}

func (r GuardedSingleConflictResolver) ResolveConflict(ctx context.Context, request SingleResolutionRequest) (result SingleResolutionResult, err error) {
	result.Intent = request.Intent
	if r.Git == nil || r.Recorder == nil {
		return result, fmt.Errorf("%w: git client and recorder are required", ErrSingleResolutionRejected)
	}
	if err := request.Intent.Validate(); err != nil {
		return result, fmt.Errorf("%w: invalid single-merge intent: %w", ErrSingleResolutionRejected, err)
	}
	if err := validateSingleResolutionRoot(r.Git, request.IntegrationRoot); err != nil {
		return result, fmt.Errorf("%w: %w", ErrSingleResolutionRejected, err)
	}
	boundary, err := snapshotWorktreePaths(ctx, r.Git)
	if err != nil {
		return result, fmt.Errorf("%w: snapshot worktree rollback boundary: %w", ErrSingleResolutionRejected, err)
	}
	defer boundary.cleanup()
	rollbackCtx := ctx
	reject := func(result SingleResolutionResult, intent plan.SingleMergeCommitIntent, cause error) (SingleResolutionResult, error) {
		return r.reject(rollbackCtx, result, intent, boundary, cause)
	}
	if request.Intent.Resolution != nil {
		if request.Intent.Resolution.Phase == plan.SingleMergeResolutionPhaseResolved || request.Intent.Resolution.Phase == plan.SingleMergeResolutionPhaseCommitted || request.Intent.Resolution.Phase == plan.SingleMergeResolutionPhaseReviewed {
			settled, settleErr := r.SettleResolved(ctx, request)
			result.Intent, result.Recovered = settled.Intent, true
			return result, settleErr
		}
		return reject(result, request.Intent, errors.New("an earlier resolution request has uncertain provider state and will not be rerun"))
	}
	if r.Agent == nil {
		return result, fmt.Errorf("%w: resolver agent is required", ErrSingleResolutionRejected)
	}
	sourceBranch, err := cleanProtectedBranch(request.SourceBranch)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrSingleResolutionRejected, err)
	}
	conflictFiles, err := normalizeResolutionPaths(request.ConflictFiles, true)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("invalid conflict paths: %w", err))
	}
	changedContext, err := normalizeResolutionPaths(request.ChangedFiles, false)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("invalid changed-file context: %w", err))
	}
	currentBranch, err := r.Git.CurrentBranch(ctx)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("inspect conflicted worktree branch: %w", err))
	}
	if strings.TrimSpace(currentBranch) != request.Intent.DefaultBranch {
		return reject(result, request.Intent, fmt.Errorf("conflicted worktree is not on protected default branch %s", request.Intent.DefaultBranch))
	}
	beforeHead, err := r.Git.RevParse(ctx, "HEAD")
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("inspect conflicted HEAD: %w", err))
	}
	if strings.TrimSpace(beforeHead) != request.Intent.DefaultParent {
		return reject(result, request.Intent, fmt.Errorf("conflicted HEAD does not match durable parent %s", request.Intent.DefaultParent))
	}
	beforeRefs, err := snapshotProtectedRefs(ctx, r.Git, []string{request.Intent.DefaultBranch, sourceBranch})
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("snapshot protected refs: %w", err))
	}
	if beforeRefs["refs/heads/"+request.Intent.DefaultBranch] != request.Intent.DefaultParent || beforeRefs["refs/heads/"+sourceBranch] != request.Intent.SourceHead {
		return reject(result, request.Intent, errors.New("protected refs do not match durable single-merge intent"))
	}
	beforeChanges, err := concretePorcelainChanges(ctx, r.Git)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("inspect prepared conflict: %w", err))
	}
	if !beforeChanges.unmerged {
		return reject(result, request.Intent, errors.New("prepared worktree has no unresolved entries"))
	}
	unexpectedPaths := pathsOutsideResolutionScope(beforeChanges.changedPaths, changedContext, conflictFiles)
	if len(unexpectedPaths) > 0 {
		boundary.preserved, err = snapshotSelectedWorktreePaths(ctx, r.Git.Root(), unexpectedPaths, boundary.backing, worktreeSnapshotLimits{
			maxPaths:       maxWorktreeBoundaryPaths,
			maxBackupBytes: maxWorktreeBoundaryBackupBytes,
		})
		if err != nil {
			return reject(result, request.Intent, fmt.Errorf("snapshot pre-existing worktree changes: %w", err))
		}
		return reject(result, request.Intent, fmt.Errorf("automatic conflict resolution refuses pre-existing non-ignored worktree changes: %s", strings.Join(unexpectedPaths, ", ")))
	}
	if unsafeResolutionPaths(beforeChanges.changedPaths) {
		return reject(result, request.Intent, errors.New("prepared conflict contains unsafe paths"))
	}
	beforeFingerprint, err := aggregateReworkContentFingerprint(r.Git.Root(), beforeChanges.changedPaths)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("fingerprint prepared conflict: %w", err))
	}
	initialMarkerSizes, err := effectiveConflictMarkerSizes(ctx, r.Git.Root(), conflictFiles)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("capture prepared conflict-marker sizes: %w", err))
	}
	originalMarkerSizes := markerSizesByPath(conflictFiles, initialMarkerSizes)

	formattedStatus, formatErr := gitops.FormatPorcelainStatusZ(request.ConflictStatus)
	if formatErr != nil {
		return reject(result, request.Intent, fmt.Errorf("invalid conflict status %q: %w", request.ConflictStatus, formatErr))
	}
	prompt, err := prompts.RenderMergeResolve(prompts.MergeResolveData{
		Operation: "single-plan squash conflict resolution", PlanID: request.Intent.PlanID,
		SourceHead: request.Intent.SourceHead, IntegrationBase: request.Intent.DefaultParent,
		VerifyCommand: request.VerifyCommand, PlanBrief: request.PlanTitle, SourceReview: request.SourceReview,
		Diff: strings.Join(changedContext, "\n"), ConflictFiles: strings.Join(conflictFiles, "\n") + "\n" + formattedStatus,
	})
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("render resolution prompt: %w", err))
	}

	gitBoundary, err := snapshotGitSessionBoundary(ctx, r.Git.Root())
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("snapshot Git metadata and refs: %w", err))
	}
	defer gitBoundary.cleanup()
	agentRequest := BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, Attempt: 1,
		IntegrationRoot: request.IntegrationRoot, Prompt: prompt, CandidatePlanID: request.Intent.PlanID, PromptTemplate: "merge-resolve",
		ProtectedGitObjectRoot: gitBoundary.objects.root,
		ProtectedGitWritePaths: gitBoundary.protectedGitWritePaths(),
	}
	if err := r.Agent.Preflight(ctx, agentRequest); err != nil {
		classified := &SingleResolutionStartupError{
			Capability: startupCapability(err), PromptAcceptance: "not_transmitted",
			Authority: SingleResolutionAuthorityPreserved, Cause: fmt.Errorf("%w: %w", ErrSingleResolutionPreflight, err),
		}
		return reject(result, request.Intent, classified)
	}

	requestedAt := r.timestamp(request.Intent.CreatedAt)
	requested := plan.SingleMergeResolution{
		Phase: plan.SingleMergeResolutionPhaseRequested, ConflictFiles: conflictFiles,
		RequestedAt: requestedAt, ChangedPaths: []string{},
	}
	if err := r.Recorder.RecordSingleMergeResolution(request.Intent, requested); err != nil {
		return reject(result, request.Intent, fmt.Errorf("persist conflict resolution request: %w", err))
	}
	result.Intent.Resolution = &requested

	result.Provider, err = r.Agent.Resolve(ctx, agentRequest)
	cleanupCtx, cancelCleanup := singleAgentCleanupContext(ctx)
	defer cancelCleanup()
	rollbackCtx = cleanupCtx
	createdNestedControls, nestedControlErr := inspectNestedGitControls(cleanupCtx, gitBoundary)
	gitBoundaryErr := compareGitSessionBoundaryExceptNested(cleanupCtx, gitBoundary)
	ignoredChanged, boundaryErr := ignoredWorktreeChanged(cleanupCtx, r.Git, boundary)
	afterChanges, statusErr := concretePorcelainChanges(cleanupCtx, r.Git)
	afterHead, headErr := r.Git.RevParse(cleanupCtx, "HEAD")
	if gitBoundaryErr != nil {
		// The provider is OS-confined from these paths, so drift here can only
		// belong to another actor that Tao does not lock. Reject without rollback:
		// resetting the integration boundary could rewind that actor's ref or
		// overwrite its linked-checkout or nested-repository work.
		return result, fmt.Errorf("%w: protected Git metadata, refs, or a linked checkout changed concurrently: %w", ErrSingleResolutionRejected, errors.Join(gitBoundaryErr, nestedControlErr, headErr))
	}
	if nestedControlErr != nil {
		if len(createdNestedControls) == 0 {
			// Every pre-existing nested control is mounted read-only for the
			// provider. Its drift therefore belongs to another process and must
			// be preserved just like drift in the top-level Git boundary.
			return result, fmt.Errorf("%w: protected nested Git control metadata changed concurrently: %w", ErrSingleResolutionRejected, errors.Join(nestedControlErr, headErr))
		}
		removeErr := removeCreatedNestedGitControls(cleanupCtx, gitBoundary, createdNestedControls)
		return reject(result, result.Intent, errors.Join(errors.New("resolver created nested Git control metadata"), nestedControlErr, removeErr))
	}
	if headErr != nil {
		return reject(result, result.Intent, fmt.Errorf("inspect HEAD after resolver: %w", headErr))
	}
	if strings.TrimSpace(afterHead) != request.Intent.DefaultParent {
		return reject(result, result.Intent, errors.New("resolver changed protected HEAD"))
	}
	if boundaryErr != nil {
		return reject(result, result.Intent, fmt.Errorf("inspect ignored paths after resolver: %w", boundaryErr))
	}
	if ignoredChanged {
		return reject(result, result.Intent, errors.New("resolver created, deleted, or changed an ignored path"))
	}
	if err != nil {
		acceptance := string(result.Provider.Provider.PromptAcceptance)
		if safePreAcceptanceFailure(acceptance, err) {
			capability := startupCapability(err)
			if acceptance == "rejected" {
				capability = plan.SingleMergeStartupPromptAcceptance
			}
			return r.rejectAndRearm(cleanupCtx, result, request, boundary, capability, acceptance, fmt.Errorf("provider session failed: %w", err))
		}
		return reject(result, result.Intent, &SingleResolutionStartupError{
			Capability: startupCapability(err), PromptAcceptance: acceptance,
			Authority: SingleResolutionAuthorityConsumed, Cause: fmt.Errorf("provider session failed: %w", err),
		})
	}
	if statusErr != nil {
		return reject(result, result.Intent, fmt.Errorf("inspect resolver edits: %w", statusErr))
	}
	if unexpected := pathsOutsideResolutionScope(afterChanges.changedPaths, changedContext, conflictFiles); len(unexpected) > 0 {
		return reject(result, result.Intent, fmt.Errorf("resolver changed paths outside the source change: %s", strings.Join(unexpected, ", ")))
	}
	output, err := decodeBatchResolutionOutput(result.Provider.Output)
	if err != nil {
		return reject(result, result.Intent, fmt.Errorf("resolver returned malformed output: %w", err))
	}
	markerPaths := append(append([]string(nil), afterChanges.markerScanPaths...), presentMarkerScanPaths(r.Git.Root(), conflictFiles)...)
	sort.Strings(markerPaths)
	markerPaths = slices.Compact(markerPaths)
	validation := validateAgentEditsAtMarkerSizes(cleanupCtx, r.Git.Root(), afterChanges.changedPaths, markerPaths, originalMarkerSizes)
	if err := singleResolutionValidationError(validation); err != nil {
		return reject(result, result.Intent, err)
	}
	afterFingerprint, err := aggregateReworkContentFingerprint(r.Git.Root(), afterChanges.changedPaths)
	if err != nil {
		return reject(result, result.Intent, fmt.Errorf("fingerprint resolver edits: %w", err))
	}
	if slices.Equal(beforeChanges.changedPaths, afterChanges.changedPaths) && beforeFingerprint == afterFingerprint {
		return reject(result, result.Intent, errors.New("resolver made no content or path changes"))
	}
	message, err := singleMergeCommitMessage(output.CommitMessage, request.Intent.PlanID, request.Intent.SourceHead)
	if err != nil {
		return reject(result, result.Intent, fmt.Errorf("resolver commit proposal is invalid: %w", err))
	}
	resolved := requested
	resolved.Phase = plan.SingleMergeResolutionPhaseResolved
	resolved.Outcome = plan.SingleMergeResolutionOutcomeResolved
	resolved.Summary = boundResolutionSummary(output.Summary)
	resolved.ChangedPaths = append([]string(nil), afterChanges.changedPaths...)
	resolved.ContentFingerprint = afterFingerprint
	resolved.CommitMessage = message
	resolved.ResolvedAt = r.timestamp(requestedAt)
	if err := r.Recorder.AdvanceSingleMergeResolution(result.Intent, resolved); err != nil {
		return reject(result, result.Intent, fmt.Errorf("persist exact resolution intent: %w", err))
	}
	result.Intent.Resolution = &resolved
	return result, nil
}

// SettleResolved validates the exact durable edit set, stages only those paths,
// creates the Tao-owned commit, and advances committed evidence. If the exact
// commit already exists at the durable parent, it is recovered without staging
// or invoking an agent.
func (r GuardedSingleConflictResolver) SettleResolved(ctx context.Context, request SingleResolutionRequest) (SingleResolutionSettlement, error) {
	settlement := SingleResolutionSettlement{Intent: request.Intent}
	if r.Git == nil || r.Recorder == nil {
		return settlement, errors.New("git client and recorder are required")
	}
	if err := request.Intent.Validate(); err != nil {
		return settlement, fmt.Errorf("invalid single-merge resolution intent: %w", err)
	}
	resolution := request.Intent.Resolution
	if resolution == nil || (resolution.Phase != plan.SingleMergeResolutionPhaseResolved && resolution.Phase != plan.SingleMergeResolutionPhaseCommitted && resolution.Phase != plan.SingleMergeResolutionPhaseReviewed) {
		return settlement, errors.New("single-merge resolution is not ready for settlement")
	}
	if err := validateSingleResolutionRoot(r.Git, request.IntegrationRoot); err != nil {
		return settlement, err
	}
	boundary, err := snapshotWorktreePaths(ctx, r.Git)
	if err != nil {
		return settlement, fmt.Errorf("snapshot worktree rollback boundary: %w", err)
	}
	defer boundary.cleanup()
	sourcePaths, err := normalizeResolutionPaths(request.ChangedFiles, false)
	if err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("invalid changed-file context: %w", err))
	}
	if unexpected := pathsOutsideResolutionScope(resolution.ChangedPaths, sourcePaths, resolution.ConflictFiles); len(unexpected) > 0 {
		boundary.preserved, err = snapshotSelectedWorktreePaths(ctx, r.Git.Root(), unexpected, boundary.backing, worktreeSnapshotLimits{
			maxPaths:       maxWorktreeBoundaryPaths,
			maxBackupBytes: maxWorktreeBoundaryBackupBytes,
		})
		if err != nil {
			return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("snapshot paths outside the source change: %w", err))
		}
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("%w: durable resolution includes paths outside the source change: %s", ErrSingleResolutionDrift, strings.Join(unexpected, ", ")))
	}
	sourceBranch, err := cleanProtectedBranch(request.SourceBranch)
	if err != nil {
		return settlement, err
	}
	sourceHead, sourceErr := r.Git.RevParse(ctx, "refs/heads/"+sourceBranch)
	if sourceErr != nil {
		return settlement, fmt.Errorf("%w: inspect source ref: %w", ErrSingleResolutionDrift, sourceErr)
	}
	if strings.TrimSpace(sourceHead) != request.Intent.SourceHead {
		return settlement, fmt.Errorf("%w: source ref changed", ErrSingleResolutionDrift)
	}
	head, headErr := r.Git.RevParse(ctx, "HEAD")
	if headErr != nil {
		return settlement, fmt.Errorf("inspect resolution HEAD: %w", headErr)
	}
	head = strings.TrimSpace(head)
	if head != request.Intent.DefaultParent {
		exact, inspectErr := inspectExactResolutionCommit(ctx, r.Git, request.Intent, head)
		if inspectErr != nil {
			return settlement, fmt.Errorf("%w: inspect possible resolution commit: %w", ErrSingleResolutionDrift, inspectErr)
		}
		if !exact {
			return settlement, fmt.Errorf("%w: HEAD is neither the durable parent nor exact resolution commit", ErrSingleResolutionDrift)
		}
		if resolution.Phase == plan.SingleMergeResolutionPhaseCommitted || resolution.Phase == plan.SingleMergeResolutionPhaseReviewed {
			if resolution.IntegrationHead != head {
				return settlement, fmt.Errorf("%w: committed head changed", ErrSingleResolutionDrift)
			}
			settlement.Head, settlement.Recovered = head, true
			return settlement, nil
		}
		return r.advanceCommitted(request.Intent, head, true)
	}
	if resolution.Phase == plan.SingleMergeResolutionPhaseCommitted || resolution.Phase == plan.SingleMergeResolutionPhaseReviewed {
		return settlement, fmt.Errorf("%w: durable committed result is absent", ErrSingleResolutionDrift)
	}
	currentBranch, branchErr := r.Git.CurrentBranch(ctx)
	if branchErr != nil || strings.TrimSpace(currentBranch) != request.Intent.DefaultBranch {
		return settlement, fmt.Errorf("%w: settlement worktree is not on %s: %w", ErrSingleResolutionDrift, request.Intent.DefaultBranch, branchErr)
	}
	changes, err := prepareMergeEdits(ctx, r.Git)
	if err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("inspect durable resolution edits: %w", err))
	}
	// These checks precede Tao's staging. Mismatches may be intervening user
	// work after a crash, so refuse them without running destructive rollback.
	if !changes.matchesPaths(resolution.ChangedPaths) {
		return settlement, fmt.Errorf("%w: changed path set differs", ErrSingleResolutionDrift)
	}
	matches, err := changes.matchesContent(r.Git.Root(), resolution.ContentFingerprint)
	if err != nil {
		return settlement, fmt.Errorf("%w: inspect content: %w", ErrSingleResolutionDrift, err)
	}
	if !matches {
		return settlement, fmt.Errorf("%w: content differs", ErrSingleResolutionDrift)
	}
	markerPaths := append(append([]string(nil), changes.markerScanPaths...), presentMarkerScanPaths(r.Git.Root(), resolution.ConflictFiles)...)
	sort.Strings(markerPaths)
	markerPaths = slices.Compact(markerPaths)
	indexedMarkerSizes, err := cachedConflictMarkerSizes(ctx, r.Git.Root(), resolution.ConflictFiles)
	if err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("inspect prepared conflict-marker sizes: %w", err))
	}
	originalMarkerSizes := markerSizesByPath(resolution.ConflictFiles, indexedMarkerSizes)
	if err := singleResolutionValidationError(validateAgentEditsAtMarkerSizes(ctx, r.Git.Root(), changes.changedPaths, markerPaths, originalMarkerSizes)); err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, err)
	}
	if err := changes.stage(ctx, r.Git); err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("stage exact resolution edits: %w", err))
	}
	staged, err := concretePorcelainChanges(ctx, r.Git)
	if err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("inspect staged resolution: %w", err))
	}
	if staged.unmerged || !slices.Equal(staged.changedPaths, resolution.ChangedPaths) {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, errors.New("staged resolution differs from durable intent"))
	}
	if err := r.Git.CommitWithoutHooks(ctx, resolution.CommitMessage); err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("commit exact resolution: %w", err))
	}
	head, err = r.Git.RevParse(ctx, "HEAD")
	if err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("capture committed resolution: %w", err))
	}
	head = strings.TrimSpace(head)
	exact, inspectErr := inspectExactResolutionCommit(ctx, r.Git, request.Intent, head)
	if inspectErr != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("inspect committed resolution: %w", inspectErr))
	}
	if !exact {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("%w: committed resolution does not match durable intent", ErrSingleResolutionDrift))
	}
	return r.advanceCommitted(request.Intent, head, false)
}

func (r GuardedSingleConflictResolver) advanceCommitted(intent plan.SingleMergeCommitIntent, head string, recovered bool) (SingleResolutionSettlement, error) {
	settlement := SingleResolutionSettlement{Intent: intent, Head: head, Recovered: recovered}
	if intent.Resolution.Phase == plan.SingleMergeResolutionPhaseCommitted {
		return settlement, nil
	}
	committed := *intent.Resolution
	committed.Phase = plan.SingleMergeResolutionPhaseCommitted
	committed.IntegrationHead = head
	committed.CommittedAt = r.timestamp(committed.ResolvedAt)
	if err := r.Recorder.AdvanceSingleMergeResolution(intent, committed); err != nil {
		return settlement, fmt.Errorf("persist committed resolution evidence: %w", err)
	}
	settlement.Intent.Resolution = &committed
	return settlement, nil
}

func inspectExactResolutionCommit(ctx context.Context, git GitClient, intent plan.SingleMergeCommitIntent, head string) (bool, error) {
	if intent.Resolution == nil {
		return false, errors.New("resolution evidence is missing")
	}
	parent, parentErr := git.RevParse(ctx, head+"^")
	message, messageErr := git.CommitMessage(ctx, head)
	if parentErr != nil || messageErr != nil {
		return false, errors.Join(parentErr, messageErr)
	}
	if strings.TrimSpace(parent) != intent.DefaultParent || strings.TrimSpace(message) != intent.Resolution.CommitMessage {
		return false, nil
	}
	states, err := git.CommitPathStates(ctx, intent.DefaultParent, head)
	if err != nil {
		return false, err
	}
	paths := make([]string, len(states))
	for i := range states {
		paths[i] = states[i].Path
	}
	if !slices.Equal(paths, intent.Resolution.ChangedPaths) {
		return false, nil
	}
	fingerprint, err := resolutionCommitContentFingerprint(states)
	if err != nil {
		return false, err
	}
	return fingerprint == intent.Resolution.ContentFingerprint, nil
}

func markerSizesByPath(paths []string, sizes []int) map[string]int {
	byPath := make(map[string]int, len(paths))
	for i, path := range paths {
		if i < len(sizes) {
			byPath[path] = sizes[i]
		}
	}
	return byPath
}

func resolutionCommitContentFingerprint(states []gitops.CommitPathState) (string, error) {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "tao.aggregate-rework-content.v2\x00")
	for i, state := range states {
		if state.Path == "" || (i > 0 && states[i-1].Path >= state.Path) {
			return "", errors.New("committed resolution paths are not unique and sorted")
		}
		writeAggregateFingerprintField(hash, state.Path)
		switch state.Mode {
		case "":
			writeAggregateFingerprintField(hash, "missing")
		case "100644", "100755":
			writeAggregateFingerprintField(hash, "regular")
			writeAggregateFingerprintField(hash, strconv.FormatBool(state.Mode == "100755"))
			writeAggregateFingerprintField(hash, state.ContentFingerprint)
		case "120000":
			writeAggregateFingerprintField(hash, "symlink")
			writeAggregateFingerprintField(hash, state.ContentFingerprint)
		default:
			return "", fmt.Errorf("committed resolution path %q has unsupported Git mode %s", state.Path, state.Mode)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (r GuardedSingleConflictResolver) reject(ctx context.Context, result SingleResolutionResult, intent plan.SingleMergeCommitIntent, boundary worktreePathSnapshot, cause error) (SingleResolutionResult, error) {
	return result, fmt.Errorf("%w: %w", ErrSingleResolutionRejected, errors.Join(cause, restoreSingleResolutionBoundary(ctx, r.Git, intent, boundary)))
}

func (r GuardedSingleConflictResolver) rejectAndRearm(ctx context.Context, result SingleResolutionResult, request SingleResolutionRequest, boundary worktreePathSnapshot, capability plan.SingleMergeStartupCapability, acceptance string, cause error) (SingleResolutionResult, error) {
	consumed := func(extra error) (SingleResolutionResult, error) {
		return result, fmt.Errorf("%w: %w", ErrSingleResolutionRejected, &SingleResolutionStartupError{
			Capability: capability, PromptAcceptance: acceptance,
			Authority: SingleResolutionAuthorityConsumed, Cause: errors.Join(cause, extra),
		})
	}
	if err := restoreSingleResolutionBoundary(ctx, r.Git, result.Intent, boundary); err != nil {
		return consumed(fmt.Errorf("restore durable parent: %w", err))
	}
	if err := inspectRequestedRearmBoundary(ctx, r.Git, result.Intent, request.SourceBranch); err != nil {
		return consumed(fmt.Errorf("prove restored request boundary: %w", err))
	}
	failure := plan.SingleMergeStartupFailure{
		Capability: capability, PromptAcceptance: acceptance,
		FailedAt: r.timestamp(result.Intent.Resolution.RequestedAt),
	}
	if err := r.Recorder.RearmSingleMergeResolution(result.Intent, failure); err != nil {
		return consumed(fmt.Errorf("persist exact request rearm: %w", err))
	}
	result.Intent.Resolution = nil
	return result, fmt.Errorf("%w: %w", ErrSingleResolutionRejected, &SingleResolutionStartupError{
		Capability: capability, PromptAcceptance: acceptance,
		Authority: SingleResolutionAuthorityRearmed, Cause: cause,
	})
}

func inspectRequestedRearmBoundary(ctx context.Context, git GitClient, intent plan.SingleMergeCommitIntent, sourceBranch string) error {
	sourceBranch, err := cleanProtectedBranch(sourceBranch)
	if err != nil {
		return err
	}
	liveSource, sourceErr := git.RevParse(ctx, "refs/heads/"+sourceBranch)
	liveDefault, defaultErr := git.RevParse(ctx, "refs/heads/"+intent.DefaultBranch)
	head, headErr := git.RevParse(ctx, "HEAD")
	branch, branchErr := git.CurrentBranch(ctx)
	status, statusErr := git.StatusPorcelain(ctx)
	if sourceErr != nil || defaultErr != nil || headErr != nil || branchErr != nil || statusErr != nil {
		return errors.Join(sourceErr, defaultErr, headErr, branchErr, statusErr)
	}
	if strings.TrimSpace(liveSource) != intent.SourceHead || strings.TrimSpace(liveDefault) != intent.DefaultParent || strings.TrimSpace(head) != intent.DefaultParent || strings.TrimSpace(branch) != intent.DefaultBranch || status != "" {
		return ErrSingleResolutionDrift
	}
	return nil
}

func (r GuardedSingleConflictResolver) settlementReject(ctx context.Context, intent plan.SingleMergeCommitIntent, boundary worktreePathSnapshot, cause error) error {
	return errors.Join(cause, restoreSingleResolutionBoundary(ctx, r.Git, intent, boundary))
}

func restoreSingleResolutionBoundary(ctx context.Context, git GitClient, intent plan.SingleMergeCommitIntent, boundary worktreePathSnapshot) error {
	return restoreSingleAgentBoundary(ctx, git, intent.DefaultBranch, intent.DefaultParent, boundary)
}

func validateSingleResolutionRoot(git GitClient, root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("integration root is required")
	}
	want, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve integration root: %w", err)
	}
	got, err := filepath.Abs(git.Root())
	if err != nil || filepath.Clean(got) != filepath.Clean(want) {
		return fmt.Errorf("git root %q does not match integration root %q", git.Root(), root)
	}
	return nil
}

func cleanProtectedBranch(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.ContainsAny(branch, "\r\n") || strings.HasPrefix(branch, "refs/") {
		return "", errors.New("source branch is invalid")
	}
	return branch, nil
}

func normalizeResolutionPaths(paths []string, required bool) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path, err := validatePorcelainPath(path)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	normalized = slices.Compact(normalized)
	if required && len(normalized) == 0 {
		return nil, errors.New("at least one path is required")
	}
	if unsafeResolutionPaths(normalized) {
		return nil, errors.New("unsafe metadata or repository-external path")
	}
	return normalized, nil
}

func pathsOutsideResolutionScope(changed, source, conflicts []string) []string {
	allowed := make(map[string]struct{}, len(source)+len(conflicts))
	for _, path := range source {
		allowed[path] = struct{}{}
	}
	for _, path := range conflicts {
		allowed[path] = struct{}{}
	}
	unexpected := make([]string, 0)
	for _, path := range changed {
		if _, ok := allowed[path]; !ok {
			unexpected = append(unexpected, path)
		}
	}
	return unexpected
}

func singleResolutionValidationError(validation agentEditValidation) error {
	switch validation.issue {
	case agentEditIssueUnsafePaths:
		return errors.New("resolver made unsafe metadata or repository-external edits")
	case agentEditIssueNoChanges:
		return errors.New("resolver left no edits")
	case agentEditIssueConflictMarkers:
		return fmt.Errorf("resolver left conflict markers in %s", strings.Join(validation.markerPaths, ", "))
	case agentEditIssueUnscannablePaths:
		return fmt.Errorf("resolver edits could not be safely scanned: %w", validation.scanErr)
	default:
		return nil
	}
}

func (r GuardedSingleConflictResolver) timestamp(notBefore time.Time) time.Time {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	now = now.UTC()
	if now.Before(notBefore) {
		return notBefore.UTC()
	}
	return now
}
