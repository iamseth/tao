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

func TestDashboardViewportBounds(t *testing.T) {
	for _, page := range []PageID{PageHistory, PageNotes, PagePlans} {
		for _, width := range []int{1, 20, 59, 60, 100} {
			for _, height := range []int{1, 3, 8, 24} {
				for _, profile := range []theme.Profile{theme.ProfileNone, theme.ProfileTrueColor} {
					model := Model{Page: page, Width: width, Height: height, Profile: profile, Snapshot: monitor.Snapshot{Rows: []monitor.Row{testActionRow()}}, NoteSnapshot: note.Snapshot{Notes: []note.CatalogNote{{ID: "note", Text: "body"}}}}
					for _, help := range []bool{false, true} {
						model.ShowShortcuts = help
						lines := strings.Split(strings.TrimPrefix(Render(model), clearScreenSequence), "\n")
						if len(lines) > height {
							t.Fatalf("%s height %d: %d lines", page, height, len(lines))
						}
						for _, line := range lines {
							if cells.Width(line) > width {
								t.Fatalf("%s width %d: %q", page, width, line)
							}
						}
					}
				}
			}
		}
	}
}

func TestDashboardNavigationRetainsListsWithoutActions(t *testing.T) {
	app := App{}
	state := loopState{page: PagePlans, selected: 1, snapshot: monitor.Snapshot{Rows: []monitor.Row{testActionRow(), testActionRow()}}, noteSnapshot: note.Snapshot{Notes: []note.CatalogNote{{ID: "one"}, {ID: "two"}, {ID: "three"}}}, pageSelections: map[PageID]int{PageNotes: 2}}
	originalRows := state.visibleRows()
	for _, keys := range [][2]term.Key{{term.KeyShiftTab, term.KeyTab}, {term.KeyArrowLeft, term.KeyArrowRight}} {
		app.handleKey(context.Background(), &state, term.KeyEvent{Key: keys[0]})
		if state.activePage() != PageNotes || state.selected != 2 {
			t.Fatal("WIP did not cycle to saved Backlog selection")
		}
		app.handleKey(context.Background(), &state, term.KeyEvent{Key: keys[1]})
		if state.activePage() != PagePlans || state.selected != 1 || !reflect.DeepEqual(state.visibleRows(), originalRows) {
			t.Fatal("WIP list state lost")
		}
	}
	for _, r := range "raMm" {
		app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: r})
	}
	if state.confirm != nil || state.detail != nil {
		t.Fatal("nil Actions opened action UI")
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'})
	if !state.showShortcuts {
		t.Fatal("help did not open")
	}
	found := false
	for _, hint := range shortcutsForPage(PagePlans) {
		if hint.key == "c" && hint.action == "Open plan Changes" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing Changes shortcut")
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc})
	if app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc}) {
		t.Fatal("first root Escape quit")
	}
	if !app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc}) {
		t.Fatal("double Escape did not quit")
	}
	for _, key := range []term.KeyEvent{{Key: term.KeyRune, Rune: 'q'}, {Key: term.KeyCtrlC}} {
		if !app.handleKey(context.Background(), &state, key) {
			t.Fatal("global quit failed")
		}
	}
	if normalizePage("") != PagePlans || len(dashboardTabs) != 3 {
		t.Fatal("dashboard contract changed")
	}
	for _, page := range []PageID{PageSettings, PageDebug} {
		if normalizePage(page) != page {
			t.Fatal("hidden diagnostics no longer renderable")
		}
	}
}
