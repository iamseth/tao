// Package agenttelemetry projects bounded session facts into generic plan
// telemetry. It owns no persistence, budget policy, or lifecycle authority.
package agenttelemetry

import (
	"time"

	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/plan"
)

// Project preserves measurement presence and the original session outcome.
// Role is supplied by the trusted operation boundary, never provider prose.
func Project(result agentsession.Result, role plan.AgentRole, runErr error) plan.AgentMetrics {
	metrics := plan.AgentMetrics{
		Role: role.Normalized(), Availability: plan.AgentMetricsAvailability(result.MetricsAvailability).Normalized(),
		Agent: result.AgentLabel, Status: plan.StatusCompleted, Result: plan.StatusCompleted,
	}
	if m := result.Metrics; m != nil {
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
		metrics.InputTokensPresent = m.InputTokensPresent
		metrics.OutputTokensPresent = m.OutputTokensPresent
		metrics.ReasoningTokensPresent = m.ReasoningTokensPresent
		metrics.CacheReadTokensPresent = m.CacheReadTokensPresent
		metrics.CacheWriteTokensPresent = m.CacheWriteTokensPresent
		metrics.TotalTokensPresent = m.TotalTokensPresent
		metrics.CostPresent = m.CostPresent
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
	return metrics
}

// Event uses only explicit identities and projected facts; warnings, errors,
// prompts and provider output are deliberately excluded. SliceID may be empty.
func Event(planID, sliceID string, timestamp time.Time, metrics plan.AgentMetrics) plan.Event {
	return plan.Event{
		Type: plan.EventTypeAgentMetrics, Timestamp: timestamp.UTC(),
		PlanID: planID, SliceID: sliceID, Agent: metrics.Agent, Metrics: &metrics,
		Message: "Captured agent metrics",
	}
}
