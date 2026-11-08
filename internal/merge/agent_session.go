package merge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/agenttelemetry"
	"github.com/iamseth/tao/internal/commandrunner"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptcapture"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/workspace"
	"github.com/iamseth/tao/prompts"
)

// BatchAgentSessionConfig configures a merge-owned agent operation. Zero-value
// provider, permission, and timeout settings use RuntimeEnv (built-ins when
// omitted). Models must already be resolved by the caller unless ResolveOptions
// supplies the shared applicable projection.
type BatchAgentSessionConfig struct {
	// ResolveOptions optionally supplies the invocation's shared, lazily admitted
	// merge projection. Direct domain callers retain the explicit-field defaults.
	ResolveOptions  func() (runtimeconfig.CommandOptions, error)
	RuntimeEnv      *runtimeconfig.EnvSnapshot
	Agent           runtimeconfig.AgentKind
	Models          runtimeconfig.ModelSelection
	ProcessStarter  agent.ProcessStarter
	SkipPermissions *bool
	Timeout         *time.Duration
	Log             io.Writer
	ControlRoot     string
	CommandRunner   commandrunner.Runner
	Metrics         func(agent.Metrics, string)
	Observe         func(BatchAgentSessionRequest, BatchAgentSessionResult, error)
	EventAppender   BatchAgentEventAppender
	Now             func() time.Time
	PromptDir       func(BatchAgentSessionRequest) string

	// FramedLog sends raw records to Log instead of pre-rendered progress.
	// Such writers own terminal presentation as well as transcript persistence.
	FramedLog bool

	// ProviderLookPath and ConfinementProbe override preflight capability
	// checks. They are intended for tests; production callers leave them nil.
	ProviderLookPath agent.LookPath
	ConfinementProbe func() error
}

// BatchAgentEventAppender owns repository-scoped batch telemetry persistence.
type BatchAgentEventAppender interface {
	AppendAgentEvent(BatchAgentEvent) error
}

// BatchAgentOperation identifies the merge-batch operation that owns a provider call.
type BatchAgentOperation string

// Agent operations attribute merge provider calls to their trusted session context.
const (
	BatchAgentOperationCandidateResolution  BatchAgentOperation = "candidate_resolution"
	BatchAgentOperationSinglePlanResolution BatchAgentOperation = "single_plan_resolution"
	BatchAgentOperationSinglePlanReview     BatchAgentOperation = "single_plan_review"
	BatchAgentOperationAggregateReview      BatchAgentOperation = "aggregate_review"
	BatchAgentOperationAggregateRework      BatchAgentOperation = "aggregate_rework"
	BatchAgentOperationProposalGeneration   BatchAgentOperation = "proposal_generation"
)

// BatchAgentSessionRequest carries trusted call-site attribution and provider input.
type BatchAgentSessionRequest struct {
	BatchID         string
	Operation       BatchAgentOperation
	Attempt         int
	IntegrationRoot string
	Prompt          string
	PromptTemplate  string
	CandidatePlanID string

	// ProtectedGitObjectRoot and ProtectedGitWritePaths are set only for
	// single-plan resolver and reviewer sessions. They activate the provider's
	// read-only host filesystem view and preserve stricter read-only submounts
	// for Git metadata beneath the resolver's writable integration worktree.
	ProtectedGitObjectRoot string
	ProtectedGitWritePaths []string
}

// BatchAgentSessionResult preserves the neutral provider result while exposing
// the final text selected for merge orchestration.
type BatchAgentSessionResult struct {
	Output   string
	Provider agentsession.Result
	// ReasoningEffort is the Tao-owned request value, not provider output.
	ReasoningEffort string
}

// BatchAgentSession is the provider-neutral session seam used by merge batches.
type BatchAgentSession struct {
	deferred           *BatchAgentSessionConfig
	runner             agentsession.Runner
	models             runtimeconfig.ModelSelection
	run                func(context.Context, agentsession.Request) (agentsession.Result, error)
	confinesFilesystem bool
	log                io.Writer
	framedLog          io.Writer
	controlRoot        string
	metrics            func(agent.Metrics, string)
	observe            func(BatchAgentSessionRequest, BatchAgentSessionResult, error)
	eventAppender      BatchAgentEventAppender
	now                func() time.Time
	promptDir          func(BatchAgentSessionRequest) string
	providerToolName   string
	providerLookPath   agent.LookPath
	confinementProbe   func() error
}

// SingleMergeAgentSessionConfig configures a plan-scoped merge session. The
// caller receives provider results directly and owns best-effort plan metrics;
// repository-scoped batch events are never written.
type SingleMergeAgentSessionConfig = BatchAgentSessionConfig

// NewSingleMergeAgentSession constructs a provider-neutral session for one
// single-plan operation. Each Resolve call starts a fresh provider process.
func NewSingleMergeAgentSession(config SingleMergeAgentSessionConfig) (BatchAgentSession, error) {
	config.EventAppender = nil
	return newBatchAgentSession(config, true)
}

// FreshSingleMergeAgentSession defers runtime configuration until an ordinary
// squash conflict actually needs an agent. Every operation gets a newly
// configured session and therefore a fresh provider process, with no retry.
type FreshSingleMergeAgentSession struct {
	config SingleMergeAgentSessionConfig
}

// NewFreshSingleMergeAgentSession constructs the deferred single-plan agent.
// Invalid runtime configuration cannot block a non-conflicting merge because it
// is resolved only by Resolve.
func NewFreshSingleMergeAgentSession(config SingleMergeAgentSessionConfig) FreshSingleMergeAgentSession {
	config.EventAppender = nil
	return FreshSingleMergeAgentSession{config: config}
}

// Preflight validates the fresh session configuration and proves the selected
// provider can start inside the OS confinement boundary without opening an
// interactive provider session.
func (s FreshSingleMergeAgentSession) Preflight(ctx context.Context, request BatchAgentSessionRequest) error {
	session, err := NewSingleMergeAgentSession(s.config)
	if err != nil {
		return fmt.Errorf("configure %s session: %w", request.Operation, err)
	}
	return session.Preflight(ctx, request)
}

// Resolve runs one fresh, attributed provider session.
func (s FreshSingleMergeAgentSession) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	if s.config.Log != nil {
		if message := singleMergeOperationStart(request.Operation); message != "" {
			_, _ = fmt.Fprintln(s.config.Log, message)
		}
	}
	session, err := NewSingleMergeAgentSession(s.config)
	if err != nil {
		return BatchAgentSessionResult{}, fmt.Errorf("configure %s session: %w", request.Operation, err)
	}
	return session.Resolve(ctx, request)
}

func singleMergeOperationStart(operation BatchAgentOperation) string {
	switch operation {
	case BatchAgentOperationSinglePlanResolution:
		return "Automatic squash conflict resolution started (attempt 1 of 1)."
	case BatchAgentOperationSinglePlanReview:
		return "Independent exact-integration review started in a fresh session (attempt 1 of 1)."
	default:
		return ""
	}
}

// SingleMergeAgentMetricsEvent projects invoked provider measurements into the
// generic plan event format. The event is telemetry only; callers persist it
// best-effort and must never use it as merge or recovery authority.
func SingleMergeAgentMetricsEvent(request BatchAgentSessionRequest, result BatchAgentSessionResult, sessionErr error, timestamp time.Time) *plan.Event {
	if !result.Provider.Invoked || request.BatchID != "" || request.CandidatePlanID == "" {
		return nil
	}
	info, _ := configtypes.LookupSessionRole(mergeSessionRole(request.Operation))
	role := plan.AgentRole(info.TelemetryRole)
	var message string
	switch request.Operation {
	case BatchAgentOperationSinglePlanResolution:
		message = "Captured single-plan conflict resolver agent metrics"
	case BatchAgentOperationSinglePlanReview:
		// Single-plan integration review retains its historical plan-event label;
		// session selection and capture still use the merge-review role.
		role = plan.AgentRoleReview
		message = "Captured independent integration reviewer agent metrics"
	case BatchAgentOperationProposalGeneration:
		message = "Captured single-plan merge proposal agent metrics"
	default:
		return nil
	}
	event := agenttelemetry.Event(request.CandidatePlanID, "", timestamp, agenttelemetry.Project(result.Provider, role, result.ReasoningEffort, sessionErr))
	event.Message = message
	return &event
}

// MergeProposalGeneratorConfig configures the exceptional single-merge
// proposal session.
type MergeProposalGeneratorConfig = BatchAgentSessionConfig

// NewMergeProposalGenerator constructs a strict central proposal generator.
// Runtime configuration remains deferred until a proposal is requested, so
// review-backed merges do not configure or invoke a provider.
func NewMergeProposalGenerator(config MergeProposalGeneratorConfig) (commitcontract.Generator, error) {
	return commitcontract.Generator{Text: mergeProposalTextSession{config: config}}, nil
}

type mergeProposalTextSession struct {
	config MergeProposalGeneratorConfig
}

func (s mergeProposalTextSession) GenerateText(ctx context.Context, repoRoot, prompt string) (string, error) {
	session, err := NewBatchAgentSession(s.config)
	if err != nil {
		return "", fmt.Errorf("configure exceptional merge proposal session: %w", err)
	}
	identity, _ := ctx.Value(batchProposalSessionIdentityKey{}).(batchProposalSessionIdentity)
	if identity.Attempt == 0 {
		identity.Attempt = 1
	}
	result, err := session.Resolve(ctx, BatchAgentSessionRequest{
		BatchID: identity.BatchID, Operation: BatchAgentOperationProposalGeneration, Attempt: identity.Attempt,
		IntegrationRoot: repoRoot, Prompt: prompt, CandidatePlanID: identity.CandidatePlanID, PromptTemplate: "merge-proposal",
	})
	return result.Output, err
}

type batchProposalSessionIdentityKey struct{}

type batchProposalSessionIdentity struct {
	BatchID         string
	Attempt         int
	CandidatePlanID string
}

func withBatchProposalSessionIdentity(ctx context.Context, batchID string, attempt int, candidatePlanID string) context.Context {
	return context.WithValue(ctx, batchProposalSessionIdentityKey{}, batchProposalSessionIdentity{
		BatchID: batchID, Attempt: attempt, CandidatePlanID: candidatePlanID,
	})
}

// NewBatchAgentSession resolves merge runtime policy and constructs one bounded
// provider-neutral session adapter.
func NewBatchAgentSession(config BatchAgentSessionConfig) (BatchAgentSession, error) {
	return newBatchAgentSession(config, false)
}

// NewDeferredBatchAgentSession keeps unused provider settings from blocking
// batch discovery or settlement. Resolve validates before starting a provider.
func NewDeferredBatchAgentSession(config BatchAgentSessionConfig) BatchAgentSession {
	return BatchAgentSession{deferred: &config}
}

func newBatchAgentSession(config BatchAgentSessionConfig, confineFilesystem bool) (BatchAgentSession, error) {
	if config.ResolveOptions != nil {
		options, err := config.ResolveOptions()
		if err != nil {
			return BatchAgentSession{}, err
		}
		config.Agent = options.RunOptions.Agent
		config.Models = options.RunOptions.Models
		config.SkipPermissions = &options.SkipPermissions
		config.Timeout = &options.RunOptions.SessionTimeout
	}
	var snapshot runtimeconfig.EnvSnapshot
	if config.RuntimeEnv != nil {
		snapshot = *config.RuntimeEnv
	}
	if config.ResolveOptions == nil {
		if err := snapshot.Require(runtimeconfig.EnvAgent, runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions); err != nil {
			return BatchAgentSession{}, err
		}
	}
	defaults := snapshot.Defaults()
	kind := config.Agent
	if kind == "" {
		kind = defaults.Agent
	}
	skip := defaults.SkipPermissions
	if config.SkipPermissions != nil {
		skip = *config.SkipPermissions
	}
	timeout := defaults.SessionTimeoutValue()
	if config.Timeout != nil {
		timeout = *config.Timeout
	}
	descriptor, ok := agent.Lookup(kind)
	if !ok {
		return BatchAgentSession{}, fmt.Errorf("unsupported agent %q", kind)
	}
	providerLookPath := config.ProviderLookPath
	if providerLookPath == nil {
		providerLookPath = exec.LookPath
	}
	starter := config.ProcessStarter
	if starter == nil {
		starter = agent.DefaultProcessStarter
	}
	if confineFilesystem {
		starter = singleMergeFilesystemConfiningProcessStarter(starter, providerLookPath)
	} else {
		starter = aggregateReviewProcessStarter(starter)
	}
	progress := config.Log
	var framedLog io.Writer
	if config.FramedLog {
		progress, framedLog = nil, config.Log
	}
	runner := agentsession.New(agentsession.Config{
		Descriptor:      descriptor,
		Models:          &config.Models,
		Deps:            agent.RuntimeDeps{ProcessStarter: starter},
		SkipPermissions: skip,
		Timeout:         timeout,
		Progress:        progress,
		CommandRunner:   config.CommandRunner,
		Now:             config.Now,
	})
	clock := config.Now
	if clock == nil {
		clock = time.Now
	}
	return BatchAgentSession{
		runner: runner, run: runner.Run, confinesFilesystem: confineFilesystem, models: config.Models,
		log: config.Log, framedLog: framedLog, controlRoot: config.ControlRoot, metrics: config.Metrics,
		observe: config.Observe, eventAppender: config.EventAppender, now: clock, promptDir: config.PromptDir,
		providerToolName: descriptor.ToolName, providerLookPath: providerLookPath,
		confinementProbe: config.ConfinementProbe,
	}, nil
}

type aggregateReviewRuntimeContextKey struct{}

// aggregateReviewProcessStarter projects scratch locations, not a filesystem
// sandbox. Inherit credentials/configuration unchanged and override only caches
// and temporary storage in the child process, never the host environment.
func aggregateReviewProcessStarter(next agent.ProcessStarter) agent.ProcessStarter {
	return func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
		root, ok := ctx.Value(aggregateReviewRuntimeContextKey{}).(string)
		if !ok {
			return next(ctx, cwd, name, args)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// An empty provider skips the single-merge Pi configuration projection.
		owned, err := newSingleMergeInvocationRuntime("")
		if err != nil {
			return nil, err
		}
		integration, err := canonicalConfinementDirectory(root, "integration worktree")
		if err == nil {
			var runtimeRoot string
			runtimeRoot, err = canonicalConfinementDirectory(owned.root, "review runtime")
			if err == nil && workspace.PathWithinRoot(integration, runtimeRoot) {
				err = errors.New("review runtime overlaps integration worktree")
			}
		}
		if err != nil {
			_ = owned.cleanup()
			return nil, err
		}
		projected := []string{
			"TMPDIR=" + filepath.Join(owned.root, "tmp"),
			"GOCACHE=" + filepath.Join(owned.root, "cache"),
			"GOLANGCI_LINT_CACHE=" + filepath.Join(owned.root, "state"),
			name,
		}
		process, err := next(ctx, cwd, "/usr/bin/env", append(projected, args...))
		if err != nil {
			_ = owned.cleanup()
			return nil, err
		}
		return newConfinementCleanupProcess(process, owned), nil
	}
}

// Preflight validates the single-plan filesystem boundary and provider readiness
// without sending a model prompt or consuming resolution authority.
func (s BatchAgentSession) Preflight(ctx context.Context, request BatchAgentSessionRequest) error {
	if s.deferred != nil {
		session, err := NewBatchAgentSession(*s.deferred)
		if err != nil {
			return err
		}
		return session.Preflight(ctx, request)
	}
	_, err := s.confinementPolicy(ctx, request, true)
	return err
}

// Resolve runs exactly one attributed session. Metrics parse failures are
// warnings and never replace the provider result or error.
func (s BatchAgentSession) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	if s.deferred != nil {
		session, err := NewBatchAgentSession(*s.deferred)
		if err != nil {
			return BatchAgentSessionResult{}, err
		}
		return session.Resolve(ctx, request)
	}
	// Guarded single-plan callers run the disposable readiness preflight before
	// recording request authority. The attributed process performs its own RPC
	// readiness handshake before prompt transmission, so probing again here
	// would add an unattributed process after durable request evidence. Explicit
	// test probes retain the historical direct-Resolve capability check.
	policy, err := s.confinementPolicy(ctx, request, s.confinementProbe != nil)
	if err != nil {
		return BatchAgentSessionResult{}, err
	}
	if policy != nil {
		ctx = context.WithValue(ctx, singleMergeFilesystemConfinementContextKey{}, *policy)
	} else if request.Operation == BatchAgentOperationAggregateReview {
		ctx = context.WithValue(ctx, aggregateReviewRuntimeContextKey{}, request.IntegrationRoot)
	}
	run := s.run
	if run == nil {
		run = s.runner.Run
	}
	role := mergeSessionRole(request.Operation)
	info, _ := configtypes.LookupSessionRole(role)
	var capture *promptcapture.Target
	if s.promptDir != nil {
		if dir := s.promptDir(request); dir != "" {
			hash, _ := prompts.TemplateVersion(request.PromptTemplate)
			if request.PromptTemplate == "merge-proposal" {
				hash = commitcontract.MergeProposalTemplateVersion()
			}
			label := string(request.Operation)
			if request.Attempt > 0 {
				label += fmt.Sprintf("-attempt-%d", request.Attempt)
			}
			if request.CandidatePlanID != "" {
				label += "-" + request.CandidatePlanID
			}
			capture = &promptcapture.Target{Dir: dir, Role: info.CaptureRole, Template: request.PromptTemplate, TemplateHash: hash, Label: label}
		}
	}
	result, err := run(ctx, agentsession.Request{
		Capture:  capture,
		Role:     role,
		RepoRoot: request.IntegrationRoot, ControlRoot: s.controlRoot, Prompt: request.Prompt, CollectMetrics: true, Log: s.framedLog,
	})
	if result.PromptCaptureWarning != "" {
		s.writeLogDiagnostic("tao prompt-capture warning: " + result.PromptCaptureWarning)
	}
	summary := agentsession.Summarize(result, err)
	if s.metrics != nil {
		metrics := agent.Metrics{}
		if result.Metrics != nil {
			metrics = *result.Metrics
		}
		s.metrics(metrics, result.MetricsWarning)
	} else if summary.ReportWarning && s.log != nil {
		s.writeLogDiagnostic("tao telemetry warning: " + summary.WarningMessage)
	}
	sessionResult := BatchAgentSessionResult{Output: agentsession.ResultText(result), Provider: result, ReasoningEffort: result.ResolvedEffort}
	s.recordTelemetry(request, result, err)
	if s.observe != nil {
		s.observe(request, sessionResult, err)
	}
	return sessionResult, err
}

func mergeSessionRole(operation BatchAgentOperation) configtypes.SessionRole {
	switch operation {
	case BatchAgentOperationAggregateReview, BatchAgentOperationSinglePlanReview:
		return "merge_review"
	case BatchAgentOperationProposalGeneration:
		return "proposal"
	default:
		return "resolver"
	}
}

func (s BatchAgentSession) recordTelemetry(request BatchAgentSessionRequest, result agentsession.Result, sessionErr error) {
	if s.eventAppender == nil || request.BatchID == "" || !result.Invoked {
		return
	}
	outcome := BatchAgentOutcomeCompleted
	if sessionErr != nil {
		outcome = BatchAgentOutcomeFailed
	}
	summary := agentsession.Summarize(result, sessionErr)
	if summary.TimedOut {
		outcome = BatchAgentOutcomeTimedOut
		durationSeconds := summary.TimeoutSeconds
		event := BatchAgentEvent{
			Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeTimeout, BatchID: request.BatchID,
			Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
			Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
			TimeoutDurationSeconds: &durationSeconds,
		}
		s.appendTelemetry(event, "session timeout")
	}
	event := BatchAgentEvent{
		Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeMetrics, BatchID: request.BatchID,
		Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
		Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
		Metrics: newBatchAgentMetrics(result),
	}
	s.appendTelemetry(event, "metrics")
}

func (s BatchAgentSession) writeLogDiagnostic(message string) {
	record := logrecord.Record{Type: logrecord.TypeDiagnostic, Content: message}
	if s.framedLog != nil {
		_ = logrecord.Write(s.framedLog, record)
	} else if s.log != nil {
		_ = logrecord.Render(s.log, record)
	}
}

func (s BatchAgentSession) appendTelemetry(event BatchAgentEvent, label string) {
	if err := s.eventAppender.AppendAgentEvent(event); err != nil && s.log != nil {
		s.writeLogDiagnostic(fmt.Sprintf("tao telemetry warning: append merge-batch %s event: %v", label, err))
	}
}
