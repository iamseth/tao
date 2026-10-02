package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

func TestChangesFreshnessUsesSharedDurationLabel(t *testing.T) {
	m := DetailChangesModel{Status: "ready", UpdatedAt: time.Unix(100, 0)}
	for _, tc := range []struct {
		now  time.Time
		want string
	}{
		{time.Unix(225, 0), "2m ago"},
		{time.Unix(99, 0), "0s ago"},
		{time.Time{}, ""},
	} {
		text := strings.Join(renderChangesPane(m, 120, 20, theme.Palette{}, tc.now), "\n")
		if tc.want == "" && strings.Contains(text, "ago") || tc.want != "" && !strings.Contains(text, tc.want) {
			t.Fatalf("age %q: %s", tc.want, text)
		}
	}
}

func TestChangesStates(t *testing.T) {
	ready := DetailChangesModel{Status: "ready", Snapshot: DetailChangesSnapshot{Availability: "ready", Scope: "worktree", Signature: "new", Files: []DetailChangedFile{{Path: "a", Display: "a", Status: 'M', Uncommitted: true}}, Dirty: true}, Diff: DetailFileDiff{Path: "a", Signature: "old", Lines: []DetailDiffLine{{Kind: DetailDiffHunk, Text: "@@ -1 +1 @@"}, {Kind: DetailDiffAdd, Text: "+ content"}}, Hunks: []int{0}}}
	for _, tc := range []struct {
		name  string
		model DetailChangesModel
		want  string
	}{
		{"unavailable", DetailChangesModel{}, "Changes unavailable"},
		{"loading", DetailChangesModel{Status: "loading"}, "Loading changes"},
		{"not-ready", DetailChangesModel{Status: "not-ready"}, "Changes not ready"},
		{"empty", DetailChangesModel{Status: "ready"}, "No changes"},
		{"error", DetailChangesModel{Status: "error", RefreshError: "oops"}, "oops"},
		{"ready", ready, "+ content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Join(renderChangesPane(tc.model, 160, 24, theme.Default().Palette(theme.ProfileNone), time.Time{}), "\n")
			if !strings.Contains(text, tc.want) {
				t.Fatalf("missing %q: %s", tc.want, text)
			}
			if strings.Contains(text, "ago") {
				t.Fatal("zero clock age")
			}
		})
	}
	ready.RefreshError = "offline"
	ready.Updating = true
	text := strings.Join(renderChangesPane(ready, 160, 24, theme.Default().Palette(theme.ProfileNone), time.Time{}), "\n")
	for _, want := range []string{"offline", "+ content", "stale", "not reviewed", "hunk 1/1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	ready.Diff.Lines = nil
	ready.Diff.Hunks = nil
	ready.Diff.Binary = true
	ready.RefreshError = ""
	ready.Updating = false
	text = strings.Join(renderChangesPane(ready, 160, 24, theme.Default().Palette(theme.ProfileNone), time.Time{}), "\n")
	if !strings.Contains(text, "Binary file") {
		t.Fatal(text)
	}
	ready.Diff.Binary = false
	ready.Diff.Lines = []DetailDiffLine{{Kind: DetailDiffMeta, Text: "Symlink (not followed)"}}
	ready.Focus = DetailChangesFocusDiff
	text = strings.Join(renderChangesPane(ready, 40, 8, theme.Default().Palette(theme.ProfileNone), time.Time{}), "\n")
	if !strings.Contains(text, "Symlink") || strings.Contains(text, "FILES") {
		t.Fatal(text)
	}
}

func TestChangesGeometryAndCachedOffsets(t *testing.T) {
	for _, width := range []int{40, 60, 84, 100, 160} {
		for _, zoom := range []bool{false, true} {
			g := changesLayout(width, 24, zoom)
			if g.Wide != (width >= 100 && !zoom) || g.Rows != 21 {
				t.Fatalf("geometry: %+v", g)
			}
			if g.Wide && (g.ListWidth < 24 || g.ListWidth > 48 || g.ListWidth+g.DiffWidth+1 != g.Width) {
				t.Fatal(g)
			}
		}
	}
	d := detailState{activeTab: detailTabChanges, changes: DetailChangesModel{Diff: DetailFileDiff{Lines: make([]DetailDiffLine, 4000)}}}
	size := term.Size{Width: 160, Height: 24}
	if got := d.maxOffset(detailTabChanges, size); got != 3985 {
		t.Fatal(got)
	}
	d.moveVertical(10, size)
	d.jumpVertical(true, size)
	if d.activityOffset != 0 || d.overviewOffset != 0 || d.selectedSliceID != "" {
		t.Fatal("Changes leaked into other tab offsets")
	}
}

func TestChangesDefensiveCapsAndColor(t *testing.T) {
	hostile := "\x1b[31m\u202e\xff\t"
	m := boundedDetailChangesModel(DetailChangesModel{Status: "ready", RefreshError: hostile, Snapshot: DetailChangesSnapshot{Files: make([]DetailChangedFile, 401), Warnings: []string{hostile}}, Diff: DetailFileDiff{Lines: make([]DetailDiffLine, 4001)}})
	if len(m.Snapshot.Files) != 400 || !m.Snapshot.FilesTruncated || len(m.Diff.Lines) != 4000 || !m.Diff.Truncated {
		t.Fatal("caps missing")
	}
	m.Snapshot.Files[0] = DetailChangedFile{Path: "a", Display: hostile, Status: 'M'}
	m.Diff = DetailFileDiff{Path: "a", Lines: []DetailDiffLine{{Kind: DetailDiffAdd, Text: strings.Repeat("\t", 1100)}}}
	m = boundedDetailChangesModel(m)
	if utf8.RuneCountInString(m.Diff.Lines[0].Text) > 1024 || !m.Diff.Truncated {
		t.Fatal("rune bound")
	}
	text := strings.Join(renderChangesPane(m, 160, 8, theme.Default().Palette(theme.ProfileTrueColor), time.Time{}), "\n")
	// Renderer-owned SGR is the only escape sequence allowed.
	for i := 0; i < len(text); i++ {
		if text[i] != '\x1b' {
			continue
		}
		i++
		if i >= len(text) || text[i] != '[' {
			t.Fatal("non-SGR escape")
		}
		i++
		for i < len(text) && (text[i] == ';' || text[i] >= '0' && text[i] <= '9') {
			i++
		}
		if i >= len(text) || text[i] != 'm' {
			t.Fatal("non-SGR escape")
		}
	}
	if strings.ContainsAny(text, "\u202e\t") || !utf8.ValidString(text) {
		t.Fatal("hostile color output")
	}
}

func TestChangesFileTruncationRemainsVisible(t *testing.T) {
	files := make([]DetailChangedFile, 401)
	for i := range files {
		path := fmt.Sprintf("file-%03d", i)
		files[i] = DetailChangedFile{Path: path, Display: path, Status: 'M', Uncommitted: true}
	}
	for _, width := range []int{40, 100, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := &detailState{activeTab: detailTabChanges, changes: boundedDetailChangesModel(DetailChangesModel{
				Status: "ready", Focus: DetailChangesFocusFiles,
				Snapshot: DetailChangesSnapshot{Availability: "ready", Scope: "worktree", Files: files,
					Total: DetailChangesStat{Files: 400}},
			})}
			if len(d.changes.Snapshot.Files) != 400 || !d.changes.Snapshot.FilesTruncated {
				t.Fatal("expected capped worktree snapshot")
			}
			s := &loopState{detail: d, size: term.Size{Width: width, Height: 24}}
			assertWarning := func() {
				t.Helper()
				lines := renderChangesPane(d.changes, width, s.size.Height-planDetailFixedLines, theme.Palette{}, time.Time{})
				if !strings.Contains(strings.Join(lines[:min(3, len(lines))], "\n"), "Files truncated") {
					t.Fatalf("missing persistent truncation warning: %v", lines)
				}
			}
			assertWarning()
			if !(App{}).handleChangesKey(s, term.KeyEvent{Key: term.KeyRune, Rune: 'G'}) {
				t.Fatal("G not handled")
			}
			if d.changes.FileIndex != 399 || d.changes.ListOffset == 0 {
				t.Fatalf("did not navigate to last retained file: %+v", d.changes)
			}
			assertWarning()
			text := strings.Join(renderChangesPane(d.changes, width, s.size.Height-planDetailFixedLines, theme.Palette{}, time.Time{}), "\n")
			if !strings.Contains(text, "file-399") {
				t.Fatal("last retained file not visible")
			}
			d.changes.Focus = DetailChangesFocusDiff
			assertWarning()
			d.changes.Zoom = true
			assertWarning()
			d.changes.RefreshError = "offline"
			assertWarning()
			s.size.Height = planDetailFixedLines + 1
			assertWarning()
		})
	}
}

func TestChangesFuncsUnavailable(t *testing.T) {
	f := DetailChangesFuncs{}
	s, err := f.Snapshot(context.Background(), nil, "")
	if err != nil || s.Availability != "unavailable" {
		t.Fatal(s, err)
	}
	d, err := f.FileDiff(context.Background(), s, "raw")
	if err != nil || d.Path != "raw" || len(d.Lines) == 0 {
		t.Fatal(d, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Snapshot(ctx, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestChangesPresentationBounds(t *testing.T) {
	raw := "raw\x1b\t\u202e\xff"
	s := boundedDetailChanges(DetailChangesSnapshot{Availability: "ready", Files: []DetailChangedFile{{Path: raw, Display: raw}}, Reason: raw})
	if s.Files[0].Path != raw {
		t.Fatal("raw identity changed")
	}
	if strings.ContainsAny(s.Files[0].Display+s.Reason, "\x1b\t\u202e") {
		t.Fatal("unsafe display")
	}
	d := boundedDetailFileDiff(DetailFileDiff{Path: raw, Lines: []DetailDiffLine{{Kind: DetailDiffHunk, Text: "@@ unsafe\x1b\t"}}, Hunks: []int{-1, 0, 999}})
	if len(d.Hunks) != 1 || d.Hunks[0] != 0 {
		t.Fatalf("hunks: %v", d.Hunks)
	}
	for _, w := range []int{0, 1, 40, 60, 84, 100, 160} {
		for _, h := range []int{0, 1, 8, 24} {
			lines := renderChangesPane(DetailChangesModel{Status: "ready", Snapshot: s, Diff: d}, w, h, theme.Theme{}.Palette(theme.ProfileNone), time.Time{})
			if len(lines) > h {
				t.Fatalf("height %d: %d", h, len(lines))
			}
			for _, line := range lines {
				if cells.Width(line) > changesLayout(w, h, false).Width || strings.Contains(line, "\x1b") {
					t.Fatalf("unsafe/long %q", line)
				}
			}
		}
	}
}
