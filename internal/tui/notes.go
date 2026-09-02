package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/note"
)

const (
	maxNoteRepositoryCells   = 24
	maxNotePrimaryTagCells   = 32
	maxNotePreviewCells      = 64
	minimumNotePreviewCells  = 12
	maxNotePaneTitleCells    = 36
	maxNotePaneIdentityCells = 48
)

type noteRowValues struct {
	repository string
	preview    string
	primaryTag string
	age        string
}

type noteTableWidths struct {
	repository int
	preview    int
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

func renderNotesPage(snapshot note.Snapshot, selected int, focusRepositoryID string, now time.Time, model Model) (lines []string, selectedLine int, metadata tableViewportMetadata) {
	items := visibleNotes(snapshot, focusRepositoryID)
	warnings := visibleNoteWarnings(snapshot, focusRepositoryID)
	selectedLine = -1
	if len(items) == 0 {
		sectionWidth := dashboardSectionWidth(model, PageNotes, "OPEN NOTES", 1)
		lines = append(lines, "", sectionRule(model.Profile, RoleAccent, "OPEN NOTES", 0, sectionWidth), "  Notes page. No open notes.")
		metadata.sections = append(metadata.sections, tableViewportSection{headingLines: []int{1}, contentLines: []int{2}})
	} else {
		widths := measureNoteTable(items, now)
		columns := noteTableColumns(widths, model.Width)
		paneWidth := noteTablePaneWidth(model.Width, columns)
		for _, bucket := range noteDateBuckets(items, now) {
			sectionWidth := dashboardSectionWidth(model, PageNotes, bucket.title, visibleWidth(fmt.Sprintf("%d", len(bucket.items))))
			lines = append(lines, "", sectionRule(model.Profile, RoleAccent, bucket.title, len(bucket.items), sectionWidth), renderNoteHeader(columns, paneWidth))
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
		sectionWidth := dashboardSectionWidth(model, PageNotes, "Warnings", visibleWidth(fmt.Sprintf("%d", len(warnings))))
		lines = append(lines, "", sectionRule(model.Profile, RoleWarn, "Warnings", len(warnings), sectionWidth))
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
		widths.repository = max(widths.repository, visibleWidth(values.repository))
		widths.preview = max(widths.preview, visibleWidth(values.preview))
		widths.primaryTag = max(widths.primaryTag, visibleWidth(values.primaryTag))
		widths.age = max(widths.age, visibleWidth(values.age))
	}
	return widths
}

func noteTableColumns(widths noteTableWidths, frameWidth int) []column {
	repositoryWidth := widths.repository
	primaryTagWidth := widths.primaryTag
	ageWidth := widths.age
	if frameWidth > 0 {
		paneWidth := max(frameWidth-visibleWidth("  "), 0)
		fixedBudget := paneWidth - 3*columnGapWidth - ageWidth - minimumNotePreviewCells
		overflow := repositoryWidth + primaryTagWidth - max(fixedBudget, 0)
		shrink := min(max(primaryTagWidth-visibleWidth("TAG"), 0), max(overflow, 0))
		primaryTagWidth -= shrink
		overflow -= shrink
		repositoryWidth -= min(max(repositoryWidth-visibleWidth("REPO"), 0), max(overflow, 0))
	}
	return []column{
		{name: "REPO", width: repositoryWidth},
		{name: "PREVIEW", width: widths.preview, flex: true},
		{name: "TAG", width: primaryTagWidth},
		{name: "AGE", width: ageWidth},
	}
}

func noteTablePaneWidth(frameWidth int, columns []column) int {
	if frameWidth > 0 {
		return max(frameWidth-visibleWidth("  "), 0)
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
	cells := []string{
		values.repository,
		Paint(profile, RoleNeutral4, values.preview),
		Paint(profile, RoleAccent, values.primaryTag),
		values.age,
	}
	line := "  " + joinRow(columns, cells, paneWidth)
	if paneWidth > 0 {
		line = padCells(line, paneWidth+visibleWidth("  "))
	}
	if selected {
		line = SelectRow(profile, line)
	}
	return line
}

func noteValues(item note.CatalogNote, now time.Time) noteRowValues {
	updated := item.UpdatedAt
	primaryTag := ""
	if len(item.Tags) > 0 {
		primaryTag = boundedOptionalNoteValue(item.Tags[0], maxNotePrimaryTagCells)
	}
	return noteRowValues{
		repository: boundedNoteValue(item.RepositoryName, maxNoteRepositoryCells),
		preview:    boundedNoteValue(item.Text, maxNotePreviewCells),
		primaryTag: primaryTag,
		age:        relativeAge(&updated, now),
	}
}

func notePaneTitle(item note.CatalogNote) string {
	title := boundedOptionalNoteValue(item.Text, maxNotePaneTitleCells)
	if title == "" {
		return "Selected note"
	}
	return displayValue(title)
}

func notePaneIdentity(item note.CatalogNote) string {
	repository := singleLineNoteValue(item.RepositoryName)
	if repository == "" {
		repository = singleLineNoteValue(item.RepositoryID)
	}
	identity := strings.Trim(strings.Join([]string{repository, singleLineNoteValue(item.ID)}, " · "), " ·")
	return displayValue(boundedOptionalNoteValue(identity, maxNotePaneIdentityCells))
}

func renderNotePane(profile Profile, item note.CatalogNote, width int) []string {
	created := "-"
	if !item.CreatedAt.IsZero() {
		created = item.CreatedAt.Format(time.RFC3339)
	}
	updated := "-"
	if !item.UpdatedAt.IsZero() {
		updated = item.UpdatedAt.Format(time.RFC3339)
	}
	tags := singleLineNoteValue(strings.Join(item.Tags, ", "))
	if tags == "" {
		tags = "-"
	}
	repository := singleLineNoteValue(item.RepositoryName)
	if repository == "" {
		repository = singleLineNoteValue(item.RepositoryID)
	}

	var lines []string
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "Note ID", value: singleLineNoteValue(item.ID)},
		{label: "Repository", value: repository},
		{label: "Tags", value: tags},
		{label: "Created", value: created},
		{label: "Updated", value: updated},
	} {
		lines = append(lines, planPreviewField(profile, field.label, field.value, width)...)
	}
	lines = append(lines, "Body:")
	lines = append(lines, renderNoteText(item.Text, width)...)
	return lines
}

func boundedOptionalNoteValue(value string, limit int) string {
	value = singleLineNoteValue(value)
	if visibleWidth(value) <= limit {
		return value
	}
	if limit <= 1 {
		return truncateCells(value, limit)
	}
	return truncateCells(value, limit-1) + "…"
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
