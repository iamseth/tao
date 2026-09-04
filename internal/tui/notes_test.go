package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term/cells"
)

func TestRenderNotesRowsWarningsFocusAndSanitization(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := note.Snapshot{
		Notes: []note.CatalogNote{
			{RepositoryID: "repo-a", RepositoryName: "alpha\x1b]0;owned\a", ID: "note-α\x1b[31m", Text: "first\nline \x1b[2J中 " + strings.Repeat("x", 100), Tags: []string{"one", "ta\x1b]52;c;bad\ag"}, CreatedAt: now.Add(-10 * 24 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
			{RepositoryID: "repo-b", RepositoryName: "beta", ID: "note-b", Text: "other", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
		},
		Warnings: []note.CatalogWarning{
			{RepositoryID: "repo-a", RepositoryName: "alpha", Err: errors.New("damaged\x1b[2J store")},
			{RepositoryID: "repo-b", RepositoryName: "beta", Err: errors.New("other warning")},
		},
	}

	frame := Render(Model{Page: PageNotes, NoteSnapshot: snapshot, Now: now, FocusRepositoryID: "repo-a", FocusRepositoryName: "alpha\x1b]0;title\a"})
	body := strings.TrimPrefix(frame, clearScreenSequence)
	for _, want := range []string{"repo alpha", "1 open note", "alpha 1", "UNTIERED", "REPO", "PREVIEW", "TAGS", "CREATED", "UPDATED", "alpha", "one, tag", "1w", "2h", "first line", "Warnings", "damaged store"} {
		if !strings.Contains(body, want) {
			t.Fatalf("notes frame missing %q:\n%s", want, frame)
		}
	}
	for _, absent := range []string{"STATUS", "beta", "other warning", "[31m", "[2J", "]0;", "]52;", strings.Repeat("x", 80)} {
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

func TestNoteTierBucketsUseAscendingPriorityAndOmitEmptyTiers(t *testing.T) {
	items := visibleNotes(note.Snapshot{Notes: []note.CatalogNote{
		{ID: "tier3", Tags: []string{"tier3"}},
		{ID: "untiered", Tags: []string{"workflow"}},
		{ID: "tier0", Tags: []string{"tier0"}},
		{ID: "tier1-first", Tags: []string{"tier1"}},
		{ID: "tier1-second", Tags: []string{"tier1"}},
		{ID: "lowest-declared-tier", Tags: []string{"tier4", "tier2"}},
	}}, "")
	buckets := noteTierBuckets(items)
	wantTitles := []string{"TIER 0", "TIER 1", "TIER 2", "TIER 3", "UNTIERED"}
	wantIDs := [][]string{{"tier0"}, {"tier1-first", "tier1-second"}, {"lowest-declared-tier"}, {"tier3"}, {"untiered"}}
	if len(buckets) != len(wantTitles) {
		t.Fatalf("bucket count = %d, want %d: %+v", len(buckets), len(wantTitles), buckets)
	}
	for index, bucket := range buckets {
		if bucket.title != wantTitles[index] {
			t.Errorf("bucket %d title = %q, want %q", index, bucket.title, wantTitles[index])
		}
		var gotIDs []string
		for _, indexed := range bucket.items {
			gotIDs = append(gotIDs, indexed.item.ID)
		}
		if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs[index]) {
			t.Errorf("bucket %q IDs = %v, want %v", bucket.title, gotIDs, wantIDs[index])
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

func TestRenderNotesSelectsRowInLaterTierBucket(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	items := []note.CatalogNote{
		{RepositoryID: "repo", RepositoryName: "repo", ID: "today", Text: "today", UpdatedAt: now.Add(-time.Hour)},
		{RepositoryID: "repo", RepositoryName: "repo", ID: "yesterday", Text: "yesterday", UpdatedAt: now.AddDate(0, 0, -1)},
		{RepositoryID: "repo", RepositoryName: "repo", ID: "older", Text: "older", UpdatedAt: now.AddDate(0, 0, -10)},
	}
	frame := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Notes: items}, Selected: 2, Now: now, Width: 70, Height: 8, Profile: ProfileANSI16})
	for _, want := range []string{"▌ UNTIERED ", boldSequence + colorSequence(SelectionBackground(ProfileANSI16), true) + "  repo", "older"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("later-bucket selection missing %q:\n%s", want, frame)
		}
	}
}

func TestRenderNotesListOmitsSelectedNoteDetails(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	item := note.CatalogNote{
		RepositoryID: "repo", RepositoryName: "repo", ID: "older",
		Text: "selected preview\nbody only visible after opening", Tags: []string{"one", "two"}, UpdatedAt: now.AddDate(0, 0, -10),
	}
	frame := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Notes: []note.CatalogNote{item}}, Now: now, Width: 80, Height: 20})
	if !strings.Contains(frame, "selected preview body only visible after") {
		t.Fatalf("selected note row is missing:\n%s", frame)
	}
	for _, unwanted := range []string{"╭", "Note ID:", "Tags:", "Body:"} {
		if strings.Contains(frame, unwanted) {
			t.Fatalf("notes list includes selected-note detail %q:\n%s", unwanted, frame)
		}
	}
}

func TestRenderNotesEmptyAndSelectedViewport(t *testing.T) {
	empty := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Warnings: []note.CatalogWarning{{RepositoryID: "repo", RepositoryName: "repo", Err: errors.New("unreadable")}}}})
	if !strings.Contains(empty, "0 open notes") || !strings.Contains(empty, "No open notes") || !strings.Contains(empty, "unreadable") {
		t.Fatalf("empty warning frame is incomplete:\n%s", empty)
	}

	items := make([]note.CatalogNote, 20)
	for index := range items {
		items[index] = note.CatalogNote{RepositoryID: "repo", RepositoryName: "repo", ID: fmt.Sprintf("note-%02d", index), Text: fmt.Sprintf("preview-%02d", index)}
	}
	frame := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Notes: items}, Selected: 17, Width: 34, Height: 7})
	lines := renderedLines(frame)
	if len(lines) != 7 || !strings.Contains(frame, "preview-17") {
		t.Fatalf("notes viewport lost selection: %#v", lines)
	}
	for _, line := range lines {
		if cells.Width(line) > 34 {
			t.Fatalf("notes viewport line exceeds width: %q", line)
		}
	}
}

func TestRenderNotesColumnsAlignAtSupportedWidths(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	item := note.CatalogNote{
		RepositoryID: "repo", RepositoryName: "alpha", ID: "note-hidden",
		Text: "preview text", Tags: []string{"primary", "secondary"}, CreatedAt: now.Add(-10 * 24 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}
	widths := measureNoteTable([]note.CatalogNote{item}, now)
	for _, width := range []int{199, 120, 100, 80, 70} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			columns := noteTableColumns(widths, width)
			paneWidth := noteTablePaneWidth(width, columns)
			header := renderNoteHeader(columns, paneWidth)
			row := renderNoteRow(item, now, columns, paneWidth, false, ProfileNone)
			if strings.Contains(header, "NOTE") || strings.Contains(header, "STATUS") {
				t.Fatalf("legacy columns remain at width %d: %q", width, header)
			}
			if cells.Width(header) != width || cells.Width(row) != width {
				t.Fatalf("rendered widths at %d = header %d row %d", width, cells.Width(header), cells.Width(row))
			}
			for _, pair := range [][2]string{{"REPO", "alpha"}, {"PREVIEW", "preview text"}, {"TAGS", "primary, secondary"}, {"CREATED", "1w"}, {"UPDATED", "2h"}} {
				if strings.Index(header, pair[0]) != strings.Index(row, pair[1]) {
					t.Errorf("%s column is misaligned at width %d: header=%q row=%q", pair[0], width, header, row)
				}
			}
		})
	}
}

func TestNoteColumnsKeepPreviewTagsAndAgesThroughDeclaredDegradation(t *testing.T) {
	widths := noteTableWidths{repository: 24, preview: 64, tags: 64, created: 7, updated: 7}
	expected := map[int]string{
		100: "REPO,PREVIEW,TAGS,CREATED,UPDATED",
		80:  "REPO,PREVIEW,TAGS,CREATED,UPDATED",
		70:  "REPO,PREVIEW,TAGS,CREATED,UPDATED",
		44:  "PREVIEW,TAGS,CREATED,UPDATED",
	}
	for _, width := range []int{100, 80, 70, 44} {
		columns := noteTableColumns(widths, width)
		if got := strings.Join(columnNames(columns), ","); got != expected[width] {
			t.Errorf("width %d columns = %q, want %q", width, got, expected[width])
		}
	}

	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	item := note.CatalogNote{RepositoryName: strings.Repeat("repository", 3), Text: "recognizable preview", Tags: []string{"one", "two"}, CreatedAt: now.Add(-10 * 24 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}
	columns := noteTableColumns(measureNoteTable([]note.CatalogNote{item}, now), 44)
	row := renderNoteRow(item, now, columns, noteTablePaneWidth(44, columns), false, ProfileNone)
	if !strings.Contains(row, "recognizable") || !strings.Contains(row, "one") || !strings.Contains(row, "1w") || !strings.Contains(row, "2h") || strings.Contains(row, "repository") {
		t.Fatalf("44-cell note row did not preserve preview, tags, and both ages while shedding repository metadata: %q", row)
	}
}

func TestRenderNoteRowUsesSemanticPaintSelectionAndEmptyTagCell(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	item := note.CatalogNote{RepositoryName: "repo", Text: "preview", UpdatedAt: now.Add(-time.Hour)}
	widths := measureNoteTable([]note.CatalogNote{item}, now)
	columns := noteTableColumns(widths, 80)
	paneWidth := noteTablePaneWidth(80, columns)
	styled := renderNoteRow(item, now, columns, paneWidth, false, ProfileTrueColor)
	if !strings.Contains(styled, Paint(ProfileTrueColor, RoleNeutral4, "preview")) {
		t.Fatalf("preview lacks neutral semantic paint: %q", styled)
	}

	plain := renderNoteRow(item, now, columns, paneWidth, false, ProfileNone)
	resolved := resolveColumns(columns, paneWidth)
	offset := cells.Width("  ")
	for _, resolvedColumn := range resolved {
		if resolvedColumn.name == "TAGS" {
			tagCell := string([]rune(plain)[offset : offset+resolvedColumn.width])
			if strings.TrimSpace(tagCell) != "" {
				t.Fatalf("untagged note cell = %q, want genuinely empty", tagCell)
			}
			break
		}
		offset += resolvedColumn.width + columnGapWidth
	}
	if selected := renderNoteRow(item, now, columns, paneWidth, true, ProfileTrueColor); selected != SelectRow(ProfileTrueColor, styled) {
		t.Fatalf("selected note row does not use shared full-row selection:\n got %q\nwant %q", selected, SelectRow(ProfileTrueColor, styled))
	}

	tagged := item
	tagged.Tags = []string{"primary", "secondary"}
	taggedWidths := measureNoteTable([]note.CatalogNote{tagged}, now)
	taggedColumns := noteTableColumns(taggedWidths, 80)
	taggedRow := renderNoteRow(tagged, now, taggedColumns, noteTablePaneWidth(80, taggedColumns), false, ProfileTrueColor)
	if !strings.Contains(taggedRow, Paint(ProfileTrueColor, RoleAccent, "primary, secondary")) {
		t.Fatalf("note row does not paint all tags: %q", taggedRow)
	}
}

func TestNoteTierSectionsReplaceTierColumn(t *testing.T) {
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	untagged := note.CatalogNote{RepositoryName: "repo", Text: "plain backlog item", Tags: []string{"workflow"}, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-time.Hour)}
	tiered := note.CatalogNote{RepositoryName: "repo", Text: "tiered work", Tags: []string{"arch-2026-09", "tier1", "workflow"}, CreatedAt: now.Add(-7 * 24 * time.Hour), UpdatedAt: now.Add(-time.Hour)}
	items := []note.CatalogNote{untagged, tiered}
	columns := noteTableColumns(measureNoteTable(items, now), 100)
	if got := strings.Join(columnNames(columns), ","); got != "REPO,PREVIEW,TAGS,CREATED,UPDATED" {
		t.Fatalf("note columns = %q, want tier-free columns", got)
	}
	paneWidth := noteTablePaneWidth(100, columns)
	row := renderNoteRow(tiered, now, columns, paneWidth, false, ProfileTrueColor)
	if strings.Contains(row, "tier1") {
		t.Fatalf("tiered row redundantly displays its section tag: %q", row)
	}
	if !strings.Contains(row, Paint(ProfileTrueColor, RoleAccent, "arch-2026-09, workflow")) {
		t.Fatalf("tag cell should show all non-tier tags: %q", row)
	}
	frame := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Notes: items}, Now: now, Width: 100, Height: 20})
	if !strings.Contains(frame, "▌ TIER 1 ") || !strings.Contains(frame, "▌ UNTIERED ") {
		t.Fatalf("note tiers did not render as sections:\n%s", frame)
	}
}

func TestIsNoteTierTagRecognizesDigitSuffixOnly(t *testing.T) {
	for tag, want := range map[string]bool{
		"tier0": true, "tier3": true, "tier12": true,
		"tier": false, "tiered": false, "tier-1": false, "Tier1": false, "notier1": false,
	} {
		if got := isNoteTierTag(tag); got != want {
			t.Errorf("isNoteTierTag(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestVisibleNotesOrderTierFirstThenRecency(t *testing.T) {
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	snapshot := note.Snapshot{Notes: []note.CatalogNote{
		{ID: "tier3-new", Tags: []string{"tier3"}, UpdatedAt: now.Add(-time.Minute)},
		{ID: "untiered-new", Tags: []string{"workflow"}, UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "tier0-old", Tags: []string{"arch-2026-09", "tier0"}, UpdatedAt: now.Add(-3 * time.Hour)},
		{ID: "tier1-newer", Tags: []string{"tier1"}, UpdatedAt: now.Add(-time.Hour)},
		{ID: "tier1-older", Tags: []string{"tier1"}, UpdatedAt: now.Add(-2 * time.Hour)},
	}}
	got := make([]string, 0, len(snapshot.Notes))
	for _, item := range visibleNotes(snapshot, "") {
		got = append(got, item.ID)
	}
	want := []string{"tier0-old", "tier1-newer", "tier1-older", "tier3-new", "untiered-new"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("visible note order = %v, want %v", got, want)
	}
}
