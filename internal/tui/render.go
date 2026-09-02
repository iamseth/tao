package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	planview "github.com/iamseth/tao/internal/view"
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
	lines := renderFrame(model, page, summary)
	frameLineCount := len(lines)
	feedbackLineCount := 0
	if page == PagePlans && strings.TrimSpace(model.ActionMessage) != "" {
		feedbackLineCount++
	}
	if page == PageSettings && strings.TrimSpace(model.SettingsMessage) != "" {
		feedbackLineCount++
	}
	if strings.TrimSpace(model.ConfirmMessage) != "" {
		feedbackLineCount++
	}
	keyHintsFooter := ""
	if shouldRenderKeyHintsFooter(model, frameLineCount, feedbackLineCount) {
		keyHintsFooter = renderKeyHintsFooter(model.Profile, page, model.Width)
	}
	selectedLine := -1
	previewStart := -1
	notePaneStart := -1
	var planRowLines []int
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
			footerHeight := 0
			if keyHintsFooter != "" {
				footerHeight = 1
			}
			bodyHeight = max(model.Height-frameLineCount-footerHeight, 0)
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
		items := visibleNotes(noteSnapshot, model.FocusRepositoryID)
		if model.Selected >= 0 && model.Selected < len(items) && selectedLine >= 0 {
			notePaneStart = len(lines)
			paneWidth := dashboardFrameWidth(model, PageNotes)
			selectedNote := items[model.Selected]
			details := renderNotePane(model.Profile, selectedNote, max(paneWidth-2, 0))
			lines = append(lines, borderedPane(model.Profile, paneWidth, notePaneTitle(selectedNote), notePaneIdentity(selectedNote), true, details)...)
		}
	case visibleCount == 0:
		lines = append(lines, "", "  No plans.")
	default:
		widths := measureTable(sections, model.Snapshot.CollectedAt, model.ActionLabels)
		selected := 0
		var selectedRow monitor.Row
		hasSelectedRow := false
		for _, section := range sections {
			if len(section.Rows) == 0 {
				continue
			}
			withAttention := section.Kind == SectionAttention
			sectionWidth := dashboardSectionWidth(model, PagePlans, section.Title, visibleWidth(fmt.Sprintf("%d", len(section.Rows))))
			columns := planTableColumns(widths, withAttention, model.Width)
			paneWidth := planTablePaneWidth(model.Width, columns)
			lines = append(lines, "", sectionRule(model.Profile, planSectionRole(section.Kind), section.Title, len(section.Rows), sectionWidth), renderHeader(columns, paneWidth))
			viewportSection := tableViewportSection{headingLines: []int{len(lines) - 2, len(lines) - 1}}
			for _, row := range section.Rows {
				if selected == model.Selected {
					selectedLine = len(lines)
					selectedRow = row
					hasSelectedRow = true
				}
				planRowLines = append(planRowLines, len(lines))
				viewportSection.contentLines = append(viewportSection.contentLines, len(lines))
				lines = append(lines, renderTableRow(row, model.Snapshot.CollectedAt, columns, paneWidth, selected == model.Selected, model.Profile, planSectionRole(section.Kind), model.ActionLabels[actionRowKey(row)]))
				selected++
			}
			viewportMetadata.sections = append(viewportMetadata.sections, viewportSection)
		}
		if hasSelectedRow {
			previewStart = len(lines)
			paneWidth := dashboardFrameWidth(model, PagePlans)
			preview := renderPlanPreview(model.Profile, selectedRow, max(paneWidth-2, 0))
			title := displayValue(singleLineDetail(planLabel(selectedRow)))
			identity := planPreviewIdentity(selectedRow)
			lines = append(lines, borderedPane(model.Profile, paneWidth, title, identity, hasSelectedRow, preview)...)
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
	if keyHintsFooter != "" {
		lines = append(lines, keyHintsFooter)
	}
	switch {
	case page == PagePlans && previewStart >= 0:
		lines = planTableViewport(lines, planRowLines, selectedLine, previewStart, footerStart, frameLineCount, model.Height, model.Profile, viewportMetadata)
	case page == PageNotes && notePaneStart >= 0:
		lines = noteTableViewport(lines, selectedLine, notePaneStart, footerStart, frameLineCount, model.Height, model.Profile, viewportMetadata)
	default:
		lines = tableViewport(lines, selectedLine, footerStart, frameLineCount, model.Height, model.Profile, viewportMetadata)
	}
	if model.ShowShortcuts {
		lines = overlayShortcutLegend(lines, page, model.Width, model.Height, model.Profile)
	}
	if model.Width > 0 {
		for index := range lines {
			lines[index] = padCells(truncateCells(lines[index], model.Width), model.Width)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func planTableViewport(lines []string, planRowLines []int, selectedLine, previewStart, footerStart, headerCount, height int, profile Profile, metadata tableViewportMetadata) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	headerCount = min(headerCount, footerStart)
	if height <= headerCount {
		return lines[:height]
	}

	footer := compactFooter(lines[footerStart:])
	available := height - headerCount
	footerLimit := max(available-1, 0) // A selected plan row takes precedence over feedback.
	if len(footer) > footerLimit {
		footer = footer[len(footer)-footerLimit:]
	}
	available -= len(footer)

	tableBody := lines[headerCount:previewStart]
	preview := lines[previewStart:footerStart]
	viewportBounds := func(contentHeight int) (tableLines []int, previewHeight, hiddenPlanRows int) {
		if len(preview) > 0 && contentHeight >= 2 {
			previewHeight = 1
			// Keep table context in extremely short frames. Once there is room
			// for both, reserve both pane borders and one advisory detail line
			// so cropping still leaves a coherent selected-plan frame.
			if contentHeight >= 6 {
				previewHeight = min(len(preview), 3)
			}
		}
		tableHeight := min(len(tableBody), contentHeight-previewHeight)
		previewHeight = min(len(preview), contentHeight-tableHeight)
		if tableHeight < min(len(tableBody), min(3, contentHeight)) && previewHeight == 0 {
			tableHeight = min(len(tableBody), min(3, contentHeight))
		}

		start := 0
		if tableHeight > 0 {
			start = selectedLine - headerCount - tableHeight/2
			start = max(0, min(start, len(tableBody)-tableHeight))
		}
		tableLines = make([]int, tableHeight)
		for index := range tableLines {
			tableLines[index] = headerCount + start + index
		}
		tableLines = metadata.preserveSelectedSectionHeadings(tableLines, selectedLine, tableHeight, headerCount, previewStart)

		hiddenPlanRows = len(planRowLines)
		visible := make(map[int]struct{}, len(tableLines))
		for _, line := range tableLines {
			visible[line] = struct{}{}
		}
		for _, line := range planRowLines {
			if _, ok := visible[line]; ok {
				hiddenPlanRows--
			}
		}
		return tableLines, previewHeight, hiddenPlanRows
	}

	tableLines, previewHeight, hiddenPlanRows := viewportBounds(available)
	if hiddenPlanRows > 0 && available > 1 {
		tableLines, previewHeight, hiddenPlanRows = viewportBounds(available - 1)
	}
	viewport := make([]string, 0, height)
	viewport = append(viewport, lines[:headerCount]...)
	for _, line := range tableLines {
		viewport = append(viewport, lines[line])
	}
	viewport = append(viewport, borderedPaneViewport(preview, previewHeight)...)
	if hiddenPlanRows > 0 && len(viewport) < height-len(footer) {
		viewport = append(viewport, moreIndicator(profile, hiddenPlanRows))
	}
	return append(viewport, footer...)
}

func noteTableViewport(lines []string, selectedLine, paneStart, footerStart, headerCount, height int, profile Profile, metadata tableViewportMetadata) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	headerCount = min(headerCount, paneStart)
	if height <= headerCount {
		return lines[:height]
	}

	contentCount := 0
	for _, section := range metadata.sections {
		contentCount += len(section.contentLines)
	}
	if contentCount == 0 {
		return tableViewport(lines, selectedLine, footerStart, headerCount, height, profile, metadata)
	}

	footer := compactFooter(lines[footerStart:])
	available := height - headerCount
	footerLimit := max(available-1, 0)
	if len(footer) > footerLimit {
		footer = footer[len(footer)-footerLimit:]
	}
	available -= len(footer)
	pane := lines[paneStart:footerStart]

	viewportBounds := func(contentHeight int) (bodyLines []int, paneHeight, visibleContent int) {
		// Prefer the complete pane whenever it fits below the selected section's
		// headings and selected row. Otherwise retain the compact crop used by
		// very short frames.
		contextHeight := metadata.selectedSectionContextHeight(selectedLine, headerCount, paneStart)
		if len(pane) >= 3 && selectedLine >= 0 {
			switch {
			case contextHeight > 0 && len(pane)+contextHeight <= contentHeight:
				paneHeight = len(pane)
			case contentHeight >= 4:
				paneHeight = 3
			}
		}
		bodyLines, visibleContent = metadata.viewportLines(selectedLine, contentHeight-paneHeight, headerCount, paneStart)
		return bodyLines, paneHeight, visibleContent
	}

	bodyLines, paneHeight, visibleContent := viewportBounds(available)
	hiddenContent := contentCount - visibleContent
	if hiddenContent > 0 && available > 1 {
		bodyLines, paneHeight, visibleContent = viewportBounds(available - 1)
		hiddenContent = contentCount - visibleContent
	}

	viewport := make([]string, 0, height)
	viewport = append(viewport, lines[:headerCount]...)
	for _, line := range bodyLines {
		viewport = append(viewport, lines[line])
	}
	viewport = append(viewport, borderedPaneViewport(pane, paneHeight)...)
	if hiddenContent > 0 && len(viewport) < height-len(footer) {
		viewport = append(viewport, moreIndicator(profile, hiddenContent))
	}
	return append(viewport, footer...)
}

func borderedPaneViewport(lines []string, height int) []string {
	height = min(max(height, 0), len(lines))
	if height == 0 {
		return nil
	}
	if height == len(lines) || height == 1 {
		return lines[:height]
	}
	viewport := make([]string, 0, height)
	viewport = append(viewport, lines[0])
	viewport = append(viewport, lines[1:height-1]...)
	return append(viewport, lines[len(lines)-1])
}

func (metadata tableViewportMetadata) selectedSectionContextHeight(selectedLine, bodyStart, bodyEnd int) int {
	for _, section := range metadata.sections {
		if slices.Contains(section.contentLines, selectedLine) {
			return len(linesWithin(section.headingLines, bodyStart, bodyEnd)) + 1
		}
	}
	return 0
}

func (metadata tableViewportMetadata) preserveSelectedSectionHeadings(tableLines []int, selectedLine, capacity, bodyStart, bodyEnd int) []int {
	if capacity <= 0 {
		return tableLines
	}
	for _, section := range metadata.sections {
		selectedContent := slices.Index(section.contentLines, selectedLine)
		if selectedContent < 0 {
			continue
		}
		headings := linesWithin(section.headingLines, bodyStart, bodyEnd)
		content := linesWithin(section.contentLines, bodyStart, bodyEnd)
		if len(headings)+1 > capacity || containsAllLines(tableLines, headings) {
			return tableLines
		}
		contentCapacity := capacity - len(headings)
		start := selectedContent - contentCapacity/2
		start = max(0, min(start, len(content)-contentCapacity))
		visibleContent := min(contentCapacity, len(content))
		preserved := make([]int, 0, len(headings)+visibleContent)
		preserved = append(preserved, headings...)
		preserved = append(preserved, content[start:start+visibleContent]...)
		return preserved
	}
	return tableLines
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

func containsAllLines(lines, required []int) bool {
	available := make(map[int]struct{}, len(lines))
	for _, line := range lines {
		available[line] = struct{}{}
	}
	for _, line := range required {
		if _, ok := available[line]; !ok {
			return false
		}
	}
	return true
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
			widths.repo = max(widths.repo, visibleWidth(values.repo))
			widths.next = max(widths.next, visibleWidth(values.next))
			widths.plan = max(widths.plan, visibleWidth(values.plan))
			widths.slices = max(widths.slices, sliceBarCells+1+visibleWidth(values.slices))
			widths.run = max(widths.run, visibleWidth(values.run))
			widths.age = max(widths.age, visibleWidth(values.age))
			widths.attention = max(widths.attention, visibleWidth(values.attention))
			widths.hasRunning = widths.hasRunning || hasVisibleRun(row)
		}
	}
	return widths
}

const minimumPlanColumnWidth = 12

func planTableColumns(widths tableWidths, withAttention bool, frameWidth int) []column {
	repoWidth := max(widths.repo, visibleWidth("REPO"))
	if frameWidth > 0 {
		paneWidth := max(frameWidth-visibleWidth("  "), 0)
		reservedWidth := max(widths.next, visibleWidth("NEXT")) + minimumPlanColumnWidth + 2*columnGapWidth
		if withAttention {
			reservedWidth += max(widths.attention, visibleWidth("ATTENTION")) + columnGapWidth
		}
		maxRepoWidth := max(paneWidth-reservedWidth, visibleWidth("REPO"))
		repoWidth = min(repoWidth, maxRepoWidth)
	}
	base := []column{
		{name: "REPO", width: repoWidth},
		{name: "NEXT", width: max(widths.next, visibleWidth("NEXT"))},
		{name: "PLAN", width: widths.plan, flex: true},
	}
	operational := []column{{name: "SLICES", width: max(widths.slices, visibleWidth("SLICES"))}}
	if widths.hasRunning {
		operational = append(operational, column{name: "RUN", width: max(widths.run, visibleWidth("RUN"))})
	}
	operational = append(operational, column{name: "AGE", width: max(widths.age, visibleWidth("AGE"))})
	var attention []column
	if withAttention {
		attention = []column{{name: "ATTENTION", width: max(widths.attention, visibleWidth("ATTENTION"))}}
	}

	columns := appendPlanTableColumns(base, operational, attention)
	if frameWidth <= 0 {
		return columns
	}
	paneWidth := max(frameWidth-visibleWidth("  "), 0)
	for len(operational) > 0 && minimumPlanTableWidth(columns) > paneWidth {
		operational = operational[:len(operational)-1]
		columns = appendPlanTableColumns(base, operational, attention)
	}
	if len(attention) > 0 && minimumPlanTableWidth(columns) > paneWidth {
		columns = appendPlanTableColumns(base, operational, nil)
	}
	return columns
}

func appendPlanTableColumns(base, operational, attention []column) []column {
	columns := make([]column, 0, len(base)+len(operational)+len(attention))
	columns = append(columns, base...)
	columns = append(columns, operational...)
	return append(columns, attention...)
}

func minimumPlanTableWidth(columns []column) int {
	width := columnGapWidth * max(len(columns)-1, 0)
	for _, item := range columns {
		if item.flex {
			width += minimumPlanColumnWidth
		} else {
			width += item.width
		}
	}
	return width
}

func planTablePaneWidth(width int, columns []column) int {
	if width > 0 {
		return max(width-visibleWidth("  "), 0)
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

func renderTableRow(row monitor.Row, now time.Time, columns []column, paneWidth int, selected bool, profile Profile, sectionRole Role, actionLabel string) string {
	values := tableRowValues(row, now, actionLabel)
	repositoryKey := strings.TrimSpace(row.RepositoryID)
	if repositoryKey == "" {
		repositoryKey = strings.TrimSpace(row.RepositoryName)
	}
	repositoryRole := RepoColor(repositoryKey)
	if selected {
		repositoryRole = RoleRepoSelected
	}
	cells := make([]string, 0, len(columns))
	for _, item := range columns {
		switch item.name {
		case "REPO":
			cells = append(cells, Paint(profile, repositoryRole, values.repo))
		case "NEXT":
			cells = append(cells, filledBadge(profile, sectionRole, values.next))
		case "PLAN":
			cells = append(cells, values.plan)
		case "SLICES":
			cells = append(cells, renderSlicesValue(profile, row))
		case "RUN":
			cells = append(cells, values.run)
		case "AGE":
			cells = append(cells, values.age)
		case "ATTENTION":
			cells = append(cells, values.attention)
		}
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

func renderSlicesValue(profile Profile, row monitor.Row) string {
	completed := max(row.OriginalCompletedCount+row.ReworkCompletedCount, 0)
	total := max(row.OriginalTotalCount+row.ReworkTotalCount, 0)
	filled := 0
	if total > 0 {
		filled = min(completed*sliceBarCells/total, sliceBarCells)
	}
	bar := Paint(profile, RoleSuccess, strings.Repeat("█", filled)) +
		Paint(profile, RoleNeutral1, strings.Repeat("░", sliceBarCells-filled))
	return bar + " " + slicesLabel(row)
}

func filledBadge(profile Profile, role Role, text string) string {
	if profile == ProfileNone || text == "" {
		return text
	}
	background, ok := RoleColor(profile, role)
	if !ok {
		return text
	}
	return colorSequence(background, true) + text + resetSequence
}

func planPreviewIdentity(row monitor.Row) string {
	sequence := "-"
	if row.Overview.Sequence != nil {
		sequence = fmt.Sprintf("%d of %d", row.Overview.Sequence.Position, row.Overview.Sequence.Total)
	}
	return strings.Join([]string{
		displayValue(singleLineDetail(row.PlanID)),
		displayValue(singleLineDetail(row.RepositoryName)),
		sequence,
	}, "  ·  ")
}

func renderPlanPreview(profile Profile, row monitor.Row, width int) []string {
	overview := row.Overview
	var lines []string
	if row.Status == plan.StatusAbandoned {
		lines = append(lines,
			"Abandoned at: "+formatAbandonedAt(row.AbandonedAt),
			"Abandonment reason: "+planview.FormatAbandonmentText(row.AbandonmentReason),
		)
	}
	lines = append(lines, planPreviewField(profile, "Benefit", overview.ExpectedBenefit, width)...)
	if width > 0 && width < 50 {
		decision := fmt.Sprintf("%s / %s", displayValue(string(overview.Disposition)), displayValue(string(overview.Readiness)))
		lines = append(lines, planPreviewField(profile, "Decision", decision, width)...)
	} else {
		lines = append(lines, planPreviewField(profile, "Readiness", string(overview.Readiness), width)...)
		disposition := displayValue(string(overview.Disposition)) + " — " + displayValue(overview.DispositionReason)
		lines = append(lines, planPreviewField(profile, "Disposition", disposition, width)...)
	}
	if priority := overview.Priority; priority != nil {
		if width > 0 && width < 80 {
			lines = append(lines, fmt.Sprintf("Priority: %s I:%s U:%s E:%s R:%s C:%s", priority.Level, priority.Impact, priority.Urgency, priority.Effort, priority.Risk, priority.Confidence))
		} else {
			lines = append(lines, renderPriorityBadges(profile, *priority))
		}
		if width <= 0 || width >= 50 {
			lines = append(lines, planPreviewField(profile, "Priority rationale", priority.Rationale, width)...)
		}
	} else {
		lines = append(lines, planPreviewField(profile, "Priority", "unranked", width)...)
	}
	sequence := "-"
	if overview.Sequence != nil {
		sequence = fmt.Sprintf("%d of %d", overview.Sequence.Position, overview.Sequence.Total)
	}
	lines = append(lines, "Sequence: "+sequence)
	scope := strings.TrimSpace(strings.Join([]string{row.SliceID, row.SliceTitle}, " — "))
	scope = strings.Trim(scope, " —")
	lines = append(lines, "Slice scope: "+displayValue(singleLineDetail(scope)))
	relationships := "-"
	if len(row.Relationships) > 0 {
		values := make([]string, 0, len(row.Relationships))
		for _, relationship := range row.Relationships {
			values = append(values, fmt.Sprintf("%s %s [%s]", relationship.Type, relationship.PlanID, relationship.State))
		}
		relationships = strings.Join(values, "; ")
	}
	lines = append(lines, planPreviewField(profile, "Relationships", relationships, width)...)
	return lines
}

func renderPriorityBadges(profile Profile, priority plan.Priority) string {
	badges := []string{filledBadge(profile, priorityLevelRole(priority.Level), displayValue(string(priority.Level)))}
	for _, dimension := range []struct {
		label string
		value string
	}{
		{label: "impact", value: string(priority.Impact)},
		{label: "urgency", value: string(priority.Urgency)},
		{label: "effort", value: string(priority.Effort)},
		{label: "risk", value: string(priority.Risk)},
		{label: "confidence", value: string(priority.Confidence)},
	} {
		badges = append(badges, Paint(profile, RoleNeutral2, dimension.label)+" "+Paint(profile, RoleNeutral4, displayValue(dimension.value)))
	}
	return strings.Join(badges, "  ")
}

func priorityLevelRole(level plan.PriorityOverallLevel) Role {
	switch level {
	case plan.PriorityOverallLevelMust:
		return RoleWarn
	case plan.PriorityOverallLevelShould:
		return RoleInfo
	default:
		return RoleNeutral3
	}
}

func planPreviewField(profile Profile, label, value string, width int) []string {
	prefix := label + ": "
	value = displayValue(singleLineDetail(value))
	available := 0
	if width > 0 {
		available = width - visibleWidth(prefix)
	}
	wrapped := wrapDetailWords(value, available)
	indent := strings.Repeat(" ", visibleWidth(prefix))
	for index := range wrapped {
		if index == 0 {
			wrapped[index] = Paint(profile, RoleNeutral2, prefix) + Paint(profile, RoleNeutral4, wrapped[index])
		} else {
			wrapped[index] = indent + Paint(profile, RoleNeutral4, wrapped[index])
		}
	}
	return wrapped
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
		return truncateCells(sliceID, maxSliceIDCells)
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
