package runqueue

import "github.com/iamseth/tao/internal/plan"

// QueueStatusCounts counts entries for each known queue status.
type QueueStatusCounts struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

// BatchSummary is a pure aggregation of a durable queue snapshot.
type BatchSummary struct {
	Total             int               `json:"total"`
	Statuses          QueueStatusCounts `json:"statuses"`
	SucceededReviewed int               `json:"succeeded_reviewed"`
}

// ReviewedPlanLookup returns the canonical reviewed predicate for Summarize.
func ReviewedPlanLookup(summaries []plan.PlanSummary) func(planID string) bool {
	reviewed := make(map[string]bool, len(summaries))
	for _, summary := range summaries {
		if !summary.Reviewed {
			continue
		}
		reviewed[summary.ID] = true
	}
	return func(planID string) bool {
		return reviewed[planID]
	}
}

// Summarize returns queue status counts from snapshot. reviewed is consulted only
// for succeeded entries; a nil reviewed function treats every plan as unreviewed.
func Summarize(snapshot QueueSnapshot, reviewed func(planID string) bool) BatchSummary {
	summary := BatchSummary{Total: len(snapshot.Entries)}
	for _, entry := range snapshot.Entries {
		switch entry.Status {
		case QueueStatusPending:
			summary.Statuses.Pending++
		case QueueStatusRunning:
			summary.Statuses.Running++
		case QueueStatusSucceeded:
			summary.Statuses.Succeeded++
			if reviewed != nil && reviewed(entry.PlanID) {
				summary.SucceededReviewed++
			}
		case QueueStatusFailed:
			summary.Statuses.Failed++
		case QueueStatusSkipped:
			summary.Statuses.Skipped++
		}
	}
	return summary
}
