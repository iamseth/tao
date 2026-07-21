package claude

import (
	"encoding/json"
	"testing"
)

func TestParseSessionMetricsSnakeCaseKeys(t *testing.T) {
	result := Result{
		SessionID: "session-claude",
		Model:     "claude-model",
		Usage: map[string]any{
			"input_tokens":  json.Number("11"),
			"output_tokens": int64(7),
			"total_tokens":  float64(28),
		},
		CostUSD: 1.25,
	}

	got := parseSessionMetrics(result)
	if got.SessionID != "session-claude" || got.ProviderID != "anthropic" || got.ModelID != "claude-model" {
		t.Fatalf("unexpected identity: %#v", got)
	}
	if got.InputTokens != 11 || got.OutputTokens != 7 || got.TotalTokens != 28 || got.Cost != 1.25 {
		t.Fatalf("unexpected numeric mapping: %#v", got)
	}
}

func TestParseSessionMetricsCamelCaseAndTotalFallback(t *testing.T) {
	result := Result{
		Model: "claude-model",
		Usage: map[string]any{
			"inputTokens":  float64(10),
			"outputTokens": float64(5),
		},
	}

	got := parseSessionMetrics(result)
	if got.InputTokens != 10 || got.OutputTokens != 5 {
		t.Fatalf("unexpected token mapping: %#v", got)
	}
	if got.TotalTokens != 15 {
		t.Fatalf("expected total fallback to input+output, got %d", got.TotalTokens)
	}
}

func TestMetricsWarningAbsentWhenNothingPresent(t *testing.T) {
	if warning := metricsWarning(Result{}); warning != "claude metrics absent from stream output" {
		t.Fatalf("expected metrics-absent warning, got %q", warning)
	}
}

func TestMetricsWarningTokensAbsentWhenUsagePresentButZero(t *testing.T) {
	result := Result{Usage: map[string]any{"input_tokens": float64(0), "other": "x"}}
	if warning := metricsWarning(result); warning != "claude token metrics absent or unparsable from stream output" {
		t.Fatalf("expected token-absent warning, got %q", warning)
	}
}

func TestMetricsWarningEmptyWhenTokensPresent(t *testing.T) {
	result := Result{
		SessionID: "session-claude",
		Usage:     map[string]any{"input_tokens": float64(10)},
	}
	if warning := metricsWarning(result); warning != "" {
		t.Fatalf("expected no warning, got %q", warning)
	}
}
