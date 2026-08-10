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
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

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

func sessionLogWriter(logFile io.Writer, out io.Writer) io.Writer {
	if out == nil {
		return logFile
	}
	return framedSessionLogWriter{log: logFile, out: out}
}

type framedSessionLogWriter struct {
	log io.Writer
	out io.Writer
}

func (w framedSessionLogWriter) Write(p []byte) (int, error) {
	n, err := w.log.Write(p)
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	record, ok := logrecord.Parse(strings.TrimSuffix(string(p), "\n"))
	if !ok {
		return len(p), nil
	}
	if err := logrecord.Render(w.out, record); err != nil {
		return 0, err
	}
	return len(p), nil
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
	session          agentsession.Runner
	agentLabel       string
	logAppender      plan.LogAppender
	eventAppender    plan.EventAppender
	sessionLogWriter io.Writer
	nowFn            func() time.Time
}

func newAgentSessionRunner(config agentSessionRunnerConfig) agentSessionRunner {
	return agentSessionRunner{
		session: agentsession.New(agentsession.Config{
			Descriptor:      config.descriptor,
			Deps:            config.deps,
			SkipPermissions: config.permissionMode == agent.PermissionModeBypassPermissions,
			Timeout:         config.sessionTimeout,
			CommandRunner:   config.commandRunner,
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
			writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: read plan state: %v", stateErr))
		}
		writeAgentLogDiagnostic(log, fmt.Sprintf("tao leak-guard warning: read plan state: %v; control checkout unknown, proceeding without leak guard", stateErr))
	}

	controlRoot := ""
	if stateErr == nil {
		controlRoot = state.Repo.Root
	}
	result, runErr := r.session.Run(ctx, agentsession.Request{
		RepoRoot: request.RepoRoot, ControlRoot: controlRoot, Prompt: request.Prompt,
		CollectMetrics: metricsRequested, NoProgressToolLimit: request.NoProgressToolLimit,
		VerificationCommands: request.VerificationCommands, Log: log,
	})

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
			Agent:           result.AgentLabel,
			DurationSeconds: &durationSeconds,
			Message:         fmt.Sprintf("%s agent session timed out after %s", result.AgentLabel, timeoutErr.Timeout),
		}
		if appendErr := r.eventAppender.AppendEvent(request.PlanDir, event); appendErr != nil {
			writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: append session timeout event: %v", appendErr))
		}
	}

	if result.ReportMetricsWarning && (stateErr == nil || result.MetricsUsable) {
		writeAgentLogDiagnostic(log, "tao telemetry warning: "+result.MetricsWarningMessage)
	}

	var capErr error
	if metricsRequested && stateErr == nil && r.eventAppender != nil && result.MetricsUsable {
		metrics := collectAgentMetrics(state, request.Metrics.SliceID, result.AgentLabel, result.MetricsMessage, result.Metrics, runErr)
		if appendErr := r.eventAppender.AppendEvent(request.PlanDir, metrics.event(now(r).UTC())); appendErr != nil {
			writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: append metrics event: %v", appendErr))
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
		writeAgentLogDiagnostic(log, "tao telemetry warning: "+warning)
	}
	if caps.OutputTokens == nil && caps.Cost == nil {
		return nil
	}

	detail, err := plan.NewFileRepository(filepath.Dir(planDir)).GetPlan(ctx, filepath.Base(planDir))
	if err != nil {
		writeAgentLogDiagnostic(log, fmt.Sprintf("tao telemetry warning: read metrics for slice cap: %v", err))
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
