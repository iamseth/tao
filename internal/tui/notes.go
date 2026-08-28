package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
)

const (
	maxNoteRepositoryCells = 24
	maxNoteIDCells         = 24
	maxNoteTagsCells       = 32
	maxNotePreviewCells    = 64
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
			repository := boundedNoteValue(warning.RepositoryName, maxNoteRepositoryCells)
			if repository == "-" {
				repository = boundedNoteValue(warning.RepositoryID, maxNoteRepositoryCells)
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
		widths.repository = max(widths.repository, visibleWidth(values.repository))
		widths.id = max(widths.id, visibleWidth(values.id))
		widths.status = max(widths.status, visibleWidth(values.status))
		widths.tags = max(widths.tags, visibleWidth(values.tags))
		widths.updated = max(widths.updated, visibleWidth(values.updated))
	}
	return widths
}

func renderNoteHeader(widths noteTableWidths) string {
	return "  " + strings.Join([]string{
		padCells("REPO", widths.repository),
		padCells("NOTE", widths.id),
		padCells("STATUS", widths.status),
		padCells("TAGS", widths.tags),
		padCells("UPDATED", widths.updated),
		"PREVIEW",
	}, "  ")
}

func renderNoteRow(item note.CatalogNote, now time.Time, widths noteTableWidths, selected, useColor bool) string {
	values := noteValues(item, now)
	cursor := "  "
	if selected {
		cursor = "> "
	}
	status := padCells(values.status, widths.status)
	if useColor {
		status = "\x1b[36m" + status + "\x1b[0m"
	}
	return cursor + strings.Join([]string{
		padCells(values.repository, widths.repository),
		padCells(values.id, widths.id),
		status,
		padCells(values.tags, widths.tags),
		padCells(values.updated, widths.updated),
		values.preview,
	}, "  ")
}

func noteValues(item note.CatalogNote, now time.Time) noteRowValues {
	updated := item.UpdatedAt
	return noteRowValues{
		repository: boundedNoteValue(item.RepositoryName, maxNoteRepositoryCells),
		id:         boundedNoteValue(item.ID, maxNoteIDCells),
		status:     "open",
		tags:       boundedNoteValue(strings.Join(item.Tags, ", "), maxNoteTagsCells),
		updated:    plan.FormatHumanTime(&updated, now),
		preview:    boundedNoteValue(item.Text, maxNotePreviewCells),
	}
}

func boundedNoteValue(value string, limit int) string {
	value = singleLineNoteValue(value)
	if value == "" {
		return "-"
	}
	if visibleWidth(value) <= limit {
		return value
	}
	if limit <= 1 {
		return truncateCells(value, limit)
	}
	return truncateCells(value, limit-1) + "…"
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
