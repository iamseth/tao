package view

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/insights"
)

const trendPolicy = "Terminal plans aged at least maturity_days at UTC day boundaries; active and never-started plans are censored. Months are UTC creation months [start, next month)."
const trendLimitations = "Planner-runtime attribution is low-confidence observational evidence, not implementation-runtime certainty or causal winners. Recorded outcomes are not proof of absence; cost is recorded telemetry, not guaranteed full cost."
const trendComparisons = "Adjacent calendar month, same cohort only: rates require complete coverage, minimum samples and strictly non-overlapping Wilson 95% intervals for better/worse; otherwise inconclusive or unavailable (missing adjacent evidence/coverage). Medians have no directional verdict or interval."

type trendProjection struct {
	Scope       InsightsScope `json:"scope"`
	Policy      string        `json:"maturity_policy"`
	Limitations string        `json:"limitations"`
	Comparisons string        `json:"comparison_policy"`
	insights.TrendReport
}

func trendChoice(value, fallback string, allowed ...string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return fallback
}

// projectTrend copies only the dedicated measurements and closed-vocabulary labels.
// Never pass diagnostic strings or the containing Report to a serializer.
func projectTrend(t insights.TrendReport, scope InsightsScope) trendProjection {
	p := trendProjection{Scope: scope, Policy: trendPolicy, Limitations: trendLimitations, Comparisons: trendComparisons,
		TrendReport: insights.TrendReport{AsOf: t.AsOf.UTC(), MaturityDays: t.MaturityDays, MinimumSamples: t.MinimumSamples, Coverage: t.Coverage, Buckets: []insights.TrendBucket{}}}
	for _, b := range t.Buckets {
		month, err := time.Parse("2006-01", b.Month)
		if err != nil {
			b.Month = "unknown"
		} else {
			b.Month = month.Format("2006-01")
		}
		b.Cohort = trendChoice(b.Cohort, "unknown", "overall", "pi", "claude", "unknown", "ambiguous")
		metrics := make([]insights.TrendMetric, 0, len(b.Metrics))
		for _, m := range b.Metrics {
			m.Key = trendChoice(m.Key, "unknown", "first_review_approved", "any_rework", "any_verification_repair", "rework_stopped", "recorded_blocker", "median_cost_per_plan", "median_hours_to_approval", "median_hours_to_merge", "unattended_rate", "escaped_defects_per_plan")
			m.Direction = trendChoice(m.Direction, "unknown", "higher", "lower")
			m.Availability = trendChoice(m.Availability, "unavailable", "available", "incomplete", "unavailable")
			m.Reason = trendChoice(m.Reason, "", "recorded_evidence_missing", "recorded_measurement_missing", "intervention_attribution_missing", "defect_linkage_missing")
			m.Comparison = trendChoice(m.Comparison, "unavailable", "better", "worse", "inconclusive", "unavailable")
			if m.Rate != nil {
				r := *m.Rate
				if !trendFinite(r.Rate, r.Lower, r.Upper) {
					m.Rate = nil
					m.Availability = "unavailable"
					m.Comparison = "unavailable"
				} else {
					m.Rate = &r
				}
			}
			if m.Median != nil {
				median := *m.Median
				m.Comparison = "unavailable"
				if !trendFinite(median.Median) {
					m.Median = nil
					m.Availability = "unavailable"
				} else {
					m.Median = &median
				}
			}
			metrics = append(metrics, m)
		}
		b.Metrics = metrics
		p.Buckets = append(p.Buckets, b)
	}
	slices.SortStableFunc(p.Buckets, func(a, b insights.TrendBucket) int {
		return cmp.Or(cmp.Compare(a.Month, b.Month), cmp.Compare(a.Cohort, b.Cohort))
	})
	return p
}

func trendFinite(values ...float64) bool {
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// RenderInsightsTrendJSON emits only the safe trend projection and scope metadata.
func RenderInsightsTrendJSON(out io.Writer, trend insights.TrendReport, scope InsightsScope) error {
	if out == nil {
		return errors.New("insights output writer is required")
	}
	if scope != InsightsScopeRepository && scope != InsightsScopeAllRepositories {
		return fmt.Errorf("invalid insights scope %q", scope)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(projectTrend(trend, scope))
}

func renderInsightsTrend(out io.Writer, trend *insights.TrendReport, scope InsightsScope) error {
	var text strings.Builder
	fmt.Fprintf(&text, "# Tao Monthly Trends (%s)\n\n", scope)
	if trend == nil {
		text.WriteString("Trend data unavailable.\n")
	} else {
		p := projectTrend(*trend, scope)
		fmt.Fprintf(&text, "As of: %s; maturity_days: %d; minimum samples: %d\n%s\n%s\n%s\n\n## Coverage\n", p.AsOf.Format(time.RFC3339Nano), p.MaturityDays, p.MinimumSamples, p.Policy, p.Limitations, p.Comparisons)
		renderTrendCoverage(&text, p.Coverage)
		if len(p.Buckets) == 0 {
			text.WriteString("No monthly evidence.\n")
		}
		for _, b := range p.Buckets {
			fmt.Fprintf(&text, "\n## %s — planner-runtime: %s (provisional: %t; age-immature: %t)\n", b.Month, b.Cohort, b.Provisional, b.AgeImmature)
			renderTrendCoverage(&text, b.Coverage)
			text.WriteString("metric | direction | measurement (Wilson 95% for rates) | samples | eligible / evidence / partial | availability | comparison | reason\n")
			for _, m := range b.Metrics {
				measurement, samples := "unavailable", "unavailable"
				if m.Rate != nil {
					r := m.Rate
					measurement = fmt.Sprintf("%d/%d = %.2f%% [%.2f%%, %.2f%%]", r.Numerator, r.Denominator, 100*r.Rate, 100*r.Lower, 100*r.Upper)
					samples = fmt.Sprintf("%d", r.Denominator)
					if r.Sparse {
						samples += " (insufficient samples)"
					}
				}
				if m.Median != nil {
					measurement = fmt.Sprintf("median %.4g", m.Median.Median)
					samples = fmt.Sprintf("%d", m.Median.Samples)
					if m.Median.Sparse {
						samples += " (insufficient samples)"
					}
				}
				fmt.Fprintf(&text, "%s | %s | %s | %s | %d / %d / %d | %s | %s | %s\n", m.Key, m.Direction, measurement, samples, m.EligiblePlans, m.EvidencePlans, m.PartialSamples, m.Availability, m.Comparison, m.Reason)
			}
		}
	}
	_, err := io.WriteString(out, text.String())
	return err
}

func renderTrendCoverage(out *strings.Builder, c insights.TrendCoverage) {
	fmt.Fprintf(out, "plans: %d; terminal: %d; eligible: %d; immature: %d; active (censored): %d; never-started (censored): %d\n", c.Plans, c.Terminal, c.Eligible, c.Immature, c.Active, c.NeverStarted)
	fmt.Fprintf(out, "missing evidence: %d; unbucketed: %d; unknown attribution: %d; ambiguous attribution: %d; event evidence: %d; review evidence: %d; malformed history: %d; skipped plans: %d; unreadable repositories: %d\n", c.MissingEvidence, c.Unbucketed, c.UnknownRuntime, c.AmbiguousRuntime, c.EventEvidence, c.ReviewEvidence, c.MalformedHistory, c.SkippedPlans, c.UnreadableRepositories)
}
