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
	for _, want := range []string{"repo alpha", "1 open note", "alpha 1", "TODAY", "REPO", "PREVIEW", "TAG", "AGE", "alpha", "one", "2h", "first line", "Warnings", "damaged store"} {
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

func TestNoteDateBucketsUseLocalCalendarBoundaries(t *testing.T) {
	location := time.FixedZone("local", 5*60*60+30*60)
	at := func(year int, month time.Month, day, hour, minute int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, location).UTC()
	}
	items := []note.CatalogNote{
		{ID: "older-first", UpdatedAt: at(2026, time.August, 16, 23, 59)},
		{ID: "today", UpdatedAt: at(2026, time.August, 19, 0, 0)},
		{ID: "yesterday-late", UpdatedAt: at(2026, time.August, 18, 23, 59)},
		{ID: "week", UpdatedAt: at(2026, time.August, 17, 12, 0)},
		{ID: "yesterday-start", UpdatedAt: at(2026, time.August, 18, 0, 0)},
		{ID: "older-second", UpdatedAt: at(2026, time.August, 1, 12, 0)},
	}

	buckets := noteDateBuckets(items, time.Date(2026, time.August, 19, 0, 30, 0, 0, location))
	wantTitles := []string{"TODAY", "YESTERDAY", "EARLIER THIS WEEK", "OLDER"}
	wantIDs := [][]string{{"today"}, {"yesterday-late", "yesterday-start"}, {"week"}, {"older-first", "older-second"}}
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

func TestNoteDateBucketsStartWeekOnMondayAndOmitEmptyBuckets(t *testing.T) {
	location := time.FixedZone("local", -7*60*60)
	now := time.Date(2026, time.August, 17, 10, 0, 0, 0, location) // Monday.
	buckets := noteDateBuckets([]note.CatalogNote{
		{ID: "monday", UpdatedAt: time.Date(2026, time.August, 17, 0, 0, 0, 0, location)},
		{ID: "sunday", UpdatedAt: time.Date(2026, time.August, 16, 23, 59, 0, 0, location)},
		{ID: "saturday", UpdatedAt: time.Date(2026, time.August, 15, 23, 59, 0, 0, location)},
	}, now)
	if len(buckets) != 3 || buckets[0].title != "TODAY" || buckets[1].title != "YESTERDAY" || buckets[2].title != "OLDER" {
		t.Fatalf("Monday buckets = %+v, want TODAY, YESTERDAY, and OLDER only", buckets)
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

func TestRenderNotesSelectsRowInLaterDateBucket(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	items := []note.CatalogNote{
		{RepositoryID: "repo", RepositoryName: "repo", ID: "today", Text: "today", UpdatedAt: now.Add(-time.Hour)},
		{RepositoryID: "repo", RepositoryName: "repo", ID: "yesterday", Text: "yesterday", UpdatedAt: now.AddDate(0, 0, -1)},
		{RepositoryID: "repo", RepositoryName: "repo", ID: "older", Text: "older", UpdatedAt: now.AddDate(0, 0, -10)},
	}
	frame := Render(Model{Page: PageNotes, NoteSnapshot: note.Snapshot{Notes: items}, Selected: 2, Now: now, Width: 70, Height: 8, Profile: ProfileANSI16})
	for _, want := range []string{"▌ OLDER ", boldSequence + colorSequence(SelectionBackground(ProfileANSI16), true) + "  repo", "older"} {
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
	if !strings.Contains(frame, "selected preview body only visible after opening") {
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
		if visibleWidth(line) > 34 {
			t.Fatalf("notes viewport line exceeds width: %q", line)
		}
	}
}

func TestRenderNotesColumnsAlignAtSupportedWidths(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	item := note.CatalogNote{
		RepositoryID: "repo", RepositoryName: "alpha", ID: "note-hidden",
		Text: "preview text", Tags: []string{"primary", "secondary"}, UpdatedAt: now.Add(-2 * time.Hour),
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
			if visibleWidth(header) != width || visibleWidth(row) != width {
				t.Fatalf("rendered widths at %d = header %d row %d", width, visibleWidth(header), visibleWidth(row))
			}
			for _, pair := range [][2]string{{"REPO", "alpha"}, {"PREVIEW", "preview text"}, {"TAG", "primary"}, {"AGE", "2h"}} {
				if strings.Index(header, pair[0]) != strings.Index(row, pair[1]) {
					t.Errorf("%s column is misaligned at width %d: header=%q row=%q", pair[0], width, header, row)
				}
			}
		})
	}
}

func TestNoteColumnsKeepPreviewAndAgeThroughDeclaredDegradation(t *testing.T) {
	widths := noteTableWidths{repository: 24, preview: 64, primaryTag: 32, age: 3}
	expected := map[int]string{
		100: "REPO,PREVIEW,TAG,AGE",
		80:  "REPO,PREVIEW,TAG,AGE",
		70:  "REPO,PREVIEW,AGE",
		44:  "PREVIEW,AGE",
	}
	for _, width := range []int{100, 80, 70, 44} {
		columns := noteTableColumns(widths, width)
		if got := strings.Join(columnNames(columns), ","); got != expected[width] {
			t.Errorf("width %d columns = %q, want %q", width, got, expected[width])
		}
	}

	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	item := note.CatalogNote{RepositoryName: strings.Repeat("repository", 3), Text: "recognizable preview", Tags: []string{strings.Repeat("tag", 12)}, UpdatedAt: now.Add(-2 * time.Hour)}
	columns := noteTableColumns(measureNoteTable([]note.CatalogNote{item}, now), 44)
	row := renderNoteRow(item, now, columns, noteTablePaneWidth(44, columns), false, ProfileNone)
	if !strings.Contains(row, "recognizable") || !strings.Contains(row, "2h") || strings.Contains(row, "repository") || strings.Contains(row, "tagtag") {
		t.Fatalf("44-cell note row did not preserve preview and age while shedding metadata: %q", row)
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
	tagStart := visibleWidth("  ") + resolved[0].width + columnGapWidth + resolved[1].width + columnGapWidth
	tagCell := string([]rune(plain)[tagStart : tagStart+resolved[2].width])
	if strings.TrimSpace(tagCell) != "" {
		t.Fatalf("untagged note cell = %q, want genuinely empty", tagCell)
	}
	if selected := renderNoteRow(item, now, columns, paneWidth, true, ProfileTrueColor); selected != SelectRow(ProfileTrueColor, styled) {
		t.Fatalf("selected note row does not use shared full-row selection:\n got %q\nwant %q", selected, SelectRow(ProfileTrueColor, styled))
	}

	tagged := item
	tagged.Tags = []string{"primary", "ignored"}
	taggedWidths := measureNoteTable([]note.CatalogNote{tagged}, now)
	taggedColumns := noteTableColumns(taggedWidths, 80)
	taggedRow := renderNoteRow(tagged, now, taggedColumns, noteTablePaneWidth(80, taggedColumns), false, ProfileTrueColor)
	if !strings.Contains(taggedRow, Paint(ProfileTrueColor, RoleAccent, "primary")) || strings.Contains(taggedRow, "ignored") {
		t.Fatalf("note row does not paint only the primary tag: %q", taggedRow)
	}
}

func TestNoteTierColumnAppearsOnlyWithTierTags(t *testing.T) {
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	untagged := note.CatalogNote{RepositoryName: "repo", Text: "plain backlog item", Tags: []string{"workflow"}, UpdatedAt: now.Add(-time.Hour)}
	columns := noteTableColumns(measureNoteTable([]note.CatalogNote{untagged}, now), 100)
	if got := strings.Join(columnNames(columns), ","); got != "REPO,PREVIEW,TAG,AGE" {
		t.Fatalf("columns without tier tags = %q, want no TIER column", got)
	}

	tiered := note.CatalogNote{RepositoryName: "repo", Text: "tiered work", Tags: []string{"arch-2026-09", "tier1"}, UpdatedAt: now.Add(-time.Hour)}
	items := []note.CatalogNote{untagged, tiered}
	columns = noteTableColumns(measureNoteTable(items, now), 100)
	if got := strings.Join(columnNames(columns), ","); got != "REPO,TIER,PREVIEW,TAG,AGE" {
		t.Fatalf("columns with a tier tag = %q, want TIER after REPO", got)
	}
	paneWidth := noteTablePaneWidth(100, columns)
	row := renderNoteRow(tiered, now, columns, paneWidth, false, ProfileTrueColor)
	if !strings.Contains(row, Paint(ProfileTrueColor, RoleInfo, "tier1")) {
		t.Fatalf("tier cell lacks tier value with info paint: %q", row)
	}
	if !strings.Contains(row, Paint(ProfileTrueColor, RoleAccent, "arch-2026-09")) {
		t.Fatalf("tag cell should show the first non-tier tag: %q", row)
	}
	header := renderNoteHeader(columns, paneWidth)
	plain := renderNoteRow(tiered, now, columns, paneWidth, false, ProfileNone)
	if strings.Index(header, "TIER") != strings.Index(plain, "tier1") {
		t.Fatalf("TIER column misaligned: header=%q row=%q", header, plain)
	}
	if withoutTier := renderNoteRow(untagged, now, columns, paneWidth, false, ProfileNone); strings.Contains(withoutTier, "tier") {
		t.Fatalf("tierless row should leave the TIER cell empty: %q", withoutTier)
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
