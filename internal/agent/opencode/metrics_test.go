package opencode

import "testing"

func TestParseSessionMetricsCopiesAccumulatedUsage(t *testing.T) {
	result := Result{
		SessionID:     "ses_1",
		ProviderID:    "openai",
		UsageObserved: true,
		Usage:         TokenUsage{Input: 7019, Output: 72, Reasoning: 37, Total: 7128},
		Cost:          0.03,
	}

	got := parseSessionMetrics(result)
	if got.SessionID != "ses_1" || got.ProviderID != "openai" {
		t.Fatalf("unexpected identity: %#v", got)
	}
	if got.InputTokens != 7019 || got.OutputTokens != 72 || got.TotalTokens != 7128 || got.Cost != 0.03 {
		t.Fatalf("unexpected numeric mapping: %#v", got)
	}
}

func TestParseSessionMetricsTotalFallback(t *testing.T) {
	result := Result{
		ProviderID:    "anthropic",
		UsageObserved: true,
		Usage:         TokenUsage{Input: 10, Output: 5, Reasoning: 3},
	}

	got := parseSessionMetrics(result)
	if got.TotalTokens != 18 {
		t.Fatalf("expected total fallback to input+output+reasoning, got %d", got.TotalTokens)
	}
}

func TestMetricsWarningAbsentWhenNothingPresent(t *testing.T) {
	if warning := metricsWarning(Result{}); warning != "opencode metrics absent from json output" {
		t.Fatalf("expected metrics-absent warning, got %q", warning)
	}
}

func TestMetricsWarningTokensAbsentWhenUsageObservedButZero(t *testing.T) {
	result := Result{SessionID: "ses_1", UsageObserved: true}
	if warning := metricsWarning(result); warning != "opencode token metrics absent or unparsable from json output" {
		t.Fatalf("expected token-absent warning, got %q", warning)
	}
}

func TestMetricsWarningEmptyWhenTokensPresent(t *testing.T) {
	result := Result{
		SessionID:     "ses_1",
		UsageObserved: true,
		Usage:         TokenUsage{Input: 10},
	}
	if warning := metricsWarning(result); warning != "" {
		t.Fatalf("expected no warning, got %q", warning)
	}
}

func TestMetricsWarningEmptyWhenOnlyCostPresent(t *testing.T) {
	result := Result{Cost: 0.5, UsageObserved: true, Usage: TokenUsage{Output: 3}}
	if warning := metricsWarning(result); warning != "" {
		t.Fatalf("expected no warning when usage present, got %q", warning)
	}
}
