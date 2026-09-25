package pi

import (
	"math"

	"github.com/iamseth/tao/internal/agent/jsonmap"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
)

// parseSessionMetrics extracts typed metrics from the session State/Stats maps.
// It preserves the dual-key fallbacks (snake_case then camelCase) and the
// model/provider fallbacks sourced from state["model"].
func parseSessionMetrics(state, stats map[string]any) agentmetrics.Metrics {
	model, _ := state["model"].(map[string]any)
	tokens, _ := stats["tokens"].(map[string]any)
	metrics := agentmetrics.Metrics{
		SessionID:         jsonmap.FirstString(stats, "session_id", "sessionId"),
		ProviderID:        jsonmap.FirstString(stats, "provider_id", "providerId"),
		ModelID:           jsonmap.FirstString(stats, "model_id", "modelId"),
		TotalMessages:     jsonmap.FirstInt64(stats, "total_messages", "totalMessages"),
		UserMessages:      jsonmap.FirstInt64(stats, "user_messages", "userMessages"),
		AssistantMessages: jsonmap.FirstInt64(stats, "assistant_messages", "assistantMessages"),
		ErroredMessages:   jsonmap.FirstInt64(stats, "errored_messages", "erroredMessages"),
		ToolCalls:         jsonmap.FirstInt64(stats, "tool_calls", "toolCalls"),
	}
	metrics.InputTokens, metrics.InputTokensPresent = tokenValue(stats, tokens, "input_tokens", "input")
	metrics.OutputTokens, metrics.OutputTokensPresent = tokenValue(stats, tokens, "output_tokens", "output")
	metrics.ReasoningTokens, metrics.ReasoningTokensPresent = tokenValue(stats, tokens, "reasoning_tokens", "reasoning")
	metrics.CacheReadTokens, metrics.CacheReadTokensPresent = tokenValue(stats, tokens, "cache_read_tokens", "cacheRead")
	metrics.CacheWriteTokens, metrics.CacheWriteTokensPresent = tokenValue(stats, tokens, "cache_write_tokens", "cacheWrite")
	metrics.TotalTokens, metrics.TotalTokensPresent = tokenValue(stats, tokens, "total_tokens", "total")
	metrics.Cost, metrics.CostPresent = agentmetrics.CostValue(stats["cost"])
	metrics.ClassifyAvailability()
	if metrics.ProviderID == "" {
		metrics.ProviderID = jsonmap.String(model, "provider")
	}
	if metrics.ModelID == "" {
		metrics.ModelID = jsonmap.String(model, "id")
	}
	return metrics
}

// collectMessageMetrics retains usage already delivered with completed assistant
// messages. It never requests more RPC work, and stream-only coverage is partial:
// only get_session_stats can establish complete session coverage.
func collectMessageMetrics(ev event, result *Result) {
	if eventType(ev) != "message_end" {
		return
	}
	message, _ := ev["message"].(map[string]any)
	if jsonmap.String(message, "role") != "assistant" {
		return
	}
	usage, _ := message["usage"].(map[string]any)
	cost, _ := usage["cost"].(map[string]any)
	incoming := parseSessionMetrics(nil, map[string]any{"tokens": usage, "cost": cost["total"]})
	// Pi message usage calls its explicit total totalTokens, unlike RPC stats.
	incoming.TotalTokens, incoming.TotalTokensPresent = agentmetrics.TokenValue(usage["totalTokens"], usage["total"])
	m := &result.Metrics
	source := tokenFields(&incoming)
	for i, field := range tokenFields(m) {
		value := *source[i].value
		if *source[i].present && *field.value <= math.MaxInt64-value {
			*field.value += value
			*field.present = true
		}
	}
	if incoming.CostPresent && !math.IsInf(m.Cost+incoming.Cost, 0) {
		m.Cost += incoming.Cost
		m.CostPresent = true
	}
	m.ClassifyAvailability()
	if m.Availability == agentmetrics.Reported {
		m.Availability = agentmetrics.Partial
	}
}

// Final stats replace, rather than add to, already observed message usage.
// Missing fields retain partial observations without claiming full coverage.
func preferSessionMetrics(stats, observed agentmetrics.Metrics) agentmetrics.Metrics {
	source := tokenFields(&observed)
	usedObserved := false
	for i, field := range tokenFields(&stats) {
		if !*field.present && *source[i].present {
			*field.value, *field.present = *source[i].value, true
			usedObserved = true
		}
	}
	if !stats.CostPresent && observed.CostPresent {
		stats.Cost, stats.CostPresent = observed.Cost, true
		usedObserved = true
	}
	if usedObserved {
		stats.Availability = agentmetrics.Partial
	}
	return stats
}

type tokenField struct {
	value   *int64
	present *bool
}

func tokenFields(m *agentmetrics.Metrics) []tokenField {
	return []tokenField{
		{&m.InputTokens, &m.InputTokensPresent},
		{&m.OutputTokens, &m.OutputTokensPresent},
		{&m.ReasoningTokens, &m.ReasoningTokensPresent},
		{&m.CacheReadTokens, &m.CacheReadTokensPresent},
		{&m.CacheWriteTokens, &m.CacheWriteTokensPresent},
		{&m.TotalTokens, &m.TotalTokensPresent},
	}
}

func tokenValue(primary, secondary map[string]any, primaryKey, secondaryKey string) (int64, bool) {
	return agentmetrics.TokenValue(primary[primaryKey], primary[secondaryKey], secondary[secondaryKey])
}
