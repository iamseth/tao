package pi

import (
	"encoding/json"
	"testing"

	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
)

func TestParseSessionMetricsCamelCaseAndNestedTokens(t *testing.T) {
	state := map[string]any{
		"model": map[string]any{"provider": "pi-provider", "id": "pi-model"},
	}
	stats := map[string]any{
		"sessionId":         "session-pi",
		"tokens":            map[string]any{"input": float64(10), "output": float64(5), "total": float64(15)},
		"totalMessages":     float64(3),
		"assistantMessages": float64(2),
		"toolCalls":         float64(4),
	}

	got := parseSessionMetrics(state, stats)
	if got.SessionID != "session-pi" || got.ProviderID != "pi-provider" || got.ModelID != "pi-model" {
		t.Fatalf("unexpected identity: %#v", got)
	}
	if got.InputTokens != 10 || got.OutputTokens != 5 || got.TotalTokens != 15 {
		t.Fatalf("unexpected token totals: %#v", got)
	}
	if got.TotalMessages != 3 || got.AssistantMessages != 2 || got.ToolCalls != 4 {
		t.Fatalf("unexpected message/tool totals: %#v", got)
	}
}

func TestParseSessionMetricsSnakeCaseAndModelFallback(t *testing.T) {
	state := map[string]any{
		"model": map[string]any{"provider": "state-provider", "id": "state-model"},
	}
	stats := map[string]any{
		"input_tokens":       json.Number("11"),
		"output_tokens":      int64(7),
		"reasoning_tokens":   int(3),
		"cache_read_tokens":  float64(2),
		"cache_write_tokens": json.Number("5"),
		"total_tokens":       json.Number("28"),
		"cost":               json.Number("1.25"),
		"erroredMessages":    json.Number("1"),
	}

	got := parseSessionMetrics(state, stats)
	if got.ProviderID != "state-provider" || got.ModelID != "state-model" {
		t.Fatalf("expected model/provider fallback from state: %#v", got)
	}
	if got.InputTokens != 11 || got.OutputTokens != 7 || got.ReasoningTokens != 3 || got.CacheReadTokens != 2 || got.CacheWriteTokens != 5 || got.TotalTokens != 28 {
		t.Fatalf("unexpected numeric conversion: %#v", got)
	}
	if got.Cost != 1.25 || got.ErroredMessages != 1 {
		t.Fatalf("unexpected cost/errored mapping: %#v", got)
	}
	if got.SessionID != "" {
		t.Fatalf("expected empty session id without stats key, got %q", got.SessionID)
	}
}

func TestParseSessionMetricsHandlesNilMaps(t *testing.T) {
	got := parseSessionMetrics(nil, nil)
	if got != (agentmetrics.Metrics{}) {
		t.Fatalf("expected zero metrics for nil maps, got %#v", got)
	}
}

func TestFloat64ValueHandlesMissingAndBadValues(t *testing.T) {
	values := map[string]any{"badFloat": json.Number("nope"), "text": "12"}
	if float64Value(values, "badFloat") != 0 || float64Value(values, "text") != 0 || float64Value(nil, "missing") != 0 {
		t.Fatal("expected bad or missing float values to convert to zero")
	}
}
