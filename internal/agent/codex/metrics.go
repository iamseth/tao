package codex

import agentmetrics "github.com/iamseth/tao/internal/agent/metrics"

// TokenUsage is the accumulated token accounting parsed from Codex
// turn.completed usage objects.
type TokenUsage struct {
	Input           int64
	CachedInput     int64
	Output          int64
	ReasoningOutput int64
	Total           int64
}

func parseSessionMetrics(result Result) agentmetrics.Metrics {
	metrics := agentmetrics.Metrics{
		SessionID:       result.SessionID,
		ProviderID:      result.ProviderID,
		ModelID:         result.ModelID,
		InputTokens:     result.Usage.Input,
		CacheReadTokens: result.Usage.CachedInput,
		OutputTokens:    result.Usage.Output,
		ReasoningTokens: result.Usage.ReasoningOutput,
		TotalTokens:     result.Usage.Total,
	}
	if metrics.TotalTokens == 0 {
		metrics.TotalTokens = result.Usage.Input + result.Usage.Output + result.Usage.ReasoningOutput
	}
	return metrics
}

func metricsWarning(result Result) string {
	if result.SessionID == "" && result.ModelID == "" && !result.UsageObserved {
		return "codex metrics absent from json output"
	}
	if result.UsageObserved && result.Usage.Input == 0 && result.Usage.CachedInput == 0 && result.Usage.Output == 0 && result.Usage.ReasoningOutput == 0 && result.Usage.Total == 0 {
		return "codex token metrics absent or unparsable from json output"
	}
	return ""
}
