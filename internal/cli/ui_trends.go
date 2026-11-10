package cli

import (
	"context"
	"errors"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/tui"
)

type uiTrendsCollector struct{ app App }

func newUITrendsCollector(a App) tui.TrendsCollector { return uiTrendsCollector{app: a} }
func (c uiTrendsCollector) Collect(ctx context.Context) (tui.TrendsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tui.TrendsSnapshot{}, err
	}
	registry := c.app.registry()
	repo, err := registry.Current(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tui.TrendsSnapshot{}, err
		}
		return tui.TrendsSnapshot{Unavailable: "current repository registration unavailable: " + err.Error()}, nil
	}
	report, err := insights.AggregateWithOptions(ctx, c.app.repository(registry.PlansDir(repo)), insights.Options{IncludeTrend: true, Now: c.app.now()})
	if err != nil {
		return tui.TrendsSnapshot{}, err
	}
	if report.Trend == nil {
		return tui.TrendsSnapshot{RepositoryName: repo.Name, Unavailable: "no monthly projection"}, nil
	}
	return uiTrendsSnapshot(*report.Trend, repo.Name), nil
}

// uiTrendsSnapshot copies the projection, including optional estimates, without
// retaining mutable domain pointers or deriving presentation statistics.
func uiTrendsSnapshot(r insights.TrendReport, repositoryName string) tui.TrendsSnapshot {
	s := tui.TrendsSnapshot{RepositoryName: repositoryName, AsOf: r.AsOf, MaturityDays: r.MaturityDays, MinimumSamples: r.MinimumSamples, Coverage: tui.TrendCoverage(r.Coverage)}
	for _, b := range r.Buckets {
		bucket := tui.TrendBucket{Month: b.Month, Cohort: b.Cohort, Provisional: b.Provisional, AgeImmature: b.AgeImmature, Coverage: tui.TrendCoverage(b.Coverage)}
		for _, m := range b.Metrics {
			metric := tui.TrendMetric{Key: m.Key, Direction: m.Direction, Availability: m.Availability, Reason: m.Reason, EligiblePlans: m.EligiblePlans, EvidencePlans: m.EvidencePlans, PartialSamples: m.PartialSamples, Comparison: m.Comparison}
			if m.Rate != nil {
				v := tui.TrendRate(*m.Rate)
				metric.Rate = &v
			}
			if m.Median != nil {
				v := tui.TrendMedian(*m.Median)
				metric.Median = &v
			}
			bucket.Metrics = append(bucket.Metrics, metric)
		}
		s.Buckets = append(s.Buckets, bucket)
	}
	return s
}
