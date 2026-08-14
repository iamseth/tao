package merge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	commitpkg "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/prompts"
)

const DefaultBatchResolutionAttempts = 3

var errBatchConflictPrepared = errors.New("batch conflict prepared for agent")

// BatchResolver is the injectable orchestration boundary for deferred batch
// candidates.
type BatchResolver interface {
	Resolve(context.Context, BatchState, string, BatchResolveOptions) (BatchResolveResult, error)
}

// BatchResolutionAgent performs exactly one provider-neutral editing session.
type BatchResolutionAgent interface {
	Resolve(context.Context, BatchAgentSessionRequest) (BatchAgentSessionResult, error)
}

type BatchResolveOptions struct {
	VerifyCommand string
	MaxAttempts   int
}

type BatchResolveResult struct {
	State    BatchState
	Resolved []string
}

// BatchAgentResolver recreates deferred candidate changes in the integration
// worktree, delegates edits only, and retains all Git settlement ownership.
type BatchAgentResolver struct {
	Store   BatchTransitionStore
	Service Service
	Agent   BatchResolutionAgent
	Now     func() time.Time
}

func (r BatchAgentResolver) Resolve(ctx context.Context, state BatchState, integrationRoot string, options BatchResolveOptions) (BatchResolveResult, error) {
	result := BatchResolveResult{State: state}
	if r.Store == nil || r.Agent == nil {
		return result, fmt.Errorf("batch transition store and resolver agent are required")
	}
	git, err := r.Service.gitClientForRoot(integrationRoot)
	if err != nil {
		return result, err
	}
	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultBatchResolutionAttempts
	}
	deferredPlans := make([]string, 0, len(state.Integrations))
	for _, integration := range state.Integrations {
		if integration.Status == batchIntegrationDeferred || integration.Status == batchIntegrationApplying {
			deferredPlans = append(deferredPlans, integration.PlanID)
		}
	}
	for _, planID := range deferredPlans {
		integrationIndex := batchIntegrationIndex(state, planID)
		if integrationIndex < 0 {
			return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("deferred plan %s is absent from integration progress", planID))
		}
		integration := &state.Integrations[integrationIndex]
		candidateIndex := batchCandidateIndex(state, integration.PlanID)
		if candidateIndex < 0 {
			return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("deferred plan %s is absent from candidate snapshot", integration.PlanID))
		}
		candidate := &state.Candidates[candidateIndex]
		if integration.Status == batchIntegrationApplying {
			currentHead, revErr := git.RevParse(ctx, "HEAD")
			if revErr != nil {
				return result, fmt.Errorf("recover resolved candidate %s: %w", candidate.PlanID, revErr)
			}
			base, committed, recoverErr := recoverApplyingBatchIntegration(ctx, git, *candidate, *integration, strings.TrimSpace(currentHead))
			if recoverErr != nil {
				return result, recoverErr
			}
			if committed {
				state, err = r.settleResolvedCandidate(state, integrationIndex, candidateIndex, strings.TrimSpace(currentHead))
				if err != nil {
					return result, err
				}
				result.Resolved = append(result.Resolved, candidate.PlanID)
				continue
			}
			if resolution := latestBatchResolution(integration); resolution != nil && resolution.CommitMessage != "" {
				var resolved, committed bool
				state, resolved, committed, err = r.finishResolvedCandidate(ctx, state, git, planID, options.VerifyCommand)
				if errors.Is(err, errResolvedCandidateContentDrift) {
					state, err = r.prepareFreshResolvedCandidate(ctx, git, state, planID)
					if err == nil {
						state, err = r.persist(state)
					}
				}
				if err != nil {
					if committed {
						return result, err
					}
					return r.block(ctx, result, state, git, BatchBlockKindResumable, err.Error())
				}
				if resolved {
					result.Resolved = append(result.Resolved, planID)
					continue
				}
			} else {
				if err := restoreBatchIntegration(ctx, git, base); err != nil {
					return result, fmt.Errorf("restore interrupted resolution for %s: %w", candidate.PlanID, err)
				}
				integration.Status = batchIntegrationDeferred
				integration.IntegrationSHA = ""
				state, err = r.persist(state)
				if err != nil {
					return result, fmt.Errorf("persist recovered resolution for %s: %w", candidate.PlanID, err)
				}
			}
			integrationIndex = batchIntegrationIndex(state, planID)
			candidateIndex = batchCandidateIndex(state, planID)
			integration = &state.Integrations[integrationIndex]
			candidate = &state.Candidates[candidateIndex]
		}
		if interruptedResolution := activeBatchResolution(integration); interruptedResolution != nil {
			if err := restoreBatchIntegration(ctx, git, interruptedResolution.BaseSHA); err != nil {
				return result, fmt.Errorf("restore interrupted agent resolution for %s: %w", candidate.PlanID, err)
			}
			interruptedResolution.CompletedAt = r.timestamp()
			interruptedResolution.Outcome = "interrupted"
			state, err = r.persist(state)
			if err != nil {
				return result, fmt.Errorf("persist interrupted resolution for %s: %w", candidate.PlanID, err)
			}
			integrationIndex = batchIntegrationIndex(state, planID)
			integration = &state.Integrations[integrationIndex]
		}
		for integration.Attempts < maxAttempts {
			beforeHead, headErr := git.RevParse(ctx, "HEAD")
			if headErr != nil {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, "capture integration head before agent resolution: "+headErr.Error())
			}
			beforeHead = strings.TrimSpace(beforeHead)
			beforeRefs, refsErr := snapshotBatchProtectedRefs(ctx, git, state)
			if refsErr != nil {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, "capture protected refs before agent resolution: "+refsErr.Error())
			}
			integrationIndex = orderBatchIntegrationsForResolution(&state, planID, beforeHead)
			if integrationIndex < 0 {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("deferred plan %s is absent from integration progress", planID))
			}
			integration = &state.Integrations[integrationIndex]
			integration.Attempts++
			state.Attempts.ConflictResolution++
			record := BatchResolution{Attempt: integration.Attempts, Kind: resolutionKind(*integration), BaseSHA: beforeHead, RequestedAt: r.timestamp()}
			integration.Resolutions = append(integration.Resolutions, record)
			state, err = r.persist(state)
			if err != nil {
				return result, fmt.Errorf("persist resolution request: %w", err)
			}
			integrationIndex = batchIntegrationIndex(state, planID)
			candidateIndex = batchCandidateIndex(state, planID)
			integration = &state.Integrations[integrationIndex]
			candidate = &state.Candidates[candidateIndex]

			if err := r.prepare(ctx, git, integration, *candidate); err != nil && !errors.Is(err, errBatchConflictPrepared) {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, err.Error())
			}
			beforeStatus, _ := git.StatusPorcelain(ctx)
			prompt, renderErr := r.renderPrompt(ctx, state, *integration, *candidate, options.VerifyCommand, beforeStatus)
			if renderErr != nil {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, renderErr.Error())
			}

			sessionResult, agentErr := r.Agent.Resolve(ctx, BatchAgentSessionRequest{
				BatchID: state.ID, Operation: BatchAgentOperationCandidateResolution, Attempt: integration.Attempts,
				IntegrationRoot: integrationRoot, Prompt: prompt, CandidatePlanID: candidate.PlanID,
			})
			output := sessionResult.Output
			changes, statusErr := concretePorcelainChanges(ctx, git)
			afterHead, afterHeadErr := git.RevParse(ctx, "HEAD")
			refsErr = compareBatchProtectedRefs(ctx, git, beforeRefs)
			changed := changes.changedPaths
			last := &integration.Resolutions[len(integration.Resolutions)-1]
			last.CompletedAt, last.Outcome, last.Summary, last.ChangedPaths = r.timestamp(), "agent_returned", boundResolutionSummary(output), changed
			if agentErr != nil {
				last.Outcome = "agent_error"
			}
			if afterHeadErr != nil || strings.TrimSpace(afterHead) != strings.TrimSpace(beforeHead) || refsErr != nil {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("agent changed protected Git refs while resolving %s", candidate.PlanID))
			}
			if agentErr != nil {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("agent resolution for %s failed: %v", candidate.PlanID, agentErr))
			}
			if statusErr != nil {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("inspect agent edits for %s: %v", candidate.PlanID, statusErr))
			}
			resolvedOutput, outputErr := decodeBatchResolutionOutput(output)
			if outputErr != nil {
				last.Outcome = "malformed_output"
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("agent returned malformed output for %s: %v", candidate.PlanID, outputErr))
			}
			markerScanPaths := presentMarkerScanPaths(integrationRoot, integration.ConflictFiles)
			validation := validateAgentEdits(integrationRoot, changed, markerScanPaths)
			switch validation.issue {
			case agentEditIssueUnsafePaths:
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("agent made unsafe metadata edits while resolving %s", candidate.PlanID))
			case agentEditIssueNoChanges, agentEditIssueConflictMarkers, agentEditIssueUnscannablePaths:
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("agent left unresolved conflicts for %s", candidate.PlanID))
			}
			message, messageErr := singleMergeCommitMessage(resolvedOutput.CommitMessage, candidate.PlanID, candidate.SourceTip)
			if messageErr != nil {
				last.Outcome = "malformed_output"
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("agent returned invalid commit proposal for %s: %v", candidate.PlanID, messageErr))
			}
			messageResolved := true
			if candidate.ReviewCommitMessage != nil {
				directMessage, directErr := singleMergeCommitMessage(*candidate.ReviewCommitMessage, candidate.PlanID, candidate.SourceTip)
				if directErr != nil {
					return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("approved review commit proposal for %s is invalid: %v", candidate.PlanID, directErr))
				}
				messageResolved = message != directMessage
			}
			contentFingerprint, fingerprintErr := resolvedCandidateContentFingerprint(integrationRoot, changed)
			if fingerprintErr != nil {
				return r.block(ctx, result, state, git, BatchBlockKindResumable, fmt.Sprintf("fingerprint agent edits for %s: %v", candidate.PlanID, fingerprintErr))
			}
			last.Summary = boundResolutionSummary(resolvedOutput.Summary)
			state, err = r.persist(state)
			if err != nil {
				_ = restoreBatchIntegration(ctx, git, beforeHead)
				return result, fmt.Errorf("persist resolution outcome for %s: %w", candidate.PlanID, err)
			}
			integrationIndex = batchIntegrationIndex(state, planID)
			candidateIndex = batchCandidateIndex(state, planID)
			integration = &state.Integrations[integrationIndex]
			candidate = &state.Candidates[candidateIndex]
			last = latestBatchResolution(integration)
			last.CommitMessage = message
			last.ChangedPaths = append([]string(nil), changed...)
			last.ContentFingerprint = contentFingerprint
			integration.CommitMessage = message
			integration.Status = batchIntegrationApplying
			integration.IntegrationBaseSHA = strings.TrimSpace(beforeHead)
			integration.IntegrationSHA = ""
			if messageResolved || candidate.CommitMessage == "" {
				candidate.CommitMessage = message
			}
			candidate.CommitMessageResolved = messageResolved
			state, err = r.persist(state)
			if err != nil {
				_ = restoreBatchIntegration(ctx, git, beforeHead)
				return result, fmt.Errorf("persist resolved candidate commit intent for %s: %w", candidate.PlanID, err)
			}
			result.State = state
			var resolved, committed bool
			state, resolved, committed, err = r.finishResolvedCandidate(ctx, state, git, planID, options.VerifyCommand)
			if errors.Is(err, errResolvedCandidateContentDrift) {
				state, err = r.prepareFreshResolvedCandidate(ctx, git, state, planID)
				if err == nil {
					state, err = r.persist(state)
				}
			}
			if err != nil {
				if committed {
					return result, err
				}
				return r.block(ctx, result, state, git, BatchBlockKindResumable, err.Error())
			}
			if resolved {
				result.Resolved = append(result.Resolved, planID)
				break
			}
			integrationIndex = batchIntegrationIndex(state, planID)
			integration = &state.Integrations[integrationIndex]
		}
		integrationIndex = batchIntegrationIndex(state, planID)
		if integrationIndex < 0 || state.Integrations[integrationIndex].Status != batchIntegrationApplied {
			return r.block(ctx, result, state, git, BatchBlockKindTerminal, fmt.Sprintf("resolution attempt cap exhausted for %s", planID))
		}
	}
	state.Status = BatchStatusReviewing
	if state.Ejection != nil && state.Ejection.Status == batchEjectionReintegrating {
		state.Ejection.Status = batchEjectionCompleted
	}
	state.BlockedReason, state.BlockKind, state.ResumeStatus = "", "", ""
	state, err = r.persist(state)
	result.State = state
	return result, err
}

type batchProtectedRefs map[string]string

func snapshotBatchProtectedRefs(ctx context.Context, git GitClient, state BatchState) (batchProtectedRefs, error) {
	candidates := effectiveBatchCandidates(state)
	refs := make(batchProtectedRefs, len(candidates)+1)
	branches := make([]string, 0, len(candidates)+1)
	branches = append(branches, state.DefaultBranch)
	for _, candidate := range candidates {
		branches = append(branches, candidate.Branch)
	}
	for _, branch := range branches {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			return nil, errors.New("protected branch is empty")
		}
		ref := branch
		if !strings.HasPrefix(ref, "refs/heads/") {
			ref = "refs/heads/" + ref
		}
		if _, ok := refs[ref]; ok {
			continue
		}
		sha, err := git.RevParse(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", ref, err)
		}
		refs[ref] = strings.TrimSpace(sha)
	}
	return refs, nil
}

func compareBatchProtectedRefs(ctx context.Context, git GitClient, before batchProtectedRefs) error {
	const missingRefSHA = "0000000000000000000000000000000000000000"
	refs := make([]string, 0, len(before))
	for ref := range before {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var mutations, restoreErrors []error
	for _, ref := range refs {
		want := before[ref]
		got, resolveErr := git.RevParse(ctx, ref)
		got = strings.TrimSpace(got)
		if resolveErr == nil && got == want {
			continue
		}
		if resolveErr != nil {
			mutations = append(mutations, fmt.Errorf("%s was deleted from %s", ref, want))
			got = missingRefSHA
		} else {
			mutations = append(mutations, fmt.Errorf("%s moved from %s to %s", ref, want, got))
		}
		if err := git.UpdateRefCAS(ctx, ref, want, got); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore %s to %s: %w", ref, want, err))
		}
	}
	if len(mutations) == 0 {
		return nil
	}
	return errors.Join(append(mutations, restoreErrors...)...)
}

func (r BatchAgentResolver) prepare(ctx context.Context, git GitClient, integration *BatchIntegration, candidate BatchCandidate) error {
	if err := git.MergeSquash(ctx, candidate.SourceTip); err != nil {
		integration.ConflictFiles = collectConflictFiles(ctx, git)
		return errBatchConflictPrepared
	}
	changed, err := git.HasStagedChanges(ctx)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("candidate %s produces no changes", candidate.PlanID)
	}
	return nil
}

func (r BatchAgentResolver) renderPrompt(ctx context.Context, state BatchState, integration BatchIntegration, candidate BatchCandidate, verifyCommand, status string) (string, error) {
	diffNames, _ := r.Service.gitClientForRoot(state.RepoRoot)
	var diff string
	if diffNames != nil {
		if names, err := diffNames.ChangedFiles(ctx, integration.IntegrationBaseSHA+".."+candidate.SourceTip); err == nil {
			diff = strings.Join(names, "\n")
		}
	}
	var prior []string
	for _, item := range state.Integrations {
		if item.Status == batchIntegrationApplied {
			prior = append(prior, item.PlanID+" @ "+item.IntegrationSHA)
		}
	}
	return prompts.RenderMergeResolve(prompts.MergeResolveData{BatchID: state.ID, PlanID: candidate.PlanID, SourceHead: candidate.SourceTip, IntegrationBase: integration.IntegrationBaseSHA, VerifyCommand: verifyCommand, PlanBrief: candidate.PlanTitle, SourceReview: candidate.ReviewBase + ".." + candidate.ReviewHead, Diff: diff, ConflictFiles: strings.Join(integration.ConflictFiles, "\n") + "\n" + status, PriorPlans: strings.Join(prior, "\n"), VerificationOutput: integration.VerificationOutput})
}

type batchResolutionOutput struct {
	Summary       string                   `json:"summary"`
	CommitMessage plan.ReviewCommitMessage `json:"commit_message"`
}

func decodeBatchResolutionOutput(output string) (batchResolutionOutput, error) {
	const outputLimit = 32 * 1024
	if strings.TrimSpace(output) == "" {
		return batchResolutionOutput{}, errors.New("output is empty")
	}
	if len(output) > outputLimit {
		return batchResolutionOutput{}, fmt.Errorf("output exceeds %d bytes", outputLimit)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	var resolved batchResolutionOutput
	if err := decoder.Decode(&resolved); err != nil {
		return batchResolutionOutput{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return batchResolutionOutput{}, errors.New("multiple JSON values")
		}
		return batchResolutionOutput{}, err
	}
	if strings.TrimSpace(resolved.Summary) == "" {
		return batchResolutionOutput{}, errors.New("summary is empty")
	}
	if len(resolved.Summary) > 4096 {
		return batchResolutionOutput{}, errors.New("summary exceeds 4096 bytes")
	}
	if err := commitpkg.ValidateProposalMessage(resolved.CommitMessage.Subject, resolved.CommitMessage.Body); err != nil {
		return batchResolutionOutput{}, err
	}
	return resolved, nil
}

var errResolvedCandidateContentDrift = errors.New("resolved candidate content drifted from durable intent")

func (r BatchAgentResolver) finishResolvedCandidate(ctx context.Context, state BatchState, git GitClient, planID, verifyCommand string) (BatchState, bool, bool, error) {
	integrationIndex := batchIntegrationIndex(state, planID)
	candidateIndex := batchCandidateIndex(state, planID)
	if integrationIndex < 0 || candidateIndex < 0 {
		return state, false, false, fmt.Errorf("resolved plan %s is absent from durable batch state", planID)
	}
	integration := &state.Integrations[integrationIndex]
	candidate := &state.Candidates[candidateIndex]
	resolution := latestBatchResolution(integration)
	if resolution == nil || resolution.CommitMessage == "" || integration.CommitMessage != resolution.CommitMessage || candidate.CommitMessage != resolution.CommitMessage {
		return state, false, false, fmt.Errorf("resolved candidate %s has no exact durable commit proposal", planID)
	}
	if err := validateBatchCommitMessage(integration.CommitMessage, *candidate); err != nil {
		return state, false, false, fmt.Errorf("resolved candidate %s commit proposal is invalid: %w", planID, err)
	}

	var changed []string
	if resolution.ContentFingerprint != "" {
		changes, err := concretePorcelainChanges(ctx, git)
		if err != nil {
			return state, false, false, fmt.Errorf("%w for %s: inspect edit set: %w", errResolvedCandidateContentDrift, planID, err)
		}
		changed = changes.changedPaths
		if !slices.Equal(changed, resolution.ChangedPaths) {
			return state, false, false, fmt.Errorf("%w for %s: edit set changed", errResolvedCandidateContentDrift, planID)
		}
		fingerprint, err := resolvedCandidateContentFingerprint(git.Root(), changed)
		if err != nil {
			return state, false, false, fmt.Errorf("%w for %s: cannot fingerprint file content before staging: %w", errResolvedCandidateContentDrift, planID, err)
		}
		if fingerprint != resolution.ContentFingerprint {
			return state, false, false, fmt.Errorf("%w for %s: file content changed", errResolvedCandidateContentDrift, planID)
		}
	} else {
		// Fingerprint-less applying intents were written by older Tao versions.
		// Preserve their historical safe recovery behavior.
		status, err := git.StatusPorcelain(ctx)
		if err != nil {
			return state, false, false, fmt.Errorf("inspect agent edits for %s: %w", planID, err)
		}
		changed = porcelainPaths(status)
	}
	markerScanPaths := presentMarkerScanPaths(git.Root(), integration.ConflictFiles)
	validation := validateAgentEdits(git.Root(), changed, markerScanPaths)
	switch validation.issue {
	case agentEditIssueUnsafePaths:
		return state, false, false, fmt.Errorf("agent made unsafe metadata edits while resolving %s", planID)
	case agentEditIssueNoChanges, agentEditIssueConflictMarkers, agentEditIssueUnscannablePaths:
		return state, false, false, fmt.Errorf("agent left unresolved conflicts for %s", planID)
	}
	stagePaths := changed
	if resolution.ContentFingerprint == "" {
		stagePaths = []string{"."}
	}
	if err := git.Add(ctx, stagePaths...); err != nil {
		return state, false, false, fmt.Errorf("stage agent resolution: %w", err)
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil || hasUnmergedStatus(status) {
		return state, false, false, fmt.Errorf("agent made no progress resolving %s", planID)
	}
	verification := resolveMergeVerifyCommandAtRoot(git.Root(), Options{VerifyCommand: verifyCommand})
	if verification.command != "" {
		output, verifyErr := r.Service.runMergeVerifyAtRoot(ctx, git.Root(), verification.command)
		if verifyErr != nil {
			integration.VerificationOutput = output
			integration.Fingerprint = resolutionFingerprint(status, output)
			integration.Status = batchIntegrationDeferred
			state.Attempts.ConflictFingerprint = integration.Fingerprint
			resolution.Outcome = "verification_failed"
			state, err = r.persist(state)
			if err != nil {
				return state, false, false, err
			}
			if err := restoreBatchIntegration(ctx, git, integration.IntegrationBaseSHA); err != nil {
				return state, false, false, fmt.Errorf("restore failed candidate verification for %s: %w", planID, err)
			}
			return state, false, false, nil
		}
	}
	if err := git.Commit(ctx, integration.CommitMessage); err != nil {
		return state, false, false, fmt.Errorf("commit resolved candidate: %w", err)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return state, false, true, err
	}
	state, err = r.settleResolvedCandidate(state, integrationIndex, candidateIndex, strings.TrimSpace(head))
	return state, err == nil, true, err
}

func (r BatchAgentResolver) prepareFreshResolvedCandidate(ctx context.Context, git GitClient, state BatchState, planID string) (BatchState, error) {
	integrationIndex := batchIntegrationIndex(state, planID)
	candidateIndex := batchCandidateIndex(state, planID)
	if integrationIndex < 0 || candidateIndex < 0 {
		return state, fmt.Errorf("restore drifted resolved candidate %s: durable batch state is incomplete", planID)
	}
	integration := &state.Integrations[integrationIndex]
	if err := restoreBatchIntegration(ctx, git, integration.IntegrationBaseSHA); err != nil {
		return state, fmt.Errorf("restore drifted resolved candidate %s: %w", planID, err)
	}
	staleMessage := integration.CommitMessage
	integration.Status = batchIntegrationDeferred
	integration.IntegrationSHA = ""
	integration.CommitMessage = ""
	if resolution := latestBatchResolution(integration); resolution != nil {
		resolution.Outcome = "content_drift"
	}
	candidate := &state.Candidates[candidateIndex]
	if candidate.CommitMessage == staleMessage {
		candidate.CommitMessage = ""
		candidate.CommitMessageResolved = false
	}
	return state, nil
}

func resolvedCandidateContentFingerprint(root string, paths []string) (string, error) {
	return aggregateReworkContentFingerprint(root, paths)
}

func (r BatchAgentResolver) settleResolvedCandidate(state BatchState, integrationIndex, candidateIndex int, head string) (BatchState, error) {
	integration := &state.Integrations[integrationIndex]
	integration.Status = batchIntegrationApplied
	integration.IntegrationSHA = head
	integration.DeferredReason, integration.ConflictFiles, integration.VerificationOutput = "", nil, ""
	state.Candidates[candidateIndex].Deferred = nil
	if !slices.Contains(state.ChosenOrder, state.Candidates[candidateIndex].PlanID) {
		state.ChosenOrder = append(state.ChosenOrder, state.Candidates[candidateIndex].PlanID)
	}
	state.IntegrationHead = head
	orderBatchIntegrationsForResolution(&state, "", head)
	persisted, err := r.persist(state)
	if err != nil {
		return state, fmt.Errorf("persist resolved candidate commit: %w", err)
	}
	return persisted, nil
}

func (r BatchAgentResolver) block(ctx context.Context, result BatchResolveResult, state BatchState, git GitClient, kind BatchBlockKind, reason string) (BatchResolveResult, error) {
	if state.IntegrationHead != "" {
		_ = restoreBatchIntegration(ctx, git, state.IntegrationHead)
	}
	BlockBatch(&state, kind, reason)
	persisted, err := r.persist(state)
	result.State = persisted
	if err != nil {
		return result, errors.Join(errors.New(reason), err)
	}
	return result, errors.New(reason)
}

func (r BatchAgentResolver) persist(state BatchState) (BatchState, error) {
	state.UpdatedAt = r.timestamp()
	return r.Store.Transition(state, state.UpdatedAt)
}
func (r BatchAgentResolver) timestamp() string {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	return now.UTC().Format(time.RFC3339Nano)
}
func batchIntegrationIndex(state BatchState, id string) int {
	for i := range state.Integrations {
		if state.Integrations[i].PlanID == id {
			return i
		}
	}
	return -1
}
func batchCandidateIndex(state BatchState, id string) int {
	for i := range state.Candidates {
		if state.Candidates[i].PlanID == id {
			return i
		}
	}
	return -1
}
func latestBatchResolution(integration *BatchIntegration) *BatchResolution {
	if integration == nil || len(integration.Resolutions) == 0 {
		return nil
	}
	return &integration.Resolutions[len(integration.Resolutions)-1]
}

func activeBatchResolution(integration *BatchIntegration) *BatchResolution {
	if integration == nil || len(integration.Resolutions) == 0 {
		return nil
	}
	resolution := latestBatchResolution(integration)
	if resolution.CompletedAt != "" || strings.TrimSpace(resolution.BaseSHA) == "" || strings.TrimSpace(resolution.BaseSHA) != strings.TrimSpace(integration.IntegrationBaseSHA) {
		return nil
	}
	return resolution
}

func resolutionKind(i BatchIntegration) string {
	if len(i.ConflictFiles) > 0 {
		return "conflict"
	}
	return "verification"
}
func boundResolutionSummary(s string) string {
	const limit = 4096
	if len(s) > limit {
		return s[:limit] + " [TRUNCATED]"
	}
	return s
}
func resolutionFingerprint(status, output string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(status+"\x00"+output)))
}
func hasUnmergedStatus(status string) bool {
	for line := range strings.SplitSeq(status, "\n") {
		if len(line) >= 2 && unmergedStatusCode(line[:2]) {
			return true
		}
	}
	return false
}

func porcelainPaths(status string) []string {
	var paths []string
	for line := range strings.SplitSeq(status, "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		if _, to, ok := strings.Cut(p, " -> "); ok {
			p = to
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

type porcelainChanges struct {
	changedPaths    []string
	markerScanPaths []string
}

type porcelainV1ZClient interface {
	StatusPorcelainV1Z(context.Context) (string, error)
}

func concretePorcelainChanges(ctx context.Context, git GitClient) (porcelainChanges, error) {
	if zeroStatus, ok := git.(porcelainV1ZClient); ok {
		status, err := zeroStatus.StatusPorcelainV1Z(ctx)
		if err != nil {
			return porcelainChanges{}, err
		}
		return parsePorcelainV1Z(status)
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return porcelainChanges{}, err
	}
	return parseConcretePorcelainV1(status)
}

func parseConcretePorcelainV1(status string) (porcelainChanges, error) {
	var changes porcelainChanges
	for line := range strings.SplitSeq(status, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if len(line) < 4 || line[2] != ' ' || !validPorcelainStatus(line[:2]) || porcelainRenameOrCopy(line[:2]) {
			return porcelainChanges{}, fmt.Errorf("ambiguous git status entry %q", line)
		}
		path, err := decodePorcelainPath(line[3:])
		if err != nil {
			return porcelainChanges{}, fmt.Errorf("decode git status path %q: %w", line[3:], err)
		}
		if line[:2] == "??" && strings.HasSuffix(path, "/") {
			return porcelainChanges{}, fmt.Errorf("git status collapsed untracked directory %q", path)
		}
		recordPorcelainChange(&changes, line[:2], path, "")
	}
	changes.normalize()
	return changes, nil
}

func parsePorcelainV1Z(status string) (porcelainChanges, error) {
	if status == "" {
		return porcelainChanges{}, nil
	}
	if !strings.HasSuffix(status, "\x00") {
		return porcelainChanges{}, errors.New("truncated NUL-delimited git status")
	}
	fields := strings.Split(status[:len(status)-1], "\x00")
	var changes porcelainChanges
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 || entry[2] != ' ' || !validPorcelainStatus(entry[:2]) {
			return porcelainChanges{}, fmt.Errorf("ambiguous NUL-delimited git status entry %q", entry)
		}
		code := entry[:2]
		target, err := validatePorcelainPath(entry[3:])
		if err != nil {
			return porcelainChanges{}, fmt.Errorf("decode NUL-delimited git status path: %w", err)
		}
		var source string
		if porcelainRenameOrCopy(code) {
			i++
			if i >= len(fields) {
				return porcelainChanges{}, fmt.Errorf("rename or copy entry for %q has no source path", target)
			}
			source, err = validatePorcelainPath(fields[i])
			if err != nil {
				return porcelainChanges{}, fmt.Errorf("decode NUL-delimited git status source path: %w", err)
			}
		}
		if code == "??" && strings.HasSuffix(target, "/") {
			return porcelainChanges{}, fmt.Errorf("git status collapsed untracked directory %q", target)
		}
		recordPorcelainChange(&changes, code, target, source)
	}
	changes.normalize()
	return changes, nil
}

func validPorcelainStatus(code string) bool {
	if code == "??" {
		return true
	}
	if len(code) != 2 || code == "  " {
		return false
	}
	const valid = " MTADRCU"
	return strings.ContainsRune(valid, rune(code[0])) && strings.ContainsRune(valid, rune(code[1]))
}

func porcelainRenameOrCopy(code string) bool {
	return strings.ContainsAny(code, "RC")
}

func decodePorcelainPath(raw string) (string, error) {
	if strings.HasPrefix(raw, "\"") {
		decoded, err := strconv.Unquote(raw)
		if err != nil {
			return "", err
		}
		return validatePorcelainPath(decoded)
	}
	if strings.Contains(raw, "\"") {
		return "", errors.New("malformed quoted path")
	}
	return validatePorcelainPath(raw)
}

func validatePorcelainPath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, '\x00') {
		return "", errors.New("empty or NUL-containing path")
	}
	if !utf8.ValidString(path) {
		return "", errors.New("path is not valid UTF-8")
	}
	return filepath.ToSlash(path), nil
}

func recordPorcelainChange(changes *porcelainChanges, code, target, source string) {
	changes.changedPaths = append(changes.changedPaths, target)
	if strings.Contains(code, "R") && source != "" {
		changes.changedPaths = append(changes.changedPaths, source)
	}
	if porcelainWorktreePathPresent(code) {
		changes.markerScanPaths = append(changes.markerScanPaths, target)
	}
}

func porcelainWorktreePathPresent(code string) bool {
	return code == "??" || (code[1] != 'D' && code != "D ")
}

func (c *porcelainChanges) normalize() {
	sort.Strings(c.changedPaths)
	c.changedPaths = slices.Compact(c.changedPaths)
	sort.Strings(c.markerScanPaths)
	c.markerScanPaths = slices.Compact(c.markerScanPaths)
}

// presentMarkerScanPaths preserves valid delete-based conflict resolutions.
// Other lookup failures remain in the scan list so the gate fails closed.
func presentMarkerScanPaths(root string, paths []string) []string {
	present := make([]string, 0, len(paths))
	for _, path := range paths {
		if unsafeResolutionPaths([]string{path}) {
			present = append(present, path)
			continue
		}
		_, err := os.Lstat(filepath.Join(root, filepath.Clean(filepath.FromSlash(path))))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			present = append(present, path)
		}
	}
	return present
}

type agentEditIssue uint8

const (
	agentEditIssueNone agentEditIssue = iota
	agentEditIssueUnsafePaths
	agentEditIssueNoChanges
	agentEditIssueConflictMarkers
	agentEditIssueUnscannablePaths
)

type agentEditValidation struct {
	issue       agentEditIssue
	markerPaths []string
	scanErr     error
}

// validateAgentEdits scans only markerScanPaths. Callers must pass the real
// conflict files or the files changed by the agent, never a repository-wide list.
func validateAgentEdits(root string, changedPaths, markerScanPaths []string) agentEditValidation {
	if unsafeResolutionPaths(changedPaths) || unsafeResolutionPaths(markerScanPaths) {
		return agentEditValidation{issue: agentEditIssueUnsafePaths}
	}
	if len(changedPaths) == 0 {
		return agentEditValidation{issue: agentEditIssueNoChanges}
	}
	markerPaths, err := conflictMarkerPaths(root, markerScanPaths)
	if err != nil {
		return agentEditValidation{issue: agentEditIssueUnscannablePaths, markerPaths: markerPaths, scanErr: err}
	}
	if len(markerPaths) > 0 {
		return agentEditValidation{issue: agentEditIssueConflictMarkers, markerPaths: markerPaths}
	}
	return agentEditValidation{issue: agentEditIssueNone}
}

// Marker prefixes are built at runtime so this function can never match its
// own source if this file ends up in a conflict-file list.
var conflictMarkerPrefixes = []string{strings.Repeat("<", 7), strings.Repeat(">", 7)}

func conflictMarkersRemain(root string, paths []string) bool {
	found, err := conflictMarkerPaths(root, paths)
	return err != nil || len(found) > 0
}

func conflictMarkerPaths(root string, paths []string) ([]string, error) {
	var found []string
	for _, path := range paths {
		fullPath := filepath.Join(root, filepath.Clean(filepath.FromSlash(path)))
		info, err := os.Lstat(fullPath)
		if err != nil {
			return found, fmt.Errorf("inspect conflict-marker path %q: %w", path, err)
		}
		var content []byte
		switch {
		case info.Mode().IsRegular():
			content, err = os.ReadFile(fullPath) //nolint:gosec // Git path is validated and confined to the integration root.
		case info.Mode()&os.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(fullPath)
			content = []byte(target)
		default:
			err = fmt.Errorf("unsupported file type %s", info.Mode().Type())
		}
		if err != nil {
			return found, fmt.Errorf("read conflict-marker path %q: %w", path, err)
		}
		if contentHasConflictMarker(string(content)) {
			found = append(found, path)
		}
	}
	return found, nil
}

func contentHasConflictMarker(content string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		for _, marker := range conflictMarkerPrefixes {
			rest, found := strings.CutPrefix(line, marker)
			if found && (rest == "" || strings.HasPrefix(rest, " ")) {
				return true
			}
		}
	}
	return false
}

func unsafeResolutionPaths(paths []string) bool {
	for _, path := range paths {
		if path == "" || filepath.IsAbs(filepath.FromSlash(path)) {
			return true
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean == ".git" || strings.HasPrefix(clean, ".git/") || clean == ".tao" || strings.HasPrefix(clean, ".tao/") {
			return true
		}
	}
	return false
}
