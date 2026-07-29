package plan

import "time"

// PlanFilter controls repository list queries.
type PlanFilter struct {
	ActiveOnly bool
}

// PlanStatusRollup counts plans by their recorded status value.
type PlanStatusRollup struct {
	Planned          int `json:"planned"`
	InProgress       int `json:"in_progress"`
	InReview         int `json:"in_review"`
	Reviewed         int `json:"reviewed"`
	ChangesRequested int `json:"changes_requested"`
	Completed        int `json:"completed"`
	Blocked          int `json:"blocked"`
}

// PlanRollup is a pure aggregation of repository plan summaries.
type PlanRollup struct {
	Total     int              `json:"total"`
	Statuses  PlanStatusRollup `json:"statuses"`
	Completed int              `json:"completed"`
	Reviewed  int              `json:"reviewed"`
	Verdicts  map[string]int   `json:"verdicts,omitempty"`
}

// PlanSummary is the compact representation used by list views.
type PlanSummary struct {
	ID                               string
	Title                            string
	Status                           string
	Dir                              string
	CurrentSliceID                   string
	CurrentSlice                     *Slice
	CompletedCount                   int
	PendingCount                     int
	TotalCount                       int
	OriginalCompletedCount           int
	OriginalTotalCount               int
	ReworkCompletedCount             int
	ReworkTotalCount                 int
	StartedAt                        *time.Time
	CompletedAt                      *time.Time
	LastActivityAt                   *time.Time
	Elapsed                          time.Duration
	Complete                         bool
	Reviewed                         bool
	ReviewVerdict                    string
	IsActive                         bool
	PlanningSessionPresent           bool
	PlanningSessionValid             bool
	PlanningSessionUnavailableReason string
	PlanningSessionDuration          time.Duration
	PlanningSessionTotalTokens       int64
	PlanningSessionTotalMessages     int64
	Metrics                          AgentMetricsTotals
	PullRequest                      *PullRequest
	Workspace                        *Workspace
	Warnings                         []string
}

// SummarizePlans returns repository-wide counts from already-derived plan summaries.
func SummarizePlans(summaries []PlanSummary) PlanRollup {
	rollup := PlanRollup{Total: len(summaries), Verdicts: make(map[string]int)}
	for _, summary := range summaries {
		switch summary.Status {
		case StatusPlanned:
			rollup.Statuses.Planned++
		case StatusInProgress:
			rollup.Statuses.InProgress++
		case StatusInReview:
			rollup.Statuses.InReview++
		case StatusReviewed:
			rollup.Statuses.Reviewed++
		case StatusChangesRequested:
			rollup.Statuses.ChangesRequested++
		case StatusCompleted:
			rollup.Statuses.Completed++
		case StatusBlocked:
			rollup.Statuses.Blocked++
		}
		if summary.Complete {
			rollup.Completed++
		}
		if summary.Reviewed {
			rollup.Reviewed++
			if summary.ReviewVerdict != "" {
				rollup.Verdicts[summary.ReviewVerdict]++
			}
		}
	}
	return rollup
}

func (p PlanSummary) Active() bool {
	return p.IsActive || active(p.Status, p.CurrentSliceID, p.CurrentSlice, p.Complete)
}

func (p PlanSummary) Runnable() bool {
	return !p.Complete && p.Status != StatusCompleted && p.PendingCount > 0
}
