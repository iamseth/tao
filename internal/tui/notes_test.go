package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/note"
)

func TestRenderNotesRowsWarningsFocusAndSanitization(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := note.Snapshot{
		Notes: []note.CatalogNote{
			{RepositoryID: "repo-a", RepositoryName: "alpha\x1b]0;owned\a", ID: "note-α\x1b[31m", Text: "first\nline \x1b[2J中 " + strings.Repeat("x", 100), Tags: []string{"one", "ta\x1b]52;c;bad\ag"}, UpdatedAt: now.Add(-2 * time.Hour)},
			{RepositoryID: "repo-b", RepositoryName: "beta", ID: "note-b", Text: "other", UpdatedAt: now.Add(-time.Minute)},
		},
		Warnings: []note.CatalogWarning{
			{RepositoryID: "repo-a", RepositoryName: "alpha", Err: errors.New("damaged\x1b[2J store")},
			{RepositoryID: "repo-b", RepositoryName: "beta", Err: errors.New("other warning")},
		},
	}

	frame := Render(Model{Page: PageNotes, NoteSnapshot: snapshot, Now: now, FocusRepositoryID: "repo-a", FocusRepositoryName: "alpha\x1b]0;title\a"})
	body := strings.TrimPrefix(frame, clearScreenSequence)
	for _, want := range []string{"repo alpha", "1 open note", "REPO", "NOTE", "STATUS", "TAGS", "UPDATED", "PREVIEW", "> alpha", "note-α", "open", "one, tag", "2h", "first line", "Warnings", "damaged store"} {
		if !strings.Contains(body, want) {
			t.Fatalf("notes frame missing %q:\n%s", want, frame)
		}
	}
	for _, absent := range []string{"beta", "other warning", "[31m", "[2J", "]0;", "]52;", strings.Repeat("x", 80)} {
		if strings.Contains(body, absent) {
			t.Fatalf("notes frame retained hidden or unsafe content %q:\n%s", absent, frame)
		}
	}
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("notes frame contains control %U: %q", r, body)
		}
	}
}

func TestBoundedNoteValueTruncatesByCells(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "wide", value: "日本語", limit: 5, want: "日本…"},
		{name: "combining", value: "e\u0301abcdef", limit: 4, want: "e\u0301ab…"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := boundedNoteValue(test.value, test.limit); got != test.want {
				t.Fatalf("boundedNoteValue(%q, %d) = %q, want %q", test.value, test.limit, got, test.want)
			}
		})
	}
}

func TestRenderNotesEmptyAndSelectedViewport(t *testing.T) {
	empty := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Warnings: []note.CatalogWarning{{RepositoryID: "repo", RepositoryName: "repo", Err: errors.New("unreadable")}}}})
	if !strings.Contains(empty, "0 open notes") || !strings.Contains(empty, "No open notes") || !strings.Contains(empty, "unreadable") {
		t.Fatalf("empty warning frame is incomplete:\n%s", empty)
	}

	items := make([]note.CatalogNote, 20)
	for index := range items {
		items[index] = note.CatalogNote{RepositoryID: "repo", RepositoryName: "repo", ID: fmt.Sprintf("note-%02d", index), Text: "preview"}
	}
	frame := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Notes: items}, Selected: 17, Width: 34, Height: 7})
	lines := renderedLines(frame)
	if len(lines) != 7 || !strings.Contains(frame, "> repo") || !strings.Contains(frame, "note-17") {
		t.Fatalf("notes viewport lost selection: %#v", lines)
	}
	for _, line := range lines {
		if visibleWidth(line) > 34 {
			t.Fatalf("notes viewport line exceeds width: %q", line)
		}
	}
}
