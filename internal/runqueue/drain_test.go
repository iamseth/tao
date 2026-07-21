package runqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/run"
)

func TestManagerWaitForDrainReturnsWhenQueueEmpty(t *testing.T) {
	executor := newControlledDrainExecutor()
	manager := New(context.Background(), executor.Execute, nil)

	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	if got := waitForDrainStarted(t, executor.started); got != "plan-a" {
		t.Fatalf("expected plan-a to start first, got %q", got)
	}
	if _, err := manager.Enqueue(queueTestRequest("plan-b")); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- manager.WaitForDrain(context.Background()) }()
	close(executor.release)

	if err := waitForDrainDone(t, done); err != nil {
		t.Fatal(err)
	}
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 2 && snapshot.Entries[0].Status == QueueStatusSucceeded && snapshot.Entries[1].Status == QueueStatusSucceeded
	})
}

func TestManagerWaitForDrainStopsWithPendingEntriesAfterActiveFinishes(t *testing.T) {
	executor := newControlledDrainExecutor()
	manager := New(context.Background(), executor.Execute, nil)

	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	if got := waitForDrainStarted(t, executor.started); got != "plan-a" {
		t.Fatalf("expected plan-a to start first, got %q", got)
	}
	if _, err := manager.Enqueue(queueTestRequest("plan-b")); err != nil {
		t.Fatal(err)
	}
	manager.RequestStop()

	done := make(chan error, 1)
	go func() { done <- manager.WaitForDrain(context.Background()) }()
	close(executor.release)

	if err := waitForDrainDone(t, done); err != nil {
		t.Fatal(err)
	}
	if !manager.StopRequested() {
		t.Fatal("expected stop to remain requested")
	}
	snapshot := manager.Queue()
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Status != QueueStatusSucceeded || snapshot.Entries[1].Status != QueueStatusPending {
		t.Fatalf("expected plan-a succeeded and plan-b still pending, got %+v", snapshot)
	}
	select {
	case got := <-executor.started:
		t.Fatalf("expected pending plan not to start after stop, got %q", got)
	default:
	}
}

func TestManagerWaitForDrainSurfacesFinalTransitionStoreFailure(t *testing.T) {
	storeErr := errors.New("final transition unavailable")
	store := &recordingQueueStore{failAt: 3, appendErr: storeErr}
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitForDrain(ctx); !errors.Is(err, storeErr) {
		t.Fatalf("WaitForDrain() error = %v, want %v", err, storeErr)
	}
	if entry := manager.Queue().Entries[0]; entry.Status != QueueStatusRunning {
		t.Fatalf("in-memory entry after final transition failure = %+v, want recoverable running entry", entry)
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if entry := persisted.Entries[0]; entry.Status != QueueStatusRunning {
		t.Fatalf("persisted entry after final transition failure = %+v, want recoverable running entry", entry)
	}

	resumed, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	entry := resumed.Queue().Entries[0]
	if entry.Status != QueueStatusPending || !entry.RecoveryPending {
		t.Fatalf("recovered entry = %+v, want recovery-pending entry", entry)
	}
}

func TestManagerWaitForDrainSurfacesPreparationStoreFailure(t *testing.T) {
	storeErr := errors.New("preparation transition unavailable")
	store := &recordingQueueStore{failAt: 2, appendErr: storeErr}
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetQueueValidator(func(context.Context, run.Request) error {
		return errors.New("plan is no longer runnable")
	})
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitForDrain(ctx); !errors.Is(err, storeErr) {
		t.Fatalf("WaitForDrain() error = %v, want %v", err, storeErr)
	}
	if entry := manager.Queue().Entries[0]; entry.Status != QueueStatusPending {
		t.Fatalf("in-memory entry after preparation failure = %+v, want pending entry", entry)
	}
}

func TestManagerWaitForDrainSurfacesWaitingSettlementStoreFailure(t *testing.T) {
	storeErr := errors.New("waiting transition unavailable")
	store := &recordingQueueStore{failAt: 2, appendErr: storeErr}
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetQueueConflictChecker(func(context.Context, run.Request, []run.Request) string {
		return "waiting for conflict"
	})
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitForDrain(ctx); !errors.Is(err, storeErr) {
		t.Fatalf("WaitForDrain() error = %v, want %v", err, storeErr)
	}
	entry := manager.Queue().Entries[0]
	if entry.Status != QueueStatusPending || entry.WaitReason != "" {
		t.Fatalf("in-memory entry after waiting settlement failure = %+v, want unchanged pending entry", entry)
	}
}

func TestManagerWaitForDrainReturnsContextCancellation(t *testing.T) {
	executor := newControlledDrainExecutor()
	manager := New(context.Background(), executor.Execute, nil)
	manager.SetDrainingPaused(true)
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.WaitForDrain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	snapshot := manager.Queue()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != QueueStatusPending {
		t.Fatalf("expected pending entry to remain queued, got %+v", snapshot)
	}
}

type controlledDrainExecutor struct {
	started chan string
	release chan struct{}
}

func newControlledDrainExecutor() *controlledDrainExecutor {
	return &controlledDrainExecutor{started: make(chan string, 2), release: make(chan struct{})}
}

func (e *controlledDrainExecutor) Execute(ctx context.Context, request run.Request) error {
	e.started <- request.Input
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.release:
		return nil
	}
}

func waitForDrainStarted(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case planID := <-started:
		return planID
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queued run to start")
	}
	return ""
}

func waitForDrainDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue drain")
	}
	return nil
}
