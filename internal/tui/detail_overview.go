package tui

import (
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
	planview "github.com/iamseth/tao/internal/view"
)

const (
	planOverviewFixedLines     = 4 // header, gap, tabs, and content gap
	detailOverviewScopePreview = 6
	detailOverviewMaxQuestions = 12
)

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
	if detail != nil {
		if event := plan.LatestRunAbort(detail.Events); event != nil {
			kind := event.AbortKind
			if kind == "" {
				kind = plan.RunAbortKindOther
			}
			items = append(items, struct {
				text string
				role theme.Role
			}{text: singleLineDetail("last run exited (" + kind + "): " + event.Message), role: theme.RoleDetailWarning})
		}
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
