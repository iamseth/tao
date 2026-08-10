package merge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/commandrunner"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// BatchAgentSessionConfig configures a merge-owned agent operation. Zero-value
// provider, permission, and timeout settings use the ordinary TAO_* runtime
// environment defaults.
type BatchAgentSessionConfig struct {
	Agent           runtimeconfig.AgentKind
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
}

// BatchAgentEventAppender owns repository-scoped batch telemetry persistence.
type BatchAgentEventAppender interface {
	AppendAgentEvent(BatchAgentEvent) error
}

// BatchAgentOperation identifies the merge-batch operation that owns a provider call.
type BatchAgentOperation string

const (
	BatchAgentOperationCandidateResolution BatchAgentOperation = "candidate_resolution"
	BatchAgentOperationAggregateReview     BatchAgentOperation = "aggregate_review"
	BatchAgentOperationAggregateRework     BatchAgentOperation = "aggregate_rework"
	BatchAgentOperationProposalGeneration  BatchAgentOperation = "proposal_generation"
)

// BatchAgentSessionRequest carries trusted call-site attribution and provider input.
type BatchAgentSessionRequest struct {
	BatchID         string
	Operation       BatchAgentOperation
	Attempt         int
	IntegrationRoot string
	Prompt          string
	CandidatePlanID string
}

// BatchAgentSessionResult preserves the neutral provider result while exposing
// the final text selected for merge orchestration.
type BatchAgentSessionResult struct {
	Output   string
	Provider agentsession.Result
}

// BatchAgentSession is the provider-neutral session seam used by merge batches.
type BatchAgentSession struct {
	runner        agentsession.Runner
	run           func(context.Context, agentsession.Request) (agentsession.Result, error)
	log           io.Writer
	controlRoot   string
	metrics       func(agent.Metrics, string)
	observe       func(BatchAgentSessionRequest, BatchAgentSessionResult, error)
	eventAppender BatchAgentEventAppender
	now           func() time.Time
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
		IntegrationRoot: repoRoot, Prompt: prompt, CandidatePlanID: identity.CandidatePlanID,
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
		starter = agent.DefaultProcessStarter
	}
	descriptor, ok := agent.Lookup(kind)
	if !ok {
		return BatchAgentSession{}, fmt.Errorf("unsupported agent %q", kind)
	}
	runner := agentsession.New(agentsession.Config{
		Descriptor:      descriptor,
		Deps:            agent.RuntimeDeps{ProcessStarter: starter},
		SkipPermissions: skip,
		Timeout:         timeout,
		Progress:        config.Log,
		CommandRunner:   config.CommandRunner,
	})
	clock := config.Now
	if clock == nil {
		clock = time.Now
	}
	return BatchAgentSession{
		runner: runner, run: runner.Run, log: config.Log, controlRoot: config.ControlRoot, metrics: config.Metrics,
		observe: config.Observe, eventAppender: config.EventAppender, now: clock,
	}, nil
}

// Resolve runs exactly one attributed session. Metrics parse failures are
// warnings and never replace the provider result or error.
func (s BatchAgentSession) Resolve(ctx context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
	run := s.run
	if run == nil {
		run = s.runner.Run
	}
	result, err := run(ctx, agentsession.Request{
		RepoRoot: request.IntegrationRoot, ControlRoot: s.controlRoot, Prompt: request.Prompt, CollectMetrics: true,
	})
	if s.metrics != nil {
		metrics := agent.Metrics{}
		if result.Metrics != nil {
			metrics = *result.Metrics
		}
		s.metrics(metrics, result.MetricsWarning)
	} else if result.ReportMetricsWarning && s.log != nil {
		_ = logrecord.Render(s.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: "tao telemetry warning: " + result.MetricsWarningMessage})
	}
	text := strings.TrimSpace(result.FinalText)
	if text == "" {
		text = strings.TrimSpace(result.Output)
	}
	sessionResult := BatchAgentSessionResult{Output: text, Provider: result}
	s.recordTelemetry(request, result, err)
	if s.observe != nil {
		s.observe(request, sessionResult, err)
	}
	return sessionResult, err
}

func (s BatchAgentSession) recordTelemetry(request BatchAgentSessionRequest, result agentsession.Result, sessionErr error) {
	if s.eventAppender == nil || request.BatchID == "" {
		return
	}
	outcome := BatchAgentOutcomeCompleted
	if sessionErr != nil {
		outcome = BatchAgentOutcomeFailed
	}
	var timeoutErr *agent.SessionTimeoutError
	if errors.As(sessionErr, &timeoutErr) {
		outcome = BatchAgentOutcomeTimedOut
		durationSeconds := int64(timeoutErr.Timeout / time.Second)
		if durationSeconds < 1 {
			durationSeconds = 1
		}
		event := BatchAgentEvent{
			Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeTimeout, BatchID: request.BatchID,
			Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
			Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
			TimeoutDurationSeconds: &durationSeconds,
		}
		s.appendTelemetry(event, "session timeout")
	}
	if result.MetricsUsable {
		event := BatchAgentEvent{
			Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeMetrics, BatchID: request.BatchID,
			Timestamp: s.now().UTC(), Operation: request.Operation, Attempt: request.Attempt,
			Agent: result.AgentLabel, PlanID: request.CandidatePlanID, Outcome: outcome,
			Metrics: newBatchAgentMetrics(result.Metrics),
		}
		s.appendTelemetry(event, "metrics")
	}
}

func (s BatchAgentSession) appendTelemetry(event BatchAgentEvent, label string) {
	if err := s.eventAppender.AppendAgentEvent(event); err != nil && s.log != nil {
		_ = logrecord.Render(s.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao telemetry warning: append merge-batch %s event: %v", label, err)})
	}
}
