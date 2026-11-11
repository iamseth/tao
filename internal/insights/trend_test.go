package insights

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func buildTrend(observations []planObservation, now time.Time) TrendReport {
	report, err := buildTrendContext(context.Background(), observations, now)
	if err != nil {
		panic(err)
	}
	return report
}

func TestTrendDefectCoverage(t *testing.T) {
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                           string
		results                        []*DefectPlanResult
		denominator, missing, immature int
		availability                   string
	}{
		{"missing", []*DefectPlanResult{{Availability: DefectUnknown}}, 0, 1, 0, "unavailable"},
		{"immature", []*DefectPlanResult{{Availability: DefectImmature}}, 0, 0, 1, "unavailable"},
		{"zero", []*DefectPlanResult{{Mature: true, Availability: DefectAvailable}}, 1, 0, 0, "available"},
		{"partial", []*DefectPlanResult{{Mature: true, Availability: DefectAvailable, WindowFixes: 2}, nil, {Availability: DefectImmature}}, 1, 1, 1, "incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var obs []planObservation
			for _, p := range tc.results {
				o := trendFixture(now)
				o.defect = p
				obs = append(obs, o)
			}
			r := buildTrend(obs, now)
			for _, b := range r.Buckets {
				m := b.Metrics[9]
				if m.Availability != tc.availability || m.EligiblePlans != tc.denominator || b.DefectMissing != tc.missing || b.DefectImmature != tc.immature {
					t.Fatalf("bucket %+v", b)
				}
				if m.Rate != nil || m.Comparison != "unavailable" {
					t.Fatal(m)
				}
			}
		})
	}
}

func TestTrendDefectIndependentWindow(t *testing.T) {
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	o := trendFixture(now)
	o.class = observationActive // legacy eligibility must not control defect counts
	o.defect = &DefectPlanResult{Mature: true, Availability: DefectIncomplete, WindowFixes: 3}
	report := buildTrend([]planObservation{o}, now)
	for _, b := range report.Buckets {
		m := b.Metrics[9]
		if m.Count == nil || m.Count.Numerator != 3 || m.Count.Denominator != 1 || m.Count.Quotient != 3 || m.Availability != "incomplete" || m.Rate != nil || m.Comparison != "unavailable" {
			t.Fatalf("defect metric: %+v", m)
		}
		if b.Coverage.Eligible != 0 {
			t.Fatal("legacy maturity changed")
		}
	}
}

func TestTrendMaturityIsolation(t *testing.T) {
	now := time.Date(2026, 3, 15, 23, 0, 0, 0, time.UTC)
	created := time.Date(2026, 3, 1, 23, 59, 0, 0, time.UTC)
	observations := []planObservation{
		observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted}, planData{createdAt: created}, now),
		observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted}, planData{createdAt: created.Add(24 * time.Hour)}, now),
		observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusInProgress, LastActivityAt: &created}, planData{createdAt: created}, now),
		observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusPlanned, LastActivityAt: &created}, planData{createdAt: created}, now),
	}
	got := buildTrend(observations, now)
	if got.Coverage.Eligible != 1 || got.Coverage.Immature != 1 || got.Coverage.Active != 1 || got.Coverage.NeverStarted != 1 {
		t.Fatalf("coverage: %+v", got.Coverage)
	}
	if !observations[1].matured {
		t.Fatal("scorecard terminal maturity changed")
	}
	if len(got.Buckets) != 2 || !got.Buckets[0].Provisional || !got.Buckets[0].AgeImmature {
		t.Fatalf("buckets: %+v", got.Buckets)
	}
}

func trendFixture(now time.Time) planObservation {
	return planObservation{createdAt: now.AddDate(0, -2, 0), class: observationTerminal, historyPresent: true, treatment: NormalizePlannerLabel("pi"), firstReviewVerdict: "approve"}
}

func TestTrendCreationCoverageAndCensoring(t *testing.T) {
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, -3, 0)
	cases := []struct {
		name                                          string
		created                                       time.Time
		status                                        string
		eligible, unbucketed, immature, active, never int
	}{
		{"boundary", now.AddDate(0, 0, -14), plan.StatusCompleted, 1, 0, 0, 0, 0},
		{"young", now.AddDate(0, 0, -13), plan.StatusCompleted, 0, 0, 1, 0, 0},
		{"abandoned", old, plan.StatusAbandoned, 1, 0, 0, 0, 0},
		{"missing", time.Time{}, plan.StatusCompleted, 0, 1, 0, 0, 0},
		{"future", now.Add(time.Second), plan.StatusCompleted, 0, 1, 0, 0, 0},
		{"active", old, plan.StatusInProgress, 0, 0, 0, 1, 0},
		{"never", old, plan.StatusPlanned, 0, 0, 0, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := observePlan(sourceIdentity{}, plan.PlanSummary{ID: "20200101-not-creation", Status: tc.status, LastActivityAt: &old}, planData{createdAt: tc.created, earliestAt: old}, now)
			c := buildTrend([]planObservation{o}, now).Coverage
			if c.Eligible != tc.eligible || c.Unbucketed != tc.unbucketed || c.Immature != tc.immature || c.Active != tc.active || c.NeverStarted != tc.never {
				t.Fatalf("coverage: %+v", c)
			}
			if !o.matured {
				t.Fatal("scorecard inactivity/terminal maturity changed")
			}
		})
	}
	// A local February timestamp is March in UTC; a closed bucket can be immature.
	o := trendFixture(now)
	o.createdAt = time.Date(2026, 2, 28, 23, 0, 0, 0, time.FixedZone("west", -7*3600))
	r := buildTrend([]planObservation{o}, now)
	if r.Buckets[0].Month != "2026-03" {
		t.Fatal(r.Buckets)
	}
	r = buildTrend([]planObservation{o}, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if r.Buckets[0].Provisional {
		t.Fatal(r.Buckets)
	}
	o.createdAt = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	r = buildTrend([]planObservation{o}, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if !r.Buckets[0].AgeImmature || r.Buckets[0].Provisional {
		t.Fatal(r.Buckets)
	}
}

func TestTrendCostMissingWithinMeasuredRole(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	data := planData{createdAt: now.AddDate(0, -2, 0), sessions: map[string][]plan.AgentMetricEvent{
		"measured": {{Metrics: plan.AgentMetrics{CostPresent: true, Cost: 0}}},
		"missing":  {{Metrics: plan.AgentMetrics{OutputTokensPresent: true, OutputTokens: 10}}},
	}}
	o := observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted}, data, now)
	m := trendMetrics([]planObservation{o})[5]
	if m.Median == nil || m.Median.Median != 0 || m.Median.Samples != 1 || m.PartialSamples != 1 || m.Availability != "incomplete" {
		t.Fatal(m)
	}
}

func TestTrendMeasurementsAndRecordedOutcomes(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	o := trendFixture(now)
	data := planData{createdAt: o.createdAt, createdAgent: "pi", historyPresent: true, sessions: make(map[string][]plan.AgentMetricEvent)}
	approved := &plan.PlanReview{Status: "completed", Verdict: "approve"}
	events := []plan.Event{
		{Type: plan.EventTypePlanReviewed, Timestamp: o.createdAt.Add(-time.Hour), Review: approved},
		{Type: plan.EventTypePlanReviewed, Timestamp: o.createdAt.Add(5 * time.Hour), Review: approved},
		{Type: plan.EventTypePlanReviewed, Timestamp: o.createdAt.Add(2 * time.Hour), Review: approved},
		{Type: plan.EventTypePlanMerged, Timestamp: o.createdAt.Add(9 * time.Hour)},
		{Type: plan.EventTypeVerificationRepairCreated}, {Type: plan.EventTypeVerificationRepairCreated},
		{Type: plan.EventTypeReworkStopped}, {Type: plan.EventTypeReworkStopped},
		{Type: plan.EventTypeSliceBlocked}, {Type: plan.EventTypeSliceBlocked},
	}
	for i, e := range events {
		consumeEvent(&data, e, i)
	}
	o = observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted}, data, now)
	o.roles = map[string]roleTotals{"implementation": {costPresent: true, cost: 0}, "review": {costPresent: false}}
	ms := trendMetrics([]planObservation{o})
	for i := range 5 {
		if ms[i].Rate == nil || ms[i].Rate.Numerator != 1 || ms[i].Rate.Denominator != 1 {
			t.Fatalf("metric %+v", ms[i])
		}
	}
	if ms[5].Median.Median != 0 || ms[5].PartialSamples != 1 || ms[5].Availability != "incomplete" {
		t.Fatal(ms[5])
	}
	if ms[6].Median.Median != 2 || ms[7].Median.Median != 9 {
		t.Fatal(ms)
	}
	if m := ms[8]; m.Availability != "unavailable" || m.Reason != "intervention_attribution_missing" || m.Rate != nil {
		t.Fatal(m)
	}
	for _, m := range ms[9:] {
		if m.Availability != "unavailable" || m.Reason == "" || m.Rate != nil || m.Median != nil {
			t.Fatal(m)
		}
	}
	for _, cost := range []float64{math.NaN(), math.Inf(1), -1} {
		o.roles = map[string]roleTotals{"implementation": {costPresent: true, cost: cost}}
		if m := trendMetrics([]planObservation{o})[5]; m.Median != nil {
			t.Fatalf("nonfinite/negative accepted: %+v", m)
		}
	}
	o.roles = nil
	o.firstApprovalAt = time.Time{}
	o.mergeAt = time.Time{}
	for _, m := range trendMetrics([]planObservation{o})[5:8] {
		if m.Median != nil || m.Availability != "unavailable" {
			t.Fatal(m)
		}
	}
	pr := observePlan(sourceIdentity{}, plan.PlanSummary{Status: plan.StatusCompleted, Complete: true}, planData{createdAt: data.createdAt, lifecycle: []plan.Event{{Type: plan.EventTypePullRequestCreated, Timestamp: now}}}, now)
	if !pr.mergeAt.IsZero() {
		t.Fatal("PR manufactured merge timing")
	}
}

func TestTrendUnattendedCoverageStates(t *testing.T) {
	attributed := planObservation{attributionComplete: true}
	unattended := planObservation{attributionComplete: true, unattended: true}
	for _, tt := range []struct {
		name         string
		observations []planObservation
		availability string
		evidence     int
		numerator    int
	}{
		{"complete", []planObservation{unattended, attributed, unattended}, "available", 3, 2},
		{"partial", []planObservation{unattended, attributed, {}}, "incomplete", 2, 1},
		{"missing", []planObservation{{}, {}}, "unavailable", 0, 0},
		{"empty", nil, "unavailable", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ms := trendMetrics(tt.observations)
			keys := []string{"first_review_approved", "any_rework", "any_verification_repair", "rework_stopped", "recorded_blocker", "median_cost_per_plan", "median_hours_to_approval", "median_hours_to_merge", "unattended_rate", "escaped_defects_per_plan"}
			if len(ms) != len(keys) {
				t.Fatalf("metric count = %d", len(ms))
			}
			for i, key := range keys {
				if ms[i].Key != key {
					t.Fatalf("metric %d = %s, want %s", i, ms[i].Key, key)
				}
			}
			m := ms[8]
			if m.Availability != tt.availability || m.EvidencePlans != tt.evidence || m.EligiblePlans != len(tt.observations) || m.Direction != "higher" || m.Comparison != "unavailable" {
				t.Fatalf("unexpected metric: %+v", m)
			}
			if tt.evidence == 0 {
				if m.Rate != nil || m.Reason != "intervention_attribution_missing" {
					t.Fatal(m)
				}
			} else if m.Rate == nil || m.Rate.Numerator != tt.numerator || m.Rate.Denominator != len(tt.observations) || m.Reason != "" {
				t.Fatal(m)
			}
			if d := ms[9]; d.Availability != "unavailable" || d.Reason != "defect_linkage_missing" || d.Direction != "lower" || d.Rate != nil {
				t.Fatal(d)
			}
		})
	}
	previous := trendMetrics([]planObservation{unattended})[8]
	current := trendMetrics([]planObservation{unattended, {}})[8]
	if got := compareTrendRates(previous, current); got != "unavailable" {
		t.Fatalf("partial coverage comparison = %s", got)
	}
}

func TestTrendAdjacentMonthsAndCancellation(t *testing.T) {
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	var observations []planObservation
	for i := range 100 {
		a := trendFixture(now)
		a.createdAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		a.firstReviewVerdict = "changes_requested"
		a.planID = "same"
		b := a
		b.createdAt = a.createdAt.AddDate(0, 1, 0)
		b.firstReviewVerdict = "approve"
		if i%2 == 0 {
			a.repository = sourceIdentity{id: "other"}
			b.repository = a.repository
		}
		observations = append(observations, a, b)
	}
	r := buildTrend(observations, now)
	for _, b := range r.Buckets {
		if b.Month == "2026-02" && (b.Metrics[0].Comparison != "better" || b.Metrics[1].Comparison != "inconclusive") {
			t.Fatal(b)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildTrendContext(ctx, observations, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := buildTrendContext(ctx, nil, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTrendComparisonConservative(t *testing.T) {
	metric := func(n, d int, direction string) TrendMetric {
		r := EstimateRate(n, d, 5)
		return TrendMetric{Rate: &r, Availability: "available", Direction: direction}
	}
	for _, tc := range []struct {
		a, b TrendMetric
		want string
	}{
		{metric(0, 100, "higher"), metric(100, 100, "higher"), "better"},
		{metric(0, 100, "lower"), metric(100, 100, "lower"), "worse"},
		{metric(100, 100, "lower"), metric(0, 100, "lower"), "better"},
		{metric(0, 4, "higher"), metric(4, 4, "higher"), "inconclusive"},
		{metric(4, 10, "higher"), metric(6, 10, "higher"), "inconclusive"},
		{TrendMetric{}, metric(1, 10, "higher"), "unavailable"},
	} {
		if got := compareTrendRates(tc.a, tc.b); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
	a, b := metric(0, 100, "higher"), metric(100, 100, "higher")
	b.Rate.Lower = a.Rate.Upper
	if compareTrendRates(a, b) != "inconclusive" {
		t.Fatal("touching intervals compared")
	}
	b.Availability = "incomplete"
	if compareTrendRates(a, b) != "unavailable" {
		t.Fatal("incomplete compared")
	}
}

func TestTrendDeterminismSparseAndUnknown(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	a := trendFixture(now)
	a.createdAt = now.AddDate(0, -4, 0)
	b := trendFixture(now)
	b.createdAt = now.AddDate(0, -2, 0)
	b.treatment = NormalizePlannerLabel("build")
	c := b
	c.treatment = NormalizePlannerLabel("")
	c.historyMalformed = true
	observations := []planObservation{a, b, c}
	before := slices.Clone(observations)
	got := buildTrend(observations, now)
	if !reflect.DeepEqual(before, observations) {
		t.Fatal("mutated input")
	}
	slices.Reverse(observations)
	if !reflect.DeepEqual(got, buildTrend(observations, now)) {
		t.Fatal("input order changed projection")
	}
	if got.Coverage.UnknownRuntime != 1 || got.Coverage.AmbiguousRuntime != 1 || got.Coverage.MalformedHistory != 1 {
		t.Fatal(got.Coverage)
	}
	for _, bucket := range got.Buckets {
		for _, m := range bucket.Metrics {
			if m.Comparison != "unavailable" {
				t.Fatal("bridged missing February", m)
			}
		}
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatal(err)
	}
	empty := buildTrend(nil, now)
	if len(empty.Buckets) != 0 || empty.Coverage.Plans != 0 {
		t.Fatal(empty)
	}
	missing := trendFixture(now)
	missing.historyPresent = false
	missing.firstReviewVerdict = ""
	for _, m := range trendMetrics([]planObservation{missing})[:5] {
		if m.Rate != nil || m.Availability != "unavailable" {
			t.Fatal(m)
		}
	}
}

func TestTrendMechanicalScope(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for _, classification := range []string{"mechanical", "judgement"} {
		active := scopeObservation(classification, plan.StatusInProgress, now)
		immature := scopeObservation(classification, plan.StatusCompleted, now)
		immature.createdAt = now
		trend := buildTrend([]planObservation{active, immature}, now)
		if trend.Coverage.Active != 1 || trend.Coverage.Immature != 1 || trend.Coverage.Eligible != 0 {
			t.Fatalf("changed censoring: %+v", trend.Coverage)
		}
	}
	mechanical := scopeObservation("mechanical", plan.StatusCompleted, now)
	judgement := scopeObservation("judgement", plan.StatusCompleted, now)
	a, b := buildTrend([]planObservation{mechanical}, now), buildTrend([]planObservation{judgement}, now)
	if !reflect.DeepEqual(a.Coverage, b.Coverage) {
		t.Fatal("classification changed eligibility")
	}
	for _, bucket := range a.Buckets {
		for _, metric := range bucket.Metrics {
			if metric.Key == "unattended_rate" && (metric.Rate == nil || metric.Rate.Numerator != 1 || metric.Rate.Denominator != 1) {
				t.Fatalf("mechanical rate: %+v", metric)
			}
		}
	}
	for _, bucket := range b.Buckets {
		for _, metric := range bucket.Metrics {
			if metric.Key == "unattended_rate" && (metric.Rate == nil || metric.Rate.Numerator != 0 || metric.Rate.Denominator != 1) {
				t.Fatalf("judgement rate: %+v", metric)
			}
		}
	}
}
