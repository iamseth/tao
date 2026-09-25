package metrics

import (
	"encoding/json"
	"math"
	"testing"
)

func TestTokenValuePresence(t *testing.T) {
	for _, value := range []any{nil, "0", json.Number("bad"), json.Number("1.5"), -1, int64(-1), -1.0, 0.5, math.Exp2(63), math.NaN(), math.Inf(1)} {
		if n, ok := TokenValue(value); ok || n != 0 {
			t.Fatalf("TokenValue(%v) = %d, %t", value, n, ok)
		}
	}
	for _, value := range []any{0, int64(0), 0.0, json.Number("0")} {
		if n, ok := TokenValue(value, 3); !ok || n != 0 {
			t.Fatalf("TokenValue(%v, 3) = %d, %t", value, n, ok)
		}
	}
	if n, ok := TokenValue("invalid", json.Number("9223372036854775807")); !ok || n != math.MaxInt64 {
		t.Fatalf("valid fallback = %d, %t", n, ok)
	}
}

func TestCostValuePresence(t *testing.T) {
	for _, value := range []any{nil, "0", json.Number("bad"), -1, math.NaN(), math.Inf(1)} {
		if n, ok := CostValue(value); ok || n != 0 {
			t.Fatalf("CostValue(%v) = %g, %t", value, n, ok)
		}
	}
	for _, value := range []any{0, int64(0), float32(0), 0.0, json.Number("0")} {
		if n, ok := CostValue(value, 3); !ok || n != 0 {
			t.Fatalf("CostValue(%v, 3) = %g, %t", value, n, ok)
		}
	}
}

func TestComputeTotalRequiresSafeCompleteComponents(t *testing.T) {
	for _, tt := range []struct {
		name    string
		metrics Metrics
		want    int64
		present bool
	}{
		{name: "absent"},
		{name: "partial", metrics: Metrics{InputTokens: 3, InputTokensPresent: true}},
		{name: "zero", metrics: Metrics{InputTokensPresent: true, OutputTokensPresent: true}, present: true},
		{name: "sum", metrics: Metrics{InputTokens: 3, OutputTokens: 2, InputTokensPresent: true, OutputTokensPresent: true}, want: 5, present: true},
		{name: "explicit zero", metrics: Metrics{InputTokens: 3, OutputTokens: 2, InputTokensPresent: true, OutputTokensPresent: true, TotalTokensPresent: true}, present: true},
		{name: "overflow", metrics: Metrics{InputTokens: math.MaxInt64, OutputTokens: 1, InputTokensPresent: true, OutputTokensPresent: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.metrics
			got.ComputeTotal()
			if got.TotalTokens != tt.want || got.TotalTokensPresent != tt.present {
				t.Fatalf("total = %+v", got)
			}
		})
	}
}
