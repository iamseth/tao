package run

import (
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
)

type collectedAgentMetrics struct {
	planID    string
	sliceID   string
	metrics   plan.AgentMetrics
	eventType string
	message   string
}

// collectAgentMetrics maps the neutral agent.Metrics emitted by an agent.Runtime
// onto plan.AgentMetrics, reusing the collectedAgentMetrics shaping. The agent
// label and message identify the source runtime, and runErr promotes the metrics
// to a failed status, matching the per-runtime collectors.
func collectAgentMetrics(state plan.State, sliceID, agentLabel, message string, m *agent.Metrics, runErr error) collectedAgentMetrics {
	metrics := plan.AgentMetrics{Agent: agentLabel, Status: plan.StatusCompleted, Result: plan.StatusCompleted}
	if m != nil {
		metrics.SessionID = m.SessionID
		metrics.ProviderID = m.ProviderID
		metrics.ModelID = m.ModelID
		metrics.InputTokens = m.InputTokens
		metrics.OutputTokens = m.OutputTokens
		metrics.ReasoningTokens = m.ReasoningTokens
		metrics.CacheReadTokens = m.CacheReadTokens
		metrics.CacheWriteTokens = m.CacheWriteTokens
		metrics.TotalTokens = m.TotalTokens
		metrics.Cost = m.Cost
		metrics.TotalMessages = m.TotalMessages
		metrics.UserMessages = m.UserMessages
		metrics.AssistantMessages = m.AssistantMessages
		metrics.ErroredMessages = m.ErroredMessages
		metrics.ToolCalls = m.ToolCalls
	}
	if runErr != nil {
		metrics.Status = "failed"
		metrics.Result = "failed"
	}
	return collectedAgentMetrics{planID: state.Plan.ID, sliceID: sliceID, metrics: metrics, eventType: plan.EventTypeAgentMetrics, message: message}
}

func (m collectedAgentMetrics) event(timestamp time.Time) plan.Event {
	eventType := m.eventType
	if eventType == "" {
		eventType = plan.EventTypeAgentMetrics
	}
	message := m.message
	if message == "" {
		message = "Captured agent metrics"
	}
	return plan.Event{
		Type:      eventType,
		Timestamp: timestamp.UTC(),
		PlanID:    m.planID,
		SliceID:   m.sliceID,
		Agent:     m.metrics.Agent,
		Metrics:   &m.metrics,
		Message:   message,
	}
}
