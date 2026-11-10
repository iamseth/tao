package tuipreview

import (
	"context"
	"strings"

	"github.com/iamseth/tao/internal/tui"
)

// TrendsCollector holds only synthetic, immutable evidence, never repository paths.
type TrendsCollector struct{ snapshot tui.TrendsSnapshot }

func NewTrendsCollector(snapshot tui.TrendsSnapshot) *TrendsCollector {
	return &TrendsCollector{snapshot: cloneTrends(snapshot)}
}

func (c *TrendsCollector) Collect(ctx context.Context) (tui.TrendsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tui.TrendsSnapshot{}, err
	}
	return cloneTrends(c.snapshot), nil
}

func cloneTrends(s tui.TrendsSnapshot) tui.TrendsSnapshot {
	s.Buckets = append([]tui.TrendBucket(nil), s.Buckets...)
	for i := range s.Buckets {
		b := &s.Buckets[i]
		b.Metrics = append([]tui.TrendMetric(nil), b.Metrics...)
		for j := range b.Metrics {
			m := &b.Metrics[j]
			if m.Rate != nil {
				v := *m.Rate
				m.Rate = &v
			}
			if m.Median != nil {
				v := *m.Median
				m.Median = &v
			}
		}
	}
	return s
}

func trendsFixture(stress bool) tui.TrendsSnapshot {
	s := tui.TrendsSnapshot{RepositoryName: "preview", AsOf: fixtureNow, MaturityDays: 14, MinimumSamples: 5,
		Coverage: tui.TrendCoverage{Plans: 90, Eligible: 60, MissingEvidence: 4, SkippedPlans: 2}}
	if stress {
		s.RepositoryName = strings.Repeat("長いリポジトリ 🌱 e\u0301 · ", 12)
	}
	// Overall is closed-month evidence; Pi includes a provisional, age-immature
	// month. Claude deliberately has no buckets. May is absent, not zero.
	for _, cohort := range []string{"overall", "pi"} {
		for i, month := range []string{"2026-04", "2026-06", "2026-07"} {
			b := tui.TrendBucket{Month: month, Cohort: cohort, Coverage: s.Coverage}
			b.Metrics = []tui.TrendMetric{
				{Key: "completion_rate", Direction: "higher", Availability: "available", EligiblePlans: 20, EvidencePlans: 20, Comparison: "better", Rate: &tui.TrendRate{Numerator: 16 + i, Denominator: 20, Rate: .8 + float64(i)*.05, Lower: .6, Upper: .98}},
				{Key: "rework_rate", Direction: "lower", Availability: "available", EligiblePlans: 20, EvidencePlans: 20, Comparison: "worse", Rate: &tui.TrendRate{Numerator: 4 + i, Denominator: 20, Rate: .2 + float64(i)*.05, Lower: .1, Upper: .5}},
				{Key: "failure_rate", Direction: "lower", Availability: "incomplete", EligiblePlans: 20, EvidencePlans: 3, PartialSamples: 2, Reason: "missing event evidence", Comparison: "inconclusive", Rate: &tui.TrendRate{Numerator: 1, Denominator: 3, Rate: 1.0 / 3, Lower: .06, Upper: .79, Sparse: true}},
				{Key: "median_cost_per_plan", Direction: "lower", Availability: "available", EligiblePlans: 20, EvidencePlans: 3, Median: &tui.TrendMedian{Samples: 3, Median: 1.25, Sparse: true}},
				{Key: "median_completion_hours", Direction: "lower", Availability: "unavailable", EligiblePlans: 20, Reason: "missing duration evidence"},
			}
			if stress {
				b.Metrics[4].Reason = strings.Repeat("不完全な証拠 🧪 e\u0301 ", 20)
			}
			s.Buckets = append(s.Buckets, b)
		}
		if cohort == "pi" {
			b := cloneTrends(tui.TrendsSnapshot{Buckets: s.Buckets[len(s.Buckets)-1:]}).Buckets[0]
			b.Month, b.Provisional, b.AgeImmature = "2026-08", true, true
			s.Buckets = append(s.Buckets, b)
		}
	}
	return s
}
