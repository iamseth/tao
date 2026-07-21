package run

import (
	"errors"
	"fmt"
)

// ErrCannotStart classifies run-start/validation failures — the plan (or the
// requested continue) is not in a state from which a run may begin. Callers can
// branch on it via errors.Is without matching on message text. The underlying reason text is
// preserved verbatim.
var ErrCannotStart = errors.New("cannot start run")

// cannotStartError pairs an exact reason message with the ErrCannotStart
// sentinel so callers can classify via errors.Is while the message is preserved.
type cannotStartError struct {
	msg string
}

func (e *cannotStartError) Error() string { return e.msg }
func (e *cannotStartError) Unwrap() error { return ErrCannotStart }

// cannotStartf builds an ErrCannotStart-classified error with the given reason,
// preserving the reason text exactly.
func cannotStartf(format string, args ...any) error {
	return &cannotStartError{msg: fmt.Sprintf(format, args...)}
}
