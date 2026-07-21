package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

// BatchCoordinatorOptions contains invocation-only controls for one batch run.
// Durable recovery decisions continue to come from BatchState.
type BatchCoordinatorOptions struct {
	DryRun        bool
	Restart       bool
	AutoEject     bool
	VerifyCommand string
}

// BatchCoordinatorResult contains all batch information rendered by callers,
// including partial progress returned with an error.
type BatchCoordinatorResult struct {
	State        BatchState
	Candidates   []BatchCandidate
	Blockers     []BatchBlocker
	Deferred     []BatchDeferral
	Resumed      bool
	DryRun       bool
	Restarted    *BatchRestartPlan
	DefaultMoved bool
}

// BatchCoordinatorStore is the durable state needed by the coordinator. Phase
// implementations receive narrower stores such as BatchTransitionStore.
type BatchCoordinatorStore interface {
	ActiveID() (string, error)
	Load(string) (BatchState, error)
	Initialize(BatchState, string) (BatchState, error)
	Transition(BatchState, string) (BatchState, error)
}

// BatchCoordinatorWorkspace is the coordinator's ownership and integration
// workspace boundary. It deliberately exposes no registry or CLI types.
type BatchCoordinatorWorkspace interface {
	DefaultReachedLandingIntent(context.Context, BatchState) (bool, error)
	Restart(context.Context, BatchState) (BatchRestartPlan, error)
	ValidateResume(context.Context, BatchState) error
	ValidateEjectionResume(context.Context, BatchState, string) error
	AcquireOwnership(BatchState, time.Time) (*BatchOwnership, error)
	Status(context.Context, string) (workspace.IntegrationWorkspace, error)
	Start(context.Context, BatchState) (workspace.IntegrationWorkspace, error)
	RemoveIntegration(context.Context, string) error
}

// BatchCoordinatorDiscovery snapshots reviewed and approved candidates.
type BatchCoordinatorDiscovery interface {
	Discover(context.Context) (BatchPreflightResult, error)
}

// BatchCoordinatorPlanner deterministically orders a candidate snapshot.
type BatchCoordinatorPlanner interface {
	PlanBatchCandidatesWithGit(context.Context, []BatchCandidate) (BatchPlanningResult, error)
}

// BatchCoordinatorIntegrator owns staged integration and ejection rebuilding.
type BatchCoordinatorIntegrator interface {
	Integrate(context.Context, BatchState, string, BatchIntegrateOptions) (BatchIntegrateResult, error)
	Eject(context.Context, BatchState, string, BatchEjectOptions) (BatchIntegrateResult, error)
}

// BatchCoordinatorLander owns durable landing intent and default movement.
type BatchCoordinatorLander interface {
	Land(context.Context, BatchState, string) (BatchLandResult, error)
}

// BatchCoordinatorSettler owns post-landing evidence and cleanup.
type BatchCoordinatorSettler interface {
	Settle(context.Context, BatchState) (BatchSettleResult, error)
}

// BatchCoordinatorSeams contains every dependency used by the complete batch
// state machine without importing caller-specific construction details.
type BatchCoordinatorSeams struct {
	Store      BatchCoordinatorStore
	Workspace  BatchCoordinatorWorkspace
	Discovery  BatchCoordinatorDiscovery
	Planner    BatchCoordinatorPlanner
	Integrator BatchCoordinatorIntegrator
	Resolver   BatchResolver
	Reviewer   BatchReviewer
	Lander     BatchCoordinatorLander
	Settler    BatchCoordinatorSettler
	Now        func() time.Time
}

// BatchCoordinator owns the complete batch state machine, including setup,
// recovery, active phase ordering, landing, and settlement. Phase implementations
// retain their narrower durable and Git responsibilities.
type BatchCoordinator struct {
	seams BatchCoordinatorSeams
}

// NewBatchCoordinator constructs a batch coordinator from domain seams.
func NewBatchCoordinator(seams BatchCoordinatorSeams) *BatchCoordinator {
	return &BatchCoordinator{seams: seams}
}

// Run prepares one new or resumed batch and coordinates it through every
// active phase that can make progress in this invocation.
func (c *BatchCoordinator) Run(ctx context.Context, options BatchCoordinatorOptions) (result BatchCoordinatorResult, err error) {
	result.DryRun = options.DryRun
	now := time.Now().UTC()
	if c != nil && c.seams.Now != nil {
		now = c.seams.Now().UTC()
	}
	if c == nil || c.seams.Store == nil {
		return result, errors.New("batch coordinator store is required")
	}
	if c.seams.Workspace == nil {
		return result, errors.New("batch coordinator workspace is required")
	}

	activeID, err := c.seams.Store.ActiveID()
	if err != nil {
		return result, err
	}
	if activeID != "" {
		state, loadErr := c.seams.Store.Load(activeID)
		if loadErr != nil {
			return result, loadErr
		}
		result.State = state
		result.Candidates = append([]BatchCandidate(nil), state.Candidates...)

		defaultReachedIntent, landingErr := c.seams.Workspace.DefaultReachedLandingIntent(ctx, state)
		if landingErr != nil {
			return result, landingErr
		}
		if options.Restart {
			if defaultReachedIntent {
				return result, fmt.Errorf("merge batch %s default has reached durable landing intent; restart is forbidden", state.ID)
			}
			preview, restartErr := c.seams.Workspace.Restart(ctx, state)
			result.Restarted = &preview
			if restartErr != nil {
				return result, restartErr
			}
			activeID = ""
		} else {
			postLanding := state.Landing != nil && (state.LandedSHA != "" || defaultReachedIntent)
			if !postLanding {
				if planID, ok := BatchEjectionResumeTarget(state); ok {
					if err := c.seams.Workspace.ValidateEjectionResume(ctx, state, planID); err != nil {
						return result, err
					}
				} else if err := c.seams.Workspace.ValidateResume(ctx, state); err != nil {
					return result, err
				}
			}
			result.Resumed = true
			if options.DryRun {
				return result, nil
			}
		}
	} else if options.Restart {
		return result, errors.New("no active merge batch to restart")
	}

	if activeID == "" {
		if c.seams.Discovery == nil {
			return result, errors.New("batch coordinator discovery is required")
		}
		preflight, discoverErr := c.seams.Discovery.Discover(ctx)
		result.Candidates = preflight.Candidates
		result.Blockers = preflight.Blockers
		if discoverErr != nil {
			return result, discoverErr
		}
		if len(preflight.Candidates) == 0 {
			return result, nil
		}
		if len(preflight.Blockers) != 0 {
			return result, errors.New("merge batch preflight blocked")
		}
		if c.seams.Planner == nil {
			return result, errors.New("batch coordinator planner is required")
		}
		planning, planErr := c.seams.Planner.PlanBatchCandidatesWithGit(ctx, preflight.Candidates)
		if planErr != nil {
			return result, planErr
		}
		result.Blockers = append(result.Blockers, planning.Blockers...)
		result.Deferred = append(result.Deferred, planning.Deferred...)
		applyBatchPlanningDeferrals(preflight.Candidates, planning.Deferred)
		result.Candidates = preflight.Candidates
		if len(planning.Blockers) != 0 {
			return result, errors.New("merge batch planning blocked")
		}

		order := make([]string, 0, len(planning.Ordered))
		for _, candidate := range planning.Ordered {
			order = append(order, candidate.PlanID)
		}
		at := now.Format(time.RFC3339Nano)
		state := BatchState{
			Schema: BatchStateSchema, ID: now.Format("20060102-150405.000000000"), Status: BatchStatusPlanned,
			RepoRoot: preflight.RepoRoot, DefaultBranch: preflight.DefaultBranch, DefaultStartSHA: preflight.DefaultStartSHA,
			Candidates: preflight.Candidates, ChosenOrder: order, CreatedAt: at, UpdatedAt: at,
		}
		result.State = state
		if options.DryRun {
			ownership, ownershipErr := c.seams.Workspace.AcquireOwnership(state, now)
			if ownershipErr != nil {
				return result, ownershipErr
			}
			defer func() { _ = ownership.Release() }()

			integrated, integrateErr := c.dryRunIntegration(ctx, state, options.VerifyCommand)
			result.State = integrated.State
			result.Deferred = integrated.Deferred
			return result, integrateErr
		}
		state, err = c.seams.Store.Initialize(state, state.CreatedAt)
		if err != nil {
			return result, err
		}
		result.State = state
	}

	ownership, err := c.seams.Workspace.AcquireOwnership(result.State, now)
	if err != nil {
		return result, err
	}
	defer func() { _ = ownership.Release() }()
	var integrationRoot string
	if result.Resumed {
		status, statusErr := c.seams.Workspace.Status(ctx, result.State.ID)
		if statusErr != nil {
			return result, statusErr
		}
		integrationRoot = status.Path
	} else {
		created, startErr := c.seams.Workspace.Start(ctx, result.State)
		if startErr != nil {
			return result, startErr
		}
		integrationRoot = created.Path
	}

	resumeEjection := result.Resumed && BatchEjectionInProgress(result.State)
	operatorEject := result.Resumed && result.State.Status == BatchStatusBlocked && BatchOperatorEjectAvailable(result.State)
	if resumeEjection || operatorEject {
		if c.seams.Integrator == nil {
			return result, errors.New("batch coordinator integrator is required")
		}
		rebuilt, ejectErr := c.seams.Integrator.Eject(ctx, result.State, integrationRoot, BatchEjectOptions{VerifyCommand: options.VerifyCommand})
		result.State = rebuilt.State
		result.Deferred = rebuilt.Deferred
		if ejectErr != nil {
			return result, ejectErr
		}
	} else if resumed, ok := ResumeBlockedBatch(result.State); ok {
		resumed.UpdatedAt = now.Format(time.RFC3339Nano)
		resumed, err = c.seams.Store.Transition(resumed, resumed.UpdatedAt)
		if err != nil {
			return result, fmt.Errorf("resume blocked merge batch: %w", err)
		}
		result.State = resumed
	}

	if result.State.Status == BatchStatusPlanned || result.State.Status == BatchStatusIntegrating {
		if c.seams.Integrator == nil {
			return result, errors.New("batch coordinator integrator is required")
		}
		integrated, integrateErr := c.seams.Integrator.Integrate(ctx, result.State, integrationRoot, BatchIntegrateOptions{VerifyCommand: options.VerifyCommand})
		result.State = integrated.State
		result.Deferred = integrated.Deferred
		if integrateErr != nil {
			return result, integrateErr
		}
	}

	result.State, err = c.resolveAndReview(ctx, result.State, integrationRoot, options)
	if err != nil {
		return result, err
	}
	landableBlocked := result.State.Status == BatchStatusBlocked && !batchBlockIsTerminal(result.State) && result.State.Review != nil && result.State.Review.Verdict == plan.ReviewVerdictApprove
	if result.State.Status == BatchStatusReadyToLand || result.State.Status == BatchStatusLanded || landableBlocked {
		if c.seams.Lander == nil {
			return result, errors.New("batch coordinator lander is required")
		}
		landed, landErr := c.seams.Lander.Land(ctx, result.State, integrationRoot)
		result.State = landed.State
		result.DefaultMoved = result.State.LandedSHA != ""
		if landErr != nil {
			return result, landErr
		}
	}
	if result.State.Status == BatchStatusLanded || result.State.Status == BatchStatusSettling || result.State.Status == BatchStatusCompleted {
		if c.seams.Settler == nil {
			return result, errors.New("batch coordinator settler is required")
		}
		settled, settleErr := c.seams.Settler.Settle(ctx, result.State)
		result.State = settled.State
		result.DefaultMoved = result.State.LandedSHA != ""
		return result, settleErr
	}
	return result, nil
}

func (c *BatchCoordinator) resolveAndReview(ctx context.Context, state BatchState, integrationRoot string, options BatchCoordinatorOptions) (BatchState, error) {
	for {
		switch state.Status {
		case BatchStatusResolving:
			if c.seams.Resolver == nil {
				return state, nil
			}
			resolved, err := c.seams.Resolver.Resolve(ctx, state, integrationRoot, BatchResolveOptions{VerifyCommand: options.VerifyCommand})
			state = resolved.State
			if err != nil || state.Status == BatchStatusResolving {
				return state, err
			}
		case BatchStatusReviewing:
			if c.seams.Reviewer == nil {
				return state, nil
			}
			reviewed, err := c.seams.Reviewer.Review(ctx, state, integrationRoot, BatchReviewOptions{VerifyCommand: options.VerifyCommand, AutoEject: options.AutoEject})
			state = reviewed.State
			if err != nil || !reviewed.ReenterPhases {
				return state, err
			}
		default:
			return state, nil
		}
	}
}

func (c *BatchCoordinator) dryRunIntegration(ctx context.Context, state BatchState, verifyCommand string) (result BatchIntegrateResult, err error) {
	result.State = state
	defer func() {
		err = errors.Join(err, c.seams.Workspace.RemoveIntegration(context.WithoutCancel(ctx), state.ID))
	}()
	created, err := c.seams.Workspace.Start(ctx, state)
	if err != nil {
		return result, err
	}
	if c.seams.Integrator == nil {
		return result, errors.New("batch coordinator integrator is required")
	}
	return c.seams.Integrator.Integrate(ctx, state, created.Path, BatchIntegrateOptions{DryRun: true, VerifyCommand: verifyCommand})
}

func applyBatchPlanningDeferrals(candidates []BatchCandidate, deferred []BatchDeferral) {
	byPlan := make(map[string]BatchDeferral, len(deferred))
	for _, item := range deferred {
		byPlan[item.PlanID] = item
	}
	for i := range candidates {
		if item, ok := byPlan[candidates[i].PlanID]; ok {
			copy := item
			candidates[i].Deferred = &copy
		}
	}
}

// BatchEjectionInProgress reports whether recovery must continue a durable
// ejection before ordinary blocked-batch handling.
func BatchEjectionInProgress(state BatchState) bool {
	return state.Ejection != nil && (state.Ejection.Status == batchEjectionPending || state.Ejection.Status == batchEjectionReintegrating)
}

// BatchEjectionResumeTarget returns the candidate excluded from immutable
// resume checks. A pending ejection takes precedence over an operator action.
func BatchEjectionResumeTarget(state BatchState) (string, bool) {
	if BatchEjectionInProgress(state) {
		return state.Ejection.PlanID, true
	}
	if state.Status == BatchStatusBlocked && BatchOperatorEjectAvailable(state) {
		return state.NonConvergence.PlanID, true
	}
	return "", false
}

// BatchOperatorEjectAvailable reports whether one attributed candidate can be
// removed while retaining a non-empty batch and the one-ejection invariant.
func BatchOperatorEjectAvailable(state BatchState) bool {
	if state.NonConvergence == nil || state.Ejection != nil || len(state.Candidates) <= 1 {
		return false
	}
	planID := strings.TrimSpace(state.NonConvergence.PlanID)
	if planID == "" || strings.TrimSpace(state.NonConvergence.Reason) == "" {
		return false
	}
	for _, candidate := range state.Candidates {
		if candidate.PlanID == planID {
			return true
		}
	}
	return false
}
