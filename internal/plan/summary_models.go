package plan

import "time"

// PlanFilter controls repository list queries.
type PlanFilter struct {
	ActiveOnly bool
}

// PlanStatusRollup counts plans by their projected summary status.
type PlanStatusRollup struct {
	Planned            int `json:"planned"`
	InProgress         int `json:"in_progress"`
	InReview           int `json:"in_review"`
	Reviewed           int `json:"reviewed"`
	ChangesRequested   int `json:"changes_requested"`
	VerificationFailed int `json:"verification_failed"`
	Completed          int `json:"completed"`
	Blocked            int `json:"blocked"`
}

// PlanRollup is a pure aggregation of repository plan summaries.
type PlanRollup struct {
	Total     int              `json:"total"`
	Statuses  PlanStatusRollup `json:"statuses"`
	Completed int              `json:"completed"`
	Reviewed  int              `json:"reviewed"`
	Verdicts  map[string]int   `json:"verdicts,omitempty"`
}

// FinalizationRecovery is the bounded read-side projection of a current
// finalization failure. It deliberately excludes review ranges, branch names,
// and commit identities; command-side gates remain the recovery authority.
type FinalizationRecovery struct {
	Phase          FinalizationFailurePhase `json:"phase"`
	Category       string                   `json:"category"`
	FailedAt       time.Time                `json:"failed_at"`
	RecoveryAction string                   `json:"recovery_action"`
}

// PlanSummary is the compact representation used by list views.
type PlanSummary struct {
	ID                               string
	Title                            string
	ChangeType                       ChangeType
	Overview                         DecisionOverview
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
	Capabilities                     RunCapabilities
	SliceCompletionPending           bool
	UnresolvedReworkStop             bool
	NextAction                       PlanNextAction
	FinalVerificationFailureKind     FinalVerificationFailureKind
	VerificationRecoveryAction       PlanActionKind
	VerificationRecoveryCommand      string
	FinalizationRecovery             *FinalizationRecovery
	PlanningSessionPresent           bool
	PlanningSessionValid             bool
	PlanningSessionUnavailableReason string
	PlanningSessionDuration          time.Duration
	PlanningSessionTotalTokens       int64
	PlanningSessionTotalMessages     int64
	Metrics                          AgentMetricsTotals
	PullRequest                      *PullRequest
	FinalizationFailure              *FinalizationFailure
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
		case StatusVerificationFailed:
			rollup.Statuses.VerificationFailed++
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
