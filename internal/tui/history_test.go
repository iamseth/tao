package tui

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
)

func TestHistoryMembershipAndOrdering(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []monitor.Row{
		{PlanID: "planned", Status: plan.StatusPlanned},
		{PlanID: "blocked", Status: plan.StatusBlocked},
		{PlanID: "failed", Status: plan.StatusVerificationFailed},
		{PlanID: "review", Status: plan.StatusInReview},
		{PlanID: "merge", Status: plan.StatusReviewed},
		{PlanID: "invalid", Status: plan.StatusInvalid},
		{PlanID: "unknown", Status: "future-status"},
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "missing"},
	}
	for i := range 25 {
		at := now.Add(time.Duration(i) * time.Hour)
		status := plan.StatusCompleted
		if i%2 == 0 {
			status = plan.StatusAbandoned
		}
		rows = append(rows, monitor.Row{PlanID: fmt.Sprintf("old-%02d", i), Status: status, UpdatedAt: &at})
	}
	before := slices.Clone(rows)
	for _, enabled := range []bool{false, true} {
		s := loopState{snapshot: monitor.Snapshot{Rows: rows}, filter: Filter{Enabled: enabled}}
		if got := len(s.visibleRows()); got != 8 {
			t.Fatalf("WIP count = %d, want 8", got)
		}
		s.page = PageHistory
		history := s.visibleRows()
		if len(history) != 25 {
			t.Fatalf("History count = %d, want 25", len(history))
		}
		for i, row := range history {
			if row.PlanID != fmt.Sprintf("old-%02d", 24-i) {
				t.Fatalf("history order: %+v", history)
			}
		}
		s.setSearchQuery("old-00")
		if got := s.visibleRows(); len(got) != 1 || got[0].PlanID != "old-00" {
			t.Fatalf("old history search: %+v", got)
		}
		frame := Render(Model{Page: PageHistory, Snapshot: s.snapshot, SearchQuery: s.searchQuery, Width: 100})
		if !strings.Contains(frame, "old-00") || !strings.Contains(frame, "1 plan") || strings.Contains(frame, "blocked") {
			t.Fatalf("history render: %s", frame)
		}
	}
	if !reflect.DeepEqual(rows, before) {
		t.Fatal("page projection mutated snapshot")
	}
}

func TestHistoryFiltersAreIndependentAndPersistTogether(t *testing.T) {
	ctx := context.Background()
	store := &fakeFilterStore{}
	app := App{FilterStore: store}
	s := loopState{filter: Filter{Enabled: true, Repositories: []string{"repo"}, Statuses: []string{plan.StatusBlocked, plan.StatusCompleted}}}
	for _, page := range []PageID{PagePlans, PageHistory} {
		s.page = page
		app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
		for _, option := range s.filterMenu.statuses {
			if option.ID == plan.StatusSkipped || historyStatus(option.ID) != (page == PageHistory) {
				t.Fatalf("%s offered %q", page, option.ID)
			}
		}
		if page == PageHistory {
			s.filterMenu.filter.Statuses = []string{plan.StatusAbandoned}
		}
		app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyEsc})
	}
	if !slices.Equal(filterForPage(s.filter, PagePlans).Statuses, []string{plan.StatusBlocked}) || !slices.Equal(filterForPage(s.filter, PageHistory).Statuses, []string{plan.StatusAbandoned}) {
		t.Fatalf("status selections crossed pages: %+v", s.filter)
	}
	if len(store.saves) == 0 || !reflect.DeepEqual(store.saves[len(store.saves)-1], s.filter) {
		t.Fatalf("persisted filters = %+v", store.saves)
	}
	// Clearing History leaves WIP's selection intact; repository filtering is shared.
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'c'})
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyEsc})
	if !slices.Equal(s.filter.Statuses, []string{plan.StatusBlocked}) || len(s.filter.Repositories) != 0 {
		t.Fatalf("clear crossed status boundary: %+v", s.filter)
	}
	// A legacy completed-only (or slice-only) filter must not empty WIP.
	for _, statuses := range [][]string{{plan.StatusCompleted}, {plan.StatusSkipped}} {
		if !filterForPage(Filter{Enabled: true, Statuses: statuses}, PagePlans).IsEmpty() {
			t.Fatalf("legacy statuses leaked into WIP: %v", statuses)
		}
	}
}

func TestHistoryNavigationRefreshAndDetails(t *testing.T) {
	active := monitor.Row{RepositoryID: "repo", PlanID: "active", Status: plan.StatusPlanned, PlanDir: "/active"}
	done := monitor.Row{RepositoryID: "repo", PlanID: "done", Status: plan.StatusCompleted, PlanDir: "/done"}
	old := monitor.Row{RepositoryID: "repo", PlanID: "old", Status: plan.StatusAbandoned, PlanDir: "/old"}
	s := loopState{snapshot: monitor.Snapshot{Rows: []monitor.Row{active, done, old}}}
	app := App{Details: &fakeDetailRepository{detail: &plan.PlanDetail{}}}
	ctx := context.Background()
	if s.activePage() != PagePlans {
		t.Fatal("startup must be WIP")
	}
	for _, want := range []PageID{PageHistory, PageNotes, PagePlans} {
		app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyTab})
		if s.activePage() != want {
			t.Fatalf("tab = %s, want %s", s.activePage(), want)
		}
	}
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyTab})
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	if row, ok := s.selectedRow(); !ok || row.PlanID != "old" {
		t.Fatalf("History bottom = %+v", row)
	}
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyEnter})
	if s.detail == nil || s.detail.row.PlanID != "old" {
		t.Fatal("History did not open selected plan")
	}
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyEsc})
	if s.detail != nil || s.activePage() != PageHistory {
		t.Fatal("detail did not return to History")
	}
	s.switchPage(-1) // WIP, leaving History selection saved.
	s.replaceSnapshot(monitor.Snapshot{Rows: []monitor.Row{old, active, done}})
	s.switchPage(1)
	if row, ok := s.selectedRow(); !ok || row.PlanID != "old" || s.selected != 0 {
		t.Fatalf("inactive History selection lost: %+v index %d", row, s.selected)
	}
	// The selected WIP row becomes terminal while History is open.
	active.Status = plan.StatusCompleted
	s.replaceSnapshot(monitor.Snapshot{Rows: []monitor.Row{old, active, done}})
	if row, ok := s.selectedRow(); !ok || row.PlanID != "old" {
		t.Fatal("refresh moved History selection")
	}
	s.switchPage(-1)
	if len(s.visibleRows()) != 0 || s.selected != 0 {
		t.Fatal("terminal plan remained in WIP")
	}
	s.switchPage(1)
	app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'c'})
	defer s.closeDetail()
	if s.detail == nil || s.detail.activeTab != detailTabChanges {
		t.Fatal("History Changes shortcut did not open")
	}
}
