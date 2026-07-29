package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// BatchAgentSessionConfig configures an internal merge-batch agent operation.
// Zero-value provider, permission, and timeout settings are loaded from the
// same TAO_* runtime environment used by ordinary runs.
type BatchAgentSessionConfig struct {
	Agent           AgentKind
	ProcessStarter  ProcessStarter
	SkipPermissions *bool
	Timeout         *time.Duration
	Log             io.Writer
	ControlRoot     string
	CommandRunner   CommandRunner
	Metrics         func(agent.Metrics, string)
}

// BatchAgentSession is the provider-neutral session seam used by merge. It has
// no plan-artifact dependency, so batch telemetry and logs can remain
// best-effort transaction data.
type BatchAgentSession struct {
	adapter         agent.SessionAdapter
	permissionMode  agent.PermissionMode
	timeout         time.Duration
	log             io.Writer
	controlRoot     string
	commandRunnerFn CommandRunner
	metrics         func(agent.Metrics, string)
}

// MergeProposalGeneratorConfig configures the exceptional single-merge
// proposal session through the same provider-neutral runtime settings as other
// agent operations.
type MergeProposalGeneratorConfig = BatchAgentSessionConfig

// NewMergeProposalGenerator constructs a strict central proposal generator.
// Construction does not start a provider session; current review-backed merges
// therefore remain a zero-call path.
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
	return session.Resolve(ctx, repoRoot, prompt)
}

func NewBatchAgentSession(config BatchAgentSessionConfig) (BatchAgentSession, error) {
	defaults, err := runtimeconfig.RuntimeEnvDefaults()
	if err != nil {
		return BatchAgentSession{}, err
	}
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
	starter := config.ProcessStarter
	if starter == nil {
		starter = defaultProcessStarter
	}
	adapter, err := agent.NewSessionAdapter(kind, agent.RuntimeDeps{ProcessStarter: starter})
	if err != nil {
		return BatchAgentSession{}, err
	}
	permission := agent.PermissionModeAuto
	if skip && adapter.Descriptor().SupportsBypassPermissions {
		permission = agent.PermissionModeBypassPermissions
	}
	return BatchAgentSession{adapter: adapter, permissionMode: permission, timeout: timeout, log: config.Log, controlRoot: config.ControlRoot, commandRunnerFn: config.CommandRunner, metrics: config.Metrics}, nil
}

// Resolve runs one repair session rooted at integrationRoot. Metrics parse
// failures are returned only as warnings and never replace the session error.
func (s BatchAgentSession) commandRunner() CommandRunner { return s.commandRunnerFn }

func (s BatchAgentSession) Resolve(ctx context.Context, integrationRoot, prompt string) (string, error) {
	runSession := func() (agent.SessionResult, error) {
		return s.adapter.Run(ctx, agent.Session{RepoRoot: integrationRoot, Prompt: prompt, PermissionMode: s.permissionMode, CollectMetrics: true, Timeout: s.timeout, Log: s.log})
	}
	var result agent.SessionResult
	var err error
	if s.controlRoot != "" {
		result, err = guardControlCheckoutLeaks(ctx, s, s.controlRoot, integrationRoot, runSession)
	} else {
		result, err = runSession()
	}
	if s.metrics != nil {
		metrics := agent.Metrics{}
		if result.Metrics != nil {
			metrics = *result.Metrics
		}
		s.metrics(metrics, result.MetricsWarning)
	} else if result.MetricsWarning != "" && s.log != nil {
		_, _ = fmt.Fprintf(s.log, "tao telemetry warning: %s\n", result.MetricsWarning)
	}
	text := strings.TrimSpace(result.FinalText)
	if text == "" {
		text = strings.TrimSpace(result.Output)
	}
	return text, err
}

type agentOperationOptions struct {
	CommitPolicy        CommitPolicy
	ExecutionMode       ExecutionMode
	StartingBranch      string
	StartingDirtyPaths  []string
	Agent               string
	CommandRunner       CommandRunner
	Now                 func() time.Time
	NoProgressToolLimit int
}

type agentSessionLog struct {
	file   *os.File
	writer io.Writer
}

func openAgentSessionLog(appender plan.LogAppender, planDir string, out io.Writer, action string, timestamp time.Time) (agentSessionLog, error) {
	logFile, err := appender.OpenLogAppend(planDir)
	if err != nil {
		return agentSessionLog{}, err
	}
	log := sessionLogWriter(logFile, out)
	if _, err := fmt.Fprintf(log, "\n--- %s %s ---\n", timestamp.Format(time.RFC3339), action); err != nil {
		_ = logFile.Close()
		return agentSessionLog{}, err
	}
	return agentSessionLog{file: logFile, writer: log}, nil
}

func (l agentSessionLog) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

func sessionLogWriter(logFile io.Writer, out io.Writer) io.Writer {
	if out == nil {
		return logFile
	}
	return io.MultiWriter(logFile, out)
}

func (o agentOperationOptions) clock() func() time.Time      { return o.Now }
func (o agentOperationOptions) commandRunner() CommandRunner { return o.CommandRunner }

// agentSessionRunner is the single session-running scaffold shared by the Pi and
// Claude executors. It owns the log envelope, plan-state read, telemetry-warning
// emission, and agent_metrics event append, delegating the actual agent call to
// an agent.Runtime resolved from the registry. The remaining per-runtime
// differences (Pi's best-effort session-info warning vs Claude's
// metrics-absent gating) are expressed as data on the runner rather than as
// duplicated control flow.
type agentSessionRunnerConfig struct {
	descriptor       agent.Descriptor
	deps             agent.RuntimeDeps
	permissionMode   agent.PermissionMode
	sessionTimeout   time.Duration
	logAppender      plan.LogAppender
	eventAppender    plan.EventAppender
	sessionLogWriter io.Writer
	commandRunner    CommandRunner
	now              func() time.Time
}

type budgetExceededError struct {
	metric    string
	threshold float64
	observed  float64
}

func (e *budgetExceededError) Error() string {
	return fmt.Sprintf("slice agent metrics %s cap exceeded: observed %g, threshold %g", e.metric, e.observed, e.threshold)
}

type agentSessionRunner struct {
	runtime        agent.Runtime
	permissionMode agent.PermissionMode
	sessionTimeout time.Duration
	agentLabel     string
	metricsMessage string
	// alwaysCollectMetrics forces metric collection even when no metrics event
	// is requested, preserving Pi's unconditional best-effort session-info read.
	alwaysCollectMetrics bool
	// metricsWarningPrefix is prepended to a runtime's MetricsWarning before it is
	// logged (Pi annotates the session-info failure; Claude logs the warning verbatim).
	metricsWarningPrefix string
	// metricsWarningInformational reports whether a MetricsWarning is advisory
	// (Pi: metrics are still valid, log unconditionally and still emit the event)
	// rather than fatal (Claude: metrics are absent, log only in the metrics
	// context and suppress the event).
	metricsWarningInformational bool
	logAppender                 plan.LogAppender
	eventAppender               plan.EventAppender
	sessionLogWriter            io.Writer
	commandRunnerFn             CommandRunner
	nowFn                       func() time.Time
}

func newAgentSessionRunner(config agentSessionRunnerConfig) agentSessionRunner {
	descriptor := config.descriptor
	return agentSessionRunner{
		runtime:                     agent.WithSessionTimeout(descriptor.NewRuntime(config.deps)),
		permissionMode:              config.permissionMode,
		sessionTimeout:              config.sessionTimeout,
		agentLabel:                  descriptor.Label,
		metricsMessage:              descriptor.MetricsMessage,
		alwaysCollectMetrics:        descriptor.AlwaysCollectMetrics,
		metricsWarningPrefix:        descriptor.MetricsWarningPrefix,
		metricsWarningInformational: descriptor.MetricsWarningInformational,
		logAppender:                 config.logAppender,
		eventAppender:               config.eventAppender,
		sessionLogWriter:            config.sessionLogWriter,
		commandRunnerFn:             config.commandRunner,
		nowFn:                       config.now,
	}
}

func (r agentSessionRunner) clock() func() time.Time { return r.nowFn }

func (r agentSessionRunner) commandRunner() CommandRunner { return r.commandRunnerFn }

func (r agentSessionRunner) RunAgentSession(ctx context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
	sessionLog, err := openAgentSessionLog(r.logAppender, request.PlanDir, r.sessionLogWriter, request.LogAction, now(r))
	if err != nil {
		return AgentSessionResult{}, err
	}
	defer func() { _ = sessionLog.Close() }()
	log := sessionLog.writer

	metricsRequested := request.Metrics != nil && request.Metrics.SliceID != ""

	state, stateErr := plan.ReadState(request.PlanDir)
	if stateErr != nil {
		if metricsRequested {
			_, _ = fmt.Fprintf(log, "tao telemetry warning: read plan state: %v\n", stateErr)
		}
		_, _ = fmt.Fprintf(log, "tao leak-guard warning: read plan state: %v; control checkout unknown, proceeding without leak guard\n", stateErr)
	}

	runSession := func() (agent.SessionResult, error) {
		return r.runtime.RunSession(ctx, agent.Session{
			RepoRoot:             request.RepoRoot,
			Prompt:               request.Prompt,
			PermissionMode:       r.permissionMode,
			CollectMetrics:       r.alwaysCollectMetrics || metricsRequested,
			NoProgressToolLimit:  request.NoProgressToolLimit,
			VerificationCommands: request.VerificationCommands,
			Timeout:              r.sessionTimeout,
			Log:                  log,
		})
	}
	var result agent.SessionResult
	var runErr error
	if stateErr == nil {
		result, runErr = guardControlCheckoutLeaks(ctx, r, state.Repo.Root, request.RepoRoot, runSession)
	} else {
		result, runErr = runSession()
	}

	var timeoutErr *agent.SessionTimeoutError
	if errors.As(runErr, &timeoutErr) && stateErr == nil && r.eventAppender != nil {
		sliceID := ""
		if request.Metrics != nil {
			sliceID = request.Metrics.SliceID
		}
		durationSeconds := int64(timeoutErr.Timeout / time.Second)
		event := plan.Event{
			Type:            plan.EventTypeSessionTimeout,
			Timestamp:       now(r).UTC(),
			PlanID:          state.Plan.ID,
			SliceID:         sliceID,
			Agent:           r.agentLabel,
			DurationSeconds: &durationSeconds,
			Message:         fmt.Sprintf("%s agent session timed out after %s", r.agentLabel, timeoutErr.Timeout),
		}
		if appendErr := r.eventAppender.AppendEvent(request.PlanDir, event); appendErr != nil {
			_, _ = fmt.Fprintf(log, "tao telemetry warning: append session timeout event: %v\n", appendErr)
		}
	}

	if result.MetricsWarning != "" && (r.metricsWarningInformational || (metricsRequested && stateErr == nil)) {
		_, _ = fmt.Fprintf(log, "tao telemetry warning: %s%s\n", r.metricsWarningPrefix, result.MetricsWarning)
	}

	var capErr error
	if metricsRequested && stateErr == nil && r.eventAppender != nil && (r.metricsWarningInformational || result.MetricsWarning == "") {
		metrics := collectAgentMetrics(state, request.Metrics.SliceID, r.agentLabel, r.metricsMessage, result.Metrics, runErr)
		if appendErr := r.eventAppender.AppendEvent(request.PlanDir, metrics.event(now(r).UTC())); appendErr != nil {
			_, _ = fmt.Fprintf(log, "tao telemetry warning: append metrics event: %v\n", appendErr)
		} else if result.Metrics != nil {
			capErr = r.enforceSliceBudgetCaps(context.WithoutCancel(ctx), request.PlanDir, state.Plan.ID, request.Metrics.SliceID, log)
		}
	}
	if capErr != nil {
		runErr = errors.Join(runErr, capErr)
	}

	return AgentSessionResult{Output: result.Output, FinalText: result.FinalText}, runErr
}

func (r agentSessionRunner) enforceSliceBudgetCaps(ctx context.Context, planDir, planID, sliceID string, log io.Writer) error {
	caps, warnings := runtimeconfig.RuntimeSliceBudgetCaps()
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(log, "tao telemetry warning: %s\n", warning)
	}
	if caps.OutputTokens == nil && caps.Cost == nil {
		return nil
	}

	detail, err := plan.NewFileRepository(filepath.Dir(planDir)).GetPlan(ctx, filepath.Base(planDir))
	if err != nil {
		_, _ = fmt.Fprintf(log, "tao telemetry warning: read metrics for slice cap: %v\n", err)
		return nil
	}
	summary := plan.SummarizeAgentTelemetry(detail)
	var totals *plan.AgentMetricsTotals
	for i := range summary.BySlice {
		if summary.BySlice[i].Key == sliceID {
			totals = &summary.BySlice[i].Totals
			break
		}
	}
	if totals == nil {
		return nil
	}

	metric := ""
	threshold := 0.0
	observed := 0.0
	if caps.OutputTokens != nil && totals.OutputTokens > *caps.OutputTokens {
		metric, threshold, observed = "output_tokens", float64(*caps.OutputTokens), float64(totals.OutputTokens)
	} else if caps.Cost != nil && totals.Cost > *caps.Cost {
		metric, threshold, observed = "cost", *caps.Cost, totals.Cost
	}
	if metric == "" {
		return nil
	}

	event := plan.Event{
		Type:      plan.EventTypeBudgetExceeded,
		Timestamp: now(r).UTC(),
		PlanID:    planID,
		SliceID:   sliceID,
		Agent:     r.agentLabel,
		Metric:    metric,
		Threshold: &threshold,
		Observed:  &observed,
		Message:   fmt.Sprintf("%s cap exceeded for slice %s: observed %g, threshold %g", metric, sliceID, observed, threshold),
	}
	if err := r.eventAppender.AppendEvent(planDir, event); err != nil {
		_, _ = fmt.Fprintf(log, "tao telemetry warning: append budget exceeded event: %v\n", err)
		return nil
	}
	return &budgetExceededError{metric: metric, threshold: threshold, observed: observed}
}

func runSliceWithAgentSession(ctx context.Context, executor AgentSessionExecutor, options agentOperationOptions, run SliceRun) error {
	prompt, err := renderWorkPrompt(workPromptData{PlanDir: run.PlanDir, RunPacket: run.RunPacket, CommitPolicy: options.CommitPolicy.String(), ExecutionMode: options.ExecutionMode.String(), Resuming: run.Resuming, ResumeAttempt: run.ResumeAttempt})
	if err != nil {
		return err
	}
	_, err = executor.RunAgentSession(ctx, AgentSessionRequest{PlanDir: run.PlanDir, RepoRoot: run.RepoRoot, LogAction: "running " + run.SliceID, Prompt: prompt, Metrics: &AgentSessionMetricsRequest{SliceID: run.SliceID}, NoProgressToolLimit: options.NoProgressToolLimit, VerificationCommands: run.VerificationCommands})
	return err
}

func createPullRequestWithAgentSession(ctx context.Context, executor AgentSessionExecutor, options agentOperationOptions, run PullRequestRun) (plan.PullRequest, error) {
	prompt, err := renderPullRequestPrompt(pullRequestPromptData{PlanDir: run.PlanDir, PlanID: run.PlanID})
	if err != nil {
		return plan.PullRequest{}, err
	}
	result, err := executor.RunAgentSession(ctx, AgentSessionRequest{PlanDir: run.PlanDir, RepoRoot: run.RepoRoot, LogAction: "creating pull request for plan " + run.PlanID, Prompt: prompt, CaptureOutput: true})
	if err != nil {
		return plan.PullRequest{}, err
	}
	return extractPullRequest(result.Output, now(options).UTC())
}

var pullRequestBodyAgentTimeout = 2 * time.Minute

func generatePullRequestBodyWithAgentSession(ctx context.Context, executor AgentSessionExecutor, _ agentOperationOptions, run PullRequestBodyRun) (string, error) {
	prompt := renderPullRequestBodyPrompt(pullRequestBodyPromptData{PlanDir: run.PlanDir, PlanID: run.PlanID, Title: run.Title, Branch: run.Branch, BaseBranch: run.BaseBranch, HeadSHA: run.HeadSHA, DraftBody: run.DraftBody})
	bodyCtx := ctx
	cancel := func() {}
	if pullRequestBodyAgentTimeout > 0 {
		bodyCtx, cancel = context.WithTimeout(ctx, pullRequestBodyAgentTimeout)
	}
	defer cancel()
	result, err := executor.RunAgentSession(bodyCtx, AgentSessionRequest{PlanDir: run.PlanDir, RepoRoot: run.RepoRoot, LogAction: "drafting pull request body for plan " + run.PlanID, Prompt: prompt, CaptureOutput: true})
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(result.FinalText)
	if body == "" {
		body = strings.TrimSpace(result.Output)
	}
	if body == "" {
		return "", fmt.Errorf("agent returned empty pull request body")
	}
	return body, nil
}
