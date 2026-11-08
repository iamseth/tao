package insights

import (
	"cmp"
	"context"
	"math"
	"slices"
	"time"
)

// TrendReport is a read-only projection of recorded outcomes, not causal evidence
// or proof of absence. Eligibility is independent of scorecard inactivity maturity.
type TrendReport struct {
	AsOf           time.Time     `json:"as_of"`
	MaturityDays   int           `json:"maturity_days"`
	MinimumSamples int           `json:"minimum_samples"`
	Coverage       TrendCoverage `json:"coverage"`
	Buckets        []TrendBucket `json:"buckets"`
}

// Coverage counts plans, never events. MissingEvidence can overlap other counts.
type TrendCoverage struct {
	Plans                  int `json:"plans"`
	Terminal               int `json:"terminal"`
	Eligible               int `json:"eligible"`
	Immature               int `json:"immature"`
	Active                 int `json:"active"`
	NeverStarted           int `json:"never_started"`
	MissingEvidence        int `json:"missing_evidence"`
	Unbucketed             int `json:"unbucketed"`
	UnknownRuntime         int `json:"unknown_runtime"`
	AmbiguousRuntime       int `json:"ambiguous_runtime"`
	EventEvidence          int `json:"event_evidence"`
	ReviewEvidence         int `json:"review_evidence"`
	MalformedHistory       int `json:"malformed_history"`
	SkippedPlans           int `json:"skipped_plans"`
	UnreadableRepositories int `json:"unreadable_repositories"`
}

type TrendBucket struct {
	Month       string        `json:"month"`
	Cohort      string        `json:"cohort"`
	Provisional bool          `json:"provisional"`
	AgeImmature bool          `json:"age_immature"`
	Coverage    TrendCoverage `json:"coverage"`
	Metrics     []TrendMetric `json:"metrics"`
}

// Optional estimates distinguish unavailable evidence from a measured zero.
// PartialSamples includes measured cost totals with known partial telemetry;
// even other cost samples describe recorded telemetry, not guaranteed full cost.
type TrendMetric struct {
	Key            string          `json:"key"`
	Direction      string          `json:"direction"`
	Availability   string          `json:"availability"`
	Reason         string          `json:"reason,omitempty"`
	EligiblePlans  int             `json:"eligible_plans"`
	EvidencePlans  int             `json:"evidence_plans"`
	PartialSamples int             `json:"partial_samples"`
	Rate           *RateEstimate   `json:"rate,omitempty"`
	Median         *MedianEstimate `json:"median,omitempty"`
	Comparison     string          `json:"comparison"`
}

func populateTrend(ctx context.Context, report *Report, acc *accumulator, options Options, now time.Time) error {
	if !options.IncludeTrend {
		return nil
	}
	trend, err := buildTrendContext(ctx, acc.trendObservations, now)
	if err != nil {
		return err
	}
	trend.Coverage.SkippedPlans = report.PlansSkipped
	trend.Coverage.UnreadableRepositories = report.RepositoryCoverage.Unreadable + report.RepositoryCoverage.Skipped
	report.Trend = &trend
	return ctx.Err()
}

func trendRuntime(o planObservation) string {
	if o.treatment.Source == "none" {
		return "unknown"
	}
	if o.treatment.Confidence == TreatmentConfidenceAmbiguous {
		return "ambiguous"
	}
	switch o.treatment.Runtime {
	case "pi", "claude":
		return o.treatment.Runtime
	}
	return "unknown"
}

func trendEligibility(o planObservation, now time.Time) (valid, old, eligible bool) {
	valid = !o.createdAt.IsZero() && !o.createdAt.After(now)
	old = valid && now.UTC().Truncate(24*time.Hour).Sub(o.createdAt.UTC().Truncate(24*time.Hour)) >= scorecardMaturityWindow
	eligible = old && o.class == observationTerminal
	return
}

func (c *TrendCoverage) add(o planObservation, now time.Time) {
	c.Plans++
	valid, old, eligible := trendEligibility(o, now)
	if !valid {
		c.Unbucketed++
	}
	if !valid || !o.historyPresent || o.historyMalformed {
		c.MissingEvidence++
	}
	if o.historyMalformed {
		c.MalformedHistory++
	}
	switch trendRuntime(o) {
	case "unknown":
		c.UnknownRuntime++
	case "ambiguous":
		c.AmbiguousRuntime++
	}
	switch o.class {
	case observationTerminal:
		c.Terminal++
		if valid && !old {
			c.Immature++
		}
	case observationActive:
		c.Active++
	case observationNeverStarted:
		c.NeverStarted++
	}
	if eligible {
		c.Eligible++
		if o.historyPresent {
			c.EventEvidence++
		}
		if o.firstReviewVerdict != "" {
			c.ReviewEvidence++
		}
	}
}

func buildTrendContext(ctx context.Context, observations []planObservation, now time.Time) (TrendReport, error) {
	now = now.UTC()
	report := TrendReport{AsOf: now, MaturityDays: 14, MinimumSamples: scorecardMinimumSamples, Buckets: []TrendBucket{}}
	type key struct{ month, cohort string }
	groups := make(map[key][]planObservation)
	for _, o := range observations {
		if err := ctx.Err(); err != nil {
			return TrendReport{}, err
		}
		report.Coverage.add(o, now)
		valid, _, _ := trendEligibility(o, now)
		if !valid {
			continue
		}
		month := o.createdAt.UTC().Format("2006-01")
		for _, cohort := range []string{"overall", trendRuntime(o)} {
			k := key{month, cohort}
			groups[k] = append(groups[k], o)
		}
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int { return cmp.Or(cmp.Compare(a.month, b.month), cmp.Compare(a.cohort, b.cohort)) })
	previous := make(map[key]int)
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return TrendReport{}, err
		}
		bucket := TrendBucket{Month: k.month, Cohort: k.cohort, Provisional: k.month == now.Format("2006-01")}
		var eligible []planObservation
		for _, o := range groups[k] {
			bucket.Coverage.add(o, now)
			_, old, ok := trendEligibility(o, now)
			bucket.AgeImmature = bucket.AgeImmature || !old
			if ok {
				eligible = append(eligible, o)
			}
		}
		bucket.Metrics = trendMetrics(eligible)
		month, _ := time.Parse("2006-01", k.month)
		prior := key{month.AddDate(0, -1, 0).Format("2006-01"), k.cohort}
		if index, ok := previous[prior]; ok {
			for i := range bucket.Metrics {
				bucket.Metrics[i].Comparison = compareTrendRates(report.Buckets[index].Metrics[i], bucket.Metrics[i])
			}
		}
		previous[k] = len(report.Buckets)
		report.Buckets = append(report.Buckets, bucket)
	}
	return report, ctx.Err()
}

func trendMetrics(observations []planObservation) []TrendMetric {
	keys := []string{"first_review_approved", "any_rework", "any_verification_repair", "rework_stopped", "recorded_blocker"}
	metrics := make([]TrendMetric, 0, 10)
	for i, key := range keys {
		m := TrendMetric{Key: key, Direction: "lower", EligiblePlans: len(observations), Availability: "unavailable", Comparison: "unavailable"}
		if i == 0 {
			m.Direction = "higher"
		}
		numerator, incomplete := 0, false
		for _, o := range observations {
			evidence := o.historyPresent
			if i == 0 {
				evidence = o.firstReviewVerdict != ""
			}
			if evidence {
				m.EvidencePlans++
			}
			incomplete = incomplete || o.historyMalformed
			values := []bool{o.firstReviewVerdict == "approve", o.recordedRework, o.verificationRepairs > 0, o.reworkStops > 0, o.recordedBlocker}
			if values[i] {
				numerator++
			}
		}
		if m.EvidencePlans > 0 || numerator > 0 {
			rate := EstimateRate(numerator, len(observations), scorecardMinimumSamples)
			m.Rate = &rate
			m.Availability = "available"
			if m.EvidencePlans < len(observations) || incomplete {
				m.Availability = "incomplete"
			}
		} else {
			m.Reason = "recorded_evidence_missing"
		}
		metrics = append(metrics, m)
	}
	for _, key := range []string{"median_cost_per_plan", "median_hours_to_approval", "median_hours_to_merge"} {
		m := TrendMetric{Key: key, Direction: "lower", EligiblePlans: len(observations), Availability: "unavailable", Comparison: "unavailable"}
		var values []float64
		for _, o := range observations {
			value, present, partial := 0.0, false, false
			switch key {
			case "median_cost_per_plan":
				partial = o.trendCostPartial
				roles := make([]string, 0, len(o.roles))
				for role := range o.roles {
					roles = append(roles, role)
				}
				slices.Sort(roles)
				for _, role := range roles {
					r := o.roles[role]
					if r.costPresent {
						if r.cost < 0 || math.IsNaN(r.cost) || math.IsInf(r.cost, 0) {
							value = math.NaN()
						}
						value += r.cost
						present = true
					}
					partial = partial || !r.costPresent || r.availability.Partial > 0 || r.availability.Unavailable > 0 || r.availability.Unknown > 0
				}
			case "median_hours_to_approval":
				present = !o.firstApprovalAt.IsZero()
				value = o.firstApprovalAt.Sub(o.createdAt).Hours()
			case "median_hours_to_merge":
				present = !o.mergeAt.IsZero()
				value = o.mergeAt.Sub(o.createdAt).Hours()
			}
			if present && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
				values = append(values, value)
				if partial {
					m.PartialSamples++
				}
			}
		}
		m.EvidencePlans = len(values)
		if len(values) > 0 {
			estimate := EstimateMedian(values, scorecardMinimumSamples)
			m.Median = &estimate
			m.Availability = "available"
			if len(values) < len(observations) || m.PartialSamples > 0 {
				m.Availability = "incomplete"
			}
		} else {
			m.Reason = "recorded_measurement_missing"
		}
		metrics = append(metrics, m)
	}
	for _, key := range []string{"unattended_rate", "escaped_defects_per_plan"} {
		reason, direction := "intervention_attribution_missing", "higher"
		if key == "escaped_defects_per_plan" {
			reason, direction = "defect_linkage_missing", "lower"
		}
		metrics = append(metrics, TrendMetric{Key: key, Direction: direction, Availability: "unavailable", Reason: reason, EligiblePlans: len(observations), Comparison: "unavailable"})
	}
	return metrics
}

func compareTrendRates(previous, current TrendMetric) string {
	if previous.Availability != "available" || current.Availability != "available" || previous.Rate == nil || current.Rate == nil {
		return "unavailable"
	}
	a, b := previous.Rate, current.Rate
	if a.Sparse || b.Sparse {
		return "inconclusive"
	}
	higher, lower := b.Lower > a.Upper, b.Upper < a.Lower
	if !higher && !lower {
		return "inconclusive"
	}
	if higher == (current.Direction == "higher") {
		return "better"
	}
	return "worse"
}
