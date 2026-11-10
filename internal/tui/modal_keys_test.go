package tui

import (
	"context"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/term"
)

func TestOrdinaryModalPrecedence(t *testing.T) {
	ctx := context.Background()
	app := App{}
	for _, menu := range []string{"sort", "filter"} {
		t.Run(menu, func(t *testing.T) {
			for _, r := range "enxS?" {
				s := loopState{page: PagePlans}
				if menu == "sort" {
					s.sortMenu = newSortMenu(PagePlans, s.sorts.For(PagePlans))
				} else {
					s.filterMenu = newPageFilterMenu(s.filter, s.snapshot, s.noteSnapshot, PagePlans)
				}
				if app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: r}) || s.page != PagePlans || s.showShortcuts || s.settingsEditor != nil {
					t.Fatalf("menu leaked %q: %+v", r, s)
				}
			}
		})
	}
	t.Run("confirmation and overlay", func(t *testing.T) {
		s := loopState{showShortcuts: true}
		called := false
		s.beginConfirm("confirm", func(bool) { called = true })
		app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'y'})
		if called {
			t.Fatal("overlay leaked confirmation key")
		}
		app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyEsc})
		if app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'q'}) || !called || s.confirm != nil {
			t.Fatal("q must decline, not quit")
		}
	})
	t.Run("settings before search", func(t *testing.T) {
		s := loopState{page: PageSettings, searchActive: true, settingsSnapshot: SettingsSnapshot{Repositories: []RepositorySetting{{ID: "repo"}}}}
		app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'e'})
		if s.settingsEditor == nil || s.searchQuery != "" {
			t.Fatal("settings e must precede search")
		}
		app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'S'})
		if s.searchQuery != "" {
			t.Fatal("editor leaked key")
		}
		if !app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyCtrlC}) {
			t.Fatal("Ctrl+C must quit editor")
		}
	})
	t.Run("search before globals", func(t *testing.T) {
		s := loopState{page: PagePlans, searchActive: true}
		for _, r := range "Sq?" {
			if app.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: r}) {
				t.Fatal("search quit")
			}
		}
		if s.searchQuery != "Sq?" || s.page != PagePlans || s.showShortcuts {
			t.Fatalf("search leaked: %+v", s)
		}
	})
}

func TestOrdinaryModalCtrlC(t *testing.T) {
	for _, mode := range []string{"sort", "filter", "shortcuts", "confirm", "editor", "search"} {
		t.Run(mode, func(t *testing.T) {
			s := loopState{page: PagePlans}
			called := false
			switch mode {
			case "sort":
				s.sortMenu = newSortMenu(PagePlans, s.sorts.For(PagePlans))
			case "filter":
				s.filterMenu = newPageFilterMenu(s.filter, s.snapshot, s.noteSnapshot, PagePlans)
			case "shortcuts":
				s.showShortcuts = true
			case "confirm":
				s.beginConfirm("confirm", func(bool) { called = true })
			case "editor":
				s.settingsEditor = &settingsEditorState{}
			case "search":
				s.searchActive = true
			}
			if !(App{}).handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyCtrlC}) || called {
				t.Fatal("Ctrl+C must quit without confirming")
			}
		})
	}
}

func TestOrdinaryEscapeTimingAndUnknownKeys(t *testing.T) {
	for _, delay := range []time.Duration{-time.Millisecond, time.Second, time.Second + time.Millisecond} {
		now := time.Unix(100, 0)
		s := loopState{now: func() time.Time { return now }}
		app := App{}
		esc := term.KeyEvent{Key: term.KeyEsc}
		if app.handleKey(context.Background(), &s, esc) {
			t.Fatal("first Escape quit")
		}
		now = now.Add(delay)
		if got := app.handleKey(context.Background(), &s, esc); got != (delay >= 0 && delay <= time.Second) {
			t.Fatalf("delay %v quit=%v", delay, got)
		}
	}
	now := time.Unix(100, 0)
	s := loopState{now: func() time.Time { return now }}
	app := App{}
	app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyEsc})
	app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyRune, Rune: '~'})
	if !s.lastRootEscape.IsZero() || app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyEsc}) {
		t.Fatal("unknown list key must interrupt double Escape")
	}
}
