package runqueue

import (
	"context"
	"errors"
	"fmt"
	"time"

	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// EntryPolicy resolves the effective automatic-rework policy for an entry.
type EntryPolicy func(QueueEntry) runtimeconfig.AutoReworkPolicy

// EntryReworker resolves the current automatic-rework mutation callback.
type EntryReworker func(QueueEntry) AutoReworker

var (
	errEntryStoppedAfterRework      = errors.New("queue stopped after automatic rework")
	errEntryRecoveryDecisionApplied = errors.New("recovered automatic rework decision applied")
)

// EntryOwnership retains one plan owner across recovered execution and rework.
// The owner callback runs until Finish is called by the synchronous entry host.
type EntryOwnership struct {
	ctx      context.Context
	complete chan error
	done     chan error
}

func beginEntryOwnership(ctx context.Context, owner PlanOwner, request run.Request) (*EntryOwnership, error) {
	if owner == nil {
		return &EntryOwnership{ctx: ctx}, nil
	}

	acquired := make(chan context.Context, 1)
	complete := make(chan error)
	done := make(chan error, 1)
	go func() {
		done <- owner(ctx, request, func(ownedCtx context.Context) error {
			acquired <- ownedCtx
			return <-complete
		})
	}()

	select {
	case ownedCtx := <-acquired:
		return &EntryOwnership{ctx: ownedCtx, complete: complete, done: done}, nil
	case err := <-done:
		if err == nil {
			err = errors.New("plan owner returned without running the owned operation")
		}
		return nil, err
	}
}

// Context returns the context supplied by the plan owner.
func (o *EntryOwnership) Context() context.Context {
	if o == nil {
		return nil
	}
	return o.ctx
}

// Finish releases retained plan ownership after execution and follow-up work.
func (o *EntryOwnership) Finish(operationErr error) error {
	if o == nil || o.complete == nil {
		return operationErr
	}
	o.complete <- operationErr
	return <-o.done
}

// EntryRecovery reports the explicit scheduler outcome of recovery and carries
// retained ownership only when ordinary execution must continue.
type EntryRecovery struct {
	Entry     QueueEntry
	Result    EntryResult
	Ownership *EntryOwnership
}

// EntryPreparation is the value-only result of preparing one scheduler
// candidate. Ready entries may carry retained recovery ownership into Drive.
type EntryPreparation struct {
	Entry     QueueEntry
	Result    EntryResult
	Ownership *EntryOwnership
}

// EntryDriver groups the dependencies needed to drive one claimed queue entry.
// It owns interrupted-entry inspection and plan ownership without access to the
// Manager queue collection or locks.
type EntryDriver struct {
	Host EntryDriverHost

	Own              PlanOwner
	Validate         Validator
	InspectRecovery  RecoveryInspector
	FinalizeRecovery RecoveryReviewer
	Execute          Executor
	Rework           AutoReworker
	ReworkForEntry   EntryReworker
	StopRequested    func() bool
	PolicyForEntry   EntryPolicy
	Now              func() time.Time
}

// Prepare validates a pending candidate before it is claimed. Recovery
// candidates are already claimed by the scheduler, so preparation first resumes
// their interrupted state machine and retains ownership when execution remains.
// Every non-ready outcome is durably applied through Host before return.
func (d EntryDriver) Prepare(ctx context.Context, entry QueueEntry) (EntryPreparation, error) {
	if err := d.validateConfiguration(false); err != nil {
		return EntryPreparation{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	preparation := EntryPreparation{Entry: entry}
	switch {
	case entry.RecoveryPending:
		recovery, err := d.Recover(ctx, entry)
		preparation = EntryPreparation(recovery)
		if err != nil || recovery.Result.Outcome != EntryOutcomeRetainedRunning {
			return preparation, err
		}
		preparation.Result = EntryResult{Outcome: EntryOutcomeReady}
	case entry.Status != QueueStatusPending:
		return EntryPreparation{}, fmt.Errorf("entry preparation requires pending entry, got %s", entry.Status)
	default:
		policy := d.policyForEntry(entry)
		if policy.Enabled && policy.MaxAttempts > 0 && entry.ReworkBaselineRound == nil {
			round := 0
			if d.InspectRecovery != nil {
				inspection, err := d.InspectRecovery(ctx, entry.PlanID)
				if err != nil {
					result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("initialize automatic rework baseline: %w", err)}
					return d.finishPreparation(ctx, preparation, result)
				}
				round = max(inspection.ReworkRound, 0)
			}
			updated := entry
			updated.ReworkBaselineRound = &round
			persisted, err := d.persistRecoveryUpdate(ctx, entry, updated)
			if err != nil {
				result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("initialize automatic rework baseline: %w", err)}
				return d.finishPreparation(ctx, preparation, result)
			}
			preparation.Entry = persisted
		}
		preparation.Result = EntryResult{Outcome: EntryOutcomeReady}
	}

	request := preparation.Entry.request
	if request.Input == "" {
		request = preparation.Entry.runRequest()
	}
	if d.Validate != nil {
		validateCtx := ctx
		if preparation.Ownership != nil {
			validateCtx = preparation.Ownership.Context()
		}
		if err := d.Validate(validateCtx, request); err != nil {
			return d.finishPreparation(ctx, preparation, EntryResult{Outcome: EntryOutcomeSkipped, Err: err})
		}
	}
	return preparation, nil
}

// FinishPreparation settles retained ownership, then applies a scheduler result
// such as waiting through the same durable host seam as execution outcomes.
func (d EntryDriver) FinishPreparation(ctx context.Context, preparation EntryPreparation, result EntryResult) error {
	_, err := d.finishPreparation(ctx, preparation, result)
	return err
}

func (d EntryDriver) finishPreparation(ctx context.Context, preparation EntryPreparation, result EntryResult) (EntryPreparation, error) {
	operationErr := result.Err
	if result.Outcome != EntryOutcomeFailed && result.Outcome != EntryOutcomeSkipped {
		operationErr = nil
	}
	if ownerErr := preparation.Ownership.Finish(operationErr); ownerErr != nil {
		if result.Outcome == EntryOutcomeSkipped {
			result.Err = ownerErr
		} else {
			result = EntryResult{Outcome: EntryOutcomeFailed, Err: ownerErr}
		}
	}
	preparation.Ownership = nil
	preparation.Result = result
	if err := d.ApplyResult(ctx, preparation.Entry, result); err != nil {
		return preparation, err
	}
	transition, apply, _ := entryTransitionForResult(preparation.Entry, result, d.Now())
	if apply {
		preparation.Entry = transition.After
	}
	return preparation, nil
}

// Recover synchronously inspects one claimed interrupted entry. Every durable
// intermediate update is applied through Host. Terminal outcomes release plan
// ownership before publication; retained-running keeps ownership for ordinary
// execution and the automatic-rework loop.
func (d EntryDriver) Recover(ctx context.Context, entry QueueEntry) (EntryRecovery, error) {
	if err := d.validateConfiguration(false); err != nil {
		return EntryRecovery{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if entry.Status != QueueStatusRunning || !entry.RecoveryPending {
		return EntryRecovery{}, fmt.Errorf("entry recovery requires recovery-pending running entry")
	}

	request := entry.request
	if request.Input == "" {
		request = entry.runRequest()
	}
	ownership, err := beginEntryOwnership(ctx, d.Own, request)
	if err != nil {
		result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("acquire recovered plan ownership: %w", err)}
		return d.finishRecovery(ctx, entry, nil, result)
	}
	ownedCtx := ownership.Context()

	inspection := RecoveryInspection{}
	if d.InspectRecovery != nil {
		inspection, err = d.InspectRecovery(ownedCtx, entry.PlanID)
		if err != nil {
			result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("inspect interrupted queue run: %w", err)}
			return d.finishRecovery(ctx, entry, ownership, result)
		}
	}

	policy := d.policyForEntry(entry)
	var budget reworkpkg.Budget
	budget.Attempts = entry.ReworkAttempts
	budget.PreviousFindingFingerprint = entry.PreviousFindingFingerprint
	baselineRecorded := entry.ReworkBaselineRound != nil
	if baselineRecorded {
		budget.BaselineRound = *entry.ReworkBaselineRound
	}
	if policy.Enabled && policy.MaxAttempts > 0 {
		recoveredBudget := budget.Recover(inspection.ReworkRound, baselineRecorded, inspection.PreviousFindingFingerprint)
		if !baselineRecorded || recoveredBudget.Attempts != budget.Attempts || recoveredBudget.PreviousFindingFingerprint != budget.PreviousFindingFingerprint {
			updated := entry
			baseline := recoveredBudget.BaselineRound
			updated.ReworkBaselineRound = &baseline
			updated.ReworkAttempts = recoveredBudget.Attempts
			updated.PreviousFindingFingerprint = recoveredBudget.PreviousFindingFingerprint
			entry, err = d.persistRecoveryUpdate(ownedCtx, entry, updated)
			if err != nil {
				result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("persist recovered automatic rework progress: %w", err)}
				return d.finishRecovery(ctx, entry, ownership, result)
			}
		}
		budget = recoveredBudget
	}

	if inspection.SlicesComplete && d.stopRequested() {
		return d.finishRecovery(ctx, entry, ownership, EntryResult{Outcome: EntryOutcomeRequeuedAfterStop})
	}
	if inspection.SlicesComplete {
		if d.FinalizeRecovery == nil {
			result := EntryResult{Outcome: EntryOutcomeFailed, Err: errors.New("resume interrupted plan finalization: recovery finalizer unavailable")}
			return d.finishRecovery(ctx, entry, ownership, result)
		}
		if err := d.FinalizeRecovery(ownedCtx, request); err != nil {
			result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("resume interrupted plan finalization: %w", err)}
			return d.finishRecovery(ctx, entry, ownership, result)
		}
		if d.InspectRecovery == nil {
			result := EntryResult{Outcome: EntryOutcomeFailed, Err: errors.New("inspect interrupted queue run after finalization: recovery inspector unavailable")}
			return d.finishRecovery(ctx, entry, ownership, result)
		}
		inspection, err = d.InspectRecovery(ownedCtx, entry.PlanID)
		if err != nil {
			result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("inspect interrupted queue run after finalization: %w", err)}
			return d.finishRecovery(ctx, entry, ownership, result)
		}
	}

	if !inspection.TerminalReview {
		if inspection.SlicesComplete {
			return d.finishRecovery(ctx, entry, ownership, EntryResult{Outcome: EntryOutcomeSucceeded})
		}
		updated := entry
		updated.RecoveryPending = false
		entry, err = d.persistRecoveryUpdate(ownedCtx, entry, updated)
		if err != nil {
			result := EntryResult{Outcome: EntryOutcomeFailed, Err: fmt.Errorf("clear interrupted queue recovery: %w", err)}
			return d.finishRecovery(ctx, entry, ownership, result)
		}
		return EntryRecovery{Entry: entry, Result: EntryResult{Outcome: EntryOutcomeRetainedRunning}, Ownership: ownership}, nil
	}

	runErr := d.runAutomaticRework(ownedCtx, &entry, reworkpkg.ExecutionState{
		Budget:              budget,
		DecideBeforeExecute: true,
	}, func(context.Context) error {
		return errEntryRecoveryDecisionApplied
	})
	if runErr != nil && !errors.Is(runErr, errEntryRecoveryDecisionApplied) {
		return d.finishRecovery(ctx, entry, ownership, EntryResult{Outcome: EntryOutcomeFailed, Err: runErr})
	}
	if entry.RecoveryPending {
		return d.finishRecovery(ctx, entry, ownership, EntryResult{Outcome: EntryOutcomeSucceeded})
	}
	if d.stopRequested() {
		return d.finishRecovery(ctx, entry, ownership, EntryResult{Outcome: EntryOutcomeRequeuedAfterStop})
	}
	return EntryRecovery{Entry: entry, Result: EntryResult{Outcome: EntryOutcomeRetainedRunning}, Ownership: ownership}, nil
}

func (d EntryDriver) persistRecoveryUpdate(ctx context.Context, before, after QueueEntry) (QueueEntry, error) {
	transition := EntryTransition{Before: before, After: after, Result: EntryResult{Outcome: EntryOutcomeRetainedRunning}}
	if err := d.Host.TransitionEntry(ctx, transition); err != nil {
		return before, err
	}
	return after, nil
}

func (d EntryDriver) finishRecovery(ctx context.Context, entry QueueEntry, ownership *EntryOwnership, result EntryResult) (EntryRecovery, error) {
	operationErr := result.Err
	if result.Outcome != EntryOutcomeFailed {
		operationErr = nil
	}
	if ownerErr := ownership.Finish(operationErr); ownerErr != nil {
		result = EntryResult{Outcome: EntryOutcomeFailed, Err: ownerErr}
	}
	if err := d.ApplyResult(ctx, entry, result); err != nil {
		return EntryRecovery{Entry: entry, Result: result}, err
	}
	transition, apply, _ := entryTransitionForResult(entry, result, d.Now())
	if apply {
		entry = transition.After
	}
	return EntryRecovery{Entry: entry, Result: result}, nil
}

func (d EntryDriver) stopRequested() bool {
	return d.StopRequested != nil && d.StopRequested()
}

func (d EntryDriver) policyForEntry(entry QueueEntry) runtimeconfig.AutoReworkPolicy {
	if d.PolicyForEntry != nil {
		return d.PolicyForEntry(entry)
	}
	return runtimeconfig.AutoReworkPolicy{}
}

func (d EntryDriver) reworkerForEntry(entry QueueEntry) AutoReworker {
	if d.ReworkForEntry != nil {
		return d.ReworkForEntry(entry)
	}
	return d.Rework
}

// Drive executes one prepared, already-claimed entry, including every
// automatic-rework round, under one plan-ownership scope and synchronously
// applies its result.
func (d EntryDriver) Drive(ctx context.Context, entry QueueEntry) (EntryResult, error) {
	return d.drive(ctx, entry, nil)
}

// DriveOwned continues a recovered entry with ownership retained by Recover.
func (d EntryDriver) DriveOwned(ctx context.Context, entry QueueEntry, ownership *EntryOwnership) (EntryResult, error) {
	if ownership == nil {
		return EntryResult{}, errors.New("owned entry drive requires retained ownership")
	}
	return d.drive(ctx, entry, ownership)
}

func (d EntryDriver) drive(ctx context.Context, entry QueueEntry, ownership *EntryOwnership) (EntryResult, error) {
	if err := d.validateConfiguration(true); err != nil {
		return EntryResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if entry.Status != QueueStatusRunning {
		return EntryResult{}, fmt.Errorf("entry driver requires running entry, got %s", entry.Status)
	}

	request := entry.request
	if request.Input == "" {
		request = entry.runRequest()
	}
	if ownership == nil {
		var err error
		ownership, err = beginEntryOwnership(ctx, d.Own, request)
		if err != nil {
			result := EntryResult{Outcome: EntryOutcomeFailed, Err: err}
			return result, d.ApplyResult(ctx, entry, result)
		}
	}

	result, operationErr := d.driveOwned(ownership.Context(), &entry, request)
	if ownerErr := ownership.Finish(operationErr); ownerErr != nil {
		if result.Outcome == EntryOutcomeSkipped {
			result.Err = ownerErr
		} else {
			result = EntryResult{Outcome: EntryOutcomeFailed, Err: ownerErr}
		}
	}
	if err := d.ApplyResult(ctx, entry, result); err != nil {
		return result, err
	}
	return result, nil
}

func (d EntryDriver) driveOwned(ctx context.Context, entry *QueueEntry, request run.Request) (EntryResult, error) {
	budget := reworkpkg.Budget{Attempts: entry.ReworkAttempts, PreviousFindingFingerprint: entry.PreviousFindingFingerprint}
	if entry.ReworkBaselineRound != nil {
		budget.BaselineRound = *entry.ReworkBaselineRound
	}
	firstExecution := true
	runErr := d.runAutomaticRework(ctx, entry, reworkpkg.ExecutionState{Budget: budget}, func(executeCtx context.Context) error {
		if !firstExecution && d.stopRequested() {
			return errEntryStoppedAfterRework
		}
		firstExecution = false
		return d.Execute(executeCtx, request)
	})
	if errors.Is(runErr, errEntryStoppedAfterRework) {
		return EntryResult{Outcome: EntryOutcomeRequeuedAfterStop}, nil
	}
	if runErr != nil {
		return EntryResult{Outcome: EntryOutcomeFailed, Err: runErr}, runErr
	}
	return EntryResult{Outcome: EntryOutcomeSucceeded}, nil
}

// runAutomaticRework adapts queue-owned progress and dynamic configuration to
// the shared rework execution boundary. The recovered state prevents the
// domain driver from replacing durable queue budget fields with a fresh budget.
func (d EntryDriver) runAutomaticRework(ctx context.Context, entry *QueueEntry, state reworkpkg.ExecutionState, execute reworkpkg.ExecuteFunc) error {
	policy := d.policyForEntry(*entry)
	// Queue configuration is intentionally dynamic. Prime Run's recovered-state
	// path even when policy is currently disabled so BeforeDecision can observe
	// policy or reworker changes made while the initial execution is active.
	initialMaxAttempts := max(policy.MaxAttempts, 1)
	var reworker AutoReworker
	driver := reworkpkg.Driver{DecideOne: func(decideCtx context.Context, planID string, baseline, attempts int, previous string, maxAttempts int) (reworkpkg.Decision, error) {
		decision, err := reworker(decideCtx, planID, baseline, attempts, previous, maxAttempts)
		decisionBudget := reworkpkg.Budget{BaselineRound: baseline, Attempts: entry.ReworkAttempts}
		if resultAttempts := decisionBudget.AttemptsAtRound(decision.Round); resultAttempts > entry.ReworkAttempts {
			entry.ReworkAttempts = resultAttempts
		}
		return decision, err
	}}
	return driver.Run(ctx, entry.PlanID, reworkpkg.RunOptions{
		Enabled:     true,
		MaxAttempts: initialMaxAttempts,
		Recovered:   &state,
		Execute:     execute,
		BeforeDecision: func(context.Context) (int, bool, error) {
			policy = d.policyForEntry(*entry)
			reworker = d.reworkerForEntry(*entry)
			proceed := policy.Enabled && policy.MaxAttempts > 0 && reworker != nil && !d.stopRequested()
			return policy.MaxAttempts, proceed, nil
		},
		PersistProgress: func(persistCtx context.Context, attempts, _ int, fingerprint string) error {
			updated := *entry
			updated.ReworkAttempts = attempts
			updated.PreviousFindingFingerprint = fingerprint
			updated.RecoveryPending = false
			if _, err := d.persistRecoveryUpdate(persistCtx, *entry, updated); err != nil {
				return fmt.Errorf("persist automatic rework progress: %w", err)
			}
			*entry = updated
			return nil
		},
	})
}

// ApplyResult validates and translates an explicit result before asking Host to
// perform the durable transition. Retained-running is an intentional no-op: the
// caller keeps ownership and will apply a later result.
func (d EntryDriver) ApplyResult(ctx context.Context, entry QueueEntry, result EntryResult) error {
	if err := d.validateConfiguration(false); err != nil {
		return err
	}
	transition, apply, err := entryTransitionForResult(entry, result, d.Now())
	if err != nil || !apply {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return d.Host.TransitionEntry(ctx, transition)
}

func (d EntryDriver) validateConfiguration(requireExecutor bool) error {
	if d.Host == nil {
		return errors.New("entry driver requires host")
	}
	if requireExecutor && d.Execute == nil {
		return errors.New("entry driver requires executor")
	}
	if d.Now == nil {
		return errors.New("entry driver requires clock")
	}
	return nil
}
