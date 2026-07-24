package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// BatchSettlementWorkspace owns only the batch integration namespace and the
// active-batch identity. Source cleanup remains behind Service.Cleanup.
type BatchSettlementWorkspace interface {
	RemoveIntegration(context.Context, string) error
	ClearActive(string) error
}

// BatchSettler replays all post-landing effects from durable landing intent.
// Recording is completed for every plan before any source ref is removed.
type BatchSettler struct {
	Store      BatchTransitionStore
	Service    Service
	Repository BatchLandResolver
	Workspace  BatchSettlementWorkspace
	Now        func() time.Time
}

// BatchSettleResult reports durable per-plan outcomes, including safe declines
// that require operator attention but do not prevent the transaction closing.
type BatchSettleResult struct {
	State BatchState
}

// Settle records missing merge evidence, safely cleans every source, then
// removes the integration namespace and active identity. Every step is
// idempotent and a failed unsafe cleanup leaves its source evidence untouched.
func (s BatchSettler) Settle(ctx context.Context, state BatchState) (BatchSettleResult, error) {
	result := BatchSettleResult{State: state}
	if s.Store == nil || s.Repository == nil || s.Workspace == nil {
		return result, errors.New("batch settlement store, plan resolver, and workspace are required")
	}
	if state.Landing == nil || state.LandedSHA == "" {
		return result, errors.New("batch settlement requires durable landing intent and landed SHA")
	}
	if state.Status != BatchStatusLanded && state.Status != BatchStatusSettling && state.Status != BatchStatusCompleted {
		return result, fmt.Errorf("batch settlement requires landed, settling, or completed status, got %s", state.Status)
	}
	git, err := s.Service.gitClient()
	if err != nil {
		return result, err
	}
	defaultTip, err := cleanRev(ctx, git, state.DefaultBranch)
	if err != nil {
		return result, fmt.Errorf("inspect landed default: %w", err)
	}
	if ok, ancestorErr := git.IsAncestor(ctx, state.LandedSHA, defaultTip); ancestorErr != nil || !ok {
		if ancestorErr != nil {
			return result, fmt.Errorf("verify landed transaction on default: %w", ancestorErr)
		}
		return result, fmt.Errorf("landed transaction %s is not an ancestor of default %s", state.LandedSHA, defaultTip)
	}
	for _, item := range state.Landing.Plans {
		ok, ancestorErr := git.IsAncestor(ctx, item.SquashSHA, state.LandedSHA)
		if ancestorErr != nil {
			return result, fmt.Errorf("verify plan %s squash evidence: %w", item.PlanID, ancestorErr)
		}
		if !ok {
			return result, fmt.Errorf("plan %s squash %s is not an ancestor of landed default %s", item.PlanID, item.SquashSHA, state.LandedSHA)
		}
		candidate := candidateByID(state.Candidates, item.PlanID)
		if candidate == nil {
			return result, fmt.Errorf("landing intent names unknown plan %s", item.PlanID)
		}
		message, messageErr := git.CommitMessage(ctx, item.SquashSHA)
		if messageErr != nil {
			return result, fmt.Errorf("read plan %s squash evidence: %w", item.PlanID, messageErr)
		}
		messageMatches := taoSquashMessageMatches(message, item.PlanID, candidate.SourceTip)
		if integrationIndex := batchIntegrationIndex(state, item.PlanID); integrationIndex >= 0 && state.Integrations[integrationIndex].CommitMessage != "" {
			messageMatches = strings.TrimSpace(message) == state.Integrations[integrationIndex].CommitMessage
		}
		if !messageMatches {
			return result, fmt.Errorf("plan %s squash %s does not carry matching Tao plan/source evidence", item.PlanID, item.SquashSHA)
		}
	}

	state = initializeBatchSettlement(state)
	if state.Status == BatchStatusLanded {
		state.Status = BatchStatusSettling
		state.BlockedReason, state.BlockKind, state.ResumeStatus = "", "", ""
		state, err = s.persist(state)
		if err != nil {
			return result, fmt.Errorf("persist batch settlement start: %w", err)
		}
	}

	var recordingErrs []error
	for _, item := range state.Landing.Plans {
		index := settlementIndex(state.Settlement, item.PlanID)
		if state.Settlement[index].MergeEvidenceRecorded {
			continue
		}
		detail, resolveErr := s.Repository.ResolvePlan(ctx, candidatePlanInput(state, item.PlanID))
		if resolveErr == nil && !hasExactMergedEvidence(detail, item.SquashSHA) {
			candidate := candidateByID(state.Candidates, item.PlanID)
			if candidate == nil {
				resolveErr = fmt.Errorf("landing intent names unknown plan %s", item.PlanID)
			} else {
				resolveErr = s.Service.AppendPlanMergedEvent(detail, candidate.Branch, item.SquashSHA)
			}
		}
		if resolveErr != nil {
			state.Settlement[index].Error = resolveErr.Error()
			recordingErrs = append(recordingErrs, fmt.Errorf("record plan %s merge evidence: %w", item.PlanID, resolveErr))
		} else {
			state.Settlement[index].MergeEvidenceRecorded = true
			state.Settlement[index].Error = ""
		}
		state, err = s.persist(state)
		if err != nil {
			return BatchSettleResult{State: state}, fmt.Errorf("persist plan %s recording outcome: %w", item.PlanID, err)
		}
	}
	if len(recordingErrs) != 0 {
		return BatchSettleResult{State: state}, errors.Join(recordingErrs...)
	}

	var cleanupErrs []error
	for _, item := range state.Landing.Plans {
		index := settlementIndex(state.Settlement, item.PlanID)
		settlement := &state.Settlement[index]
		if settlement.Completed {
			continue
		}
		detail, resolveErr := s.Repository.ResolvePlan(ctx, candidatePlanInput(state, item.PlanID))
		if resolveErr != nil {
			settlement.Error = resolveErr.Error()
			cleanupErrs = append(cleanupErrs, fmt.Errorf("reload plan %s for cleanup: %w", item.PlanID, resolveErr))
		} else {
			options := Options{allowNonAncestralCleanup: true}
			_, cleanErr := s.Service.Cleanup(ctx, detail, options)
			switch {
			case cleanErr == nil || cleanupAlreadySettled(cleanErr):
				settlement.WorkspaceCleaned = true
				settlement.BranchCleaned = true
				settlement.Completed = true
				settlement.RequiresAttention = false
				settlement.Error = ""
			default:
				settlement.Error = cleanErr.Error()
				if errors.Is(cleanErr, ErrCleanupDeclined) {
					// Dirty/current/protected decisions are explicit safe outcomes:
					// retain the source and close the batch with actionable audit data.
					settlement.RequiresAttention = true
					settlement.Completed = true
				} else {
					cleanupErrs = append(cleanupErrs, fmt.Errorf("clean plan %s: %w", item.PlanID, cleanErr))
				}
			}
		}
		state, err = s.persist(state)
		if err != nil {
			return BatchSettleResult{State: state}, fmt.Errorf("persist plan %s cleanup outcome: %w", item.PlanID, err)
		}
	}
	if len(cleanupErrs) != 0 {
		return BatchSettleResult{State: state}, errors.Join(cleanupErrs...)
	}
	for _, settlement := range state.Settlement {
		if !settlement.MergeEvidenceRecorded || !settlement.Completed {
			return BatchSettleResult{State: state}, errors.New("batch settlement remains incomplete")
		}
	}

	if state.Status != BatchStatusCompleted {
		state.Status = BatchStatusCompleted
		state.BlockedReason, state.BlockKind, state.ResumeStatus = "", "", ""
		state, err = s.persist(state)
		if err != nil {
			return BatchSettleResult{State: state}, fmt.Errorf("persist completed batch settlement: %w", err)
		}
	}
	if state.Finalization == nil {
		state.Finalization = &BatchFinalization{}
	}
	if !state.Finalization.IntegrationCleaned {
		if err := s.Workspace.RemoveIntegration(ctx, state.ID); err != nil {
			state.Finalization.Error = err.Error()
			persisted, persistErr := s.persist(state)
			if persistErr == nil {
				state = persisted
			}
			return BatchSettleResult{State: state}, errors.Join(fmt.Errorf("clean batch integration namespace: %w", err), persistErr)
		}
		state.Finalization.IntegrationCleaned = true
		state.Finalization.Error = ""
		state, err = s.persist(state)
		if err != nil {
			return BatchSettleResult{State: state}, fmt.Errorf("persist batch integration cleanup: %w", err)
		}
	}
	if err := s.Workspace.ClearActive(state.ID); err != nil {
		return BatchSettleResult{State: state}, fmt.Errorf("clear completed active merge batch: %w", err)
	}
	return BatchSettleResult{State: state}, nil
}

func (s BatchSettler) persist(state BatchState) (BatchState, error) {
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	state.UpdatedAt = now.Format(time.RFC3339Nano)
	return s.Store.Transition(state, state.UpdatedAt)
}

func hasExactMergedEvidence(detail *plan.PlanDetail, squashSHA string) bool {
	if detail == nil {
		return false
	}
	for _, event := range detail.Events {
		if event.Type == plan.EventTypePlanMerged && strings.TrimSpace(event.MergedDefaultSHA) == strings.TrimSpace(squashSHA) {
			return true
		}
	}
	return false
}
