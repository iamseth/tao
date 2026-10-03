package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/agenttelemetry"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptcapture"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/prompts"
)

type agentOperationOptions struct {
	RuntimeEnv          *runtimeconfig.EnvSnapshot
	Models              runtimeconfig.ModelSelection
	CommitPolicy        CommitPolicy
	ExecutionMode       ExecutionMode
	StartingBranch      string
	StartingDirtyPaths  []string
	Agent               string
	CommandRunner       CommandRunner
	reviewGitFactory    reviewGitFactory
	Now                 func() time.Time
	NoProgressToolLimit int
}

type agentSessionLog struct {
	file   *os.File
	writer io.Writer
}

func openAgentSessionLog(appender plan.LogAppender, planDir string, out io.Writer, action string, timestamp time.Time, clock func() time.Time) (agentSessionLog, error) {
	logFile, err := appender.OpenLogAppend(planDir)
	if err != nil {
		return agentSessionLog{}, err
	}
	log := logrecord.TimestampWriter(logrecord.TeeWriter(logFile, out), clock)
	if err := logrecord.Write(log, logrecord.Record{Type: logrecord.TypeSession, Content: action, Timestamp: timestamp.Format(time.RFC3339)}); err != nil {
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

func writeAgentLogDiagnostic(log io.Writer, message string) {
	if log != nil {
		_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: message})
	}
}

func (o agentOperationOptions) clock() func() time.Time      { return o.Now }
func (o agentOperationOptions) commandRunner() CommandRunner { return o.CommandRunner }

// agentSessionRunner adapts the plan-agnostic bounded runner to plan storage. It
// owns the log envelope, plan-state lookup, plan event shaping, and slice-budget
// enforcement; internal/agentsession owns the single provider call and
// descriptor-driven warning classification.
type agentSessionRunnerConfig struct {
	runtimeEnv       *runtimeconfig.EnvSnapshot
	descriptor       agent.Descriptor
	deps             agent.RuntimeDeps
	skipPermissions  bool
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

const implementationWrapUpNotice = `Tao session wrap-up notice (advisory): the existing hard deadline is approaching and will not be extended. Stop scope expansion. Use tao slice-complete only when ready under all existing gates. If safely pre-intent and unfinished, use tao slice-blocked with a private bounded --resume-note-file outside the repository recording last action, next action, why, and do-not guidance. Do not weaken verification, commit manually, start new sessions, or alter or interfere with existing completion intent. This notice grants no retry, continuation, completion, or recovery authority.`

type agentSessionRunner struct {
	sessionTimeout   time.Duration
	runtimeEnv       runtimeconfig.EnvSnapshot
	session          agentsession.Runner
	agentLabel       string
	logAppender      plan.LogAppender
	eventAppender    plan.EventAppender
	sessionLogWriter io.Writer
	nowFn            func() time.Time
}

func newAgentSessionRunner(config agentSessionRunnerConfig) agentSessionRunner {
	return agentSessionRunner{
		runtimeEnv:     runtimeEnv(config.runtimeEnv),
		sessionTimeout: config.sessionTimeout,
		session: agentsession.New(agentsession.Config{
			Descriptor:      config.descriptor,
			Deps:            config.deps,
			SkipPermissions: config.skipPermissions,
			Timeout:         config.sessionTimeout,
			CommandRunner:   config.commandRunner,
			Now:             config.now,
		}),
		agentLabel:       config.descriptor.Label,
		logAppender:      config.logAppender,
		eventAppender:    config.eventAppender,
		sessionLogWriter: config.sessionLogWriter,
		nowFn:            config.now,
	}
}

func (r agentSessionRunner) clock() func() time.Time { return r.nowFn }

func (r agentSessionRunner) RunAgentSession(ctx context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
	// This is the live operation context, not persisted telemetry or lifecycle evidence.
	implementation := request.Metrics != nil && request.Metrics.SliceID != "" &&
		(request.Metrics.Role == plan.AgentRoleExecution || request.Metrics.Role == plan.AgentRoleRework)
	var warning *agent.SessionWarning
	if implementation {
		percent, err := r.runtimeEnv.SessionWarnPercent()
		if err != nil {
			return AgentSessionResult{}, err
		}
		if percent > 0 && r.sessionTimeout > 0 {
			warning = &agent.SessionWarning{Percent: percent, Message: implementationWrapUpNotice}
		}
	}
	if request.Metrics != nil && request.Metrics.EnforceSliceCaps && request.Metrics.SliceID != "" &&
		(request.Metrics.Role == plan.AgentRoleExecution || request.Metrics.Role == plan.AgentRoleRework) {
		if _, err := r.runtimeEnv.Budget(); err != nil {
			return AgentSessionResult{}, err
		}
	}
	sessionLog, err := openAgentSessionLog(r.logAppender, request.PlanDir, r.sessionLogWriter, request.LogAction, now(r), r.clock())
	if err != nil {
		return AgentSessionResult{}, err
	}
	defer func() { _ = sessionLog.Close() }()
	log := sessionLog.writer

	metricsRequested := request.Metrics != nil

	state, stateErr := plan.ReadState(request.PlanDir)
	if stateErr != nil {
		if metricsRequested {
			writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: read plan state: %v", stateErr))
		}
		writeAgentLogDiagnostic(log, fmt.Sprintf("tao leak-guard warning: read plan state: %v; control checkout unknown, proceeding without leak guard", stateErr))
	}

	controlRoot := ""
	if stateErr == nil {
		controlRoot = state.Repo.Root
	}
	var bindLifetime func(context.Context) (context.Context, func() error, error)
	if implementation {
		bindLifetime = func(sessionCtx context.Context) (context.Context, func() error, error) {
			return startSliceCompletionLifetime(sessionCtx, request.PlanDir, request.Metrics.SliceID)
		}
	}
	role, sliceID := plan.AgentRoleUnknown, ""
	if request.Metrics != nil {
		role, sliceID = request.Metrics.Role.Normalized(), request.Metrics.SliceID
	}
	hash, _ := prompts.TemplateVersion(request.PromptTemplate)
	if request.PromptTemplate == "pr-body" {
		hash = promptcapture.Hash(pullRequestBodyPromptTemplate)
	}
	target := promptcapture.Target{Dir: promptcapture.Dir(request.PlanDir), Role: string(role), Template: request.PromptTemplate, TemplateHash: hash, Label: sliceID}
	result, runErr := r.session.Run(ctx, agentsession.Request{
		Capture:      &target,
		BindLifetime: bindLifetime,
		Warning:      warning,
		RepoRoot:     request.RepoRoot, ControlRoot: controlRoot, Prompt: request.Prompt, Model: request.Model, Effort: request.Effort,
		CollectMetrics: metricsRequested, NoProgressToolLimit: request.NoProgressToolLimit,
		VerificationCommands: request.VerificationCommands, Log: log,
	})

	if result.PromptCaptureWarning != "" {
		writeAgentLogDiagnostic(log, "tao prompt-capture warning: "+result.PromptCaptureWarning)
	}
	outcome := agentsession.Summarize(result, runErr)
	if outcome.TimedOut && stateErr == nil && r.eventAppender != nil {
		var timeoutErr *agent.SessionTimeoutError
		errors.As(runErr, &timeoutErr)
		sliceID := ""
		if request.Metrics != nil {
			sliceID = request.Metrics.SliceID
		}
		durationSeconds := outcome.TimeoutSeconds
		event := plan.Event{
			Type:            plan.EventTypeSessionTimeout,
			Timestamp:       now(r).UTC(),
			PlanID:          state.Plan.ID,
			SliceID:         sliceID,
			Agent:           result.AgentLabel,
			DurationSeconds: &durationSeconds,
			Message:         fmt.Sprintf("%s agent session timed out after %s", result.AgentLabel, timeoutErr.Timeout),
		}
		if appendErr := r.eventAppender.AppendEvent(request.PlanDir, event); appendErr != nil {
			writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: append session timeout event: %v", appendErr))
		}
	}

	if outcome.ReportWarning && (stateErr == nil || outcome.MetricsUsable) {
		writeAgentLogDiagnostic(log, "tao telemetry warning: "+outcome.WarningMessage)
	}

	var capErr error
	if metricsRequested && result.Invoked && stateErr == nil && r.eventAppender != nil {
		metrics := agenttelemetry.Project(result, request.Metrics.Role, request.Effort, runErr)
		publishAgentMetrics(ctx, metrics)
		if appendErr := r.eventAppender.AppendEvent(request.PlanDir, agenttelemetry.Event(state.Plan.ID, request.Metrics.SliceID, now(r).UTC(), metrics)); appendErr != nil {
			writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: append metrics event: %v", appendErr))
		} else if request.Metrics.EnforceSliceCaps && request.Metrics.SliceID != "" &&
			(request.Metrics.Role == plan.AgentRoleExecution || request.Metrics.Role == plan.AgentRoleRework) &&
			result.MetricsUsable && result.Metrics != nil && metrics.Availability != plan.AgentMetricsUnavailable {
			capErr = r.enforceSliceBudgetCaps(context.WithoutCancel(ctx), request.PlanDir, state.Plan.ID, request.Metrics.SliceID, log)
		}
	}
	if capErr != nil {
		runErr = errors.Join(runErr, capErr)
	}

	return AgentSessionResult{Output: result.Output, FinalText: result.FinalText}, runErr
}

func (r agentSessionRunner) enforceSliceBudgetCaps(ctx context.Context, planDir, planID, sliceID string, log io.Writer) error {
	budget, err := r.runtimeEnv.Budget()
	if err != nil {
		return err
	}
	outputTokensStop, costStop := budget.Slice.OutputTokens.Stop, budget.Slice.Cost.Stop
	if outputTokensStop == nil && costStop == nil {
		return nil
	}

	detail, err := plan.NewFileRepository(filepath.Dir(planDir)).GetPlan(ctx, filepath.Base(planDir))
	if err != nil {
		writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: read metrics for slice cap: %v", err))
		return nil
	}
	// Retain legacy unclassified usage, but do not charge newly attributed
	// non-execution work against implementation caps, even with the same slice ID.
	var attempts []plan.AgentMetricEvent
	for _, event := range plan.AgentMetricsEvents(detail.Events) {
		role := event.Metrics.Role.Normalized()
		if role == plan.AgentRoleExecution || role == plan.AgentRoleRework || role == plan.AgentRoleUnknown {
			attempts = append(attempts, event)
		}
	}
	summary := plan.SummarizeAgentMetrics(attempts)
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
	if outputTokensStop != nil && totals.OutputTokens > *outputTokensStop {
		metric, threshold, observed = "output_tokens", float64(*outputTokensStop), float64(totals.OutputTokens)
	} else if costStop != nil && totals.Cost > *costStop {
		metric, threshold, observed = "cost", *costStop, totals.Cost
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
		writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: append budget exceeded event: %v", err))
		return nil
	}
	return &budgetExceededError{metric: metric, threshold: threshold, observed: observed}
}

func runSliceWithAgentSession(ctx context.Context, executor AgentSessionExecutor, options agentOperationOptions, run SliceRun) error {
	prompt, err := renderWorkPrompt(workPromptData{PlanDir: run.PlanDir, RunPacket: run.RunPacket, CommitPolicy: options.CommitPolicy.String(), ExecutionMode: options.ExecutionMode.String(), Resuming: run.Resuming, ResumeAttempt: run.ResumeAttempt})
	if err != nil {
		return err
	}
	role := plan.AgentRoleExecution
	if plan.IsReworkSliceID(run.SliceID) {
		role = plan.AgentRoleRework
	}
	model := run.Model
	if model == "" {
		model = options.Models.For(runtimeconfig.ModelRoleRun)
	}
	_, err = executor.RunAgentSession(ctx, AgentSessionRequest{Model: model, Effort: options.Models.EffortFor(runtimeconfig.ModelRoleRun), PlanDir: run.PlanDir, RepoRoot: run.RepoRoot, LogAction: "running " + run.SliceID, Prompt: prompt, PromptTemplate: prompts.PromptRun, Metrics: &AgentSessionMetricsRequest{SliceID: run.SliceID, Role: role, EnforceSliceCaps: true}, NoProgressToolLimit: options.NoProgressToolLimit, VerificationCommands: run.VerificationCommands})
	return err
}

func createPullRequestWithAgentSession(ctx context.Context, executor AgentSessionExecutor, options agentOperationOptions, run PullRequestRun) (plan.PullRequest, error) {
	prompt, err := renderPullRequestPrompt(pullRequestPromptData{PlanDir: run.PlanDir, PlanID: run.PlanID})
	if err != nil {
		return plan.PullRequest{}, err
	}
	result, err := executor.RunAgentSession(ctx, AgentSessionRequest{Model: options.Models.For(runtimeconfig.ModelRoleDefault), Effort: options.Models.EffortFor(runtimeconfig.ModelRoleDefault), PlanDir: run.PlanDir, RepoRoot: run.RepoRoot, LogAction: "creating pull request for plan " + run.PlanID, Prompt: prompt, PromptTemplate: prompts.PromptPR, CaptureOutput: true, Metrics: &AgentSessionMetricsRequest{Role: plan.AgentRolePullRequest}})
	if err != nil {
		return plan.PullRequest{}, err
	}
	return extractPullRequest(result.Output, now(options).UTC())
}

var pullRequestBodyAgentTimeout = 2 * time.Minute

func generatePullRequestBodyWithAgentSession(ctx context.Context, executor AgentSessionExecutor, options agentOperationOptions, run PullRequestBodyRun) (string, error) {
	prompt := renderPullRequestBodyPrompt(pullRequestBodyPromptData{PlanDir: run.PlanDir, PlanID: run.PlanID, Title: run.Title, Branch: run.Branch, BaseBranch: run.BaseBranch, HeadSHA: run.HeadSHA, DraftBody: run.DraftBody})
	bodyCtx := ctx
	cancel := func() {}
	if pullRequestBodyAgentTimeout > 0 {
		bodyCtx, cancel = context.WithTimeout(ctx, pullRequestBodyAgentTimeout)
	}
	defer cancel()
	result, err := executor.RunAgentSession(bodyCtx, AgentSessionRequest{Model: options.Models.For(runtimeconfig.ModelRoleDefault), Effort: options.Models.EffortFor(runtimeconfig.ModelRoleDefault), PlanDir: run.PlanDir, RepoRoot: run.RepoRoot, LogAction: "drafting pull request body for plan " + run.PlanID, Prompt: prompt, PromptTemplate: "pr-body", CaptureOutput: true, Metrics: &AgentSessionMetricsRequest{Role: plan.AgentRolePullRequest}})
	if err != nil {
		return "", err
	}
	body := agentsession.ResultText(agentsession.Result{FinalText: result.FinalText, Output: result.Output})
	if body == "" {
		return "", fmt.Errorf("agent returned empty pull request body")
	}
	return body, nil
}
