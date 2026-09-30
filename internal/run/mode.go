package run

import "github.com/iamseth/tao/internal/runtimeconfig"

type CommitPolicy = runtimeconfig.CommitPolicy

type ExecutionMode = runtimeconfig.ExecutionMode

type AgentKind = runtimeconfig.AgentKind

type ResolvedRunOptions = runtimeconfig.ResolvedRunOptions

// RecoveryMode contains invocation-only recovery entry flags, never defaults.
type RecoveryMode struct {
	RestartBlocked     bool
	RepairVerification bool
	Reverify           bool
}

// Request is the run service's input: a plan addressed by Input plus the
// resolved run options the executor reads. Callers build it from the staged
// runtimeconfig model (NewConfigFromStages(...).ResolvedOptions()).
type Request struct {
	Input string
	RecoveryMode
	ResolvedRunOptions
}

// ForNextRound returns a copy with recovery entry modes cleared: they are
// single-shot and spent after the first execution of an automatic-rework loop.
func (r Request) ForNextRound() Request {
	r.Continue = false
	r.RestartBlocked = false
	r.RepairVerification = false
	return r
}

// Shared run selectors expose commit policies, execution placement, and agent
// runtimes to run callers; normalization rejects the legacy plan commit policy.
const (
	CommitPolicyPlan  = runtimeconfig.CommitPolicyPlan
	CommitPolicySlice = runtimeconfig.CommitPolicySlice
	CommitPolicyNone  = runtimeconfig.CommitPolicyNone

	ExecutionModeIsolated = runtimeconfig.ExecutionModeIsolated
	ExecutionModeCurrent  = runtimeconfig.ExecutionModeCurrent

	AgentPi     = runtimeconfig.AgentPi
	AgentClaude = runtimeconfig.AgentClaude
)
