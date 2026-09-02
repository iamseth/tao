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
	maxNotePrimaryTagCells  = 32
	maxNotePreviewCells     = 64
	minimumNotePreviewCells = 12
)

type noteRowValues struct {
	repository string
	preview    string
	tier       string
	primaryTag string
	age        string
}

type noteTableWidths struct {
	repository int
	preview    int
	tier       int
	primaryTag int
	age        int
}

type indexedNote struct {
	item  note.CatalogNote
	index int
}

type noteDateBucket struct {
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
	for _, tag := range item.Tags {
		if !isNoteTierTag(tag) {
			continue
		}
		if rank, err := strconv.Atoi(strings.TrimPrefix(tag, "tier")); err == nil {
			return rank
		}
	}
	return math.MaxInt
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
		for _, bucket := range noteDateBuckets(items, now) {
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

func noteDateBuckets(items []note.CatalogNote, now time.Time) []noteDateBucket {
	location := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	yesterday := today.AddDate(0, 0, -1)
	weekStart := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	buckets := []noteDateBucket{
		{title: "TODAY"},
		{title: "YESTERDAY"},
		{title: "EARLIER THIS WEEK"},
		{title: "OLDER"},
	}
	for index, item := range items {
		updated := item.UpdatedAt.In(location)
		bucketIndex := 3
		switch {
		case !updated.Before(today):
			bucketIndex = 0
		case !updated.Before(yesterday):
			bucketIndex = 1
		case !updated.Before(weekStart):
			bucketIndex = 2
		}
		buckets[bucketIndex].items = append(buckets[bucketIndex].items, indexedNote{item: item, index: index})
	}
	visible := buckets[:0]
	for _, bucket := range buckets {
		if len(bucket.items) > 0 {
			visible = append(visible, bucket)
		}
	}
	return visible
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
		repository: len("REPO"), preview: len("PREVIEW"),
		primaryTag: len("TAG"), age: len("AGE"),
	}
	for _, item := range items {
		values := noteValues(item, now)
		widths.repository = max(widths.repository, cells.Width(values.repository))
		widths.preview = max(widths.preview, cells.Width(values.preview))
		widths.primaryTag = max(widths.primaryTag, cells.Width(values.primaryTag))
		widths.age = max(widths.age, cells.Width(values.age))
		if values.tier != "" {
			widths.tier = max(widths.tier, len("TIER"), cells.Width(values.tier))
		}
	}
	return widths
}

func noteTableColumns(widths noteTableWidths, frameWidth int) []column {
	columns := make([]column, 0, 5)
	columns = append(columns, column{name: "REPO", width: widths.repository, priority: 20})
	// A zero tier width means no visible note carries a tier tag; the column
	// stays out entirely rather than rendering with no information.
	if widths.tier > 0 {
		columns = append(columns, column{name: "TIER", width: widths.tier, priority: 30})
	}
	columns = append(columns,
		column{name: "PREVIEW", width: widths.preview, flex: true, required: true, priority: 40, minimum: minimumNotePreviewCells},
		column{name: "TAG", width: widths.primaryTag, priority: 10},
		column{name: "AGE", width: widths.age, required: true, priority: 40},
	)
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
		case "TIER":
			rowCells = append(rowCells, Paint(profile, RoleInfo, values.tier))
		case "PREVIEW":
			rowCells = append(rowCells, Paint(profile, RoleNeutral4, values.preview))
		case "TAG":
			rowCells = append(rowCells, Paint(profile, RoleAccent, values.primaryTag))
		case "AGE":
			rowCells = append(rowCells, values.age)
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
	updated := item.UpdatedAt
	tier := ""
	primaryTag := ""
	for _, tag := range item.Tags {
		if isNoteTierTag(tag) {
			if tier == "" {
				tier = boundedOptionalNoteValue(tag, maxNotePrimaryTagCells)
			}
			continue
		}
		if primaryTag == "" {
			primaryTag = boundedOptionalNoteValue(tag, maxNotePrimaryTagCells)
		}
	}
	return noteRowValues{
		repository: boundedNoteValue(item.RepositoryName, maxNoteRepositoryCells),
		preview:    boundedNoteValue(item.Text, maxNotePreviewCells),
		tier:       tier,
		primaryTag: primaryTag,
		age:        relativeAge(&updated, now),
	}
}

// isNoteTierTag recognizes the priority convention "tier<digits>" so the tier
// can render as its own column instead of competing for the single TAG cell.
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

func boundedOptionalNoteValue(value string, limit int) string {
	return cells.TruncateEllipsis(singleLineNoteValue(value), limit)
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
