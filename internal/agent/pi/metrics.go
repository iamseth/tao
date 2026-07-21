package pi

import (
	"encoding/json"

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
		InputTokens:       firstInt64Value(stats, tokens, "input_tokens", "input"),
		OutputTokens:      firstInt64Value(stats, tokens, "output_tokens", "output"),
		ReasoningTokens:   firstInt64Value(stats, tokens, "reasoning_tokens", "reasoning"),
		CacheReadTokens:   firstInt64Value(stats, tokens, "cache_read_tokens", "cacheRead"),
		CacheWriteTokens:  firstInt64Value(stats, tokens, "cache_write_tokens", "cacheWrite"),
		TotalTokens:       firstInt64Value(stats, tokens, "total_tokens", "total"),
		Cost:              float64Value(stats, "cost"),
		TotalMessages:     firstInt64Value(stats, nil, "total_messages", "totalMessages"),
		UserMessages:      firstInt64Value(stats, nil, "user_messages", "userMessages"),
		AssistantMessages: firstInt64Value(stats, nil, "assistant_messages", "assistantMessages"),
		ErroredMessages:   firstInt64Value(stats, nil, "errored_messages", "erroredMessages"),
		ToolCalls:         firstInt64Value(stats, nil, "tool_calls", "toolCalls"),
	}
	if metrics.ProviderID == "" {
		metrics.ProviderID = jsonmap.String(model, "provider")
	}
	if metrics.ModelID == "" {
		metrics.ModelID = jsonmap.String(model, "id")
	}
	return metrics
}

// firstInt64Value is Pi's two-map lookup: it tries the primary map under both
// key spellings before falling back to the secondary map. This differs from the
// shared variadic jsonmap.FirstInt64, so it stays local to the Pi runtime.
func firstInt64Value(primary map[string]any, secondary map[string]any, primaryKey string, secondaryKey string) int64 {
	if value := jsonmap.Int64(primary, primaryKey); value != 0 {
		return value
	}
	if value := jsonmap.Int64(primary, secondaryKey); value != 0 {
		return value
	}
	return jsonmap.Int64(secondary, secondaryKey)
}

func float64Value(values map[string]any, key string) float64 {
	switch value := values[key].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case int:
		return float64(value)
	case json.Number:
		parsed, _ := value.Float64()
		return parsed
	default:
		return 0
	}
}
