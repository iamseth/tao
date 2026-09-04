package tui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term/cells"
)

const (
	maxNoteRepositoryCells  = 24
	maxNoteTagsCells        = 64
	maxNotePreviewCells     = 64
	minimumNotePreviewCells = 12
	minimumNoteTagsCells    = 8
)

type noteRowValues struct {
	repository string
	preview    string
	tags       string
	created    string
	updated    string
}

type noteTableWidths struct {
	repository int
	preview    int
	tags       int
	created    int
	updated    int
}

type indexedNote struct {
	item  note.CatalogNote
	index int
}

type noteTierBucket struct {
	title string
	items []indexedNote
}

func visibleNotes(snapshot note.Snapshot, focusRepositoryID string) []note.CatalogNote {
	visible := make([]note.CatalogNote, 0, len(snapshot.Notes))
	for _, item := range snapshot.Notes {
		if focusRepositoryID == "" || item.RepositoryID == focusRepositoryID {
			visible = append(visible, item)
		}
	}
	// Stable tier ordering on top of the collector's recency order: lower
	// tiers first, untiered notes last, recency preserved within a tier.
	sort.SliceStable(visible, func(i, j int) bool {
		return noteTierRank(visible[i]) < noteTierRank(visible[j])
	})
	return visible
}

// noteTierRank orders tiered notes ahead of untiered ones, lowest tier first.
func noteTierRank(item note.CatalogNote) int {
	rank := math.MaxInt
	for _, tag := range item.Tags {
		if !isNoteTierTag(tag) {
			continue
		}
		if candidate, err := strconv.Atoi(strings.TrimPrefix(tag, "tier")); err == nil {
			rank = min(rank, candidate)
		}
	}
	return rank
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

func renderNotesPage(snapshot note.Snapshot, selected int, focusRepositoryID string, now time.Time, model Model) (lines []string, selectedLine int, metadata tableViewportMetadata) {
	items := visibleNotes(snapshot, focusRepositoryID)
	warnings := visibleNoteWarnings(snapshot, focusRepositoryID)
	selectedLine = -1
	if len(items) == 0 {
		sectionWidth := dashboardSectionWidth(model, PageNotes, "OPEN NOTES", 0)
		lines = append(lines, "", sectionTitleRule(model.Profile, RoleAccent, "OPEN NOTES", sectionWidth), "  Notes page. No open notes.")
		metadata.sections = append(metadata.sections, tableViewportSection{headingLines: []int{1}, contentLines: []int{2}})
	} else {
		widths := measureNoteTable(items, now)
		columns := noteTableColumns(widths, model.Width)
		paneWidth := noteTablePaneWidth(model.Width, columns)
		for _, bucket := range noteTierBuckets(items) {
			sectionWidth := dashboardSectionWidth(model, PageNotes, bucket.title, 0)
			lines = append(lines, "", sectionTitleRule(model.Profile, RoleAccent, bucket.title, sectionWidth), renderNoteHeader(columns, paneWidth))
			section := tableViewportSection{headingLines: []int{len(lines) - 2, len(lines) - 1}}
			for _, indexed := range bucket.items {
				isSelected := indexed.index == selected
				if isSelected {
					selectedLine = len(lines)
				}
				section.contentLines = append(section.contentLines, len(lines))
				lines = append(lines, renderNoteRow(indexed.item, now, columns, paneWidth, isSelected, model.Profile))
			}
			metadata.sections = append(metadata.sections, section)
		}
	}
	if len(warnings) > 0 {
		sectionWidth := dashboardSectionWidth(model, PageNotes, "Warnings", 0)
		lines = append(lines, "", sectionTitleRule(model.Profile, RoleWarn, "Warnings", sectionWidth))
		section := tableViewportSection{headingLines: []int{len(lines) - 1}}
		for _, warning := range warnings {
			repository := boundedNoteValue(warning.RepositoryName, maxNoteRepositoryCells)
			if repository == "-" {
				repository = boundedNoteValue(warning.RepositoryID, maxNoteRepositoryCells)
			}
			message := singleLineDetail(warning.Error())
			section.contentLines = append(section.contentLines, len(lines))
			lines = append(lines, "  "+repository+": "+displayValue(message))
		}
		metadata.sections = append(metadata.sections, section)
	}
	return lines, selectedLine, metadata
}

func noteTierBuckets(items []note.CatalogNote) []noteTierBucket {
	var buckets []noteTierBucket
	for index, item := range items {
		rank := noteTierRank(item)
		title := "UNTIERED"
		if rank != math.MaxInt {
			title = fmt.Sprintf("TIER %d", rank)
		}
		if len(buckets) == 0 || buckets[len(buckets)-1].title != title {
			buckets = append(buckets, noteTierBucket{title: title})
		}
		last := len(buckets) - 1
		buckets[last].items = append(buckets[last].items, indexedNote{item: item, index: index})
	}
	return buckets
}

func noteRepositoryBreakdown(items []note.CatalogNote) string {
	type repositoryCount struct {
		id    string
		name  string
		count int
	}
	counts := make(map[string]*repositoryCount)
	for _, item := range items {
		key := item.RepositoryID
		if key == "" {
			key = item.RepositoryName
		}
		count, ok := counts[key]
		if !ok {
			name := boundedNoteValue(item.RepositoryName, maxNoteRepositoryCells)
			if name == "-" {
				name = boundedNoteValue(item.RepositoryID, maxNoteRepositoryCells)
			}
			count = &repositoryCount{id: item.RepositoryID, name: name}
			counts[key] = count
		}
		count.count++
	}
	ordered := make([]repositoryCount, 0, len(counts))
	for _, count := range counts {
		ordered = append(ordered, *count)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].name != ordered[j].name {
			return ordered[i].name < ordered[j].name
		}
		return ordered[i].id < ordered[j].id
	})
	parts := make([]string, 0, len(ordered))
	for _, count := range ordered {
		parts = append(parts, fmt.Sprintf("%s %d", count.name, count.count))
	}
	return strings.Join(parts, " · ")
}

func measureNoteTable(items []note.CatalogNote, now time.Time) noteTableWidths {
	widths := noteTableWidths{
		repository: len("REPO"), preview: len("PREVIEW"), tags: len("TAGS"),
		created: len("CREATED"), updated: len("UPDATED"),
	}
	for _, item := range items {
		values := noteValues(item, now)
		widths.repository = max(widths.repository, cells.Width(values.repository))
		widths.preview = max(widths.preview, cells.Width(values.preview))
		widths.tags = max(widths.tags, cells.Width(values.tags))
		widths.created = max(widths.created, cells.Width(values.created))
		widths.updated = max(widths.updated, cells.Width(values.updated))
	}
	return widths
}

func noteTableColumns(widths noteTableWidths, frameWidth int) []column {
	columns := []column{
		{name: "REPO", width: widths.repository, priority: 20},
		{name: "PREVIEW", width: widths.preview, flex: true, required: true, priority: 40, minimum: minimumNotePreviewCells},
		{name: "TAGS", width: widths.tags, flex: true, required: true, priority: 40, minimum: minimumNoteTagsCells},
		{name: "CREATED", width: widths.created, required: true, priority: 40},
		{name: "UPDATED", width: widths.updated, required: true, priority: 40},
	}
	if frameWidth <= 0 {
		return columns
	}
	return fitColumns(columns, max(frameWidth-cells.Width("  "), 0))
}

func noteTablePaneWidth(frameWidth int, columns []column) int {
	if frameWidth > 0 {
		return max(frameWidth-cells.Width("  "), 0)
	}
	return columnsWidth(columns)
}

func renderNoteHeader(columns []column, paneWidth int) string {
	headers := make([]string, len(columns))
	for index, item := range columns {
		headers[index] = item.name
	}
	return "  " + joinRow(columns, headers, paneWidth)
}

func renderNoteRow(item note.CatalogNote, now time.Time, columns []column, paneWidth int, selected bool, profile Profile) string {
	values := noteValues(item, now)
	rowCells := make([]string, 0, len(columns))
	for _, item := range columns {
		switch item.name {
		case "REPO":
			rowCells = append(rowCells, values.repository)
		case "PREVIEW":
			rowCells = append(rowCells, Paint(profile, RoleNeutral4, values.preview))
		case "TAGS":
			rowCells = append(rowCells, Paint(profile, RoleAccent, values.tags))
		case "CREATED":
			rowCells = append(rowCells, values.created)
		case "UPDATED":
			rowCells = append(rowCells, values.updated)
		}
	}
	line := "  " + joinRow(columns, rowCells, paneWidth)
	if paneWidth > 0 {
		line = cells.Pad(line, paneWidth+cells.Width("  "))
	}
	if selected {
		line = SelectRow(profile, line)
	}
	return line
}

func noteValues(item note.CatalogNote, now time.Time) noteRowValues {
	tags := make([]string, 0, len(item.Tags))
	for _, tag := range item.Tags {
		if !isNoteTierTag(tag) {
			if tag = singleLineNoteValue(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	return noteRowValues{
		repository: boundedNoteValue(item.RepositoryName, maxNoteRepositoryCells),
		preview:    boundedNoteValue(item.Text, maxNotePreviewCells),
		tags:       cells.TruncateEllipsis(strings.Join(tags, ", "), maxNoteTagsCells),
		created:    noteRelativeAge(item.CreatedAt, now),
		updated:    noteRelativeAge(item.UpdatedAt, now),
	}
}

func noteRelativeAge(value, now time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return relativeAge(&value, now)
}

// isNoteTierTag recognizes the priority convention "tier<digits>" used to
// group notes into ordered priority sections.
func isNoteTierTag(tag string) bool {
	rest, found := strings.CutPrefix(tag, "tier")
	if !found || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func boundedNoteValue(value string, limit int) string {
	value = singleLineNoteValue(value)
	if value == "" {
		return "-"
	}
	return cells.TruncateEllipsis(value, limit)
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
