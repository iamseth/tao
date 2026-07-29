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

func TestSummarizeRework(t *testing.T) {
	tests := []struct {
		name   string
		events []Event
		want   ReworkSummary
	}{
		{name: "no history"},
		{
			name: "rounds and distinct fingerprints",
			events: []Event{
				{Type: EventTypeReworkRound, Round: 1, Fingerprint: "findings-a"},
				{Type: EventTypeReworkRound, Round: 2, Fingerprint: "findings-b"},
				{Type: EventTypeReworkStopped, Fingerprint: "findings-b", Reason: "equivalent findings"},
			},
			want: ReworkSummary{Rounds: 2, LatestStoppedReason: "equivalent findings", DistinctFindingFingerprints: 2},
		},
		{
			name: "latest stopped reason uses first line and message fallback",
			events: []Event{
				{Type: EventTypeReworkStopped, Reason: "old reason"},
				{Type: EventTypeReworkStopped, Message: "latest reason\nextra detail"},
			},
			want: ReworkSummary{LatestStoppedReason: "latest reason"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SummarizeRework(tt.events); got != tt.want {
				t.Fatalf("SummarizeRework() = %+v, want %+v", got, tt.want)
			}
		})
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
		{Type: "opencode_metrics", PlanID: "plan", SliceID: "legacy", Metrics: &AgentMetrics{Agent: "opencode", SessionID: "legacy", OutputTokens: 9}},
		{Type: "", PlanID: "plan", Metrics: &AgentMetrics{SessionID: "empty"}},
		{Type: "slice_completed", PlanID: "plan"},
		{Type: "legacy_metrics", PlanID: "plan", SliceID: "unknown", Metrics: &AgentMetrics{Agent: "pi", SessionID: "unknown"}},
	}

	metrics := AgentMetricsEvents(events)
	if len(metrics) != 3 {
		t.Fatalf("expected three metrics events, got %d", len(metrics))
	}
	if metrics[0].PlanID != "plan" || metrics[0].SliceID != "001-a" || metrics[0].Metrics.SessionID != "session-1" || metrics[0].Metrics.Agent != "pi" {
		t.Fatalf("unexpected metrics event: %+v", metrics[0])
	}
	if metrics[1].Metrics.Agent != "pi" || metrics[1].Metrics.TotalTokens != 12 {
		t.Fatalf("unexpected agent metrics event: %+v", metrics[1])
	}
	if metrics[2].SliceID != "legacy" || metrics[2].Metrics.Agent != "opencode" || metrics[2].Metrics.OutputTokens != 9 {
		t.Fatalf("unexpected legacy opencode metrics event: %+v", metrics[2])
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

func TestDefaultAgentBudgetThresholds(t *testing.T) {
	got := DefaultAgentBudgetThresholds()
	if got.Slice.OutputTokens != 40000 || got.Slice.Cost != 5 || got.Slice.ToolCalls != 120 || got.Slice.AssistantMessages != 80 || got.Slice.ErroredMessages != 0 {
		t.Fatalf("unexpected slice defaults: %+v", got.Slice)
	}
	if got.Plan.OutputTokens != 150000 || got.Plan.Cost != 20 || got.Plan.ToolCalls != 400 || got.Plan.AssistantMessages != 300 || got.Plan.ErroredMessages != 0 {
		t.Fatalf("unexpected plan defaults: %+v", got.Plan)
	}
}

func TestAgentTelemetryBudgetWarningsUseScopedMetrics(t *testing.T) {
	thresholds := DefaultAgentBudgetThresholds()
	summary := SummarizeAgentMetrics([]AgentMetricEvent{
		{PlanID: "plan", SliceID: "001-a", Metrics: AgentMetrics{SessionID: "session-1", OutputTokens: thresholds.Slice.OutputTokens + 1, Cost: thresholds.Slice.Cost + 1, ToolCalls: thresholds.Slice.ToolCalls + 1, AssistantMessages: thresholds.Slice.AssistantMessages + 1, ErroredMessages: 1, TotalTokens: 9999999}},
	})

	warnings := AgentTelemetryBudgetWarnings(summary, thresholds)
	if len(warnings) != 6 {
		t.Fatalf("expected five slice warnings and one plan error warning, got %+v", warnings)
	}
	seen := make(map[string]bool)
	for _, warning := range warnings {
		if warning.Scope == "plan" {
			if warning.Metric != "errored_messages" {
				t.Fatalf("unexpected plan warning: %+v", warning)
			}
			continue
		}
		if warning.Scope != "slice" || warning.SliceID != "001-a" {
			t.Fatalf("unexpected warning scope: %+v", warning)
		}
		seen[warning.Metric] = true
	}
	for _, metric := range []string{"output_tokens", "cost", "tool_calls", "assistant_messages", "errored_messages"} {
		if !seen[metric] {
			t.Fatalf("missing %s warning: %+v", metric, warnings)
		}
	}
	if seen["total_tokens"] {
		t.Fatalf("total tokens must not produce a budget warning: %+v", warnings)
	}
}

func TestAgentTelemetryBudgetWarningsUsePlanThresholds(t *testing.T) {
	thresholds := DefaultAgentBudgetThresholds()
	summary := SummarizeAgentMetrics([]AgentMetricEvent{
		{SliceID: "001-a", Metrics: AgentMetrics{SessionID: "one", OutputTokens: 80000}},
		{SliceID: "002-b", Metrics: AgentMetrics{SessionID: "two", OutputTokens: 80000}},
	})

	warnings := AgentTelemetryBudgetWarnings(summary, thresholds)
	if len(warnings) != 3 {
		t.Fatalf("expected one plan and two slice warnings, got %+v", warnings)
	}
	var planWarning *AgentBudgetWarning
	for i := range warnings {
		if warnings[i].Scope == "plan" && warnings[i].Metric == "output_tokens" {
			planWarning = &warnings[i]
			break
		}
	}
	if planWarning == nil || planWarning.Threshold != 150000 || planWarning.Observed != 160000 {
		t.Fatalf("unexpected plan warning: %+v", planWarning)
	}
}
