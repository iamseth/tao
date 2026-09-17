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
	NextAction   plan.PlanNextAction        `json:"next_action"`
	Finalization *plan.FinalizationRecovery `json:"finalization,omitempty"`
	Abandonment  *ShowAbandonment           `json:"abandonment,omitempty"`
	Warnings     []string                   `json:"warnings"`
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
