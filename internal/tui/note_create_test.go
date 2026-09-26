package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

func TestNoteCreationTarget(t *testing.T) {
	repositories := []NoteRepository{{ID: "repo-a", Name: "Alpha"}, {ID: "repo-b", Name: "Beta"}}
	for _, tc := range []struct {
		name      string
		focus     string
		items     []NoteRepository
		err       error
		wantID    string
		wantCount int
		wantError string
	}{
		{name: "empty", wantError: "tao init"},
		{name: "unavailable", items: repositories, err: errors.New("private\x1b[31m error"), wantError: "tao repo list"},
		{name: "unavailable focused", focus: "repo-a", items: repositories, err: errors.New("unavailable"), wantError: "tao repo list"},
		{name: "one still requires choice", items: repositories[:1], wantCount: 1},
		{name: "multiple", items: repositories, wantCount: 2},
		{name: "exact focus", focus: "repo-b", items: repositories, wantID: "repo-b"},
		{name: "prefix not focus", focus: "repo", items: repositories, wantError: "clear repository filters"},
		{name: "name not focus", focus: "Alpha", items: repositories, wantError: "clear repository filters"},
		{name: "stale focus", focus: "removed", items: repositories, wantError: "clear repository filters"},
		{name: "stale focus empty inventory", focus: "removed", wantError: "clear repository filters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, picker, err := noteCreationTarget(repositoryFilter(tc.focus), tc.items, tc.err)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(err.Error(), "private") {
					t.Fatalf("error = %v, want actionable %q", err, tc.wantError)
				}
				if picker != nil || target != (NoteRepository{}) {
					t.Fatalf("failure offered a destination: %+v, %+v", target, picker)
				}
				return
			}
			if err != nil || target.ID != tc.wantID {
				t.Fatalf("target = %+v, error = %v", target, err)
			}
			if tc.wantCount > 0 {
				if picker == nil || len(picker.items) != tc.wantCount {
					t.Fatalf("picker = %+v, want %d items", picker, tc.wantCount)
				}
			} else if picker != nil {
				t.Fatal("focused target unexpectedly opened picker")
			}
		})
	}
}

func TestNoteRepositoryPickerInventoryIndependentAndDeterministic(t *testing.T) {
	// No note snapshot, repository root or execution-health evidence is needed.
	items := []NoteRepository{
		{ID: "empty-repo", Name: "Zulu"},
		{ID: "duplicate-b", Name: "Same"},
		{ID: "duplicate-a", Name: "Same"},
	}
	original := append([]NoteRepository(nil), items...)
	_, picker, err := noteCreationTarget(Filter{}, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []NoteRepository{items[2], items[1], items[0]}
	if !reflect.DeepEqual(picker.items, want) || !reflect.DeepEqual(items, original) {
		t.Fatalf("sorted items = %+v; input = %+v", picker.items, items)
	}
	_, reversed, _ := noteCreationTarget(Filter{}, []NoteRepository{items[2], items[0], items[1]}, nil)
	if !reflect.DeepEqual(picker.items, reversed.items) {
		t.Fatal("inventory order changed picker order")
	}
	items[0].ID = "mutated"
	if picker.items[2].ID != "empty-repo" {
		t.Fatal("picker retained mutable inventory backing array")
	}
	text := strings.Join(picker.render(term.Size{Width: 80, Height: 12}, ProfileNone), "\n")
	for _, label := range []string{"Same [duplicate-a]", "Same [duplicate-b]", "Zulu [empty-repo]"} {
		if !strings.Contains(text, label) {
			t.Fatalf("missing %q in picker:\n%s", label, text)
		}
	}
}

func TestNoteRepositoryPickerNavigation(t *testing.T) {
	picker := testNotePicker(10)
	size := term.Size{Width: 80, Height: 7} // Three visible destinations.
	for _, tc := range []struct {
		key      term.KeyEvent
		selected int
		offset   int
	}{
		{term.KeyEvent{Key: term.KeyArrowUp}, 0, 0},
		{term.KeyEvent{Key: term.KeyArrowDown}, 1, 0},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'j'}, 2, 0},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'j'}, 3, 1},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'k'}, 2, 1},
		{term.KeyEvent{Key: term.KeyPageDown}, 5, 3},
		{term.KeyEvent{Key: term.KeyPageDown}, 8, 6},
		{term.KeyEvent{Key: term.KeyPageDown}, 9, 7},
		{term.KeyEvent{Key: term.KeyArrowDown}, 9, 7},
		{term.KeyEvent{Key: term.KeyPageUp}, 6, 6},
		{term.KeyEvent{Key: term.KeyRune, Rune: '/'}, 6, 6},
		{term.KeyEvent{Key: term.KeyRune, Rune: 'n'}, 6, 6},
	} {
		action, target := picker.handleKey(tc.key, size)
		if action != notePickerContinue || target.ID != "" || picker.selected != tc.selected || picker.offset != tc.offset {
			t.Fatalf("key %+v: action %v target %+v selection %d offset %d; want %d/%d", tc.key, action, target, picker.selected, picker.offset, tc.selected, tc.offset)
		}
	}
	action, target := picker.handleKey(term.KeyEvent{Key: term.KeyEnter}, size)
	if action != notePickerSelected || target != picker.items[6] {
		t.Fatalf("selection = %v %+v", action, target)
	}
	for _, bounds := range []int{-100, 100} {
		picker.selected, picker.offset = bounds, bounds
		action, target = picker.handleKey(term.KeyEvent{Key: term.KeyEnter}, size)
		want := picker.items[0]
		if bounds > 0 {
			want = picker.items[9]
		}
		if action != notePickerSelected || target != want {
			t.Fatalf("out-of-bounds selection = %v %+v", action, target)
		}
	}
}

func TestNoteRepositoryPickerCancelAndQuit(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		for _, tc := range []struct {
			key  term.KeyEvent
			want notePickerAction
		}{
			{term.KeyEvent{Key: term.KeyEsc}, notePickerCancelled},
			{term.KeyEvent{Key: term.KeyBackspace}, notePickerCancelled},
			{term.KeyEvent{Key: term.KeyCtrlC}, notePickerQuit},
			{term.KeyEvent{Key: term.KeyRune, Rune: 'q'}, notePickerQuit},
			{term.KeyEvent{Key: term.KeyRune, Rune: 'Q'}, notePickerQuit},
		} {
			picker := testNotePicker(count)
			action, target := picker.handleKey(tc.key, term.Size{Width: 80, Height: 24})
			if action != tc.want || target != (NoteRepository{}) {
				t.Fatalf("count %d key %+v: %v %+v", count, tc.key, action, target)
			}
		}
	}
	for _, count := range []int{0, 1} {
		picker := testNotePicker(count)
		action, target := picker.handleKey(term.KeyEvent{Key: term.KeyEnter}, term.Size{Width: 80, Height: 24})
		if count == 0 && (action != notePickerContinue || target.ID != "") {
			t.Fatal("empty picker selected a destination")
		}
		if count == 1 && (action != notePickerSelected || target != picker.items[0]) {
			t.Fatal("singleton picker did not accept explicit selection")
		}
	}
}

func TestNoteRepositoryPickerRenderingBoundsAndResize(t *testing.T) {
	for _, count := range []int{0, 1, 20} {
		picker := testNotePicker(count)
		picker.selected = max(0, count-1)
		before := picker
		for _, profile := range []Profile{ProfileNone, ProfileANSI16, ProfileTrueColor} {
			for _, width := range []int{-1, 0, 1, 8, 15, 16, 40, 80} {
				for _, height := range []int{-1, 0, 1, 2, 3, 4, 5, 8, 30} {
					size := term.Size{Width: width, Height: height}
					lines := picker.render(size, profile)
					if len(lines) > max(0, height) || (width <= 0 && len(lines) != 0) {
						t.Fatalf("size %+v: %d lines", size, len(lines))
					}
					for _, line := range lines {
						if cells.Width(line) > width {
							t.Fatalf("size %+v overflow: %q", size, line)
						}
					}
					if count > 0 && width >= 40 && height > 0 {
						if !strings.Contains(strings.Join(lines, "\n"), "["+picker.items[count-1].ID+"]") {
							t.Fatalf("resize %+v lost selection: %v", size, lines)
						}
					}
				}
			}
		}
		if !reflect.DeepEqual(picker, before) {
			t.Fatal("render mutated picker")
		}
	}
	picker := testNotePicker(20)
	picker.selected, picker.offset = 19, 18
	picker.clamp(term.Size{Width: 80, Height: 30})
	if picker.offset != 0 || picker.selected != 19 {
		t.Fatalf("expansion did not clamp viewport: %+v", picker)
	}
	picker.clamp(term.Size{Width: 80, Height: 5})
	if picker.offset != 19 || picker.selected != 19 {
		t.Fatalf("shrink did not track selection: %+v", picker)
	}
}

func TestNoteRepositoryPickerSanitizedLabels(t *testing.T) {
	item := NoteRepository{
		ID:   "repo-\x1b[31mred\x1b[0m\n\u202eID",
		Name: "\x1b]52;c;secret\a\x1b[32mHello\x1b[0m\n\t" + strings.Repeat("界", 100),
	}
	name := noteRepositoryName(item)
	if cells.Width(name) > 32 || !strings.Contains(name, "Hello") || !strings.HasSuffix(name, "…") {
		t.Fatalf("unbounded name: %q", name)
	}
	picker := noteRepositoryPicker{items: []NoteRepository{item}}
	for _, line := range picker.render(term.Size{Width: 80, Height: 8}, ProfileNone) {
		if strings.Contains(line, "secret") {
			t.Fatalf("OSC payload leaked: %q", line)
		}
		for _, r := range line {
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				t.Fatalf("unsafe rune %U in %q", r, line)
			}
		}
	}
	action, target := picker.handleKey(term.KeyEvent{Key: term.KeyEnter}, term.Size{Width: 80, Height: 8})
	if action != notePickerSelected || target != item {
		t.Fatal("display sanitization changed target identity")
	}
	for _, width := range []int{1, 8, 30, 80} {
		label := noteRepositoryLabel(NoteRepository{ID: "repo-distinguishing-id", Name: strings.Repeat("n", 200)}, width)
		if cells.Width(label) > width {
			t.Fatalf("label overflow: %q", label)
		}
		if width >= 30 && !strings.Contains(label, "[repo-distinguishing-id]") {
			t.Fatalf("name displaced distinguishing ID: %q", label)
		}
	}
}

func testNotePicker(count int) noteRepositoryPicker {
	picker := noteRepositoryPicker{}
	for index := range count {
		picker.items = append(picker.items, NoteRepository{ID: fmt.Sprintf("repo-%02d", index), Name: "Repository"})
	}
	return picker
}
