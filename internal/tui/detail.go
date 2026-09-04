package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
	planview "github.com/iamseth/tao/internal/view"
)

const (
	detailLogTailLines         = 200
	detailLogKeepLines         = 1000
	detailLogTabWidth          = 4
	planDetailHeaderGap        = 1
	planDetailPaneGap          = 1
	planDetailFixedLines       = 6 // header, two gaps, tabs, and pane borders
	planOverviewFixedLines     = 4 // header, gap, tabs, and content gap
	detailOverviewScopePreview = 6
	detailOverviewMaxQuestions = 12
	noteDetailHeaderLines      = 8
	noteDetailFooter           = "↑/↓/j/k scroll  Bksp/Esc back  q quit"
)

// detailTab identifies the independently navigable plan-detail views.
type detailTab int

const (
	detailTabOverview detailTab = iota
	detailTabSlices
	detailTabActivity
	detailTabCount
)

func (t detailTab) label() string {
	return [...]string{"Overview", "Slices", "Activity"}[max(0, min(int(detailTabCount)-1, int(t)))]
}

// DetailRepository is the read-only plan and log boundary used by the detail
// page. FileRepository satisfies it, while tests can inject a follower without
// touching plan artifacts.
type DetailRepository interface {
	plan.Resolver
	plan.LogTailReader
	plan.LogFollower
}

// DetailModel contains the render-neutral state for one detail frame.
type DetailModel struct {
	Plan            *plan.PlanDetail
	Row             monitor.Row
	Log             string
	SliceLog        string
	SelectedSliceID string
	SliceOpen       bool
	ActiveTab       detailTab
	OverviewOffset  int
	ActivityOffset  int
	SliceOffset     int
	Width           int
	Height          int
	UseColor        bool
	Profile         Profile
	ShowShortcuts   bool
	ScopeExpanded   bool
	LoadError       string
	FollowError     string
	Inspection      detailInspectionView
}

type detailState struct {
	row               monitor.Row
	plan              *plan.PlanDetail
	selectedSliceID   string
	sliceOpen         bool
	activeTab         detailTab
	overviewOffset    int
	activityOffset    int
	sliceOffset       int
	log               string
	sliceLogs         map[string]string
	activeLogSlice    string
	loadError         string
	followError       string
	scopeExpanded     bool
	updates           <-chan detailFollowUpdate
	inspection        detailInspectionView
	inspectionKey     string
	inspectionUpdates <-chan detailInspectionUpdate
	inspectionCancel  context.CancelFunc
	ctx               context.Context
	cancel            context.CancelFunc
}

type detailFollowUpdate struct {
	text string
	err  error
}

// RenderNoteDetail builds a bounded, read-only frame for one open note.
func RenderNoteDetail(item note.CatalogNote, width, height int) string {
	return renderNoteDetail(item, width, height, 0)
}

func renderNoteDetail(item note.CatalogNote, width, height, offset int) string {
	header, body := noteDetailSections(item, width)
	bodyHeight := noteDetailBodyHeight(len(body), height)
	offset = max(0, min(offset, len(body)-bodyHeight))
	lines := append([]string(nil), header...)
	lines = append(lines, body[offset:offset+bodyHeight]...)
	lines = append(lines, noteDetailFooter)
	return fitDetailFrame(lines, width, height)
}

func noteDetailSections(item note.CatalogNote, width int) (header, body []string) {
	created := "-"
	if !item.CreatedAt.IsZero() {
		created = item.CreatedAt.Format(time.RFC3339)
	}
	updated := "-"
	if !item.UpdatedAt.IsZero() {
		updated = item.UpdatedAt.Format(time.RFC3339)
	}
	header = []string{
		"Tao UI | NOTE DETAIL",
		"Repository: " + displayValue(singleLineNoteValue(item.RepositoryName)),
		"Note: " + displayValue(singleLineNoteValue(item.ID)),
		"Status: open",
		"Tags: " + displayValue(singleLineNoteValue(strings.Join(item.Tags, ", "))),
		"Created: " + created,
		"Updated: " + updated,
		"Text:",
	}
	return header, renderNoteText(item.Text, width)
}

func noteDetailBodyHeight(bodyLines, height int) int {
	if height <= 0 {
		return bodyLines
	}
	return max(0, min(bodyLines, height-noteDetailHeaderLines-1))
}

func renderNoteText(text string, width int) []string {
	text = sanitizeNoteText(text)
	if text == "" {
		return []string{"  -"}
	}
	available := width - 2
	if width <= 0 {
		available = 0
	} else {
		available = max(available, 1)
	}
	var lines []string
	for _, source := range strings.Split(text, "\n") {
		if available <= 0 || cells.Width(source) <= available {
			lines = append(lines, "  "+source)
			continue
		}
		for cells.Width(source) > available {
			prefix := cells.Truncate(source, available)
			if prefix == "" {
				_, size := utf8.DecodeRuneInString(source)
				source = source[size:]
				continue
			}
			lines = append(lines, "  "+prefix)
			source = strings.TrimPrefix(source, prefix)
		}
		lines = append(lines, "  "+source)
	}
	return lines
}

func sanitizeNoteText(value string) string {
	var printable strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) {
			switch value[index+1] {
			case '[':
				index = skipDetailCSI(value, index+2)
				continue
			case ']':
				index = skipDetailOSC(value, index+2)
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		switch r {
		case '\u009b':
			index = skipDetailCSI(value, index+size)
			continue
		case '\u009d':
			index = skipDetailOSC(value, index+size)
			continue
		case '\n':
			printable.WriteRune(r)
		default:
			if unicode.IsPrint(r) {
				printable.WriteRune(r)
			} else {
				printable.WriteByte(' ')
			}
		}
		index += size
	}
	return strings.TrimSpace(printable.String())
}

// RenderDetail builds one complete detail-page frame without writing it.
func RenderDetail(model DetailModel) string {
	if model.SliceOpen {
		return RenderSliceDetail(model)
	}
	id, _, repoName, status := detailHeaderValues(model)
	phase := displayValue(strings.TrimSpace(string(model.Row.Phase)))
	heartbeat := "-"
	if model.Row.Liveness == monitor.LivenessLive || model.Row.Liveness == monitor.LivenessStale {
		heartbeat = durationLabel(model.Row.HeartbeatAge) + " ago"
	}
	profile := model.Profile
	if profile == ProfileNone {
		profile = profileForEnabledColor(model.UseColor)
	}
	tab := model.ActiveTab
	if tab < detailTabOverview || tab >= detailTabCount {
		tab = detailTabOverview
	}
	header := "Tao UI | " + singleLineDetail(id) + " | " + singleLineDetail(repoName)
	if tab != detailTabOverview {
		header += " | " + singleLineDetail(status) + " | " + singleLineDetail(phase) + " | " + singleLineDetail(heartbeat)
	} else if phase != "-" || heartbeat != "-" {
		header += " | " + singleLineDetail(phase) + " | " + singleLineDetail(heartbeat)
	}
	if profile != ProfileNone {
		header = Paint(profile, RoleDetailSecondary, header)
	}
	tabs := renderDetailTabs(tab, profile)
	paneHeight := 24
	if model.Height > 0 {
		paneHeight = max(model.Height-planOverviewFixedLines, 0)
	}
	bodyHeight := paneHeight
	if tab != detailTabOverview {
		bodyHeight = max(paneHeight-2, 0)
	}
	var content []string
	switch {
	case model.LoadError != "":
		content = []string{"unable to load plan: " + singleLineDetail(model.LoadError)}
	case model.Plan == nil:
		content = []string{"Plan details unavailable."}
	default:
		switch tab {
		case detailTabSlices:
			content = renderSlicesPane(model.Plan, model.SelectedSliceID, model.Width, bodyHeight, model.UseColor)
		case detailTabActivity:
			content = renderActivityPane(model.Log, model.FollowError, model.Width, bodyHeight, model.ActivityOffset)
		default:
			content = renderOverviewPane(model.Plan, model.Row, model.Width, bodyHeight, model.OverviewOffset, model.Inspection, profile, model.ScopeExpanded)
		}
	}

	lines := []string{header, "", tabs}
	if tab == detailTabOverview {
		lines = append(lines, "")
		lines = append(lines, content...)
	} else {
		lines = append(lines, "")
		if paneHeight > 0 {
			lines = append(lines, renderPaneBox(strings.ToUpper(tab.label()), content, model.Width, paneHeight)...)
		}
	}
	if model.Width > 0 {
		for index := range lines {
			lines[index] = cells.Truncate(lines[index], model.Width)
		}
	}
	if model.Height > 0 {
		if len(lines) > model.Height {
			lines = lines[:model.Height]
		}
		for len(lines) < model.Height {
			lines = append(lines, " ")
		}
	}
	if model.ShowShortcuts {
		lines = overlayPlanDetailShortcuts(lines, model.Width, model.Height, model.UseColor)
	}
	if profile != ProfileNone {
		for index, line := range lines {
			if model.Width > 0 {
				line = cells.Pad(line, model.Width)
			}
			if line == "" {
				line = " "
			}
			lines[index] = fillRow(profile, RoleDetailBackground, line)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func renderDetailTabs(active detailTab, profile Profile) string {
	parts := make([]string, 0, detailTabCount)
	for tab := detailTabOverview; tab < detailTabCount; tab++ {
		label := tab.label()
		role := RoleDetailMuted
		if tab == active {
			label = "[" + label + "]"
			role = RoleDetailInfo
		}
		parts = append(parts, Paint(profile, role, label))
	}
	return strings.Join(parts, "  ")
}

func renderOverviewPane(detail *plan.PlanDetail, row monitor.Row, width, height, offset int, inspection detailInspectionView, profile Profile, scopeExpanded bool) []string {
	if detail == nil {
		return nil
	}
	overview := plan.ProjectDecisionOverview(detail)
	status := detail.State.Status
	if strings.TrimSpace(status) == "" {
		status = row.Status
	}
	priorityLevel := "-"
	if overview.Priority != nil {
		priorityLevel = overviewDisplay(string(overview.Priority.Level))
	}

	var lines []string
	appendOverviewTitle(&lines, detail.State.Plan.Title, width, profile)
	lines = append(lines, renderDetailMetadata([]detailGridField{
		{label: "TYPE", value: overviewDisplay(string(detail.State.Plan.ChangeType)), role: RoleDetailInfo},
		{label: "STATUS", value: overviewDisplay(status), role: detailStateRole(status)},
		{label: "READINESS", value: overviewDisplay(string(overview.Readiness)), role: detailStateRole(string(overview.Readiness))},
		{label: "PRIORITY", value: priorityLevel, role: detailPriorityRole(priorityLevel)},
	}, width, profile)...)

	attention := detailAttentionLines(detail, row, inspection, width, profile)
	if len(attention) > 0 {
		lines = append(lines, Paint(profile, RoleDetailWarning, "! ATTENTION"))
		lines = append(lines, attention...)
	} else if inspection.status == detailInspectionLoading || inspection.status == detailInspectionUnavailable {
		lines = append(lines, Paint(profile, RoleDetailMuted, "INSPECTION  ")+Paint(profile, detailStalenessRole(inspection), detailStalenessSummary(inspection)))
	}

	lines = append(lines, "", detailSectionHeading("CONTEXT", width, profile))
	appendOverviewLabeledText(&lines, "Problem", overview.Problem, width, profile)
	lines = append(lines, "")
	appendOverviewLabeledText(&lines, "Why now", overview.WhyNow, width, profile)
	lines = append(lines, "")
	appendOverviewLabeledText(&lines, "Expected benefit", overview.ExpectedBenefit, width, profile)
	questions := boundedOverviewValues(detail.State.OpenQuestions, detailOverviewMaxQuestions)
	if len(questions) > 0 {
		lines = append(lines, "", Paint(profile, RoleDetailMuted, "Open questions"))
		for _, question := range questions {
			appendOverviewBullet(&lines, question, width, profile, RoleDetailSecondary, "?")
		}
	}

	lines = append(lines, "", detailSectionHeading("SUCCESS CRITERIA", width, profile))
	if len(overview.SuccessCriteria) == 0 {
		appendOverviewBullet(&lines, "-", width, profile, RoleDetailMuted, "☐")
	} else {
		for _, criterion := range overview.SuccessCriteria {
			appendOverviewBullet(&lines, criterion, width, profile, RoleDetailSecondary, "☐")
		}
	}

	if overview.Priority != nil {
		priority := overview.Priority
		lines = append(lines, "", detailSectionHeading("PRIORITY", width, profile))
		lines = append(lines, renderPriorityGrid(priority, width, profile)...)
		appendOverviewMutedParagraph(&lines, "Rationale", priority.Rationale, width, profile)
	}

	lines = append(lines, "", detailSectionHeading("SCOPE", width, profile))
	scope := detailScope(detail)
	visibleScope := scope
	if !scopeExpanded && len(visibleScope) > detailOverviewScopePreview {
		visibleScope = visibleScope[:detailOverviewScopePreview]
	}
	if len(visibleScope) == 0 {
		appendOverviewBullet(&lines, "-", width, profile, RoleDetailMuted, "•")
	} else {
		for _, file := range visibleScope {
			appendOverviewBullet(&lines, file, width, profile, RoleDetailSecondary, "•")
		}
	}
	if remaining := len(scope) - len(visibleScope); remaining > 0 {
		appendOverviewBullet(&lines, fmt.Sprintf("+%d more — press e to expand", remaining), width, profile, RoleDetailInfo, "")
	} else if scopeExpanded && len(scope) > detailOverviewScopePreview {
		appendOverviewBullet(&lines, "press e to collapse", width, profile, RoleDetailInfo, "↥")
	}

	return fitDetailPaneAt(lines, width, height, offset)
}

func detailAbandonmentLines(detail *plan.PlanDetail, row monitor.Row) []string {
	if detail.State.Status != plan.StatusAbandoned && row.Status != plan.StatusAbandoned {
		return nil
	}
	abandonedAt := row.AbandonedAt
	reason := row.AbandonmentReason
	if evidence := plan.ProjectAbandonment(detail.Events); evidence != nil {
		if abandonedAt == nil && !evidence.AbandonedAt.IsZero() {
			at := evidence.AbandonedAt.UTC()
			abandonedAt = &at
		}
		if strings.TrimSpace(reason) == "" {
			reason = evidence.Reason
		}
	}
	return []string{
		"Abandoned at: " + formatAbandonedAt(abandonedAt),
		"Abandonment reason: " + planview.FormatAbandonmentText(reason),
	}
}

func overviewDisplay(value string) string {
	value = cells.Truncate(singleLineDetail(value), 240)
	if value == "" {
		return "-"
	}
	return value
}

type detailGridField struct {
	label string
	value string
	role  Role
}

func appendOverviewTitle(lines *[]string, title string, width int, profile Profile) {
	wrapped := wrapDetailWords(overviewDisplay(title), detailContentWidth(width, 0))
	for _, line := range wrapped {
		if profile == ProfileNone {
			*lines = append(*lines, line)
		} else {
			*lines = append(*lines, boldSequence+Paint(profile, RoleDetailPrimary, line))
		}
	}
}

func renderDetailMetadata(fields []detailGridField, width int, profile Profile) []string {
	var lines []string
	var line strings.Builder
	lineWidth := 0
	separator := Paint(profile, RoleDetailMuted, "  ·  ")
	for _, field := range fields {
		value := overviewDisplay(field.value)
		plainWidth := cells.Width(field.label) + 2 + cells.Width(value)
		styled := Paint(profile, RoleDetailMuted, field.label) + "  " + Paint(profile, field.role, value)
		separatorWidth := 0
		if lineWidth > 0 {
			separatorWidth = 5
		}
		if width > 0 && lineWidth > 0 && lineWidth+separatorWidth+plainWidth > width {
			lines = append(lines, line.String())
			line.Reset()
			lineWidth = 0
			separatorWidth = 0
		}
		if separatorWidth > 0 {
			line.WriteString(separator)
		}
		line.WriteString(styled)
		lineWidth += separatorWidth + plainWidth
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return lines
}

func appendOverviewLabeledText(lines *[]string, label, value string, width int, profile Profile) {
	*lines = append(*lines, Paint(profile, RoleDetailMuted, label))
	appendOverviewText(lines, overviewDisplay(value), width, profile, RoleDetailSecondary, "  ")
}

func renderPriorityGrid(priority *plan.Priority, width int, profile Profile) []string {
	rows := [][]detailGridField{
		{
			{label: "Impact", value: string(priority.Impact), role: detailPriorityRole(string(priority.Impact))},
			{label: "Urgency", value: string(priority.Urgency), role: detailPriorityRole(string(priority.Urgency))},
			{label: "Risk", value: string(priority.Risk), role: detailRiskRole(string(priority.Risk))},
		},
		{
			{label: "Effort", value: string(priority.Effort), role: RoleDetailSecondary},
			{label: "Confidence", value: string(priority.Confidence), role: detailPriorityRole(string(priority.Confidence))},
		},
	}
	const gutter = 2
	columnWidths := []int{15, 19}
	thirdWidth := cells.Width(rows[0][2].label + " " + overviewDisplay(rows[0][2].value))
	requiredWidth := columnWidths[0] + gutter + columnWidths[1] + gutter + thirdWidth
	if width > 0 && requiredWidth > width {
		return renderDetailGrid(append(rows[0], rows[1]...), width, 3, profile)
	}
	lines := make([]string, 0, len(rows))
	for _, fields := range rows {
		var line strings.Builder
		for column, field := range fields {
			value := overviewDisplay(field.value)
			cell := Paint(profile, RoleDetailMuted, field.label) + " " + Paint(profile, field.role, value)
			if column < len(fields)-1 {
				cell = cells.Pad(cell, columnWidths[column]) + strings.Repeat(" ", gutter)
			}
			line.WriteString(cell)
		}
		lines = append(lines, line.String())
	}
	return lines
}

func appendOverviewMutedParagraph(lines *[]string, label, value string, width int, profile Profile) {
	if value = singleLineDetail(value); value == "" {
		return
	}
	prefix := label + "  "
	wrapped := wrapDetailWords(value, detailContentWidth(width, cells.Width(prefix)))
	for index, line := range wrapped {
		if index == 0 {
			*lines = append(*lines, Paint(profile, RoleDetailMuted, prefix+line))
		} else {
			*lines = append(*lines, Paint(profile, RoleDetailMuted, strings.Repeat(" ", cells.Width(prefix))+line))
		}
	}
}

func appendOverviewText(lines *[]string, value string, width int, profile Profile, role Role, indent string) {
	for _, line := range wrapDetailWords(singleLineDetail(value), detailContentWidth(width, cells.Width(indent))) {
		*lines = append(*lines, indent+Paint(profile, role, line))
	}
}

func appendOverviewBullet(lines *[]string, value string, width int, profile Profile, role Role, marker string) {
	value = singleLineDetail(value)
	if value == "" {
		return
	}
	prefix := "  " + marker + " "
	wrapped := wrapDetailWords(value, detailContentWidth(width, cells.Width(prefix)))
	for index, line := range wrapped {
		if index == 0 {
			*lines = append(*lines, Paint(profile, RoleDetailMuted, prefix)+Paint(profile, role, line))
		} else {
			*lines = append(*lines, strings.Repeat(" ", cells.Width(prefix))+Paint(profile, role, line))
		}
	}
}

func detailContentWidth(width, prefix int) int {
	if width <= 0 {
		return 0
	}
	return max(width-prefix, 1)
}

func detailSectionHeading(title string, width int, profile Profile) string {
	plainTitle := strings.ToUpper(singleLineDetail(title))
	if width <= 0 {
		width = 72
	}
	dividerWidth := max(min(width-cells.Width(plainTitle)-1, 20), 1)
	return Paint(profile, RoleDetailSecondary, plainTitle) + " " + Paint(profile, RoleDetailDivider, strings.Repeat("─", dividerWidth))
}

func renderDetailGrid(fields []detailGridField, width, maxColumns int, profile Profile) []string {
	if len(fields) == 0 {
		return nil
	}
	available := width
	if available <= 0 {
		available = 96
	}
	columns := min(maxColumns, len(fields))
	const gutter = 3
	for columns > 1 && (available-gutter*(columns-1))/columns < 18 {
		columns--
	}
	columnWidth := max((available-gutter*(columns-1))/columns, 1)
	rows := make([]string, 0, (len(fields)+columns-1)/columns)
	for start := 0; start < len(fields); start += columns {
		var row strings.Builder
		for column := 0; column < columns && start+column < len(fields); column++ {
			field := fields[start+column]
			plain := field.label + "  " + overviewDisplay(field.value)
			plain = cells.TruncateEllipsis(plain, columnWidth)
			labelWidth := min(cells.Width(field.label), cells.Width(plain))
			value := strings.TrimSpace(cells.Truncate(plain, columnWidth)[labelWidth:])
			cell := Paint(profile, RoleDetailMuted, cells.Truncate(field.label, labelWidth))
			if value != "" {
				cell += "  " + Paint(profile, field.role, value)
			}
			if column+1 < columns && start+column+1 < len(fields) {
				cell = cells.Pad(cell, columnWidth) + strings.Repeat(" ", gutter)
			}
			row.WriteString(cell)
		}
		rows = append(rows, row.String())
	}
	return rows
}

func detailStateRole(value string) Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ready", "completed", "approved", "current", "done":
		return RoleDetailSuccess
	case "blocked", "invalid", "failed", "error", "abandoned", "obsolete":
		return RoleDetailError
	case "needs_refinement", "conditional", "deferred", "changes_requested", "stale":
		return RoleDetailWarning
	default:
		return RoleDetailInfo
	}
}

func detailPriorityRole(value string) Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "must", "high":
		return RoleDetailInfo
	default:
		return RoleDetailSecondary
	}
}

func detailRiskRole(value string) Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return RoleDetailError
	case "medium":
		return RoleDetailWarning
	default:
		return RoleDetailSecondary
	}
}

func detailStalenessSummary(inspection detailInspectionView) string {
	switch inspection.status {
	case detailInspectionLoading:
		return "checking…"
	case detailInspectionReady:
		if len(inspection.findings) == 0 {
			return "current"
		}
		if len(inspection.findings) == 1 {
			return "1 finding"
		}
		return fmt.Sprintf("%d findings", len(inspection.findings))
	case detailInspectionFailed:
		return "check failed"
	default:
		return "unavailable"
	}
}

func detailStalenessRole(inspection detailInspectionView) Role {
	switch inspection.status {
	case detailInspectionReady:
		if len(inspection.findings) == 0 {
			return RoleDetailSuccess
		}
		return RoleDetailWarning
	case detailInspectionFailed:
		return RoleDetailError
	case detailInspectionLoading:
		return RoleDetailInfo
	default:
		return RoleDetailMuted
	}
}

func detailAttentionLines(detail *plan.PlanDetail, row monitor.Row, inspection detailInspectionView, width int, profile Profile) []string {
	var items []struct {
		text string
		role Role
	}
	for _, line := range detailAbandonmentLines(detail, row) {
		items = append(items, struct {
			text string
			role Role
		}{text: line, role: RoleDetailError})
	}
	for _, reason := range row.AttentionReasons {
		items = append(items, struct {
			text string
			role Role
		}{text: strings.ReplaceAll(string(reason), "_", " "), role: RoleDetailWarning})
	}
	for _, warning := range append(append([]string(nil), row.Warnings...), row.RelationshipWarnings...) {
		items = append(items, struct {
			text string
			role Role
		}{text: warning, role: RoleDetailWarning})
	}
	switch inspection.status {
	case detailInspectionReady:
		for _, finding := range inspection.findings {
			role := RoleDetailWarning
			switch {
			case strings.EqualFold(finding.Severity, "error"):
				role = RoleDetailError
			case strings.EqualFold(finding.Severity, "info"):
				role = RoleDetailInfo
			}
			items = append(items, struct {
				text string
				role Role
			}{text: overviewDisplay(finding.Severity) + ": " + overviewDisplay(finding.Message), role: role})
		}
	case detailInspectionFailed:
		items = append(items, struct {
			text string
			role Role
		}{text: "Inspection failed: " + overviewDisplay(inspection.err), role: RoleDetailError})
	}
	var lines []string
	seen := make(map[string]struct{})
	for _, item := range items {
		item.text = singleLineDetail(item.text)
		if item.text == "" {
			continue
		}
		if _, duplicate := seen[item.text]; duplicate {
			continue
		}
		seen[item.text] = struct{}{}
		appendOverviewBullet(&lines, item.text, width, profile, item.role, "•")
	}
	return lines
}

func boundedOverviewValues(values []string, limit int) []string {
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		if value = cells.Truncate(singleLineDetail(value), 240); value != "" {
			result = append(result, value)
			if len(result) == limit {
				break
			}
		}
	}
	return result
}

func detailScope(detail *plan.PlanDetail) []string {
	seen := make(map[string]struct{})
	var scope []string
	for _, slice := range orderedDetailSlices(detail) {
		for _, file := range slice.ExpectedFiles {
			file = cells.Truncate(singleLineDetail(file), 240)
			if file == "" {
				continue
			}
			if _, exists := seen[file]; exists {
				continue
			}
			seen[file] = struct{}{}
			scope = append(scope, file)
		}
	}
	return scope
}

func wrapDetailLines(lines []string, width int) []string {
	available := width - 4
	if width <= 0 {
		available = 0
	}
	var result []string
	for _, line := range lines {
		result = append(result, wrapDetailWords(singleLineDetail(line), available)...)
	}
	return result
}

func renderActivityPane(log, followError string, width, height, offset int) []string {
	lines := RenderLogPane(log, 0, int(^uint(0)>>1))
	if len(lines) == 0 {
		lines = []string{"No agent log output."}
	}
	if followError != "" {
		lines = append(lines, "log follow stopped: "+singleLineDetail(followError))
	}
	return fitDetailPaneAt(lines, width, height, offset)
}

func fitDetailPaneAt(lines []string, width, height, offset int) []string {
	if height <= 0 || len(lines) == 0 {
		return nil
	}
	offset = max(0, min(offset, max(len(lines)-height, 0)))
	end := min(offset+height, len(lines))
	return fitDetailPane(lines[offset:end], width, height, 0)
}

func wrapDetailField(label, value string, width int) []string {
	value = singleLineDetail(value)
	if value == "" {
		return nil
	}
	prefix := label + ": "
	available := width - cells.Width(prefix)
	if width <= 0 {
		available = 0
	}
	wrapped := wrapDetailWords(value, available)
	indent := strings.Repeat(" ", cells.Width(prefix))
	for index := range wrapped {
		if index == 0 {
			wrapped[index] = prefix + wrapped[index]
		} else {
			wrapped[index] = indent + wrapped[index]
		}
	}
	return wrapped
}

func wrapDetailWords(value string, available int) []string {
	if available <= 0 {
		return []string{value}
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(value) {
		if cells.Width(word) > available {
			if current != "" {
				lines = append(lines, current)
			}
			for cells.Width(word) > available {
				prefix := cells.Truncate(word, available)
				if prefix == "" {
					_, size := utf8.DecodeRuneInString(word)
					word = word[size:]
					continue
				}
				lines = append(lines, prefix)
				word = strings.TrimPrefix(word, prefix)
			}
			current = word
			continue
		}
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if cells.Width(candidate) <= available {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func renderLogBox(log, followError string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	bodyHeight := max(height-2, 0)
	content := RenderLogPane(log, 0, bodyHeight)
	if len(content) == 0 && bodyHeight > 0 {
		content = []string{"No agent log output."}
	}
	if followError != "" && bodyHeight > 0 {
		content = append(content, "log follow stopped: "+singleLineDetail(followError))
		if len(content) > bodyHeight {
			content = content[len(content)-bodyHeight:]
		}
	}
	return renderPaneBox("LOG", content, width, height)
}

func renderPaneBox(title string, content []string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if width > 0 && width < 4 {
		return []string{cells.Truncate(title, width)}
	}
	boxWidth := width
	if boxWidth <= 0 {
		boxWidth = cells.Width(title) + 4
		for _, line := range content {
			boxWidth = max(boxWidth, cells.Width(line)+4)
		}
		boxWidth = max(boxWidth, 40)
	}
	if height == 1 {
		return []string{paneBoxTop(title, boxWidth)}
	}
	bodyHeight := max(height-2, 0)
	innerWidth := max(boxWidth-4, 0)
	lines := []string{paneBoxTop(title, boxWidth)}
	for len(content) < bodyHeight {
		content = append(content, "")
	}
	if len(content) > bodyHeight {
		content = content[:bodyHeight]
	}
	for _, line := range content {
		line = cells.Truncate(line, innerWidth)
		visible := cells.Width(line)
		lines = append(lines, "│ "+line+strings.Repeat(" ", max(innerWidth-visible, 0))+" │")
	}
	lines = append(lines, "└"+strings.Repeat("─", boxWidth-2)+"┘")
	return lines
}

func paneBoxTop(title string, width int) string {
	label := " " + title + " "
	inside := width - 2
	if inside <= 0 {
		return cells.Truncate(title, width)
	}
	if cells.Width(label) > inside {
		return "┌" + strings.Repeat("─", inside) + "┐"
	}
	return "┌" + label + strings.Repeat("─", inside-cells.Width(label)) + "┐"
}

func detailHeaderValues(model DetailModel) (id, title, repoName, status string) {
	id = model.Row.PlanID
	title = model.Row.PlanTitle
	repoName = model.Row.RepositoryName
	status = model.Row.Status
	if model.Plan != nil {
		if model.Plan.State.Plan.ID != "" {
			id = model.Plan.State.Plan.ID
		}
		if model.Plan.State.Plan.Title != "" {
			title = model.Plan.State.Plan.Title
		}
		if model.Plan.State.Repo.Name != "" {
			repoName = model.Plan.State.Repo.Name
		}
		if model.Plan.State.Status != "" {
			status = model.Plan.State.Status
		}
	}
	return displayValue(id), displayValue(title), displayValue(repoName), displayValue(status)
}

// RenderSlicesPane renders queue-authoritative slice order. The slices.json
// array is only an ID lookup; completed_slices followed by pending_slices owns
// presentation order.
func RenderSlicesPane(detail *plan.PlanDetail, width, height int, useColor bool) []string {
	return renderSlicesPane(detail, "", width, height, useColor)
}

func renderSlicesPane(detail *plan.PlanDetail, selectedID string, width, height int, useColor bool) []string {
	if detail == nil {
		return nil
	}
	ordered := orderedDetailSlices(detail)
	if len(ordered) == 0 {
		return fitDetailPane([]string{"  No slices."}, width, height, 0)
	}

	statusWidth := 0
	idWidth := 0
	for _, slice := range ordered {
		statusWidth = max(statusWidth, cells.Width(displayValue(slice.Status)))
		idWidth = max(idWidth, cells.Width(displayValue(slice.ID)))
	}
	if selectedID == "" && detail.State.Plan.CurrentSlice != nil {
		selectedID = *detail.State.Plan.CurrentSlice
	}
	if selectedID == "" && len(ordered) > 0 {
		selectedID = ordered[0].ID
	}
	lines := make([]string, 0, len(ordered))
	selectedLine := 0
	for _, slice := range ordered {
		cursor := "  "
		if slice.ID == selectedID {
			cursor = "> "
			selectedLine = len(lines)
		}
		status := cells.Pad(displayValue(slice.Status), statusWidth)
		status = colorStatus(profileForEnabledColor(useColor), status, slice.Status)
		id := cells.Pad(displayValue(slice.ID), idWidth)
		line := cursor + status + "  " + id + "  " + displayValue(slice.Title)
		if marker := approvalMarker(slice.Approval); marker != "" {
			line += "  " + marker
		}
		lines = append(lines, line)
		if note := strings.TrimSpace(slice.BlockerNote); note != "" {
			titleColumn := 2 + statusWidth + 2 + idWidth + 2
			lines = append(lines, strings.Repeat(" ", titleColumn)+"blocker: "+note)
		}
	}
	return fitDetailPane(lines, width, height, selectedLine)
}

// RenderSliceDetail renders the selected slice as a bounded read-only frame.
func RenderSliceDetail(model DetailModel) string {
	selected, ok := findDetailSlice(model.Plan, model.SelectedSliceID)
	id, status, approval := "-", "-", "-"
	var details []string
	var summary []string
	if ok {
		id = displayValue(singleLineDetail(selected.ID))
		status = displayValue(singleLineDetail(selected.Status))
		approval = sliceApprovalStatus(selected.Approval)
		summary = append(summary, wrapDetailField("Title", selected.Title, model.Width)...)
		summary = append(summary, wrapDetailField("Goal", selected.Goal, model.Width)...)
		summary = append(summary, wrapDetailField("Context", selected.Context, model.Width)...)
		details = sliceDetailLines(selected)
	}
	if len(details) == 0 {
		details = []string{"No additional details."}
	}
	details = wrapDetailLines(details, model.Width)

	filteredLog := model.SliceLog
	if filteredLog == "" {
		filteredLog = filterSliceLog(model.Log, id)
	}
	allLogLines := RenderLogPane(filteredLog, 0, int(^uint(0)>>1))
	desiredLogHeight := max(len(allLogLines)+2, 3)

	desiredDetailHeight := max(len(details)+2, 3)
	detailHeight := desiredDetailHeight
	logHeight := desiredLogHeight
	if model.Height > 0 {
		fixedHeight := 1 + planDetailHeaderGap + len(summary) + planDetailPaneGap
		available := max(model.Height-fixedHeight, 0)
		detailHeight = min(desiredDetailHeight, available)
		logHeight = 0
		if available >= 2+planDetailPaneGap+3 {
			detailHeight = min(desiredDetailHeight, available-planDetailPaneGap-3)
			logHeight = available - detailHeight - planDetailPaneGap
		}
	}

	lines := []string{"Tao UI | " + id + " | " + status + " | approval: " + approval}
	if len(summary) > 0 {
		lines = append(lines, make([]string, planDetailHeaderGap)...)
		lines = append(lines, summary...)
	}
	if detailHeight > 0 {
		lines = append(lines, make([]string, planDetailPaneGap)...)
		lines = append(lines, renderPaneBox("DETAIL", fitDetailPaneAt(details, model.Width, max(detailHeight-2, 0), model.SliceOffset), model.Width, detailHeight)...)
	}
	if logHeight > 0 {
		lines = append(lines, make([]string, planDetailPaneGap)...)
		lines = append(lines, renderLogBox(filteredLog, "", model.Width, logHeight)...)
	}
	if model.Width > 0 {
		for index := range lines {
			lines[index] = cells.Truncate(lines[index], model.Width)
		}
	}
	if model.Height > 0 && len(lines) > model.Height {
		lines = lines[:model.Height]
	}
	if model.ShowShortcuts {
		lines = overlaySliceDetailShortcuts(lines, model.Width, model.Height, model.UseColor)
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func sliceDetailLines(selected plan.Slice) []string {
	var details []string
	appendDetailList(&details, "Dependencies", selected.DependsOn)
	if selected.Approval != nil {
		appendDetailValue(&details, "Approval", sliceApprovalStatus(selected.Approval))
		appendDetailValue(&details, "Approval reason", selected.Approval.Reason)
	}
	if len(selected.RequiredInputs) > 0 {
		values := make([]string, 0, len(selected.RequiredInputs))
		for _, input := range selected.RequiredInputs {
			value := input.Path
			if input.Kind != "" {
				value += " (" + input.Kind + ")"
			}
			if input.Reason != "" {
				value += " — " + input.Reason
			}
			values = append(values, value)
		}
		appendDetailList(&details, "Required inputs", values)
	}
	appendDetailList(&details, "Tasks", selected.Tasks)
	appendDetailList(&details, "Expected files", selected.ExpectedFiles)
	appendDetailValue(&details, "Verification source", selected.Verification.Source)
	appendDetailList(&details, "Verification commands", selected.Verification.Commands)
	appendDetailList(&details, "Manual checks", selected.Verification.ManualChecks)
	appendDetailValue(&details, "Blocker", selected.BlockerNote)
	appendDetailValue(&details, "Notes", selected.Notes)
	if len(selected.VerificationResults) > 0 {
		values := make([]string, 0, len(selected.VerificationResults))
		for _, result := range selected.VerificationResults {
			value := result.Command
			if result.Result != "" {
				value += ": " + result.Result
			}
			if result.Details != "" {
				value += " — " + result.Details
			}
			values = append(values, value)
		}
		appendDetailList(&details, "Verification results", values)
	}
	if selected.Completion != nil {
		value := selected.Completion.Outcome
		if selected.Completion.CommitSHA != "" {
			value += " (" + selected.Completion.CommitSHA + ")"
		}
		appendDetailValue(&details, "Commit outcome", value)
	}
	return details
}

func sliceApprovalStatus(approval *plan.Approval) string {
	if approval == nil || !approval.Required {
		return "not required"
	}
	if approval.Approved {
		return "approved"
	}
	return "required"
}

func sliceDetailMaxOffset(detail *plan.PlanDetail, selectedID string, width, height int) int {
	if height <= 0 {
		return 0
	}
	selected, ok := findDetailSlice(detail, selectedID)
	if !ok {
		return 0
	}
	var summary []string
	summary = append(summary, wrapDetailField("Title", selected.Title, width)...)
	summary = append(summary, wrapDetailField("Goal", selected.Goal, width)...)
	summary = append(summary, wrapDetailField("Context", selected.Context, width)...)
	details := sliceDetailLines(selected)
	if len(details) == 0 {
		details = []string{"No additional details."}
	}
	details = wrapDetailLines(details, width)
	available := max(height-(1+planDetailHeaderGap+len(summary)+planDetailPaneGap), 0)
	detailHeight := min(max(len(details)+2, 3), available)
	if available >= 2+planDetailPaneGap+3 {
		detailHeight = min(max(len(details)+2, 3), available-planDetailPaneGap-3)
	}
	return max(len(details)-max(detailHeight-2, 0), 0)
}

func findDetailSlice(detail *plan.PlanDetail, id string) (plan.Slice, bool) {
	if detail == nil {
		return plan.Slice{}, false
	}
	for _, slice := range orderedDetailSlices(detail) {
		if slice.ID == id {
			return slice, true
		}
	}
	return plan.Slice{}, false
}

func appendDetailValue(lines *[]string, label, value string) {
	if value = singleLineDetail(value); value != "" {
		*lines = append(*lines, label+": "+value)
	}
}

func appendDetailList(lines *[]string, label string, values []string) {
	added := false
	for _, value := range values {
		if value = singleLineDetail(value); value != "" {
			if !added {
				*lines = append(*lines, label+":")
				added = true
			}
			*lines = append(*lines, "  - "+value)
		}
	}
}

func singleLineDetail(value string) string {
	var printable strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) {
			switch value[index+1] {
			case '[':
				index = skipDetailCSI(value, index+2)
				printable.WriteByte(' ')
				continue
			case ']':
				index = skipDetailOSC(value, index+2)
				printable.WriteByte(' ')
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(value[index:])
		switch r {
		case '\u009b':
			index = skipDetailCSI(value, index+size)
			printable.WriteByte(' ')
			continue
		case '\u009d':
			index = skipDetailOSC(value, index+size)
			printable.WriteByte(' ')
			continue
		}
		if unicode.IsPrint(r) {
			printable.WriteRune(r)
		} else {
			printable.WriteByte(' ')
		}
		index += size
	}
	return strings.Join(strings.Fields(printable.String()), " ")
}

func skipDetailCSI(value string, index int) int {
	for index < len(value) {
		if value[index] >= '@' && value[index] <= '~' {
			return index + 1
		}
		index++
	}
	return len(value)
}

func skipDetailOSC(value string, index int) int {
	for index < len(value) {
		if value[index] == '\a' {
			return index + 1
		}
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '\\' {
			return index + 2
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == '\u009c' {
			return index + size
		}
		index += size
	}
	return len(value)
}

func fitDetailFrame(lines []string, width, height int) string {
	if width > 0 {
		for index := range lines {
			lines[index] = cells.Truncate(lines[index], width)
		}
	}
	if height > 0 && len(lines) > height {
		footer := lines[len(lines)-1]
		if height == 1 {
			lines = []string{footer}
		} else {
			lines = append(lines[:height-1], footer)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if height <= 0 || len(lines) < height {
		frame += "\n"
	}
	return frame
}

func orderedDetailSlices(detail *plan.PlanDetail) []plan.Slice {
	byID := make(map[string]plan.Slice, len(detail.Slices.Slices))
	for _, slice := range detail.Slices.Slices {
		byID[slice.ID] = slice
	}
	ids := make([]string, 0, len(detail.State.Plan.CompletedSlices)+len(detail.State.Plan.PendingSlices)+1)
	ids = append(ids, detail.State.Plan.CompletedSlices...)
	ids = append(ids, detail.State.Plan.PendingSlices...)
	if detail.State.Plan.CurrentSlice != nil {
		ids = append(ids, *detail.State.Plan.CurrentSlice)
	}
	seen := make(map[string]struct{}, len(ids))
	ordered := make([]plan.Slice, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if slice, ok := byID[id]; ok {
			ordered = append(ordered, slice)
		}
	}
	return ordered
}

func approvalMarker(approval *plan.Approval) string {
	if approval == nil || !approval.Required {
		return ""
	}
	if approval.Approved {
		return "[approval: approved]"
	}
	return "[approval required]"
}

func fitDetailPane(lines []string, width, height, focus int) []string {
	if height <= 0 || len(lines) == 0 {
		return nil
	}
	start := 0
	if len(lines) > height {
		start = focus - height/2
		start = max(start, 0)
		start = min(start, len(lines)-height)
		lines = lines[start : start+height]
	}
	result := append([]string(nil), lines...)
	if width > 0 {
		for index := range result {
			result[index] = cells.Truncate(result[index], width)
		}
	}
	return result
}

// RenderLogPane presents framed records using the tao log convention, passes
// ordinary lines through, and pins the visible window to the newest output.
func RenderLogPane(text string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	presented := presentPlanLog(text)
	lines := strings.Split(presented, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	for index := range lines {
		lines[index] = strings.ReplaceAll(lines[index], "\t", strings.Repeat(" ", detailLogTabWidth))
		if width > 0 {
			lines[index] = cells.Truncate(lines[index], width)
		}
	}
	return lines
}

func projectSliceLogs(text string, keepLines int) (map[string]string, string) {
	logs := make(map[string]string)
	active := ""
	appendSliceLogRecords(logs, &active, text, keepLines)
	return logs, active
}

func appendSliceLogRecords(logs map[string]string, active *string, text string, keepLines int) {
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		line := text
		suffix := ""
		if newline >= 0 {
			line = text[:newline]
			suffix = "\n"
			text = text[newline+1:]
		} else {
			text = ""
		}
		if record, ok := logrecord.Parse(line); ok && record.Type == logrecord.TypeSession {
			*active = runningSliceID(record.Content)
		}
		if *active == "" {
			continue
		}
		logs[*active] = tailDetailLog(logs[*active]+line+suffix, keepLines)
	}
}

func runningSliceID(action string) string {
	const prefix = "running "
	action = strings.TrimSpace(action)
	if !strings.HasPrefix(action, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(action, prefix))
}

func filterSliceLog(text, sliceID string) string {
	if strings.TrimSpace(sliceID) == "" || sliceID == "-" {
		return ""
	}
	var filtered strings.Builder
	active := false
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		line := text
		suffix := ""
		if newline >= 0 {
			line = text[:newline]
			suffix = "\n"
			text = text[newline+1:]
		} else {
			text = ""
		}
		if record, ok := logrecord.Parse(line); ok && record.Type == logrecord.TypeSession {
			active = strings.TrimSpace(record.Content) == "running "+sliceID
		}
		if active {
			filtered.WriteString(line)
			filtered.WriteString(suffix)
		}
	}
	return filtered.String()
}

func presentPlanLog(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		newline := strings.IndexByte(text, '\n')
		if newline < 0 {
			if record, ok := logrecord.Parse(text); ok {
				out.WriteString(presentLogRecord(record))
			} else {
				out.WriteString(text)
			}
			break
		}
		line := text[:newline]
		text = text[newline+1:]
		if record, ok := logrecord.Parse(line); ok {
			out.WriteString(presentLogRecord(record))
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func presentLogRecord(record logrecord.Record) string {
	var rendered strings.Builder
	if record.Type == logrecord.TypeSession {
		rendered.WriteString("--- " + singleLineDetail(record.Content) + " ---\n")
	} else {
		_ = logrecord.Render(&rendered, record)
	}
	timestamp := logTimestamp(record.Timestamp)
	if timestamp == "" {
		return rendered.String()
	}
	lines := strings.Split(strings.TrimSuffix(rendered.String(), "\n"), "\n")
	var presented strings.Builder
	for _, line := range lines {
		presented.WriteString("[" + timestamp + "] " + line + "\n")
	}
	return presented.String()
}

func logTimestamp(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return parsed.Format("15:04:05")
}

func tailDetailLog(text string, lines int) string {
	if lines <= 0 || text == "" {
		return text
	}
	trimmed := strings.TrimSuffix(text, "\n")
	parts := strings.Split(trimmed, "\n")
	if len(parts) <= lines {
		return text
	}
	return strings.Join(parts[len(parts)-lines:], "\n") + "\n"
}

type detailUpdateWriter struct {
	ctx     context.Context
	updates chan<- detailFollowUpdate
	pending []byte
}

func (w *detailUpdateWriter) Write(value []byte) (int, error) {
	w.pending = append(w.pending, value...)
	newline := bytes.LastIndexByte(w.pending, '\n')
	if newline < 0 {
		return len(value), nil
	}
	complete := string(append([]byte(nil), w.pending[:newline+1]...))
	w.pending = append([]byte(nil), w.pending[newline+1:]...)
	if err := w.send(complete); err != nil {
		return len(value), err
	}
	return len(value), nil
}

func (w *detailUpdateWriter) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	pending := string(w.pending)
	w.pending = nil
	return w.send(pending)
}

func (w *detailUpdateWriter) send(text string) error {
	select {
	case w.updates <- detailFollowUpdate{text: text}:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

// replaySkippingWriter removes the initial file replay performed by FollowLog;
// the same bytes have already seeded the page through ReadLogTail.
type replaySkippingWriter struct {
	seed    []byte
	pending []byte
	matched bool
	out     io.Writer
}

func newReplaySkippingWriter(seed string, out io.Writer) *replaySkippingWriter {
	return &replaySkippingWriter{seed: []byte(seed), matched: seed == "", out: out}
}

func (w *replaySkippingWriter) Write(value []byte) (int, error) {
	if w.matched {
		_, err := w.out.Write(value)
		return len(value), err
	}
	w.pending = append(w.pending, value...)
	if index := bytes.Index(w.pending, w.seed); index >= 0 {
		remainder := append([]byte(nil), w.pending[index+len(w.seed):]...)
		w.pending = nil
		w.seed = nil
		w.matched = true
		if len(remainder) > 0 {
			if _, err := w.out.Write(remainder); err != nil {
				return len(value), err
			}
		}
		return len(value), nil
	}
	if keep := len(w.seed) - 1; len(w.pending) > keep {
		w.pending = append([]byte(nil), w.pending[len(w.pending)-keep:]...)
	}
	return len(value), nil
}

func followDetailLog(ctx context.Context, repository DetailRepository, planDir, seed string, updates chan<- detailFollowUpdate) {
	defer close(updates)
	appends := &detailUpdateWriter{ctx: ctx, updates: updates}
	writer := newReplaySkippingWriter(seed, appends)
	for {
		err := repository.FollowLog(ctx, planDir, writer)
		if err == nil {
			_ = appends.Flush()
			return
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			select {
			case updates <- detailFollowUpdate{err: err}:
			case <-ctx.Done():
			}
			return
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}
