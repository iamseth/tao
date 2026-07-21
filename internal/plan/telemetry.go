package plan

import (
	"sort"
	"time"
)

// Telemetry derives local summaries from best-effort agent metric events; it must not
// affect plan lifecycle or artifact readability.
const EventTypeAgentMetrics = "agent_metrics"

const (
	DefaultAgentTotalTokensBudget       int64 = 200000
	DefaultAgentToolCallsBudget         int64 = 75
	DefaultAgentAssistantMessagesBudget int64 = 50
	DefaultAgentErroredMessagesBudget   int64 = 0
)

// AgentMetrics is the durable metrics payload stored on agent_metrics events.
type AgentMetrics struct {
	Agent             string  `json:"agent,omitempty"`
	SessionID         string  `json:"session_id"`
	ProviderID        string  `json:"provider_id,omitempty"`
	ModelID           string  `json:"model_id,omitempty"`
	Status            string  `json:"status,omitempty"`
	Result            string  `json:"result,omitempty"`
	InputTokens       int64   `json:"input_tokens,omitempty"`
	OutputTokens      int64   `json:"output_tokens,omitempty"`
	ReasoningTokens   int64   `json:"reasoning_tokens,omitempty"`
	CacheReadTokens   int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens  int64   `json:"cache_write_tokens,omitempty"`
	TotalTokens       int64   `json:"total_tokens,omitempty"`
	Cost              float64 `json:"cost,omitempty"`
	TotalMessages     int64   `json:"total_messages,omitempty"`
	UserMessages      int64   `json:"user_messages,omitempty"`
	AssistantMessages int64   `json:"assistant_messages,omitempty"`
	ErroredMessages   int64   `json:"errored_messages,omitempty"`
	ToolCalls         int64   `json:"tool_calls,omitempty"`
}

type AgentMetricEvent struct {
	PlanID    string       `json:"plan_id"`
	SliceID   string       `json:"slice_id,omitempty"`
	Timestamp time.Time    `json:"timestamp"`
	Metrics   AgentMetrics `json:"metrics"`
}

type AgentTelemetrySummary struct {
	Totals     AgentMetricsTotals  `json:"totals"`
	BySlice    []AgentMetricsGroup `json:"by_slice"`
	ByAgent    []AgentMetricsGroup `json:"by_agent"`
	ByModel    []AgentMetricsGroup `json:"by_model"`
	ByProvider []AgentMetricsGroup `json:"by_provider"`
	Events     []AgentMetricEvent  `json:"events"`
}

type AgentAudit struct {
	Planning  []AgentAuditEntry `json:"planning,omitempty"`
	Execution []AgentAuditEntry `json:"execution,omitempty"`
}

type AgentAuditEntry struct {
	Agent     string    `json:"agent"`
	Source    string    `json:"source"`
	EventType string    `json:"event_type,omitempty"`
	SliceID   string    `json:"slice_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type AgentMetricsTotals struct {
	Sessions          int     `json:"sessions"`
	Attempts          int     `json:"attempts"`
	FailedAttempts    int     `json:"failed_attempts"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	ReasoningTokens   int64   `json:"reasoning_tokens"`
	CacheReadTokens   int64   `json:"cache_read_tokens"`
	CacheWriteTokens  int64   `json:"cache_write_tokens"`
	TotalTokens       int64   `json:"total_tokens"`
	Cost              float64 `json:"cost"`
	TotalMessages     int64   `json:"total_messages"`
	UserMessages      int64   `json:"user_messages"`
	AssistantMessages int64   `json:"assistant_messages"`
	ErroredMessages   int64   `json:"errored_messages"`
	ToolCalls         int64   `json:"tool_calls"`
}

type AgentMetricsGroup struct {
	Key    string             `json:"key"`
	Totals AgentMetricsTotals `json:"totals"`
}

type AgentBudgetWarning struct {
	Scope     string `json:"scope"`
	SliceID   string `json:"slice_id,omitempty"`
	Metric    string `json:"metric"`
	Threshold int64  `json:"threshold"`
	Observed  int64  `json:"observed"`
	Message   string `json:"message"`
}

func AgentMetricsEvents(events []Event) []AgentMetricEvent {
	metrics := make([]AgentMetricEvent, 0)
	for _, event := range events {
		if event.Type != EventTypeAgentMetrics || event.Metrics == nil {
			continue
		}
		metricsPayload := *event.Metrics
		if metricsPayload.Agent == "" {
			metricsPayload.Agent = event.Agent
		}
		metrics = append(metrics, AgentMetricEvent{
			PlanID:    event.PlanID,
			SliceID:   event.SliceID,
			Timestamp: event.Timestamp,
			Metrics:   metricsPayload,
		})
	}
	return metrics
}

// AgentAuditTrail summarizes which agents produced planning and execution evidence.
func AgentAuditTrail(detail *PlanDetail) AgentAudit {
	if detail == nil {
		return AgentAudit{}
	}
	audit := AgentAudit{}
	if detail.PlanningSession.Stats != nil && detail.PlanningSession.Stats.Agent != "" {
		audit.Planning = append(audit.Planning, AgentAuditEntry{Agent: detail.PlanningSession.Stats.Agent, Source: PlanningSessionStatsFile})
	}
	for _, event := range detail.Events {
		agent := event.Agent
		if agent == "" && event.Metrics != nil {
			agent = event.Metrics.Agent
		}
		if agent == "" {
			continue
		}
		entry := AgentAuditEntry{Agent: agent, Source: "events.jsonl", EventType: event.Type, SliceID: event.SliceID, Timestamp: event.Timestamp}
		switch event.Type {
		case "plan_created", "planning_session_capture":
			audit.Planning = append(audit.Planning, entry)
		case EventTypeRunContext, EventTypeAgentMetrics:
			audit.Execution = append(audit.Execution, entry)
		}
	}
	return audit
}

// SummarizeAgentTelemetry summarizes generic agent_metrics events.
func SummarizeAgentTelemetry(detail *PlanDetail) AgentTelemetrySummary {
	return SummarizeAgentMetrics(AgentMetricsEvents(detail.Events))
}

func SummarizeAgentMetrics(events []AgentMetricEvent) AgentTelemetrySummary {
	summary := AgentTelemetrySummary{Events: append([]AgentMetricEvent(nil), events...)}
	seenSessions := make(map[string]bool)
	bySlice := make(map[string]*AgentMetricsTotals)
	byAgent := make(map[string]*AgentMetricsTotals)
	byModel := make(map[string]*AgentMetricsTotals)
	byProvider := make(map[string]*AgentMetricsTotals)
	bySliceSessions := make(map[string]map[string]bool)
	byAgentSessions := make(map[string]map[string]bool)
	byModelSessions := make(map[string]map[string]bool)
	byProviderSessions := make(map[string]map[string]bool)

	for _, event := range events {
		addMetrics(&summary.Totals, event.Metrics, seenSessions)
		addGroupMetrics(bySlice, bySliceSessions, event.SliceID, event.Metrics)
		addGroupMetrics(byAgent, byAgentSessions, event.Metrics.Agent, event.Metrics)
		addGroupMetrics(byModel, byModelSessions, event.Metrics.ModelID, event.Metrics)
		addGroupMetrics(byProvider, byProviderSessions, event.Metrics.ProviderID, event.Metrics)
	}

	summary.BySlice = sortedGroups(bySlice)
	summary.ByAgent = sortedGroups(byAgent)
	summary.ByModel = sortedGroups(byModel)
	summary.ByProvider = sortedGroups(byProvider)
	return summary
}

func AgentBudgetWarnings(detail *PlanDetail) []AgentBudgetWarning {
	return AgentTelemetryBudgetWarnings(SummarizeAgentTelemetry(detail))
}

func AgentTelemetryBudgetWarnings(summary AgentTelemetrySummary) []AgentBudgetWarning {
	warnings := make([]AgentBudgetWarning, 0)
	warnings = appendBudgetWarnings(warnings, "plan", "", summary.Totals)
	for _, group := range summary.BySlice {
		warnings = appendBudgetWarnings(warnings, "slice", group.Key, group.Totals)
	}
	sortBudgetWarnings(warnings)
	return warnings
}

func appendBudgetWarnings(warnings []AgentBudgetWarning, scope string, sliceID string, totals AgentMetricsTotals) []AgentBudgetWarning {
	warnings = appendBudgetWarning(warnings, scope, sliceID, "total_tokens", DefaultAgentTotalTokensBudget, totals.TotalTokens)
	warnings = appendBudgetWarning(warnings, scope, sliceID, "tool_calls", DefaultAgentToolCallsBudget, totals.ToolCalls)
	warnings = appendBudgetWarning(warnings, scope, sliceID, "assistant_messages", DefaultAgentAssistantMessagesBudget, totals.AssistantMessages)
	warnings = appendBudgetWarning(warnings, scope, sliceID, "errored_messages", DefaultAgentErroredMessagesBudget, totals.ErroredMessages)
	return warnings
}

func appendBudgetWarning(warnings []AgentBudgetWarning, scope string, sliceID string, metric string, threshold int64, observed int64) []AgentBudgetWarning {
	if observed <= threshold {
		return warnings
	}
	warning := AgentBudgetWarning{
		Scope:     scope,
		SliceID:   sliceID,
		Metric:    metric,
		Threshold: threshold,
		Observed:  observed,
	}
	if sliceID != "" {
		warning.Message = "Agent metrics " + metric + " budget exceeded for slice " + sliceID
	} else {
		warning.Message = "Agent metrics " + metric + " budget exceeded for plan"
	}
	return append(warnings, warning)
}

func sortBudgetWarnings(warnings []AgentBudgetWarning) {
	sort.Slice(warnings, func(i, j int) bool {
		leftExcess := warnings[i].Observed - warnings[i].Threshold
		rightExcess := warnings[j].Observed - warnings[j].Threshold
		if leftExcess != rightExcess {
			return leftExcess > rightExcess
		}
		if warnings[i].Metric != warnings[j].Metric {
			return warnings[i].Metric < warnings[j].Metric
		}
		if warnings[i].Scope != warnings[j].Scope {
			return warnings[i].Scope < warnings[j].Scope
		}
		return warnings[i].SliceID < warnings[j].SliceID
	})
}

func addGroupMetrics(groups map[string]*AgentMetricsTotals, groupSessions map[string]map[string]bool, key string, metrics AgentMetrics) {
	if key == "" {
		return
	}
	totals := groups[key]
	if totals == nil {
		totals = &AgentMetricsTotals{}
		groups[key] = totals
	}
	seen := groupSessions[key]
	if seen == nil {
		seen = make(map[string]bool)
		groupSessions[key] = seen
	}
	addMetrics(totals, metrics, seen)
}

func addMetrics(totals *AgentMetricsTotals, metrics AgentMetrics, seenSessions map[string]bool) {
	totals.Attempts++
	if metrics.SessionID != "" && !seenSessions[metrics.SessionID] {
		seenSessions[metrics.SessionID] = true
		totals.Sessions++
	}
	if metrics.Status == StatusCompleted || metrics.Result == StatusCompleted {
		// Completed attempts are the common case; every other non-empty result remains visible as failed.
	} else if metrics.Status != "" || metrics.Result != "" {
		totals.FailedAttempts++
	}
	totals.InputTokens += nonNegativeInt64(metrics.InputTokens)
	totals.OutputTokens += nonNegativeInt64(metrics.OutputTokens)
	totals.ReasoningTokens += nonNegativeInt64(metrics.ReasoningTokens)
	totals.CacheReadTokens += nonNegativeInt64(metrics.CacheReadTokens)
	totals.CacheWriteTokens += nonNegativeInt64(metrics.CacheWriteTokens)
	totals.TotalTokens += nonNegativeInt64(metrics.TotalTokens)
	totals.Cost += nonNegativeFloat64(metrics.Cost)
	totals.TotalMessages += nonNegativeInt64(metrics.TotalMessages)
	totals.UserMessages += nonNegativeInt64(metrics.UserMessages)
	totals.AssistantMessages += nonNegativeInt64(metrics.AssistantMessages)
	totals.ErroredMessages += nonNegativeInt64(metrics.ErroredMessages)
	totals.ToolCalls += nonNegativeInt64(metrics.ToolCalls)
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func nonNegativeFloat64(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func sortedGroups(groups map[string]*AgentMetricsTotals) []AgentMetricsGroup {
	result := make([]AgentMetricsGroup, 0, len(groups))
	for key, totals := range groups {
		result = append(result, AgentMetricsGroup{Key: key, Totals: *totals})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result
}
