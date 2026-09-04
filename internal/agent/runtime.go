package agent

import (
	"context"
	"io"
	"time"

	"github.com/iamseth/tao/internal/agent/lifecycle"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agent/perm"
)

// Runtime is the canonical agent-runtime contract. The run and planning layers
// drive built-in agents exclusively through this interface so agent-kind
// selection stays a registry lookup instead of a transport-shaped switch.
type Runtime interface {
	RunSession(ctx context.Context, session Session) (SessionResult, error)
}

// PermissionMode is the neutral permission policy passed to a Runtime. Agents
// with permission controls map these values onto their own CLI flags; Pi ignores
// the value entirely, matching the underlying client behavior.
type PermissionMode = perm.PermissionMode

const (
	PermissionModeAuto              PermissionMode = perm.PermissionModeAuto
	PermissionModePlan              PermissionMode = perm.PermissionModePlan
	PermissionModeBypassPermissions PermissionMode = perm.PermissionModeBypassPermissions
)

// Session is the provider-neutral description of a single agent run. Each field
// maps onto the corresponding underlying client request; fields a given client
// ignores are dropped by the adapter to preserve current behavior.
type Session struct {
	RepoRoot             string
	Prompt               string
	PermissionMode       PermissionMode
	CollectMetrics       bool
	NoProgressToolLimit  int
	VerificationCommands []string
	// Timeout caps a single Runtime session's wall-clock duration. A zero value
	// means no timeout.
	Timeout time.Duration
	// Log receives framed records suitable for durable agent-log storage.
	Log io.Writer
	// Progress receives a human-readable rendering of the same records.
	Progress io.Writer
}

// SessionResult is the provider-neutral outcome of a Runtime session. Metrics is
// nil when the session did not request metric collection. MetricsWarning
// explains why typed metrics could not be captured, or is empty when Metrics is
// usable.
type SessionResult struct {
	Output           string
	FinalText        string
	PromptAcceptance PromptAcceptance
	Metrics          *Metrics
	MetricsWarning   string
}

// PromptAcceptance is the provider-neutral classification of whether an
// attributed prompt could have been accepted by the provider.
type PromptAcceptance = lifecycle.PromptAcceptance

const (
	PromptAcceptanceUnknown        = lifecycle.PromptAcceptanceUnknown
	PromptAcceptanceNotTransmitted = lifecycle.PromptAcceptanceNotTransmitted
	PromptAcceptanceRejected       = lifecycle.PromptAcceptanceRejected
	PromptAcceptanceAccepted       = lifecycle.PromptAcceptanceAccepted
)

// Metrics is the provider-neutral superset of typed agent session metrics.
type Metrics = agentmetrics.Metrics
