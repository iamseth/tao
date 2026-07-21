package plan

import (
	"encoding/json"
	"testing"
)

func TestEventDecodesAgentMetrics(t *testing.T) {
	var event Event
	if err := json.Unmarshal([]byte(`{
  "type":"agent_metrics",
  "timestamp":"2026-05-01T23:00:00Z",
  "plan_id":"plan",
  "slice_id":"001-a",
  "metrics":{
    "session_id":"ses_1234567890",
    "provider_id":"anthropic",
    "model_id":"claude-sonnet-4",
    "status":"completed",
    "result":"completed",
    "input_tokens":100,
    "output_tokens":40,
    "reasoning_tokens":5,
    "cache_read_tokens":10,
    "cache_write_tokens":3,
    "total_tokens":158,
    "cost":0.42,
    "total_messages":3,
    "user_messages":1,
    "assistant_messages":2,
    "errored_messages":1,
    "tool_calls":4
  },
  "message":"captured metrics"
}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.Metrics == nil {
		t.Fatal("expected metrics payload")
	}
	if event.Metrics.SessionID != "ses_1234567890" || event.Metrics.ProviderID != "anthropic" || event.Metrics.ModelID != "claude-sonnet-4" {
		t.Fatalf("unexpected model fields: %+v", event.Metrics)
	}
	if event.Metrics.InputTokens != 100 || event.Metrics.OutputTokens != 40 || event.Metrics.TotalTokens != 158 {
		t.Fatalf("unexpected token fields: %+v", event.Metrics)
	}
	if event.Metrics.Cost != 0.42 || event.Metrics.ToolCalls != 4 {
		t.Fatalf("unexpected cost/tool fields: %+v", event.Metrics)
	}
	if event.Metrics.TotalMessages != 3 || event.Metrics.UserMessages != 1 || event.Metrics.AssistantMessages != 2 || event.Metrics.ErroredMessages != 1 {
		t.Fatalf("unexpected message fields: %+v", event.Metrics)
	}
}

func TestSummarizeAgentMetricsAggregatesTotalsAndFailures(t *testing.T) {
	events := []AgentMetricEvent{
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{SessionID: "session-1", ProviderID: "anthropic", ModelID: "claude", Status: StatusCompleted, Result: StatusCompleted, InputTokens: 100, OutputTokens: 50, ReasoningTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 5, TotalTokens: 185, Cost: 0.25, TotalMessages: 3, UserMessages: 1, AssistantMessages: 2, ErroredMessages: 0, ToolCalls: 3}},
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{SessionID: "session-1", ProviderID: "anthropic", ModelID: "claude", Status: StatusBlocked, Result: StatusBlocked, InputTokens: 30, OutputTokens: 10, TotalTokens: 40, Cost: 0.05, TotalMessages: 2, UserMessages: 1, AssistantMessages: 1, ErroredMessages: 1, ToolCalls: 1}},
		{PlanID: "plan", SliceID: "002-b", Metrics: AgentMetrics{SessionID: "session-2", ProviderID: "openai", ModelID: "gpt", Status: StatusCompleted, Result: StatusCompleted, InputTokens: 70, OutputTokens: 20, TotalTokens: 90, Cost: 0.10, TotalMessages: 2, UserMessages: 1, AssistantMessages: 1, ErroredMessages: 0, ToolCalls: 2}},
	}

	summary := SummarizeAgentMetrics(events)
	if summary.Totals.Sessions != 2 || summary.Totals.Attempts != 3 || summary.Totals.FailedAttempts != 1 {
		t.Fatalf("unexpected attempt totals: %+v", summary.Totals)
	}
	if summary.Totals.InputTokens != 200 || summary.Totals.OutputTokens != 80 || summary.Totals.ReasoningTokens != 10 || summary.Totals.TotalTokens != 315 {
		t.Fatalf("unexpected token totals: %+v", summary.Totals)
	}
	if summary.Totals.Cost != 0.40 || summary.Totals.TotalMessages != 7 || summary.Totals.UserMessages != 3 || summary.Totals.AssistantMessages != 4 || summary.Totals.ErroredMessages != 1 || summary.Totals.ToolCalls != 6 {
		t.Fatalf("unexpected activity totals: %+v", summary.Totals)
	}
	if len(summary.BySlice) != 2 || summary.BySlice[0].Key != "001-a" || summary.BySlice[0].Totals.Sessions != 1 || summary.BySlice[0].Totals.FailedAttempts != 1 {
		t.Fatalf("unexpected slice groups: %+v", summary.BySlice)
	}
	if len(summary.ByModel) != 2 || summary.ByModel[0].Key != "claude" || summary.ByModel[0].Totals.TotalTokens != 225 {
		t.Fatalf("unexpected model groups: %+v", summary.ByModel)
	}
	if len(summary.ByProvider) != 2 || summary.ByProvider[1].Key != "openai" || summary.ByProvider[1].Totals.ToolCalls != 2 {
		t.Fatalf("unexpected provider groups: %+v", summary.ByProvider)
	}
}

func TestSummarizeAgentMetricsClampsNegativeValues(t *testing.T) {
	summary := SummarizeAgentMetrics([]AgentMetricEvent{{Metrics: AgentMetrics{
		Status:            StatusCompleted,
		Result:            StatusCompleted,
		InputTokens:       -1,
		OutputTokens:      -2,
		ReasoningTokens:   -3,
		CacheReadTokens:   -4,
		CacheWriteTokens:  -5,
		TotalTokens:       -6,
		Cost:              -0.5,
		TotalMessages:     -7,
		UserMessages:      -8,
		AssistantMessages: -9,
		ErroredMessages:   -10,
		ToolCalls:         -11,
	}}})

	if summary.Totals.InputTokens != 0 || summary.Totals.OutputTokens != 0 || summary.Totals.ReasoningTokens != 0 || summary.Totals.CacheReadTokens != 0 || summary.Totals.CacheWriteTokens != 0 || summary.Totals.TotalTokens != 0 || summary.Totals.Cost != 0 || summary.Totals.TotalMessages != 0 || summary.Totals.UserMessages != 0 || summary.Totals.AssistantMessages != 0 || summary.Totals.ErroredMessages != 0 || summary.Totals.ToolCalls != 0 {
		t.Fatalf("negative metrics should not reduce totals: %+v", summary.Totals)
	}
}

func TestAgentMetricsEventsFiltersMissingPayloads(t *testing.T) {
	events := []Event{
		{Type: EventTypeAgentMetrics, PlanID: "plan", SliceID: "001-a", Agent: "pi", Metrics: &AgentMetrics{SessionID: "session-1"}},
		{Type: EventTypeAgentMetrics, PlanID: "plan", SliceID: "001-b", Metrics: &AgentMetrics{Agent: "pi", SessionID: "session-2", TotalTokens: 12}},
		{Type: EventTypeAgentMetrics, PlanID: "plan", SliceID: "001-b"},
		{Type: "slice_completed", PlanID: "plan"},
		{Type: "legacy_metrics", PlanID: "plan", SliceID: "legacy", Metrics: &AgentMetrics{Agent: "pi", SessionID: "legacy"}},
	}

	metrics := AgentMetricsEvents(events)
	if len(metrics) != 2 {
		t.Fatalf("expected two metrics events, got %d", len(metrics))
	}
	if metrics[0].PlanID != "plan" || metrics[0].SliceID != "001-a" || metrics[0].Metrics.SessionID != "session-1" || metrics[0].Metrics.Agent != "pi" {
		t.Fatalf("unexpected metrics event: %+v", metrics[0])
	}
	if metrics[1].Metrics.Agent != "pi" || metrics[1].Metrics.TotalTokens != 12 {
		t.Fatalf("unexpected agent metrics event: %+v", metrics[1])
	}
}

func TestSummarizeAgentMetricsAggregatesAgents(t *testing.T) {
	events := []AgentMetricEvent{
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{Agent: "pi", SessionID: "pi-1", Status: StatusCompleted, Result: StatusCompleted, TotalTokens: 20, AssistantMessages: 2, ToolCalls: 3}},
	}

	summary := SummarizeAgentMetrics(events)
	if summary.Totals.Sessions != 1 || summary.Totals.TotalTokens != 20 || summary.Totals.ToolCalls != 3 {
		t.Fatalf("unexpected totals: %+v", summary.Totals)
	}
	if len(summary.ByAgent) != 1 || summary.ByAgent[0].Key != "pi" {
		t.Fatalf("unexpected agent groups: %+v", summary.ByAgent)
	}
	if summary.ByAgent[0].Totals.TotalTokens != 20 || summary.ByAgent[0].Totals.AssistantMessages != 2 {
		t.Fatalf("unexpected pi totals: %+v", summary.ByAgent[0])
	}
}

func TestAgentAuditTrailIncludesPlanningAndExecutionAgents(t *testing.T) {
	detail := &PlanDetail{
		PlanningSession: PlanningSessionArtifacts{Stats: &PlanningSessionStats{Agent: "pi"}},
		Events: []Event{
			{Type: "plan_created", PlanID: "plan-a", Agent: "pi"},
			{Type: EventTypeRunContext, PlanID: "plan-a", SliceID: "001-a", Agent: "pi"},
			{Type: EventTypeAgentMetrics, PlanID: "plan-a", SliceID: "001-a", Metrics: &AgentMetrics{Agent: "pi", SessionID: "run"}},
		},
	}

	audit := AgentAuditTrail(detail)
	if len(audit.Planning) != 2 || audit.Planning[0].Agent != "pi" || audit.Planning[1].Agent != "pi" {
		t.Fatalf("unexpected planning attribution: %+v", audit.Planning)
	}
	if len(audit.Execution) != 2 || audit.Execution[0].Agent != "pi" || audit.Execution[1].Agent != "pi" {
		t.Fatalf("unexpected execution attribution: %+v", audit.Execution)
	}
}

func TestAgentTelemetryBudgetWarningsNoneBelowThresholds(t *testing.T) {
	summary := SummarizeAgentMetrics([]AgentMetricEvent{
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{SessionID: "session-1", Status: StatusCompleted, Result: StatusCompleted, TotalTokens: 1000, ToolCalls: 3, AssistantMessages: 2}},
	})

	warnings := AgentTelemetryBudgetWarnings(summary)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %+v", warnings)
	}
}

func TestAgentTelemetryBudgetWarningsIncludePlanAndSliceThresholdDetails(t *testing.T) {
	summary := SummarizeAgentMetrics([]AgentMetricEvent{
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{SessionID: "session-1", Status: StatusCompleted, Result: StatusCompleted, TotalTokens: DefaultAgentTotalTokensBudget + 1}},
	})

	warnings := AgentTelemetryBudgetWarnings(summary)
	if len(warnings) != 2 {
		t.Fatalf("expected plan and slice warnings, got %+v", warnings)
	}
	if warnings[0].Metric != "total_tokens" || warnings[0].Threshold != DefaultAgentTotalTokensBudget || warnings[0].Observed != DefaultAgentTotalTokensBudget+1 {
		t.Fatalf("unexpected first warning details: %+v", warnings[0])
	}
	if warnings[1].Metric != "total_tokens" || warnings[1].Scope != "slice" || warnings[1].SliceID != "001-a" {
		t.Fatalf("unexpected slice warning details: %+v", warnings[1])
	}
}

func TestAgentTelemetryBudgetWarningsErroredMessages(t *testing.T) {
	summary := SummarizeAgentMetrics([]AgentMetricEvent{
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{SessionID: "session-1", Status: StatusCompleted, Result: StatusCompleted, ErroredMessages: 1}},
	})

	warnings := AgentTelemetryBudgetWarnings(summary)
	if len(warnings) != 2 {
		t.Fatalf("expected plan and slice errored-message warnings, got %+v", warnings)
	}
	for _, warning := range warnings {
		if warning.Metric != "errored_messages" || warning.Threshold != DefaultAgentErroredMessagesBudget || warning.Observed != 1 {
			t.Fatalf("unexpected errored-message warning: %+v", warning)
		}
	}
}

func TestAgentTelemetryBudgetWarningsSortedByLargestOutlier(t *testing.T) {
	summary := SummarizeAgentMetrics([]AgentMetricEvent{
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{SessionID: "session-1", Status: StatusCompleted, Result: StatusCompleted, ToolCalls: DefaultAgentToolCallsBudget + 1}},
		{PlanID: "plan", SliceID: "002-b", Metrics: AgentMetrics{SessionID: "session-2", Status: StatusCompleted, Result: StatusCompleted, TotalTokens: DefaultAgentTotalTokensBudget + 1000}},
	})

	warnings := AgentTelemetryBudgetWarnings(summary)
	if len(warnings) < 2 {
		t.Fatalf("expected multiple warnings, got %+v", warnings)
	}
	if warnings[0].Metric != "total_tokens" || warnings[0].Observed-warnings[0].Threshold != 1000 {
		t.Fatalf("expected largest outlier first, got %+v", warnings)
	}
	if warnings[len(warnings)-1].Metric != "tool_calls" || warnings[len(warnings)-1].Observed-warnings[len(warnings)-1].Threshold != 1 {
		t.Fatalf("expected smallest outlier last, got %+v", warnings)
	}
}
