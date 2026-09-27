package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func scorecardFixtures() []plan.PlanSummary {
	recent := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	old := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	return []plan.PlanSummary{
		{ID: "20260901-120000-pi", Dir: filepath.Join("testdata", "plan-scorecard-pi-complete"), Status: plan.StatusCompleted, Complete: true, Reviewed: true, ReviewVerdict: "approve", OriginalTotalCount: 2, ReworkTotalCount: 1},
		{ID: "20260920-120000-claude", Dir: filepath.Join("testdata", "plan-scorecard-claude-active"), Status: plan.StatusInProgress, LastActivityAt: &recent},
		{ID: "20260801-120000-legacy", Dir: filepath.Join("testdata", "plan-scorecard-legacy-label"), Status: plan.StatusPlanned, LastActivityAt: &old},
		{ID: "not-a-date", Dir: filepath.Join("testdata", "plan-scorecard-missing-created"), Status: plan.StatusPlanned},
	}
}

func TestScorecardFixtureCoverage(t *testing.T) {
	now := time.Date(2026, 9, 26, 23, 59, 0, 0, time.UTC)
	summaries := scorecardFixtures()
	report, err := AggregateWithOptions(context.Background(), fixtureLister{summaries: summaries}, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	want := ScorecardCoverage{Plans: 4, NeverStarted: 2, Active: 1, Terminal: 1, Matured: 2, Censored: 2, TreatmentHigh: 2, TreatmentLow: 1, TreatmentMissing: 1, PlanningMetricsPlans: 1, RoleAttributedSessions: 2, UnattributedSessions: 2, MaturityWindowDays: 14, MinimumSamples: 5}
	if report.Scorecard.Coverage != want {
		t.Fatalf("coverage = %+v, want %+v", report.Scorecard.Coverage, want)
	}
	multi, err := AggregateSourcesWithOptions(context.Background(), fixtureSourceLister{sources: []RepositorySource{{ID: "repo", Plans: fixtureLister{summaries: summaries}}}}, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if multi.Scorecard.Coverage != want {
		t.Fatalf("source coverage = %+v", multi.Scorecard.Coverage)
	}
	routes := fixtureRouteLister{records: routingFixture(t)}
	withRoutes, err := AggregateWithOptions(context.Background(), fixtureLister{summaries: summaries}, Options{Now: now, Routes: routes})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withRoutes.Scorecard, report.Scorecard) || withRoutes.PlannerRouting.Records != len(routes.records) {
		t.Fatalf("routing evidence must coexist with, but not change, the scorecard: %+v", withRoutes)
	}
	multiWithRoutes, err := AggregateSourcesWithOptions(context.Background(), fixtureSourceLister{sources: []RepositorySource{{ID: "repo", Plans: fixtureLister{summaries: summaries}, Routes: routes}}}, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(multiWithRoutes.Scorecard, multi.Scorecard) || !reflect.DeepEqual(multiWithRoutes.PlannerRouting, withRoutes.PlannerRouting) {
		t.Fatalf("source aggregation lost scorecard or routing evidence: %+v", multiWithRoutes)
	}
	classes := []observationClass{observationTerminal, observationActive, observationNeverStarted, observationNeverStarted}
	eras := []string{"2026-09", "2026-09", "2026-08", "unknown"}
	for i, summary := range summaries {
		data, err := readPlanEvents(context.Background(), summary.Dir)
		if err != nil {
			t.Fatal(err)
		}
		got := observePlan(sourceIdentity{id: "repo"}, summary, data, now)
		if got.class != classes[i] || got.era != eras[i] || got.repositoryKey != "repo" {
			t.Fatalf("observation %s = %+v", summary.ID, got)
		}
		if i == 3 && got.treatment.Source != "none" {
			t.Fatalf("missing creation treatment = %+v", got.treatment)
		}
		if i == 0 {
			if !got.merged || !got.exactApproval || !got.hasApproval || got.hoursToApproval != 48 || got.reviewRounds != 1 || got.firstReviewVerdict != "changes_requested" || got.severities["high"] != 1 || got.reworkSlices != 1 {
				t.Fatalf("quality = %+v", got)
			}
			if got.roles["execution"].sessions != 1 || got.roles["execution"].outputTokens != 30 || got.roles["execution"].totalTokens != 70 || got.roles["execution"].toolCalls != 3 {
				t.Fatalf("roles = %+v", got.roles)
			}
		}
	}
}

func TestScorecardQualityReliabilityAndBounds(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	data := planData{sessions: make(map[string][]plan.AgentMetricEvent)}
	events := []plan.Event{
		{Type: "slice_started", Timestamp: now.Add(-3 * time.Hour)},
		{Type: "plan_reviewed", Timestamp: now.Add(-time.Hour), Review: &plan.PlanReview{Status: "completed", Verdict: "approve", Base: "base", Head: "head"}},
		{Type: "plan_merged"},
		{Type: "plan_reopened"},
		{Type: "verification_repair_created"}, {Type: "rework_stopped"},
		{Type: "slice_blocked", Reason: "network unavailable"},
		{Type: "session_timeout"}, {Type: "slice_resume_failed"}, {Type: "verification_command_invalid"},
		{Type: "budget_exceeded"},
		{Type: "finalization_failed", FinalizationFailure: &plan.FinalizationFailure{Category: "head_drift"}},
		{Type: "agent_metrics", Metrics: &plan.AgentMetrics{Role: "unrecognized", Status: "failed", OutputTokens: -1, Cost: -1}},
		{Type: "agent_metrics", Metrics: &plan.AgentMetrics{Role: plan.AgentRolePlanning, Agent: "pi", ProviderID: "provider"}},
	}
	for _, kind := range []plan.FinalVerificationFailureKind{"code", "tool_missing", "timeout", "cancelled", "invalid_command"} {
		events = append(events, plan.Event{Type: "final_verification", Result: "failed", FailureKind: kind}, plan.Event{Type: "final_verification", Result: "passed", FailureKind: kind})
	}
	findings := []plan.ReviewFinding{{Severity: "HIGH"}, {Severity: "high"}}
	for i := 0; i < 30; i++ {
		findings = append(findings, plan.ReviewFinding{Severity: fmt.Sprintf("%s%d", strings.Repeat("界", 60), i)})
	}
	events = append(events, plan.Event{Type: "plan_reviewed", Review: &plan.PlanReview{Status: "completed", Verdict: "changes_requested", Findings: findings}})
	for i, event := range events {
		consumeEvent(&data, event, i+1)
	}
	summary := plan.PlanSummary{Status: plan.StatusInProgress, OriginalTotalCount: 3, ReworkTotalCount: 2, Warnings: []string{"warning"}}
	got := observePlan(sourceIdentity{}, summary, data, now)
	if got.class != observationActive || got.matured || got.merged || got.exactApproval || !got.started || got.completed || got.abandoned {
		t.Fatalf("lifecycle = %+v", got)
	}
	if got.treatment.Source != "none" || !got.planningMetrics {
		t.Fatalf("metrics must not invent missing creation: %+v", got)
	}
	if !got.hasApproval || got.hoursToApproval != 2 || got.firstReviewVerdict != "approve" || got.reviewRounds != 1 || got.originalSlices != 3 || got.reworkSlices != 2 || got.verificationRepairs != 1 || got.reworkStops != 1 || got.validationWarnings != 1 {
		t.Fatalf("facts = %+v", got)
	}
	if len(got.severities) != 16 || got.severities["high"] != 2 || got.severities["other"] != 16 {
		t.Fatalf("severities = %+v", got.severities)
	}
	wantReliability := map[string]int{"blocked_unreachable_service": 1, "session_timeout": 1, "slice_resume_failed": 1, "verification_command_invalid": 1, "budget_exceeded": 1, "finalization_head_drift": 1, "provider_failure": 1, "changes_requested": 1, "final_verification_code": 1, "final_verification_tool_missing": 1, "final_verification_timeout": 1, "final_verification_cancelled": 1, "final_verification_invalid_command": 1}
	if !reflect.DeepEqual(got.reliability, wantReliability) {
		t.Fatalf("reliability = %+v", got.reliability)
	}
	if got.roles["unknown"].sessions != 1 || got.roles["unknown"].cost != 0 || got.roles["unknown"].outputTokens != 0 {
		t.Fatalf("unknown role = %+v", got.roles)
	}
}

func TestScorecardClassificationAndTreatment(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                  string
		summary               plan.PlanSummary
		events                []plan.Event
		class                 observationClass
		merged, pr, abandoned bool
	}{
		{name: "never started", summary: plan.PlanSummary{Status: plan.StatusPlanned}, class: observationNeverStarted},
		{name: "summary start", summary: plan.PlanSummary{Status: plan.StatusPlanned, StartedAt: &now}, class: observationActive},
		{name: "event start", summary: plan.PlanSummary{Status: plan.StatusPlanned}, events: []plan.Event{{Type: "slice_started"}}, class: observationActive},
		{name: "abandoned", summary: plan.PlanSummary{Status: plan.StatusAbandoned}, class: observationTerminal, abandoned: true},
		{name: "legacy completed", summary: plan.PlanSummary{Status: plan.StatusCompleted}, class: observationTerminal},
		{name: "merged", summary: plan.PlanSummary{Status: plan.StatusReviewed}, events: []plan.Event{{Type: "plan_merged"}}, class: observationTerminal, merged: true},
		{name: "pull request completed", summary: plan.PlanSummary{Status: plan.StatusReviewed, Complete: true, PullRequest: &plan.PullRequest{}}, class: observationTerminal, pr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := planData{lifecycle: tc.events}
			got := observePlan(sourceIdentity{}, tc.summary, data, now)
			if got.class != tc.class || got.matured != (tc.class == observationTerminal) || got.merged != tc.merged || got.pullRequestCompleted != tc.pr || got.abandoned != tc.abandoned {
				t.Fatalf("observation = %+v", got)
			}
		})
	}
	summary := plan.PlanSummary{ID: "20260230-invalid", Status: plan.StatusPlanned}
	data := planData{createdAgent: "build", createdAt: now}
	got := observePlan(sourceIdentity{}, summary, data, now)
	if got.era != "unknown" || got.effort != "unknown" || got.risk != "unknown" || got.changeType != "unknown" || got.repositoryKey != "unknown" {
		t.Fatalf("strata = %+v", got)
	}
	coverage := scorecardCoverage([]planObservation{got})
	if coverage.TreatmentAmbiguous != 1 || coverage.TreatmentMissing != 0 || coverage.ReasoningEffortRecorded != 0 {
		t.Fatalf("coverage = %+v", coverage)
	}
	summary.Overview.Priority = &plan.Priority{Effort: plan.PriorityEffortSmall, Risk: plan.PriorityLevelLow}
	summary.ChangeType = "feature"
	got = observePlan(sourceIdentity{}, summary, data, now)
	if got.effort != "small" || got.risk != "low" || got.changeType != "feature" {
		t.Fatalf("strata = %+v", got)
	}
}

func TestScorecardPartialHistoryAndHistogramBounds(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	data := planData{createdAt: now.Add(-time.Hour), createdAgent: "pi"}
	data.lifecycle = []plan.Event{
		{Type: "plan_reviewed", Review: &plan.PlanReview{Status: "completed", Verdict: "approve"}},
		{Type: "plan_reviewed", Timestamp: now, Review: &plan.PlanReview{Status: "completed", Verdict: "approve", Base: "base", Head: "head"}},
		{Type: "plan_reopened"},
	}
	for i := 0; i < 100; i++ {
		data.blockedReasons = append(data.blockedReasons, fmt.Sprintf("reason-%d", i))
		data.lifecycle = append(data.lifecycle, plan.Event{Type: "finalization_failed", FinalizationFailure: &plan.FinalizationFailure{Category: fmt.Sprintf("category-%d", i)}})
	}
	got := observePlan(sourceIdentity{}, plan.PlanSummary{Reviewed: true, ReviewVerdict: "approve"}, data, now)
	if got.hasApproval || got.exactApproval {
		t.Fatalf("partial/stale approval = %+v", got)
	}
	if len(got.reliability) > 64 || got.reliability["other"] == 0 {
		t.Fatalf("unbounded reliability: %+v", got.reliability)
	}
	for _, count := range got.reliability {
		if count <= 0 {
			t.Fatal("nonpositive histogram count")
		}
	}
	empty := scorecardCoverage(nil)
	if empty.Plans != 0 || empty.MaturityWindowDays != 14 || empty.MinimumSamples != 5 {
		t.Fatalf("empty coverage = %+v", empty)
	}
	before := time.Now()
	if resolved := (Options{}).now(); resolved.Before(before) || resolved.After(time.Now()) {
		t.Fatalf("zero clock = %v", resolved)
	}
}

func TestScorecardMeasurementPresence(t *testing.T) {
	for _, test := range []struct {
		name, metrics              string
		cost, output, total, calls []float64
	}{
		{name: "absent telemetry"},
		{name: "identity only", metrics: `{"role":"planning","agent":"pi","session_id":"s"}`},
		{name: "unavailable", metrics: `{"role":"planning","availability":"unavailable"}`},
		{name: "reported does not synthesize measurements", metrics: `{"role":"planning","availability":"reported"}`},
		{name: "null measurements", metrics: `{"role":"planning","cost":null,"output_tokens":null,"total_tokens":null,"tool_calls":null}`},
		{name: "explicit zeros", metrics: `{"role":"planning","availability":"reported","cost":0,"output_tokens":0,"total_tokens":0,"tool_calls":0}`, cost: []float64{0}, output: []float64{0}, total: []float64{0}, calls: []float64{0}},
		{name: "partial output", metrics: `{"role":"planning","availability":"partial","output_tokens":12}`, output: []float64{12}},
		{name: "partial zero cost", metrics: `{"role":"planning","availability":"partial","cost":0}`, cost: []float64{0}},
		{name: "legacy values", metrics: `{"role":"planning","cost":2,"output_tokens":12,"total_tokens":20,"tool_calls":3}`, cost: []float64{2}, output: []float64{12}, total: []float64{20}, calls: []float64{3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			contents := ""
			if test.metrics != "" {
				contents = `{"type":"agent_metrics","metrics":` + test.metrics + "}\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			data, err := readPlanEvents(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			o := observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted, Complete: true, OriginalTotalCount: 2}, data, time.Time{})
			observations := make([]planObservation, scorecardMinimumSamples)
			for i := range observations {
				observations[i] = o
			}
			out := buildCohortOutcomes(observations)
			for _, got := range []EfficiencyMedians{out.Efficiency.MaturedAll, out.Efficiency.MaturedCompleted} {
				for _, metric := range []struct {
					name    string
					got     MedianEstimate
					values  []float64
					divisor float64
				}{
					{"cost", got.Cost, test.cost, 1}, {"output", got.OutputTokens, test.output, 1},
					{"total", got.TotalTokens, test.total, 1}, {"calls", got.ToolCalls, test.calls, 1},
					{"slice cost", got.CostPerOriginalSlice, test.cost, 2}, {"slice output", got.OutputTokensPerOriginalSlice, test.output, 2},
					{"planning cost", got.PlanningCost, test.cost, 1}, {"planning output", got.PlanningOutputTokens, test.output, 1},
				} {
					var values []float64
					for range observations {
						for _, value := range metric.values {
							values = append(values, value/metric.divisor)
						}
					}
					if want := EstimateMedian(values, scorecardMinimumSamples); metric.got != want {
						t.Errorf("%s = %+v, want %+v", metric.name, metric.got, want)
					}
				}
				wantSessions := scorecardMinimumSamples
				if test.metrics == "" {
					wantSessions = 0
				}
				if got.Sessions.Samples != wantSessions {
					t.Errorf("sessions = %+v", got.Sessions)
				}
			}
		})
	}
}

func TestScorecardPartialSessionMeasurements(t *testing.T) {
	data := planData{sessions: make(map[string][]plan.AgentMetricEvent)}
	for i, metrics := range []plan.AgentMetrics{
		{Role: plan.AgentRoleExecution, SessionID: "s", Availability: plan.AgentMetricsPartial, Cost: 2, OutputTokensPresent: true},
		{Role: plan.AgentRoleExecution, SessionID: "s", Availability: plan.AgentMetricsPartial, TotalTokens: 10},
		{Role: plan.AgentRoleExecution, SessionID: "missing", Availability: plan.AgentMetricsUnavailable},
		{Role: plan.AgentRolePlanning, SessionID: "p", Availability: plan.AgentMetricsPartial, OutputTokens: 8},
		{Role: plan.AgentRolePlanning, SessionID: "p", Agent: "pi"},
	} {
		consumeEvent(&data, plan.Event{Type: plan.EventTypeAgentMetrics, Metrics: &metrics}, i+1)
	}
	o := observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted, Complete: true, OriginalTotalCount: 2}, data, time.Time{})
	if got := o.roles["execution"].availability; got != (plan.AgentMetricsAvailabilityCounts{Partial: 2, Unavailable: 1}) {
		t.Fatalf("execution availability = %+v", got)
	}
	if got := o.roles["planning"].availability; got != (plan.AgentMetricsAvailabilityCounts{Partial: 1, Unknown: 1}) {
		t.Fatalf("planning availability = %+v", got)
	}
	got := efficiencyMedians([]planObservation{o})
	if got.Sessions.Median != 3 || got.Cost != EstimateMedian([]float64{2}, 5) || got.OutputTokens != EstimateMedian([]float64{8}, 5) || got.TotalTokens != EstimateMedian([]float64{10}, 5) || got.CostPerOriginalSlice != EstimateMedian([]float64{1}, 5) || got.OutputTokensPerOriginalSlice != EstimateMedian([]float64{4}, 5) || got.PlanningCost.Samples != 0 || got.PlanningOutputTokens != EstimateMedian([]float64{8}, 5) || got.ToolCalls.Samples != 0 {
		t.Fatalf("partial recorded totals = %+v", got)
	}
}

func TestScorecardMissingMeasurementsCannotQualifyInversions(t *testing.T) {
	outcomes := func(cost float64, measured int) CohortOutcomes {
		var observations []planObservation
		for i := range 10 {
			data := planData{sessions: make(map[string][]plan.AgentMetricEvent)}
			metrics := plan.AgentMetrics{Role: plan.AgentRolePlanning, Agent: "pi"}
			if i < measured {
				metrics.Cost, metrics.CostPresent = cost, true
			}
			consumeEvent(&data, plan.Event{Type: plan.EventTypeAgentMetrics, Metrics: &metrics}, 1)
			observations = append(observations, observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted, Complete: true, OriginalTotalCount: 2}, data, time.Time{}))
		}
		got := buildCohortOutcomes(observations)
		for _, efficiency := range []EfficiencyMedians{got.Efficiency.MaturedAll, got.Efficiency.MaturedCompleted} {
			for _, estimate := range []MedianEstimate{efficiency.Cost, efficiency.CostPerOriginalSlice, efficiency.PlanningCost} {
				if estimate.Samples != measured || estimate.Sparse != (measured < scorecardMinimumSamples) {
					t.Fatalf("missing measurements changed sample threshold: %+v", estimate)
				}
			}
		}
		return got
	}
	for _, measured := range []int{0, 4, 5} {
		card := Scorecard{
			Cohorts: []TreatmentCohort{{Key: "a", Outcomes: outcomes(1, measured)}, {Key: "b", Outcomes: outcomes(0, measured)}},
			Strata:  []StratumBreakdown{{Stratum: "risk", Key: "high", Cohorts: []TreatmentCohort{{Key: "a", Outcomes: outcomes(0, measured)}, {Key: "b", Outcomes: outcomes(1, measured)}}}},
		}
		inversions := scorecardInversions(card)
		if measured < scorecardMinimumSamples && len(inversions) != 0 {
			t.Fatalf("missing costs qualified inversion: %+v", inversions)
		}
		if measured == scorecardMinimumSamples && (len(inversions) != 1 || inversions[0].Metric != "median_cost_matured_completed") {
			t.Fatalf("measured zero costs did not qualify inversion: %+v", inversions)
		}
	}
}

func TestBuildScorecardOutcomes(t *testing.T) {
	var observations []planObservation
	for _, label := range []string{"pi", "claude"} {
		for i := 1; i <= 6; i++ {
			observations = append(observations, planObservation{
				planID: fmt.Sprintf("%s-%d", label, i), treatment: NormalizePlannerLabel(label),
				repositoryKey: "repo", effort: "small", risk: "low", changeType: "feature", era: "2026-09",
				class: observationTerminal, matured: true, started: i != 6,
				completed: i <= 4, exactApproval: i <= 3, firstReviewVerdict: []string{"", "approve", "approve", "changes_requested", "approve", "", ""}[i],
				merged: i <= 2, pullRequestCompleted: i == 4, abandoned: i == 6,
				reworkStops: i % 2, reworkSlices: i % 3, verificationRepairs: i % 2,
				reviewRounds: i, originalSlices: 2, hasApproval: i <= 3, hoursToApproval: float64(i * 10),
				validationWarnings: i % 2, severities: map[string]int{"high": i},
				reliability: map[string]int{"provider_failure": 1, "changes_requested": 2, "final_verification_code": 1},
				roles: map[string]roleTotals{
					"execution":    {sessions: 2, outputTokens: int64(i * 100), totalTokens: int64(i * 200), cost: float64(i), toolCalls: int64(i), outputPresent: true, totalPresent: true, costPresent: true, toolCallsPresent: true},
					"unattributed": {sessions: 1, cost: 1, costPresent: true},
				},
			})
		}
	}
	// Recent active plans contribute to Started and usage, never matured rates.
	observations = append(observations, planObservation{treatment: NormalizePlannerLabel("pi"), started: true, class: observationActive})
	got := buildScorecard(observations)
	if len(got.Cohorts) != 2 || got.Cohorts[0].Key != "claude" || got.Cohorts[1].Key != "pi" {
		t.Fatalf("cohorts = %+v", got.Cohorts)
	}
	for _, cohort := range got.Cohorts {
		o := cohort.Outcomes
		if o.Matured != 6 || o.Quality.Completed != EstimateRate(4, 6, 5) || o.Quality.ExactApproval != EstimateRate(3, 6, 5) || o.Quality.FirstReviewApproved != EstimateRate(3, 6, 5) {
			t.Fatalf("quality = %+v", o)
		}
		for _, rate := range []RateEstimate{o.Quality.Merged, o.Quality.AnyRework} {
			if rate.Denominator != 6 || rate.Sparse {
				t.Fatalf("rate = %+v", rate)
			}
		}
		if o.Quality.Merged.Numerator != 2 || o.Quality.PullRequestCompleted.Numerator != 1 || o.Quality.Abandoned.Numerator != 1 || o.Quality.ReworkStopped.Numerator != 3 || o.Quality.AnyRework.Numerator != 4 || o.Quality.AnyVerificationRepair.Numerator != 3 || o.Quality.ReviewRounds.Median != 3 || o.Quality.OriginalSlices.Median != 2 || o.Quality.ValidationWarningPlans != 3 {
			t.Fatalf("quality counts = %+v", o.Quality)
		}
		all, completed := o.Efficiency.MaturedAll, o.Efficiency.MaturedCompleted
		if all.Plans != 6 || all.Cost != EstimateMedian([]float64{2, 3, 4, 5, 6, 7}, 5) || completed.Plans != 4 || completed.Cost.Median != 3 || !completed.Cost.Sparse || all.OutputTokens.Median != 300 || all.TotalTokens.Median != 600 || all.ToolCalls.Median != 3 || all.Sessions.Median != 3 || all.CostPerOriginalSlice.Median != 2 || all.OutputTokensPerOriginalSlice.Median != 150 || all.HoursToApproval.Median != 20 || all.HoursToApproval.Samples != 3 || all.PlanningCost.Samples != 0 {
			t.Fatalf("efficiency = %+v", o.Efficiency)
		}
		if len(o.Efficiency.ByRole) != 2 || o.Efficiency.ByRole[1].Role != "unattributed" || o.Efficiency.ByRole[0].Cost != 21 || math.Abs(o.Efficiency.RoleAttributedRatio-2.0/3) > 1e-12 {
			t.Fatalf("roles = %+v", o.Efficiency)
		}
		if !reflect.DeepEqual(o.Quality.FindingSeverities, []LabelCount{{Label: "high", Count: 21}}) || !reflect.DeepEqual(o.Reliability.Infrastructure, []LabelCount{{Label: "provider_failure", Count: 6}}) || !reflect.DeepEqual(o.Reliability.Quality, []LabelCount{{Label: "changes_requested", Count: 12}, {Label: "final_verification_code", Count: 6}, {Label: "rework_stopped", Count: 3}, {Label: "verification_repair_created", Count: 3}}) {
			t.Fatalf("histograms = %+v", o)
		}
	}
	if got.Cohorts[1].Outcomes.Quality.Started != EstimateRate(6, 7, 5) || got.Cohorts[1].Outcomes.Censored != 1 {
		t.Fatal("active plan not censored")
	}
	slices.Reverse(observations)
	if reverse := buildScorecard(observations); !reflect.DeepEqual(got, reverse) {
		t.Fatal("reversed observations changed scorecard")
	}
}

func TestBuildScorecardSparseAndExcluded(t *testing.T) {
	observations := []planObservation{
		{treatment: NormalizePlannerLabel("pi"), matured: true, completed: true, planningMetrics: true, roles: map[string]roleTotals{"planning": {sessions: 1, cost: 2, outputTokens: 100, costPresent: true, outputPresent: true}}},
		{treatment: NormalizePlannerLabel("build")}, {treatment: NormalizePlannerLabel("build")},
		{treatment: NormalizePlannerLabel("gpt-5.6-sol")}, {treatment: NormalizePlannerLabel("")},
	}
	got := buildScorecard(observations)
	want := []ExcludedLabel{{Label: "build", Plans: 2, Reason: "ambiguous"}, {Label: "", Plans: 1, Reason: "missing"}, {Label: "gpt-5.6-sol", Plans: 1, Reason: "ambiguous"}}
	if !reflect.DeepEqual(got.ExcludedLabels, want) {
		t.Fatalf("excluded = %+v", got.ExcludedLabels)
	}
	// Check every exported estimate, including empty medians, rather than a subset.
	var checkSparse func(reflect.Value)
	checkSparse = func(v reflect.Value) {
		if v.Kind() != reflect.Struct {
			return
		}
		if sparse := v.FieldByName("Sparse"); sparse.IsValid() && !sparse.Bool() {
			t.Errorf("non-sparse estimate: %+v", v.Interface())
		}
		for i := 0; i < v.NumField(); i++ {
			checkSparse(v.Field(i))
		}
	}
	checkSparse(reflect.ValueOf(got.Cohorts[0].Outcomes))
	e := got.Cohorts[0].Outcomes.Efficiency.MaturedAll
	if e.CostPerOriginalSlice.Samples != 0 || e.OutputTokensPerOriginalSlice.Samples != 0 || e.PlanningCost.Median != 2 || e.PlanningOutputTokens.Median != 100 {
		t.Fatalf("missing original slices / planning = %+v", e)
	}
}

func TestBuildScorecardInversions(t *testing.T) {
	// Globally pi completes 12/15 and claude 9/15. Within repo-a,
	// pi completes 9/10 versus claude 5/5. Repo-b ties at 3/5 and 6/10.
	var observations []planObservation
	for _, row := range []struct {
		label, repo  string
		n, completed int
	}{
		{"pi", "repo-a", 10, 9}, {"claude", "repo-a", 5, 5},
		{"pi", "repo-b", 5, 3}, {"claude", "repo-b", 10, 6},
	} {
		for i := 0; i < row.n; i++ {
			observations = append(observations, planObservation{treatment: NormalizePlannerLabel(row.label), repositoryKey: row.repo, matured: true, completed: i < row.completed})
		}
	}
	got := buildScorecard(observations)
	if len(got.Inversions) != 1 || got.Inversions[0].Metric != "completed_rate" || got.Inversions[0].Stratum != "repository" || got.Inversions[0].Key != "repo-a" || got.Inversions[0].CohortA != "claude" || got.Inversions[0].CohortB != "pi" {
		t.Fatalf("inversions = %+v", got.Inversions)
	}
	for i := range observations {
		observations[i].repositoryKey = fmt.Sprintf("sparse-%d", i)
	}
	if got := buildScorecard(observations); len(got.Inversions) != 0 {
		t.Fatalf("sparse inversions = %+v", got.Inversions)
	}
}

func TestBuildScorecardBoundsAndOrdering(t *testing.T) {
	var observations []planObservation
	for i := 0; i < 40; i++ {
		observations = append(observations, planObservation{
			treatment:     NormalizePlannerLabel(fmt.Sprintf("pi/model-%02d", i)),
			repositoryKey: fmt.Sprintf("repo-%02d", i), effort: "small", risk: "low", changeType: "feature", era: "2026-09",
			matured: true, completed: true,
			severities:  map[string]int{fmt.Sprintf("severity-%02d", i): 1, "other": 1},
			reliability: map[string]int{fmt.Sprintf("blocked_reason-%02d", i): 1, "other": 1},
		})
	}
	observations = append(observations, planObservation{treatment: NormalizePlannerLabel("pi/model-00"), repositoryKey: "other", effort: "small", risk: "low", changeType: "feature", era: "2026-09"})
	got := buildScorecard(observations)
	if len(got.Cohorts) != 32 || got.Cohorts[0].Key != "other" || got.Cohorts[0].Outcomes.Plans != 9 || got.Cohorts[0].Confidence != TreatmentConfidenceAmbiguous || got.Cohorts[0].Runtime != "" {
		t.Fatalf("capped cohorts = %+v", got.Cohorts)
	}
	var strata []string
	plans, repoKeys := 0, 0
	for _, stratum := range got.Strata {
		if len(strata) == 0 || strata[len(strata)-1] != stratum.Stratum {
			strata = append(strata, stratum.Stratum)
		}
		if stratum.Stratum == "repository" {
			repoKeys++
			for _, cohort := range stratum.Cohorts {
				plans += cohort.Outcomes.Plans
			}
		}
	}
	if repoKeys != 32 || plans != 41 || !reflect.DeepEqual(strata, []string{"repository", "effort", "risk", "change_type", "era"}) {
		t.Fatalf("strata = %v, repo keys %d, plans %d", strata, repoKeys, plans)
	}
	slices.Reverse(observations)
	if !reflect.DeepEqual(got, buildScorecard(observations)) {
		t.Fatal("overflow depends on observation order")
	}
	// Put all labels in one cohort to exercise histogram overflow, including
	// an existing 'other', equal-count lexical ties, and count conservation.
	for i := range observations {
		observations[i].treatment = NormalizePlannerLabel("pi")
	}
	got = buildScorecard(observations)
	o := got.Cohorts[0].Outcomes
	for _, histogram := range [][]LabelCount{o.Quality.FindingSeverities, o.Reliability.Infrastructure} {
		if len(histogram) != 16 || histogram[0] != (LabelCount{Label: "other", Count: 65}) {
			t.Fatalf("histogram = %+v", histogram)
		}
		total := 0
		for i, entry := range histogram {
			total += entry.Count
			if i > 1 && histogram[i-1].Label >= entry.Label {
				t.Fatal("tied histogram not lexical")
			}
		}
		if total != 80 {
			t.Fatalf("histogram total = %d", total)
		}
	}
}

func TestBuildScorecardStableFloatingTotalsAndMixedTreatments(t *testing.T) {
	var observations []planObservation
	for i, cost := range []float64{1e16, 1, 1} {
		treatment := NormalizePlannerLabel("pi")
		if i == 0 {
			treatment.Provider, treatment.Confidence = "provider", TreatmentConfidenceLow
		}
		observations = append(observations, planObservation{treatment: treatment, matured: true, roles: map[string]roleTotals{"execution": {cost: cost, sessions: 1, costPresent: true}}})
	}
	got := buildScorecard(observations)
	slices.Reverse(observations)
	if !reflect.DeepEqual(got, buildScorecard(observations)) {
		t.Fatal("floating totals or treatment fields depend on observation order")
	}
	if cohort := got.Cohorts[0]; cohort.Provider != "" || cohort.Confidence != TreatmentConfidenceLow || cohort.Runtime != "pi" {
		t.Fatalf("mixed treatment = %+v", cohort)
	}
	if _, err := json.Marshal(Report{Scorecard: got}); err != nil {
		t.Fatal(err)
	}
}

func TestScorecardInversionMetricSelection(t *testing.T) {
	outcomes := func(value float64) CohortOutcomes {
		rate := RateEstimate{Rate: value, Denominator: 5}
		median := MedianEstimate{Median: value, Samples: 5}
		return CohortOutcomes{
			Quality:    QualityOutcomes{ExactApproval: rate, Completed: rate, ReworkStopped: rate},
			Efficiency: EfficiencyOutcomes{MaturedCompleted: EfficiencyMedians{Cost: median}, MaturedAll: EfficiencyMedians{HoursToApproval: median}},
		}
	}
	card := Scorecard{
		Cohorts: []TreatmentCohort{{Key: "a", Outcomes: outcomes(1)}, {Key: "b", Outcomes: outcomes(0)}},
		Strata:  []StratumBreakdown{{Stratum: "risk", Key: "high", Cohorts: []TreatmentCohort{{Key: "a", Outcomes: outcomes(0)}, {Key: "b", Outcomes: outcomes(1)}}}},
	}
	var metrics []string
	for _, inversion := range scorecardInversions(card) {
		metrics = append(metrics, inversion.Metric)
		if inversion.GlobalDelta != 1 || inversion.StratumDelta != -1 {
			t.Fatalf("inversion = %+v", inversion)
		}
	}
	if !reflect.DeepEqual(metrics, []string{"completed_rate", "exact_approval_rate", "median_cost_matured_completed", "median_hours_to_approval", "rework_stopped_rate"}) {
		t.Fatalf("inversion metrics = %v", metrics)
	}
}

func TestScorecardExistingPlansMarshalAndSourceIDs(t *testing.T) {
	lister := fixtureLister{summaries: []plan.PlanSummary{{ID: "alpha", Dir: "testdata/plan-alpha"}, {ID: "beta", Dir: "testdata/plan-beta"}}}
	report, err := Aggregate(context.Background(), lister)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatal(err)
	}
	if report.Scorecard.Coverage.Plans != report.PlansScanned || report.PlansScanned != 2 {
		t.Fatalf("coverage = %+v", report.Scorecard.Coverage)
	}
	multi, err := AggregateSourcesWithOptions(context.Background(), fixtureSourceLister{sources: []RepositorySource{
		{ID: "repo-z", Name: "same name", Plans: fixtureLister{summaries: scorecardFixtures()}},
		{ID: "repo-a", Name: "same name", Plans: fixtureLister{summaries: scorecardFixtures()}},
	}}, Options{Now: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, stratum := range multi.Scorecard.Strata {
		if stratum.Stratum == "repository" {
			keys = append(keys, stratum.Key)
		}
	}
	if !reflect.DeepEqual(keys, []string{"repo-a", "repo-z"}) {
		t.Fatalf("repository keys = %v", keys)
	}
}

func TestScorecardMaturityUsesUTCDay(t *testing.T) {
	now := time.Date(2026, 9, 26, 23, 59, 0, 0, time.UTC)
	boundary := now.Truncate(24 * time.Hour).Add(-14 * 24 * time.Hour)
	for _, offset := range []time.Duration{-time.Second, 0, time.Second} {
		last := boundary.Add(offset)
		summary := plan.PlanSummary{Status: plan.StatusInProgress, LastActivityAt: &last}
		got := observePlan(sourceIdentity{}, summary, planData{}, now)
		if got.matured != (offset <= 0) {
			t.Fatalf("offset %s: matured %v", offset, got.matured)
		}
		same := observePlan(sourceIdentity{}, summary, planData{}, now.In(time.FixedZone("west", -7*3600)))
		if !reflect.DeepEqual(got, same) {
			t.Fatal("timezone changed observation")
		}
	}
}
