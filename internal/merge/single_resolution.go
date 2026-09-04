package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	beforeFingerprint, err := resolutionContentFingerprint(r.Git.Root(), beforeChanges.changedPaths)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("fingerprint prepared conflict: %w", err))
	}
	initialMarkerSizes, err := effectiveConflictMarkerSizes(ctx, r.Git.Root(), conflictFiles)
	if err != nil {
		return reject(result, request.Intent, fmt.Errorf("capture prepared conflict-marker sizes: %w", err))
	}
	originalMarkerSizes := markerSizesByPath(conflictFiles, initialMarkerSizes)

	prompt, err := prompts.RenderMergeResolve(prompts.MergeResolveData{
		Operation: "single-plan squash conflict resolution", PlanID: request.Intent.PlanID,
		SourceHead: request.Intent.SourceHead, IntegrationBase: request.Intent.DefaultParent,
		VerifyCommand: request.VerifyCommand, PlanBrief: request.PlanTitle, SourceReview: request.SourceReview,
		Diff: strings.Join(changedContext, "\n"), ConflictFiles: strings.Join(conflictFiles, "\n") + "\n" + request.ConflictStatus,
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
		IntegrationRoot: request.IntegrationRoot, Prompt: prompt, CandidatePlanID: request.Intent.PlanID,
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
	afterFingerprint, err := resolutionContentFingerprint(r.Git.Root(), afterChanges.changedPaths)
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
	changes, err := concretePorcelainChanges(ctx, r.Git)
	if err != nil {
		return settlement, r.settlementReject(ctx, request.Intent, boundary, fmt.Errorf("inspect durable resolution edits: %w", err))
	}
	// These checks precede Tao's staging. Mismatches may be intervening user
	// work after a crash, so refuse them without running destructive rollback.
	if !slices.Equal(changes.changedPaths, resolution.ChangedPaths) {
		return settlement, fmt.Errorf("%w: changed path set differs", ErrSingleResolutionDrift)
	}
	fingerprint, err := resolutionContentFingerprint(r.Git.Root(), changes.changedPaths)
	if err != nil {
		return settlement, fmt.Errorf("%w: inspect content: %w", ErrSingleResolutionDrift, err)
	}
	if fingerprint != resolution.ContentFingerprint {
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
	if err := r.Git.Add(ctx, changes.stagePaths...); err != nil {
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
	if strings.TrimSpace(liveSource) != intent.SourceHead || strings.TrimSpace(liveDefault) != intent.DefaultParent || strings.TrimSpace(head) != intent.DefaultParent || strings.TrimSpace(branch) != intent.DefaultBranch || strings.TrimSpace(status) != "" {
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

const (
	maxWorktreeBoundaryPaths       = 250_000
	maxWorktreeBoundaryBackupBytes = int64(1 << 30)
)

type worktreePathState struct {
	exists       bool
	mode         fs.FileMode
	content      string
	fingerprint  string
	backingStart int64
	contentSize  int64
	backed       bool
}

type worktreeSnapshotLimits struct {
	maxPaths       int
	maxBackupBytes int64
}

type worktreeSnapshotBacking struct {
	file *os.File
	size int64
}

func newWorktreeSnapshotBacking(pattern string) (*worktreeSnapshotBacking, error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, err
	}
	name := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return nil, fmt.Errorf("secure temporary backing: %w", err)
	}
	// Provider sessions run as the current user. Keeping a discoverable name in
	// the shared temporary directory would therefore let the child replace the
	// rollback bytes. The open descriptor is close-on-exec; unlink its name before
	// any untrusted session starts and retain only the descriptor.
	if err := os.Remove(name); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return nil, fmt.Errorf("unlink temporary backing: %w", err)
	}
	return &worktreeSnapshotBacking{file: file}, nil
}

func (b *worktreeSnapshotBacking) cleanup() {
	if b != nil && b.file != nil {
		_ = b.file.Close()
	}
}

func (b *worktreeSnapshotBacking) verify(stateSets ...map[string]worktreePathState) error {
	hasBackedSection := false
	for _, states := range stateSets {
		for _, state := range states {
			hasBackedSection = hasBackedSection || state.backed
		}
	}
	if !hasBackedSection {
		return nil
	}
	if b == nil || b.file == nil {
		return errors.New("temporary snapshot backing is unavailable")
	}
	info, err := b.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect temporary snapshot backing: %w", err)
	}
	if info.Size() != b.size {
		return fmt.Errorf("temporary snapshot backing size changed: got %d, want %d", info.Size(), b.size)
	}
	for _, states := range stateSets {
		for _, state := range states {
			if !state.backed {
				continue
			}
			if state.backingStart < 0 || state.contentSize < 0 || state.backingStart > b.size-state.contentSize {
				return errors.New("temporary snapshot backing section is out of bounds")
			}
			hash := sha256.New()
			copied, err := io.Copy(hash, io.NewSectionReader(b.file, state.backingStart, state.contentSize))
			if err != nil {
				return fmt.Errorf("verify temporary snapshot backing section: %w", err)
			}
			if copied != state.contentSize {
				return errors.New("temporary snapshot backing section is truncated")
			}
			if hex.EncodeToString(hash.Sum(nil)) != state.fingerprint {
				return errors.New("temporary snapshot backing fingerprint changed")
			}
		}
	}
	return nil
}

type worktreePathSnapshot struct {
	paths        map[string]struct{}
	ignored      map[string]worktreePathState
	ignoredRoots []string
	preserved    map[string]worktreePathState
	backing      *worktreeSnapshotBacking
}

type gitRefState struct {
	object string
	symref string
}

type gitMetadataScope struct {
	path      string
	recursive bool
}

type gitObjectDatabaseGuard struct {
	root string
}

type linkedCheckoutSnapshot struct {
	root          string
	rootMode      fs.FileMode
	paths         map[string]worktreePathState
	excludedRoots []string
}

type gitSessionBoundary struct {
	root                    string
	gitDir                  string
	commonDir               string
	refs                    map[string]gitRefState
	metadata                map[string]worktreePathState
	scopes                  []gitMetadataScope
	nestedControlRoots      []string
	nestedControlExclusions []string
	nestedControls          map[string]worktreePathState
	linkedCheckouts         []linkedCheckoutSnapshot
	objects                 *gitObjectDatabaseGuard
	backing                 *worktreeSnapshotBacking
}

func (s gitSessionBoundary) cleanup() {
	s.backing.cleanup()
}

func (s worktreePathSnapshot) cleanup() {
	s.backing.cleanup()
}

func (s gitSessionBoundary) protectedGitWritePaths() []string {
	paths := make([]string, 0, len(s.scopes)+len(s.nestedControlRoots)+len(s.linkedCheckouts)+1)
	for _, scope := range s.scopes {
		paths = append(paths, scope.path)
	}
	paths = append(paths, s.nestedControlRoots...)
	for _, checkout := range s.linkedCheckouts {
		paths = append(paths, checkout.root)
	}
	paths = append(paths, s.objects.root)
	return paths
}

func snapshotGitSessionBoundary(ctx context.Context, root string) (snapshot gitSessionBoundary, err error) {
	root, err = filepath.Abs(root)
	if err != nil {
		return snapshot, fmt.Errorf("resolve worktree root: %w", err)
	}
	gitDir, err := gitBoundaryPath(ctx, root, "--absolute-git-dir")
	if err != nil {
		return snapshot, fmt.Errorf("resolve active gitdir: %w", err)
	}
	commonDir, err := gitBoundaryPath(ctx, root, "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return snapshot, fmt.Errorf("resolve common gitdir: %w", err)
	}
	scopes := []gitMetadataScope{
		{path: filepath.Join(root, ".git")},
		{path: gitDir, recursive: true},
	}
	if filepath.Clean(commonDir) != filepath.Clean(gitDir) {
		scopes = append(scopes, gitMetadataScope{path: commonDir, recursive: true})
	}
	// The potentially unbounded object database is protected separately by a
	// read-only session guard rather than copied into rollback storage. Retain its
	// mutable administrative info tree in the exact metadata snapshot as well.
	scopes = append(scopes, gitMetadataScope{path: filepath.Join(commonDir, "objects", "info"), recursive: true})
	backing, err := newWorktreeSnapshotBacking("tao-git-boundary-*")
	if err != nil {
		return snapshot, fmt.Errorf("create Git metadata backing: %w", err)
	}
	snapshot = gitSessionBoundary{
		root: root, gitDir: gitDir, commonDir: commonDir, scopes: scopes,
		metadata:       make(map[string]worktreePathState),
		nestedControls: make(map[string]worktreePathState),
		backing:        backing,
	}
	complete := false
	defer func() {
		if !complete {
			snapshot.cleanup()
			snapshot = gitSessionBoundary{}
		}
	}()
	paths, err := gitMetadataPaths(ctx, snapshot)
	if err != nil {
		return snapshot, err
	}
	for _, path := range paths {
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			snapshot.metadata[path] = worktreePathState{}
			continue
		}
		if statErr != nil {
			return snapshot, fmt.Errorf("inspect Git metadata %s: %w", path, statErr)
		}
		state, stateErr := backUpWorktreePathState(ctx, path, fileInfoDirEntry{info}, snapshot.backing, maxWorktreeBoundaryBackupBytes)
		if stateErr != nil {
			return snapshot, fmt.Errorf("snapshot Git metadata %s: %w", path, stateErr)
		}
		snapshot.metadata[path] = state
	}
	checkoutRoots, err := linkedCheckoutRoots(ctx, root)
	if err != nil {
		return snapshot, err
	}
	canonicalRoot, err := canonicalWorktreeRoot(root)
	if err != nil {
		return snapshot, err
	}
	for _, checkoutRoot := range checkoutRoots {
		if checkoutRoot != canonicalRoot && pathWithinConfinementRoot(checkoutRoot, canonicalRoot) {
			snapshot.nestedControlExclusions = append(snapshot.nestedControlExclusions, checkoutRoot)
		}
	}
	nestedScopes, err := nestedGitControlScopes(ctx, root, snapshot.nestedControlExclusions)
	if err != nil {
		return snapshot, err
	}
	for _, scope := range nestedScopes {
		snapshot.nestedControlRoots = append(snapshot.nestedControlRoots, scope.path)
	}
	nestedPaths, err := gitMetadataScopePaths(ctx, nestedScopes, "")
	if err != nil {
		return snapshot, fmt.Errorf("snapshot nested Git controls: %w", err)
	}
	if len(snapshot.metadata)+len(nestedPaths) > maxWorktreeBoundaryPaths {
		return snapshot, fmt.Errorf("git metadata snapshot exceeds %d-path limit", maxWorktreeBoundaryPaths)
	}
	for _, path := range nestedPaths {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return snapshot, fmt.Errorf("nested Git control %q escapes the integration worktree", path)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return snapshot, fmt.Errorf("inspect nested Git control %s: %w", path, statErr)
		}
		state, stateErr := backUpWorktreePathState(ctx, path, fileInfoDirEntry{info}, snapshot.backing, maxWorktreeBoundaryBackupBytes)
		if stateErr != nil {
			return snapshot, fmt.Errorf("snapshot nested Git control %s: %w", path, stateErr)
		}
		snapshot.nestedControls[filepath.ToSlash(rel)] = state
	}
	snapshot.refs, err = snapshotAllGitRefs(ctx, root)
	if err != nil {
		return snapshot, err
	}
	snapshot.linkedCheckouts, err = snapshotLinkedCheckoutFilesystems(ctx, root, checkoutRoots, snapshot.backing, len(snapshot.metadata)+len(snapshot.nestedControls))
	if err != nil {
		return snapshot, err
	}
	objectRoot := filepath.Join(commonDir, "objects")
	info, err := os.Stat(objectRoot)
	if err != nil {
		return snapshot, fmt.Errorf("inspect Git object database: %w", err)
	}
	if !info.IsDir() {
		return snapshot, errors.New("inspect Git object database: object root is not a directory")
	}
	// Writes are denied only in the provider subprocess. Tao must never change
	// host modes here: an interrupted or overlapping plan session could otherwise
	// leave the shared object database unwritable or restore another session's
	// temporary mode as the repository's final mode.
	snapshot.objects = &gitObjectDatabaseGuard{root: objectRoot}
	complete = true
	return snapshot, nil
}

func snapshotLinkedCheckoutFilesystems(ctx context.Context, integrationRoot string, roots []string, backing *worktreeSnapshotBacking, pathCount int) ([]linkedCheckoutSnapshot, error) {
	integrationRoot, err := canonicalWorktreeRoot(integrationRoot)
	if err != nil {
		return nil, err
	}
	snapshots := make([]linkedCheckoutSnapshot, 0, max(0, len(roots)-1))
	for _, root := range roots {
		if root == integrationRoot {
			continue
		}
		info, statErr := os.Lstat(root)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect linked checkout %s: %w", root, statErr)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("linked checkout root %s is not a directory", root)
		}
		excluded := nestedCheckoutRoots(root, roots)
		states, walkErr := snapshotCheckoutPathStates(ctx, root, excluded, backing, &pathCount)
		if walkErr != nil {
			return nil, fmt.Errorf("snapshot linked checkout %s: %w", root, walkErr)
		}
		snapshots = append(snapshots, linkedCheckoutSnapshot{root: root, rootMode: info.Mode(), paths: states, excludedRoots: excluded})
	}
	return snapshots, nil
}

func linkedCheckoutRoots(ctx context.Context, root string) ([]string, error) {
	output, err := runGitBoundaryCommand(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("list linked checkouts: %w", err)
	}
	roots := make([]string, 0)
	seen := make(map[string]struct{})
	for field := range strings.SplitSeq(output, "\x00") {
		path, ok := strings.CutPrefix(field, "worktree ")
		if !ok {
			continue
		}
		path, err = canonicalWorktreeRoot(path)
		if err != nil {
			return nil, fmt.Errorf("resolve linked checkout %q: %w", path, err)
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		roots = append(roots, path)
	}
	if len(roots) == 0 {
		return nil, errors.New("git reported no linked checkout roots")
	}
	sort.Strings(roots)
	return roots, nil
}

func canonicalWorktreeRoot(root string) (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	return filepath.Clean(root), nil
}

func nestedGitControlScopes(ctx context.Context, root string, excludedRoots []string) ([]gitMetadataScope, error) {
	roots, err := nestedGitControlRoots(ctx, root, excludedRoots)
	if err != nil {
		return nil, err
	}
	scopes := make([]gitMetadataScope, 0, len(roots))
	for _, path := range roots {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect nested Git control %s: %w", path, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("nested Git control %s is neither a regular file nor directory", path)
		}
		scopes = append(scopes, gitMetadataScope{path: path, recursive: info.IsDir()})
	}
	return scopes, nil
}

// nestedGitControlRoots discovers control paths without interpreting their
// filesystem type. This lets cleanup attribute provider-created unsupported
// entries before strict metadata inspection rejects them.
func nestedGitControlRoots(ctx context.Context, root string, excludedRoots []string) ([]string, error) {
	topLevel := filepath.Clean(filepath.Join(root, ".git"))
	roots := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		cleanPath := filepath.Clean(path)
		if cleanPath == topLevel || slices.Contains(excludedRoots, cleanPath) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root || entry.Name() != ".git" {
			return nil
		}
		roots = append(roots, cleanPath)
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover nested Git controls: %w", err)
	}
	sort.Strings(roots)
	return roots, nil
}

func nestedCheckoutRoots(root string, roots []string) []string {
	prefix := root + string(filepath.Separator)
	nested := make([]string, 0)
	for _, candidate := range roots {
		if candidate != root && strings.HasPrefix(candidate, prefix) {
			nested = append(nested, candidate)
		}
	}
	sort.Strings(nested)
	return nested
}

func snapshotCheckoutPathStates(ctx context.Context, root string, excludedRoots []string, backing *worktreeSnapshotBacking, pathCount *int) (map[string]worktreePathState, error) {
	states := make(map[string]worktreePathState)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if slices.Contains(excludedRoots, filepath.Clean(path)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		(*pathCount)++
		if *pathCount > maxWorktreeBoundaryPaths {
			return fmt.Errorf("linked checkout snapshot exceeds %d-path limit", maxWorktreeBoundaryPaths)
		}
		state, err := backUpWorktreePathState(ctx, path, entry, backing, maxWorktreeBoundaryBackupBytes)
		if err != nil {
			return err
		}
		states[rel] = state
		return nil
	})
	return states, err
}

func gitBoundaryPath(ctx context.Context, root string, args ...string) (string, error) {
	output, err := runGitBoundaryCommand(ctx, root, append([]string{"rev-parse"}, args...)...)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(output)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Clean(path), nil
}

func runGitBoundaryCommand(ctx context.Context, root string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...) //nolint:gosec // fixed Git executable and internally constructed boundary arguments.
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func gitMetadataPaths(ctx context.Context, boundary gitSessionBoundary) ([]string, error) {
	return gitMetadataScopePaths(ctx, boundary.scopes, boundary.commonDir)
}

func gitMetadataScopePaths(ctx context.Context, scopes []gitMetadataScope, commonDir string) ([]string, error) {
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	add := func(path string) error {
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			return nil
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		if len(paths) > maxWorktreeBoundaryPaths {
			return fmt.Errorf("git metadata snapshot exceeds %d-path limit", maxWorktreeBoundaryPaths)
		}
		return nil
	}
	for _, scope := range scopes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Lstat(scope.path)
		if errors.Is(err, os.ErrNotExist) {
			if err := add(scope.path); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if !scope.recursive || !info.IsDir() {
			if err := add(scope.path); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(scope.path, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if path != scope.path && commonDir != "" && filepath.Clean(scope.path) == filepath.Clean(commonDir) {
				rel, relErr := filepath.Rel(scope.path, path)
				if relErr != nil {
					return relErr
				}
				first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
				if first == "objects" {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			return add(path)
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func snapshotAllGitRefs(ctx context.Context, root string) (map[string]gitRefState, error) {
	output, err := runGitBoundaryCommand(ctx, root, "for-each-ref", "--format=%(refname)%09%(objectname)%09%(symref)")
	if err != nil {
		return nil, fmt.Errorf("snapshot all refs: %w", err)
	}
	refs := make(map[string]gitRefState)
	for line := range strings.SplitSeq(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || !strings.HasPrefix(fields[0], "refs/") || fields[1] == "" {
			return nil, fmt.Errorf("parse ref snapshot line %q", line)
		}
		refs[fields[0]] = gitRefState{object: fields[1], symref: fields[2]}
	}
	return refs, nil
}

const singleAgentCleanupTimeout = 30 * time.Second

func singleAgentCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), singleAgentCleanupTimeout)
}

func compareGitSessionBoundary(ctx context.Context, before gitSessionBoundary) error {
	return errors.Join(compareGitSessionBoundaryExceptNested(ctx, before), compareNestedGitControls(ctx, before))
}

func compareGitSessionBoundaryExceptNested(ctx context.Context, before gitSessionBoundary) error {
	stateSets := make([]map[string]worktreePathState, 0, len(before.linkedCheckouts)+2)
	stateSets = append(stateSets, before.metadata, before.nestedControls)
	for _, checkout := range before.linkedCheckouts {
		stateSets = append(stateSets, checkout.paths)
	}
	if err := before.backing.verify(stateSets...); err != nil {
		return fmt.Errorf("safety-critical temporary Git boundary backing changed: %w", err)
	}
	paths, inspectErr := gitMetadataPaths(ctx, before)
	changed := inspectErr != nil
	if inspectErr == nil {
		if len(paths) != len(before.metadata) {
			changed = true
		} else {
			var fingerprinted int64
			for _, path := range paths {
				want, ok := before.metadata[path]
				if !ok {
					changed = true
					break
				}
				info, err := os.Lstat(path)
				if errors.Is(err, os.ErrNotExist) {
					changed = changed || want.exists
					continue
				}
				if err != nil {
					inspectErr = errors.Join(inspectErr, err)
					changed = true
					continue
				}
				got, size, err := fingerprintWorktreePathState(ctx, path, fileInfoDirEntry{info}, maxWorktreeBoundaryBackupBytes-fingerprinted)
				if err != nil {
					inspectErr = errors.Join(inspectErr, err)
					changed = true
					continue
				}
				fingerprinted += size
				changed = changed || !sameWorktreePathState(got, want)
			}
		}
	}
	afterRefs, refsErr := snapshotAllGitRefs(ctx, before.root)
	if refsErr == nil && !reflect.DeepEqual(before.refs, afterRefs) {
		refsErr = errors.New("git ref set changed outside Tao's mutation authority; preserving current refs")
	}
	checkoutErr := compareLinkedCheckouts(ctx, before)
	var mutation error
	if changed {
		mutation = errors.New("safety-critical Git metadata changed outside Tao's mutation authority; preserving current metadata")
	}
	return errors.Join(mutation, inspectErr, refsErr, checkoutErr)
}

func compareNestedGitControls(ctx context.Context, before gitSessionBoundary) error {
	_, err := inspectNestedGitControls(ctx, before)
	return err
}

// inspectNestedGitControls distinguishes controls created in the provider's
// writable view from drift in controls that were mounted read-only. A non-empty
// created result is returned only when every pre-existing control is unchanged,
// making those newly discovered roots safe for Tao to remove.
func inspectNestedGitControls(ctx context.Context, before gitSessionBoundary) ([]string, error) {
	if err := before.backing.verify(before.nestedControls); err != nil {
		return nil, fmt.Errorf("verify nested Git control snapshot: %w", err)
	}
	currentControlRoots, err := nestedGitControlRoots(ctx, before.root, before.nestedControlExclusions)
	if err != nil {
		return nil, err
	}
	beforeRoots := make(map[string]struct{}, len(before.nestedControlRoots))
	for _, root := range before.nestedControlRoots {
		beforeRoots[filepath.Clean(root)] = struct{}{}
	}
	created := make([]string, 0)
	for _, root := range currentControlRoots {
		if _, ok := beforeRoots[filepath.Clean(root)]; !ok {
			created = append(created, filepath.Clean(root))
		}
	}

	// Exclude only transaction-created roots from strict type inspection. Any
	// unsupported pre-existing root still fails as concurrent drift and is never
	// returned as safe cleanup authority.
	inspectionExclusions := append([]string(nil), before.nestedControlExclusions...)
	inspectionExclusions = append(inspectionExclusions, created...)
	scopes, err := nestedGitControlScopes(ctx, before.root, inspectionExclusions)
	if err != nil {
		return nil, err
	}
	currentRoots := make(map[string]gitMetadataScope, len(scopes))
	for _, scope := range scopes {
		currentRoots[filepath.Clean(scope.path)] = scope
	}

	preexistingScopes := make([]gitMetadataScope, 0, len(before.nestedControlRoots))
	for _, root := range before.nestedControlRoots {
		scope, ok := currentRoots[filepath.Clean(root)]
		if !ok {
			return nil, errors.New("pre-existing nested Git control set changed outside Tao's mutation authority")
		}
		rel, relErr := filepath.Rel(before.root, root)
		want, stateOK := before.nestedControls[filepath.ToSlash(rel)]
		if relErr != nil || !stateOK || scope.recursive != want.mode.IsDir() {
			return nil, errors.New("pre-existing nested Git control set changed outside Tao's mutation authority")
		}
		preexistingScopes = append(preexistingScopes, scope)
	}
	paths, err := gitMetadataScopePaths(ctx, preexistingScopes, "")
	if err != nil {
		return nil, fmt.Errorf("inspect pre-existing nested Git controls: %w", err)
	}
	if len(paths) != len(before.nestedControls) {
		return nil, errors.New("pre-existing nested Git control metadata changed outside Tao's mutation authority")
	}
	var fingerprinted int64
	for _, path := range paths {
		rel, relErr := filepath.Rel(before.root, path)
		if relErr != nil {
			return nil, relErr
		}
		want, ok := before.nestedControls[filepath.ToSlash(rel)]
		if !ok {
			return nil, errors.New("pre-existing nested Git control metadata changed outside Tao's mutation authority")
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, fmt.Errorf("inspect pre-existing nested Git control %s: %w", path, statErr)
		}
		got, size, fingerprintErr := fingerprintWorktreePathState(ctx, path, fileInfoDirEntry{info}, maxWorktreeBoundaryBackupBytes-fingerprinted)
		if fingerprintErr != nil {
			return nil, fmt.Errorf("inspect pre-existing nested Git control %s: %w", path, fingerprintErr)
		}
		fingerprinted += size
		if !sameWorktreePathState(got, want) {
			return nil, errors.New("pre-existing nested Git control metadata changed outside Tao's mutation authority")
		}
	}
	if len(created) > 0 {
		return created, errors.New("nested Git controls were created during the provider transaction")
	}
	return nil, nil
}

func removeCreatedNestedGitControls(ctx context.Context, before gitSessionBoundary, roots []string) error {
	beforeRoots := make(map[string]struct{}, len(before.nestedControlRoots))
	for _, root := range before.nestedControlRoots {
		beforeRoots[filepath.Clean(root)] = struct{}{}
	}
	roots = append([]string(nil), roots...)
	sort.Slice(roots, func(i, j int) bool {
		if len(roots[i]) == len(roots[j]) {
			return roots[i] > roots[j]
		}
		return len(roots[i]) > len(roots[j])
	})
	roots = slices.Compact(roots)
	var removeErrs []error
	for _, path := range roots {
		if err := ctx.Err(); err != nil {
			removeErrs = append(removeErrs, err)
			break
		}
		path = filepath.Clean(path)
		if _, existed := beforeRoots[path]; existed {
			removeErrs = append(removeErrs, fmt.Errorf("refuse to remove pre-existing nested Git control: %s", path))
			continue
		}
		rel, err := filepath.Rel(before.root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			removeErrs = append(removeErrs, fmt.Errorf("refuse to remove nested Git control outside integration worktree: %s", path))
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			removeErrs = append(removeErrs, fmt.Errorf("remove transaction-created nested Git control %s: %w", path, err))
		}
	}
	return errors.Join(removeErrs...)
}

func compareLinkedCheckouts(ctx context.Context, before gitSessionBoundary) error {
	var errs []error
	fingerprintedBytes := int64(0)
	pathCount := 0
	for _, checkout := range before.linkedCheckouts {
		current, rootMode, inspectErr := fingerprintCheckoutPathStates(ctx, checkout, &pathCount, &fingerprintedBytes)
		if inspectErr == nil && rootMode == checkout.rootMode && sameCheckoutPathStates(current, checkout.paths) {
			continue
		}
		errs = append(errs, errors.Join(
			fmt.Errorf("linked checkout filesystem changed outside Tao's mutation authority; preserving current contents: %s", checkout.root),
			inspectErr,
		))
	}
	return errors.Join(errs...)
}

func fingerprintCheckoutPathStates(ctx context.Context, checkout linkedCheckoutSnapshot, pathCount *int, fingerprintedBytes *int64) (map[string]worktreePathState, fs.FileMode, error) {
	info, err := os.Lstat(checkout.root)
	if err != nil {
		return nil, 0, err
	}
	if !info.IsDir() {
		return nil, info.Mode(), fmt.Errorf("linked checkout root is not a directory")
	}
	states := make(map[string]worktreePathState)
	err = filepath.WalkDir(checkout.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == checkout.root {
			return nil
		}
		if slices.Contains(checkout.excludedRoots, filepath.Clean(path)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(checkout.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		(*pathCount)++
		if *pathCount > maxWorktreeBoundaryPaths {
			return fmt.Errorf("linked checkout inspection exceeds %d-path limit", maxWorktreeBoundaryPaths)
		}
		// Record the path before reading it so restoration can still remove a
		// newly created oversized or unreadable entry when inspection fails.
		states[rel] = worktreePathState{mode: entry.Type()}
		state, size, err := fingerprintWorktreePathState(ctx, path, entry, maxWorktreeBoundaryBackupBytes-*fingerprintedBytes)
		if err != nil {
			return err
		}
		*fingerprintedBytes += size
		states[rel] = state
		return nil
	})
	return states, info.Mode(), err
}

func sameCheckoutPathStates(got, want map[string]worktreePathState) bool {
	if len(got) != len(want) {
		return false
	}
	for path, wantState := range want {
		gotState, ok := got[path]
		if !ok || !sameWorktreePathState(gotState, wantState) {
			return false
		}
	}
	return true
}

func snapshotWorktreePaths(ctx context.Context, git GitClient) (worktreePathSnapshot, error) {
	return snapshotWorktreePathsWithLimits(ctx, git, worktreeSnapshotLimits{
		maxPaths:       maxWorktreeBoundaryPaths,
		maxBackupBytes: maxWorktreeBoundaryBackupBytes,
	})
}

func snapshotWorktreePathsWithLimits(ctx context.Context, git GitClient, limits worktreeSnapshotLimits) (snapshot worktreePathSnapshot, err error) {
	if limits.maxPaths <= 0 || limits.maxBackupBytes <= 0 {
		return worktreePathSnapshot{}, errors.New("worktree snapshot limits must be positive")
	}
	root := filepath.Clean(git.Root())
	ignoredRoots, err := ignoredWorktreeRoots(ctx, git)
	if err != nil {
		return worktreePathSnapshot{}, err
	}
	backing, err := newWorktreeSnapshotBacking("tao-merge-boundary-*")
	if err != nil {
		return worktreePathSnapshot{}, fmt.Errorf("create worktree snapshot backing: %w", err)
	}
	snapshot = worktreePathSnapshot{
		paths:        make(map[string]struct{}),
		ignored:      make(map[string]worktreePathState),
		ignoredRoots: ignoredRoots,
		backing:      backing,
	}
	snapshotBacking := snapshot.backing
	complete := false
	defer func() {
		if !complete {
			snapshotBacking.cleanup()
			snapshot = worktreePathSnapshot{}
		}
	}()
	pathCount := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		pathCount++
		if pathCount > limits.maxPaths {
			return fmt.Errorf("worktree snapshot exceeds %d-path limit", limits.maxPaths)
		}
		snapshot.paths[rel] = struct{}{}
		if !withinIgnoredRoot(rel, ignoredRoots) {
			return nil
		}
		state, err := backUpWorktreePathState(ctx, path, entry, snapshot.backing, limits.maxBackupBytes)
		if err != nil {
			return fmt.Errorf("snapshot ignored path %s: %w", rel, err)
		}
		snapshot.ignored[rel] = state
		return nil
	})
	if err != nil {
		return worktreePathSnapshot{}, err
	}
	complete = true
	return snapshot, nil
}

func ignoredWorktreeRoots(ctx context.Context, git GitClient) ([]string, error) {
	raw, err := git.StatusPorcelainIgnoredV1Z(ctx)
	if err != nil {
		return nil, err
	}
	roots := make([]string, 0)
	for record := range strings.SplitSeq(raw, "\x00") {
		if record == "" || !strings.HasPrefix(record, "!! ") {
			continue
		}
		path := strings.TrimSuffix(record[3:], "/")
		path, err = validatePorcelainPath(path)
		if err != nil {
			return nil, fmt.Errorf("parse ignored path: %w", err)
		}
		clean := filepath.Clean(filepath.FromSlash(path))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".git" || strings.HasPrefix(clean, ".git"+string(filepath.Separator)) {
			return nil, fmt.Errorf("ignored path %q escapes the worktree boundary", path)
		}
		roots = append(roots, filepath.ToSlash(clean))
	}
	sort.Strings(roots)
	return slices.Compact(roots), nil
}

func withinIgnoredRoot(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func backUpWorktreePathState(ctx context.Context, path string, entry fs.DirEntry, backing *worktreeSnapshotBacking, maxBytes int64) (worktreePathState, error) {
	info, err := entry.Info()
	if err != nil {
		return worktreePathState{}, err
	}
	state := worktreePathState{exists: true, mode: info.Mode()}
	switch {
	case info.Mode().IsRegular():
		if backing == nil || backing.file == nil {
			return worktreePathState{}, errors.New("temporary snapshot backing is unavailable")
		}
		start := backing.size
		remaining := maxBytes - start
		fingerprint, size, err := streamWorktreeFile(ctx, path, backing.file, remaining, maxBytes)
		if err != nil {
			truncateErr := backing.file.Truncate(start)
			_, seekErr := backing.file.Seek(start, io.SeekStart)
			return worktreePathState{}, errors.Join(err, truncateErr, seekErr)
		}
		state.fingerprint = fingerprint
		state.backingStart = backing.size
		state.contentSize = size
		state.backed = true
		backing.size += size
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return worktreePathState{}, err
		}
		state.content = target
	case info.IsDir():
	default:
		return worktreePathState{}, fmt.Errorf("unsupported filesystem mode %s", info.Mode())
	}
	return state, nil
}

func fingerprintWorktreePathState(ctx context.Context, path string, entry fs.DirEntry, remainingBytes int64) (worktreePathState, int64, error) {
	info, err := entry.Info()
	if err != nil {
		return worktreePathState{}, 0, err
	}
	state := worktreePathState{exists: true, mode: info.Mode()}
	switch {
	case info.Mode().IsRegular():
		fingerprint, size, err := streamWorktreeFile(ctx, path, nil, remainingBytes, maxWorktreeBoundaryBackupBytes)
		if err != nil {
			return worktreePathState{}, 0, err
		}
		state.fingerprint = fingerprint
		state.contentSize = size
		return state, size, nil
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return worktreePathState{}, 0, err
		}
		state.content = target
	case info.IsDir():
	default:
		return worktreePathState{}, 0, fmt.Errorf("unsupported filesystem mode %s", info.Mode())
	}
	return state, 0, nil
}

func streamWorktreeFile(ctx context.Context, path string, backing io.Writer, remainingBytes, totalLimit int64) (fingerprint string, total int64, err error) {
	if remainingBytes < 0 {
		return "", 0, fmt.Errorf("worktree file backup exceeds %d-byte limit", totalLimit)
	}
	file, err := os.Open(path) //nolint:gosec // path was discovered beneath the selected integration root.
	if err != nil {
		return "", 0, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	hash := sha256.New()
	writer := io.Writer(hash)
	if backing != nil {
		writer = io.MultiWriter(hash, backing)
	}
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", total, err
		}
		n, readErr := file.Read(buffer)
		if int64(n) > remainingBytes-total {
			return "", total, fmt.Errorf("worktree file backup exceeds %d-byte limit", totalLimit)
		}
		if n > 0 {
			written, writeErr := writer.Write(buffer[:n])
			if writeErr != nil {
				return "", total, writeErr
			}
			if written != n {
				return "", total, io.ErrShortWrite
			}
			total += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", total, readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), total, nil
}

var errIgnoredWorktreeChanged = errors.New("ignored worktree changed")

func ignoredWorktreeChanged(ctx context.Context, git GitClient, before worktreePathSnapshot) (bool, error) {
	afterRoots, err := ignoredWorktreeRoots(ctx, git)
	if err != nil {
		return false, err
	}
	roots := append(append([]string(nil), before.ignoredRoots...), afterRoots...)
	sort.Strings(roots)
	roots = slices.Compact(roots)
	root := filepath.Clean(git.Root())
	seen := make(map[string]struct{}, len(before.ignored))
	pathCount := 0
	var fingerprintedBytes int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		pathCount++
		if pathCount > maxWorktreeBoundaryPaths {
			return fmt.Errorf("worktree inspection exceeds %d-path limit", maxWorktreeBoundaryPaths)
		}
		if !withinIgnoredRoot(rel, roots) {
			return nil
		}
		want, existed := before.ignored[rel]
		if !existed {
			if _, existed = before.paths[rel]; !existed {
				return errIgnoredWorktreeChanged
			}
			return nil
		}
		got, size, err := fingerprintWorktreePathState(ctx, path, entry, maxWorktreeBoundaryBackupBytes-fingerprintedBytes)
		if err != nil {
			return fmt.Errorf("fingerprint ignored path %s: %w", rel, err)
		}
		fingerprintedBytes += size
		seen[rel] = struct{}{}
		if !sameWorktreePathState(got, want) {
			return errIgnoredWorktreeChanged
		}
		return nil
	})
	if errors.Is(err, errIgnoredWorktreeChanged) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(seen) != len(before.ignored), nil
}

func sameWorktreePathState(got, want worktreePathState) bool {
	if got.exists != want.exists || got.mode != want.mode {
		return false
	}
	if got.mode.IsRegular() {
		return got.contentSize == want.contentSize && got.fingerprint == want.fingerprint
	}
	return got.content == want.content
}

type fileInfoDirEntry struct{ fs.FileInfo }

func (entry fileInfoDirEntry) Type() fs.FileMode          { return entry.Mode().Type() }
func (entry fileInfoDirEntry) Info() (fs.FileInfo, error) { return entry.FileInfo, nil }

func restoreSingleAgentBoundary(ctx context.Context, git GitClient, branch, head string, boundary worktreePathSnapshot) error {
	if git == nil {
		return errors.New("restore protected boundary: git client is unavailable")
	}
	if err := boundary.backing.verify(boundary.ignored, boundary.preserved); err != nil {
		return fmt.Errorf("refuse rollback with changed temporary backing: %w", err)
	}
	var restoreErrs []error
	if err := removeTransactionCreatedPaths(ctx, git.Root(), boundary.paths); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("remove transaction-created paths: %w", err))
	}
	// Resetting to HEAD first discards tracked edits without moving whichever
	// protected ref an untrusted session may have checked out. Tao then returns
	// to the expected branch before restoring the durable head.
	if err := git.ResetHard(ctx, "HEAD"); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("discard rejected edits: %w", err))
	}
	current, currentErr := git.CurrentBranch(ctx)
	if currentErr != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("inspect branch during rollback: %w", currentErr))
	} else if strings.TrimSpace(current) != branch {
		if err := git.Checkout(ctx, branch); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("checkout %s during rollback: %w", branch, err))
			return errors.Join(restoreErrs...)
		}
	}
	if err := git.ResetHard(ctx, head); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore durable head: %w", err))
	}
	if err := restoreWorktreePathStates(git.Root(), boundary.ignored, boundary.backing); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore ignored paths: %w", err))
	}
	if err := restoreWorktreePathStates(git.Root(), boundary.preserved, boundary.backing); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore pre-existing worktree changes: %w", err))
	}
	return errors.Join(restoreErrs...)
}

func restoreWorktreePathStates(root string, states map[string]worktreePathState, backing *worktreeSnapshotBacking) error {
	backed := false
	for _, state := range states {
		backed = backed || state.backed
	}
	if backed {
		if err := backing.verify(states); err != nil {
			return fmt.Errorf("refuse restoration with changed temporary backing: %w", err)
		}
	}
	paths := make([]string, 0, len(states))
	for path := range states {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return paths[i] < paths[j]
	})
	var restoreErrs []error
	for _, path := range paths {
		state := states[path]
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErrs = append(restoreErrs, fmt.Errorf("inspect %s: %w", path, err))
			continue
		}
		if !state.exists {
			if err == nil {
				if err := os.Remove(fullPath); err != nil {
					restoreErrs = append(restoreErrs, fmt.Errorf("restore deletion %s: %w", path, err))
				}
			}
			continue
		}
		if err == nil && info.Mode().Type() != state.mode.Type() {
			if err := os.Remove(fullPath); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("replace %s: %w", path, err))
				continue
			}
			info = nil
		}
		switch {
		case state.mode.IsDir():
			if info == nil {
				if err := os.Mkdir(fullPath, 0o700); err != nil {
					restoreErrs = append(restoreErrs, fmt.Errorf("create directory %s: %w", path, err))
					continue
				}
			}
			if err := os.Chmod(fullPath, 0o700); err != nil { //nolint:gosec // G302: owner traversal is required temporarily while restoring directory descendants.
				restoreErrs = append(restoreErrs, fmt.Errorf("prepare directory %s: %w", path, err))
			}
		case state.mode.IsRegular():
			if info != nil {
				if err := os.Chmod(fullPath, 0o600); err != nil {
					restoreErrs = append(restoreErrs, fmt.Errorf("prepare file %s: %w", path, err))
					continue
				}
			}
			if err := restoreRegularWorktreeFile(fullPath, state, backing); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore file %s: %w", path, err))
				continue
			}
			if err := os.Chmod(fullPath, state.mode.Perm()); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore mode for %s: %w", path, err))
			}
		case state.mode&os.ModeSymlink != 0:
			if info != nil {
				if err := os.Remove(fullPath); err != nil {
					restoreErrs = append(restoreErrs, fmt.Errorf("replace symlink %s: %w", path, err))
					continue
				}
			}
			if err := os.Symlink(state.content, fullPath); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore symlink %s: %w", path, err))
			}
		}
	}
	for i := len(paths) - 1; i >= 0; i-- {
		path := paths[i]
		state := states[path]
		if state.mode.IsDir() {
			if err := os.Chmod(filepath.Join(root, filepath.FromSlash(path)), state.mode.Perm()); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore directory mode for %s: %w", path, err))
			}
		}
	}
	return errors.Join(restoreErrs...)
}

func restoreRegularWorktreeFile(path string, state worktreePathState, backing *worktreeSnapshotBacking) error {
	if !state.backed {
		return os.WriteFile(path, []byte(state.content), 0o600)
	}
	if backing == nil || backing.file == nil {
		return errors.New("temporary snapshot backing is unavailable")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path was validated beneath the selected integration root.
	if err != nil {
		return err
	}
	copied, copyErr := io.Copy(file, io.NewSectionReader(backing.file, state.backingStart, state.contentSize))
	if copyErr == nil && copied != state.contentSize {
		copyErr = io.ErrUnexpectedEOF
	}
	closeErr := file.Close()
	return errors.Join(copyErr, closeErr)
}

func removeTransactionCreatedPaths(ctx context.Context, root string, boundary map[string]struct{}) error {
	current, err := snapshotAllWorktreePaths(ctx, root)
	if err != nil {
		return err
	}
	created := make([]string, 0)
	for path := range current {
		if _, existed := boundary[path]; !existed {
			created = append(created, path)
		}
	}
	sort.Slice(created, func(i, j int) bool {
		leftDepth := strings.Count(filepath.Clean(created[i]), string(filepath.Separator))
		rightDepth := strings.Count(filepath.Clean(created[j]), string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return created[i] > created[j]
	})
	var removeErrs []error
	for _, path := range created {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErrs = append(removeErrs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(removeErrs...)
}

func snapshotAllWorktreePaths(ctx context.Context, root string) (map[string]struct{}, error) {
	root = filepath.Clean(root)
	paths := make(map[string]struct{})
	pathCount := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		pathCount++
		if pathCount > maxWorktreeBoundaryPaths {
			return fmt.Errorf("worktree snapshot exceeds %d-path limit", maxWorktreeBoundaryPaths)
		}
		paths[rel] = struct{}{}
		return nil
	})
	return paths, err
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

func snapshotPreexistingWorktreeBoundary(ctx context.Context, git GitClient) (_ []string, snapshot *worktreePathSnapshot, err error) {
	changes, err := concretePorcelainChanges(ctx, git)
	if err != nil {
		return nil, nil, err
	}
	if len(changes.changedPaths) == 0 {
		return nil, nil, nil
	}
	paths, err := snapshotAllWorktreePaths(ctx, git.Root())
	if err != nil {
		return nil, nil, err
	}
	backing, err := newWorktreeSnapshotBacking("tao-merge-boundary-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create worktree snapshot backing: %w", err)
	}
	snapshot = &worktreePathSnapshot{paths: paths, backing: backing}
	defer func() {
		if err != nil {
			snapshot.cleanup()
			snapshot = nil
		}
	}()
	snapshot.preserved, err = snapshotSelectedWorktreePaths(ctx, git.Root(), changes.changedPaths, backing, worktreeSnapshotLimits{
		maxPaths:       maxWorktreeBoundaryPaths,
		maxBackupBytes: maxWorktreeBoundaryBackupBytes,
	})
	if err != nil {
		return nil, snapshot, err
	}
	return append([]string(nil), changes.changedPaths...), snapshot, nil
}

func snapshotSelectedWorktreePaths(ctx context.Context, root string, paths []string, backing *worktreeSnapshotBacking, limits worktreeSnapshotLimits) (map[string]worktreePathState, error) {
	if limits.maxPaths <= 0 || limits.maxBackupBytes <= 0 {
		return nil, errors.New("selected worktree snapshot limits must be positive")
	}
	if backing == nil || backing.file == nil {
		return nil, errors.New("temporary snapshot backing is unavailable")
	}
	states := make(map[string]worktreePathState, min(len(paths), limits.maxPaths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := validatePorcelainPath(path)
		if err != nil {
			return nil, err
		}
		if _, seen := states[path]; seen {
			continue
		}
		if len(states) >= limits.maxPaths {
			return nil, fmt.Errorf("selected worktree snapshot exceeds %d-path limit", limits.maxPaths)
		}
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			states[path] = worktreePathState{}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}
		state, err := backUpWorktreePathState(ctx, fullPath, fileInfoDirEntry{info}, backing, limits.maxBackupBytes)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", path, err)
		}
		states[path] = state
	}
	return states, nil
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

func resolutionContentFingerprint(root string, paths []string) (string, error) {
	return aggregateReworkContentFingerprint(root, paths)
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
