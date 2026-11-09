package view

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/insights"
)

func presentationTrend() insights.TrendReport {
	rate := insights.EstimateRate(0, 6, 5)
	median := insights.EstimateMedian([]float64{0, 2}, 5)
	return insights.TrendReport{AsOf: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), MaturityDays: 14, MinimumSamples: 5,
		Coverage: insights.TrendCoverage{Plans: 10, Eligible: 6, Active: 1, NeverStarted: 1, Unbucketed: 1, UnknownRuntime: 1},
		Buckets: []insights.TrendBucket{
			{Month: "2026-11", Cohort: "unknown", Provisional: true, AgeImmature: true},
			{Month: "2026-10", Cohort: "overall", AgeImmature: true, Metrics: []insights.TrendMetric{
				{Key: "recorded_blocker", Direction: "lower", Availability: "available", EligiblePlans: 6, EvidencePlans: 6, Rate: &rate, Comparison: "inconclusive"},
				{Key: "median_cost_per_plan", Direction: "lower", Availability: "incomplete", EligiblePlans: 6, EvidencePlans: 2, PartialSamples: 1, Median: &median, Comparison: "unavailable"},
				{Key: "unattended_rate", Direction: "higher", Availability: "available", EligiblePlans: 5, EvidencePlans: 5, Rate: new(insights.EstimateRate(2, 5, 5)), Comparison: "inconclusive"},
				{Key: "unattended_rate", Direction: "higher", Availability: "unavailable", Reason: "intervention_attribution_missing", Comparison: "unavailable"},
			}},
		}}
}

func TestInsightsTrendPresentation(t *testing.T) {
	for _, scope := range []InsightsScope{InsightsScopeRepository, InsightsScopeAllRepositories} {
		trend := presentationTrend()
		before, _ := json.Marshal(trend)
		var text, again, encoded, encodedAgain bytes.Buffer
		report := insights.Report{Trend: &trend, BlockedReasons: []insights.ReasonBucket{{Reason: "private diagnostic /source/path prompt"}}}
		options := InsightsOptions{Scope: scope, Format: InsightsFormatTrend}
		if err := RenderInsights(&text, report, options); err != nil {
			t.Fatal(err)
		}
		if err := RenderInsights(&again, report, options); err != nil {
			t.Fatal(err)
		}
		if err := RenderInsightsTrendJSON(&encoded, trend, scope); err != nil {
			t.Fatal(err)
		}
		if err := RenderInsightsTrendJSON(&encodedAgain, trend, scope); err != nil {
			t.Fatal(err)
		}
		if encoded.String() != encodedAgain.String() {
			t.Fatal("nondeterministic JSON")
		}
		var decoded trendProjection
		if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, projectTrend(trend, scope)) {
			t.Fatal("JSON and text projection disagree")
		}
		if text.String() != again.String() {
			t.Fatal("nondeterministic")
		}
		after, _ := json.Marshal(trend)
		if !bytes.Equal(before, after) {
			t.Fatal("mutated input")
		}
		for _, want := range []string{"0/6 = 0.00%", "2/5 = 40.00%", "5 / 5 / 0 | available", "recorded_blocker", "median 0", "2 (insufficient samples)", "6 / 2 / 1 | incomplete", "unattended_rate", "intervention_attribution_missing", "active (censored): 1", "never-started (censored): 1", "unbucketed: 1", "unknown attribution: 1", "provisional: false; age-immature: true", "provisional: true; age-immature: true", "inconclusive", "Adjacent calendar month", "Medians have no directional verdict"} {
			if !strings.Contains(text.String(), want) {
				t.Errorf("missing %q: %s", want, text.String())
			}
		}
		if strings.Index(text.String(), "## 2026-10") > strings.Index(text.String(), "## 2026-11") {
			t.Fatal("not chronological")
		}
		for _, output := range []string{text.String(), encoded.String()} {
			if strings.Contains(output, "private diagnostic") || strings.Contains(output, "/source/path") {
				t.Fatal("raw report leaked")
			}
		}
	}
}

func TestInsightsTrendUntrustedAndNonfinite(t *testing.T) {
	for _, label := range []string{"\x1b[31msecret\n/source/path", "秘密 prompt", string([]byte{0xff, 0xfe}), "<script>raw events</script>"} {
		trend := presentationTrend()
		trend.Buckets[0].Month = label
		trend.Buckets[0].Cohort = label
		metric := &trend.Buckets[1].Metrics[0]
		metric.Key = label
		metric.Direction = label
		metric.Reason = label
		metric.Comparison = label
		metric.Availability = label
		metric.Rate.Rate = math.NaN()
		trend.Buckets[1].Metrics[1].Median.Median = math.Inf(1)
		var text, encoded bytes.Buffer
		if err := RenderInsights(&text, insights.Report{Trend: &trend}, InsightsOptions{Scope: InsightsScopeRepository, Format: InsightsFormatTrend}); err != nil {
			t.Fatal(err)
		}
		if err := RenderInsightsTrendJSON(&encoded, trend, InsightsScopeRepository); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(encoded.Bytes()) {
			t.Fatal("invalid JSON")
		}
		for _, output := range []string{text.String(), encoded.String()} {
			if strings.Contains(output, label) || strings.Contains(output, "secret") || !utf8.ValidString(output) || strings.Contains(output, "NaN") {
				t.Fatalf("unsafe output: %s", output)
			}
		}
		if !math.IsNaN(metric.Rate.Rate) {
			t.Fatal("input modified")
		}
	}
}

func TestTrendBlockerPlanCountsAndMedianComparison(t *testing.T) {
	trend := presentationTrend()
	trend.Buckets[1].Metrics[1].Comparison = "better"
	for _, scope := range []InsightsScope{InsightsScopeRepository, InsightsScopeAllRepositories} {
		var text, encoded bytes.Buffer
		report := insights.Report{Trend: &trend, BlockedReasons: []insights.ReasonBucket{{Reason: "legacy_diagnostic", Count: 99}}}
		if err := RenderInsights(&text, report, InsightsOptions{Scope: scope, Format: InsightsFormatTrend}); err != nil {
			t.Fatal(err)
		}
		if err := RenderInsightsTrendJSON(&encoded, trend, scope); err != nil {
			t.Fatal(err)
		}
		var p trendProjection
		if err := json.Unmarshal(encoded.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		m := p.Buckets[0].Metrics[0]
		if m.Key != "recorded_blocker" || m.Rate.Numerator != 0 || m.Rate.Denominator != 6 || m.EligiblePlans != 6 {
			t.Fatalf("blockers not counted by plan: %+v", m)
		}
		if p.Buckets[0].Metrics[1].Comparison != "unavailable" {
			t.Fatal("median given directional verdict")
		}
		if strings.Contains(text.String(), "legacy_diagnostic") || strings.Contains(text.String(), "| better |") {
			t.Fatal(text.String())
		}
	}
}

type trendFailWriter struct{}

var errTrendWrite = errors.New("trend write failed")

func (trendFailWriter) Write([]byte) (int, error) { return 0, errTrendWrite }

func TestInsightsTrendEmptyAndErrors(t *testing.T) {
	for _, scope := range []InsightsScope{InsightsScopeRepository, InsightsScopeAllRepositories} {
		for _, trend := range []*insights.TrendReport{nil, {}} {
			var text bytes.Buffer
			if err := RenderInsights(&text, insights.Report{Trend: trend}, InsightsOptions{Scope: scope, Format: InsightsFormatTrend}); err != nil {
				t.Fatal(err)
			}
			want := "No monthly evidence."
			if trend == nil {
				want = "Trend data unavailable."
			}
			if !strings.Contains(text.String(), want) {
				t.Fatal(text.String())
			}
		}
		if err := RenderInsights(trendFailWriter{}, insights.Report{Trend: new(presentationTrend())}, InsightsOptions{Scope: scope, Format: InsightsFormatTrend}); !errors.Is(err, errTrendWrite) {
			t.Fatal(err)
		}
		if err := RenderInsightsTrendJSON(trendFailWriter{}, presentationTrend(), scope); !errors.Is(err, errTrendWrite) {
			t.Fatal(err)
		}
		var encoded bytes.Buffer
		if err := RenderInsightsTrendJSON(&encoded, insights.TrendReport{}, scope); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(encoded.String(), `"buckets": []`) {
			t.Fatal(encoded.String())
		}
	}
	if err := RenderInsightsTrendJSON(nil, presentationTrend(), InsightsScopeRepository); err == nil {
		t.Fatal("nil writer accepted")
	}
	if err := RenderInsightsTrendJSON(&bytes.Buffer{}, presentationTrend(), "bad"); err == nil {
		t.Fatal("bad scope accepted")
	}
}
