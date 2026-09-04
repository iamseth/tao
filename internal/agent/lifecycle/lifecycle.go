// Package lifecycle defines provider-neutral classifications for the boundary
// between starting an agent process and its acceptance of an attributed prompt.
package lifecycle

// PromptAcceptance describes the strongest structured fact known about prompt
// delivery. Only NotTransmitted and Rejected prove that a provider did not
// accept the prompt; Unknown deliberately fails closed.
type PromptAcceptance string

const (
	PromptAcceptanceUnknown        PromptAcceptance = "unknown"
	PromptAcceptanceNotTransmitted PromptAcceptance = "not_transmitted"
	PromptAcceptanceRejected       PromptAcceptance = "rejected"
	PromptAcceptanceAccepted       PromptAcceptance = "accepted"
)

// ProvenPreAcceptance reports whether the classification proves that the
// provider did not accept the attributed prompt.
func (p PromptAcceptance) ProvenPreAcceptance() bool {
	return p == PromptAcceptanceNotTransmitted || p == PromptAcceptanceRejected
}
