package opencode

import agentmetrics "github.com/iamseth/tao/internal/agent/metrics"

// TokenUsage is the accumulated token accounting parsed from the OpenCode
// `step_finish` events. OpenCode reports usage per assistant step, so each
// field is summed across every step observed in the stream. The per-step
// "total" already folds in reasoning and cache-read tokens, so summing the
// reported totals yields the session total.
type TokenUsage struct {
	Input      int64
	Output     int64
	Reasoning  int64
	Total      int64
	CacheRead  int64
	CacheWrite int64
}

// parseSessionMetrics extracts typed metrics from an OpenCode Result. The
// OpenCode JSON stream omits a model identifier, so ModelID is currently always
// empty. Future requested-model plumb-through belongs in the openCodeRuntime
// adapter.
// TotalTokens falls back to input+output+reasoning when the stream omits an
// explicit total.
func parseSessionMetrics(result Result) agentmetrics.Metrics {
	metrics := agentmetrics.Metrics{
		SessionID:    result.SessionID,
		ProviderID:   result.ProviderID,
		ModelID:      result.ModelID,
		InputTokens:  result.Usage.Input,
		OutputTokens: result.Usage.Output,
		TotalTokens:  result.Usage.Total,
		Cost:         result.Cost,
	}
	if metrics.TotalTokens == 0 {
		metrics.TotalTokens = result.Usage.Input + result.Usage.Output + result.Usage.Reasoning
	}
	return metrics
}

// metricsWarning reports why OpenCode metrics could not be captured from the
// JSON output, or an empty string when typed metrics are usable. Because the
// model identifier is never present in the stream, its absence alone is not a
// warning condition.
func metricsWarning(result Result) string {
	if result.SessionID == "" && result.ProviderID == "" && !result.UsageObserved && result.Cost == 0 {
		return "opencode metrics absent from json output"
	}
	if result.UsageObserved && result.Usage.Input == 0 && result.Usage.Output == 0 && result.Usage.Total == 0 {
		return "opencode token metrics absent or unparsable from json output"
	}
	return ""
}
