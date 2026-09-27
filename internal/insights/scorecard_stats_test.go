package insights

import (
	"math"
	"reflect"
	"slices"
	"testing"
)

func TestEstimateRate(t *testing.T) {
	tests := []struct {
		name                            string
		numerator, denominator, minimum int
		want                            RateEstimate
	}{
		{"empty", 0, 0, 5, RateEstimate{Sparse: true}},
		{"empty without minimum", 0, 0, 0, RateEstimate{Sparse: true}},
		{"no successes", 0, 10, 5, RateEstimate{Denominator: 10, Lower: 0, Upper: 0.277532800}},
		{"all successes", 10, 10, 5, RateEstimate{Numerator: 10, Denominator: 10, Rate: 1, Lower: 0.722467200, Upper: 1}},
		{"half", 5, 10, 5, RateEstimate{Numerator: 5, Denominator: 10, Rate: 0.5, Lower: 0.236593090, Upper: 0.763406910}},
		{"boundary", 1, 5, 5, RateEstimate{Numerator: 1, Denominator: 5, Rate: 0.2, Lower: 0.036224109, Upper: 0.624465370}},
		{"sparse", 1, 5, 6, RateEstimate{Numerator: 1, Denominator: 5, Rate: 0.2, Lower: 0.036224109, Upper: 0.624465370, Sparse: true}},
		{"negative numerator", -1, 10, 5, RateEstimate{Denominator: 10, Upper: 0.277532800}},
		{"excess numerator", 11, 10, 5, RateEstimate{Numerator: 10, Denominator: 10, Rate: 1, Lower: 0.722467200, Upper: 1}},
		{"negative denominator", 3, -10, 5, RateEstimate{Sparse: true}},
		{"negative inputs", -3, -10, 5, RateEstimate{Sparse: true}},
		{"zero denominator with successes", 3, 0, 5, RateEstimate{Sparse: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateRate(tt.numerator, tt.denominator, tt.minimum)
			if got.Numerator != tt.want.Numerator || got.Denominator != tt.want.Denominator || got.Sparse != tt.want.Sparse {
				t.Errorf("EstimateRate() = %+v, want %+v", got, tt.want)
			}
			for name, pair := range map[string][2]float64{"rate": {got.Rate, tt.want.Rate}, "lower": {got.Lower, tt.want.Lower}, "upper": {got.Upper, tt.want.Upper}} {
				if math.IsNaN(pair[0]) || math.Abs(pair[0]-pair[1]) > 1e-6 {
					t.Errorf("%s = %.9f, want %.9f", name, pair[0], pair[1])
				}
			}
			if got.Lower < 0 || got.Upper > 1 || got.Lower > got.Rate || got.Upper < got.Rate {
				t.Errorf("unbounded interval: %+v", got)
			}
		})
	}
}

func TestEstimateMedian(t *testing.T) {
	tests := []struct {
		name    string
		values  []float64
		minimum int
		want    MedianEstimate
	}{
		{"nil", nil, 0, MedianEstimate{Sparse: true}},
		{"empty", []float64{}, 5, MedianEstimate{Sparse: true}},
		{"odd boundary", []float64{9, 1, 5}, 3, MedianEstimate{Samples: 3, Median: 5}},
		{"even nearest rank", []float64{9, 1, 5, 3}, 5, MedianEstimate{Samples: 4, Median: 3, Sparse: true}},
		{"singleton", []float64{2.5}, 1, MedianEstimate{Samples: 1, Median: 2.5}},
		{"negative and repeated", []float64{1.5, -2.5, -2.5}, 2, MedianEstimate{Samples: 3, Median: -2.5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := slices.Clone(tt.values)
			if got := EstimateMedian(tt.values, tt.minimum); got != tt.want {
				t.Errorf("EstimateMedian() = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.values, before) {
				t.Errorf("mutated input: %v, was %v", tt.values, before)
			}
		})
	}
}

func TestDetectInversionsSimpson(t *testing.T) {
	// Treatment A succeeds more often for both small and large stones, but
	// receives more difficult cases and has a lower aggregate success rate.
	global := []ComparisonPoint{{"b", 289.0 / 350, 350}, {"a", 273.0 / 350, 350}}
	strata := []StratumComparison{
		{Stratum: "size", Key: "small", Points: []ComparisonPoint{{"a", 81.0 / 87, 87}, {"b", 234.0 / 270, 270}}},
		{Stratum: "size", Key: "large", Points: []ComparisonPoint{{"b", 55.0 / 80, 80}, {"a", 192.0 / 263, 263}}},
	}
	want := []Inversion{
		{Metric: "success", CohortA: "a", CohortB: "b", Stratum: "size", Key: "large", GlobalDelta: global[1].Value - global[0].Value, StratumDelta: strata[1].Points[1].Value - strata[1].Points[0].Value},
		{Metric: "success", CohortA: "a", CohortB: "b", Stratum: "size", Key: "small", GlobalDelta: global[1].Value - global[0].Value, StratumDelta: strata[0].Points[0].Value - strata[0].Points[1].Value},
	}
	if got := DetectInversions("success", global, strata, 5); !reflect.DeepEqual(got, want) {
		t.Errorf("DetectInversions() = %+v, want %+v", got, want)
	}
}

func TestDetectInversionsEligibility(t *testing.T) {
	tests := []struct {
		name   string
		global []ComparisonPoint
		points []ComparisonPoint
		count  int
	}{
		{"boundary", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, []ComparisonPoint{{"a", 1, 5}, {"b", 2, 5}}, 1},
		{"sparse global a", []ComparisonPoint{{"a", 2, 4}, {"b", 1, 5}}, []ComparisonPoint{{"a", 1, 5}, {"b", 2, 5}}, 0},
		{"sparse global b", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 4}}, []ComparisonPoint{{"a", 1, 5}, {"b", 2, 5}}, 0},
		{"sparse stratum a", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, []ComparisonPoint{{"a", 1, 4}, {"b", 2, 5}}, 0},
		{"sparse stratum b", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, []ComparisonPoint{{"a", 1, 5}, {"b", 2, 4}}, 0},
		{"global tie", []ComparisonPoint{{"a", 1, 5}, {"b", 1, 5}}, []ComparisonPoint{{"a", 1, 5}, {"b", 2, 5}}, 0},
		{"stratum tie", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, []ComparisonPoint{{"a", 1, 5}, {"b", 1, 5}}, 0},
		{"same direction", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, 0},
		{"missing cohort", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, []ComparisonPoint{{"a", 1, 5}, {"c", 2, 5}}, 0},
		{"self comparison", []ComparisonPoint{{"a", 2, 5}, {"a", 1, 5}}, []ComparisonPoint{{"a", 1, 5}, {"a", 2, 5}}, 0},
		{"empty global", nil, []ComparisonPoint{{"a", 1, 5}, {"b", 2, 5}}, 0},
		{"empty stratum", []ComparisonPoint{{"a", 2, 5}, {"b", 1, 5}}, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectInversions("success", tt.global, []StratumComparison{{Stratum: "risk", Key: "high", Points: tt.points}}, 5)
			if len(got) != tt.count {
				t.Errorf("DetectInversions() = %+v, want %d inversions", got, tt.count)
			}
		})
	}
}

func TestDetectInversionsOrdering(t *testing.T) {
	global := []ComparisonPoint{{"c", 3, 10}, {"a", 1, 10}, {"b", 2, 10}}
	points := []ComparisonPoint{{"b", 2, 10}, {"c", 1, 10}, {"a", 3, 10}}
	strata := []StratumComparison{
		{Stratum: "risk", Key: "low", Points: slices.Clone(points)},
		{Stratum: "repo", Key: "z", Points: slices.Clone(points)},
		{Stratum: "repo", Key: "a", Points: slices.Clone(points)},
	}
	var want []Inversion
	for _, pair := range []struct {
		a, b  string
		delta float64
	}{{"a", "b", -1}, {"a", "c", -2}, {"b", "c", -1}} {
		for _, stratum := range []struct{ name, key string }{{"repo", "a"}, {"repo", "z"}, {"risk", "low"}} {
			want = append(want, Inversion{Metric: "success", CohortA: pair.a, CohortB: pair.b, Stratum: stratum.name, Key: stratum.key, GlobalDelta: pair.delta, StratumDelta: -pair.delta})
		}
	}
	before := slices.Clone(global)
	if got := DetectInversions("success", global, strata, 5); !reflect.DeepEqual(got, want) {
		t.Errorf("DetectInversions() = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(global, before) || strata[0].Stratum != "risk" || strata[1].Key != "z" {
		t.Fatal("mutated input order")
	}
	for _, stratum := range strata {
		if !reflect.DeepEqual(stratum.Points, points) {
			t.Fatal("mutated stratum points")
		}
	}
	slices.Reverse(global)
	slices.Reverse(strata)
	for i := range strata {
		slices.Reverse(strata[i].Points)
	}
	if got := DetectInversions("success", global, strata, 5); !reflect.DeepEqual(got, want) {
		t.Errorf("shuffled DetectInversions() = %+v, want %+v", got, want)
	}
}
