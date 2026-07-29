package cli

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runstatus"
)

func TestHerdrStatusReporterThreadsStatus(t *testing.T) {
	expectedErr := errors.New("boom")
	tracker := &recordingHerdrTracker{}
	reporter := herdrStatusReporter{tracker: tracker}

	err := reporter.Track("run herdr-status", func() error { return expectedErr })
	if !errors.Is(err, expectedErr) {
		t.Fatalf("Track error = %v, want %v", err, expectedErr)
	}
	if tracker.status != "run herdr-status" {
		t.Fatalf("tracker status = %q, want run herdr-status", tracker.status)
	}
	if !tracker.called {
		t.Fatal("tracker did not run function")
	}
}

func TestRuntimeStatusReporterHeartbeatsAndCleansUp(t *testing.T) {
	publisher := newRecordingRuntimePublisher()
	ticker := &recordingRuntimeTicker{ticks: make(chan time.Time, 1)}
	reporter := runtimeStatusReporter{
		base:         herdrStatusReporter{tracker: &recordingHerdrTracker{}},
		newPublisher: func(run.StatusInvocation) runtimeStatusPublisher { return publisher },
		newTicker:    func(time.Duration) runtimeStatusTicker { return ticker },
		interval:     runstatus.PublicationInterval,
	}
	startedAt := time.Date(2026, 7, 29, 5, 30, 0, 0, time.UTC)
	expectedErr := context.Canceled
	err := reporter.TrackInvocation("run live", run.StatusInvocation{RepoID: "repo-a", PlanID: "plan-a", StartedAt: startedAt}, func(phases run.PhaseReporter) error {
		phases.ReportPhase(run.PhasePreparingExecution, nil)
		ticker.ticks <- startedAt.Add(runstatus.PublicationInterval)
		select {
		case <-publisher.heartbeatCalled:
		case <-time.After(time.Second):
			t.Fatal("heartbeat was not published")
		}
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("TrackInvocation error = %v, want cancellation", err)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.heartbeats != 1 || publisher.removes != 1 {
		t.Fatalf("heartbeats=%d removes=%d, want one each", publisher.heartbeats, publisher.removes)
	}
	if len(publisher.phases) != 2 || publisher.phases[0] != run.PhaseWaitingForOwnership || publisher.phases[1] != run.PhasePreparingExecution {
		t.Fatalf("phases = %q", publisher.phases)
	}
	if !ticker.isStopped() {
		t.Fatal("ticker was not stopped")
	}
}

func TestRuntimeStatusReporterSuppressesPublisherFailures(t *testing.T) {
	publisher := newRecordingRuntimePublisher()
	publisher.err = errors.New("status store unavailable")
	ticker := &recordingRuntimeTicker{ticks: make(chan time.Time, 1)}
	reporter := runtimeStatusReporter{
		newPublisher: func(run.StatusInvocation) runtimeStatusPublisher { return publisher },
		newTicker:    func(time.Duration) runtimeStatusTicker { return ticker },
	}
	expectedErr := errors.New("run failed")
	err := reporter.TrackInvocation("run failing", run.StatusInvocation{PlanID: "plan-a"}, func(run.PhaseReporter) error {
		ticker.ticks <- time.Now()
		select {
		case <-publisher.heartbeatCalled:
		case <-time.After(time.Second):
			t.Fatal("heartbeat was not attempted")
		}
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("status failure changed wrapped error: %v", err)
	}
	publisher.mu.Lock()
	removes := publisher.removes
	publisher.mu.Unlock()
	if removes != 1 || !ticker.isStopped() {
		t.Fatalf("cleanup after failures: removes=%d stopped=%v", removes, ticker.isStopped())
	}
}

func TestRuntimeStatusReporterCleansUpAfterPanic(t *testing.T) {
	publisher := newRecordingRuntimePublisher()
	ticker := &recordingRuntimeTicker{ticks: make(chan time.Time)}
	reporter := runtimeStatusReporter{
		newPublisher: func(run.StatusInvocation) runtimeStatusPublisher { return publisher },
		newTicker:    func(time.Duration) runtimeStatusTicker { return ticker },
	}
	if recovered := runtimeReporterPanicValue(reporter); recovered != "boom" {
		t.Fatalf("panic = %v, want boom", recovered)
	}
	publisher.mu.Lock()
	removes := publisher.removes
	publisher.mu.Unlock()
	if removes != 1 || !ticker.isStopped() {
		t.Fatalf("panic cleanup: removes=%d stopped=%v", removes, ticker.isStopped())
	}
}

func runtimeReporterPanicValue(reporter runtimeStatusReporter) (recovered any) {
	defer func() { recovered = recover() }()
	_ = reporter.TrackInvocation("run panic", run.StatusInvocation{PlanID: "plan-a"}, func(run.PhaseReporter) error {
		panic("boom")
	})
	return nil
}

type recordingRuntimePublisher struct {
	mu              sync.Mutex
	phases          []runstatus.Phase
	heartbeats      int
	removes         int
	err             error
	heartbeatCalled chan struct{}
}

func newRecordingRuntimePublisher() *recordingRuntimePublisher {
	return &recordingRuntimePublisher{heartbeatCalled: make(chan struct{}, 1)}
}

func (p *recordingRuntimePublisher) Publish(phase runstatus.Phase, _ *runstatus.SliceDetail) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phases = append(p.phases, phase)
	return p.err
}

func (p *recordingRuntimePublisher) Heartbeat() error {
	p.mu.Lock()
	p.heartbeats++
	err := p.err
	p.mu.Unlock()
	select {
	case p.heartbeatCalled <- struct{}{}:
	default:
	}
	return err
}

func (p *recordingRuntimePublisher) Remove() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removes++
	return p.err
}

type recordingRuntimeTicker struct {
	ticks   chan time.Time
	mu      sync.Mutex
	stopped bool
}

func (t *recordingRuntimeTicker) C() <-chan time.Time { return t.ticks }

func (t *recordingRuntimeTicker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
}

func (t *recordingRuntimeTicker) isStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

type recordingHerdrTracker struct {
	status string
	called bool
}

func (r *recordingHerdrTracker) Track(status string, fn func() error) error {
	r.status = status
	r.called = true
	return fn()
}
