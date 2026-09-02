package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
)

const (
	clearScreenSequence = "\x1b[H\x1b[2J"
	maxSliceIDCells     = 20
	sliceBarCells       = 10
)

// Model contains the render-neutral state for one UI frame.
type Model struct {
	Snapshot            monitor.Snapshot
	NoteSnapshot        note.Snapshot
	DebugSnapshot       DebugSnapshot
	SettingsSnapshot    SettingsSnapshot
	Page                PageID
	Selected            int
	Width               int
	Height              int
	Now                 time.Time
	HideCompleted       bool
	FocusRepositoryID   string
	FocusRepositoryName string
	Profile             Profile
	ShowShortcuts       bool
	SearchQuery         string
	SearchActive        bool
	DebugOffset         int
	ConfirmMessage      string
	ActionLabels        map[string]string
	ActionMessage       string
	SettingsMessage     string
}

type rowValues struct {
	repo      string
	next      string
	plan      string
	slices    string
	run       string
	age       string
	attention string
}

type tableWidths struct {
	repo       int
	next       int
	plan       int
	slices     int
	run        int
	age        int
	attention  int
	hasRunning bool
}

type tableViewportSection struct {
	headingLines []int
	contentLines []int
}

type tableViewportMetadata struct {
	sections []tableViewportSection
}

func (metadata tableViewportMetadata) offset(lineOffset int) tableViewportMetadata {
	shifted := tableViewportMetadata{sections: make([]tableViewportSection, len(metadata.sections))}
	for index, section := range metadata.sections {
		shifted.sections[index].headingLines = offsetLineNumbers(section.headingLines, lineOffset)
		shifted.sections[index].contentLines = offsetLineNumbers(section.contentLines, lineOffset)
	}
	return shifted
}

func offsetLineNumbers(lines []int, offset int) []int {
	shifted := make([]int, len(lines))
	for index, line := range lines {
		shifted[index] = line + offset
	}
	return shifted
}

// Render builds one complete terminal frame without writing it.
func Render(model Model) string {
	page := normalizePage(model.Page)
	planRows := FilterPlanRows(model.Snapshot.Rows, model.SearchQuery)
	noteSnapshot := FilterNoteSnapshot(model.NoteSnapshot, model.SearchQuery)
	sections := BuildRepositorySections(planRows, !model.HideCompleted, model.FocusRepositoryID)
	visibleCount := 0
	for _, section := range sections {
		visibleCount += len(section.Rows)
	}
	searchSummary := ""
	if page != PageDebug && page != PageSettings && (model.SearchActive || normalizedSearchQuery(model.SearchQuery) != "") {
		searchSummary = searchHeaderLabel(model.SearchQuery, model.SearchActive)
	}
	var summary *frameSummary
	switch page {
	case PagePlans:
		attentionCount := 0
		for _, section := range sections {
			if section.Kind == SectionAttention {
				attentionCount = len(section.Rows)
				break
			}
		}
		summary = &frameSummary{primary: planCountLabel(visibleCount), attentionCount: attentionCount, extra: searchSummary}
	case PageNotes:
		items := visibleNotes(noteSnapshot, model.FocusRepositoryID)
		extra := searchSummary
		if breakdown := noteRepositoryBreakdown(items); breakdown != "" {
			if extra != "" {
				extra += "  ·  "
			}
			extra += breakdown
		}
		summary = &frameSummary{
			primary:        noteCountLabel(len(items)),
			attentionCount: len(visibleNoteWarnings(noteSnapshot, model.FocusRepositoryID)),
			attentionNoun:  "warnings",
			extra:          extra,
		}
	}
	lines := renderFrame(model, page)
	frameLineCount := len(lines)
	selectedLine := -1
	var viewportMetadata tableViewportMetadata
	switch {
	case page == PageSettings:
		settingsLines, settingsSelectedLine, settingsMetadata := renderSettingsPage(model)
		selectedLine = len(lines) + settingsSelectedLine
		viewportMetadata = settingsMetadata.offset(len(lines))
		if settingsSelectedLine < 0 {
			selectedLine = -1
		}
		lines = append(lines, settingsLines...)
	case page == PageDebug:
		body := renderDebugPage(model)
		bodyHeight := len(body)
		if model.Height > 0 {
			bodyHeight = max(model.Height-frameLineCount, 0)
		}
		start := max(0, min(model.DebugOffset, max(len(body)-bodyHeight, 0)))
		if bodyHeight > 0 && len(body)-start > bodyHeight {
			visibleHeight := max(bodyHeight-1, 0)
			end := min(start+visibleHeight, len(body))
			lines = append(lines, body[start:end]...)
			lines = append(lines, moreIndicator(model.Profile, len(body)-(end-start)))
		} else {
			end := min(start+bodyHeight, len(body))
			lines = append(lines, body[start:end]...)
		}
	case page == PageNotes:
		now := model.Now
		if now.IsZero() {
			now = time.Now()
		}
		noteLines, noteSelectedLine, noteMetadata := renderNotesPage(noteSnapshot, model.Selected, model.FocusRepositoryID, now, model)
		selectedLine = len(lines) + noteSelectedLine
		viewportMetadata = noteMetadata.offset(len(lines))
		if noteSelectedLine < 0 {
			selectedLine = -1
		}
		lines = append(lines, noteLines...)
	case visibleCount == 0:
		lines = append(lines, "", "  No plans.")
	default:
		widths := measureTable(sections, model.Snapshot.CollectedAt, model.ActionLabels)
		selected := 0
		for _, section := range sections {
			if len(section.Rows) == 0 {
				continue
			}
			withAttention := section.Kind == SectionAttention
			sectionWidth := dashboardSectionWidth(model, PagePlans, section.Title, 0)
			columns := planTableColumns(widths, withAttention, model.Width)
			paneWidth := planTablePaneWidth(model.Width, columns)
			lines = append(lines, "", sectionTitleRule(model.Profile, planSectionRole(section.Kind), section.Title, sectionWidth), renderHeader(columns, paneWidth))
			viewportSection := tableViewportSection{headingLines: []int{len(lines) - 2, len(lines) - 1}}
			for _, row := range section.Rows {
				if selected == model.Selected {
					selectedLine = len(lines)
				}
				viewportSection.contentLines = append(viewportSection.contentLines, len(lines))
				lines = append(lines, renderTableRow(row, model.Snapshot.CollectedAt, columns, paneWidth, selected == model.Selected, model.Profile, model.ActionLabels[actionRowKey(row)]))
				selected++
			}
			viewportMetadata.sections = append(viewportMetadata.sections, viewportSection)
		}
	}
	footerStart := len(lines)
	if page == PagePlans && strings.TrimSpace(model.ActionMessage) != "" {
		lines = append(lines, "", model.ActionMessage)
	}
	if page == PageSettings && strings.TrimSpace(model.SettingsMessage) != "" {
		lines = append(lines, "", model.SettingsMessage)
	}
	if strings.TrimSpace(model.ConfirmMessage) != "" {
		lines = append(lines, "", model.ConfirmMessage+" [y/n]")
	}
	if summary != nil {
		lines = append(lines, renderFrameSummary(model.Profile, *summary))
	}
	lines = tableViewport(lines, selectedLine, footerStart, frameLineCount, model.Height, model.Profile, viewportMetadata)
	if summary != nil && model.Height > len(lines) {
		bottom := lines[len(lines)-1]
		lines = append(lines[:len(lines)-1], make([]string, model.Height-len(lines))...)
		lines = append(lines, bottom)
	}
	if model.ShowShortcuts {
		lines = overlayShortcutLegend(lines, page, model.Width, model.Height, model.Profile)
	}
	if model.Width > 0 {
		for index := range lines {
			lines[index] = cells.Pad(cells.Truncate(lines[index], model.Width), model.Width)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func linesWithin(lines []int, start, end int) []int {
	within := make([]int, 0, len(lines))
	for _, line := range lines {
		if line >= start && line < end {
			within = append(within, line)
		}
	}
	return within
}

func compactFooter(lines []string) []string {
	footer := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			footer = append(footer, line)
		}
	}
	return footer
}

func tableViewport(lines []string, selectedLine, footerStart, headerCount, height int, profile Profile, metadata tableViewportMetadata) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	headerCount = min(headerCount, footerStart)
	if height <= headerCount {
		return lines[:height]
	}

	contentCount := 0
	for _, section := range metadata.sections {
		contentCount += len(section.contentLines)
	}
	if contentCount == 0 {
		return legacyTableViewport(lines, selectedLine, footerStart, headerCount, height, profile)
	}

	footer := compactFooter(lines[footerStart:])
	available := height - headerCount
	footerLimit := max(available-1, 0) // Reserve one line for page content.
	if len(footer) > footerLimit {
		footer = footer[len(footer)-footerLimit:]
	}
	available -= len(footer)

	bodyLines, visibleContent := metadata.viewportLines(selectedLine, available, headerCount, footerStart)
	hiddenContent := contentCount - visibleContent
	if hiddenContent > 0 && available > 1 {
		bodyLines, visibleContent = metadata.viewportLines(selectedLine, available-1, headerCount, footerStart)
		hiddenContent = contentCount - visibleContent
	}

	viewport := make([]string, 0, height)
	viewport = append(viewport, lines[:headerCount]...)
	for _, line := range bodyLines {
		viewport = append(viewport, lines[line])
	}
	if hiddenContent > 0 && len(viewport) < height-len(footer) {
		viewport = append(viewport, moreIndicator(profile, hiddenContent))
	}
	return append(viewport, footer...)
}

func (metadata tableViewportMetadata) viewportLines(selectedLine, capacity, bodyStart, bodyEnd int) ([]int, int) {
	if capacity <= 0 || len(metadata.sections) == 0 {
		return nil, 0
	}

	sections := make([]tableViewportSection, len(metadata.sections))
	selectedSection := 0
	selectedContent := -1
	minimumLines := 0
	for index, section := range metadata.sections {
		sections[index].headingLines = linesWithin(section.headingLines, bodyStart, bodyEnd)
		sections[index].contentLines = linesWithin(section.contentLines, bodyStart, bodyEnd)
		minimumLines += len(sections[index].headingLines)
		if len(sections[index].contentLines) > 0 {
			minimumLines++
		}
		if contentIndex := slices.Index(sections[index].contentLines, selectedLine); contentIndex >= 0 {
			selectedSection = index
			selectedContent = contentIndex
		}
	}

	// If every section cannot be represented, preserve the selected section's
	// complete heading skeleton and selected row. Supported frame sizes have
	// enough room for the all-section path below.
	if minimumLines > capacity {
		section := sections[selectedSection]
		headings := section.headingLines
		if len(section.contentLines) == 0 {
			return headings[:min(len(headings), capacity)], 0
		}
		if len(headings) >= capacity {
			headings = headings[:max(capacity-1, 0)]
		}
		contentCapacity := capacity - len(headings)
		if selectedContent < 0 {
			selectedContent = 0
		}
		start := selectedContent - contentCapacity/2
		start = max(0, min(start, len(section.contentLines)-contentCapacity))
		visibleContent := min(contentCapacity, len(section.contentLines))
		visibleLines := append([]int(nil), headings...)
		visibleLines = append(visibleLines, section.contentLines[start:start+visibleContent]...)
		slices.Sort(visibleLines)
		return visibleLines, visibleContent
	}

	visibleLines := make([]int, 0, capacity)
	visibleContent := 0
	for _, section := range sections {
		visibleLines = append(visibleLines, section.headingLines...)
	}

	contentBudget := capacity - len(visibleLines)
	sectionBudgets := make([]int, len(sections))
	for index, section := range sections {
		if len(section.contentLines) > 0 {
			sectionBudgets[index] = 1
			contentBudget--
		}
	}
	// Rows outside the selectable section cannot be reached by moving the
	// cursor, so keep as many of them visible as the shared budget permits.
	for index, section := range sections {
		if contentBudget == 0 {
			break
		}
		if index == selectedSection {
			continue
		}
		extra := min(contentBudget, len(section.contentLines)-sectionBudgets[index])
		sectionBudgets[index] += extra
		contentBudget -= extra
	}
	selectedExtra := min(contentBudget, len(sections[selectedSection].contentLines)-sectionBudgets[selectedSection])
	sectionBudgets[selectedSection] += selectedExtra
	contentBudget -= selectedExtra
	for index, section := range sections {
		if contentBudget == 0 {
			break
		}
		extra := min(contentBudget, len(section.contentLines)-sectionBudgets[index])
		sectionBudgets[index] += extra
		contentBudget -= extra
	}
	for index, section := range sections {
		budget := sectionBudgets[index]
		if budget == 0 {
			continue
		}
		anchor := 0
		if index == selectedSection && selectedContent >= 0 {
			anchor = selectedContent
		}
		start := anchor - budget/2
		start = max(0, min(start, len(section.contentLines)-budget))
		visibleLines = append(visibleLines, section.contentLines[start:start+budget]...)
		visibleContent += budget
	}
	slices.Sort(visibleLines)
	return visibleLines, visibleContent
}

func legacyTableViewport(lines []string, selectedLine, footerStart, headerCount, height int, profile Profile) []string {
	body := lines[headerCount:footerStart]
	footer := compactFooter(lines[footerStart:])
	available := height - headerCount
	footerLimit := available
	if len(body) > 0 {
		footerLimit--
	}
	footerLimit = max(footerLimit, 0)
	if len(footer) > footerLimit {
		footer = footer[len(footer)-footerLimit:]
	}

	bodyHeight := min(len(body), available-len(footer))
	showMore := len(body) > bodyHeight && bodyHeight > 1
	if showMore {
		bodyHeight--
	}
	start := 0
	selectedBodyLine := selectedLine - headerCount
	if selectedBodyLine >= 0 && selectedBodyLine < len(body) {
		start = selectedBodyLine - bodyHeight/2
	} else {
		for start < len(body) && strings.TrimSpace(body[start]) == "" {
			start++
		}
	}
	start = max(0, min(start, len(body)-bodyHeight))

	viewport := make([]string, 0, height)
	viewport = append(viewport, lines[:headerCount]...)
	viewport = append(viewport, body[start:start+bodyHeight]...)
	if showMore {
		viewport = append(viewport, moreIndicator(profile, len(body)-bodyHeight))
	}
	return append(viewport, footer...)
}

func planCountLabel(count int) string {
	if count == 1 {
		return "1 plan"
	}
	return fmt.Sprintf("%d plans", count)
}

func measureTable(sections []Section, now time.Time, actionLabels map[string]string) tableWidths {
	widths := tableWidths{
		repo: len("REPO"), next: len("NEXT"), plan: len("PLAN"), slices: len("SLICES"),
		run: len("RUN"), age: len("AGE"), attention: len("ATTENTION"),
	}
	for _, section := range sections {
		for _, row := range section.Rows {
			values := tableRowValues(row, now, actionLabels[actionRowKey(row)])
			widths.repo = max(widths.repo, cells.Width(values.repo))
			widths.next = max(widths.next, cells.Width(values.next))
			widths.plan = max(widths.plan, cells.Width(values.plan))
			widths.slices = max(widths.slices, sliceBarCells+1+cells.Width(values.slices))
			widths.run = max(widths.run, cells.Width(values.run))
			widths.age = max(widths.age, cells.Width(values.age))
			widths.attention = max(widths.attention, cells.Width(values.attention))
			widths.hasRunning = widths.hasRunning || hasVisibleRun(row)
		}
	}
	return widths
}

const (
	minimumNextColumnWidth = 7
	minimumPlanColumnWidth = 12
)

func planTableColumns(widths tableWidths, withAttention bool, frameWidth int) []column {
	columns := []column{
		{name: "REPO", width: max(widths.repo, cells.Width("REPO")), priority: 40},
		{name: "NEXT", width: max(widths.next, cells.Width("NEXT")), required: true, priority: 60, minimum: minimumNextColumnWidth},
		{name: "PLAN", width: widths.plan, flex: true, required: true, priority: 60, minimum: minimumPlanColumnWidth},
		{name: "SLICES", width: max(widths.slices, cells.Width("SLICES")), priority: 30},
	}
	if widths.hasRunning {
		columns = append(columns, column{name: "RUN", width: max(widths.run, cells.Width("RUN")), priority: 20})
	}
	columns = append(columns, column{name: "AGE", width: max(widths.age, cells.Width("AGE")), priority: 10})
	if withAttention {
		columns = append(columns, column{
			name: "ATTENTION", width: max(widths.attention, cells.Width("ATTENTION")),
			priority: 50,
		})
	}
	if frameWidth <= 0 {
		return columns
	}
	return fitColumns(columns, max(frameWidth-cells.Width("  "), 0))
}

func planTablePaneWidth(width int, columns []column) int {
	if width > 0 {
		return max(width-cells.Width("  "), 0)
	}
	return columnsWidth(columns)
}

func renderHeader(columns []column, paneWidth int) string {
	headers := make([]string, len(columns))
	for index, item := range columns {
		headers[index] = item.name
	}
	return "  " + joinRow(columns, headers, paneWidth)
}

func planSectionRole(kind SectionKind) Role {
	switch kind {
	case SectionAttention:
		return RoleWarn
	case SectionReadyToMerge:
		return RoleSuccess
	case SectionPlanned:
		return RoleInfo
	case SectionHistory:
		return RoleNeutral5
	default:
		return RoleNeutral5
	}
}

func renderTableRow(row monitor.Row, now time.Time, columns []column, paneWidth int, selected bool, profile Profile, actionLabel string) string {
	values := tableRowValues(row, now, actionLabel)
	repositoryKey := strings.TrimSpace(row.RepositoryID)
	if repositoryKey == "" {
		repositoryKey = strings.TrimSpace(row.RepositoryName)
	}
	repositoryRole := RepoColor(repositoryKey)
	if selected {
		repositoryRole = RoleRepoSelected
	}
	rowCells := make([]string, 0, len(columns))
	for _, item := range columns {
		switch item.name {
		case "REPO":
			rowCells = append(rowCells, Paint(profile, repositoryRole, values.repo))
		case "NEXT":
			rowCells = append(rowCells, values.next)
		case "PLAN":
			rowCells = append(rowCells, values.plan)
		case "SLICES":
			rowCells = append(rowCells, renderSlicesValue(profile, row))
		case "RUN":
			rowCells = append(rowCells, values.run)
		case "AGE":
			rowCells = append(rowCells, values.age)
		case "ATTENTION":
			rowCells = append(rowCells, values.attention)
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

func renderSlicesValue(profile Profile, row monitor.Row) string {
	completed := max(row.OriginalCompletedCount+row.ReworkCompletedCount, 0)
	total := max(row.OriginalTotalCount+row.ReworkTotalCount, 0)
	filled := 0
	if total > 0 {
		filled = min(completed*sliceBarCells/total, sliceBarCells)
	}
	bar := Paint(profile, RoleNeutral5, strings.Repeat("━", filled)) +
		Paint(profile, RoleNeutral2, strings.Repeat("─", sliceBarCells-filled))
	return bar + " " + slicesLabel(row)
}

func tableRowValues(row monitor.Row, now time.Time, actionLabel string) rowValues {
	next := planNextAction(row)
	if strings.TrimSpace(actionLabel) != "" {
		next = actionLabel
	}
	return rowValues{
		repo:      displayValue(row.RepositoryName),
		next:      " " + displayValue(next) + " ",
		plan:      planLabel(row),
		slices:    slicesLabel(row),
		run:       combinedRunLabel(row),
		age:       relativeAge(row.UpdatedAt, now),
		attention: attentionLabel(row.AttentionReasons),
	}
}

func planLabel(row monitor.Row) string {
	id := strings.TrimSpace(row.PlanID)
	if slug, ok := plan.PlanSlug(id); ok {
		return slug
	}
	if id != "" {
		return id
	}
	return displayValue(row.PlanTitle)
}

func phaseLabel(row monitor.Row) string {
	if row.Status == plan.StatusAbandoned {
		return "-"
	}
	if isStalled(row) {
		return fmt.Sprintf("stalled? (%s old)", durationLabel(row.HeartbeatAge))
	}
	phase := strings.TrimSpace(string(row.Phase))
	sliceID := strings.TrimSpace(row.SliceID)
	if sliceID != "" && (phase == "" || phase == "running_slice") {
		return cells.Truncate(sliceID, maxSliceIDCells)
	}
	return displayValue(phase)
}

func runAgeLabel(row monitor.Row) string {
	if row.Status == plan.StatusAbandoned || (row.Liveness != monitor.LivenessLive && row.Liveness != monitor.LivenessStale) {
		return "-"
	}
	return durationLabel(row.InvocationDuration)
}

func hasVisibleRun(row monitor.Row) bool {
	return row.Liveness == monitor.LivenessLive || isStalled(row)
}

func combinedRunLabel(row monitor.Row) string {
	if !hasVisibleRun(row) {
		return "-"
	}
	phase := phaseLabel(row)
	age := runAgeLabel(row)
	if phase == "-" {
		return age
	}
	if age == "-" {
		return phase
	}
	return phase + " " + age
}

func relativeAge(value *time.Time, now time.Time) string {
	if value == nil {
		return "-"
	}
	age := max(now.Sub(*value), 0)
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%dm", max(int(age/time.Minute), 1))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age/time.Hour))
	case age < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
	case age < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(age/(7*24*time.Hour)))
	case age < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(age/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy", int(age/(365*24*time.Hour)))
	}
}

func formatAbandonedAt(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func durationLabel(duration time.Duration) string {
	duration = max(duration, 0)
	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%ds", duration/time.Second)
	case duration < time.Hour:
		return fmt.Sprintf("%dm", duration/time.Minute)
	default:
		return fmt.Sprintf("%dh", duration/time.Hour)
	}
}

func slicesLabel(row monitor.Row) string {
	completed := row.OriginalCompletedCount + row.ReworkCompletedCount
	value := fmt.Sprintf("%d/%d", completed, row.OriginalTotalCount)
	if row.ReworkTotalCount > 0 {
		value += fmt.Sprintf("+%d", row.ReworkTotalCount)
	}
	return value
}

func attentionLabel(reasons []monitor.AttentionReason) string {
	if len(reasons) == 0 {
		return "-"
	}
	labels := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		switch reason {
		case monitor.AttentionBlocked:
			labels = append(labels, "blocked")
		case monitor.AttentionChangesRequested:
			labels = append(labels, "changes requested")
		case monitor.AttentionApprovalRequired:
			labels = append(labels, "approval required")
		case monitor.AttentionSliceCompletionPending:
			labels = append(labels, "slice completion pending")
		case monitor.AttentionReworkStopped:
			labels = append(labels, "rework stopped")
		case monitor.AttentionRunCrashed:
			labels = append(labels, "crashed?")
		default:
			labels = append(labels, strings.ReplaceAll(string(reason), "_", " "))
		}
	}
	return strings.Join(labels, ", ")
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func colorStatus(profile Profile, value, status string) string {
	role := RoleRepo
	switch status {
	case plan.StatusCompleted, plan.StatusReviewed:
		role = RoleSuccess
	case plan.StatusInProgress:
		role = RoleAccent
	case plan.StatusBlocked, plan.StatusPlanned, plan.StatusPending, plan.StatusChangesRequested, plan.StatusVerificationFailed:
		role = RoleWarn
	}
	return Paint(profile, role, value)
}
