package plan

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAgentMetricsMeasurementRoundTrip(t *testing.T) {
	fields := []string{"input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_write_tokens", "total_tokens", "cost"}
	presence := func(m AgentMetrics) []bool {
		return []bool{m.InputTokensPresent, m.OutputTokensPresent, m.ReasoningTokensPresent, m.CacheReadTokensPresent, m.CacheWriteTokensPresent, m.TotalTokensPresent, m.CostPresent}
	}
	tests := []struct {
		name    string
		input   string
		present map[string]bool
	}{
		{name: "legacy omitted", input: `{"session_id":"legacy"}`},
		{name: "null is absent", input: `{"total_tokens":null,"cost":null}`},
		{name: "reported does not invent presence", input: `{"availability":"reported"}`},
		{name: "reported zeros", input: `{"availability":"reported","input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"total_tokens":0,"cost":0}`, present: map[string]bool{"input_tokens": true, "output_tokens": true, "reasoning_tokens": true, "cache_read_tokens": true, "cache_write_tokens": true, "total_tokens": true, "cost": true}},
		{name: "partial", input: `{"availability":"partial","output_tokens":12,"cost":0}`, present: map[string]bool{"output_tokens": true, "cost": true}},
		{name: "unavailable", input: `{"availability":"unavailable","status":"completed"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m AgentMetrics
			if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
				t.Fatal(err)
			}
			for round := 0; round < 2; round++ {
				for i, got := range presence(m) {
					if got != tt.present[fields[i]] {
						t.Fatalf("%s presence = %v", fields[i], got)
					}
				}
				encoded, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &raw); err != nil {
					t.Fatal(err)
				}
				for _, field := range fields {
					if _, ok := raw[field]; ok != tt.present[field] {
						t.Fatalf("%s presence lost in %s", field, encoded)
					}
				}
				var decoded AgentMetrics
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(m, decoded) {
					t.Fatalf("round trip changed metrics: %+v -> %+v", m, decoded)
				}
				m = decoded
			}
			totals := SummarizeAgentMetrics([]AgentMetricEvent{{Metrics: m}}).Totals
			gotPresence := []bool{totals.InputTokensPresent, totals.OutputTokensPresent, totals.ReasoningTokensPresent, totals.CacheReadTokensPresent, totals.CacheWriteTokensPresent, totals.TotalTokensPresent, totals.CostPresent}
			if !reflect.DeepEqual(gotPresence, presence(m)) {
				t.Fatalf("summary lost presence: %+v", totals)
			}
		})
	}
}

func TestAgentMetricsGoZeroPresence(t *testing.T) {
	for _, present := range []bool{false, true} {
		m := AgentMetrics{InputTokensPresent: present, OutputTokensPresent: present, ReasoningTokensPresent: present, CacheReadTokensPresent: present, CacheWriteTokensPresent: present, TotalTokensPresent: present, CostPresent: present}
		encoded, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var decoded AgentMetrics
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m, decoded) {
			t.Fatalf("Go zero presence changed: %+v -> %s", m, encoded)
		}
	}
	// Existing emitters do not need to set presence flags for nonzero values.
	m := AgentMetrics{InputTokens: 1, OutputTokens: 2, ReasoningTokens: 3, CacheReadTokens: 4, CacheWriteTokens: 5, TotalTokens: 15, Cost: 0.25}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AgentMetrics
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.InputTokensPresent || !decoded.OutputTokensPresent || !decoded.ReasoningTokensPresent || !decoded.CacheReadTokensPresent || !decoded.CacheWriteTokensPresent || !decoded.TotalTokensPresent || !decoded.CostPresent {
		t.Fatalf("nonzero measurements lost: %s", encoded)
	}
	before := SummarizeAgentMetrics([]AgentMetricEvent{{Metrics: m}}).Totals
	after := SummarizeAgentMetrics([]AgentMetricEvent{{Metrics: decoded}}).Totals
	if before != after {
		t.Fatalf("round trip changed totals: %+v -> %+v", before, after)
	}
}

func TestAgentMetricsRoleAndAvailabilityCompatibility(t *testing.T) {
	for _, role := range []AgentRole{AgentRolePlanning, AgentRoleExecution, AgentRoleReview, AgentRoleRework, AgentRolePullRequest, AgentRoleMerge, AgentRoleUnknown, "", "future-role", " REVIEW "} {
		for _, availability := range []AgentMetricsAvailability{AgentMetricsReported, AgentMetricsPartial, AgentMetricsUnavailable, "", "future-availability"} {
			m := AgentMetrics{Role: role, Availability: availability}
			encoded, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var decoded AgentMetrics
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded != m {
				t.Fatalf("raw metadata changed: %+v -> %+v", m, decoded)
			}
			wantRole := role
			if role == "" || role == "future-role" || role == " REVIEW " {
				wantRole = AgentRoleUnknown
			}
			summary := SummarizeAgentMetrics([]AgentMetricEvent{{Metrics: decoded}})
			if len(summary.ByRole) != 1 || summary.ByRole[0].Key != string(wantRole) {
				t.Fatalf("unexpected role grouping: %+v", summary.ByRole)
			}
			want := AgentMetricsAvailabilityCounts{}
			switch availability {
			case AgentMetricsReported:
				want.Reported = 1
			case AgentMetricsPartial:
				want.Partial = 1
			case AgentMetricsUnavailable:
				want.Unavailable = 1
			default:
				want.Unknown = 1
			}
			if summary.Totals.Availability != want || summary.ByRole[0].Totals.Availability != want {
				t.Fatalf("unexpected availability: %+v", summary)
			}
			if summary.Events[0].Metrics != m {
				t.Fatal("summary rewrote raw metrics")
			}
		}
	}
}

func TestSummarizeAgentMetricsRolesReconcile(t *testing.T) {
	roles := []AgentRole{AgentRoleReview, AgentRoleExecution, AgentRolePlanning, AgentRoleRework, AgentRolePullRequest, AgentRoleMerge, "future-role", "", AgentRoleExecution}
	availability := []AgentMetricsAvailability{AgentMetricsReported, AgentMetricsPartial, AgentMetricsUnavailable, "future-value", ""}
	var events []AgentMetricEvent
	for i, role := range roles {
		m := AgentMetrics{Role: role, Availability: availability[i%len(availability)], Agent: "pi", SessionID: "shared", ProviderID: "provider", ModelID: "model", Status: StatusCompleted, InputTokens: 1, OutputTokens: 2, ReasoningTokens: 3, CacheReadTokens: 4, CacheWriteTokens: 5, TotalTokens: 15, Cost: 0.25, TotalMessages: 6, UserMessages: 1, AssistantMessages: 5, ErroredMessages: 1, ToolCalls: 7}
		if i%2 == 0 {
			m.Status = StatusBlocked
		}
		events = append(events, AgentMetricEvent{SliceID: "001-a", Metrics: m})
	}
	summary := SummarizeAgentMetrics(events)
	var keys []string
	var sum AgentMetricsTotals
	for _, group := range summary.ByRole {
		keys = append(keys, group.Key)
		// Every numeric total except distinct sessions is additive across roles.
		dst, src := reflect.ValueOf(&sum).Elem(), reflect.ValueOf(group.Totals)
		for i := 0; i < dst.NumField(); i++ {
			switch dst.Field(i).Kind() {
			case reflect.Int, reflect.Int64:
				dst.Field(i).SetInt(dst.Field(i).Int() + src.Field(i).Int())
			case reflect.Float64:
				dst.Field(i).SetFloat(dst.Field(i).Float() + src.Field(i).Float())
			case reflect.Bool:
				dst.Field(i).SetBool(dst.Field(i).Bool() || src.Field(i).Bool())
			}
		}
		sum.Availability.Reported += group.Totals.Availability.Reported
		sum.Availability.Partial += group.Totals.Availability.Partial
		sum.Availability.Unavailable += group.Totals.Availability.Unavailable
		sum.Availability.Unknown += group.Totals.Availability.Unknown
	}
	if !reflect.DeepEqual(keys, []string{"execution", "merge", "planning", "pull_request", "review", "rework", "unknown"}) {
		t.Fatalf("role order = %v", keys)
	}
	if sum.Sessions != 7 || summary.Totals.Sessions != 1 {
		t.Fatalf("distinct sessions should not add across roles: %d vs %d", sum.Sessions, summary.Totals.Sessions)
	}
	sum.Sessions = summary.Totals.Sessions
	if sum != summary.Totals {
		t.Fatalf("role totals do not reconcile: %+v vs %+v", sum, summary.Totals)
	}
	if summary.Totals.Attempts != 9 || summary.Totals.FailedAttempts != 5 || summary.Totals.Availability != (AgentMetricsAvailabilityCounts{Reported: 2, Partial: 2, Unavailable: 2, Unknown: 3}) {
		t.Fatalf("unexpected attempt totals: %+v", summary.Totals)
	}
	for _, groups := range [][]AgentMetricsGroup{summary.BySlice, summary.ByAgent, summary.ByModel, summary.ByProvider} {
		if len(groups) != 1 || groups[0].Totals != summary.Totals {
			t.Fatalf("existing dimension changed: %+v", groups)
		}
	}
	if !reflect.DeepEqual(events, summary.Events) {
		t.Fatal("summary changed raw events")
	}
	// Input order cannot affect role order or deduplication.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if got := SummarizeAgentMetrics(events); !reflect.DeepEqual(got.ByRole, summary.ByRole) {
		t.Fatalf("unstable role summary: %+v", got.ByRole)
	}
}

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

func TestProjectReworkChurn(t *testing.T) {
	review := func(sliceID string, findings ...ReviewFinding) Event {
		return Event{
			Type:    EventTypePlanReviewed,
			SliceID: sliceID,
			Review: &PlanReview{
				Status:        ReviewStatusCompleted,
				Verdict:       ReviewVerdictChangesRequested,
				FindingsCount: len(findings),
				Findings:      findings,
			},
		}
	}
	finding := func(file string, line int) ReviewFinding {
		return ReviewFinding{Severity: "important", File: file, Line: line, Message: "case-study finding"}
	}
	completedRounds := func(findings ...ReviewFinding) []Event {
		events := make([]Event, 0, len(findings)*3)
		for index, item := range findings {
			requestedRound := index + 1
			events = append(events, review("", item))
			if requestedRound < len(findings) {
				events = append(events,
					Event{Type: EventTypePlanReopened},
					Event{Type: EventTypeReworkRound, Round: requestedRound},
				)
			}
		}
		return events
	}

	tests := []struct {
		name       string
		baseline   int
		stopBefore int
		events     []Event
		want       ReworkChurn
	}{
		{
			name:       "workflow recovery stops before round seven opens",
			stopBefore: 7,
			events: completedRounds(
				finding("internal/run/run.go", 81),
				finding("internal/workspace/resolve.go", 144),
				finding("internal/run/run.go", 82),
				finding("internal/workspace/resolve.go", 145),
				finding("internal/run/run.go", 83),
				finding("internal/run/run.go", 84),
				finding("internal/workspace/resolve.go", 146),
			),
			want: ReworkChurn{
				Rounds: []int{1, 2, 3, 4, 5, 6, 7},
				FileRounds: map[string][]int{
					"internal/run/run.go":           {1, 3, 5, 6},
					"internal/workspace/resolve.go": {2, 4, 7},
				},
				AnchorRounds: map[string][]int{
					"internal/run/run.go:81":            {1},
					"internal/run/run.go:82":            {3},
					"internal/run/run.go:83":            {5},
					"internal/run/run.go:84":            {6},
					"internal/workspace/resolve.go:144": {2},
					"internal/workspace/resolve.go:145": {4},
					"internal/workspace/resolve.go:146": {7},
				},
			},
		},
		{
			name:       "verification repair stops before round four opens",
			stopBefore: 4,
			events: completedRounds(
				finding("internal/plan/derive.go", 180),
				finding("internal/plan/derive.go", 181),
				finding("internal/plan/derive.go", 205),
				finding("internal/run/run.go", 42),
			),
			want: ReworkChurn{
				Rounds: []int{1, 2, 3, 4},
				FileRounds: map[string][]int{
					"internal/plan/derive.go": {1, 2, 3},
					"internal/run/run.go":     {4},
				},
				AnchorRounds: map[string][]int{
					"internal/plan/derive.go:180": {1},
					"internal/plan/derive.go:181": {2},
					"internal/plan/derive.go:205": {3},
					"internal/run/run.go:42":      {4},
				},
			},
		},
		{
			name:       "stale merge intent stops before round three opens",
			stopBefore: 3,
			events: completedRounds(
				finding("internal/merge/intent.go", 55),
				finding("internal\\merge\\intent.go", 72),
				finding("internal/merge/tmp/../intent.go/", 55),
			),
			want: ReworkChurn{
				Rounds:     []int{1, 2, 3},
				FileRounds: map[string][]int{"internal/merge/intent.go": {1, 2, 3}},
				AnchorRounds: map[string][]int{
					"internal/merge/intent.go:55": {1, 3},
					"internal/merge/intent.go:72": {2},
				},
			},
		},
		{
			name:   "unrelated files remain distinct",
			events: completedRounds(finding("first.go", 10), finding("second.go", 20)),
			want: ReworkChurn{
				Rounds:       []int{1, 2},
				FileRounds:   map[string][]int{"first.go": {1}, "second.go": {2}},
				AnchorRounds: map[string][]int{"first.go:10": {1}, "second.go:20": {2}},
			},
		},
		{
			name:     "baseline excludes earlier rounds",
			baseline: 1,
			events:   completedRounds(finding("initial.go", 1), finding("shared.go", 10), finding("shared.go", 10)),
			want: ReworkChurn{
				Rounds:       []int{2, 3},
				FileRounds:   map[string][]int{"shared.go": {2, 3}},
				AnchorRounds: map[string][]int{"shared.go:10": {2, 3}},
			},
		},
		{
			name:     "encoded slice round reconciles ordering mismatch",
			baseline: 1,
			events: []Event{
				review("", finding("initial.go", 1)),
				review("r301-fix", finding("mismatch.go", 30)),
				review("", finding("after.go", 40)),
			},
			want: ReworkChurn{
				Rounds:       []int{3, 4},
				FileRounds:   map[string][]int{"mismatch.go": {3}, "after.go": {4}},
				AnchorRounds: map[string][]int{"mismatch.go:30": {3}, "after.go:40": {4}},
			},
		},
		{
			name: "rework event reconciles missing encoded slice",
			events: []Event{
				review("", finding("initial.go", 1)),
				{Type: EventTypeReworkRound, Round: 3},
				review("", finding("after.go", 40)),
			},
			want: ReworkChurn{
				Rounds:       []int{1, 4},
				FileRounds:   map[string][]int{"initial.go": {1}, "after.go": {4}},
				AnchorRounds: map[string][]int{"initial.go:1": {1}, "after.go:40": {4}},
			},
		},
		{name: "events absent"},
		{
			name: "legacy findings count without payload degrades empty",
			events: []Event{
				review("", finding("initial.go", 1)),
				review("", finding("visible.go", 2)),
				{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusCompleted, FindingsCount: 1}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectReworkChurn(tt.events, tt.baseline); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ProjectReworkChurn() = %#v, want %#v", got, tt.want)
			}
			for _, event := range tt.events {
				if tt.stopBefore > 0 && event.Type == EventTypeReworkRound && event.Round >= tt.stopBefore {
					t.Fatalf("fixture opened rework round %d before the round-%d stop decision", event.Round, tt.stopBefore)
				}
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
		{Type: "", PlanID: "plan", Metrics: &AgentMetrics{SessionID: "empty"}},
		{Type: "slice_completed", PlanID: "plan"},
		{Type: "legacy_metrics", PlanID: "plan", SliceID: "unknown", Metrics: &AgentMetrics{Agent: "pi", SessionID: "unknown"}},
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

func TestProjectReviewRoundsAssignsRequestedRoundsFromDurableOrdering(t *testing.T) {
	findings := func(file string) *PlanReview {
		return &PlanReview{
			Status:        ReviewStatusCompleted,
			Verdict:       ReviewVerdictChangesRequested,
			FindingsCount: 1,
			Findings:      []ReviewFinding{{File: file, Line: 12, Message: file}},
		}
	}
	events := []Event{
		{Type: EventTypePlanReviewed, Review: findings("first.go")},
		{Type: EventTypeReworkRound, Round: 1},
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested}},
		{Type: EventTypePlanReviewed, Review: findings("second.go")},
		{Type: EventTypeReworkRound, Round: 2},
		{Type: EventTypePlanReviewed, SliceID: "r701-encoded", Review: findings("third.go")},
		{Type: EventTypePlanReviewed, Review: findings("fourth.go")},
	}

	rounds, complete := ProjectReviewRounds(events)
	if !complete {
		t.Fatal("durable findings payloads reported incomplete")
	}
	want := []int{1, 2, 7, 8}
	got := make([]int, 0, len(rounds))
	for _, round := range rounds {
		got = append(got, round.Round)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assigned rounds = %#v, want %#v", got, want)
	}
	if file := rounds[0].Event.Review.Findings[0].File; file != "first.go" {
		t.Fatalf("round one review = %q, want the initial findings review", file)
	}
}

func TestProjectReviewRoundsReportsIncompleteLegacyFindingPayloads(t *testing.T) {
	events := []Event{
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested, FindingsCount: 2}},
	}
	rounds, complete := ProjectReviewRounds(events)
	if complete {
		t.Fatal("counted findings without a payload reported complete")
	}
	if len(rounds) != 0 {
		t.Fatalf("assigned rounds = %#v, want none", rounds)
	}
}

func TestProjectReviewRoundsIgnoresNonBlockingReviewFindings(t *testing.T) {
	blocking := func(file string) *PlanReview {
		return &PlanReview{
			Status:        ReviewStatusCompleted,
			Verdict:       ReviewVerdictChangesRequested,
			FindingsCount: 1,
			Findings:      []ReviewFinding{{File: file, Line: 47, Message: file}},
		}
	}
	nonBlocking := func(verdict, file string) *PlanReview {
		return &PlanReview{
			Status:        ReviewStatusCompleted,
			Verdict:       verdict,
			FindingsCount: 1,
			Findings:      []ReviewFinding{{File: file, Line: 47, Message: file}},
		}
	}
	events := []Event{
		{Type: EventTypePlanReviewed, Review: blocking("recurring.go")},
		{Type: EventTypeReworkRound, Round: 1},
		{Type: EventTypePlanReviewed, Review: nonBlocking(ReviewVerdictComment, "recurring.go")},
		{Type: EventTypePlanReviewed, Review: nonBlocking(ReviewVerdictApprove, "recurring.go")},
		{Type: EventTypePlanReviewed, Review: blocking("recurring.go")},
	}

	rounds, complete := ProjectReviewRounds(events)
	if !complete {
		t.Fatal("non-blocking findings reported an incomplete payload")
	}
	want := []int{1, 2}
	got := make([]int, 0, len(rounds))
	for _, round := range rounds {
		got = append(got, round.Round)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assigned rounds = %#v, want %#v; comment and approve reviews must not consume rounds", got, want)
	}

	churn := ProjectReworkChurn(events, 0)
	if got, want := churn.FileRounds["recurring.go"], []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("file rounds = %#v, want %#v; non-blocking findings must not reach churn", got, want)
	}
	if got, want := churn.AnchorRounds["recurring.go:47"], []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("anchor rounds = %#v, want %#v; non-blocking findings must not reach churn", got, want)
	}
}

func TestProjectReworkChurnCountsFirstReviewAfterPullRequestReopen(t *testing.T) {
	blocking := func(file string, line int) *PlanReview {
		return &PlanReview{
			Status:        ReviewStatusCompleted,
			Verdict:       ReviewVerdictChangesRequested,
			FindingsCount: 1,
			Findings:      []ReviewFinding{{File: file, Line: line, Message: file}},
		}
	}
	// A pull-request reopen creates round 2 and now records it durably. That
	// round becomes the fresh baseline, so the Tao reviews that follow must be
	// assigned rounds 3, 4 and 5 rather than reusing the baseline round.
	events := []Event{
		{Type: EventTypePlanReviewed, Review: blocking("pre-pr.go", 1)},
		{Type: EventTypePlanReopened},
		{Type: EventTypeReworkRound, Round: 2},
		{Type: EventTypePlanReviewed, Review: blocking("recurring.go", 47)},
		{Type: EventTypeReworkRound, Round: 3},
		{Type: EventTypePlanReviewed, Review: blocking("recurring.go", 47)},
		{Type: EventTypeReworkRound, Round: 4},
		{Type: EventTypePlanReviewed, Review: blocking("recurring.go", 47)},
	}

	rounds, complete := ProjectReviewRounds(events)
	if !complete {
		t.Fatal("durable findings payloads reported incomplete")
	}
	want := []int{1, 3, 4, 5}
	got := make([]int, 0, len(rounds))
	for _, round := range rounds {
		got = append(got, round.Round)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assigned rounds = %#v, want %#v; the first post-reopen review must follow the recorded round", got, want)
	}

	// Under the fresh pull-request baseline every post-baseline review counts,
	// so three recurrences of one file reach the recurrence threshold here
	// rather than one review later.
	churn := ProjectReworkChurn(events, 2)
	if got, want := churn.FileRounds["recurring.go"], []int{3, 4, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("post-baseline file rounds = %#v, want %#v", got, want)
	}
	if got, want := churn.AnchorRounds["recurring.go:47"], []int{3, 4, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("post-baseline anchor rounds = %#v, want %#v", got, want)
	}
	if _, carried := churn.FileRounds["pre-pr.go"]; carried {
		t.Fatal("pre-reopen findings leaked past the fresh pull-request baseline")
	}
}
