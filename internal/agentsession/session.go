package agentsession

import (
	"context"
	"io"
	"time"

	"github.com/iamseth/tao/internal/agent"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/promptcapture"
)

// Config describes the stable policy and dependencies for bounded sessions.
type Config struct {
	Now             func() time.Time
	Model           string
	Effort          string
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
	now            func() time.Time
	model          string
	effort         string
	runtime        agent.Runtime
	descriptor     agent.Descriptor
	permissionMode agent.PermissionMode
	timeout        time.Duration
	progress       io.Writer
	commandRunner  commandrunner.Runner
}

// New constructs a bounded session runner from a provider descriptor.
func New(config Config) Runner {
	if config.Now == nil {
		config.Now = time.Now
	}
	runtime := config.Runtime
	if runtime == nil {
		runtime = config.Descriptor.NewRuntime(config.Deps)
	}
	permissionMode := agent.PermissionModeAuto
	if config.SkipPermissions && config.Descriptor.SupportsBypassPermissions {
		permissionMode = agent.PermissionModeBypassPermissions
	}
	return Runner{
		now:            config.Now,
		model:          config.Model,
		effort:         config.Effort,
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
	Capture *promptcapture.Target
	Warning *agent.SessionWarning
	// BindLifetime coordinates nested work using the actual provider deadline.
	BindLifetime         func(context.Context) (context.Context, func() error, error)
	Model                string
	Effort               string
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
	PromptHash           string
	PromptTemplate       string
	PromptPath           string
	PromptCaptureWarning string
	// Invoked distinguishes a provider attempt from a pre-session guard failure.
	Invoked               bool
	Output                string
	FinalText             string
	PromptAcceptance      agent.PromptAcceptance
	Metrics               *agent.Metrics
	MetricsWarning        string
	MetricsWarningMessage string
	ReportMetricsWarning  bool
	// MetricsUsable preserves warning policy, not measurement completeness.
	MetricsUsable       bool
	MetricsAvailability agentmetrics.Availability
	AgentLabel          string
	MetricsMessage      string
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
	model := request.Model
	if model == "" {
		model = r.model
	}
	effort := request.Effort
	if effort == "" {
		effort = r.effort
	}
	promptHash := promptcapture.Hash(request.Prompt)
	var promptTemplate, promptPath, promptCaptureWarning string
	if request.Capture != nil {
		promptTemplate = request.Capture.Template
		written, err := promptcapture.Write(*request.Capture, promptcapture.Meta{Agent: r.descriptor.Label, Model: model, Effort: effort, StartedAt: r.now()}, request.Prompt)
		if err != nil {
			promptCaptureWarning = err.Error()
		} else {
			promptPath = written.Path
		}
	}
	invoked := false
	run := func() (agent.SessionResult, error) {
		invoked = true
		return r.runtime.RunSession(ctx, agent.Session{
			Model:                model,
			Effort:               effort,
			RepoRoot:             request.RepoRoot,
			Prompt:               request.Prompt,
			PermissionMode:       r.permissionMode,
			CollectMetrics:       r.descriptor.AlwaysCollectMetrics || metricsRequested,
			NoProgressToolLimit:  request.NoProgressToolLimit,
			VerificationCommands: request.VerificationCommands,
			Timeout:              r.timeout,
			Warning:              request.Warning,
			BindLifetime:         request.BindLifetime,
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
		PromptHash:            promptHash,
		PromptTemplate:        promptTemplate,
		PromptPath:            promptPath,
		PromptCaptureWarning:  promptCaptureWarning,
		Invoked:               invoked,
		Output:                raw.Output,
		FinalText:             raw.FinalText,
		PromptAcceptance:      raw.PromptAcceptance,
		Metrics:               raw.Metrics,
		MetricsAvailability:   raw.MetricsAvailability(),
		MetricsWarning:        raw.MetricsWarning,
		MetricsWarningMessage: warningMessage,
		ReportMetricsWarning:  raw.MetricsWarning != "" && (r.descriptor.MetricsWarningInformational || metricsRequested),
		MetricsUsable:         r.descriptor.MetricsWarningInformational || raw.MetricsWarning == "",
		AgentLabel:            r.descriptor.Label,
		MetricsMessage:        r.descriptor.MetricsMessage,
	}, err
}
