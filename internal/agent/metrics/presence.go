package metrics

import (
	"encoding/json"
	"math"
)

// Availability describes measurement coverage independently of session outcome.
// The zero value is unknown, for compatibility with runtimes without metadata.
type Availability string

const (
	Reported    Availability = "reported"
	Partial     Availability = "partial"
	Unavailable Availability = "unavailable"
)

// ClassifyAvailability requires the common displayed measurements for reported
// coverage. Provider-specific reasoning/cache counters are optional.
func (m *Metrics) ClassifyAvailability() {
	switch {
	case m.InputTokensPresent && m.OutputTokensPresent && m.TotalTokensPresent && m.CostPresent:
		m.Availability = Reported
	case m.InputTokensPresent || m.OutputTokensPresent || m.TotalTokensPresent || m.CostPresent || m.ReasoningTokensPresent || m.CacheReadTokensPresent || m.CacheWriteTokensPresent:
		m.Availability = Partial
	default:
		m.Availability = Unavailable
	}
}

// TokenValue returns the first valid nonnegative integer, including zero.
// Invalid, fractional, or overflowing values do not establish presence.
func TokenValue(values ...any) (int64, bool) {
	for _, value := range values {
		switch v := value.(type) {
		case int:
			if v >= 0 {
				return int64(v), true
			}
		case int64:
			if v >= 0 {
				return v, true
			}
		case json.Number:
			if n, err := v.Int64(); err == nil && n >= 0 {
				return n, true
			}
		case float64:
			if v >= 0 && v < math.Exp2(63) && math.Trunc(v) == v {
				return int64(v), true
			}
		}
	}
	return 0, false
}

// CostValue returns the first finite nonnegative cost, including zero.
func CostValue(values ...any) (float64, bool) {
	for _, value := range values {
		var n float64
		switch v := value.(type) {
		case float64:
			n = v
		case float32:
			n = float64(v)
		case int:
			n = float64(v)
		case int64:
			n = float64(v)
		case json.Number:
			var err error
			n, err = v.Float64()
			if err != nil {
				continue
			}
		default:
			continue
		}
		if n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n) {
			return n, true
		}
	}
	return 0, false
}

// ComputeTotal preserves explicit totals (including zero). A derived total is
// measured only when both components exist and their sum cannot overflow.
func (m *Metrics) ComputeTotal() {
	if !m.TotalTokensPresent && m.InputTokensPresent && m.OutputTokensPresent && m.InputTokens <= math.MaxInt64-m.OutputTokens {
		m.TotalTokens = m.InputTokens + m.OutputTokens
		m.TotalTokensPresent = true
	}
}
