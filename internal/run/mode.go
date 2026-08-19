package run

import "github.com/iamseth/tao/internal/runtimeconfig"

type Mode = runtimeconfig.Mode

type CommitPolicy = runtimeconfig.CommitPolicy

type ExecutionMode = runtimeconfig.ExecutionMode

type AgentKind = runtimeconfig.AgentKind

type ResolvedRunOptions = runtimeconfig.ResolvedRunOptions

// Request is the run service's input: a plan addressed by Input plus the
// resolved run options the executor reads. Callers build it from the staged
// runtimeconfig model (NewConfigFromStages(...).ResolvedOptions()).
type Request struct {
	Input string
	ResolvedRunOptions
}

const (
	ModeRun  = runtimeconfig.ModeRun
	ModeStep = runtimeconfig.ModeStep

	CommitPolicyPlan  = runtimeconfig.CommitPolicyPlan
	CommitPolicySlice = runtimeconfig.CommitPolicySlice
	CommitPolicyNone  = runtimeconfig.CommitPolicyNone

	ExecutionModeIsolated = runtimeconfig.ExecutionModeIsolated
	ExecutionModeCurrent  = runtimeconfig.ExecutionModeCurrent

	AgentPi     = runtimeconfig.AgentPi
	AgentClaude = runtimeconfig.AgentClaude
)
