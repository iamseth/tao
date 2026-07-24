package merge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	commitpkg "github.com/iamseth/tao/internal/commit"
)

const (
	batchIntegrationApplying = "applying"
	batchIntegrationApplied  = "applied"
	batchIntegrationDeferred = "deferred"

	batchEjectionPending       = "pending"
	batchEjectionReintegrating = "reintegrating"
	batchEjectionCompleted     = "completed"
)

// BatchTransitionStore is the durable boundary used by staged integration.
type BatchTransitionStore interface {
	Transition(BatchState, string) (BatchState, error)
}

// BatchIntegrateOptions controls staged integration. DryRun may only be used
// with a disposable integrationRoot; it performs the same Git simulations but
// writes no batch or plan state.
type BatchIntegrateOptions struct {
	VerifyCommand string
	DryRun        bool
}

// BatchEjectOptions identifies the attributed candidate and the verification
// command used while rebuilding the reduced integration.
type BatchEjectOptions struct {
	PlanID        string
	Reason        string
	VerifyCommand string
}

// BatchIntegrateResult reports the clean prefix and candidates deferred while
// trying to extend it.
type BatchIntegrateResult struct {
	State    BatchState
	Applied  []string
	Deferred []BatchDeferral
}

// BatchIntegrator applies immutable candidate tips in an isolated worktree.
type BatchIntegrator struct {
	Store   BatchTransitionStore
	Service Service
	Now     func() time.Time
}

// Eject durably records removal of one attributed candidate, restores the
// integration branch to the immutable default start, and rebuilds the reduced
// ordered set through Integrate. Repeating the call resumes whichever durable
// phase was interrupted.
func (b BatchIntegrator) Eject(ctx context.Context, state BatchState, integrationRoot string, options BatchEjectOptions) (BatchIntegrateResult, error) {
	result := BatchIntegrateResult{State: state}
	if b.Store == nil {
		return result, fmt.Errorf("batch transition store is required")
	}
	git, err := b.Service.gitClientForRoot(integrationRoot)
	if err != nil {
		return result, err
	}
	planID := strings.TrimSpace(options.PlanID)
	reason := strings.TrimSpace(options.Reason)
	if state.Ejection == nil {
		if planID == "" && state.NonConvergence != nil {
			planID = state.NonConvergence.PlanID
		}
		if reason == "" && state.NonConvergence != nil {
			reason = state.NonConvergence.Reason
		}
		if planID == "" || reason == "" {
			return result, errors.New("batch eject requires an attributed plan and reason")
		}
		if candidateByID(state.Candidates, planID) == nil {
			return result, fmt.Errorf("batch eject names unknown plan %s", planID)
		}
		if len(effectiveBatchCandidates(state)) <= 1 {
			return result, errors.New("batch eject requires at least one remaining candidate")
		}
		markBatchCandidateDeferred(&state, planID, BatchDeferral{PlanID: planID, Reason: reason})
		state.ChosenOrder = slicesDeleteValue(state.ChosenOrder, planID)
		state.Ejection = &BatchEjection{PlanID: planID, Reason: reason, Status: batchEjectionPending}
		state, err = b.persist(state) // write-ahead intent precedes reset
		if err != nil {
			return result, fmt.Errorf("persist batch eject intent: %w", err)
		}
		result.State = state
	} else {
		if planID != "" && planID != state.Ejection.PlanID {
			return result, fmt.Errorf("batch eject already targets plan %s", state.Ejection.PlanID)
		}
		planID, reason = state.Ejection.PlanID, state.Ejection.Reason
	}

	if state.Ejection.Status == batchEjectionCompleted {
		return BatchIntegrateResult{State: state}, nil
	}
	if state.Ejection.Status == batchEjectionPending {
		if err := restoreBatchIntegration(ctx, git, state.DefaultStartSHA); err != nil {
			return result, fmt.Errorf("restore integration for batch eject: %w", err)
		}
		state.Status = BatchStatusIntegrating
		state.Integrations = nil
		state.IntegrationHead = state.DefaultStartSHA
		if state.Review != nil {
			state.AggregateReviewSequence = max(state.AggregateReviewSequence, state.Review.Attempts)
		}
		state.Attempts = BatchAttempts{}
		state.Verification = nil
		state.Review = nil
		state.NonConvergence = nil
		state.Landing = nil
		state.Settlement = nil
		state.Finalization = nil
		state.LandedSHA = ""
		state.BlockedReason, state.BlockKind, state.ResumeStatus = "", "", ""
		state.Ejection = &BatchEjection{PlanID: planID, Reason: reason, Status: batchEjectionReintegrating}
		state, err = b.persist(state)
		if err != nil {
			return result, fmt.Errorf("persist batch eject rebuild: %w", err)
		}
		result.State = state
	}
	if state.Ejection.Status == batchEjectionReintegrating && state.Status == BatchStatusResolving {
		// Deferred and applying resolver records are owned by BatchAgentResolver.
		// Re-entering Integrate would skip their still-deferred candidates and
		// overwrite the resolving phase before the resolver can recover its work.
		return BatchIntegrateResult{State: state}, nil
	}
	return b.Integrate(ctx, state, integrationRoot, BatchIntegrateOptions{VerifyCommand: options.VerifyCommand})
}

func slicesDeleteValue(values []string, remove string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}

// Integrate creates one squash commit per green candidate. Every durable intent
// precedes Git mutation, and every failed attempt restores the exact prior head.
func (b BatchIntegrator) Integrate(ctx context.Context, state BatchState, integrationRoot string, options BatchIntegrateOptions) (BatchIntegrateResult, error) {
	result := BatchIntegrateResult{State: state}
	if strings.TrimSpace(integrationRoot) == "" {
		return result, fmt.Errorf("integration root is required")
	}
	if !options.DryRun && b.Store == nil {
		return result, fmt.Errorf("batch transition store is required")
	}
	git, err := b.Service.gitClientForRoot(integrationRoot)
	if err != nil {
		return result, err
	}
	startingHead, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return result, fmt.Errorf("capture integration head: %w", err)
	}
	startingHead = strings.TrimSpace(startingHead)
	if startingHead == "" {
		return result, fmt.Errorf("capture integration head: empty revision")
	}
	if state.IntegrationHead == "" {
		state.IntegrationHead = startingHead
	}
	if !options.DryRun && state.Status == BatchStatusPlanned {
		state.Status = BatchStatusIntegrating
		state, err = b.persist(state)
		if err != nil {
			return result, fmt.Errorf("persist integration start: %w", err)
		}
	}
	if !options.DryRun {
		state, err = b.prepareCandidateMessages(ctx, git, state)
		result.State = state
		if err != nil {
			return result, err
		}
	}

	candidates := orderedBatchCandidates(state)
	for _, candidate := range candidates {
		currentHead, revErr := git.RevParse(ctx, "HEAD")
		if revErr != nil {
			return result, fmt.Errorf("capture integration base for %s: %w", candidate.PlanID, revErr)
		}
		currentHead = strings.TrimSpace(currentHead)
		priorHead := currentHead
		recordIndex := applyingBatchIntegrationIndex(state, candidate.PlanID)
		commitAlreadyApplied := false
		if recordIndex >= 0 {
			priorHead, commitAlreadyApplied, err = recoverApplyingBatchIntegration(ctx, git, candidate, state.Integrations[recordIndex], currentHead)
			if err != nil {
				return result, err
			}
			if !commitAlreadyApplied {
				if err := restoreBatchIntegration(ctx, git, priorHead); err != nil {
					return result, fmt.Errorf("restore interrupted integration for %s: %w", candidate.PlanID, err)
				}
			}
		} else {
			message := candidate.CommitMessage
			if options.DryRun && message == "" {
				message = batchSquashCommitMessage(candidate)
			}
			record := BatchIntegration{PlanID: candidate.PlanID, SourceHead: candidate.SourceTip, IntegrationBaseSHA: priorHead, CommitMessage: message, Status: batchIntegrationApplying, Attempts: 1}
			state.Integrations = append(state.Integrations, record)
			recordIndex = len(state.Integrations) - 1
			if !options.DryRun {
				state, err = b.persist(state)
				if err != nil {
					return result, fmt.Errorf("persist integration intent for %s: %w", candidate.PlanID, err)
				}
			}
		}

		var deferral *batchCandidateDeferral
		if commitAlreadyApplied {
			deferral = b.verifyAppliedCandidate(ctx, git, candidate, priorHead, options)
		} else {
			message := state.Integrations[recordIndex].CommitMessage
			if message == "" {
				message = batchSquashCommitMessage(candidate)
			}
			deferral = b.applyCandidate(ctx, git, candidate, message, priorHead, options)
		}
		if deferral != nil {
			state.Integrations[recordIndex].Status = batchIntegrationDeferred
			state.Integrations[recordIndex].DeferredReason = deferral.Reason
			state.Integrations[recordIndex].ConflictFiles = append([]string(nil), deferral.files...)
			state.Integrations[recordIndex].VerificationOutput = deferral.output
			markBatchCandidateDeferred(&state, candidate.PlanID, deferral.BatchDeferral)
			result.Deferred = append(result.Deferred, deferral.BatchDeferral)
			if !options.DryRun {
				state, err = b.persist(state)
				if err != nil {
					return result, fmt.Errorf("persist deferral for %s: %w", candidate.PlanID, err)
				}
			}
			continue
		}

		head, revErr := git.RevParse(ctx, "HEAD")
		if revErr != nil {
			_ = restoreBatchIntegration(ctx, git, priorHead)
			return result, fmt.Errorf("capture integration commit for %s: %w", candidate.PlanID, revErr)
		}
		state.Integrations[recordIndex].Status = batchIntegrationApplied
		state.Integrations[recordIndex].IntegrationSHA = strings.TrimSpace(head)
		state.IntegrationHead = strings.TrimSpace(head)
		if !options.DryRun {
			state, err = b.persist(state)
			if err != nil {
				rollbackErr := restoreBatchIntegration(ctx, git, priorHead)
				return result, errors.Join(fmt.Errorf("persist integration commit for %s: %w", candidate.PlanID, err), rollbackErr)
			}
		}
		result.Applied = append(result.Applied, candidate.PlanID)
	}

	plannedDeferrals := appendPlannedBatchDeferrals(&state)
	result.Deferred = append(result.Deferred, plannedDeferrals...)
	if len(plannedDeferrals) != 0 && !options.DryRun {
		state, err = b.persist(state)
		if err != nil {
			return result, fmt.Errorf("persist planned integration deferrals: %w", err)
		}
	}

	if options.DryRun {
		if err := restoreBatchIntegration(ctx, git, startingHead); err != nil {
			return result, err
		}
	} else {
		state.Status = batchIntegrationResultStatus(state)
		if state.Status == BatchStatusReviewing && state.Ejection != nil && state.Ejection.Status == batchEjectionReintegrating {
			state.Ejection.Status = batchEjectionCompleted
		}
		state, err = b.persist(state)
		if err != nil {
			return result, fmt.Errorf("persist integration result: %w", err)
		}
	}
	result.State = state
	return result, nil
}

func (b BatchIntegrator) prepareCandidateMessages(ctx context.Context, git GitClient, state BatchState) (BatchState, error) {
	for i := range state.Candidates {
		candidate := &state.Candidates[i]
		if candidate.Deferred != nil && state.Ejection != nil && state.Ejection.PlanID == candidate.PlanID {
			continue
		}
		integrationIndex := batchIntegrationIndex(state, candidate.PlanID)
		if integrationIndex >= 0 {
			integration := state.Integrations[integrationIndex]
			if integration.Status == batchIntegrationApplied {
				continue
			}
			if integration.Status == batchIntegrationApplying {
				// Old applying records predate exact message intent. Their historical
				// deterministic trailer recovery remains authoritative.
				if integration.CommitMessage == "" {
					continue
				}
				if err := validateBatchCommitMessage(integration.CommitMessage, *candidate); err != nil {
					return b.blockMessagePreparation(state, candidate.PlanID, err)
				}
				continue
			}
		}

		if err := validateBatchCandidateBinding(ctx, git, state, *candidate); err != nil {
			return b.blockMessagePreparation(state, candidate.PlanID, err)
		}
		message := candidate.CommitMessage
		if candidate.ReviewCommitMessage != nil && !candidate.CommitMessageResolved {
			expected, err := singleMergeCommitMessage(*candidate.ReviewCommitMessage, candidate.PlanID, candidate.SourceTip)
			if err != nil {
				return b.blockMessagePreparation(state, candidate.PlanID, fmt.Errorf("approved review commit proposal is invalid: %w", err))
			}
			if message != "" && message != expected {
				return b.blockMessagePreparation(state, candidate.PlanID, errors.New("prepared commit message drifted from the immutable approved review proposal"))
			}
			message = expected
		} else if message == "" {
			var err error
			message, err = b.Service.generateSingleMergeMessage(ctx, git, commitpkg.MergeProposalContext{
				RepoRoot: git.Root(), PlanID: candidate.PlanID, DefaultBranch: state.DefaultBranch,
				DefaultParent: state.DefaultStartSHA, SourceBranch: candidate.Branch, SourceHead: candidate.SourceTip,
			})
			if err != nil {
				return b.blockMessagePreparation(state, candidate.PlanID, err)
			}
		}
		if err := validateBatchCommitMessage(message, *candidate); err != nil {
			return b.blockMessagePreparation(state, candidate.PlanID, err)
		}
		if candidate.CommitMessage == message {
			continue
		}
		candidate.CommitMessage = message
		var err error
		state, err = b.persist(state)
		if err != nil {
			return state, fmt.Errorf("persist commit message for batch candidate %s: %w", candidate.PlanID, err)
		}
	}
	return state, nil
}

func validateBatchCandidateBinding(ctx context.Context, git GitClient, state BatchState, candidate BatchCandidate) error {
	defaultHead, err := git.RevParse(ctx, state.DefaultBranch)
	if err != nil {
		return fmt.Errorf("inspect default branch: %w", err)
	}
	if strings.TrimSpace(defaultHead) != state.DefaultStartSHA {
		return fmt.Errorf("default branch drifted from batch snapshot %s", state.DefaultStartSHA)
	}
	sourceHead, err := git.RevParse(ctx, candidate.Branch)
	if err != nil {
		return fmt.Errorf("inspect source branch: %w", err)
	}
	if strings.TrimSpace(sourceHead) != candidate.SourceTip {
		return fmt.Errorf("source branch drifted from candidate snapshot %s", candidate.SourceTip)
	}
	if candidate.ReviewHead != "" && candidate.ReviewHead != candidate.SourceTip {
		return fmt.Errorf("approved review head %s does not match candidate source %s", candidate.ReviewHead, candidate.SourceTip)
	}
	if candidate.ReviewBase != "" {
		base, err := git.MergeBase(ctx, state.DefaultBranch, candidate.Branch)
		if err != nil {
			return fmt.Errorf("inspect approved review base: %w", err)
		}
		if strings.TrimSpace(base) != candidate.ReviewBase {
			return fmt.Errorf("approved review base %s does not match live merge base %s", candidate.ReviewBase, strings.TrimSpace(base))
		}
	}
	return nil
}

func validateBatchCommitMessage(message string, candidate BatchCandidate) error {
	if err := commitpkg.ValidateMessage(message); err != nil {
		return fmt.Errorf("batch candidate commit message is invalid: %w", err)
	}
	if !taoSquashMessageMatches(message, candidate.PlanID, candidate.SourceTip) {
		return errors.New("batch candidate commit message does not carry exact Tao plan/source trailers")
	}
	return nil
}

func (b BatchIntegrator) blockMessagePreparation(state BatchState, planID string, cause error) (BatchState, error) {
	reason := fmt.Sprintf("prepare commit message for batch candidate %s: %v", planID, cause)
	BlockBatch(&state, BatchBlockKindResumable, reason)
	persisted, err := b.persist(state)
	if err != nil {
		return state, errors.Join(errors.New(reason), fmt.Errorf("persist resumable message block: %w", err))
	}
	return persisted, errors.New(reason)
}

type batchCandidateDeferral struct {
	BatchDeferral
	files  []string
	output string
}

func applyingBatchIntegrationIndex(state BatchState, planID string) int {
	for i := range state.Integrations {
		if state.Integrations[i].PlanID == planID && state.Integrations[i].Status == batchIntegrationApplying {
			return i
		}
	}
	return -1
}

// recoverApplyingBatchIntegration is the single crash-mid-squash resume oracle.
// Its three call sites are in batch_integrate.go, batch_agent.go, and
// batch_workspace.go; the workspace call uses the revision-aware helper below.
func recoverApplyingBatchIntegration(ctx context.Context, git GitClient, candidate BatchCandidate, integration BatchIntegration, currentHead string) (string, bool, error) {
	return recoverApplyingBatchIntegrationAtRevision(ctx, git, candidate, integration, currentHead, "HEAD")
}

func recoverApplyingBatchIntegrationAtRevision(ctx context.Context, git GitClient, candidate BatchCandidate, integration BatchIntegration, currentHead, revision string) (string, bool, error) {
	base := strings.TrimSpace(integration.IntegrationBaseSHA)
	if integration.SourceHead != candidate.SourceTip || base == "" {
		return "", false, fmt.Errorf("recover integration intent for %s: persisted intent does not match immutable candidate", candidate.PlanID)
	}
	if currentHead == base {
		return base, false, nil
	}
	parent, err := git.RevParse(ctx, revision+"^")
	if err != nil {
		return "", false, fmt.Errorf("recover integration intent for %s: inspect commit parent: %w", candidate.PlanID, err)
	}
	message, err := git.CommitMessage(ctx, revision)
	if err != nil {
		return "", false, fmt.Errorf("recover integration intent for %s: inspect commit message: %w", candidate.PlanID, err)
	}
	messageMatches := taoSquashMessageMatches(message, candidate.PlanID, candidate.SourceTip)
	if integration.CommitMessage != "" {
		messageMatches = strings.TrimSpace(message) == integration.CommitMessage
	}
	if strings.TrimSpace(parent) != base || !messageMatches {
		return "", false, fmt.Errorf("recover integration intent for %s: integration HEAD does not match the intended Tao-owned squash", candidate.PlanID)
	}
	return base, true, nil
}

func (b BatchIntegrator) deferCandidate(ctx context.Context, git GitClient, candidate BatchCandidate, priorHead, reason string, files []string, output string) *batchCandidateDeferral {
	if err := restoreBatchIntegration(ctx, git, priorHead); err != nil {
		reason += "; restore failed: " + err.Error()
	}
	return &batchCandidateDeferral{BatchDeferral: BatchDeferral{PlanID: candidate.PlanID, Reason: reason}, files: files, output: boundMergeVerifyOutput(output)}
}

func (b BatchIntegrator) applyCandidate(ctx context.Context, git GitClient, candidate BatchCandidate, message, priorHead string, options BatchIntegrateOptions) *batchCandidateDeferral {
	deferCandidate := func(reason string, files []string) *batchCandidateDeferral {
		return b.deferCandidate(ctx, git, candidate, priorHead, reason, files, "")
	}
	if err := git.MergeSquash(ctx, candidate.SourceTip); err != nil {
		files := collectConflictFiles(ctx, git)
		return deferCandidate("squash conflict: "+err.Error(), files)
	}
	changed, err := git.HasStagedChanges(ctx)
	if err != nil {
		return deferCandidate("inspect squash result: "+err.Error(), nil)
	}
	if !changed {
		return deferCandidate("candidate produces no changes", nil)
	}
	if err := git.Commit(ctx, message); err != nil {
		return deferCandidate("create squash commit: "+err.Error(), nil)
	}
	return b.verifyAppliedCandidate(ctx, git, candidate, priorHead, options)
}

func (b BatchIntegrator) verifyAppliedCandidate(ctx context.Context, git GitClient, candidate BatchCandidate, priorHead string, options BatchIntegrateOptions) *batchCandidateDeferral {
	resolution := resolveMergeVerifyCommandAtRoot(git.Root(), Options{VerifyCommand: options.VerifyCommand})
	if resolution.command == "" {
		return nil
	}
	output, err := b.Service.runMergeVerifyAtRoot(ctx, git.Root(), resolution.command)
	if err != nil {
		return b.deferCandidate(ctx, git, candidate, priorHead, "verification failed: "+err.Error(), nil, output)
	}
	return nil
}

func (b BatchIntegrator) persist(state BatchState) (BatchState, error) {
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	state.UpdatedAt = now.Format(time.RFC3339Nano)
	return b.Store.Transition(state, state.UpdatedAt)
}

func orderBatchIntegrationsForResolution(state *BatchState, nextPlanID, pendingBase string) int {
	if nextPlanID != "" && batchIntegrationIndex(*state, nextPlanID) < 0 {
		return -1
	}
	ordered := make([]BatchIntegration, 0, len(state.Integrations))
	for _, integration := range state.Integrations {
		if integration.IntegrationSHA != "" {
			ordered = append(ordered, integration)
		}
	}
	nextIndex := -1
	if nextPlanID != "" {
		for _, integration := range state.Integrations {
			if integration.IntegrationSHA == "" && integration.PlanID == nextPlanID {
				nextIndex = len(ordered)
				ordered = append(ordered, integration)
				break
			}
		}
	}
	for _, integration := range state.Integrations {
		if integration.IntegrationSHA == "" && integration.PlanID != nextPlanID {
			ordered = append(ordered, integration)
		}
	}
	for i := range ordered {
		if ordered[i].IntegrationSHA == "" {
			ordered[i].IntegrationBaseSHA = pendingBase
		}
	}
	state.Integrations = ordered
	return nextIndex
}

func appendPlannedBatchDeferrals(state *BatchState) []BatchDeferral {
	var appended []BatchDeferral
	for _, candidate := range effectiveBatchCandidates(*state) {
		if candidate.Deferred == nil || batchIntegrationIndex(*state, candidate.PlanID) >= 0 {
			continue
		}
		record := BatchIntegration{
			PlanID:             candidate.PlanID,
			SourceHead:         candidate.SourceTip,
			IntegrationBaseSHA: state.IntegrationHead,
			CommitMessage:      candidate.CommitMessage,
			Status:             batchIntegrationDeferred,
			DeferredReason:     candidate.Deferred.Reason,
		}
		state.Integrations = append(state.Integrations, record)
		appended = append(appended, *candidate.Deferred)
	}
	return appended
}

func batchIntegrationResultStatus(state BatchState) BatchStatus {
	effective := make(map[string]bool, len(state.Candidates))
	for _, candidate := range effectiveBatchCandidates(state) {
		effective[candidate.PlanID] = true
	}
	applied := make(map[string]bool, len(effective))
	for _, integration := range state.Integrations {
		if !effective[integration.PlanID] {
			continue
		}
		switch integration.Status {
		case batchIntegrationDeferred:
			return BatchStatusResolving
		case batchIntegrationApplied:
			applied[integration.PlanID] = true
		}
	}
	for planID := range effective {
		if !applied[planID] {
			return BatchStatusIntegrating
		}
	}
	return BatchStatusReviewing
}

func orderedBatchCandidates(state BatchState) []BatchCandidate {
	settled := make(map[string]bool, len(state.Integrations))
	for _, integration := range state.Integrations {
		if integration.Status == batchIntegrationApplied || integration.Status == batchIntegrationDeferred {
			settled[integration.PlanID] = true
		}
	}
	byID := make(map[string]BatchCandidate, len(state.Candidates))
	for _, candidate := range state.Candidates {
		if !settled[candidate.PlanID] {
			byID[candidate.PlanID] = candidate
		}
	}
	order := append([]string(nil), state.ChosenOrder...)
	if len(order) == 0 {
		for id := range byID {
			order = append(order, id)
		}
		sort.Strings(order)
	}
	result := make([]BatchCandidate, 0, len(order))
	for _, id := range order {
		if candidate, ok := byID[id]; ok && candidate.Deferred == nil {
			result = append(result, candidate)
		}
	}
	return result
}

func markBatchCandidateDeferred(state *BatchState, planID string, deferral BatchDeferral) {
	for i := range state.Candidates {
		if state.Candidates[i].PlanID == planID {
			state.Candidates[i].Deferred = &deferral
			return
		}
	}
}

func restoreBatchIntegration(ctx context.Context, git GitClient, head string) error {
	return errors.Join(git.ResetHard(ctx, head), git.CleanUntracked(ctx))
}

func batchSquashCommitMessage(candidate BatchCandidate) string {
	title := strings.TrimSpace(candidate.PlanTitle)
	if title == "" {
		title = "Tao plan " + strings.TrimSpace(candidate.PlanID)
	}
	return fmt.Sprintf("%s\n\nTao-Plan: %s\nTao-Source-Head: %s", title, strings.TrimSpace(candidate.PlanID), strings.TrimSpace(candidate.SourceTip))
}
