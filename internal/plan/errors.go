package plan

import (
	"errors"
	"fmt"
)

// Sentinel errors classify plan-facing failures by intent so callers can branch
// on them via errors.Is rather than matching on message text. They carry classification
// only; the message text returned to callers is preserved verbatim.
var (
	// ErrNotFound indicates a requested plan, slice, or path does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid indicates a malformed or ambiguous reference, or a refused
	// destructive request — conditions a caller should treat as a bad request.
	ErrInvalid = errors.New("invalid")
	// ErrActive indicates the plan is active and the requested mutation conflicts.
	ErrActive = errors.New("active")
	// ErrApprovalNotRequired indicates approval was supplied for a slice that
	// does not require it.
	ErrApprovalNotRequired = errors.New("slice does not require approval")
	// ErrApproverRequired indicates a required approver identity was missing.
	ErrApproverRequired = errors.New("approver is required")
)

// classifiedError pairs an exact message with one or more sentinel causes so
// callers can classify via errors.Is/errors.As while the message text is
// preserved verbatim.
type classifiedError struct {
	msg    string
	causes []error
}

func (e *classifiedError) Error() string   { return e.msg }
func (e *classifiedError) Unwrap() []error { return e.causes }

// classify wraps a sentinel with a formatted message, preserving the message
// text exactly while making the sentinel reachable via errors.Is.
func classify(cause error, format string, args ...any) error {
	return &classifiedError{msg: fmt.Sprintf(format, args...), causes: []error{cause}}
}
