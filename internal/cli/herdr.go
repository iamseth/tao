package cli

import (
	"time"

	"github.com/iamseth/tao/internal/herdr"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

type herdrTracker interface {
	Track(status string, fn func() error) error
}

type herdrStatusReporter struct {
	tracker herdrTracker
}

func newHerdrStatusReporter() run.StatusReporter {
	return herdrStatusReporter{tracker: herdr.New()}
}

func (r herdrStatusReporter) Track(status string, fn func() error) error {
	if r.tracker == nil {
		return fn()
	}
	return r.tracker.Track(status, fn)
}

type runtimeStatusPublisher interface {
	Publish(runstatus.Phase, *runstatus.SliceDetail) error
	Heartbeat() error
	Remove() error
}

type runtimeStatusTicker interface {
	C() <-chan time.Time
	Stop()
}

type wallRuntimeStatusTicker struct {
	*time.Ticker
}

func (t wallRuntimeStatusTicker) C() <-chan time.Time { return t.Ticker.C }

type runtimeStatusReporter struct {
	base         run.StatusReporter
	newPublisher func(run.StatusInvocation) runtimeStatusPublisher
	newTicker    func(time.Duration) runtimeStatusTicker
	interval     time.Duration
}

func newRuntimeStatusReporter(base run.StatusReporter, now func() time.Time) run.StatusReporter {
	if now == nil {
		now = time.Now
	}
	return runtimeStatusReporter{
		base: base,
		newPublisher: func(invocation run.StatusInvocation) runtimeStatusPublisher {
			registry := taodata.NewRegistry("")
			store := runstatus.NewStore(registry.RuntimeStatusDir(taodata.Repo{ID: invocation.RepoID}), now)
			return runstatus.NewPublisher(store, runstatus.Record{
				Schema:              runstatus.Schema,
				RepoID:              invocation.RepoID,
				RepoName:            invocation.RepoName,
				PlanID:              invocation.PlanID,
				PlanTitle:           invocation.PlanTitle,
				Phase:               run.PhaseWaitingForOwnership,
				InvocationStartedAt: invocation.StartedAt,
				HeartbeatAt:         invocation.StartedAt,
			})
		},
		newTicker: func(interval time.Duration) runtimeStatusTicker {
			return wallRuntimeStatusTicker{Ticker: time.NewTicker(interval)}
		},
		interval: runstatus.PublicationInterval,
	}
}

func (r runtimeStatusReporter) Track(status string, fn func() error) error {
	if r.base == nil {
		return fn()
	}
	return r.base.Track(status, fn)
}

func (r runtimeStatusReporter) TrackInvocation(status string, invocation run.StatusInvocation, fn func(run.PhaseReporter) error) error {
	operation := func() error {
		publisher := r.publisher(invocation)
		if publisher == nil {
			return fn(nil)
		}
		phaseReporter := bestEffortPhaseReporter{publisher: publisher}
		phaseReporter.ReportPhase(run.PhaseWaitingForOwnership, nil)

		interval := r.interval
		if interval <= 0 {
			interval = runstatus.PublicationInterval
		}
		newTicker := r.newTicker
		if newTicker == nil {
			newTicker = func(interval time.Duration) runtimeStatusTicker {
				return wallRuntimeStatusTicker{Ticker: time.NewTicker(interval)}
			}
		}
		ticker := newTicker(interval)
		stop := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C():
					_ = publisher.Heartbeat()
				case <-stop:
					return
				}
			}
		}()
		defer func() {
			close(stop)
			<-stopped
			_ = publisher.Remove()
		}()
		return fn(phaseReporter)
	}
	if r.base == nil {
		return operation()
	}
	return r.base.Track(status, operation)
}

func (r runtimeStatusReporter) publisher(invocation run.StatusInvocation) runtimeStatusPublisher {
	if r.newPublisher == nil {
		return nil
	}
	return r.newPublisher(invocation)
}

type bestEffortPhaseReporter struct {
	publisher runtimeStatusPublisher
}

func (r bestEffortPhaseReporter) ReportPhase(phase runstatus.Phase, slice *runstatus.SliceDetail) {
	if r.publisher != nil {
		_ = r.publisher.Publish(phase, slice)
	}
}

func (a App) withDefaultStatusReporter() App {
	if a.StatusReporter == nil {
		a.StatusReporter = newRuntimeStatusReporter(newHerdrStatusReporter(), a.now)
	}
	return a
}
