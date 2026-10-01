package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

func TestReviewBoundsAndIsolation(t *testing.T) {
	for _, width := range []int{1, 20, 59, 60, 100} {
		for _, height := range []int{1, 3, 8, 24} {
			for _, profile := range []theme.Profile{theme.ProfileNone, theme.ProfileTrueColor} {
				model := Model{Page: PageReview, Width: width, Height: height, Profile: profile}
				empty := Render(model)
				model.Snapshot = monitor.Snapshot{Rows: []monitor.Row{testActionRow()}}
				model.NoteSnapshot = note.Snapshot{Notes: []note.CatalogNote{{ID: "secret-note", Text: "secret-body"}}}
				model.SearchQuery = "secret"
				model.FilterMessage = "secret-filter"
				model.ConfirmMessage = "secret-confirm"
				if got := Render(model); got != empty {
					t.Fatal("Review body depends on list data or action messages")
				}
				for _, help := range []bool{false, true} {
					model.ShowShortcuts = help
					lines := strings.Split(strings.TrimPrefix(Render(model), clearScreenSequence), "\n")
					if len(lines) > height {
						t.Fatalf("height %d: %d lines", height, len(lines))
					}
					for _, line := range lines {
						if cells.Width(line) > width {
							t.Fatalf("width %d: %q", width, line)
						}
					}
				}
			}
		}
	}
}

func TestReviewKeysAreInertAndRetainLists(t *testing.T) {
	launcher := &recordingActionLauncher{}
	app := App{Actions: newTestActions(t, launcher, nil, nil), NotePlanningLauncher: notePlanningFunc(func(context.Context, note.CatalogNote) error { t.Fatal("Review launched note planning"); return nil })}
	state := loopState{page: PagePlans, selected: 1, searchQuery: "plan", snapshot: monitor.Snapshot{Rows: []monitor.Row{testActionRow(), testActionRow()}}, pageSelections: map[PageID]int{PageNotes: 2}}
	originalRows := state.visibleRows()
	state.switchPage(1)
	if state.activePage() != PageReview || state.pageRowCount() != 0 {
		t.Fatal("Review has list rows")
	}
	saved := state.pageSelections[PagePlans]
	filter := state.filter
	for _, r := range "nperRaAmMcCdD0123fF/ gGjk" {
		app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: r})
	}
	for _, key := range []term.Key{term.KeyEnter, term.KeyBackspace, term.KeyArrowUp, term.KeyArrowDown, term.KeyPageUp, term.KeyPageDown, term.KeyCtrlG} {
		app.handleKey(context.Background(), &state, term.KeyEvent{Key: key})
	}
	if len(launcher.calls) != 0 || state.confirm != nil || state.detail != nil || state.noteDetail != nil || state.filterMenu != nil || state.searchActive || state.selected != 0 || state.searchQuery != "plan" || !reflect.DeepEqual(state.filter, filter) {
		t.Fatalf("Review changed list/action state: %+v", state)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'})
	if !state.showShortcuts {
		t.Fatal("help did not open")
	}
	for _, hint := range shortcutsForPage(PageReview) {
		if hint.action != "Switch tabs" && hint.action != "Quit" && hint.action != "Close shortcuts" {
			t.Fatalf("Review advertises non-navigation action: %+v", hint)
		}
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc})
	for _, key := range []term.Key{term.KeyShiftTab, term.KeyArrowRight, term.KeyArrowLeft} {
		app.handleKey(context.Background(), &state, term.KeyEvent{Key: key})
	}
	if state.activePage() != PagePlans || state.selected != saved || !reflect.DeepEqual(state.visibleRows(), originalRows) || state.pageSelections[PageNotes] != 2 {
		t.Fatal("list state lost")
	}
	state.switchPage(1)
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyTab})
	if state.activePage() != PageNotes {
		t.Fatal("Review Tab did not wrap to Backlog")
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyShiftTab})
	if state.activePage() != PageReview {
		t.Fatal("Backlog Shift+Tab did not wrap to Review")
	}
	if app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc}) {
		t.Fatal("first Escape quit")
	}
	if !app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc}) {
		t.Fatal("double Escape did not quit")
	}
	if state.searchQuery != "plan" {
		t.Fatal("Review Escape cleared list search")
	}
	if !app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'q'}) {
		t.Fatal("q did not quit")
	}
	if !app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyCtrlC}) {
		t.Fatal("Ctrl+C did not quit")
	}
	if normalizePage("") != PagePlans {
		t.Fatal("default changed")
	}
	for _, tab := range dashboardTabs {
		if tab.ID == PageSettings || tab.ID == PageDebug {
			t.Fatal("diagnostics visible")
		}
	}
}

func TestReviewNavigationAndRender(t *testing.T) {
	if got := adjacentPage(PagePlans, 1); got != PageID("review") {
		t.Fatalf("next page = %q, want review", got)
	}
	for _, page := range []PageID{PageSettings, PageDebug} {
		if normalizePage(page) != page {
			t.Fatalf("hidden page %s no longer renderable", page)
		}
	}
	frame := Render(Model{Page: PageID("review"), Width: 80, Height: 20})
	for _, want := range []string{"Backlog", "WIP", "Review — coming soon", "Changed files", "Diff"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q in %s", want, frame)
		}
	}
}
