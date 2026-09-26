package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

func testFilterMenu() *filterMenu {
	return &filterMenu{
		repositories: []FilterOption{{ID: "a", Name: "Alpha", Available: true}, {ID: "missing", Name: "missing"}},
		statuses:     []FilterOption{{ID: "planned", Name: "planned", Available: true}},
		tags:         []FilterOption{{ID: "bug", Name: "bug", Available: true}},
	}
}

func TestFilterMenuDiscoveryAndWorkingCopy(t *testing.T) {
	original := Filter{Enabled: true, Repositories: []string{"a", "missing"}, Statuses: []string{"old", "planned"}, Tags: []string{"bug", "old"}}
	plans := monitor.Snapshot{Rows: []monitor.Row{{RepositoryID: "a", RepositoryName: "Alpha", Status: "planned"}}}
	notes := note.Snapshot{Notes: []note.CatalogNote{{RepositoryID: "b", RepositoryName: "Beta", Tags: []string{"bug"}}}}
	menu := newFilterMenu(original, plans, notes)
	if !reflect.DeepEqual(menu.repositories, DiscoverRepositories(plans, notes, original.Repositories)) ||
		!reflect.DeepEqual(menu.statuses, DiscoverStatuses(plans, original.Statuses)) ||
		!reflect.DeepEqual(menu.tags, DiscoverTags(notes, original.Tags)) {
		t.Fatal("menu did not discover snapshot and saved options")
	}
	// Removing the first value shifts the underlying array, exposing shallow copies.
	for _, values := range []*[]string{&menu.filter.Repositories, &menu.filter.Statuses, &menu.filter.Tags} {
		toggleFilterValue(values, (*values)[0])
	}
	if !slices.Equal(original.Repositories, []string{"a", "missing"}) ||
		!slices.Equal(original.Statuses, []string{"old", "planned"}) ||
		!slices.Equal(original.Tags, []string{"bug", "old"}) {
		t.Fatalf("working copy changed original: %+v", original)
	}
	if plans.Rows[0].RepositoryID != "a" || !slices.Equal(notes.Notes[0].Tags, []string{"bug"}) {
		t.Fatal("menu changed snapshot")
	}
}

func TestFilterMenuNavigation(t *testing.T) {
	menu := testFilterMenu()
	size := term.Size{Width: 80, Height: 9} // Three content rows, including headings.
	for _, tc := range []struct {
		key      term.KeyEvent
		selected int
		offset   int
	}{
		{term.KeyEvent{Key: term.KeyArrowUp}, 0, 0},
		{term.KeyEvent{Key: term.KeyArrowDown}, 1, 0},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'j'}, 2, 1},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'j'}, 3, 3},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'k'}, 2, 3},
		{term.KeyEvent{Key: term.KeyPageDown}, 5, 6},
		{term.KeyEvent{Key: term.KeyArrowDown}, 5, 6},
		{term.KeyEvent{Key: term.KeyPageUp}, 2, 3},
		{term.KeyEvent{Key: term.KeyPageUp}, 0, 0},
		{term.KeyEvent{Key: term.KeyPageUp}, 0, 0},
		{term.KeyEvent{Key: term.KeyRune, Rune: '/'}, 0, 0},
	} {
		action, change := menu.handleKey(tc.key, size)
		if action != filterMenuContinue || change != (filterMenuChange{}) || menu.selected != tc.selected || menu.offset != tc.offset {
			t.Fatalf("key %+v: action %v change %+v selection/offset %d/%d; want %d/%d", tc.key, action, change, menu.selected, menu.offset, tc.selected, tc.offset)
		}
	}
	for _, bound := range []int{-100, 100} {
		menu.selected, menu.offset = bound, bound
		menu.handleKey(term.KeyEvent{}, size)
		want := 0
		if bound > 0 {
			want = menu.optionCount() + 1
		}
		if menu.selected != want {
			t.Fatalf("bound %d: selection %d", bound, menu.selected)
		}
	}
	menu.clamp(term.Size{Width: 80, Height: 40})
	if menu.offset != 0 {
		t.Fatalf("expanded viewport offset = %d", menu.offset)
	}
	menu.clamp(term.Size{Width: 80, Height: 7})
	if menu.offset != len(menu.rows())-1 {
		t.Fatalf("shrunken viewport offset = %d", menu.offset)
	}
}

func TestFilterMenuToggleCriteria(t *testing.T) {
	for _, key := range []term.KeyEvent{{Key: term.KeyEnter}, {Key: term.KeyRune, Rune: ' '}} {
		menu := testFilterMenu()
		for _, tc := range []struct {
			selected int
			values   *[]string
			id       string
		}{
			{1, &menu.filter.Repositories, "a"},
			{2, &menu.filter.Repositories, "missing"},
			{3, &menu.filter.Statuses, "planned"},
			{4, &menu.filter.Tags, "bug"},
		} {
			menu.selected = tc.selected
			for _, want := range []bool{true, false} {
				action, change := menu.handleKey(key, term.Size{Width: 80, Height: 24})
				if action != filterMenuContinue || change != (filterMenuChange{}) || slices.Contains(*tc.values, tc.id) != want || menu.filter.Enabled {
					t.Fatalf("toggle %s to %t: action %v change %+v filter %+v", tc.id, want, action, change, menu.filter)
				}
			}
		}
	}
}

func TestFilterMenuEnabledAndClearReporting(t *testing.T) {
	size := term.Size{Width: 80, Height: 24}
	menu := testFilterMenu()
	menu.filter = Filter{Repositories: []string{"a"}, Statuses: []string{"planned"}, Tags: []string{"bug"}}
	for _, key := range []term.KeyEvent{{Key: term.KeyEnter}, {Key: term.KeyRune, Rune: ' '}, {Key: term.KeyRune, Rune: 't'}} {
		before := menu.filter
		action, change := menu.handleKey(key, size)
		if action != filterMenuContinue || change != (filterMenuChange{EnabledChanged: true}) || menu.filter.Enabled == before.Enabled {
			t.Fatalf("toggle: action %v change %+v filter %+v", action, change, menu.filter)
		}
		before.Enabled = menu.filter.Enabled
		if !reflect.DeepEqual(menu.filter, before) {
			t.Fatal("enable toggle lost criteria")
		}
	}
	menu.selected = 3
	_, change := menu.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 't'}, size)
	if !change.EnabledChanged || menu.filter.Enabled || menu.filter.IsEmpty() {
		t.Fatal("t did not disable from a criterion row while retaining criteria")
	}
	for _, enabled := range []bool{false, true} {
		for _, key := range []term.KeyEvent{{Key: term.KeyEnter}, {Key: term.KeyRune, Rune: ' '}, {Key: term.KeyRune, Rune: 'c'}} {
			menu.filter = Filter{Enabled: enabled, Repositories: []string{"missing"}, Statuses: []string{"planned"}, Tags: []string{"bug"}}
			menu.selected = menu.optionCount() + 1
			if key.Rune == 'c' {
				menu.selected = 1 // Clear from anywhere.
			}
			for range 2 { // An already-empty clear remains an explicit clear action.
				action, change := menu.handleKey(key, size)
				if action != filterMenuContinue || change != (filterMenuChange{Cleared: true}) || !menu.filter.IsEmpty() || menu.filter.Enabled != enabled {
					t.Fatalf("clear: action %v change %+v filter %+v", action, change, menu.filter)
				}
			}
			_, change = menu.handleKey(term.KeyEvent{}, size)
			if change != (filterMenuChange{}) {
				t.Fatal("change report leaked across keys")
			}
		}
	}
}

func TestFilterMenuCloseAndQuit(t *testing.T) {
	for _, tc := range []struct {
		key  term.KeyEvent
		want filterMenuAction
	}{
		{term.KeyEvent{Key: term.KeyEsc}, filterMenuClosed},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'f'}, filterMenuClosed},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'q'}, filterMenuQuit},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'Q'}, filterMenuQuit},
		{term.KeyEvent{Key: term.KeyCtrlC}, filterMenuQuit},
	} {
		menu := testFilterMenu()
		menu.filter = Filter{Enabled: true, Repositories: []string{"a"}}
		before := menu.filter
		action, change := menu.handleKey(tc.key, term.Size{Width: 80, Height: 24})
		if action != tc.want || change != (filterMenuChange{}) || !reflect.DeepEqual(menu.filter, before) {
			t.Fatalf("key %+v: action %v change %+v filter %+v", tc.key, action, change, menu.filter)
		}
	}
}

func TestFilterMenuRender(t *testing.T) {
	menu := testFilterMenu()
	menu.filter = Filter{Enabled: true, Repositories: []string{"a", "missing"}, Tags: []string{"bug"}}
	text := strings.Join(menu.render(term.Size{Width: 100, Height: 24}, ProfileNone), "\n")
	for _, want := range []string{"Filters", "> Filter on/off: on", "Repositories", "[x] Alpha [a]", "[x] missing (unavailable)", "Statuses", "[ ] planned", "Tags", "[x] bug", "Clear all", "PgUp/PgDn", "Space/Enter", "t on/off", "c clear", "Esc/f close", "q quit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	menu.filter.Enabled = false
	if text := strings.Join(menu.render(term.Size{Width: 80, Height: 24}, ProfileNone), "\n"); !strings.Contains(text, "Filter on/off: off") || !strings.Contains(text, "[x] bug") {
		t.Fatalf("disabled render lost configuration: %s", text)
	}
}

func TestFilterMenuNarrowRenderingAndResize(t *testing.T) {
	for _, menu := range []*filterMenu{{}, testFilterMenu()} {
		menu.selected = menu.optionCount() + 1
		before := *menu
		for _, profile := range []Profile{ProfileNone, ProfileANSI16, ProfileTrueColor} {
			for _, width := range []int{-1, 0, 1, 8, 11, 12, 20, 40, 80, 100} {
				for _, height := range []int{-1, 0, 1, 3, 6, 7, 9, 30} {
					size := term.Size{Width: width, Height: height}
					lines := menu.render(size, profile)
					if len(lines) > max(0, height) || width <= 0 && len(lines) != 0 {
						t.Fatalf("size %+v: %d lines", size, len(lines))
					}
					for _, line := range lines {
						if cells.Width(line) > width {
							t.Fatalf("size %+v overflow: %q", size, line)
						}
					}
					if width >= 20 && height >= 7 && !strings.Contains(strings.Join(lines, "\n"), "> Clear all") {
						t.Fatalf("resize %+v lost selection: %v", size, lines)
					}
				}
			}
		}
		if !reflect.DeepEqual(*menu, before) {
			t.Fatal("render mutated menu")
		}
	}
}

func TestFilterMenuSanitizesLabels(t *testing.T) {
	menu := &filterMenu{repositories: []FilterOption{{ID: "bad\nID", Name: "\x1b]52;c;secret\a界\n\t\u202ename"}}}
	text := strings.Join(menu.render(term.Size{Width: 80, Height: 24}, ProfileNone), "")
	for _, forbidden := range []string{"\x1b", "\a", "\n", "\t", "\u202e", "secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe label %q in %q", forbidden, text)
		}
	}
}

func TestOverlayBoxCentersAndPreservesSurroundingRows(t *testing.T) {
	background := []string{"top", "one", "two", "three", "bottom"}
	got := overlayBox(background, []string{"box", "row"}, 11, 5)
	want := []string{"top", "    box", "    row", "three", "bottom"}
	if !slices.Equal(got, want) {
		t.Fatalf("overlay = %q, want %q", got, want)
	}
	if got := overlayBox([]string{"top"}, nil, 11, 5); !slices.Equal(got, []string{"top"}) {
		t.Fatalf("empty box changed background: %q", got)
	}
	if got := overlayBox(nil, []string{"box"}, 0, 0); len(got) != 20 || got[9] != strings.Repeat(" ", 28)+"box" {
		t.Fatalf("default dimensions overlay = %q", got)
	}
}
