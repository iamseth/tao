package claude

import (
	"encoding/json"
	"testing"

	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
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

func TestParseSessionMetricsPresence(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		event                      event
		availability               agentmetrics.Availability
		input, output, total, cost bool
	}{
		{name: "absent", availability: agentmetrics.Unavailable},
		{name: "identity only", event: event{"session_id": "session", "model": "model"}, availability: agentmetrics.Unavailable},
		{name: "complete", event: event{"usage": map[string]any{"input_tokens": 3, "output_tokens": 2}, "total_cost_usd": 1.0}, availability: agentmetrics.Reported, input: true, output: true, total: true, cost: true},
		{name: "zero", event: event{"usage": map[string]any{"inputTokens": 0, "outputTokens": 0}, "total_cost_usd": 0}, availability: agentmetrics.Reported, input: true, output: true, total: true, cost: true},
		{name: "partial", event: event{"usage": map[string]any{"input_tokens": 0}}, availability: agentmetrics.Partial, input: true},
		{name: "cost only zero", event: event{"cost_usd": 0}, availability: agentmetrics.Partial, cost: true},
		{name: "invalid", event: event{"usage": map[string]any{"input_tokens": "0", "output_tokens": nil}, "cost": "bad"}, availability: agentmetrics.Unavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var result Result
			extractTelemetry(tt.event, &result)
			got := parseSessionMetrics(result)
			if got.Availability != tt.availability || got.InputTokensPresent != tt.input || got.OutputTokensPresent != tt.output || got.TotalTokensPresent != tt.total || got.CostPresent != tt.cost {
				t.Fatalf("presence = %+v", got)
			}
		})
	}
}

func TestParseSessionMetricsExplicitZeroWinsFallback(t *testing.T) {
	got := parseSessionMetrics(Result{Usage: map[string]any{"input_tokens": 0, "inputTokens": 9, "output_tokens": 5, "total_tokens": 0}})
	if got.InputTokens != 0 || !got.InputTokensPresent || got.TotalTokens != 0 || !got.TotalTokensPresent {
		t.Fatalf("explicit zero lost: %+v", got)
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
