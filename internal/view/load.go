package view

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/rework"
)

type Repository interface {
	GetPlan(ctx context.Context, id string) (*plan.PlanDetail, error)
}

type Options struct {
	Now func() time.Time
}

type Plan struct {
	Detail  *plan.PlanDetail
	Derived plan.DerivedPlan
	Now     time.Time
}

// ShowPayload is the stable, explicit projection used by structured plan
// inspection. It deliberately excludes raw plan artifacts.
type ShowPayload struct {
	Schema       string                     `json:"schema"`
	ID           string                     `json:"id"`
	Title        string                     `json:"title"`
	Status       string                     `json:"status"`
	Repository   ShowRepository             `json:"repository"`
	Progress     ShowProgress               `json:"progress"`
	Rework       ShowRework                 `json:"rework"`
	Telemetry    ShowTelemetry              `json:"telemetry"`
	NextAction   plan.PlanNextAction        `json:"next_action"`
	Finalization *plan.FinalizationRecovery `json:"finalization,omitempty"`
	Abandonment  *ShowAbandonment           `json:"abandonment,omitempty"`
	Warnings     []string                   `json:"warnings"`
}

// ShowTelemetry exposes only recorded aggregates, never event or provider identity.
type ShowTelemetry struct {
	Totals      ShowTelemetryTotals `json:"totals"`
	ByRole      []ShowRoleTotals    `json:"by_role"`
	Limitations []string            `json:"limitations"`
}

type ShowRoleTotals struct {
	Role   plan.AgentRole      `json:"role"`
	Totals ShowTelemetryTotals `json:"totals"`
}

// Nullable measurements distinguish absent observations from measured zero.
// PartialRecordedTotals concerns recorded attempts, not coverage of all work.
type ShowTelemetryTotals struct {
	Sessions              int                                 `json:"sessions"`
	Attempts              int                                 `json:"attempts"`
	FailedAttempts        int                                 `json:"failed_attempts"`
	Availability          plan.AgentMetricsAvailabilityCounts `json:"availability"`
	PartialRecordedTotals bool                                `json:"partial_recorded_totals"`
	InputTokens           *int64                              `json:"input_tokens"`
	OutputTokens          *int64                              `json:"output_tokens"`
	ReasoningTokens       *int64                              `json:"reasoning_tokens"`
	CacheReadTokens       *int64                              `json:"cache_read_tokens"`
	CacheWriteTokens      *int64                              `json:"cache_write_tokens"`
	TotalTokens           *int64                              `json:"total_tokens"`
	Cost                  *float64                            `json:"cost"`
}

func ProjectShowTelemetry(events []plan.Event) ShowTelemetry {
	summary := plan.SummarizeAgentMetrics(plan.AgentMetricsEvents(events))
	out := ShowTelemetry{
		Totals: projectShowTelemetryTotals(summary.Totals),
		ByRole: make([]ShowRoleTotals, 0, len(summary.ByRole)),
		Limitations: []string{
			"Totals sum recorded usage only; missing phase events do not prove the phase did not run.",
			"Unknown availability includes legacy coverage; null measurements are unavailable/unknown, not zero.",
			"Interactive planning is not collected; direct note generation is planning work only in surviving validated plans.",
			"Merge-batch usage is separate repository-scoped telemetry and is not included here.",
			"Attempts include failures; unique sessions are deduplicated overall and per role and need not add across roles.",
		},
	}
	for _, group := range summary.ByRole {
		out.ByRole = append(out.ByRole, ShowRoleTotals{Role: plan.AgentRole(group.Key).Normalized(), Totals: projectShowTelemetryTotals(group.Totals)})
	}
	return out
}

func projectShowTelemetryTotals(t plan.AgentMetricsTotals) ShowTelemetryTotals {
	return ShowTelemetryTotals{
		Sessions: t.Sessions, Attempts: t.Attempts, FailedAttempts: t.FailedAttempts,
		Availability:          t.Availability,
		PartialRecordedTotals: t.Availability.Partial+t.Availability.Unavailable+t.Availability.Unknown > 0,
		InputTokens:           showMeasurement(t.InputTokens, t.InputTokensPresent),
		OutputTokens:          showMeasurement(t.OutputTokens, t.OutputTokensPresent),
		ReasoningTokens:       showMeasurement(t.ReasoningTokens, t.ReasoningTokensPresent),
		CacheReadTokens:       showMeasurement(t.CacheReadTokens, t.CacheReadTokensPresent),
		CacheWriteTokens:      showMeasurement(t.CacheWriteTokens, t.CacheWriteTokensPresent),
		TotalTokens:           showMeasurement(t.TotalTokens, t.TotalTokensPresent),
		Cost:                  showMeasurement(t.Cost, t.CostPresent),
	}
}

func showMeasurement[T int64 | float64](value T, present bool) *T {
	if !present {
		return nil
	}
	return &value
}

type ShowRepository struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
}

type ShowProgress struct {
	Completed      int    `json:"completed"`
	Pending        int    `json:"pending"`
	Total          int    `json:"total"`
	CurrentSliceID string `json:"current_slice_id,omitempty"`
	NextSliceID    string `json:"next_slice_id,omitempty"`
}

// ShowRework is the display-safe, read-only rework history shown by plan
// inspection and the live run header.
type ShowRework struct {
	Rounds                    int      `json:"rounds"`
	CurrentStopClassification string   `json:"current_stop_classification,omitempty"`
	RecurringFiles            []string `json:"recurring_files"`
}

// ProjectShowRework derives current rework facts without treating them as
// lifecycle or recovery evidence. Recurring files are ranked by round count,
// then path, so the first item is suitable for compact presentation.
func ProjectShowRework(events []plan.Event) ShowRework {
	summary := plan.SummarizeRework(events)
	churn := plan.ProjectReworkChurn(events, 0)
	files := make([]string, 0)
	for file, rounds := range churn.FileRounds {
		if len(rounds) >= 2 {
			files = append(files, file)
		}
	}
	slices.SortFunc(files, func(a, b string) int {
		if byRounds := len(churn.FileRounds[b]) - len(churn.FileRounds[a]); byRounds != 0 {
			return byRounds
		}
		return strings.Compare(a, b)
	})

	projection := ShowRework{Rounds: summary.Rounds, RecurringFiles: files}
	if plan.HasUnresolvedReworkStop(events) {
		projection.CurrentStopClassification = string(rework.StopKindForPersistedReason(summary.LatestStoppedReason))
	}
	return projection
}

// ShowAbandonment is an explicit display-safe projection rather than a raw
// event. Reason is normalized and bounded; malformed zero timestamps remain
// absent instead of being presented as evidence.
type ShowAbandonment struct {
	Reason      string     `json:"reason"`
	AbandonedAt *time.Time `json:"abandoned_at,omitempty"`
}

func (loaded Plan) ShowPayload() ShowPayload {
	detail := loaded.Detail
	var abandonment *ShowAbandonment
	if plan.PlanLifecycleStatus(detail) == plan.StatusAbandoned {
		abandonment = projectShowAbandonment(loaded.Derived.Abandonment)
	}
	return ShowPayload{
		Schema: "tao.show.v1",
		ID:     detail.State.Plan.ID,
		Title:  detail.State.Plan.Title,
		Status: plan.PlanLifecycleStatus(detail),
		Repository: ShowRepository{
			Name:   detail.State.Repo.Name,
			Branch: detail.State.Repo.Branch,
		},
		Progress: ShowProgress{
			Completed:      loaded.Derived.CompletedCount,
			Pending:        loaded.Derived.PendingCount,
			Total:          loaded.Derived.TotalCount,
			CurrentSliceID: loaded.Derived.CurrentSliceID,
			NextSliceID:    loaded.Derived.NextSliceID,
		},
		Rework:       ProjectShowRework(detail.Events),
		Telemetry:    ProjectShowTelemetry(detail.Events),
		NextAction:   loaded.DisplayNextAction(),
		Finalization: cloneFinalizationRecovery(loaded.Derived.FinalizationRecovery),
		Abandonment:  abandonment,
		Warnings:     append([]string{}, detail.Warnings...),
	}
}

// DisplayNextAction removes duplicated untrusted abandonment prose from the
// generic lifecycle recommendation. The bounded evidence is projected in its
// dedicated field and rendered separately by text views.
func (loaded Plan) DisplayNextAction() plan.PlanNextAction {
	next := loaded.Derived.NextAction
	next.Alternatives = append([]plan.PlanAction{}, next.Alternatives...)
	if loaded.Detail != nil && plan.PlanLifecycleStatus(loaded.Detail) == plan.StatusAbandoned {
		next.Primary.Reason = "the plan was abandoned"
	}
	return next
}

func projectShowAbandonment(source *plan.AbandonmentEvidence) *ShowAbandonment {
	if source == nil {
		return nil
	}
	out := &ShowAbandonment{Reason: FormatAbandonmentText(source.Reason)}
	if !source.AbandonedAt.IsZero() {
		at := source.AbandonedAt.UTC()
		out.AbandonedAt = &at
	}
	return out
}

func cloneFinalizationRecovery(source *plan.FinalizationRecovery) *plan.FinalizationRecovery {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func LoadPlan(ctx context.Context, repo Repository, id string, options Options) (Plan, error) {
	detail, err := repo.GetPlan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}

	return Plan{Detail: detail, Derived: plan.Derive(detail, now), Now: now}, nil
}
