package runqueue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/run"
)

func TestWatchStopChannelRequestsStopAndPausesDrain(t *testing.T) {
	started := make(chan string, 1)
	manager := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		return nil
	}, nil)
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	defer close(done)
	stopped := watchStopChannel(manager, signals, done)

	signals <- os.Interrupt
	waitForSignalWatcher(t, stopped)
	if !manager.StopRequested() {
		t.Fatal("expected signal watcher to request stop")
	}
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Queue()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != QueueStatusPending {
		t.Fatalf("expected stop signal to pause draining, got %+v", snapshot)
	}
	select {
	case got := <-started:
		t.Fatalf("expected paused manager not to start queued run, got %q", got)
	default:
	}
}

func TestWatchStopChannelDoneExitsWithoutStopping(t *testing.T) {
	manager := New(context.Background(), nil, nil)
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	stopped := watchStopChannel(manager, signals, done)

	close(done)
	waitForSignalWatcher(t, stopped)
	if manager.StopRequested() {
		t.Fatal("expected closing done not to request stop")
	}
	signals <- os.Interrupt
	if manager.StopRequested() {
		t.Fatal("expected exited watcher not to process later signals")
	}
}

func TestWatchStopSignalsStopIsIdempotent(t *testing.T) {
	manager := New(context.Background(), nil, nil)
	stop := WatchStopSignals(manager)

	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
		stop()
	}()
	waitForSignalWatcher(t, done)
}

func waitForSignalWatcher(t *testing.T, stopped <-chan struct{}) {
	t.Helper()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal watcher")
	}
}
