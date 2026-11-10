package tui

import (
	"math"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

func TestRenderTrendsBoundsAndEvidence(t *testing.T) {
	m := TrendsModel{Width: 60, Height: 30, Snapshot: TrendsSnapshot{RepositoryName: "界 repository", Buckets: []TrendBucket{{Month: "2026-09", Cohort: "overall", Metrics: []TrendMetric{{Key: "first_review_approved", Direction: "higher", Availability: "available", Comparison: "better", Rate: &TrendRate{Rate: 0, Denominator: 20}}}}}}}
	s := RenderTrends(m)
	t.Logf("synthetic narrow frame (not historical evidence):\n%s", s)
	for _, want := range []string{"monthly", "0.0%", "n=20", "↑ better"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q: %s", want, s)
		}
	}
	m.Snapshot.Buckets[0].Provisional = true
	s = RenderTrends(m)
	if strings.Contains(s, "↑") || !strings.Contains(s, "provisional") {
		t.Fatal(s)
	}
	for w := 1; w < 100; w++ {
		m.Width = w
		m.Height = 4
		s = RenderTrends(m)
		if len(strings.Split(s, "\n")) > 4 {
			t.Fatal("height")
		}
		for _, line := range strings.Split(s, "\n") {
			if cells.Width(line) > w {
				t.Fatalf("width %d: %q", w, line)
			}
		}
	}
}

func TestTrendsComparisonDoesNotInventClaims(t *testing.T) {
	for _, direction := range []string{"higher", "lower", "unknown"} {
		for _, comparison := range []string{"better", "worse", "inconclusive", "flat", "unavailable"} {
			m := TrendMetric{Direction: direction, Comparison: comparison, Availability: "available", Rate: &TrendRate{Rate: .5, Denominator: 20}}
			text, _ := trendComparison(m, false)
			allowed := direction != "unknown" && (comparison == "better" || comparison == "worse")
			if strings.ContainsAny(text, "↑↓") != allowed {
				t.Fatal(direction, comparison, text)
			}
			if allowed {
				up := (comparison == "better") == (direction == "higher")
				if strings.Contains(text, "↑") != up {
					t.Fatal(text)
				}
			}
			for _, kind := range []string{"provisional", "sparse", "median", "missing", "incomplete", "nan"} {
				copy := m
				r := *m.Rate
				copy.Rate = &r
				switch kind {
				case "sparse":
					copy.Rate.Sparse = true
				case "median":
					copy.Rate = nil
					copy.Median = &TrendMedian{Median: 1, Samples: 20}
				case "missing":
					copy.Rate = nil
				case "incomplete":
					copy.Availability = "incomplete"
				case "nan":
					copy.Rate.Rate = math.NaN()
				}
				text, _ = trendComparison(copy, kind == "provisional")
				if strings.ContainsAny(text, "↑↓") {
					t.Fatal(kind, text)
				}
			}
		}
	}
}

func TestTrendsMissingMonthsUnitsAndScrolling(t *testing.T) {
	metric := TrendMetric{Key: "median_cost_per_plan", Availability: "incomplete", EvidencePlans: 3, EligiblePlans: 10, PartialSamples: 1, Median: &TrendMedian{Median: 0, Samples: 3, Sparse: true}}
	m := TrendsModel{Width: 180, Height: 40, Snapshot: TrendsSnapshot{Buckets: []TrendBucket{{Month: "2026-07", Cohort: "pi", Metrics: []TrendMetric{metric}}, {Month: "2026-09", Cohort: "pi", AgeImmature: true, Metrics: []TrendMetric{metric}}}}, Cohort: "pi"}
	plain := RenderTrends(m)
	for _, want := range []string{"calendar gap", "0.0 USD n=3", "incomplete", "partial=1", "age-immature", "evidence=3/10"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %s: %s", want, plain)
		}
	}
	if strings.Contains(plain, "n=0") {
		t.Fatal(plain)
	}
	m.Profile = theme.ProfileANSI256
	colored := RenderTrends(m)
	if !strings.Contains(colored, "\x1b[") {
		t.Fatal("no palette")
	}
	for _, line := range strings.Split(colored, "\n") {
		if cells.Width(line) > m.Width {
			t.Fatal(line)
		}
	}
	m.Height = 3
	m.ScrollOffset = 999
	scrolled := RenderTrends(m)
	if strings.Contains(scrolled, "Trends ·") || len(strings.Split(scrolled, "\n")) != 3 {
		t.Fatal(scrolled)
	}
	m.Cohort = "claude"
	m.Height = 40
	m.ScrollOffset = 0
	if !strings.Contains(RenderTrends(m), "no monthly evidence") {
		t.Fatal("empty cohort")
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		metric.Median.Median = v
		s, _, ok := trendValue(metric)
		if ok || strings.Contains(s, "NaN") || strings.Contains(s, "Inf") {
			t.Fatal(s)
		}
	}
	metric.Median.Median = 4
	metric.Key = "median_hours_to_merge"
	s, _, _ := trendValue(metric)
	if !strings.Contains(s, "4.0 h") {
		t.Fatal(s)
	}
}
