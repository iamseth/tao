package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
)

const (
	maxNoteRepositoryRunes = 24
	maxNoteIDRunes         = 24
	maxNoteTagsRunes       = 32
	maxNotePreviewRunes    = 64
)

type noteRowValues struct {
	repository string
	id         string
	status     string
	tags       string
	updated    string
	preview    string
}

type noteTableWidths struct {
	repository int
	id         int
	status     int
	tags       int
	updated    int
}

func visibleNotes(snapshot note.Snapshot, focusRepositoryID string) []note.CatalogNote {
	if focusRepositoryID == "" {
		return snapshot.Notes
	}
	visible := make([]note.CatalogNote, 0, len(snapshot.Notes))
	for _, item := range snapshot.Notes {
		if item.RepositoryID == focusRepositoryID {
			visible = append(visible, item)
		}
	}
	return visible
}

func visibleNoteWarnings(snapshot note.Snapshot, focusRepositoryID string) []note.CatalogWarning {
	if focusRepositoryID == "" {
		return snapshot.Warnings
	}
	visible := make([]note.CatalogWarning, 0, len(snapshot.Warnings))
	for _, warning := range snapshot.Warnings {
		if warning.RepositoryID == focusRepositoryID {
			visible = append(visible, warning)
		}
	}
	return visible
}

func renderNotesPage(snapshot note.Snapshot, selected int, focusRepositoryID string, now time.Time, useColor bool) (lines []string, selectedLine int) {
	items := visibleNotes(snapshot, focusRepositoryID)
	warnings := visibleNoteWarnings(snapshot, focusRepositoryID)
	selectedLine = -1
	if len(items) == 0 {
		lines = append(lines, "", "  Notes page. No open notes.")
	} else {
		widths := measureNoteTable(items, now)
		lines = append(lines, "", renderNoteHeader(widths))
		for index, item := range items {
			if index == selected {
				selectedLine = len(lines)
			}
			lines = append(lines, renderNoteRow(item, now, widths, index == selected, useColor))
		}
	}
	if len(warnings) > 0 {
		lines = append(lines, "", "Warnings")
		for _, warning := range warnings {
			repository := boundedNoteValue(warning.RepositoryName, maxNoteRepositoryRunes)
			if repository == "-" {
				repository = boundedNoteValue(warning.RepositoryID, maxNoteRepositoryRunes)
			}
			message := singleLineDetail(warning.Error())
			lines = append(lines, "  "+repository+": "+displayValue(message))
		}
	}
	return lines, selectedLine
}

func measureNoteTable(items []note.CatalogNote, now time.Time) noteTableWidths {
	widths := noteTableWidths{
		repository: len("REPO"), id: len("NOTE"), status: len("STATUS"),
		tags: len("TAGS"), updated: len("UPDATED"),
	}
	for _, item := range items {
		values := noteValues(item, now)
		widths.repository = max(widths.repository, utf8.RuneCountInString(values.repository))
		widths.id = max(widths.id, utf8.RuneCountInString(values.id))
		widths.status = max(widths.status, utf8.RuneCountInString(values.status))
		widths.tags = max(widths.tags, utf8.RuneCountInString(values.tags))
		widths.updated = max(widths.updated, utf8.RuneCountInString(values.updated))
	}
	return widths
}

func renderNoteHeader(widths noteTableWidths) string {
	return "  " + strings.Join([]string{
		padRunes("REPO", widths.repository),
		padRunes("NOTE", widths.id),
		padRunes("STATUS", widths.status),
		padRunes("TAGS", widths.tags),
		padRunes("UPDATED", widths.updated),
		"PREVIEW",
	}, "  ")
}

func renderNoteRow(item note.CatalogNote, now time.Time, widths noteTableWidths, selected, useColor bool) string {
	values := noteValues(item, now)
	cursor := "  "
	if selected {
		cursor = "> "
	}
	status := padRunes(values.status, widths.status)
	if useColor {
		status = "\x1b[36m" + status + "\x1b[0m"
	}
	return cursor + strings.Join([]string{
		padRunes(values.repository, widths.repository),
		padRunes(values.id, widths.id),
		status,
		padRunes(values.tags, widths.tags),
		padRunes(values.updated, widths.updated),
		values.preview,
	}, "  ")
}

func noteValues(item note.CatalogNote, now time.Time) noteRowValues {
	updated := item.UpdatedAt
	return noteRowValues{
		repository: boundedNoteValue(item.RepositoryName, maxNoteRepositoryRunes),
		id:         boundedNoteValue(item.ID, maxNoteIDRunes),
		status:     "open",
		tags:       boundedNoteValue(strings.Join(item.Tags, ", "), maxNoteTagsRunes),
		updated:    plan.FormatHumanTime(&updated, now),
		preview:    boundedNoteValue(item.Text, maxNotePreviewRunes),
	}
}

func boundedNoteValue(value string, limit int) string {
	value = singleLineNoteValue(value)
	if value == "" {
		return "-"
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func singleLineNoteValue(value string) string {
	return strings.Join(strings.Fields(sanitizeNoteText(value)), " ")
}

func noteIdentity(item note.CatalogNote) string {
	return item.RepositoryID + "\x00" + item.ID
}

func noteCountLabel(count int) string {
	if count == 1 {
		return "1 open note"
	}
	return fmt.Sprintf("%d open notes", count)
}
