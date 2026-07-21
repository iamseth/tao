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
}

func (r timeoutRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	if session.Timeout <= 0 {
		return r.inner.RunSession(ctx, session)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, session.Timeout)
	defer cancel()

	result, err := r.inner.RunSession(timeoutCtx, session)
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		return result, &SessionTimeoutError{Timeout: session.Timeout}
	}
	return result, err
}
