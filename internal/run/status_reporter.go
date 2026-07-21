package run

import "github.com/iamseth/tao/internal/plan"

const maxRunStatusLabelRunes = 32

// StatusReporter is the optional run-boundary reporter. Implementations must be
// best-effort: reporting failures must not fail or delay the wrapped run.
type StatusReporter interface {
	Track(status string, fn func() error) error
}

func trackRunStatus(reporter StatusReporter, status string, fn func() error) error {
	if reporter == nil {
		return fn()
	}
	return reporter.Track(status, fn)
}

func runStatusLabel(planID string) string {
	name, ok := plan.PlanSlug(planID)
	if !ok {
		name = planID
	}
	return capRunes("run "+name, maxRunStatusLabelRunes)
}

func capRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
