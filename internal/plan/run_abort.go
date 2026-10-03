// Run-abort events are diagnostic only and never lifecycle, recovery, or merge authority.
package plan

import (
	"strings"

	"github.com/iamseth/tao/internal/agentinput"
)

const (
	RunAbortKindControlCheckoutLeak = "control_checkout_leak"
	RunAbortKindCanceled            = "canceled"
	RunAbortKindOther               = "other"

	MaxRunAbortMessageRunes = 512
)

// BoundRunAbortMessage retains only the trimmed, bounded first error line.
func BoundRunAbortMessage(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return agentinput.CapRunes(strings.TrimSpace(line), MaxRunAbortMessageRunes)
}

// LatestRunAbort returns a copy of the abort only while it is the newest event.
func LatestRunAbort(events []Event) *Event {
	if len(events) == 0 || events[len(events)-1].Type != EventTypeRunAborted {
		return nil
	}
	event := events[len(events)-1]
	return &event
}
