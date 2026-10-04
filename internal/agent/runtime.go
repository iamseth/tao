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

// SessionGrace bounds how long a live Tao-owned completion may defer
// cancellation past Timeout with Max. Active is a cheap liveness probe.
type SessionGrace struct {
	Max    time.Duration
	Active func() bool
}

// SessionWarning requests one advisory notice at Percent (1–99) of the session
// timeout. Nil, zero, and invalid policies do not schedule a notice.
type SessionWarning struct {
	Percent int
	Message string
}

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
	// Model is an opaque provider model selector passed to the runtime's
	// --model flag when non-empty.
	Model string
	// Effort is an opaque reasoning-effort selector passed as a native runtime flag when non-empty.
	Effort string
	// Timeout caps a single Runtime session's wall-clock duration. A zero value
	// means no timeout.
	Timeout time.Duration
	Warning *SessionWarning
	// Grace is disabled when nil, Max is zero, or Active is nil. Grace never
	// extends agent turns.
	Grace *SessionGrace
	// WarningMessages is transient, decorator-owned delivery. Providers may
	// ignore it; notices never extend the deadline or authorize completion.
	WarningMessages <-chan string
	// BindLifetime is optional transient coordination for nested work. The
	// decorator supplies the actual timeout context and closes it on return.
	BindLifetime func(context.Context) (context.Context, func() error, error)
	// Log receives framed records suitable for durable agent-log storage.
	Log io.Writer
	// Progress receives a human-readable rendering of the same records.
	Progress io.Writer
}

// SessionResult is the provider-neutral outcome of a Runtime session. Metrics is
// nil when the session did not request metric collection. MetricsWarning
// explains capture issues under the existing provider warning policy. It does
// not establish coverage; Metrics.Availability and presence flags do that.
type SessionResult struct {
	Output           string
	FinalText        string
	PromptAcceptance PromptAcceptance
	Metrics          *Metrics
	MetricsWarning   string
}

// MetricsAvailability returns explicit coverage, without inferring it from
// session success or warning prose. A nil metrics result has no measurements;
// a legacy nonnil metrics result without metadata retains unknown coverage.
func (r SessionResult) MetricsAvailability() agentmetrics.Availability {
	if r.Metrics == nil {
		return agentmetrics.Unavailable
	}
	return r.Metrics.Availability
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
