package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

// TrendsCollector is optional, read-only evidence collection, never execution authority.
type TrendsCollector interface {
	Collect(context.Context) (TrendsSnapshot, error)
}

type TrendsSnapshot struct {
	RepositoryName               string
	AsOf                         time.Time
	MaturityDays, MinimumSamples int
	Coverage                     TrendCoverage
	Buckets                      []TrendBucket
	Unavailable                  string
}
type TrendCoverage struct {
	Plans, Terminal, Eligible, Immature, Active, NeverStarted                             int
	MissingEvidence, Unbucketed, UnknownRuntime, AmbiguousRuntime                         int
	EventEvidence, ReviewEvidence, MalformedHistory, SkippedPlans, UnreadableRepositories int
}
type TrendBucket struct {
	Month, Cohort            string
	Provisional, AgeImmature bool
	Coverage                 TrendCoverage
	Metrics                  []TrendMetric
}
type TrendMetric struct {
	Key, Direction, Availability, Reason         string
	EligiblePlans, EvidencePlans, PartialSamples int
	Rate                                         *TrendRate
	Median                                       *TrendMedian
	Comparison                                   string
}
type TrendRate struct {
	Numerator, Denominator int
	Rate, Lower, Upper     float64
	Sparse                 bool
}
type TrendMedian struct {
	Samples int
	Median  float64
	Sparse  bool
}
type TrendsModel struct {
	Snapshot                    TrendsSnapshot
	Cohort                      string
	Width, Height, ScrollOffset int
	Theme                       theme.Theme
	Profile                     theme.Profile
	Loading, Stale              bool
	Failure                     string
}

// RenderTrends renders a bounded, scrollable page body. It never estimates or
// compares evidence: all comparisons come from the monthly projection.
func RenderTrends(m TrendsModel) string {
	if m.Width <= 0 || m.Height <= 0 {
		return ""
	}
	p := m.Theme.Palette(m.Profile)
	var lines []string
	add := func(s string, role theme.Role) {
		lines = append(lines, p.Paint(role, cells.Truncate(singleLineDetail(s), m.Width)))
	}
	cohort := m.Cohort
	if cohort == "" {
		cohort = "overall"
	}
	add("Trends · monthly · "+cohort+" · "+m.Snapshot.RepositoryName, theme.RoleDetailPrimary)
	observed := "unavailable"
	if !m.Snapshot.AsOf.IsZero() {
		observed = m.Snapshot.AsOf.UTC().Format(time.RFC3339)
	}
	add("Observed "+observed, theme.RoleDetailMuted)
	if m.Loading {
		add("Loading trends…", theme.RoleDetailMuted)
	}
	if m.Stale || m.Failure != "" {
		add("Stale / collection failed: "+m.Failure, theme.RoleDetailWarning)
	}
	if m.Snapshot.Unavailable != "" {
		add("Unavailable: "+m.Snapshot.Unavailable, theme.RoleDetailMuted)
	}
	c := m.Snapshot.Coverage
	add(fmt.Sprintf("Plans %d · eligible %d · missing %d · skipped %d", c.Plans, c.Eligible, c.MissingEvidence, c.SkippedPlans), theme.RoleDetailMuted)
	add("Provisional: unfinished month; comparisons withheld", theme.RoleDetailMuted)
	add("Age-immature: not yet eligible for outcome metrics", theme.RoleDetailMuted)
	var buckets []TrendBucket
	for _, b := range m.Snapshot.Buckets {
		if b.Cohort == cohort {
			buckets = append(buckets, b)
		}
	}
	if len(buckets) == 0 {
		add("Unavailable: no monthly evidence for this cohort", theme.RoleDetailMuted)
	} else {
		// Keep the projection's metric order, including unavailable metrics.
		latest := buckets[len(buckets)-1]
		for i, metric := range latest.Metrics {
			add(strings.ReplaceAll(metric.Key, "_", " ")+" (good: "+metric.Direction+")", theme.RoleDetailPrimary)
			// Latest value and sample count take priority over historical columns.
			value, _, _ := trendValue(metric)
			comparison, role := trendComparison(metric, latest.Provisional)
			suffix := ""
			if latest.Provisional {
				suffix += " provisional"
				role = theme.RoleDetailMuted
			}
			if latest.AgeImmature {
				suffix += " age-immature"
			}
			add(value+" "+comparison+suffix, role)
			add(latest.Month+" · "+trendEvidence(metric), theme.RoleDetailMuted)
			history, spark := trendHistory(buckets, i, m.Width)
			add(history, theme.RoleDetailMuted)
			add(spark, theme.RoleDetailMuted)
		}
	}
	offset := min(max(m.ScrollOffset, 0), max(len(lines)-m.Height, 0))
	return strings.Join(lines[offset:min(offset+m.Height, len(lines))], "\n")
}

func trendValue(m TrendMetric) (string, float64, bool) {
	n := m.EvidencePlans
	v := 0.0
	unit := ""
	sparse := false
	if m.Rate != nil {
		n = m.Rate.Denominator
		v = m.Rate.Rate * 100
		unit = "%"
		sparse = m.Rate.Sparse
	} else if m.Median != nil {
		n = m.Median.Samples
		v = m.Median.Median
		sparse = m.Median.Sparse
		if m.Key == "median_cost_per_plan" {
			unit = " USD"
		} else {
			unit = " h"
		}
	}
	valid := m.Availability != "unavailable" && (m.Rate != nil || m.Median != nil) && !math.IsNaN(v) && !math.IsInf(v, 0)
	value := "—"
	if valid {
		value = fmt.Sprintf("%.1f%s", v, unit)
	}
	value += fmt.Sprintf(" n=%d", n)
	if sparse {
		value += " sparse"
	}
	if m.Availability == "incomplete" {
		value += " incomplete"
	}
	if m.PartialSamples > 0 {
		value += fmt.Sprintf(" partial=%d", m.PartialSamples)
	}
	return value, v, valid
}
func trendEvidence(m TrendMetric) string {
	s := fmt.Sprintf("%s evidence=%d/%d", m.Availability, m.EvidencePlans, m.EligiblePlans)
	if m.PartialSamples > 0 {
		s += fmt.Sprintf(" partial=%d", m.PartialSamples)
	}
	if m.Reason != "" {
		s += " " + m.Reason
	}
	if m.Rate != nil && !math.IsNaN(m.Rate.Lower) && !math.IsNaN(m.Rate.Upper) && !math.IsInf(m.Rate.Lower, 0) && !math.IsInf(m.Rate.Upper, 0) {
		s += fmt.Sprintf(" CI95 %.1f–%.1f%%", 100*m.Rate.Lower, 100*m.Rate.Upper)
	}
	return s
}
func trendComparison(m TrendMetric, provisional bool) (string, theme.Role) {
	_, _, valid := trendValue(m)
	if provisional {
		return "comparison withheld", theme.RoleDetailMuted
	}
	if valid && m.Availability == "available" && m.Rate != nil && !m.Rate.Sparse && (m.Direction == "higher" || m.Direction == "lower") {
		if m.Comparison == "better" || m.Comparison == "worse" {
			up := (m.Comparison == "better") == (m.Direction == "higher")
			arrow := "↓"
			if up {
				arrow = "↑"
			}
			role := theme.RoleDetailSuccess
			if m.Comparison == "worse" {
				role = theme.RoleDetailWarning
			}
			return arrow + " " + m.Comparison, role
		}
	}
	if m.Comparison == "inconclusive" {
		return "inconclusive", theme.RoleDetailMuted
	}
	return "comparison unavailable", theme.RoleDetailMuted
}
func trendHistory(bs []TrendBucket, index, width int) (string, string) {
	// Build only the newest columns that can fit. Missing calendar months are
	// explicit gaps, not synthetic observations with n=0.
	var cols, marks []string
	// Median glyphs use a neutral within-row scale for display only, never a
	// comparison or a claim about direction. Rates retain their fixed 0–100 scale.
	maximum := 0.0
	for _, b := range bs {
		if index < len(b.Metrics) {
			_, v, ok := trendValue(b.Metrics[index])
			if ok {
				maximum = max(maximum, v)
			}
		}
	}
	used := 0
	for j := len(bs) - 1; j >= 0; j-- {
		b := bs[j]
		if index >= len(b.Metrics) {
			continue
		}
		value, v, ok := trendValue(b.Metrics[index])
		col := b.Month + " " + value
		if b.Provisional {
			col += " provisional"
		}
		if b.AgeImmature {
			col += " age-immature"
		}
		if used+cells.Width(col)+3 > width {
			break
		}
		cols = append([]string{col}, cols...)
		used += cells.Width(col) + 3
		mark := "·"
		if ok {
			levels := []rune("▁▂▃▄▅▆▇█")
			if b.Metrics[index].Rate != nil {
				mark = string(levels[int(max(0, min(7, v*7/100)))])
			} else {
				level := 0
				if maximum > 0 {
					level = int(max(0, min(7, v/maximum*7)))
				}
				mark = string(levels[level])
			}
		}
		marks = append([]string{mark}, marks...)
		if j > 0 {
			current, e1 := time.Parse("2006-01", b.Month)
			prior, e2 := time.Parse("2006-01", bs[j-1].Month)
			if e1 == nil && e2 == nil && prior.AddDate(0, 1, 0).Before(current) {
				cols = append([]string{"[calendar gap]"}, cols...)
				marks = append([]string{"·"}, marks...)
				used += 17
			}
		}
	}
	return strings.Join(cols, " | "), "Recorded: " + strings.Join(marks, "")
}
