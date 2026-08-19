package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	planview "github.com/iamseth/tao/internal/view"
)

const (
	clearScreenSequence = "\x1b[H\x1b[2J"
	plansFooterHints    = "r run  a approve  m merge  M merge all  f repository  c completed  Enter plan  Tab/←/→ tabs  q quit  Esc Esc quit"
	notesFooterHints    = "f repository  Tab/←/→ tabs  q quit  Esc Esc quit"
	footerHints         = plansFooterHints
	maxSliceIDRunes     = 20
)

// Model contains the render-neutral state for one UI frame.
type Model struct {
	Snapshot            monitor.Snapshot
	NoteSnapshot        note.Snapshot
	Page                PageID
	Selected            int
	Width               int
	Height              int
	Now                 time.Time
	HideCompleted       bool
	FocusRepositoryID   string
	FocusRepositoryName string
	UseColor            bool
	ConfirmMessage      string
	ActionLabels        map[string]string
	ActionMessage       string
}

type rowValues struct {
	repo      string
	plan      string
	status    string
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
	phase     int
	run       int
	slices    int
	updated   int
	attention int
}

// Render builds one complete terminal frame without writing it.
func Render(model Model) string {
	page := normalizePage(model.Page)
	sections := BuildRepositorySections(model.Snapshot.Rows, !model.HideCompleted, model.FocusRepositoryID)
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

	header := fmt.Sprintf("Tao UI | %s", focusLabel)
	if page == PagePlans {
		header += " | " + planCountLabel(visibleCount)
	} else {
		header += " | " + noteCountLabel(len(visibleNotes(model.NoteSnapshot, model.FocusRepositoryID)))
	}
	lines := []string{header, renderTabBar(page)}
	selectedLine := -1
	switch {
	case page == PageNotes:
		now := model.Now
		if now.IsZero() {
			now = time.Now()
		}
		noteLines, noteSelectedLine := renderNotesPage(model.NoteSnapshot, model.Selected, model.FocusRepositoryID, now, model.UseColor)
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
		for _, section := range sections {
			if len(section.Rows) == 0 {
				continue
			}
			lines = append(lines, "", section.Title, renderHeader(widths, section.Kind == SectionAttention))
			for _, row := range section.Rows {
				if selected == model.Selected {
					selectedLine = len(lines)
				}
				lines = append(lines, renderTableRow(row, model.Snapshot.CollectedAt, widths, section.Kind == SectionAttention, selected == model.Selected, model.UseColor, model.ActionLabels[actionRowKey(row)]))
				selected++
			}
		}
	}
	footerStart := len(lines)
	if page == PagePlans && strings.TrimSpace(model.ActionMessage) != "" {
		lines = append(lines, "", model.ActionMessage)
	}
	if strings.TrimSpace(model.ConfirmMessage) != "" {
		lines = append(lines, "", model.ConfirmMessage+" [y/n]")
	}
	footer := plansFooterHints
	if page == PageNotes {
		footer = notesFooterHints
	}
	lines = append(lines, "", footer)
	lines = tableViewport(lines, selectedLine, footerStart, 2, model.Height)
	if model.Width > 0 {
		for index := range lines {
			lines[index] = truncateANSI(lines[index], model.Width)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
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
	footer := make([]string, 0, len(lines)-footerStart)
	for _, line := range lines[footerStart:] {
		if strings.TrimSpace(line) != "" {
			footer = append(footer, line)
		}
	}
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
		repo: len("REPO"), plan: len("PLAN"), status: len("STATUS"), phase: len("PHASE/SLICE"),
		run: len("RUN"), slices: len("SLICES"), updated: len("UPDATED"), attention: len("ATTENTION"),
	}
	for _, section := range sections {
		for _, row := range section.Rows {
			values := tableRowValues(row, now, actionLabels[actionRowKey(row)])
			widths.repo = max(widths.repo, utf8.RuneCountInString(values.repo))
			widths.plan = max(widths.plan, utf8.RuneCountInString(values.plan))
			widths.status = max(widths.status, utf8.RuneCountInString(values.status))
			widths.phase = max(widths.phase, utf8.RuneCountInString(values.phase))
			widths.run = max(widths.run, utf8.RuneCountInString(values.run))
			widths.slices = max(widths.slices, utf8.RuneCountInString(values.slices))
			widths.updated = max(widths.updated, utf8.RuneCountInString(values.updated))
			widths.attention = max(widths.attention, utf8.RuneCountInString(values.attention))
		}
	}
	return widths
}

func renderHeader(widths tableWidths, withAttention bool) string {
	updated := "UPDATED"
	if withAttention {
		updated = padRunes(updated, widths.updated)
	}
	line := "  " + strings.Join([]string{
		padRunes("REPO", widths.repo),
		padRunes("PLAN", widths.plan),
		padRunes("STATUS", widths.status),
		padRunes("PHASE/SLICE", widths.phase),
		padRunes("RUN", widths.run),
		padRunes("SLICES", widths.slices),
		updated,
	}, "  ")
	if withAttention {
		line += "  ATTENTION"
	}
	return line
}

func renderTableRow(row monitor.Row, now time.Time, widths tableWidths, withAttention, selected, useColor bool, actionLabel string) string {
	values := tableRowValues(row, now, actionLabel)
	cursor := "  "
	if selected {
		cursor = "> "
	}
	status := padRunes(values.status, widths.status)
	if useColor {
		status = colorStatus(status, row.Status)
	}
	updated := values.updated
	if withAttention {
		updated = padRunes(updated, widths.updated)
	}
	line := cursor + strings.Join([]string{
		padRunes(values.repo, widths.repo),
		padRunes(values.plan, widths.plan),
		status,
		padRunes(values.phase, widths.phase),
		padRunes(values.run, widths.run),
		padRunes(values.slices, widths.slices),
		updated,
	}, "  ")
	if withAttention {
		line += "  " + values.attention
	}
	return line
}

func tableRowValues(row monitor.Row, now time.Time, actionLabel string) rowValues {
	status := row.Status
	if strings.TrimSpace(actionLabel) != "" {
		status = actionLabel
	}
	return rowValues{
		repo:      displayValue(row.RepositoryName),
		plan:      planLabel(row),
		status:    displayValue(status),
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
	if isStalled(row) {
		return fmt.Sprintf("stalled? (%s old)", durationLabel(row.HeartbeatAge))
	}
	phase := strings.TrimSpace(string(row.Phase))
	sliceID := strings.TrimSpace(row.SliceID)
	if sliceID != "" && (phase == "" || phase == "running_slice") {
		runes := []rune(sliceID)
		if len(runes) > maxSliceIDRunes {
			runes = runes[:maxSliceIDRunes]
		}
		return string(runes)
	}
	return displayValue(phase)
}

func runAgeLabel(row monitor.Row) string {
	if row.Liveness != monitor.LivenessLive && row.Liveness != monitor.LivenessStale {
		return "-"
	}
	return durationLabel(row.InvocationDuration)
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

func padRunes(value string, width int) string {
	visible := utf8.RuneCountInString(value)
	if visible >= width {
		return value
	}
	return value + strings.Repeat(" ", width-visible)
}

func colorStatus(value, status string) string {
	code := "35"
	switch status {
	case plan.StatusCompleted, plan.StatusReviewed:
		code = "32"
	case plan.StatusInProgress:
		code = "36"
	case plan.StatusInReview:
		code = "34"
	case plan.StatusBlocked:
		code = "31"
	case plan.StatusPlanned, plan.StatusPending, plan.StatusChangesRequested:
		code = "33"
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func truncateANSI(value string, width int) string {
	if width <= 0 {
		return value
	}
	var result strings.Builder
	visible := 0
	styleActive := false
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			end := index + 2
			for end < len(value) && (value[end] < '@' || value[end] > '~') {
				end++
			}
			if end < len(value) {
				sequence := value[index : end+1]
				result.WriteString(sequence)
				if value[end] == 'm' {
					styleActive = sequence != "\x1b[0m"
				}
				index = end + 1
				continue
			}
		}
		if visible >= width {
			break
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		result.WriteRune(r)
		visible++
		index += size
	}
	if styleActive {
		result.WriteString("\x1b[0m")
	}
	return result.String()
}
