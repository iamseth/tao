package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Telemetry derives local summaries from best-effort agent metric events; it must not
// affect plan lifecycle or artifact readability.
const EventTypeAgentMetrics = "agent_metrics"

// AgentBudgetScopeThresholds defines advisory limits for one telemetry scope.
type AgentBudgetScopeThresholds struct {
	OutputTokens      int64
	Cost              float64
	ToolCalls         int64
	AssistantMessages int64
	ErroredMessages   int64
}

// AgentBudgetThresholds defines separate advisory limits for slices and plans.
type AgentBudgetThresholds struct {
	Slice AgentBudgetScopeThresholds
	Plan  AgentBudgetScopeThresholds
}

// DefaultAgentBudgetThresholds returns the built-in telemetry warning limits.
func DefaultAgentBudgetThresholds() AgentBudgetThresholds {
	return AgentBudgetThresholds{
		Slice: AgentBudgetScopeThresholds{OutputTokens: 40000, Cost: 5, ToolCalls: 120, AssistantMessages: 80},
		Plan:  AgentBudgetScopeThresholds{OutputTokens: 150000, Cost: 20, ToolCalls: 400, AssistantMessages: 300},
	}
}

// AgentRole is assigned by trusted operation context, never inferred from text.
type AgentRole string

const (
	AgentRolePlanning    AgentRole = "planning"
	AgentRoleExecution   AgentRole = "execution"
	AgentRoleReview      AgentRole = "review"
	AgentRoleRework      AgentRole = "rework"
	AgentRolePullRequest AgentRole = "pull_request"
	AgentRoleMerge       AgentRole = "merge"
	AgentRoleUnknown     AgentRole = "unknown"
)

// Normalized bounds summary keys without changing the recorded value.
func (r AgentRole) Normalized() AgentRole {
	switch r {
	case AgentRolePlanning, AgentRoleExecution, AgentRoleReview, AgentRoleRework, AgentRolePullRequest, AgentRoleMerge:
		return r
	default:
		return AgentRoleUnknown
	}
}

// AgentMetricsAvailability describes measurement coverage, not session success.
type AgentMetricsAvailability string

const (
	AgentMetricsReported    AgentMetricsAvailability = "reported"
	AgentMetricsPartial     AgentMetricsAvailability = "partial"
	AgentMetricsUnavailable AgentMetricsAvailability = "unavailable"
	AgentMetricsUnknown     AgentMetricsAvailability = "unknown"
)

func (a AgentMetricsAvailability) Normalized() AgentMetricsAvailability {
	switch a {
	case AgentMetricsReported, AgentMetricsPartial, AgentMetricsUnavailable:
		return a
	default:
		return AgentMetricsUnknown
	}
}

// AgentMetricsAvailabilityCounts counts events, including failed attempts.
type AgentMetricsAvailabilityCounts struct {
	Reported    int `json:"reported"`
	Partial     int `json:"partial"`
	Unavailable int `json:"unavailable"`
	Unknown     int `json:"unknown"`
}

// AgentMetrics is the durable metrics payload stored on agent_metrics events.
// Presence flags retain explicit zero measurements; nonzero values remain
// serializable without flags for compatibility with existing emitters.
type AgentMetrics struct {
	Role                    AgentRole                `json:"role,omitempty"`
	Availability            AgentMetricsAvailability `json:"availability,omitempty"`
	InputTokensPresent      bool                     `json:"-"`
	OutputTokensPresent     bool                     `json:"-"`
	ReasoningTokensPresent  bool                     `json:"-"`
	CacheReadTokensPresent  bool                     `json:"-"`
	CacheWriteTokensPresent bool                     `json:"-"`
	CostPresent             bool                     `json:"-"`
	Agent                   string                   `json:"agent,omitempty"`
	SessionID               string                   `json:"session_id"`
	ProviderID              string                   `json:"provider_id,omitempty"`
	ModelID                 string                   `json:"model_id,omitempty"`
	Status                  string                   `json:"status,omitempty"`
	Result                  string                   `json:"result,omitempty"`
	InputTokens             int64                    `json:"input_tokens,omitempty"`
	OutputTokens            int64                    `json:"output_tokens,omitempty"`
	ReasoningTokens         int64                    `json:"reasoning_tokens,omitempty"`
	CacheReadTokens         int64                    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens        int64                    `json:"cache_write_tokens,omitempty"`
	TotalTokens             int64                    `json:"total_tokens,omitempty"`
	TotalTokensPresent      bool                     `json:"-"`
	Cost                    float64                  `json:"cost,omitempty"`
	TotalMessages           int64                    `json:"total_messages,omitempty"`
	UserMessages            int64                    `json:"user_messages,omitempty"`
	AssistantMessages       int64                    `json:"assistant_messages,omitempty"`
	ErroredMessages         int64                    `json:"errored_messages,omitempty"`
	ToolCalls               int64                    `json:"tool_calls,omitempty"`
}

// MarshalJSON preserves explicitly measured zeros without turning legacy Go
// zero values into evidence of measurement.
func (m AgentMetrics) MarshalJSON() ([]byte, error) {
	type plain AgentMetrics
	return json.Marshal(struct {
		plain
		InputTokens      *int64   `json:"input_tokens,omitempty"`
		OutputTokens     *int64   `json:"output_tokens,omitempty"`
		ReasoningTokens  *int64   `json:"reasoning_tokens,omitempty"`
		CacheReadTokens  *int64   `json:"cache_read_tokens,omitempty"`
		CacheWriteTokens *int64   `json:"cache_write_tokens,omitempty"`
		TotalTokens      *int64   `json:"total_tokens,omitempty"`
		Cost             *float64 `json:"cost,omitempty"`
	}{
		plain:            plain(m),
		InputTokens:      recordedMeasurement(m.InputTokens, m.InputTokensPresent),
		OutputTokens:     recordedMeasurement(m.OutputTokens, m.OutputTokensPresent),
		ReasoningTokens:  recordedMeasurement(m.ReasoningTokens, m.ReasoningTokensPresent),
		CacheReadTokens:  recordedMeasurement(m.CacheReadTokens, m.CacheReadTokensPresent),
		CacheWriteTokens: recordedMeasurement(m.CacheWriteTokens, m.CacheWriteTokensPresent),
		TotalTokens:      recordedMeasurement(m.TotalTokens, m.TotalTokensPresent),
		Cost:             recordedMeasurement(m.Cost, m.CostPresent),
	})
}

func recordedMeasurement[T int64 | float64](value T, present bool) *T {
	if value != 0 || present {
		return &value
	}
	return nil
}

// UnmarshalJSON distinguishes measured zero from omitted or null measurements.
func (m *AgentMetrics) UnmarshalJSON(data []byte) error {
	type plain AgentMetrics
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = AgentMetrics(decoded)
	present := func(key string) bool {
		value, exists := fields[key]
		return exists && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
	}
	m.InputTokensPresent = present("input_tokens")
	m.OutputTokensPresent = present("output_tokens")
	m.ReasoningTokensPresent = present("reasoning_tokens")
	m.CacheReadTokensPresent = present("cache_read_tokens")
	m.CacheWriteTokensPresent = present("cache_write_tokens")
	m.TotalTokensPresent = present("total_tokens")
	m.CostPresent = present("cost")
	return nil
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
	ByRole     []AgentMetricsGroup `json:"by_role"`
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
	Availability            AgentMetricsAvailabilityCounts `json:"availability"`
	InputTokensPresent      bool                           `json:"input_tokens_present"`
	OutputTokensPresent     bool                           `json:"output_tokens_present"`
	ReasoningTokensPresent  bool                           `json:"reasoning_tokens_present"`
	CacheReadTokensPresent  bool                           `json:"cache_read_tokens_present"`
	CacheWriteTokensPresent bool                           `json:"cache_write_tokens_present"`
	TotalTokensPresent      bool                           `json:"total_tokens_present"`
	CostPresent             bool                           `json:"cost_present"`
	Sessions                int                            `json:"sessions"`
	Attempts                int                            `json:"attempts"`
	FailedAttempts          int                            `json:"failed_attempts"`
	InputTokens             int64                          `json:"input_tokens"`
	OutputTokens            int64                          `json:"output_tokens"`
	ReasoningTokens         int64                          `json:"reasoning_tokens"`
	CacheReadTokens         int64                          `json:"cache_read_tokens"`
	CacheWriteTokens        int64                          `json:"cache_write_tokens"`
	TotalTokens             int64                          `json:"total_tokens"`
	Cost                    float64                        `json:"cost"`
	TotalMessages           int64                          `json:"total_messages"`
	UserMessages            int64                          `json:"user_messages"`
	AssistantMessages       int64                          `json:"assistant_messages"`
	ErroredMessages         int64                          `json:"errored_messages"`
	ToolCalls               int64                          `json:"tool_calls"`
}

type AgentMetricsGroup struct {
	Key    string             `json:"key"`
	Totals AgentMetricsTotals `json:"totals"`
}

type AgentBudgetWarning struct {
	Scope     string  `json:"scope"`
	SliceID   string  `json:"slice_id,omitempty"`
	Metric    string  `json:"metric"`
	Threshold float64 `json:"threshold"`
	Observed  float64 `json:"observed"`
	Message   string  `json:"message"`
}

// ReworkSummary is compact advisory context derived from durable rework events.
type ReworkSummary struct {
	Rounds                      int
	LatestStoppedReason         string
	DistinctFindingFingerprints int
}

// ReworkChurn is a read-only projection of the review rounds in which finding
// files and file:line anchors appeared. Map values contain distinct rounds in
// ascending order.
type ReworkChurn struct {
	Rounds       []int
	FileRounds   map[string][]int
	AnchorRounds map[string][]int
}

// ReviewRound pairs one durable findings review with the rework round it
// requests.
type ReviewRound struct {
	Round int
	Event Event
}

// ProjectReviewRounds assigns every durable review that requested rework the
// round it requests, so the initial changes-requested review requests round one.
// Durable rework-round events and review ordering supply current round numbers,
// while an encoded rework slice ID takes precedence for legacy review events.
// Only completed changes-requested reviews request a round: the review contract
// permits findings under comment and approve verdicts, but those observations
// never asked for work, so they neither consume a round number nor reach churn
// classification. Reviews carrying no findings are skipped. Complete reports
// whether every requesting review carried a usable finding payload; callers that
// must not act on partial history discard the whole sequence when it is false.
//
// This is the single round assignment for durable review history. Callers that
// need per-round findings must project through it rather than reproducing the
// ordering, so churn detection, review context, and stop evidence cannot drift
// apart.
func ProjectReviewRounds(events []Event) ([]ReviewRound, bool) {
	rounds := make([]ReviewRound, 0, len(events))
	complete := true
	nextRound := 1
	for _, event := range events {
		if event.Type == EventTypeReworkRound && event.Round >= nextRound {
			nextRound = event.Round + 1
		}
		if event.Type != EventTypePlanReviewed || event.Review == nil || event.Review.Status != ReviewStatusCompleted {
			continue
		}
		// A dropped payload is judged before the verdict, because a review that
		// lost its findings may also have lost the verdict that would have made
		// them a rework request.
		if event.Review.FindingsCount > 0 && len(event.Review.Findings) == 0 {
			complete = false
		}
		if event.Review.Verdict != ReviewVerdictChangesRequested || len(event.Review.Findings) == 0 {
			continue
		}

		round := nextRound
		if encoded := ReworkRoundFromSliceID(event.SliceID); encoded > 0 {
			round = encoded
		}
		if round >= nextRound {
			nextRound = round + 1
		} else {
			nextRound++
		}
		rounds = append(rounds, ReviewRound{Round: round, Event: event})
	}
	return rounds, complete
}

// ProjectReworkChurn derives finding history after baseline from the durable
// review rounds ProjectReviewRounds assigns. Incomplete legacy finding payloads
// make the entire projection unavailable rather than allowing partial history to
// drive current behavior.
func ProjectReworkChurn(events []Event, baseline int) ReworkChurn {
	if baseline < 0 {
		baseline = 0
	}
	projection := ReworkChurn{}
	reviewRounds, complete := ProjectReviewRounds(events)
	if !complete {
		return ReworkChurn{}
	}
	for _, reviewRound := range reviewRounds {
		round := reviewRound.Round
		if round <= baseline {
			continue
		}

		projection.Rounds = appendDistinctRound(projection.Rounds, round)
		for _, finding := range reviewRound.Event.Review.Findings {
			file := normalizeReworkFindingPath(finding.File)
			if file == "" {
				continue
			}
			if projection.FileRounds == nil {
				projection.FileRounds = make(map[string][]int)
			}
			projection.FileRounds[file] = appendDistinctRound(projection.FileRounds[file], round)
			if projection.AnchorRounds == nil {
				projection.AnchorRounds = make(map[string][]int)
			}
			anchor := fmt.Sprintf("%s:%d", file, finding.Line)
			projection.AnchorRounds[anchor] = appendDistinctRound(projection.AnchorRounds[anchor], round)
		}
	}
	sort.Ints(projection.Rounds)
	for file := range projection.FileRounds {
		sort.Ints(projection.FileRounds[file])
	}
	for anchor := range projection.AnchorRounds {
		sort.Ints(projection.AnchorRounds[anchor])
	}
	return projection
}

func appendDistinctRound(rounds []int, round int) []int {
	for _, existing := range rounds {
		if existing == round {
			return rounds
		}
	}
	return append(rounds, round)
}

// normalizeReworkFindingPath intentionally matches the canonical path treatment
// used by rework finding comparison.
func normalizeReworkFindingPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "" {
		return ""
	}
	clean := path.Clean(value)
	if clean == "." {
		return ""
	}
	return clean
}

// SummarizeRework derives rework history without affecting plan readability.
func SummarizeRework(events []Event) ReworkSummary {
	summary := ReworkSummary{}
	fingerprints := make(map[string]struct{})
	for _, event := range events {
		switch event.Type {
		case EventTypeReworkRound:
			if event.Round > summary.Rounds {
				summary.Rounds = event.Round
			} else if event.Round == 0 {
				summary.Rounds++
			}
		case EventTypeReworkStopped:
			summary.LatestStoppedReason = firstNonemptyLine(event.Reason, event.Message)
		default:
			continue
		}
		if fingerprint := firstNonemptyLine(event.Fingerprint); fingerprint != "" {
			fingerprints[fingerprint] = struct{}{}
		}
	}
	summary.DistinctFindingFingerprints = len(fingerprints)
	return summary
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
	byRole := make(map[string]*AgentMetricsTotals)
	bySliceSessions := make(map[string]map[string]bool)
	byAgentSessions := make(map[string]map[string]bool)
	byModelSessions := make(map[string]map[string]bool)
	byProviderSessions := make(map[string]map[string]bool)
	byRoleSessions := make(map[string]map[string]bool)

	for _, event := range events {
		addMetrics(&summary.Totals, event.Metrics, seenSessions)
		addGroupMetrics(bySlice, bySliceSessions, event.SliceID, event.Metrics)
		addGroupMetrics(byAgent, byAgentSessions, event.Metrics.Agent, event.Metrics)
		addGroupMetrics(byModel, byModelSessions, event.Metrics.ModelID, event.Metrics)
		addGroupMetrics(byProvider, byProviderSessions, event.Metrics.ProviderID, event.Metrics)
		addGroupMetrics(byRole, byRoleSessions, string(event.Metrics.Role.Normalized()), event.Metrics)
	}

	summary.BySlice = sortedGroups(bySlice)
	summary.ByAgent = sortedGroups(byAgent)
	summary.ByModel = sortedGroups(byModel)
	summary.ByProvider = sortedGroups(byProvider)
	summary.ByRole = sortedGroups(byRole)
	return summary
}

func AgentBudgetWarnings(detail *PlanDetail, thresholds AgentBudgetThresholds) []AgentBudgetWarning {
	return AgentTelemetryBudgetWarnings(SummarizeAgentTelemetry(detail), thresholds)
}

func AgentTelemetryBudgetWarnings(summary AgentTelemetrySummary, thresholds AgentBudgetThresholds) []AgentBudgetWarning {
	warnings := make([]AgentBudgetWarning, 0)
	warnings = appendBudgetWarnings(warnings, "plan", "", summary.Totals, thresholds.Plan)
	for _, group := range summary.BySlice {
		warnings = appendBudgetWarnings(warnings, "slice", group.Key, group.Totals, thresholds.Slice)
	}
	sortBudgetWarnings(warnings)
	return warnings
}

func appendBudgetWarnings(warnings []AgentBudgetWarning, scope string, sliceID string, totals AgentMetricsTotals, thresholds AgentBudgetScopeThresholds) []AgentBudgetWarning {
	warnings = appendBudgetWarning(warnings, scope, sliceID, "output_tokens", float64(thresholds.OutputTokens), float64(totals.OutputTokens))
	warnings = appendBudgetWarning(warnings, scope, sliceID, "cost", thresholds.Cost, totals.Cost)
	warnings = appendBudgetWarning(warnings, scope, sliceID, "tool_calls", float64(thresholds.ToolCalls), float64(totals.ToolCalls))
	warnings = appendBudgetWarning(warnings, scope, sliceID, "assistant_messages", float64(thresholds.AssistantMessages), float64(totals.AssistantMessages))
	warnings = appendBudgetWarning(warnings, scope, sliceID, "errored_messages", float64(thresholds.ErroredMessages), float64(totals.ErroredMessages))
	return warnings
}

func appendBudgetWarning(warnings []AgentBudgetWarning, scope string, sliceID string, metric string, threshold float64, observed float64) []AgentBudgetWarning {
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
	switch metrics.Availability.Normalized() {
	case AgentMetricsReported:
		totals.Availability.Reported++
	case AgentMetricsPartial:
		totals.Availability.Partial++
	case AgentMetricsUnavailable:
		totals.Availability.Unavailable++
	default:
		totals.Availability.Unknown++
	}
	totals.InputTokensPresent = totals.InputTokensPresent || metrics.InputTokensPresent || metrics.InputTokens != 0
	totals.OutputTokensPresent = totals.OutputTokensPresent || metrics.OutputTokensPresent || metrics.OutputTokens != 0
	totals.ReasoningTokensPresent = totals.ReasoningTokensPresent || metrics.ReasoningTokensPresent || metrics.ReasoningTokens != 0
	totals.CacheReadTokensPresent = totals.CacheReadTokensPresent || metrics.CacheReadTokensPresent || metrics.CacheReadTokens != 0
	totals.CacheWriteTokensPresent = totals.CacheWriteTokensPresent || metrics.CacheWriteTokensPresent || metrics.CacheWriteTokens != 0
	totals.TotalTokensPresent = totals.TotalTokensPresent || metrics.TotalTokensPresent || metrics.TotalTokens != 0
	totals.CostPresent = totals.CostPresent || metrics.CostPresent || metrics.Cost != 0
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
