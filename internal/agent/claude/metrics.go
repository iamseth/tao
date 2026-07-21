package claude

import (
	"github.com/iamseth/tao/internal/agent/jsonmap"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
)

// parseSessionMetrics extracts typed metrics from a Claude Result. The provider
// is always Anthropic, and TotalTokens falls back to input+output when the
// stream omits an explicit total.
func parseSessionMetrics(result Result) agentmetrics.Metrics {
	metrics := agentmetrics.Metrics{
		SessionID:    result.SessionID,
		ProviderID:   "anthropic",
		ModelID:      result.Model,
		InputTokens:  jsonmap.FirstInt64(result.Usage, "input_tokens", "inputTokens"),
		OutputTokens: jsonmap.FirstInt64(result.Usage, "output_tokens", "outputTokens"),
		TotalTokens:  jsonmap.FirstInt64(result.Usage, "total_tokens", "totalTokens"),
		Cost:         result.CostUSD,
	}
	if metrics.TotalTokens == 0 {
		metrics.TotalTokens = metrics.InputTokens + metrics.OutputTokens
	}
	return metrics
}

// metricsWarning reports why Claude metrics could not be captured from the
// stream output, or an empty string when typed metrics are usable.
func metricsWarning(result Result) string {
	if result.SessionID == "" && result.Model == "" && len(result.Usage) == 0 && result.CostUSD == 0 {
		return "claude metrics absent from stream output"
	}
	if len(result.Usage) > 0 &&
		jsonmap.FirstInt64(result.Usage, "input_tokens", "inputTokens") == 0 &&
		jsonmap.FirstInt64(result.Usage, "output_tokens", "outputTokens") == 0 &&
		jsonmap.FirstInt64(result.Usage, "total_tokens", "totalTokens") == 0 {
		return "claude token metrics absent or unparsable from stream output"
	}
	return ""
}
