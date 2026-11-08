package tui

import (
	"strings"

	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

const sliceDetailFooter = "←/→ prev/next  ↑/↓ scroll  ? help  Esc back"

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
