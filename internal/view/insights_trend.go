package view

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
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
	if t.Defects != nil {
		d := projectDefects(*t.Defects)
		p.Defects = &d
	}
	for _, d := range t.DefectRepositories {
		p.DefectRepositories = append(p.DefectRepositories, projectDefects(d))
	}
	slices.SortStableFunc(p.DefectRepositories, func(a, b insights.DefectReport) int { return cmp.Compare(a.RepositoryID, b.RepositoryID) })
	for _, b := range t.Buckets {
		b.DefectMissing = max(0, b.DefectMissing)
		b.DefectImmature = max(0, b.DefectImmature)
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
			m.Reason = trendChoice(m.Reason, "", "recorded_evidence_missing", "recorded_measurement_missing", "intervention_attribution_missing", "defect_linkage_missing", "observed_lower_bound")
			m.Comparison = trendChoice(m.Comparison, "unavailable", "better", "worse", "inconclusive", "unavailable")
			if m.Key == "escaped_defects_per_plan" {
				m.Rate = nil
				m.Median = nil
				m.Comparison = "unavailable"
			} else {
				m.Count = nil
			}
			if m.Count != nil {
				m.Comparison = "unavailable"
				m.Direction = "unknown"
				m.Rate = nil
				m.Median = nil
				c := *m.Count
				if c.Numerator < 0 || c.Denominator <= 0 || c.Quotient < 0 || !trendFinite(c.Quotient) {
					m.Count = nil
					m.Availability = "unavailable"
				} else {
					c.Quotient = float64(c.Numerator) / float64(c.Denominator)
					m.Count = &c
				}
			}
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

var safeDefectID = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-z0-9]+(-[a-z0-9]+)*$`)
var safeDefectSHA = regexp.MustCompile(`^[a-fA-F0-9]{40}([a-fA-F0-9]{24})?$`)
var safeDefectRepository = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func defectSHA(s string) string {
	if safeDefectSHA.MatchString(s) {
		return strings.ToLower(s)
	}
	return ""
}
func defectAvailability(a insights.DefectAvailability) insights.DefectAvailability {
	return insights.DefectAvailability(trendChoice(string(a), "unavailable", "available", "incomplete", "unavailable", "unknown", "immature"))
}
func projectDefectDetails(raw []insights.DefectAttribution) []insights.DefectAttribution {
	var out []insights.DefectAttribution
	for _, d := range raw {
		sha := defectSHA(d.FixSHA)
		if sha == "" {
			continue
		}
		confidence := insights.DefectLowConfidence
		switch d.Method {
		case insights.DefectExplicit:
			confidence = insights.DefectAssertion
		case insights.DefectLine, insights.DefectFile:
		default:
			continue
		}
		out = append(out, insights.DefectAttribution{FixSHA: sha, CommitterAt: d.CommitterAt.UTC(), Method: d.Method, Confidence: confidence})
	}
	slices.SortFunc(out, func(a, b insights.DefectAttribution) int {
		return cmp.Or(a.CommitterAt.Compare(b.CommitterAt), cmp.Compare(a.FixSHA, b.FixSHA), cmp.Compare(a.Method, b.Method))
	})
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}
func projectDefects(raw insights.DefectReport) insights.DefectReport {
	d := insights.DefectReport{ObservedAt: raw.ObservedAt.UTC(), Availability: defectAvailability(raw.Availability)}
	if safeDefectRepository.MatchString(raw.RepositoryID) {
		d.RepositoryID = raw.RepositoryID
	}
	s := raw.Coverage.Scan
	d.Coverage.Scan = insights.DefectScan{Usable: s.Usable, Complete: s.Complete, PinnedTip: defectSHA(s.PinnedTip), Truncated: s.Truncated, TimedOut: s.TimedOut, MissingHistory: s.MissingHistory}
	c := raw.Coverage
	d.Coverage.Examined = max(0, c.Examined)
	d.Coverage.Attributed = max(0, c.Attributed)
	d.Coverage.Untagged = max(0, c.Untagged)
	d.Coverage.Unattributed = max(0, c.Unattributed)
	d.Coverage.Unsupported = max(0, c.Unsupported)
	d.Coverage.InvalidReferences = max(0, c.InvalidReferences)
	d.Coverage.FutureFixes = max(0, c.FutureFixes)
	d.Coverage.Attribution = safeDefectCount(c.Attribution)
	d.Count = safeDefectCount(raw.Count)
	if raw.Count.Numerator < 0 || raw.Count.Denominator < 0 || raw.Count.Quotient < 0 || !trendFinite(raw.Count.Quotient) {
		d.Availability = insights.DefectUnavailable
	}
	for _, p := range raw.Plans {
		if !safeDefectID.MatchString(p.ID) || len(p.ID) > 256 {
			continue
		}
		v := insights.DefectPlanResult{ID: p.ID, MergeAt: p.MergeAt.UTC(), Mature: p.Mature, Availability: defectAvailability(p.Availability), WindowFixes: max(0, p.WindowFixes)}
		if month, err := time.Parse("2006-01", p.CreationMonth); err == nil {
			v.CreationMonth = month.Format("2006-01")
		}
		v.Planner = insights.NormalizePlannerLabel(trendChoice(p.Planner.Runtime, "unknown", "pi", "claude"))
		v.Planner.Confidence = insights.TreatmentConfidence(trendChoice(string(p.Planner.Confidence), "ambiguous", "high", "low", "ambiguous"))
		v.Methods = insights.DefectMethods{Explicit: max(0, p.Methods.Explicit), Line: max(0, p.Methods.Line), File: max(0, p.Methods.File)}
		if p.WindowFixes < 0 {
			v.Availability = insights.DefectUnknown
		}
		v.Fixes = projectDefectDetails(p.Fixes)
		v.LaterFixes = projectDefectDetails(p.LaterFixes)
		d.Plans = append(d.Plans, v)
	}
	slices.SortStableFunc(d.Plans, func(a, b insights.DefectPlanResult) int { return cmp.Compare(a.ID, b.ID) })
	if len(d.Plans) > 1000 {
		d.Plans = d.Plans[:1000]
	}
	return d
}
func safeDefectCount(c insights.DefectCount) insights.DefectCount {
	if c.Numerator < 0 || c.Denominator <= 0 || c.Quotient < 0 || !trendFinite(c.Quotient) {
		return insights.DefectCount{}
	}
	return insights.DefectCount{Numerator: c.Numerator, Denominator: c.Denominator, Quotient: float64(c.Numerator) / float64(c.Denominator)}
}
func renderDefects(out *strings.Builder, d insights.DefectReport) {
	fmt.Fprintf(out, "\n### Observed escaped defects — %s\nAs of: %s; pinned tip: %s; coverage: %s; scan complete: %t; truncated: %t; timed out: %t; missing history: %t\n", d.RepositoryID, d.ObservedAt.Format(time.RFC3339Nano), d.Coverage.Scan.PinnedTip, d.Availability, d.Coverage.Scan.Complete, d.Coverage.Scan.Truncated, d.Coverage.Scan.TimedOut, d.Coverage.Scan.MissingHistory)
	out.WriteString("Elapsed 30-day post-merge window; incomplete counts are observed lower bounds. Explicit links are assertions; line/file attribution is low confidence, not proof of causation. Detail lists are capped at 100 fixes and 1000 plans.\n")
	fmt.Fprintf(out, "Fix coverage: examined %d; attributed %d; untagged %d; unattributed %d; unsupported %d; invalid references %d; future fixes %d\n", d.Coverage.Examined, d.Coverage.Attributed, d.Coverage.Untagged, d.Coverage.Unattributed, d.Coverage.Unsupported, d.Coverage.InvalidReferences, d.Coverage.FutureFixes)
	for _, p := range d.Plans {
		fmt.Fprintf(out, "- %s: merged %s; mature %t; %s; window fixes %d\n", p.ID, p.MergeAt.Format(time.RFC3339Nano), p.Mature, p.Availability, p.WindowFixes)
		for _, group := range []struct {
			label string
			fixes []insights.DefectAttribution
		}{{"window", p.Fixes}, {"later", p.LaterFixes}} {
			for _, f := range group.fixes {
				fmt.Fprintf(out, "  - %s: %s at %s (%s, %s)\n", group.label, f.FixSHA, f.CommitterAt.Format(time.RFC3339Nano), f.Method, f.Confidence)
			}
		}
	}
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
		if p.Defects != nil {
			renderDefects(&text, *p.Defects)
		}
		for _, d := range p.DefectRepositories {
			renderDefects(&text, d)
		}
		if len(p.Buckets) == 0 {
			text.WriteString("No monthly evidence.\n")
		}
		for _, b := range p.Buckets {
			fmt.Fprintf(&text, "\n## %s — planner-runtime: %s (provisional: %t; age-immature: %t)\n", b.Month, b.Cohort, b.Provisional, b.AgeImmature)
			renderTrendCoverage(&text, b.Coverage)
			if b.DefectMissing > 0 || b.DefectImmature > 0 {
				fmt.Fprintf(&text, "Defect coverage: missing %d; immature %d (30-day window).\n", max(0, b.DefectMissing), max(0, b.DefectImmature))
			}
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
				if m.Count != nil {
					measurement = fmt.Sprintf("%d/%d = %.4g (observed count quotient)", m.Count.Numerator, m.Count.Denominator, m.Count.Quotient)
					samples = fmt.Sprint(m.Count.Denominator)
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
