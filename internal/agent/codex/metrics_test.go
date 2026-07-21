package codex

import "testing"

func TestParseSessionMetricsCopiesAccumulatedUsage(t *testing.T) {
	result := Result{
		SessionID:     "thread_1",
		ProviderID:    "openai",
		ModelID:       "gpt-5-codex",
		UsageObserved: true,
		Usage:         TokenUsage{Input: 10, CachedInput: 2, Output: 4, ReasoningOutput: 3, Total: 17},
	}

	got := parseSessionMetrics(result)
	if got.SessionID != "thread_1" || got.ProviderID != "openai" || got.ModelID != "gpt-5-codex" {
		t.Fatalf("unexpected identity: %#v", got)
	}
	if got.InputTokens != 10 || got.CacheReadTokens != 2 || got.OutputTokens != 4 || got.ReasoningTokens != 3 || got.TotalTokens != 17 || got.Cost != 0 {
		t.Fatalf("unexpected numeric mapping: %#v", got)
	}
}

func TestParseSessionMetricsTotalFallback(t *testing.T) {
	result := Result{UsageObserved: true, Usage: TokenUsage{Input: 10, Output: 5, ReasoningOutput: 3}}

	got := parseSessionMetrics(result)
	if got.TotalTokens != 18 {
		t.Fatalf("expected total fallback to input+output+reasoning, got %d", got.TotalTokens)
	}
}

func TestMetricsWarningAbsentWhenNothingPresent(t *testing.T) {
	for _, result := range []Result{{}, {ProviderID: "openai"}} {
		if warning := metricsWarning(result); warning != "codex metrics absent from json output" {
			t.Fatalf("expected metrics-absent warning, got %q", warning)
		}
	}
}

func TestMetricsWarningTokensAbsentWhenUsageObservedButZero(t *testing.T) {
	result := Result{SessionID: "thread_1", ProviderID: "openai", UsageObserved: true}
	if warning := metricsWarning(result); warning != "codex token metrics absent or unparsable from json output" {
		t.Fatalf("expected token-absent warning, got %q", warning)
	}
}

func TestMetricsWarningEmptyWhenTokensPresent(t *testing.T) {
	for _, usage := range []TokenUsage{{Input: 10}, {ReasoningOutput: 5}} {
		result := Result{SessionID: "thread_1", ProviderID: "openai", UsageObserved: true, Usage: usage}
		if warning := metricsWarning(result); warning != "" {
			t.Fatalf("expected no warning for %#v, got %q", usage, warning)
		}
	}
}
