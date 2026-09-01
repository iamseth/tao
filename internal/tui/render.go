package tui

import (
	"fmt"
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
	plan      string
	status    string
	next      string
	phase     string
	run       string
	slices    string
	updated   string
	attention string
}

type tableWidths struct {
	repo      int
	plan      int
	status    int
	next      int
	phase     int
	run       int
	slices    int
	updated   int
	attention int
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
	focusLabel := "Repositories: all"
	if model.FocusRepositoryID != "" {
		name := singleLineDetail(model.FocusRepositoryName)
		if name == "" {
			name = singleLineDetail(model.FocusRepositoryID)
		}
		focusLabel = "Repository: " + name
	}

	activePageLabel := Paint(model.Profile, RoleNeutral5, pageLabel(page))
	header := fmt.Sprintf("Tao UI | %s | %s", activePageLabel, focusLabel)
	switch page {
	case PagePlans:
		header += " | " + planCountLabel(visibleCount)
	case PageNotes:
		header += " | " + noteCountLabel(len(visibleNotes(noteSnapshot, model.FocusRepositoryID)))
	case PageSettings:
		header = fmt.Sprintf("Tao UI | %s | %s", activePageLabel, settingsCountLabel(len(model.SettingsSnapshot.Repositories)))
	case PageDebug:
		header = fmt.Sprintf("Tao UI | %s | diagnostics", activePageLabel)
	}
	if page != PageDebug && page != PageSettings && (model.SearchActive || normalizedSearchQuery(model.SearchQuery) != "") {
		header += " | " + searchHeaderLabel(model.SearchQuery, model.SearchActive)
	}
	lines := []string{header}
	selectedLine := -1
	previewStart := -1
	switch {
	case page == PageSettings:
		settingsLines, settingsSelectedLine := renderSettingsPage(model)
		selectedLine = len(lines) + settingsSelectedLine
		if settingsSelectedLine < 0 {
			selectedLine = -1
		}
		lines = append(lines, settingsLines...)
	case page == PageDebug:
		body := renderDebugPage(model)
		bodyHeight := len(body)
		if model.Height > 0 {
			bodyHeight = max(model.Height-1, 0)
		}
		start := max(0, min(model.DebugOffset, max(len(body)-bodyHeight, 0)))
		end := min(start+bodyHeight, len(body))
		lines = append(lines, body[start:end]...)
	case page == PageNotes:
		now := model.Now
		if now.IsZero() {
			now = time.Now()
		}
		noteLines, noteSelectedLine := renderNotesPage(noteSnapshot, model.Selected, model.FocusRepositoryID, now, model.Profile)
		selectedLine = len(lines) + noteSelectedLine
		if noteSelectedLine < 0 {
			selectedLine = -1
		}
		lines = append(lines, noteLines...)
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
			lines = append(lines, "", section.Title, renderHeader(widths, section.Kind == SectionAttention))
			for _, row := range section.Rows {
				if selected == model.Selected {
					selectedLine = len(lines)
					selectedRow = row
					hasSelectedRow = true
				}
				lines = append(lines, renderTableRow(row, model.Snapshot.CollectedAt, widths, section.Kind == SectionAttention, selected == model.Selected, model.Profile, model.ActionLabels[actionRowKey(row)]))
				selected++
			}
		}
		if hasSelectedRow {
			previewStart = len(lines)
			lines = append(lines, renderPlanPreview(selectedRow, model.Width)...)
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
	if page == PagePlans && previewStart >= 0 {
		lines = planTableViewport(lines, selectedLine, previewStart, footerStart, model.Height)
	} else {
		lines = tableViewport(lines, selectedLine, footerStart, 1, model.Height)
	}
	if model.Width > 0 {
		for index := range lines {
			lines[index] = truncateCells(lines[index], model.Width)
		}
	}
	if model.ShowShortcuts {
		lines = overlayShortcutLegend(lines, page, model.Width, model.Height, model.Profile)
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func planTableViewport(lines []string, selectedLine, previewStart, footerStart, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	const headerCount = 1
	if height <= headerCount {
		return lines[:height]
	}

	footer := compactFooter(lines[footerStart:])
	available := height - headerCount
	if len(footer) >= available {
		footer = footer[len(footer)-(available-1):]
	}

	available -= len(footer)
	tableBody := lines[headerCount:previewStart]
	preview := lines[previewStart:footerStart]
	// Keep enough table context for its section and column headings when space
	// permits. A selected row always takes precedence over preview content.
	tableHeight := min(len(tableBody), min(3, available))
	previewHeight := min(len(preview), available-tableHeight)
	tableHeight = min(len(tableBody), available-previewHeight)

	start := selectedLine - headerCount - tableHeight/2
	start = max(0, min(start, len(tableBody)-tableHeight))
	viewport := make([]string, 0, height)
	viewport = append(viewport, lines[:headerCount]...)
	viewport = append(viewport, tableBody[start:start+tableHeight]...)
	viewport = append(viewport, preview[:previewHeight]...)
	return append(viewport, footer...)
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

func tableViewport(lines []string, selectedLine, footerStart, headerCount, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	headerCount = min(headerCount, footerStart)
	if height <= headerCount {
		return lines[:height]
	}
	if height == headerCount+1 {
		return append(append([]string(nil), lines[:headerCount]...), lines[len(lines)-1])
	}

	body := lines[headerCount:footerStart]
	footer := compactFooter(lines[footerStart:])
	available := height - headerCount
	footerLimit := available
	if len(body) > 0 {
		footerLimit-- // Reserve one line for page content.
	}
	if len(footer) > footerLimit {
		footer = footer[len(footer)-footerLimit:]
	}

	bodyHeight := min(len(body), available-len(footer))
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
		repo: len("REPO"), plan: len("PLAN"), status: len("STATUS"), next: len("NEXT"), phase: len("PHASE/SLICE"),
		run: len("RUN AGE"), slices: len("SLICES"), updated: len("UPDATED"), attention: len("ATTENTION"),
	}
	for _, section := range sections {
		for _, row := range section.Rows {
			values := tableRowValues(row, now, actionLabels[actionRowKey(row)])
			widths.repo = max(widths.repo, visibleWidth(values.repo))
			widths.plan = max(widths.plan, visibleWidth(values.plan))
			widths.status = max(widths.status, visibleWidth(values.status))
			widths.next = max(widths.next, visibleWidth(values.next))
			widths.phase = max(widths.phase, visibleWidth(values.phase))
			widths.run = max(widths.run, visibleWidth(values.run))
			widths.slices = max(widths.slices, visibleWidth(values.slices))
			widths.updated = max(widths.updated, visibleWidth(values.updated))
			widths.attention = max(widths.attention, visibleWidth(values.attention))
		}
	}
	return widths
}

func renderHeader(widths tableWidths, withAttention bool) string {
	updated := "UPDATED"
	if withAttention {
		updated = padCells(updated, widths.updated)
	}
	line := "  " + strings.Join([]string{
		padCells("REPO", widths.repo),
		padCells("PLAN", widths.plan),
		padCells("STATUS", widths.status),
		padCells("NEXT", widths.next),
		padCells("PHASE/SLICE", widths.phase),
		padCells("RUN AGE", widths.run),
		padCells("SLICES", widths.slices),
		updated,
	}, "  ")
	if withAttention {
		line += "  ATTENTION"
	}
	return line
}

func renderTableRow(row monitor.Row, now time.Time, widths tableWidths, withAttention, selected bool, profile Profile, actionLabel string) string {
	values := tableRowValues(row, now, actionLabel)
	cursor := "  "
	if selected {
		cursor = "> "
	}
	status := padCells(values.status, widths.status)
	status = colorStatus(profile, status, row.Status)
	updated := values.updated
	if withAttention {
		updated = padCells(updated, widths.updated)
	}
	line := cursor + strings.Join([]string{
		padCells(values.repo, widths.repo),
		padCells(values.plan, widths.plan),
		status,
		padCells(values.next, widths.next),
		padCells(values.phase, widths.phase),
		padCells(values.run, widths.run),
		padCells(values.slices, widths.slices),
		updated,
	}, "  ")
	if withAttention {
		line += "  " + values.attention
	}
	return line
}

func renderPlanPreview(row monitor.Row, width int) []string {
	overview := row.Overview
	lines := []string{"SELECTED PLAN — advisory context"}
	if row.Status == plan.StatusAbandoned {
		lines = append(lines,
			"Abandoned at: "+formatAbandonedAt(row.AbandonedAt),
			"Abandonment reason: "+planview.FormatAbandonmentText(row.AbandonmentReason),
		)
	}
	lines = append(lines, "Benefit: "+displayValue(singleLineDetail(overview.ExpectedBenefit)))
	if width > 0 && width < 50 {
		lines = append(lines, fmt.Sprintf("Decision: %s / %s", displayValue(string(overview.Disposition)), displayValue(string(overview.Readiness))))
	} else {
		lines = append(lines,
			"Readiness: "+displayValue(string(overview.Readiness)),
			"Disposition: "+displayValue(string(overview.Disposition))+" — "+displayValue(singleLineDetail(overview.DispositionReason)),
		)
	}
	if priority := overview.Priority; priority != nil {
		if width > 0 && width < 80 {
			lines = append(lines, fmt.Sprintf("Priority: %s I:%s U:%s E:%s R:%s C:%s", priority.Level, priority.Impact, priority.Urgency, priority.Effort, priority.Risk, priority.Confidence))
		} else {
			lines = append(lines, fmt.Sprintf("Priority: level=%s  impact=%s  urgency=%s  effort=%s  risk=%s  confidence=%s", priority.Level, priority.Impact, priority.Urgency, priority.Effort, priority.Risk, priority.Confidence))
		}
		if width <= 0 || width >= 50 {
			lines = append(lines, "Priority rationale: "+displayValue(singleLineDetail(priority.Rationale)))
		}
	} else {
		lines = append(lines, "Priority: unranked")
	}
	sequence := "-"
	if overview.Sequence != nil {
		sequence = fmt.Sprintf("%d of %d", overview.Sequence.Position, overview.Sequence.Total)
	}
	lines = append(lines, "Sequence: "+sequence)
	scope := strings.TrimSpace(strings.Join([]string{row.SliceID, row.SliceTitle}, " — "))
	scope = strings.Trim(scope, " —")
	lines = append(lines, "Slice scope: "+displayValue(singleLineDetail(scope)))
	if len(row.Relationships) == 0 {
		lines = append(lines, "Relationships: -")
	} else {
		values := make([]string, 0, len(row.Relationships))
		for _, relationship := range row.Relationships {
			values = append(values, fmt.Sprintf("%s %s [%s]", relationship.Type, relationship.PlanID, relationship.State))
		}
		lines = append(lines, "Relationships: "+strings.Join(values, "; "))
	}
	return lines
}

func tableRowValues(row monitor.Row, now time.Time, actionLabel string) rowValues {
	status := row.Status
	if strings.TrimSpace(actionLabel) != "" {
		status = actionLabel
	}
	next := row.NextAction
	if strings.TrimSpace(next) == "" || row.Status == plan.StatusAbandoned {
		next = monitor.DeriveNextAction(row)
	}
	return rowValues{
		repo:      displayValue(row.RepositoryName),
		plan:      planLabel(row),
		status:    displayValue(status),
		next:      displayValue(next),
		phase:     phaseLabel(row),
		run:       runAgeLabel(row),
		slices:    slicesLabel(row),
		updated:   plan.FormatHumanTime(row.UpdatedAt, now),
		attention: attentionLabel(row.AttentionReasons),
	}
}

func planLabel(row monitor.Row) string {
	if strings.TrimSpace(row.PlanID) == "" {
		return displayValue(row.PlanTitle)
	}
	if _, ok := plan.PlanSlug(row.PlanID); ok {
		return row.PlanID
	}
	title := strings.TrimSpace(row.PlanTitle)
	if title == "" || title == row.PlanID {
		return row.PlanID
	}
	return planview.ShortPlanID(row.PlanID) + " " + title
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
