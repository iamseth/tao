package merge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// BatchLandResolver reloads source plans immediately before their gates and
// again when durable merge evidence is recorded.
type BatchLandResolver interface {
	ResolvePlan(context.Context, string) (*plan.PlanDetail, error)
}

// BatchLander owns the write-ahead intent, the one default ref movement, and
// per-plan merge evidence. Source cleanup is deliberately a later phase.
type BatchLander struct {
	Store      BatchTransitionStore
	Service    Service
	Repository BatchLandResolver
	Health     BatchHealthCheck
	Now        func() time.Time
}

// BatchLandResult reports the newest state that was durably reached.
type BatchLandResult struct {
	State BatchState
}

// Land rechecks all mutable inputs, persists exact intent, fast-forwards the
// checked-out default once, and records each squash commit as plan evidence.
func (l BatchLander) Land(ctx context.Context, state BatchState, integrationRoot string) (BatchLandResult, error) {
	result := BatchLandResult{State: state}
	if l.Store == nil || l.Repository == nil {
		return result, errors.New("batch landing store and plan resolver are required")
	}
	landableBlocked := state.Status == BatchStatusBlocked && !batchBlockIsTerminal(state) && state.Review != nil && state.Review.Verdict == plan.ReviewVerdictApprove
	if state.Status != BatchStatusReadyToLand && state.Status != BatchStatusLanded && !landableBlocked {
		return result, fmt.Errorf("batch landing requires ready_to_land or landed status, got %s", state.Status)
	}
	rootGit, err := l.Service.gitClient()
	if err != nil {
		return result, err
	}
	integrationGit, err := l.Service.gitClientForRoot(integrationRoot)
	if err != nil {
		return result, err
	}

	defaultTip, err := cleanRev(ctx, rootGit, state.DefaultBranch)
	if err != nil {
		return result, err
	}
	if state.Landing != nil {
		expected, intentErr := landingIntentFromState(state)
		if intentErr != nil || !sameLandingIntent(state.Landing, expected) {
			if intentErr != nil {
				return result, fmt.Errorf("invalid durable landing intent: %w", intentErr)
			}
			return result, errors.New("durable landing intent does not match staged batch evidence")
		}
	}
	// A durable intent plus default at the integration head is proof that Git
	// completed before a prior process stopped. Never attempt a second merge.
	alreadyLanded := state.Landing != nil && defaultTip == state.Landing.IntegrationHead
	if !alreadyLanded {
		if state.Landing != nil && defaultTip != state.Landing.DefaultParentSHA {
			return result, fmt.Errorf("default branch drifted after landing intent: expected %s or %s, got %s", state.Landing.DefaultParentSHA, state.Landing.IntegrationHead, defaultTip)
		}
		intent, gateErr := l.finalGate(ctx, state, rootGit, integrationGit)
		if gateErr != nil {
			return l.block(result, state, gateErr.Error())
		}
		if state.Landing == nil {
			state.Landing = intent
			state.BlockedReason, state.BlockKind, state.ResumeStatus = "", "", ""
			state.Status = BatchStatusReadyToLand
			persisted, persistErr := l.persist(state)
			if persistErr != nil {
				return resultWithState(state), fmt.Errorf("persist batch landing intent: %w", persistErr)
			}
			state = persisted
		} else if !sameLandingIntent(state.Landing, intent) {
			return result, errors.New("live landing inputs do not match durable landing intent")
		}
		if err := rootGit.MergeFFOnly(ctx, state.Landing.IntegrationHead); err != nil {
			after, inspectErr := cleanRev(ctx, rootGit, state.DefaultBranch)
			if inspectErr != nil {
				return result, errors.Join(err, fmt.Errorf("prove default after failed fast-forward: %w", inspectErr))
			}
			switch after {
			case state.Landing.IntegrationHead:
				// Git moved the ref but reported failure; settle from durable intent.
			case state.Landing.DefaultParentSHA:
				return result, err
			default:
				return result, fmt.Errorf("%w; default changed unexpectedly to %s", err, after)
			}
		}
	}

	landed, err := cleanRev(ctx, rootGit, state.DefaultBranch)
	if err != nil {
		return result, err
	}
	if state.Landing == nil || landed != state.Landing.IntegrationHead {
		return result, fmt.Errorf("landed default %s does not match intended integration head", landed)
	}
	if state.Status != BatchStatusLanded || state.LandedSHA == "" || state.Landing.LandedDefaultSHA == "" {
		state.Status = BatchStatusLanded
		state.LandedSHA = landed
		state.Landing.LandedDefaultSHA = landed
		state.BlockedReason, state.BlockKind, state.ResumeStatus = "", "", ""
		persisted, persistErr := l.persist(state)
		if persistErr != nil {
			return resultWithState(state), fmt.Errorf("persist landed default: %w", persistErr)
		}
		state = persisted
	}

	state = initializeBatchSettlement(state)
	for _, item := range state.Landing.Plans {
		index := settlementIndex(state.Settlement, item.PlanID)
		if index < 0 || state.Settlement[index].MergeEvidenceRecorded {
			continue
		}
		detail, resolveErr := l.Repository.ResolvePlan(ctx, candidatePlanInput(state, item.PlanID))
		if resolveErr != nil {
			return resultWithState(state), fmt.Errorf("reload plan %s for merge recording: %w", item.PlanID, resolveErr)
		}
		candidate := candidateByID(state.Candidates, item.PlanID)
		if candidate == nil {
			return resultWithState(state), fmt.Errorf("landing intent names unknown plan %s", item.PlanID)
		}
		if err := l.Service.AppendPlanMergedEvent(detail, candidate.Branch, item.SquashSHA); err != nil {
			return resultWithState(state), fmt.Errorf("record plan %s merge evidence: %w", item.PlanID, err)
		}
		state.Settlement[index].MergeEvidenceRecorded = true
		state.Settlement[index].Error = ""
		persisted, persistErr := l.persist(state)
		if persistErr != nil {
			return resultWithState(state), fmt.Errorf("persist plan %s merge settlement: %w", item.PlanID, persistErr)
		}
		state = persisted
	}
	return resultWithState(state), nil
}

func (l BatchLander) finalGate(ctx context.Context, state BatchState, rootGit, integrationGit GitClient) (*BatchLanding, error) {
	if filepath.Clean(rootGit.Root()) != filepath.Clean(state.RepoRoot) {
		return nil, fmt.Errorf("repository root changed: expected %s, got %s", state.RepoRoot, rootGit.Root())
	}
	if err := requireCurrentBranch(ctx, rootGit, state.DefaultBranch, "default"); err != nil {
		return nil, err
	}
	if err := requireCleanWorktree(ctx, rootGit); err != nil {
		return nil, fmt.Errorf("default worktree: %w", err)
	}
	if tip, err := cleanRev(ctx, rootGit, state.DefaultBranch); err != nil || tip != state.DefaultStartSHA {
		return nil, mismatchError("default start", state.DefaultStartSHA, tip, err)
	}
	if err := requireCurrentBranch(ctx, integrationGit, "tao/integration/"+state.ID, "integration"); err != nil {
		return nil, err
	}
	if err := requireCleanWorktree(ctx, integrationGit); err != nil {
		return nil, fmt.Errorf("integration worktree: %w", err)
	}
	if head, err := cleanRev(ctx, integrationGit, "HEAD"); err != nil || head != state.IntegrationHead {
		return nil, mismatchError("integration head", state.IntegrationHead, head, err)
	}
	if state.Verification == nil || !state.Verification.Passed || strings.TrimSpace(state.Verification.Command) == "" || state.Verification.HeadSHA != state.IntegrationHead || state.Verification.CompletedAt == "" {
		return nil, errors.New("final verification does not cover the integration head")
	}
	if state.Review == nil || state.Review.Status != "completed" || state.Review.Verdict != plan.ReviewVerdictApprove || state.Review.BaseSHA != state.DefaultStartSHA || state.Review.HeadSHA != state.IntegrationHead || state.Review.CompletedAt == "" {
		return nil, errors.New("aggregate approval does not cover the integration head and default base")
	}
	if state.Ejection != nil && state.Ejection.Status != batchEjectionCompleted {
		return nil, errors.New("batch ejection rebuild is incomplete")
	}
	if drifts := validatePersistedProgress(state); len(drifts) != 0 {
		return nil, fmt.Errorf("invalid persisted integration progress: %s", drifts[0].Reason)
	}
	if ok, err := rootGit.IsAncestor(ctx, state.DefaultStartSHA, state.IntegrationHead); err != nil || !ok {
		if err != nil {
			return nil, fmt.Errorf("check expected fast-forward: %w", err)
		}
		return nil, errors.New("integration head is not a fast-forward of default start")
	}

	effectiveCandidates := effectiveBatchCandidates(state)
	effectivePlanIDs := make(map[string]bool, len(effectiveCandidates))
	for _, candidate := range effectiveCandidates {
		effectivePlanIDs[candidate.PlanID] = true
	}
	plans := make([]BatchLandingPlan, 0, len(state.Integrations))
	for integrationIndex := range state.Integrations {
		integration := &state.Integrations[integrationIndex]
		id := integration.PlanID
		if !effectivePlanIDs[id] {
			return nil, fmt.Errorf("plan %s is not an effective batch candidate", id)
		}
		candidate := candidateByID(state.Candidates, id)
		if candidate == nil || integration.Status != batchIntegrationApplied || integration.IntegrationSHA == "" {
			return nil, fmt.Errorf("plan %s has no applied squash commit", id)
		}
		detail, err := l.Repository.ResolvePlan(ctx, candidate.PlanDir)
		if err != nil {
			return nil, fmt.Errorf("reload plan %s for final gate: %w", id, err)
		}
		health := l.Health
		if health == nil {
			health = defaultBatchHealthCheck
		}
		if err := health(ctx, detail.State.Repo); err != nil {
			return nil, fmt.Errorf("plan %s repository health: %w", id, err)
		}
		if err := l.Service.CheckPreMergeGate(ctx, detail, Options{}); err != nil {
			return nil, fmt.Errorf("plan %s final merge gate: %w", id, err)
		}
		if tip, err := cleanRev(ctx, rootGit, candidate.Branch); err != nil || tip != candidate.SourceTip {
			return nil, mismatchError("plan "+id+" source tip", candidate.SourceTip, tip, err)
		}
		if candidate.ReviewHead != candidate.SourceTip {
			return nil, fmt.Errorf("plan %s review head no longer covers its source tip", id)
		}
		if base, err := rootGit.MergeBase(ctx, state.DefaultBranch, candidate.Branch); err != nil || strings.TrimSpace(base) != candidate.ReviewBase {
			return nil, mismatchError("plan "+id+" review base", candidate.ReviewBase, strings.TrimSpace(base), err)
		}
		if ok, err := rootGit.IsAncestor(ctx, integration.IntegrationSHA, state.IntegrationHead); err != nil || !ok {
			if err != nil {
				return nil, fmt.Errorf("check plan %s squash ancestry: %w", id, err)
			}
			return nil, fmt.Errorf("plan %s squash commit is not an ancestor of integration head", id)
		}
		plans = append(plans, BatchLandingPlan{PlanID: id, SquashSHA: integration.IntegrationSHA})
	}
	if len(plans) != len(effectiveCandidates) {
		return nil, errors.New("landing order does not contain every effective batch candidate")
	}
	return &BatchLanding{DefaultParentSHA: state.DefaultStartSHA, IntegrationHead: state.IntegrationHead, Plans: plans, AggregateReviewHead: state.Review.HeadSHA, ExpectedFastForward: true}, nil
}

func (l BatchLander) persist(state BatchState) (BatchState, error) {
	now := time.Now().UTC()
	if l.Now != nil {
		now = l.Now().UTC()
	}
	state.UpdatedAt = now.Format(time.RFC3339Nano)
	return l.Store.Transition(state, state.UpdatedAt)
}

func (l BatchLander) block(result BatchLandResult, state BatchState, reason string) (BatchLandResult, error) {
	BlockBatch(&state, BatchBlockKindResumable, reason)
	persisted, err := l.persist(state)
	if err != nil {
		return result, errors.Join(errors.New(reason), err)
	}
	return resultWithState(persisted), errors.New(reason)
}

func cleanRev(ctx context.Context, git GitClient, rev string) (string, error) {
	value, err := git.RevParse(ctx, rev)
	return strings.TrimSpace(value), err
}

func requireCurrentBranch(ctx context.Context, git GitClient, expected, label string) error {
	client, ok := git.(interface {
		CurrentBranch(context.Context) (string, error)
	})
	if !ok {
		return fmt.Errorf("%s Git client cannot report current branch", label)
	}
	actual, err := client.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("inspect %s current branch: %w", label, err)
	}
	if strings.TrimSpace(actual) != expected {
		return fmt.Errorf("%s branch mismatch: expected %s, got %s", label, expected, strings.TrimSpace(actual))
	}
	return nil
}

func mismatchError(scope, expected, actual string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", scope, err)
	}
	return fmt.Errorf("%s mismatch: expected %s, got %s", scope, expected, actual)
}

func candidateByID(candidates []BatchCandidate, id string) *BatchCandidate {
	for i := range candidates {
		if candidates[i].PlanID == id {
			return &candidates[i]
		}
	}
	return nil
}

func candidatePlanInput(state BatchState, id string) string {
	if candidate := candidateByID(state.Candidates, id); candidate != nil {
		return candidate.PlanDir
	}
	return id
}

func initializeBatchSettlement(state BatchState) BatchState {
	for _, item := range state.Landing.Plans {
		if settlementIndex(state.Settlement, item.PlanID) < 0 {
			state.Settlement = append(state.Settlement, BatchSettlement{PlanID: item.PlanID})
		}
	}
	return state
}

func settlementIndex(settlement []BatchSettlement, id string) int {
	for i := range settlement {
		if settlement[i].PlanID == id {
			return i
		}
	}
	return -1
}

func landingIntentFromState(state BatchState) (*BatchLanding, error) {
	if state.Review == nil {
		return nil, errors.New("aggregate review is missing")
	}
	plans := make([]BatchLandingPlan, 0, len(state.Integrations))
	for _, integration := range state.Integrations {
		if integration.Status != batchIntegrationApplied || strings.TrimSpace(integration.IntegrationSHA) == "" {
			return nil, fmt.Errorf("plan %s has no applied squash commit", integration.PlanID)
		}
		plans = append(plans, BatchLandingPlan{PlanID: integration.PlanID, SquashSHA: integration.IntegrationSHA})
	}
	return &BatchLanding{DefaultParentSHA: state.DefaultStartSHA, IntegrationHead: state.IntegrationHead, Plans: plans, AggregateReviewHead: state.Review.HeadSHA, ExpectedFastForward: true}, nil
}

func sameLandingIntent(a, b *BatchLanding) bool {
	if a == nil || b == nil || a.DefaultParentSHA != b.DefaultParentSHA || a.IntegrationHead != b.IntegrationHead || a.AggregateReviewHead != b.AggregateReviewHead || a.ExpectedFastForward != b.ExpectedFastForward || len(a.Plans) != len(b.Plans) {
		return false
	}
	for i := range a.Plans {
		if a.Plans[i] != b.Plans[i] {
			return false
		}
	}
	return true
}

func resultWithState(state BatchState) BatchLandResult { return BatchLandResult{State: state} }
