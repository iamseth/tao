package runqueue

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// PlanLister is the minimal plan repository surface needed to reconcile the
// durable run queue.
type PlanLister interface {
	ListPlans(ctx context.Context, filter plan.PlanFilter) ([]plan.PlanSummary, error)
}

// ReconcileResult summarizes queue reconciliation work.
type ReconcileResult struct {
	Runnable      int
	Enqueued      int
	AlreadyQueued int
}

// Reconcile enqueues every currently runnable plan. Duplicate pending or active
// plans are treated as already reconciled and left to Manager's queue dedupe.
func Reconcile(ctx context.Context, lister PlanLister, manager *Manager, options run.ResolvedRunOptions) (ReconcileResult, error) {
	if lister == nil {
		return ReconcileResult{}, errors.New("runqueue reconcile requires plan lister")
	}
	if manager == nil {
		return ReconcileResult{}, errors.New("runqueue reconcile requires manager")
	}

	var result ReconcileResult
	summaries, err := lister.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return result, err
	}
	for _, summary := range summaries {
		if !summary.Runnable() {
			continue
		}
		result.Runnable++
		request := run.Request{Input: summary.ID, ResolvedRunOptions: options}
		if _, err := manager.Enqueue(request); err != nil {
			if manager.planQueuedOrActive(summary.ID) {
				result.AlreadyQueued++
				continue
			}
			return result, err
		}
		result.Enqueued++
	}
	return result, nil
}

func (m *Manager) planQueuedOrActive(planID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.planQueuedOrActiveLocked(planID)
}

// StoppedAutoReworkSource is the plan repository surface needed to reconcile
// plans whose persisted automatic-rework budget stopped.
type StoppedAutoReworkSource interface {
	PlanLister
	ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error)
}

// StoppedAutoReworkOptions configures ReconcileStoppedAutoRework.
type StoppedAutoReworkOptions struct {
	Policy        runtimeconfig.AutoReworkPolicy
	ActiveOnly    bool
	ReworkRestart bool
	RunOptions    run.ResolvedRunOptions
}

// ReconcileStoppedAutoRework enqueues changes-requested plans whose persisted
// automatic-rework budget is stopped. It grants no fresh budget itself: every
// enqueue flows through the manager's auto-rework decision gate, and a
// restartable recovery entry is replaced only on an explicit rework restart.
func ReconcileStoppedAutoRework(ctx context.Context, source StoppedAutoReworkSource, manager *Manager, options StoppedAutoReworkOptions) (ReconcileResult, error) {
	var result ReconcileResult
	if !options.Policy.Enabled || options.Policy.MaxAttempts == 0 {
		return result, nil
	}
	if source == nil {
		return result, errors.New("stopped auto-rework reconcile requires a plan source")
	}
	if manager == nil {
		return result, errors.New("stopped auto-rework reconcile requires manager")
	}
	summaries, err := source.ListPlans(ctx, plan.PlanFilter{ActiveOnly: options.ActiveOnly})
	if err != nil {
		return result, err
	}
	for _, summary := range summaries {
		if summary.Status != plan.StatusChangesRequested || summary.Runnable() {
			continue
		}
		detail, err := source.ResolvePlan(ctx, summary.ID)
		if err != nil {
			return result, err
		}
		_, stopped, err := rework.GuardAutoReworkRestart(detail, true)
		if err != nil {
			return result, err
		}
		if !stopped {
			continue
		}
		result.Runnable++
		if options.ReworkRestart && queueHasRestartableRecoveryPlan(manager.Queue(), summary.ID) {
			if _, err := manager.Dequeue(summary.ID); err != nil {
				return result, fmt.Errorf("replace stopped automatic rework queue entry for %s: %w", summary.ID, err)
			}
		}
		request := run.Request{Input: summary.ID, ResolvedRunOptions: options.RunOptions}
		if _, err := manager.EnqueueAutoReworkDecision(request); err != nil {
			if queueHasPendingOrRunningPlan(manager.Queue(), summary.ID) {
				result.AlreadyQueued++
				continue
			}
			return result, err
		}
		result.Enqueued++
	}
	return result, nil
}

func queueHasPendingOrRunningPlan(snapshot QueueSnapshot, planID string) bool {
	for _, entry := range snapshot.Entries {
		if entry.PlanID == planID && (entry.Status == QueueStatusPending || entry.Status == QueueStatusRunning) {
			return true
		}
	}
	return false
}

func queueHasRestartableRecoveryPlan(snapshot QueueSnapshot, planID string) bool {
	for _, entry := range snapshot.Entries {
		if entry.PlanID == planID && entry.Status == QueueStatusPending && entry.RecoveryPending {
			return true
		}
	}
	return false
}
