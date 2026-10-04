package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/tui"
)

type uiPlanningSignalTransition struct {
	foreground bool
	ack        chan struct{}
}

// uiPlanningSignals keeps SIGINT subscribed (never ignored) so an exec child
// retains its native signal disposition. Only the dashboard's cancellation
// policy changes while the synchronous planning launcher owns the terminal.
// The event goroutine serializes transitions with signal delivery.
type uiPlanningSignals struct {
	ctx         context.Context
	cancel      context.CancelFunc
	transitions chan uiPlanningSignalTransition
	done        chan struct{}
}

func newUIPlanningSignals(parent context.Context) *uiPlanningSignals {
	return newUIPlanningSignalsWith(parent, signal.Notify, signal.Stop)
}

func newUIPlanningSignalsWith(parent context.Context, notify func(chan<- os.Signal, ...os.Signal), stop func(chan<- os.Signal)) *uiPlanningSignals {
	ctx, cancel := context.WithCancel(parent)
	scope := &uiPlanningSignals{ctx: ctx, cancel: cancel, transitions: make(chan uiPlanningSignalTransition), done: make(chan struct{})}
	interrupts, terminate := make(chan os.Signal, 1), make(chan os.Signal, 1)
	notify(interrupts, os.Interrupt)
	notify(terminate, syscall.SIGTERM)
	go func() {
		defer close(scope.done)
		defer cancel()
		defer stop(terminate)
		defer func() { stop(interrupts) }()
		foreground := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-terminate:
				return
			case <-interrupts:
				if !foreground {
					return
				}
			case transition := <-scope.transitions:
				if !transition.foreground {
					// Stop waits for pending deliveries to the old subscription.
					// Subscribe its replacement first, avoiding a default-disposition
					// gap, then discard overlapping foreground-era deliveries. A
					// queued Ctrl+C must not kill the newly resumed dashboard.
					next := make(chan os.Signal, 1)
					notify(next, os.Interrupt)
					stop(interrupts)
					select {
					case <-next:
					default:
					}
					interrupts = next
				}
				foreground = transition.foreground
				close(transition.ack)
			}
		}
	}()
	return scope
}

func (s *uiPlanningSignals) Context() context.Context { return s.ctx }
func (s *uiPlanningSignals) Close()                   { s.cancel(); <-s.done }

func (s *uiPlanningSignals) setForeground(foreground bool) {
	transition := uiPlanningSignalTransition{foreground: foreground, ack: make(chan struct{})}
	select {
	case s.transitions <- transition:
		select {
		case <-transition.ack:
		case <-s.done:
		}
	case <-s.done:
	}
}

type scopedPlanFixLauncher struct {
	signals  *uiPlanningSignals
	launcher tui.PlanFixLauncher
}

func (l scopedPlanFixLauncher) Launch(ctx context.Context, row monitor.Row) error {
	l.signals.setForeground(true)
	defer l.signals.setForeground(false)
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(l.signals.Context(), cancel)
	defer stop()
	if err := l.signals.Context().Err(); err != nil {
		return err
	}
	return l.launcher.Launch(childCtx, row)
}

type scopedNotePlanningLauncher struct {
	signals  *uiPlanningSignals
	launcher tui.NotePlanningLauncher
}

func (l scopedNotePlanningLauncher) Launch(ctx context.Context, item note.CatalogNote) error {
	l.signals.setForeground(true)
	defer l.signals.setForeground(false)
	// Also bind injected launchers to dashboard shutdown even if a caller supplies
	// a narrower context. The synchronous Launch contract includes waiting/reaping.
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(l.signals.Context(), cancel)
	defer stop()
	if err := l.signals.Context().Err(); err != nil {
		return err
	}
	return l.launcher.Launch(childCtx, item)
}
