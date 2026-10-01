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
	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
	planview "github.com/iamseth/tao/internal/view"
)

const (
	detailLogTailLines         = 200
	detailLogKeepLines         = 1000
	detailLogTabWidth          = 4
	planDetailFixedLines       = 6 // header, two gaps, tabs, and pane borders
	planOverviewFixedLines     = 4 // header, gap, tabs, and content gap
	detailOverviewScopePreview = 6
	detailOverviewMaxQuestions = 12
	noteDetailHeaderLines      = 8
	noteDetailFooter           = "←/→ prev/next ↑/↓ PgUp/PgDn p plan ^G edit Esc back"
	noteDetailFooterWide       = "←/→ prev/next ↑/↓ PgUp/PgDn p plan ^G edit c copy d/D del 0-3 tier Esc back"
	sliceDetailFooter          = "←/→ prev/next  ↑/↓ scroll  ? help  Esc back"
)

// detailTab identifies the independently navigable plan-detail views.
type detailTab int

const (
	detailTabOverview detailTab = iota
	detailTabSlices
	detailTabActivity
	detailTabChanges
	detailTabCount
)

// DetailTab exposes detail selection to deterministic preview adapters.
type DetailTab = detailTab

const (
	DetailTabOverview = detailTabOverview
	DetailTabSlices   = detailTabSlices
	DetailTabActivity = detailTabActivity
	DetailTabChanges  = detailTabChanges
)

func (t detailTab) label() string {
	return [...]string{"Overview", "Slices", "Activity", "Changes"}[max(0, min(int(detailTabCount)-1, int(t)))]
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
	Changes         DetailChangesModel
	Now             time.Time
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
	Profile         theme.Profile
	Theme           theme.Theme
	ShowShortcuts   bool
	ScopeExpanded   bool
	LoadError       string
	FollowError     string
	ActionMessage   string
	Inspection      detailInspectionView
}

// Palette preserves the legacy UseColor fallback when no profile is supplied.
func (m DetailModel) Palette() theme.Palette {
	profile := m.Profile
	if profile == theme.ProfileNone {
		profile = profileForEnabledColor(m.UseColor)
	}
	return m.Theme.Palette(profile)
}

type detailState struct {
	changesLoadState
	changes           DetailChangesModel
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

// RenderNoteDetail builds a bounded frame for one open note.
func RenderNoteDetail(item note.CatalogNote, width, height int) string {
	return renderNoteDetail(item, width, height, 0)
}

func renderNoteDetail(item note.CatalogNote, width, height, offset int) string {
	return renderNoteDetailWithMessage(item, width, height, offset, "")
}

func renderNoteDetailWithMessage(item note.CatalogNote, width, height, offset int, message string) string {
	header, body := noteDetailSections(item, width)
	footerLines := 1
	if strings.TrimSpace(message) != "" {
		footerLines++
	}
	bodyHeight := noteDetailBodyHeightWithFooter(len(body), height, footerLines)
	offset = max(0, min(offset, len(body)-bodyHeight))
	lines := append([]string(nil), header...)
	lines = append(lines, body[offset:offset+bodyHeight]...)
	if strings.TrimSpace(message) != "" {
		lines = append(lines, singleLineDetail(message))
	}
	footer := noteDetailFooter
	if width <= 0 || width >= cells.Width(noteDetailFooterWide) {
		footer = noteDetailFooterWide
	}
	lines = append(lines, footer)
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
		"Repository: " + rowlabel.DisplayValue(singleLineNoteValue(item.RepositoryName)),
		"Note: " + rowlabel.DisplayValue(singleLineNoteValue(item.ID)),
		"Status: open",
		"Tags: " + rowlabel.DisplayValue(singleLineNoteValue(strings.Join(item.Tags, ", "))),
		"Created: " + created,
		"Updated: " + updated,
		"Text:",
	}
	return header, renderNoteText(item.Text, width)
}

func noteDetailBodyHeightWithFooter(bodyLines, height, footerLines int) int {
	if height <= 0 {
		return bodyLines
	}
	return max(0, min(bodyLines, height-noteDetailHeaderLines-footerLines))
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
	phase := rowlabel.DisplayValue(strings.TrimSpace(string(model.Row.Phase)))
	heartbeat := "-"
	if model.Row.Liveness == monitor.LivenessLive || model.Row.Liveness == monitor.LivenessStale {
		heartbeat = rowlabel.DurationLabel(model.Row.HeartbeatAge) + " ago"
	}
	palette := model.Palette()
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
	if palette.Enabled() {
		header = palette.Paint(theme.RoleDetailSecondary, header)
	}
	tabs := renderDetailTabs(tab, palette)
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
	case tab == detailTabChanges:
		content = renderChangesPane(model.Changes, model.Width, bodyHeight, palette, model.Now)
	case model.LoadError != "":
		content = []string{"unable to load plan: " + singleLineDetail(model.LoadError)}
	case model.Plan == nil:
		content = []string{"Plan details unavailable."}
	default:
		switch tab {
		case detailTabSlices:
			content = renderSlicesPane(model.Plan, model.SelectedSliceID, model.Width, bodyHeight, model.Theme.Palette(profileForEnabledColor(model.UseColor)))
		case detailTabActivity:
			content = renderActivityPane(model.Log, model.FollowError, model.Width, bodyHeight, model.ActivityOffset)
		default:
			content = renderOverviewPane(model.Plan, model.Row, model.Width, bodyHeight, model.OverviewOffset, model.Inspection, palette, model.ScopeExpanded)
		}
	}

	// Reuse the header spacer so feedback never changes scroll geometry.
	feedback := cells.Truncate(singleLineDetail(model.ActionMessage), 240)
	lines := []string{header, feedback, tabs}
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
		lines = overlayPlanDetailShortcuts(lines, model.Width, model.Height, model.Theme.Palette(profileForEnabledColor(model.UseColor)), tab)
	}
	if palette.Enabled() {
		for index, line := range lines {
			if model.Width > 0 {
				line = cells.Pad(line, model.Width)
			}
			if line == "" {
				line = " "
			}
			lines[index] = palette.FillRow(theme.RoleDetailBackground, line)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func renderDetailTabs(active detailTab, palette theme.Palette) string {
	parts := make([]string, 0, detailTabCount)
	for tab := detailTabOverview; tab < detailTabCount; tab++ {
		label := tab.label()
		role := theme.RoleDetailMuted
		if tab == active {
			label = "[" + label + "]"
			role = theme.RoleDetailInfo
		}
		parts = append(parts, palette.Paint(role, label))
	}
	return strings.Join(parts, "  ")
}

func renderOverviewPane(detail *plan.PlanDetail, row monitor.Row, width, height, offset int, inspection detailInspectionView, palette theme.Palette, scopeExpanded bool) []string {
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
	appendOverviewTitle(&lines, detail.State.Plan.Title, width, palette)
	lines = append(lines, renderDetailMetadata([]detailGridField{
		{label: "TYPE", value: overviewDisplay(string(detail.State.Plan.ChangeType)), role: theme.RoleDetailInfo},
		{label: "STATUS", value: overviewDisplay(status), role: detailStateRole(status)},
		{label: "READINESS", value: overviewDisplay(string(overview.Readiness)), role: detailStateRole(string(overview.Readiness))},
		{label: "PRIORITY", value: priorityLevel, role: detailPriorityRole(priorityLevel)},
	}, width, palette)...)

	attention := detailAttentionLines(detail, row, inspection, width, palette)
	if len(attention) > 0 {
		lines = append(lines, palette.Paint(theme.RoleDetailWarning, "! ATTENTION"))
		lines = append(lines, attention...)
	} else if inspection.status == detailInspectionLoading || inspection.status == detailInspectionUnavailable {
		lines = append(lines, palette.Paint(theme.RoleDetailMuted, "INSPECTION  ")+palette.Paint(detailStalenessRole(inspection), detailStalenessSummary(inspection)))
	}

	if overview.Priority != nil {
		priority := overview.Priority
		lines = append(lines, "", detailSectionHeading("PRIORITY", width, palette))
		lines = append(lines, renderPriorityGrid(priority, width, palette)...)
		appendOverviewMutedParagraph(&lines, "Rationale", priority.Rationale, width, palette)
	}

	lines = append(lines, "", detailSectionHeading("CONTEXT", width, palette))
	appendOverviewLabeledText(&lines, "Problem", overview.Problem, width, palette)
	lines = append(lines, "")
	appendOverviewLabeledText(&lines, "Why now", overview.WhyNow, width, palette)
	lines = append(lines, "")
	appendOverviewLabeledText(&lines, "Expected benefit", overview.ExpectedBenefit, width, palette)
	questions := boundedOverviewValues(detail.State.OpenQuestions, detailOverviewMaxQuestions)
	if len(questions) > 0 {
		lines = append(lines, "", palette.Paint(theme.RoleDetailMuted, "Open questions"))
		for _, question := range questions {
			appendOverviewBullet(&lines, question, width, palette, theme.RoleDetailBody, "?")
		}
	}

	lines = append(lines, "", detailSectionHeading("SUCCESS CRITERIA", width, palette))
	if len(overview.SuccessCriteria) == 0 {
		appendOverviewChecklistItem(&lines, "-", width, palette, theme.RoleDetailMuted)
	} else {
		for _, criterion := range overview.SuccessCriteria {
			appendOverviewChecklistItem(&lines, criterion, width, palette, theme.RoleDetailSecondary)
		}
	}

	lines = append(lines, "", detailSectionHeading("SCOPE", width, palette))
	scope := detailScope(detail)
	visibleScope := scope
	if !scopeExpanded && len(visibleScope) > detailOverviewScopePreview {
		visibleScope = visibleScope[:detailOverviewScopePreview]
	}
	if len(visibleScope) == 0 {
		appendOverviewBullet(&lines, "-", width, palette, theme.RoleDetailMuted, "•")
	} else {
		for _, file := range visibleScope {
			appendOverviewBullet(&lines, file, width, palette, theme.RoleDetailSecondary, "•")
		}
	}
	if remaining := len(scope) - len(visibleScope); remaining > 0 {
		appendOverviewBullet(&lines, fmt.Sprintf("+%d more — press e to expand", remaining), width, palette, theme.RoleDetailInfo, "")
	} else if scopeExpanded && len(scope) > detailOverviewScopePreview {
		appendOverviewBullet(&lines, "press e to collapse", width, palette, theme.RoleDetailInfo, "↥")
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
	role  theme.Role
}

func appendOverviewTitle(lines *[]string, title string, width int, palette theme.Palette) {
	wrapped := wrapDetailWords(overviewDisplay(title), detailContentWidth(width, 0))
	for _, line := range wrapped {
		if !palette.Enabled() {
			*lines = append(*lines, line)
		} else {
			*lines = append(*lines, theme.Bold+palette.Paint(theme.RoleDetailPrimary, line))
		}
	}
}

func renderDetailMetadata(fields []detailGridField, width int, palette theme.Palette) []string {
	var lines []string
	var line strings.Builder
	lineWidth := 0
	separator := palette.Paint(theme.RoleDetailMuted, "  ·  ")
	for _, field := range fields {
		value := overviewDisplay(field.value)
		plainWidth := cells.Width(field.label) + 2 + cells.Width(value)
		styled := palette.Paint(theme.RoleDetailMuted, field.label) + "  " + palette.Paint(field.role, value)
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

func appendOverviewLabeledText(lines *[]string, label, value string, width int, palette theme.Palette) {
	*lines = append(*lines, palette.Paint(theme.RoleDetailSecondary, label))
	appendOverviewText(lines, overviewDisplay(value), width, palette, theme.RoleDetailBody, "  ")
}

func renderPriorityGrid(priority *plan.Priority, width int, palette theme.Palette) []string {
	rows := [][]detailGridField{
		{
			{label: "Impact", value: string(priority.Impact), role: detailPriorityRole(string(priority.Impact))},
			{label: "Urgency", value: string(priority.Urgency), role: detailPriorityRole(string(priority.Urgency))},
			{label: "Risk", value: string(priority.Risk), role: detailRiskRole(string(priority.Risk))},
		},
		{
			{label: "Effort", value: string(priority.Effort), role: theme.RoleDetailSecondary},
			{label: "Confidence", value: string(priority.Confidence), role: detailPriorityRole(string(priority.Confidence))},
		},
	}
	columnWidths := []int{13, 17}
	const gutter = 2
	thirdWidth := cells.Width(rows[0][2].label) + 1 + cells.Width(overviewDisplay(rows[0][2].value))
	requiredWidth := columnWidths[0] + gutter + columnWidths[1] + gutter + thirdWidth
	if width > 0 && requiredWidth > width {
		fields := append(append([]detailGridField(nil), rows[0]...), rows[1]...)
		lines := make([]string, 0, len(fields))
		for _, field := range fields {
			line := palette.Paint(theme.RoleDetailMuted, cells.Pad(field.label, 10)) + " " + palette.Paint(field.role, overviewDisplay(field.value))
			lines = append(lines, cells.TruncateEllipsis(line, width))
		}
		return lines
	}
	lines := make([]string, 0, len(rows))
	for _, fields := range rows {
		var line strings.Builder
		for column, field := range fields {
			cell := palette.Paint(theme.RoleDetailMuted, field.label) + " " + palette.Paint(field.role, overviewDisplay(field.value))
			if column < len(fields)-1 {
				cell = cells.Pad(cell, columnWidths[column]) + strings.Repeat(" ", gutter)
			}
			line.WriteString(cell)
		}
		lines = append(lines, line.String())
	}
	return lines
}

func appendOverviewMutedParagraph(lines *[]string, label, value string, width int, palette theme.Palette) {
	if value = singleLineDetail(value); value == "" {
		return
	}
	prefix := "  " + label + "  "
	wrapped := wrapDetailWords(value, detailContentWidth(width, cells.Width(prefix)))
	for index, line := range wrapped {
		if index == 0 {
			*lines = append(*lines, palette.Paint(theme.RoleDetailMuted, prefix+line))
		} else {
			*lines = append(*lines, palette.Paint(theme.RoleDetailMuted, strings.Repeat(" ", cells.Width(prefix))+line))
		}
	}
}

func appendOverviewText(lines *[]string, value string, width int, palette theme.Palette, role theme.Role, indent string) {
	for _, line := range wrapDetailWords(singleLineDetail(value), detailContentWidth(width, cells.Width(indent))) {
		*lines = append(*lines, indent+palette.Paint(role, line))
	}
}

func appendOverviewBullet(lines *[]string, value string, width int, palette theme.Palette, role theme.Role, marker string) {
	value = singleLineDetail(value)
	if value == "" {
		return
	}
	prefix := "  " + marker + " "
	wrapped := wrapDetailWords(value, detailContentWidth(width, cells.Width(prefix)))
	for index, line := range wrapped {
		if index == 0 {
			*lines = append(*lines, palette.Paint(theme.RoleDetailMuted, prefix)+palette.Paint(role, line))
		} else {
			*lines = append(*lines, strings.Repeat(" ", cells.Width(prefix))+palette.Paint(role, line))
		}
	}
}

func appendOverviewChecklistItem(lines *[]string, value string, width int, palette theme.Palette, role theme.Role) {
	value = singleLineDetail(value)
	if value == "" {
		return
	}
	prefix := "  ☐ "
	continuation := strings.Repeat(" ", cells.Width(prefix))
	wrapped := wrapDetailWords(value, detailContentWidth(width, cells.Width(prefix)))
	for index, line := range wrapped {
		if index == 0 {
			*lines = append(*lines, palette.Paint(theme.RoleDetailMuted, prefix)+palette.Paint(role, line))
		} else {
			*lines = append(*lines, continuation+palette.Paint(role, line))
		}
	}
}

func detailContentWidth(width, prefix int) int {
	if width <= 0 {
		return 0
	}
	return max(width-prefix, 1)
}

func detailSectionHeading(title string, width int, palette theme.Palette) string {
	plainTitle := strings.ToUpper(singleLineDetail(title))
	if width <= 0 {
		width = 72
	}
	dividerWidth := max(min(width-cells.Width(plainTitle)-1, 20), 1)
	return palette.Paint(theme.RoleDetailSecondary, plainTitle) + " " + palette.Paint(theme.RoleDetailDivider, strings.Repeat("─", dividerWidth))
}

func detailStateRole(value string) theme.Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ready", "completed", "approved", "current", "done", "pass", "passed", "success", "succeeded", "committed":
		return theme.RoleDetailSuccess
	case "blocked", "invalid", "fail", "failed", "failure", "error", "abandoned", "obsolete":
		return theme.RoleDetailError
	case "needs_refinement", "conditional", "deferred", "changes_requested", "stale":
		return theme.RoleDetailWarning
	default:
		return theme.RoleDetailInfo
	}
}

func detailPriorityRole(value string) theme.Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "must", "high":
		return theme.RoleDetailInfo
	default:
		return theme.RoleDetailSecondary
	}
}

func detailRiskRole(value string) theme.Role {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return theme.RoleDetailError
	case "medium":
		return theme.RoleDetailWarning
	default:
		return theme.RoleDetailSecondary
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

func detailStalenessRole(inspection detailInspectionView) theme.Role {
	switch inspection.status {
	case detailInspectionReady:
		if len(inspection.findings) == 0 {
			return theme.RoleDetailSuccess
		}
		return theme.RoleDetailWarning
	case detailInspectionFailed:
		return theme.RoleDetailError
	case detailInspectionLoading:
		return theme.RoleDetailInfo
	default:
		return theme.RoleDetailMuted
	}
}

func detailAttentionLines(detail *plan.PlanDetail, row monitor.Row, inspection detailInspectionView, width int, palette theme.Palette) []string {
	var items []struct {
		text string
		role theme.Role
	}
	for _, line := range detailAbandonmentLines(detail, row) {
		items = append(items, struct {
			text string
			role theme.Role
		}{text: line, role: theme.RoleDetailError})
	}
	for _, reason := range row.AttentionReasons {
		text := strings.ReplaceAll(string(reason), "_", " ")
		if reason == monitor.AttentionRunCrashed && detail != nil && detail.State.Plan.MergeCommitIntent != nil {
			text = "merge crashed"
		}
		items = append(items, struct {
			text string
			role theme.Role
		}{text: text, role: theme.RoleDetailWarning})
	}
	for _, warning := range append(append([]string(nil), row.Warnings...), row.RelationshipWarnings...) {
		items = append(items, struct {
			text string
			role theme.Role
		}{text: warning, role: theme.RoleDetailWarning})
	}
	switch inspection.status {
	case detailInspectionReady:
		for _, finding := range inspection.findings {
			role := theme.RoleDetailWarning
			switch {
			case strings.EqualFold(finding.Severity, "error"):
				role = theme.RoleDetailError
			case strings.EqualFold(finding.Severity, "info"):
				role = theme.RoleDetailInfo
			}
			items = append(items, struct {
				text string
				role theme.Role
			}{text: overviewDisplay(finding.Message), role: role})
		}
	case detailInspectionFailed:
		items = append(items, struct {
			text string
			role theme.Role
		}{text: "Inspection failed: " + overviewDisplay(inspection.err), role: theme.RoleDetailError})
	}
	var lines []string
	seen := make(map[string]struct{})
	for _, item := range items {
		item.text = conciseAttentionText(item.text)
		if item.text == "" {
			continue
		}
		if _, duplicate := seen[item.text]; duplicate {
			continue
		}
		seen[item.text] = struct{}{}
		appendOverviewBullet(&lines, item.text, width, palette, item.role, "•")
	}
	return lines
}

func conciseAttentionText(value string) string {
	value = singleLineDetail(value)
	lower := strings.ToLower(value)
	for _, prefix := range []string{"info:", "warning:", "error:"} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(value[len(prefix):])
		}
	}
	return value
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
	return rowlabel.DisplayValue(id), rowlabel.DisplayValue(title), rowlabel.DisplayValue(repoName), rowlabel.DisplayValue(status)
}

// RenderSlicesPane renders queue-authoritative slice order. The slices.json
// array is only an ID lookup; completed_slices followed by pending_slices owns
// presentation order.
func RenderSlicesPane(detail *plan.PlanDetail, width, height int, useColor bool) []string {
	return renderSlicesPane(detail, "", width, height, theme.Default().Palette(profileForEnabledColor(useColor)))
}

func renderSlicesPane(detail *plan.PlanDetail, selectedID string, width, height int, palette theme.Palette) []string {
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
		statusWidth = max(statusWidth, cells.Width(rowlabel.DisplayValue(slice.Status)))
		idWidth = max(idWidth, cells.Width(rowlabel.DisplayValue(slice.ID)))
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
		status := cells.Pad(rowlabel.DisplayValue(slice.Status), statusWidth)
		status = colorStatus(palette, status, slice.Status)
		id := cells.Pad(rowlabel.DisplayValue(slice.ID), idWidth)
		line := cursor + status + "  " + id + "  " + rowlabel.DisplayValue(slice.Title)
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
	palette := model.Palette()
	selected, ok := findDetailSlice(model.Plan, model.SelectedSliceID)
	id := "-"
	header := []string{palette.Paint(theme.RoleDetailMuted, "Tao UI | -")}
	body := []string{palette.Paint(theme.RoleDetailMuted, "Slice details unavailable.")}
	if ok {
		id = rowlabel.DisplayValue(singleLineDetail(selected.ID))
		header = []string{palette.Paint(theme.RoleDetailMuted, "Tao UI | "+id), ""}
		appendOverviewTitle(&header, selected.Title, model.Width, palette)
		header = append(header, renderDetailMetadata([]detailGridField{
			{label: "STATUS", value: selected.Status, role: detailStateRole(selected.Status)},
			{label: "APPROVAL", value: sliceApprovalStatus(selected.Approval), role: sliceApprovalRole(selected.Approval)},
		}, model.Width, palette)...)
		if selected.Approval != nil {
			appendOverviewMutedParagraph(&header, "Approval rationale", selected.Approval.Reason, model.Width, palette)
		}
		body = renderSlicePlan(selected, model.Width, palette)
	}

	filteredLog := model.SliceLog
	if filteredLog == "" {
		filteredLog = filterSliceLog(model.Log, id)
	}
	document := append([]string(nil), body...)
	if len(document) > 0 {
		document = append(document, "")
	}
	document = append(document, renderSliceLogSection(filteredLog, model.Width, palette)...)
	bodyHeight := len(document)
	if model.Height > 0 {
		bodyHeight = max(model.Height-len(header)-2, 0)
	}

	lines := append([]string(nil), header...)
	if bodyHeight > 0 {
		lines = append(lines, "")
		lines = append(lines, fitDetailPaneAt(document, model.Width, bodyHeight, model.SliceOffset)...)
	}
	if model.Height > 0 && len(lines) >= model.Height {
		lines = lines[:model.Height-1]
	}
	lines = append(lines, sliceDetailFooter)
	if model.Width > 0 {
		for index := range lines {
			lines[index] = cells.Truncate(lines[index], model.Width)
		}
	}
	if model.ShowShortcuts {
		lines = overlaySliceDetailShortcuts(lines, model.Width, model.Height, model.Theme.Palette(profileForEnabledColor(model.UseColor)))
	}
	if palette.Enabled() {
		for index, line := range lines {
			if model.Width > 0 {
				line = cells.Pad(line, model.Width)
			}
			if line == "" {
				line = " "
			}
			lines[index] = palette.FillRow(theme.RoleDetailBackground, line)
		}
	}
	frame := clearScreenSequence + strings.Join(lines, "\n")
	if model.Height <= 0 || len(lines) < model.Height {
		frame += "\n"
	}
	return frame
}

func renderSlicePlan(selected plan.Slice, width int, palette theme.Palette) []string {
	var lines []string
	appendSliceSection(&lines, "GOAL", width, palette)
	appendSliceParagraph(&lines, selected.Goal, width, palette, theme.RoleNeutral3)

	appendSliceSection(&lines, "CONTEXT", width, palette)
	appendSliceParagraph(&lines, selected.Context, width, palette, theme.RoleNeutral3)

	appendSliceSection(&lines, "DEPENDENCY", width, palette)
	dependencies := make([]sliceDependencyItem, 0, len(selected.DependsOn))
	for _, dependency := range selected.DependsOn {
		if dependency = singleLineDetail(dependency); dependency != "" {
			dependencies = append(dependencies, sliceDependencyItem{value: dependency})
		}
	}
	inputs := make([]sliceDependencyItem, 0, len(selected.RequiredInputs))
	for _, input := range selected.RequiredInputs {
		path := singleLineDetail(input.Path)
		if path == "" {
			continue
		}
		if kind := singleLineDetail(input.Kind); kind != "" {
			path += " (" + kind + ")"
		}
		inputs = append(inputs, sliceDependencyItem{value: path, reason: singleLineDetail(input.Reason)})
	}
	if len(dependencies) == 0 && len(inputs) == 0 {
		appendSliceEmpty(&lines, palette)
	} else {
		appendSliceDependencyGroup(&lines, "Slices", dependencies, width, palette)
		appendSliceDependencyGroup(&lines, "Inputs", inputs, width, palette)
	}

	if blocker := singleLineDetail(selected.BlockerNote); blocker != "" {
		appendSliceSection(&lines, "BLOCKER", width, palette)
		appendSliceParagraph(&lines, blocker, width, palette, theme.RoleDetailWarning)
	}

	appendSliceSection(&lines, "TASKS", width, palette)
	appendSliceTasks(&lines, selected.Tasks, width, palette)

	appendSliceSection(&lines, "EXPECTED FILES", width, palette)
	if !appendSliceValues(&lines, selected.ExpectedFiles, width, palette, theme.RoleDetailPrimary, "•") {
		appendSliceEmpty(&lines, palette)
	}

	appendSliceSection(&lines, "VERIFICATION", width, palette)
	lines = append(lines, palette.Paint(theme.RoleDetailMuted, "Source / rationale"))
	appendSliceParagraph(&lines, selected.Verification.Source, width, palette, theme.RoleDetailBody)
	lines = append(lines, "", palette.Paint(theme.RoleDetailMuted, "Commands"))
	if !appendSliceValues(&lines, selected.Verification.Commands, width, palette, theme.RoleDetailInfo, "›") {
		appendSliceEmpty(&lines, palette)
	}
	lines = append(lines, "", palette.Paint(theme.RoleDetailMuted, "Manual checks"))
	appendSliceChecklist(&lines, selected.Verification.ManualChecks, width, palette, theme.RoleDetailSecondary)
	if len(selected.VerificationResults) > 0 {
		lines = append(lines, "", palette.Paint(theme.RoleDetailMuted, "Results"))
		for _, result := range selected.VerificationResults {
			if command := singleLineDetail(result.Command); command != "" {
				appendSliceBullet(&lines, command, width, palette, theme.RoleDetailPrimary, "›")
			}
			resultText := singleLineDetail(result.Result)
			if details := singleLineDetail(result.Details); details != "" {
				if resultText != "" {
					resultText += " — "
				}
				resultText += details
			}
			if resultText != "" {
				appendSliceIndentedText(&lines, resultText, width, palette, detailStateRole(result.Result), "    ")
			}
		}
	}

	if notes := singleLineDetail(selected.Notes); notes != "" {
		appendSliceSection(&lines, "NOTES", width, palette)
		appendSliceParagraph(&lines, notes, width, palette, theme.RoleDetailBody)
	}
	if selected.Completion != nil {
		appendSliceSection(&lines, "COMPLETION", width, palette)
		appendSliceLabeledValue(&lines, "Outcome", selected.Completion.Outcome, width, palette, detailStateRole(selected.Completion.Outcome))
		appendSliceLabeledValue(&lines, "Commit", selected.Completion.CommitSHA, width, palette, theme.RoleDetailPrimary)
	}
	return lines
}

func appendSliceSection(lines *[]string, title string, width int, palette theme.Palette) {
	if len(*lines) > 0 {
		*lines = append(*lines, "")
	}
	*lines = append(*lines, detailSectionHeading(title, width, palette))
}

func appendSliceParagraph(lines *[]string, value string, width int, palette theme.Palette, role theme.Role) {
	value = singleLineDetail(value)
	if value == "" {
		appendSliceEmpty(lines, palette)
		return
	}
	appendSliceIndentedText(lines, value, width, palette, role, "  ")
}

type sliceDependencyItem struct {
	value  string
	reason string
}

func appendSliceDependencyGroup(lines *[]string, label string, items []sliceDependencyItem, width int, palette theme.Palette) {
	if len(items) == 0 {
		return
	}
	const labelWidth = 6
	compact := width <= 0 || width >= 24
	if !compact {
		*lines = append(*lines, "  "+palette.Paint(theme.RoleDetailMuted, label))
	}
	for index, item := range items {
		prefix := "    • "
		if compact {
			groupLabel := ""
			if index == 0 {
				groupLabel = label
			}
			prefix = "  " + cells.Pad(groupLabel, labelWidth) + "  • "
		}
		wrapped := wrapDetailWords(item.value, sliceDetailContentWidth(width, cells.Width(prefix)))
		for lineIndex, line := range wrapped {
			if lineIndex == 0 {
				*lines = append(*lines, palette.Paint(theme.RoleDetailMuted, prefix)+palette.Paint(theme.RoleDetailPrimary, line))
			} else {
				*lines = append(*lines, strings.Repeat(" ", cells.Width(prefix))+palette.Paint(theme.RoleDetailPrimary, line))
			}
		}
		if item.reason != "" {
			indent := strings.Repeat(" ", cells.Width(prefix))
			appendSliceIndentedText(lines, item.reason, width, palette, theme.RoleDetailBody, indent)
		}
	}
}

func appendSliceTasks(lines *[]string, values []string, width int, palette theme.Palette) {
	added := false
	for _, value := range values {
		if value = singleLineDetail(value); value != "" {
			if added {
				*lines = append(*lines, "")
			}
			appendSliceBullet(lines, value, width, palette, theme.RoleDetailSecondary, "☐")
			added = true
		}
	}
	if !added {
		appendSliceEmpty(lines, palette)
	}
}

func appendSliceChecklist(lines *[]string, values []string, width int, palette theme.Palette, role theme.Role) {
	added := false
	for _, value := range values {
		if value = singleLineDetail(value); value != "" {
			appendSliceBullet(lines, value, width, palette, role, "☐")
			added = true
		}
	}
	if !added {
		appendSliceEmpty(lines, palette)
	}
}

func appendSliceValues(lines *[]string, values []string, width int, palette theme.Palette, role theme.Role, marker string) bool {
	added := false
	for _, value := range values {
		if value = singleLineDetail(value); value != "" {
			appendSliceBullet(lines, value, width, palette, role, marker)
			added = true
		}
	}
	return added
}

func appendSliceBullet(lines *[]string, value string, width int, palette theme.Palette, role theme.Role, marker string) {
	prefix := "  " + marker + " "
	wrapped := wrapDetailWords(value, sliceDetailContentWidth(width, cells.Width(prefix)))
	for index, line := range wrapped {
		if index == 0 {
			*lines = append(*lines, palette.Paint(theme.RoleDetailMuted, prefix)+palette.Paint(role, line))
		} else {
			*lines = append(*lines, strings.Repeat(" ", cells.Width(prefix))+palette.Paint(role, line))
		}
	}
}

func appendSliceIndentedText(lines *[]string, value string, width int, palette theme.Palette, role theme.Role, indent string) {
	for _, line := range wrapDetailWords(value, sliceDetailContentWidth(width, cells.Width(indent))) {
		*lines = append(*lines, indent+palette.Paint(role, line))
	}
}

func appendSliceLabeledValue(lines *[]string, label, value string, width int, palette theme.Palette, role theme.Role) {
	value = singleLineDetail(value)
	if value == "" {
		return
	}
	prefix := "  " + label + "  "
	wrapped := wrapDetailWords(value, sliceDetailContentWidth(width, cells.Width(prefix)))
	for index, line := range wrapped {
		if index == 0 {
			*lines = append(*lines, palette.Paint(theme.RoleDetailMuted, prefix)+palette.Paint(role, line))
		} else {
			*lines = append(*lines, strings.Repeat(" ", cells.Width(prefix))+palette.Paint(role, line))
		}
	}
}

func appendSliceEmpty(lines *[]string, palette theme.Palette) {
	*lines = append(*lines, "  "+palette.Paint(theme.RoleDetailMuted, "None."))
}

func sliceDetailContentWidth(width, prefix int) int {
	const proseWidth = 88
	if width <= 0 {
		width = proseWidth
	}
	return max(min(width, proseWidth)-prefix, 1)
}

func sliceApprovalRole(approval *plan.Approval) theme.Role {
	if approval == nil || !approval.Required || approval.Approved {
		return theme.RoleDetailSuccess
	}
	return theme.RoleDetailWarning
}

func renderSliceLogSection(log string, width int, palette theme.Palette) []string {
	lines := []string{detailSectionHeading("LOG", width, palette)}
	entries := RenderLogPane(log, 0, int(^uint(0)>>1))
	if len(entries) == 0 {
		return append(lines, "  "+palette.Paint(theme.RoleDetailMuted, "No log output."))
	}
	for _, entry := range entries {
		lines = append(lines, palette.Paint(sliceLogRole(entry), entry))
	}
	return lines
}

func sliceLogRole(line string) theme.Role {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "error"), strings.Contains(lower, "failed"), strings.Contains(lower, "failure"):
		return theme.RoleDetailError
	case strings.Contains(lower, "warning"), strings.Contains(lower, "warn:"):
		return theme.RoleDetailWarning
	case strings.Contains(lower, "passed"), strings.Contains(lower, "success"), strings.Contains(lower, "completed"), strings.Contains(line, "✓"):
		return theme.RoleDetailSuccess
	default:
		return theme.RoleDetailMuted
	}
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

func sliceDetailMaxOffset(detail *plan.PlanDetail, selectedID string, width, height int, logs ...string) int {
	if height <= 0 {
		return 0
	}
	selected, ok := findDetailSlice(detail, selectedID)
	if !ok {
		return 0
	}
	header := []string{"Tao UI | " + rowlabel.DisplayValue(singleLineDetail(selected.ID)), ""}
	appendOverviewTitle(&header, selected.Title, width, theme.Palette{})
	header = append(header, renderDetailMetadata([]detailGridField{
		{label: "STATUS", value: selected.Status},
		{label: "APPROVAL", value: sliceApprovalStatus(selected.Approval)},
	}, width, theme.Palette{})...)
	if selected.Approval != nil {
		appendOverviewMutedParagraph(&header, "Approval rationale", selected.Approval.Reason, width, theme.Palette{})
	}
	document := renderSlicePlan(selected, width, theme.Palette{})
	document = append(document, "")
	log := ""
	if len(logs) > 0 {
		log = logs[0]
	}
	document = append(document, renderSliceLogSection(log, width, theme.Palette{})...)
	bodyHeight := max(height-len(header)-2, 0)
	return max(len(document)-bodyHeight, 0)
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
