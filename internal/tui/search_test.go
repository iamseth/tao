package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
)

func TestSearchFiltersPlansAndNotesCaseInsensitively(t *testing.T) {
	rows := []monitor.Row{
		{RepositoryName: "alpha", PlanID: "owner-flow", PlanTitle: "Owner approval", Status: plan.StatusPlanned},
		{RepositoryName: "beta", PlanID: "other", PlanTitle: "Unrelated work", Status: plan.StatusPlanned},
	}
	filteredRows := FilterPlanRows(rows, "OWNER")
	if len(filteredRows) != 1 || filteredRows[0].PlanID != "owner-flow" {
		t.Fatalf("filtered plans = %+v, want owner-flow", filteredRows)
	}

	snapshot := note.Snapshot{Notes: []note.CatalogNote{
		{RepositoryName: "alpha", ID: "first", Text: "Ask the owner before release."},
		{RepositoryName: "beta", ID: "second", Text: "Unrelated", Tags: []string{"follow-up"}},
	}}
	filteredNotes := FilterNoteSnapshot(snapshot, "OWNER")
	if len(filteredNotes.Notes) != 1 || filteredNotes.Notes[0].ID != "first" {
		t.Fatalf("filtered notes = %+v, want first", filteredNotes.Notes)
	}
	if got := FilterNoteSnapshot(snapshot, "follow-UP"); len(got.Notes) != 1 || got.Notes[0].ID != "second" {
		t.Fatalf("tag-filtered notes = %+v, want second", got.Notes)
	}
}

func TestSearchFiltersPlansByDecisionMetadata(t *testing.T) {
	decisionRow := monitor.Row{
		PlanID: "decision-plan",
		Overview: plan.DecisionOverview{
			Problem:           "Operators cannot compare work.",
			WhyNow:            "The backlog is growing.",
			ExpectedBenefit:   "Faster portfolio choices.",
			Readiness:         plan.DecisionReadinessNeedsRefinement,
			SuccessCriteria:   []string{"Tradeoffs remain explainable."},
			Disposition:       plan.DecisionDispositionConditional,
			DispositionReason: "Confirm staffing first.",
			Priority: &plan.Priority{
				Level: plan.PriorityOverallLevelMust, Impact: plan.PriorityLevelHigh,
				Urgency: plan.PriorityLevelMedium, Effort: plan.PriorityEffortSmall,
				Risk: plan.PriorityLevelLow, Confidence: plan.PriorityLevelHigh,
				Rationale: "High leverage with bounded effort.",
			},
			Sequence: &plan.Sequence{Position: 2, Total: 7, Relationships: []plan.PlanRelation{{
				PlanID: "foundation-plan", Type: plan.PlanRelationAfter, Reason: "Reuse its stable projection.",
			}}},
		},
	}
	rows := []monitor.Row{decisionRow, {PlanID: "unrelated", PlanTitle: "Routine maintenance"}}
	for _, query := range []string{
		"cannot compare", "backlog is growing", "portfolio choices", "needs_refinement",
		"tradeoffs remain", "conditional", "staffing first", "must", "bounded effort",
		"2 of 7", "foundation-plan", "after", "stable projection",
	} {
		t.Run(query, func(t *testing.T) {
			got := FilterPlanRows(rows, query)
			if len(got) != 1 || got[0].PlanID != decisionRow.PlanID {
				t.Fatalf("FilterPlanRows(%q) = %+v, want decision-plan", query, got)
			}
		})
	}
}

func TestRenderSearchStateAndResults(t *testing.T) {
	model := Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{RepositoryName: "alpha", PlanID: "owner", PlanTitle: "Owner approval", Status: plan.StatusPlanned},
			{RepositoryName: "beta", PlanID: "other", PlanTitle: "Unrelated", Status: plan.StatusPlanned},
		}},
		SearchQuery:  "owner",
		SearchActive: true,
	}
	frame := Render(model)
	for _, want := range []string{"tao │▸plans  notes  settings  debug", "1 plan", "Search: /owner█", "owner"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("search frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "> beta") || strings.Contains(frame, "  other  ") {
		t.Fatalf("search frame retained non-match:\n%s", frame)
	}
}

func TestSearchInputAppliesAcrossPagesAndEscapeOrBackspaceClears(t *testing.T) {
	state := loopState{
		snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{RepositoryName: "alpha", PlanID: "owner", PlanTitle: "Owner approval", Status: plan.StatusPlanned},
			{RepositoryName: "beta", PlanID: "other", PlanTitle: "Unrelated", Status: plan.StatusPlanned},
		}},
		noteSnapshot: note.Snapshot{Notes: []note.CatalogNote{
			{RepositoryName: "alpha", ID: "owner-note", Text: "Owner decision"},
			{RepositoryName: "beta", ID: "other-note", Text: "Unrelated"},
		}},
	}
	app := App{}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '/'})
	if !state.searchActive || state.searchQuery != "" {
		t.Fatalf("slash search state active=%t query=%q", state.searchActive, state.searchQuery)
	}
	for _, r := range "owner" {
		if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: r}); quit {
			t.Fatalf("search rune %q unexpectedly quit", r)
		}
	}
	if len(state.visibleRows()) != 1 || state.visibleRows()[0].PlanID != "owner" {
		t.Fatalf("live plan search rows = %+v", state.visibleRows())
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEnter})
	if state.searchActive || state.searchQuery != "owner" {
		t.Fatalf("submitted search active=%t query=%q", state.searchActive, state.searchQuery)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyTab})
	if state.activePage() != PageNotes || len(state.visibleNotes()) != 1 || state.visibleNotes()[0].ID != "owner-note" {
		t.Fatalf("note search page=%q notes=%+v", state.activePage(), state.visibleNotes())
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyBackspace})
	if state.searchQuery != "" || len(state.visibleNotes()) != 2 {
		t.Fatalf("Backspace did not clear search query=%q notes=%+v", state.searchQuery, state.visibleNotes())
	}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '/'})
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'x'})
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc})
	if state.searchActive || state.searchQuery != "" || len(state.visibleNotes()) != 2 {
		t.Fatalf("Escape did not clear active search active=%t query=%q notes=%+v", state.searchActive, state.searchQuery, state.visibleNotes())
	}
}
