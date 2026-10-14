package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/theme"
)

func TestSortMenuNavigationAndRender(t *testing.T) {
	m := newSortMenu(PagePlans, Sort{})
	size := term.Size{Width: 100, Height: 9}
	for range 20 {
		m.handleKey(term.KeyEvent{Key: term.KeyPageDown}, size)
	}
	if m.selected != len(m.options) || m.offset == 0 {
		t.Fatalf("not clamped: %+v", m)
	}
	for range 20 {
		m.handleKey(term.KeyEvent{Key: term.KeyPageUp}, size)
	}
	if m.selected != 0 || m.offset != 0 {
		t.Fatalf("not clamped: %+v", m)
	}
	for _, r := range "jjk" {
		m.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: r}, size)
	}
	if m.selected != 1 {
		t.Fatal("navigation")
	}
	palette := theme.Default().Palette(theme.ProfileNone)
	text := strings.Join(m.render(term.Size{Width: 100, Height: 24}, palette), "\n")
	for _, want := range []string{"Direction: ascending", "(x) Default", "( ) AGE", "Esc/s close"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if got := strings.Join(m.render(term.Size{Width: 10, Height: 6}, palette), ""); got != "[Sort]" {
		t.Fatal(got)
	}
	before := *m
	m.render(term.Size{Width: 15, Height: 7}, palette)
	if m.selected != before.selected || m.offset != before.offset {
		t.Fatal("render mutated viewport")
	}
	for _, key := range []term.KeyEvent{{Key: term.KeyEsc}, {Key: term.KeyRune, Rune: 's'}} {
		if action, changed := m.handleKey(key, size); action != sortMenuClosed || changed {
			t.Fatal("close")
		}
	}
	for _, key := range []term.KeyEvent{{Key: term.KeyCtrlC}, {Key: term.KeyRune, Rune: 'q'}} {
		if action, changed := m.handleKey(key, size); action != sortMenuQuit || changed {
			t.Fatal("quit")
		}
	}
}

type recordingSortStore struct {
	saves []PageSorts
	err   error
}

func (s *recordingSortStore) Load(context.Context) (PageSorts, error) { return PageSorts{}, nil }
func (s *recordingSortStore) Save(_ context.Context, sorts PageSorts) error {
	s.saves = append(s.saves, sorts)
	return s.err
}

func TestSortMenuLoop(t *testing.T) {
	ctx := context.Background()
	for _, page := range []PageID{PagePlans, PageHistory, PageNotes, PageSettings, PageDebug} {
		t.Run(string(page), func(t *testing.T) {
			var out bytes.Buffer
			app := App{Output: &out}
			state := loopState{page: page, size: term.Size{Width: 120, Height: 40}}
			press := func(r rune) { app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyRune, Rune: r}) }
			press('s')
			list := isPlanPage(page) || page == PageNotes
			if (state.sortMenu != nil) != list {
				t.Fatal("wrong menu admission")
			}
			if list {
				press('S')
				if state.activePage() != page {
					t.Fatal("sort not modal")
				}
				if err := app.writeFrame(state); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "Direction: ascending") || !strings.Contains(out.String(), "all repos") {
					t.Fatal(out.String())
				}
				press('s')
				press('f')
				press('S')
				if state.activePage() != page {
					t.Fatal("filter not modal")
				}
				press('f')
			}
			press('S')
			if state.activePage() != PageSettings {
				t.Fatal("settings shortcut")
			}
		})
	}
}

func TestSortMenuSavesAndPreservesIdentity(t *testing.T) {
	for _, page := range []PageID{PagePlans, PageHistory, PageNotes} {
		t.Run(string(page), func(t *testing.T) {
			store := &recordingSortStore{}
			app := App{SortStore: store}
			status := "planned"
			if page == PageHistory {
				status = "completed"
			}
			state := loopState{page: page, selected: 1, size: term.Size{Width: 100, Height: 24},
				snapshot:     monitor.Snapshot{Rows: []monitor.Row{{RepositoryID: "b", PlanID: "other", Status: status}, {RepositoryID: "a", PlanID: "target", Status: status}}},
				noteSnapshot: note.Snapshot{Notes: []note.CatalogNote{{RepositoryID: "b", ID: "other"}, {RepositoryID: "a", ID: "target"}}},
			}
			ctx := context.Background()
			app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyRune, Rune: 's'})
			state.sortMenu.selected = 2 // REPO
			app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEnter})
			if len(store.saves) != 1 || store.saves[0].For(page).Column != SortRepo {
				t.Fatal("sort not saved")
			}
			if state.selected != 0 {
				t.Fatalf("selection moved: %d", state.selected)
			}
			if page == PageNotes {
				if item, ok := state.noteAt(state.selected); !ok || item.ID != "target" {
					t.Fatal("note identity lost")
				}
			} else if item, ok := state.selectedRow(); !ok || item.PlanID != "target" {
				t.Fatal("plan identity lost")
			}
			store.err = errors.New("disk full")
			app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyRune, Rune: 'd'})
			if !state.sorts.For(page).Descending || !strings.Contains(state.filterMessage, "disk full") {
				t.Fatal("save error lost memory or warning")
			}
		})
	}
}

func TestSortMenuDirection(t *testing.T) {
	m := newSortMenu(PagePlans, Sort{})
	size := term.Size{Width: 100, Height: 24}
	_, changed := m.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'd'}, size)
	if changed {
		t.Fatal("default direction changed")
	}
	m.selected = len(m.options)
	_, changed = m.handleKey(term.KeyEvent{Key: term.KeyEnter}, size)
	if !changed || m.sort.Column != SortAge || !m.sort.Descending {
		t.Fatalf("age default: %+v", m.sort)
	}
	_, changed = m.handleKey(term.KeyEvent{Key: term.KeyEnter}, size)
	if !changed || m.sort.Descending {
		t.Fatal("reselection did not flip")
	}
	_, changed = m.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'd'}, size)
	if !changed || !m.sort.Descending {
		t.Fatal("direction did not flip")
	}
}
