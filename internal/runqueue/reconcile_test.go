package runqueue

import (
	"context"
	"reflect"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

func TestReconcileEnqueuesRunnablePlans(t *testing.T) {
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		<-release
		return nil
	}, nil)
	t.Cleanup(func() { close(release) })

	lister := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "plan-a", Status: plan.StatusPlanned, PendingCount: 1},
		{ID: "complete-bool", Status: plan.StatusPlanned, Complete: true, PendingCount: 1},
		{ID: "completed-status", Status: plan.StatusCompleted, PendingCount: 1},
		{ID: "no-pending", Status: plan.StatusPlanned},
		{ID: "plan-b", Status: plan.StatusInProgress, PendingCount: 2},
	}}

	result, err := Reconcile(context.Background(), lister, runs, reconcileTestRunOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result != (ReconcileResult{Runnable: 2, Enqueued: 2}) {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	if len(lister.filters) != 1 || lister.filters[0] != (plan.PlanFilter{}) {
		t.Fatalf("expected one unfiltered ListPlans call, got %+v", lister.filters)
	}

	snapshot := runs.Queue()
	if got, want := queuePlanIDs(snapshot), []string{"plan-a", "plan-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected queued plans: got %v want %v", got, want)
	}
	if snapshot.Entries[0].request.CommitPolicy != run.CommitPolicySlice || snapshot.Entries[1].request.ExecutionMode != run.ExecutionModeIsolated {
		t.Fatalf("expected reconciled requests to carry run options, got %+v", snapshot.Entries)
	}
}

func TestReconcileIsIdempotentForQueuedAndActivePlans(t *testing.T) {
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		<-release
		return nil
	}, nil)
	t.Cleanup(func() { close(release) })
	lister := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "plan-a", Status: plan.StatusPlanned, PendingCount: 1},
		{ID: "plan-b", Status: plan.StatusPlanned, PendingCount: 1},
	}}

	first, err := Reconcile(context.Background(), lister, runs, reconcileTestRunOptions())
	if err != nil {
		t.Fatal(err)
	}
	if first != (ReconcileResult{Runnable: 2, Enqueued: 2}) {
		t.Fatalf("unexpected first reconcile result: %+v", first)
	}

	second, err := Reconcile(context.Background(), lister, runs, reconcileTestRunOptions())
	if err != nil {
		t.Fatal(err)
	}
	if second != (ReconcileResult{Runnable: 2, AlreadyQueued: 2}) {
		t.Fatalf("unexpected second reconcile result: %+v", second)
	}
	if got, want := queuePlanIDs(runs.Queue()), []string{"plan-a", "plan-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected queued plans after second reconcile: got %v want %v", got, want)
	}
}

func reconcileTestRunOptions() run.ResolvedRunOptions {
	return run.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: run.CommitPolicySlice, ExecutionMode: run.ExecutionModeIsolated, Agent: run.AgentPi}
}

func queuePlanIDs(snapshot QueueSnapshot) []string {
	ids := make([]string, len(snapshot.Entries))
	for i, entry := range snapshot.Entries {
		ids[i] = entry.PlanID
	}
	return ids
}

type fakePlanLister struct {
	summaries []plan.PlanSummary
	filters   []plan.PlanFilter
}

func (f *fakePlanLister) ListPlans(ctx context.Context, filter plan.PlanFilter) ([]plan.PlanSummary, error) {
	f.filters = append(f.filters, filter)
	return append([]plan.PlanSummary(nil), f.summaries...), nil
}
