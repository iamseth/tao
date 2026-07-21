package runqueue

import (
	"context"
	"errors"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
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
