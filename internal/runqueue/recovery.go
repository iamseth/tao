package runqueue

import (
	"context"
	"errors"
	"slices"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
)

// RecoveryRepository is the persisted-plan lookup needed to interpret an
// interrupted queue entry.
type RecoveryRepository interface {
	ResolvePlan(context.Context, string) (*plan.PlanDetail, error)
}

// NewRecoveryInspector returns a repository-backed interpretation of plan
// lifecycle, review, and automatic-rework progress.
func NewRecoveryInspector(repo RecoveryRepository) RecoveryInspector {
	return func(ctx context.Context, planID string) (RecoveryInspection, error) {
		if repo == nil {
			return RecoveryInspection{}, errors.New("queue recovery inspector requires repository")
		}
		detail, err := repo.ResolvePlan(ctx, planID)
		if err != nil {
			return RecoveryInspection{}, err
		}

		currentReview := plan.CurrentReview(detail)
		slicesComplete := plan.AnalyzeLifecycle(detail).Complete
		inspection := RecoveryInspection{
			SlicesComplete: slicesComplete,
			ReviewPending:  slicesComplete && reviewPending(detail, currentReview),
			TerminalReview: currentReview != nil && currentReview.Status == plan.ReviewStatusCompleted && (currentReview.Verdict == plan.ReviewVerdictApprove || currentReview.Verdict == plan.ReviewVerdictChangesRequested),
			ReworkRound:    reworkpkg.RoundCount(detail),
		}
		if currentReview == nil && inspection.ReworkRound > 0 {
			persistedReview := plan.PersistedReview(detail)
			findings := reworkpkg.ReviewFindings(detail)
			if persistedReview != nil && persistedReview.Status == plan.ReviewStatusCompleted && persistedReview.Verdict == plan.ReviewVerdictChangesRequested && len(findings) > 0 {
				inspection.PreviousFindingFingerprint = reworkpkg.ReworkFindingsFingerprint(findings)
			}
		}
		return inspection, nil
	}
}

func reviewPending(detail *plan.PlanDetail, current *plan.PlanReview) bool {
	if current != nil {
		return false
	}
	persisted := plan.PersistedReview(detail)
	if persisted == nil || persisted.Status != plan.ReviewStatusError {
		return true
	}
	for _, event := range slices.Backward(detail.Events) {
		switch event.Type {
		case plan.EventTypePlanReviewed:
			return event.Review != nil && event.Review.Status != plan.ReviewStatusError
		case plan.EventTypePlanReopened:
			return true
		}
	}
	// Legacy plans may persist review error metadata without an event. Treat it
	// as an already-attempted best-effort review, matching ordinary run behavior.
	return false
}
