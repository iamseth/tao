package tui

import (
	"fmt"
	"strings"
)

const (
	columnGapWidth   = 2
	minimumFlexWidth = 1
)

type column struct {
	name  string
	width int
	flex  bool
}

// joinRow pads cells to their corresponding column widths and separates them
// with the frame's fixed gap. A flex column consumes the space left in the
// pane after fixed columns and gaps.
func joinRow(columns []column, cells []string, paneWidth int) string {
	resolved := resolveColumns(columns, paneWidth)
	parts := make([]string, 0, len(resolved))
	for index, item := range resolved {
		value := ""
		if index < len(cells) {
			value = cells[index]
		}
		parts = append(parts, padCells(truncateCells(value, item.width), item.width))
	}
	row := strings.Join(parts, strings.Repeat(" ", columnGapWidth))
	if paneWidth > 0 {
		return padCells(truncateCells(row, paneWidth), paneWidth)
	}
	return row
}

func resolveColumns(columns []column, paneWidth int) []column {
	resolved := append([]column(nil), columns...)
	fixedWidth := columnGapWidth * max(len(resolved)-1, 0)
	flexIndexes := make([]int, 0, len(resolved))
	preferredFlexWidth := 0
	for index := range resolved {
		if resolved[index].flex {
			resolved[index].width = max(resolved[index].width, minimumFlexWidth)
			preferredFlexWidth += resolved[index].width
			flexIndexes = append(flexIndexes, index)
			continue
		}
		resolved[index].width = max(resolved[index].width, 0)
		fixedWidth += resolved[index].width
	}
	if len(flexIndexes) == 0 {
		return resolved
	}

	available := max(paneWidth-fixedWidth, minimumFlexWidth*len(flexIndexes))
	if available >= preferredFlexWidth {
		extra := available - preferredFlexWidth
		for offset, index := range flexIndexes {
			resolved[index].width += extra / len(flexIndexes)
			if offset < extra%len(flexIndexes) {
				resolved[index].width++
			}
		}
		return resolved
	}

	remaining := available
	for offset, index := range flexIndexes {
		minimumForLater := minimumFlexWidth * (len(flexIndexes) - offset - 1)
		resolved[index].width = min(resolved[index].width, max(remaining-minimumForLater, minimumFlexWidth))
		remaining -= resolved[index].width
	}
	return resolved
}

// sectionRule renders a section title and count into a full-width rule.
func sectionRule(profile Profile, role Role, title string, count, width int) string {
	return sectionRuleTail(profile, role, title, fmt.Sprintf("%d", count), width)
}

// sectionRuleColumns replaces the section count with aligned column headers.
func sectionRuleColumns(profile Profile, role Role, title string, columns []column, width int) string {
	headers := make([]string, len(columns))
	for index, item := range columns {
		headers[index] = item.name
	}
	return sectionRuleTail(profile, role, title, joinRow(columns, headers, columnsWidth(columns)), width)
}

func sectionRuleTail(profile Profile, role Role, title, tail string, width int) string {
	if width <= 0 {
		return ""
	}
	lead := "▌ " + title + " "
	trailing := " " + tail + " "
	fixedWidth := visibleWidth(lead) + visibleWidth(trailing) + 1
	if fixedWidth > width {
		return truncateCells(Paint(profile, role, lead+tail), width)
	}
	middle := strings.Repeat("─", width-fixedWidth)
	return Paint(profile, role, lead) +
		Paint(profile, RoleNeutral0, middle) +
		Paint(profile, role, trailing) +
		Paint(profile, RoleNeutral0, "─")
}

func columnsWidth(columns []column) int {
	width := columnGapWidth * max(len(columns)-1, 0)
	for _, item := range columns {
		width += max(item.width, 0)
	}
	return width
}

func moreIndicator(profile Profile, count int) string {
	return Paint(profile, RoleNeutral2, fmt.Sprintf("+ %d more  ↓", max(count, 0)))
}

// borderedPane wraps lines in a fixed-width border. The selected border uses
// the accent role; an unselected list uses a subdued neutral border.
func borderedPane(profile Profile, width int, title, identity string, hasSelection bool, lines []string) []string {
	if width <= 0 {
		return nil
	}
	borderRole := RoleNeutral1
	if hasSelection {
		borderRole = RoleAccent
	}
	if width == 1 {
		return []string{Paint(profile, borderRole, "│")}
	}

	innerWidth := width - 2
	top := paneTopBorder(innerWidth, title, identity)
	bottom := "╰" + strings.Repeat("─", innerWidth) + "╯"
	pane := make([]string, 0, len(lines)+2)
	pane = append(pane, Paint(profile, borderRole, "╭"+top+"╮"))
	for _, line := range lines {
		body := padCells(truncateCells(line, innerWidth), innerWidth)
		pane = append(pane, Paint(profile, borderRole, "│")+body+Paint(profile, borderRole, "│"))
	}
	pane = append(pane, Paint(profile, borderRole, bottom))
	return pane
}

func paneTopBorder(width int, title, identity string) string {
	if width <= 0 {
		return ""
	}
	left := "─"
	if strings.TrimSpace(title) != "" {
		left = "─ " + title + " "
	}
	right := ""
	if strings.TrimSpace(identity) != "" {
		right = " " + identity + " ─"
	}
	if visibleWidth(left)+visibleWidth(right) > width {
		if right == "" {
			return padCells(truncateCells(left, width), width)
		}
		rightWidth := min(visibleWidth(right), max(width/2, 1))
		right = truncateCells(right, rightWidth)
		left = truncateCells(left, width-visibleWidth(right))
	}
	return left + strings.Repeat("─", max(width-visibleWidth(left)-visibleWidth(right), 0)) + right
}
