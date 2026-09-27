package insights

import (
	"cmp"
	"math"
	"slices"
)

// RateEstimate reports a proportion and its Wilson 95% confidence interval.
type RateEstimate struct {
	Numerator   int     `json:"numerator"`
	Denominator int     `json:"denominator"`
	Rate        float64 `json:"rate"`
	Lower       float64 `json:"lower"`
	Upper       float64 `json:"upper"`
	Sparse      bool    `json:"sparse"`
}

// EstimateRate clamps counts to 0 <= numerator <= denominator before estimating.
func EstimateRate(numerator, denominator, minimumSamples int) RateEstimate {
	denominator = max(0, denominator)
	if denominator == 0 {
		return RateEstimate{Sparse: true}
	}
	numerator = min(max(0, numerator), denominator)
	const z = 1.959964
	n := float64(denominator)
	p := float64(numerator) / n
	divisor := 1 + z*z/n
	center := (p + z*z/(2*n)) / divisor
	margin := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / divisor
	lower, upper := max(0, center-margin), min(1, center+margin)
	// Preserve exact endpoints despite floating-point cancellation.
	if numerator == 0 {
		lower = 0
	}
	if numerator == denominator {
		upper = 1
	}
	return RateEstimate{
		Numerator:   numerator,
		Denominator: denominator,
		Rate:        p,
		Lower:       lower,
		Upper:       upper,
		Sparse:      denominator < minimumSamples,
	}
}

// MedianEstimate reports the nearest-rank median and sample coverage.
type MedianEstimate struct {
	Samples int     `json:"samples"`
	Median  float64 `json:"median"`
	Sparse  bool    `json:"sparse"`
}

// EstimateMedian sorts a clone so the caller's observations remain unchanged.
func EstimateMedian(values []float64, minimumSamples int) MedianEstimate {
	if len(values) == 0 {
		return MedianEstimate{Sparse: true}
	}
	values = slices.Clone(values)
	slices.Sort(values)
	return MedianEstimate{
		Samples: len(values),
		Median:  nearestRank(values, 0.50),
		Sparse:  len(values) < minimumSamples,
	}
}

// ComparisonPoint supplies one cohort's metric value and supporting sample size.
type ComparisonPoint struct {
	Cohort  string
	Value   float64
	Samples int
}

// StratumComparison groups cohort values for one stratum key.
type StratumComparison struct {
	Stratum string
	Key     string
	Points  []ComparisonPoint
}

// Inversion records opposite global and within-stratum directions for a pair.
// Both deltas are cohort A minus cohort B, with cohort names ordered lexically.
type Inversion struct {
	Metric       string  `json:"metric"`
	CohortA      string  `json:"cohort_a"`
	CohortB      string  `json:"cohort_b"`
	Stratum      string  `json:"stratum"`
	Key          string  `json:"key"`
	GlobalDelta  float64 `json:"global_delta"`
	StratumDelta float64 `json:"stratum_delta"`
}

// DetectInversions compares sufficiently sampled cohort pairs without assigning
// a preferred direction to the metric. Inputs contain one point per cohort.
func DetectInversions(metric string, global []ComparisonPoint, strata []StratumComparison, minimumSamples int) []Inversion {
	global = slices.Clone(global)
	slices.SortFunc(global, func(a, b ComparisonPoint) int { return cmp.Compare(a.Cohort, b.Cohort) })
	var inversions []Inversion
	for _, stratum := range strata {
		points := make(map[string]ComparisonPoint, len(stratum.Points))
		for _, point := range stratum.Points {
			points[point.Cohort] = point
		}
		for i, a := range global {
			localA, ok := points[a.Cohort]
			if !ok || a.Samples < minimumSamples || localA.Samples < minimumSamples {
				continue
			}
			for _, b := range global[i+1:] {
				localB, ok := points[b.Cohort]
				if !ok || a.Cohort == b.Cohort || b.Samples < minimumSamples || localB.Samples < minimumSamples {
					continue
				}
				globalDelta := a.Value - b.Value
				stratumDelta := localA.Value - localB.Value
				if (globalDelta > 0 && stratumDelta < 0) || (globalDelta < 0 && stratumDelta > 0) {
					inversions = append(inversions, Inversion{
						Metric: metric, CohortA: a.Cohort, CohortB: b.Cohort,
						Stratum: stratum.Stratum, Key: stratum.Key,
						GlobalDelta: globalDelta, StratumDelta: stratumDelta,
					})
				}
			}
		}
	}
	slices.SortFunc(inversions, func(a, b Inversion) int {
		return cmp.Or(
			cmp.Compare(a.Metric, b.Metric),
			cmp.Compare(a.CohortA, b.CohortA),
			cmp.Compare(a.CohortB, b.CohortB),
			cmp.Compare(a.Stratum, b.Stratum),
			cmp.Compare(a.Key, b.Key),
		)
	})
	return inversions
}
