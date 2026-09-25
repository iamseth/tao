package view

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type fakeRepository struct {
	detail *plan.PlanDetail
	err    error
}

func (f fakeRepository) GetPlan(ctx context.Context, id string) (*plan.PlanDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.detail, ctx.Err()
}

func TestShowTelemetrySafeRolesAndReconciliation(t *testing.T) {
	roles := []plan.AgentRole{plan.AgentRoleReview, plan.AgentRoleMerge, plan.AgentRolePlanning,
		plan.AgentRoleExecution, plan.AgentRoleRework, plan.AgentRolePullRequest, "", "untrusted-role\x1b[31m", plan.AgentRoleUnknown}
	events := make([]plan.Event, 0, len(roles))
	for i, role := range roles {
		availability := []plan.AgentMetricsAvailability{plan.AgentMetricsReported, plan.AgentMetricsPartial, plan.AgentMetricsUnavailable, "future-secret"}[i%4]
		events = append(events, plan.Event{Type: plan.EventTypeAgentMetrics, Message: "provider-output-secret", Metrics: &plan.AgentMetrics{
			Role: role, Availability: availability, SessionID: "session-secret", Agent: "agent-secret", ModelID: "model-secret", ProviderID: "provider-secret",
			Result: "failed", InputTokens: 1, OutputTokens: 2, ReasoningTokens: 3, CacheReadTokens: 4, CacheWriteTokens: 5, TotalTokens: 15, Cost: 0.25,
		}})
	}
	detail := &plan.PlanDetail{Events: events}
	got := (Plan{Detail: detail}).ShowPayload().Telemetry
	wantRoles := []plan.AgentRole{plan.AgentRoleExecution, plan.AgentRoleMerge, plan.AgentRolePlanning, plan.AgentRolePullRequest, plan.AgentRoleReview, plan.AgentRoleRework, plan.AgentRoleUnknown}
	var actualRoles []plan.AgentRole
	var sums ShowTelemetryTotals
	var tokens [6]int64
	var cost float64
	for _, group := range got.ByRole {
		actualRoles = append(actualRoles, group.Role)
		totals := group.Totals
		sums.Attempts += totals.Attempts
		sums.FailedAttempts += totals.FailedAttempts
		sums.Sessions += totals.Sessions
		for i, value := range []*int64{totals.InputTokens, totals.OutputTokens, totals.ReasoningTokens, totals.CacheReadTokens, totals.CacheWriteTokens, totals.TotalTokens} {
			tokens[i] += *value
		}
		cost += *totals.Cost
		if a := totals.Availability; a.Reported+a.Partial+a.Unavailable+a.Unknown != totals.Attempts {
			t.Fatalf("availability does not reconcile: %+v", totals)
		}
	}
	if !reflect.DeepEqual(actualRoles, wantRoles) || sums.Attempts != 9 || sums.FailedAttempts != 9 || sums.Sessions != 7 || got.Totals.Sessions != 1 {
		t.Fatalf("roles/counters: %+v; sums %+v", got, sums)
	}
	for i, value := range []*int64{got.Totals.InputTokens, got.Totals.OutputTokens, got.Totals.ReasoningTokens, got.Totals.CacheReadTokens, got.Totals.CacheWriteTokens, got.Totals.TotalTokens} {
		if *value != tokens[i] {
			t.Fatalf("token field %d does not reconcile", i)
		}
	}
	if cost != *got.Totals.Cost || !got.Totals.PartialRecordedTotals || got.Totals.Availability != (plan.AgentMetricsAvailabilityCounts{Reported: 3, Partial: 2, Unavailable: 2, Unknown: 2}) {
		t.Fatalf("totals = %+v", got.Totals)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	if err := RenderShowTelemetry(&text, got); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"untrusted-role", "future-secret", "provider-output-secret", "session-secret", "agent-secret", "model-secret", "provider-secret", "session_id", `"events":`} {
		if strings.Contains(string(encoded), secret) || strings.Contains(text.String(), secret) {
			t.Fatalf("raw telemetry escaped projection: %q", secret)
		}
	}
	slices.Reverse(events)
	reversed := ProjectShowTelemetry(events)
	otherJSON, err := json.Marshal(reversed)
	if err != nil {
		t.Fatal(err)
	}
	var otherText bytes.Buffer
	if err := RenderShowTelemetry(&otherText, reversed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, otherJSON) || text.String() != otherText.String() {
		t.Fatal("projection order depends on event order")
	}
}

func TestShowTelemetryMeasurementPresence(t *testing.T) {
	for _, tc := range []struct {
		name, metrics, wantJSON, wantText string
		partial                           bool
	}{
		{"zero", `{"role":"execution","availability":"reported","total_tokens":0,"cost":0}`, `"total_tokens":0,"cost":0`, "Tokens: 0;", false},
		{"partial", `{"role":"review","availability":"partial","output_tokens":12}`, `"total_tokens":null,"cost":null`, "Tokens: unavailable/unknown;", true},
		{"unavailable", `{"availability":"unavailable"}`, `"total_tokens":null,"cost":null`, "cost unavailable/unknown", true},
		{"legacy", `{"total_tokens":23}`, `"total_tokens":23,"cost":null`, "unknown/legacy 1", true},
		{"future", `{"availability":"future"}`, `"total_tokens":null,"cost":null`, "unknown/legacy 1", true},
		{"no events", "", `"total_tokens":null,"cost":null`, "Total: unavailable/unknown; sessions 0; attempts 0; failed 0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []plan.Event
			if tc.metrics != "" {
				var metrics plan.AgentMetrics
				if err := json.Unmarshal([]byte(tc.metrics), &metrics); err != nil {
					t.Fatal(err)
				}
				events = append(events, plan.Event{Type: plan.EventTypeAgentMetrics, Metrics: &metrics})
			}
			got := ProjectShowTelemetry(events)
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var text bytes.Buffer
			if err := RenderShowTelemetry(&text, got); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), tc.wantJSON) || !strings.Contains(text.String(), tc.wantText) || got.Totals.PartialRecordedTotals != tc.partial {
				t.Fatalf("JSON %s\ntext %s", encoded, text.String())
			}
			if tc.name == "zero" && !strings.Contains(text.String(), "cost $0.0000") {
				t.Fatal("measured zero cost lost")
			}
			if tc.name == "no events" && !strings.Contains(string(encoded), `"by_role":[]`) {
				t.Fatal("empty role list must be explicit")
			}
		})
	}
}

func TestLoadPlanDerivesState(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusPlanned, Plan: plan.PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPlanned}}},
	}

	loaded, err := LoadPlan(context.Background(), fakeRepository{detail: detail}, "plan", Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Detail != detail || loaded.Now != now {
		t.Fatalf("unexpected loaded plan: %+v", loaded)
	}
	if !loaded.Derived.Runnable || loaded.Derived.NextSliceID != "001-a" {
		t.Fatalf("unexpected derived state: %+v", loaded.Derived)
	}
}

func TestShowPayloadCarriesLoadedRecommendation(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusPlanned,
			Repo:   plan.Repo{Name: "repo", Branch: "main"},
			Plan:   plan.PlanState{ID: "plan", Title: "Plan", PendingSlices: []string{"001-a"}},
		},
		Slices:   plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPending}}},
		Warnings: []string{"warning"},
	}
	loaded, err := LoadPlan(context.Background(), fakeRepository{detail: detail}, "plan", Options{})
	if err != nil {
		t.Fatal(err)
	}
	payload := loaded.ShowPayload()
	if payload.Schema != "tao.show.v1" || payload.ID != "plan" || payload.Status != plan.StatusPlanned {
		t.Fatalf("unexpected show payload: %+v", payload)
	}
	if !reflect.DeepEqual(payload.NextAction, loaded.Derived.NextAction) {
		t.Fatalf("payload recommendation = %+v, loaded recommendation = %+v", payload.NextAction, loaded.Derived.NextAction)
	}
	if payload.Progress.Pending != 1 || payload.Progress.NextSliceID != "001-a" {
		t.Fatalf("unexpected progress projection: %+v", payload.Progress)
	}
	payload.Warnings[0] = "changed"
	if detail.Warnings[0] != "warning" {
		t.Fatal("show payload aliases raw plan warnings")
	}
}

func TestShowPayloadProjectsBoundedAbandonmentWithoutAliasingRawReason(t *testing.T) {
	at := time.Date(2026, 9, 1, 17, 0, 0, 0, time.FixedZone("offset", 3600))
	raw := "superseded\nby\ta safer path\x1b[31m " + strings.Repeat("界", 120)
	detail := &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusAbandoned, Plan: plan.PlanState{ID: "plan", Title: "Plan"}},
		Events: []plan.Event{{Type: plan.EventTypePlanAbandoned, Timestamp: at, Reason: raw}},
	}
	loaded, err := LoadPlan(context.Background(), fakeRepository{detail: detail}, "plan", Options{})
	if err != nil {
		t.Fatal(err)
	}
	payload := loaded.ShowPayload()
	if payload.Abandonment == nil || payload.Abandonment.AbandonedAt == nil || payload.Abandonment.AbandonedAt.Location() != time.UTC {
		t.Fatalf("abandonment payload = %+v", payload.Abandonment)
	}
	if got := payload.Abandonment.Reason; strings.ContainsAny(got, "\n\t\x1b") || len([]rune(got)) > abandonmentReasonExcerptRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("unsafe or unbounded reason = %q", got)
	}
	if payload.NextAction.Primary.Reason != "the plan was abandoned" || strings.Contains(payload.NextAction.Primary.Reason, raw) {
		t.Fatalf("unsafe next action = %+v", payload.NextAction.Primary)
	}
	payload.Abandonment.Reason = "changed"
	if detail.Events[0].Reason != raw {
		t.Fatal("show abandonment aliases raw event evidence")
	}
}

func TestFormatAbandonmentTextHandlesMissingAndMalformedReasons(t *testing.T) {
	if got := FormatAbandonmentText(" \n\t "); got != abandonmentReasonFallback {
		t.Fatalf("missing reason = %q", got)
	}
	got := FormatAbandonmentText(" stop\nnow\x00 " + strings.Repeat("界", 120))
	if strings.ContainsAny(got, "\n\x00") || len([]rune(got)) != abandonmentReasonExcerptRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("malformed reason = %q", got)
	}
}

func TestRenderShowReworkPopulated(t *testing.T) {
	events := []plan.Event{
		{Type: plan.EventTypePlanReviewed, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Findings: []plan.ReviewFinding{{File: "initial.go", Line: 1}}, FindingsCount: 1}},
		{Type: plan.EventTypeReworkRound, Round: 1},
		{Type: plan.EventTypePlanReviewed, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Findings: []plan.ReviewFinding{{File: "b.go", Line: 2}, {File: "a.go", Line: 3}}, FindingsCount: 2}},
		{Type: plan.EventTypeReworkRound, Round: 2},
		{Type: plan.EventTypePlanReviewed, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Findings: []plan.ReviewFinding{{File: "b.go", Line: 4}, {File: "a.go", Line: 5}}, FindingsCount: 2}},
		{Type: plan.EventTypeReworkRound, Round: 3},
		{Type: plan.EventTypePlanReviewed, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Findings: []plan.ReviewFinding{{File: "b.go", Line: 6}}, FindingsCount: 1}},
		{Type: plan.EventTypeReworkStopped, Reason: "automatic rework stalled on equivalent consecutive findings"},
	}
	projection := ProjectShowRework(events)
	if projection.Rounds != 3 || projection.CurrentStopClassification != "findings_stalled" || !reflect.DeepEqual(projection.RecurringFiles, []string{"b.go", "a.go"}) {
		t.Fatalf("rework projection = %+v", projection)
	}

	var out bytes.Buffer
	if err := RenderShowRework(&out, projection); err != nil {
		t.Fatal(err)
	}
	want := "\nRework:\nRounds: 3\nCurrent stop: findings_stalled\nRecurring files:\n- b.go\n- a.go\n"
	if got := out.String(); got != want {
		t.Fatalf("rework output = %q, want %q", got, want)
	}
}

func TestRenderShowReworkEmpty(t *testing.T) {
	projection := ProjectShowRework(nil)
	var out bytes.Buffer
	if err := RenderShowRework(&out, projection); err != nil {
		t.Fatal(err)
	}
	want := "\nRework:\nRounds: 0\nCurrent stop: -\nRecurring files: -\n"
	if got := out.String(); got != want {
		t.Fatalf("rework output = %q, want %q", got, want)
	}
}

func TestRenderVerificationFindings(t *testing.T) {
	var out bytes.Buffer
	err := RenderVerificationFindings(&out, []plan.VerificationFinding{{
		Severity:   "error",
		SliceID:    "001-a",
		Message:    "missing command",
		Path:       "slices.json",
		Suggestion: "add verification",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "Verification Findings:\n- error 001-a: missing command (slices.json) suggestion: add verification\n"
	if got := out.String(); got != want {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRenderVerificationFindingsSkipsEmptyList(t *testing.T) {
	var out strings.Builder
	if err := RenderVerificationFindings(&out, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" {
		t.Fatalf("expected no output, got %q", out.String())
	}
}
