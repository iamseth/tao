package agentsession

import (
	"context"
	"io"
	"maps"
	"time"

	"github.com/iamseth/tao/internal/agent"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/promptcapture"
)

// Config describes the stable policy and dependencies for bounded sessions.
type Config struct {
	// GateEnv optionally pins allowlisted environment values for provider children only.
	GateEnv map[string]string
	// Models is copied at construction; nil preserves legacy model/effort defaults.
	Models          *configtypes.ModelSelection
	DefaultRole     configtypes.SessionRole
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
	gateEnv        map[string]string
	models         *configtypes.ModelSelection
	defaultRole    configtypes.SessionRole
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
	var models *configtypes.ModelSelection
	if config.Models != nil {
		selection := *config.Models
		models = &selection
	}
	return Runner{
		gateEnv:        maps.Clone(config.GateEnv),
		models:         models,
		defaultRole:    config.DefaultRole,
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
	Role configtypes.SessionRole
	// Nil attribution fails closed; false preserves the provider outcome.
	AttributeControlCheckoutChange func(context.Context, ControlCheckoutChange) bool
	Capture                        *promptcapture.Target
	Warning                        *agent.SessionWarning
	// BindLifetime coordinates nested work using the actual provider deadline.
	BindLifetime         func(context.Context) (context.Context, func() error, error)
	Grace                *agent.SessionGrace
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
	// ResolvedModel and ResolvedEffort are requested settings, not provider metrics.
	ResolvedModel         string
	ResolvedEffort        string
	ControlCheckoutChange *ControlCheckoutChange
	PromptHash            string
	PromptTemplate        string
	PromptPath            string
	PromptCaptureWarning  string
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
	model, effort := r.model, r.effort
	role := request.Role
	if role == "" {
		role = r.defaultRole
	}
	if info, ok := configtypes.LookupSessionRole(role); ok && r.models != nil {
		model, effort = r.models.For(info.ModelRole), r.models.EffortFor(info.ModelRole)
	}
	if request.Model != "" {
		model = request.Model
	}
	if request.Effort != "" {
		effort = request.Effort
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
		providerCtx := ctx
		if len(r.gateEnv) != 0 {
			providerCtx = process.WithGateEnv(ctx, r.gateEnv)
		}
		return r.runtime.RunSession(providerCtx, agent.Session{
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
			Grace:                request.Grace,
			Log:                  request.Log,
			Progress:             progress,
		})
	}

	var change *ControlCheckoutChange
	var attribute func(context.Context, ControlCheckoutChange) bool
	if request.AttributeControlCheckoutChange != nil {
		attribute = func(ctx context.Context, evidence ControlCheckoutChange) bool {
			if request.AttributeControlCheckoutChange(ctx, evidence) {
				return true
			}
			change = &evidence
			return false
		}
	}
	var raw agent.SessionResult
	var err error
	if request.ControlRoot != "" {
		raw, err = guardControlCheckoutLeaks(ctx, r.commandRunner, request.ControlRoot, request.RepoRoot, run, attribute)
	} else {
		raw, err = run()
	}
	warningMessage := ""
	if raw.MetricsWarning != "" {
		warningMessage = r.descriptor.MetricsWarningPrefix + raw.MetricsWarning
	}
	return Result{
		ResolvedModel:         model,
		ResolvedEffort:        effort,
		ControlCheckoutChange: change,
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
