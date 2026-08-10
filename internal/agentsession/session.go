package agentsession

import (
	"context"
	"io"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/commandrunner"
)

// Config describes the stable policy and dependencies for bounded sessions.
type Config struct {
	Descriptor      agent.Descriptor
	Runtime         agent.Runtime
	Deps            agent.RuntimeDeps
	SkipPermissions bool
	Timeout         time.Duration
	Progress        io.Writer
	CommandRunner   commandrunner.Runner
}

// Runner invokes exactly one provider session for each Run call.
type Runner struct {
	runtime        agent.Runtime
	descriptor     agent.Descriptor
	permissionMode agent.PermissionMode
	timeout        time.Duration
	progress       io.Writer
	commandRunner  commandrunner.Runner
}

// New constructs a bounded session runner from a provider descriptor.
func New(config Config) Runner {
	runtime := config.Runtime
	if runtime == nil {
		runtime = config.Descriptor.NewRuntime(config.Deps)
	}
	permissionMode := agent.PermissionModeAuto
	if config.SkipPermissions && config.Descriptor.SupportsBypassPermissions {
		permissionMode = agent.PermissionModeBypassPermissions
	}
	return Runner{
		runtime:        agent.WithSessionTimeout(runtime),
		descriptor:     config.Descriptor,
		permissionMode: permissionMode,
		timeout:        config.Timeout,
		progress:       config.Progress,
		commandRunner:  config.CommandRunner,
	}
}

// Request describes one provider call. ControlRoot enables leak detection when
// it differs from RepoRoot.
type Request struct {
	RepoRoot             string
	ControlRoot          string
	Prompt               string
	CollectMetrics       bool
	NoProgressToolLimit  int
	VerificationCommands []string
	Log                  io.Writer
	Progress             io.Writer
}

// Result is the neutral provider result plus descriptor-driven telemetry
// classification. Domain adapters decide whether and where to persist it.
type Result struct {
	Output                string
	FinalText             string
	Metrics               *agent.Metrics
	MetricsWarning        string
	MetricsWarningMessage string
	ReportMetricsWarning  bool
	MetricsUsable         bool
	AgentLabel            string
	MetricsMessage        string
}

// Run invokes the configured provider exactly once unless a pre-session leak
// fingerprint cannot be captured. Provider output is preserved alongside
// timeout and other session errors.
func (r Runner) Run(ctx context.Context, request Request) (Result, error) {
	metricsRequested := request.CollectMetrics
	progress := request.Progress
	if progress == nil {
		progress = r.progress
	}
	run := func() (agent.SessionResult, error) {
		return r.runtime.RunSession(ctx, agent.Session{
			RepoRoot:             request.RepoRoot,
			Prompt:               request.Prompt,
			PermissionMode:       r.permissionMode,
			CollectMetrics:       r.descriptor.AlwaysCollectMetrics || metricsRequested,
			NoProgressToolLimit:  request.NoProgressToolLimit,
			VerificationCommands: request.VerificationCommands,
			Timeout:              r.timeout,
			Log:                  request.Log,
			Progress:             progress,
		})
	}

	var raw agent.SessionResult
	var err error
	if request.ControlRoot != "" {
		raw, err = guardControlCheckoutLeaks(ctx, r.commandRunner, request.ControlRoot, request.RepoRoot, run)
	} else {
		raw, err = run()
	}
	warningMessage := ""
	if raw.MetricsWarning != "" {
		warningMessage = r.descriptor.MetricsWarningPrefix + raw.MetricsWarning
	}
	return Result{
		Output:                raw.Output,
		FinalText:             raw.FinalText,
		Metrics:               raw.Metrics,
		MetricsWarning:        raw.MetricsWarning,
		MetricsWarningMessage: warningMessage,
		ReportMetricsWarning:  raw.MetricsWarning != "" && (r.descriptor.MetricsWarningInformational || metricsRequested),
		MetricsUsable:         r.descriptor.MetricsWarningInformational || raw.MetricsWarning == "",
		AgentLabel:            r.descriptor.Label,
		MetricsMessage:        r.descriptor.MetricsMessage,
	}, err
}
