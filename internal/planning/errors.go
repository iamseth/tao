package planning

import (
	"errors"
	"fmt"
)

// Sentinel errors classify planning failures by intent so command callers can
// branch with errors.Is rather than matching on message text. They carry
// classification only; the message text returned to callers is preserved
// verbatim.
var (
	// ErrSessionNotFound indicates the requested planning session does not exist.
	ErrSessionNotFound = errors.New("planning session not found")
	// ErrInvalidSession indicates a malformed or ambiguous session reference.
	ErrInvalidSession = errors.New("invalid planning session")
	// ErrServiceUnavailable indicates the planning service or repository cannot
	// currently satisfy the request.
	ErrServiceUnavailable = errors.New("planning service unavailable")
)

// classifiedError pairs an exact message with a sentinel cause so callers can
// classify via errors.Is while the message text is preserved verbatim.
type classifiedError struct {
	msg   string
	cause error
}

func (e *classifiedError) Error() string { return e.msg }
func (e *classifiedError) Unwrap() error { return e.cause }

// classify wraps a sentinel with a formatted message, preserving the message
// text exactly while making the sentinel reachable via errors.Is.
func classify(cause error, format string, args ...any) error {
	return &classifiedError{msg: fmt.Sprintf(format, args...), cause: cause}
}
