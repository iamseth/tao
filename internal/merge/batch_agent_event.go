package merge

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/agent"
)

const (
	// BatchAgentEventSchema versions repository-scoped merge-batch telemetry.
	BatchAgentEventSchema = "tao.merge-batch-agent-event.v1"

	BatchAgentEventTypeMetrics = "agent_metrics"
	BatchAgentEventTypeTimeout = "session_timeout"

	BatchAgentOutcomeCompleted = "completed"
	BatchAgentOutcomeFailed    = "failed"
	BatchAgentOutcomeTimedOut  = "timed_out"

	maxBatchAgentEventIdentity = 256
	maxBatchAgentEventAgent    = 64
	maxBatchAgentEventBytes    = 16 << 10
)

// BatchAgentEvent is best-effort transaction telemetry. It is deliberately
// separate from BatchState and the transition log because it is not recovery
// or lifecycle evidence.
type BatchAgentEvent struct {
	Schema                 string              `json:"schema"`
	Type                   string              `json:"type"`
	BatchID                string              `json:"batch_id"`
	Timestamp              time.Time           `json:"timestamp"`
	Operation              BatchAgentOperation `json:"operation"`
	Attempt                int                 `json:"attempt"`
	Agent                  string              `json:"agent"`
	PlanID                 string              `json:"plan_id,omitempty"`
	Outcome                string              `json:"outcome"`
	Metrics                *BatchAgentMetrics  `json:"metrics,omitempty"`
	TimeoutDurationSeconds *int64              `json:"timeout_duration_seconds,omitempty"`
}

// BatchAgentMetrics is the bounded provider-neutral numeric payload retained
// for one batch call. Provider text and transcripts are never stored here.
type BatchAgentMetrics struct {
	SessionID         string  `json:"session_id,omitempty"`
	ProviderID        string  `json:"provider_id,omitempty"`
	ModelID           string  `json:"model_id,omitempty"`
	InputTokens       int64   `json:"input_tokens,omitempty"`
	OutputTokens      int64   `json:"output_tokens,omitempty"`
	ReasoningTokens   int64   `json:"reasoning_tokens,omitempty"`
	CacheReadTokens   int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens  int64   `json:"cache_write_tokens,omitempty"`
	TotalTokens       int64   `json:"total_tokens,omitempty"`
	Cost              float64 `json:"cost,omitempty"`
	TotalMessages     int64   `json:"total_messages,omitempty"`
	UserMessages      int64   `json:"user_messages,omitempty"`
	AssistantMessages int64   `json:"assistant_messages,omitempty"`
	ErroredMessages   int64   `json:"errored_messages,omitempty"`
	ToolCalls         int64   `json:"tool_calls,omitempty"`
}

func newBatchAgentMetrics(value *agent.Metrics) *BatchAgentMetrics {
	if value == nil {
		return &BatchAgentMetrics{}
	}
	return &BatchAgentMetrics{
		SessionID: value.SessionID, ProviderID: value.ProviderID, ModelID: value.ModelID,
		InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, ReasoningTokens: value.ReasoningTokens,
		CacheReadTokens: value.CacheReadTokens, CacheWriteTokens: value.CacheWriteTokens, TotalTokens: value.TotalTokens,
		Cost: value.Cost, TotalMessages: value.TotalMessages, UserMessages: value.UserMessages,
		AssistantMessages: value.AssistantMessages, ErroredMessages: value.ErroredMessages, ToolCalls: value.ToolCalls,
	}
}

func (e BatchAgentEvent) validate() error {
	if e.Schema != BatchAgentEventSchema {
		return fmt.Errorf("unsupported merge batch agent event schema %q", e.Schema)
	}
	if e.Type != BatchAgentEventTypeMetrics && e.Type != BatchAgentEventTypeTimeout {
		return fmt.Errorf("unsupported merge batch agent event type %q", e.Type)
	}
	if strings.TrimSpace(e.BatchID) == "" || len(e.BatchID) > maxBatchAgentEventIdentity || strings.ContainsAny(e.BatchID, `/\\`) {
		return fmt.Errorf("invalid merge batch agent event batch_id")
	}
	if e.Timestamp.IsZero() || e.Attempt < 1 || strings.TrimSpace(e.Agent) == "" || len(e.Agent) > maxBatchAgentEventAgent || len(e.PlanID) > maxBatchAgentEventIdentity {
		return fmt.Errorf("invalid merge batch agent event attribution")
	}
	switch e.Operation {
	case BatchAgentOperationCandidateResolution, BatchAgentOperationAggregateReview, BatchAgentOperationAggregateRework, BatchAgentOperationProposalGeneration:
	default:
		return fmt.Errorf("unsupported merge batch agent operation %q", e.Operation)
	}
	if e.Outcome != BatchAgentOutcomeCompleted && e.Outcome != BatchAgentOutcomeFailed && e.Outcome != BatchAgentOutcomeTimedOut {
		return fmt.Errorf("unsupported merge batch agent outcome %q", e.Outcome)
	}
	if e.Type == BatchAgentEventTypeMetrics {
		if e.Metrics == nil || e.TimeoutDurationSeconds != nil {
			return fmt.Errorf("merge batch metrics event requires only metrics payload")
		}
		if len(e.Metrics.SessionID) > maxBatchAgentEventIdentity || len(e.Metrics.ProviderID) > maxBatchAgentEventIdentity || len(e.Metrics.ModelID) > maxBatchAgentEventIdentity {
			return fmt.Errorf("merge batch metrics identity exceeds limit")
		}
	}
	if e.Type == BatchAgentEventTypeTimeout && (e.TimeoutDurationSeconds == nil || *e.TimeoutDurationSeconds <= 0 || e.Metrics != nil) {
		return fmt.Errorf("merge batch timeout event requires only a positive duration")
	}
	return nil
}
