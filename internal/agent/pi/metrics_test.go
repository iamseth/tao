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
	if got != (agentmetrics.Metrics{Availability: agentmetrics.Unavailable}) {
		t.Fatalf("expected zero metrics for nil maps, got %#v", got)
	}
}

func TestParseSessionMetricsPresence(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		stats                      map[string]any
		availability               agentmetrics.Availability
		input, output, total, cost bool
	}{
		{name: "absent", availability: agentmetrics.Unavailable},
		{name: "identity only", stats: map[string]any{"sessionId": "session", "modelId": "model", "toolCalls": 2}, availability: agentmetrics.Unavailable},
		{name: "complete", stats: map[string]any{"tokens": map[string]any{"input": 3, "output": 2, "total": 5}, "cost": 1.0}, availability: agentmetrics.Reported, input: true, output: true, total: true, cost: true},
		{name: "zero", stats: map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0, "cost": 0}, availability: agentmetrics.Reported, input: true, output: true, total: true, cost: true},
		{name: "partial", stats: map[string]any{"input_tokens": 0}, availability: agentmetrics.Partial, input: true},
		{name: "cost only zero", stats: map[string]any{"cost": 0}, availability: agentmetrics.Partial, cost: true},
		{name: "invalid", stats: map[string]any{"input_tokens": "0", "output_tokens": nil, "cost": json.Number("bad")}, availability: agentmetrics.Unavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSessionMetrics(nil, tt.stats)
			if got.Availability != tt.availability || got.InputTokensPresent != tt.input || got.OutputTokensPresent != tt.output || got.TotalTokensPresent != tt.total || got.CostPresent != tt.cost {
				t.Fatalf("presence = %+v", got)
			}
		})
	}
}

func TestMessageMetricsRemainPartialAndFinalStatsDoNotDoubleCount(t *testing.T) {
	var result Result
	for _, line := range []string{
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":3,"output":0,"totalTokens":3,"cacheRead":0,"cost":{"total":0}}}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":{"input":2,"output":1,"totalTokens":3,"cost":{"total":0.5}}}}`,
		`{"type":"message_update","message":{"role":"assistant","usage":{"input":999}}}`,
		`{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":999}}]}`,
		`{"type":"message_end","message":{"role":"toolResult","usage":{"input":999}}}`,
	} {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		collectMessageMetrics(ev, &result)
	}
	m := result.Metrics
	if m.Availability != agentmetrics.Partial || m.InputTokens != 5 || m.OutputTokens != 1 || m.TotalTokens != 6 || m.Cost != 0.5 || !m.CacheReadTokensPresent {
		t.Fatalf("observed usage = %+v", m)
	}
	absent := parseSessionMetrics(nil, nil)
	if got := preferSessionMetrics(absent, m); got != m {
		t.Fatalf("absent stats lost observations: %+v", got)
	}
	stats := parseSessionMetrics(nil, map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0, "cache_read_tokens": 0, "cost": 0})
	if got := preferSessionMetrics(stats, m); got != stats {
		t.Fatalf("final zero stats must replace observations: %+v", got)
	}
	stats = parseSessionMetrics(nil, map[string]any{"input_tokens": 7})
	got := preferSessionMetrics(stats, m)
	if got.Availability != agentmetrics.Partial || got.InputTokens != 7 || got.Cost != 0.5 || !got.CostPresent || got.TotalTokens != 6 {
		t.Fatalf("partial stats lost observations: %+v", got)
	}
}

func TestParseSessionMetricsZeroWinsAliasFallback(t *testing.T) {
	got := parseSessionMetrics(nil, map[string]any{"input_tokens": 0, "input": 9, "tokens": map[string]any{"input": 10, "reasoning": 0, "cacheRead": 0, "cacheWrite": 0}})
	if got.InputTokens != 0 || !got.InputTokensPresent || !got.ReasoningTokensPresent || !got.CacheReadTokensPresent || !got.CacheWriteTokensPresent {
		t.Fatalf("explicit zero lost: %+v", got)
	}
}
