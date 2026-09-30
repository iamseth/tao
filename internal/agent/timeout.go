package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SessionTimeoutError classifies an agent session stopped by Session.Timeout.
// It unwraps to context.DeadlineExceeded so callers can use errors.Is for the
// standard deadline class while errors.As identifies Tao's session timeout.
type SessionTimeoutError struct {
	Timeout time.Duration
}

func (e *SessionTimeoutError) Error() string {
	if e.Timeout <= 0 {
		return "agent session timed out"
	}
	return fmt.Sprintf("agent session timed out after %s", e.Timeout)
}

func (e *SessionTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

// WithSessionTimeout returns a Runtime decorator that enforces Session.Timeout.
func WithSessionTimeout(runtime Runtime) Runtime {
	return timeoutRuntime{inner: runtime}
}

type timeoutRuntime struct {
	inner Runtime
	// newWarningTimer is an internal timer seam; nil uses the wall clock.
	newWarningTimer func(time.Duration) (<-chan time.Time, func())
}

func (r timeoutRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	start := time.Now()
	session.WarningMessages = nil
	timeoutCtx := ctx
	if session.Timeout > 0 {
		var cancel context.CancelFunc
		timeoutCtx, cancel = context.WithDeadline(ctx, start.Add(session.Timeout))
		defer cancel()
	}
	liveCtx := timeoutCtx
	if session.BindLifetime != nil {
		var closeLifetime func() error
		var err error
		liveCtx, closeLifetime, err = session.BindLifetime(timeoutCtx)
		if err != nil {
			return SessionResult{}, err
		}
		defer func() { _ = closeLifetime() }()
	}
	if warning := session.Warning; session.Timeout > 0 && warning != nil && warning.Percent > 0 && warning.Percent < 100 {
		// Divide before multiplying to avoid overflowing even a maximal duration.
		percent := time.Duration(warning.Percent)
		delay := session.Timeout/100*percent + session.Timeout%100*percent/100
		messages := make(chan string, 1)
		session.WarningMessages = messages
		newTimer := r.newWarningTimer
		if newTimer == nil {
			newTimer = func(d time.Duration) (<-chan time.Time, func()) {
				timer := time.NewTimer(d)
				return timer.C, func() { timer.Stop() }
			}
		}
		ticks, stop := newTimer(time.Until(start.Add(delay)))
		defer stop()
		warningCtx, cancelWarning := context.WithCancel(liveCtx)
		done := make(chan struct{})
		message := warning.Message
		go func() {
			defer close(done)
			select {
			case <-warningCtx.Done():
			case <-timeoutCtx.Done():
			case <-ticks:
				if warningCtx.Err() == nil && timeoutCtx.Err() == nil {
					messages <- message // One buffered send never waits on a provider.
				}
			}
		}()
		defer func() { cancelWarning(); <-done }()
	}
	result, err := r.inner.RunSession(liveCtx, session)
	if session.Timeout > 0 && errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		return result, &SessionTimeoutError{Timeout: session.Timeout}
	}
	return result, err
}
