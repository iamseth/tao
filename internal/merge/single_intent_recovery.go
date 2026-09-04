package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

// SingleMergeIntentPhase is the recovery-relevant durable phase of a
// single-plan merge intent.
type SingleMergeIntentPhase string

const (
	SingleMergeIntentPhaseUnresolved                 SingleMergeIntentPhase = "unresolved"
	SingleMergeIntentPhaseRequestedPreUsableProvider SingleMergeIntentPhase = "requested_pre_usable_provider"
	SingleMergeIntentPhaseRequestedAmbiguous         SingleMergeIntentPhase = "requested_ambiguous"
	SingleMergeIntentPhaseResolved                   SingleMergeIntentPhase = "resolved"
	SingleMergeIntentPhaseCommitted                  SingleMergeIntentPhase = "committed"
	SingleMergeIntentPhaseReviewed                   SingleMergeIntentPhase = "reviewed"
	SingleMergeIntentPhaseRolledBack                 SingleMergeIntentPhase = "rolled_back"
)

// SingleMergeIntentRecoveryVerdict identifies the only safe next class of
// action for a durable single-plan merge intent.
type SingleMergeIntentRecoveryVerdict string

const (
	SingleMergeIntentRecoveryRestartable              SingleMergeIntentRecoveryVerdict = "restartable"
	SingleMergeIntentRecoveryRebaseAndReviewRequired  SingleMergeIntentRecoveryVerdict = "rebase-and-review-required"
	SingleMergeIntentRecoverySettleExistingResolution SingleMergeIntentRecoveryVerdict = "settle-existing-resolution"
	SingleMergeIntentRecoveryManualOnly               SingleMergeIntentRecoveryVerdict = "manual-only"
)

// SingleMergeIntentLiveState is a caller's read-only projection of live Git.
// Booleans that assert exactness must be established by the caller rather than
// inferred from ancestry.
type SingleMergeIntentLiveState struct {
	PlanID              string
	DefaultBranch       string
	SourceHead          string
	LiveDefault         string
	SourceBranchExists  bool
	DefaultBranchExists bool
	DefaultAdvanced     bool
	DefaultRewound      bool
	// Dirty reports uncommitted changes in the integration worktree only.
	// ExactResolutionEdits can explain this dirt; it never explains
	// PlanWorktreeDirty, which is a separate worktree the resolution does
	// not touch. Keep the two signals distinct so exactness and cleanliness
	// are not decided by one overloaded boolean.
	Dirty                bool
	PlanWorktreeDirty    bool
	ActiveOperation      string
	ExactIntendedSquash  bool
	ExactResolutionEdits bool
	ProviderWasUsable    bool
	OwnershipUnsafe      bool
}

// SingleMergeIntentRecovery is the domain decision for an intent and live Git
// snapshot.
type SingleMergeIntentRecovery struct {
	Phase   SingleMergeIntentPhase
	Verdict SingleMergeIntentRecoveryVerdict
	Reason  string
}

// SingleMergeRestartResult describes either the exact stale transaction that
// was discarded or an exact intended squash that was settled instead.
type SingleMergeRestartResult struct {
	Recovery         SingleMergeIntentRecovery
	Discarded        *plan.SingleMergeCommitIntent
	MergedDefaultSHA string
	NextAction       string
}

func latestSingleMergeRestart(detail *plan.PlanDetail) *plan.Event {
	if detail == nil {
		return nil
	}
	var restarted *plan.Event
	for i := range detail.Events {
		event := &detail.Events[i]
		if event.Type == plan.EventTypeSingleMergeIntentRestarted && (restarted == nil || event.Timestamp.After(restarted.Timestamp)) {
			restarted = event
		}
	}
	return restarted
}

// pendingSingleMergeRestart keeps a restart settlement active until a later
// completed review covers the live source tip and Git proves that tip contains
// the default head recorded by the restart. A changed review head alone is not
// evidence that the required rebase happened.
func pendingSingleMergeRestart(ctx context.Context, git GitClient, detail *plan.PlanDetail) (*plan.Event, error) {
	restarted := latestSingleMergeRestart(detail)
	if restarted == nil {
		return nil, nil
	}
	review := plan.CurrentReview(detail)
	reviewHead := ""
	if review != nil && review.Status == plan.ReviewStatusCompleted && review.ReviewedAt.After(restarted.Timestamp) {
		reviewHead = strings.TrimSpace(review.Head)
	}
	if reviewHead == "" || reviewHead == strings.TrimSpace(restarted.PriorHead) {
		return restarted, nil
	}
	branch := strings.TrimSpace(restarted.Branch)
	baseline := strings.TrimSpace(restarted.BaselineHead)
	if branch == "" || baseline == "" {
		return restarted, nil
	}
	sourceHead, err := git.RevParse(ctx, "refs/heads/"+branch)
	if err != nil {
		return restarted, fmt.Errorf("resolve restarted single-merge source %s: %w", branch, err)
	}
	if strings.TrimSpace(sourceHead) != reviewHead {
		return restarted, nil
	}
	containsBaseline, err := git.IsAncestor(ctx, baseline, reviewHead)
	if err != nil {
		return restarted, fmt.Errorf("verify restarted single-merge source contains baseline %s: %w", baseline, err)
	}
	if !containsBaseline {
		return restarted, nil
	}
	return nil, nil
}

func singleMergeRestartInstruction(event plan.Event) string {
	return fmt.Sprintf("rebase %s onto %s, then run tao review --run %s", event.Branch, event.BaselineBranch, event.PlanID)
}

// ClassifySingleMergeIntentRecovery applies the phase x live-Git recovery
// table without mutating either plan state or Git.
func ClassifySingleMergeIntentRecovery(intent plan.SingleMergeCommitIntent, live SingleMergeIntentLiveState) SingleMergeIntentRecovery {
	phase := singleMergeIntentPhase(intent, live)
	result := func(verdict SingleMergeIntentRecoveryVerdict, reason string) SingleMergeIntentRecovery {
		return SingleMergeIntentRecovery{Phase: phase, Verdict: verdict, Reason: reason}
	}

	// Exact landing is authoritative only when the caller proved both the
	// recorded parent and canonical message. It precedes all eligibility gates.
	if live.ExactIntendedSquash {
		return result(SingleMergeIntentRecoverySettleExistingResolution, "the exact intended squash is already present")
	}
	if strings.TrimSpace(live.PlanID) != intent.PlanID || strings.TrimSpace(live.DefaultBranch) != intent.DefaultBranch {
		return result(SingleMergeIntentRecoveryManualOnly, "the live repository identity does not match the durable intent")
	}
	if !live.SourceBranchExists || !live.DefaultBranchExists {
		return result(SingleMergeIntentRecoveryManualOnly, "a branch recorded by the durable intent is missing")
	}
	if live.OwnershipUnsafe {
		return result(SingleMergeIntentRecoveryManualOnly, "the recorded branch or worktree ownership is not healthy")
	}
	if strings.TrimSpace(live.SourceHead) != intent.SourceHead {
		return result(SingleMergeIntentRecoveryManualOnly, "the source branch moved from the durable intent head")
	}
	if operation := strings.TrimSpace(live.ActiveOperation); operation != "" {
		return result(SingleMergeIntentRecoveryManualOnly, "a Git "+operation+" operation is in progress")
	}
	// The durable resolution only ever authorizes edits in the integration
	// worktree, so unrelated plan-worktree dirt is never explained evidence
	// and must be inspected before exactness can authorize settlement.
	if live.PlanWorktreeDirty {
		return result(SingleMergeIntentRecoveryManualOnly, "the plan worktree has uncommitted changes the durable resolution does not explain")
	}
	if phase == SingleMergeIntentPhaseResolved && live.ExactResolutionEdits {
		return result(SingleMergeIntentRecoverySettleExistingResolution, "exact durable resolution edits must be settled")
	}
	if live.Dirty {
		return result(SingleMergeIntentRecoveryManualOnly, "the integration worktree is dirty")
	}
	if live.DefaultRewound || (!live.DefaultAdvanced && strings.TrimSpace(live.LiveDefault) != intent.DefaultParent) {
		return result(SingleMergeIntentRecoveryManualOnly, "the default branch was rewound or diverged from the durable parent")
	}

	switch phase {
	case SingleMergeIntentPhaseUnresolved:
		if live.DefaultAdvanced {
			return result(SingleMergeIntentRecoveryRestartable, "the source is unchanged and the default branch advanced cleanly")
		}
		return result(SingleMergeIntentRecoverySettleExistingResolution, "the clean unresolved intent remains at its recorded source and default boundary")
	case SingleMergeIntentPhaseRequestedPreUsableProvider:
		return result(SingleMergeIntentRecoveryManualOnly, "the resolution request was recorded before a usable provider was established")
	case SingleMergeIntentPhaseRequestedAmbiguous:
		return result(SingleMergeIntentRecoveryManualOnly, "the resolution provider may have run, so its effects are ambiguous")
	case SingleMergeIntentPhaseResolved:
		return result(SingleMergeIntentRecoveryManualOnly, "resolved evidence does not match the live worktree")
	case SingleMergeIntentPhaseCommitted, SingleMergeIntentPhaseReviewed:
		return result(SingleMergeIntentRecoverySettleExistingResolution, "durable committed resolution authority must be settled")
	case SingleMergeIntentPhaseRolledBack:
		return result(SingleMergeIntentRecoveryRebaseAndReviewRequired, "the prior resolution was rolled back; rebase and refresh the review")
	default:
		return result(SingleMergeIntentRecoveryManualOnly, "the durable intent phase is not recognized")
	}
}

func singleMergeIntentPhase(intent plan.SingleMergeCommitIntent, live SingleMergeIntentLiveState) SingleMergeIntentPhase {
	if intent.Resolution == nil {
		return SingleMergeIntentPhaseUnresolved
	}
	switch intent.Resolution.Phase {
	case plan.SingleMergeResolutionPhaseRequested:
		if live.ProviderWasUsable {
			return SingleMergeIntentPhaseRequestedAmbiguous
		}
		return SingleMergeIntentPhaseRequestedPreUsableProvider
	case plan.SingleMergeResolutionPhaseResolved:
		return SingleMergeIntentPhaseResolved
	case plan.SingleMergeResolutionPhaseCommitted:
		return SingleMergeIntentPhaseCommitted
	case plan.SingleMergeResolutionPhaseReviewed:
		return SingleMergeIntentPhaseReviewed
	case plan.SingleMergeResolutionPhaseRolledBack:
		return SingleMergeIntentPhaseRolledBack
	default:
		return SingleMergeIntentPhase(intent.Resolution.Phase)
	}
}

var ErrSingleMergeIntentDrift = errors.New("single-merge intent drift")

// SingleMergeIntentDriftError retains the exact recovery boundary while
// preserving the historical user-facing drift message.
type SingleMergeIntentDriftError struct {
	PlanID        string
	DefaultBranch string
	DefaultParent string
	LiveDefault   string
	SourceHead    string
	Phase         SingleMergeIntentPhase
	Verdict       SingleMergeIntentRecoveryVerdict
	Reason        string
}

func (e *SingleMergeIntentDriftError) Error() string {
	return fmt.Sprintf("default branch %s drifted from single-merge intent parent %s and does not contain the exact intended squash", e.DefaultBranch, e.DefaultParent)
}

func (e *SingleMergeIntentDriftError) Is(target error) bool {
	return target == ErrSingleMergeIntentDrift
}
