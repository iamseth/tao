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

// Request is the sole caller-owned invocation option input to run and review,
// together with the plan addressed by Input. Callers build resolved options from
// the staged runtimeconfig model (NewConfigFromStages(...).ResolvedOptions()).
// Do not also copy them into the service's ExecutionConfig defaults.
type Request struct {
	Input string
	RecoveryMode
	ResolvedRunOptions
	// Via is an optional bounded provenance label naming who or what relayed
	// an operator decision (currently only review --run). It is provenance
	// only, never authority, and is not a recovery flag: ForNextRound leaves
	// it unchanged and nothing clears it.
	Via string
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
