package cli

import (
	"github.com/iamseth/tao/internal/herdr"
	"github.com/iamseth/tao/internal/run"
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

func (a App) withDefaultStatusReporter() App {
	if a.StatusReporter == nil {
		a.StatusReporter = newHerdrStatusReporter()
	}
	return a
}
