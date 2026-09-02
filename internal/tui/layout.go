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
	name     string
	width    int
	flex     bool
	required bool
	priority int
	minimum  int
}

// fitColumns keeps required columns and removes optional columns from lowest
// to highest priority until their preferred widths fit. If only required
// columns remain, values shrink to their declared semantic minima while the
// original column order is preserved.
func fitColumns(columns []column, paneWidth int) []column {
	if len(columns) == 0 || paneWidth <= 0 {
		return append([]column(nil), columns...)
	}
	fitted := append([]column(nil), columns...)
	for index := range fitted {
		fitted[index].width = max(fitted[index].width, columnMinimum(fitted[index]))
	}
	for preferredColumnsWidth(fitted) > paneWidth {
		drop := -1
		for index, item := range fitted {
			if item.required || (drop >= 0 && item.priority >= fitted[drop].priority) {
				continue
			}
			drop = index
		}
		if drop < 0 {
			break
		}
		fitted = append(fitted[:drop], fitted[drop+1:]...)
	}

	overflow := preferredColumnsWidth(fitted) - paneWidth
	for overflow > 0 {
		shrink := -1
		for index, item := range fitted {
			if item.flex || item.width <= columnMinimum(item) {
				continue
			}
			if shrink < 0 || item.priority < fitted[shrink].priority {
				shrink = index
			}
		}
		if shrink < 0 {
			break
		}
		amount := min(overflow, fitted[shrink].width-columnMinimum(fitted[shrink]))
		fitted[shrink].width -= amount
		overflow -= amount
	}
	return fitted
}

func preferredColumnsWidth(columns []column) int {
	width := columnGapWidth * max(len(columns)-1, 0)
	for _, item := range columns {
		if item.flex {
			width += columnMinimum(item)
		} else {
			width += max(item.width, columnMinimum(item))
		}
	}
	return width
}

func columnMinimum(item column) int {
	minimum := item.minimum
	if minimum <= 0 {
		minimum = visibleWidth(item.name)
	}
	if item.flex {
		minimum = max(minimum, minimumFlexWidth)
	}
	return minimum
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
	bottom := "╰" + strings.Repeat("─", innerWidth) + "╯"
	pane := make([]string, 0, len(lines)+2)
	pane = append(pane, paintPaneTopBorder(profile, borderRole, innerWidth, title, identity))
	for _, line := range lines {
		body := padCells(truncateCells(line, innerWidth), innerWidth)
		pane = append(pane, Paint(profile, borderRole, "│")+body+Paint(profile, borderRole, "│"))
	}
	pane = append(pane, Paint(profile, borderRole, bottom))
	return pane
}

type paneTopBorderParts struct {
	left   string
	middle string
	right  string
}

func paintPaneTopBorder(profile Profile, borderRole Role, width int, title, identity string) string {
	parts := paneTopBorderLayout(width, title, identity)
	if parts.right == "" {
		return Paint(profile, borderRole, "╭"+parts.left+parts.middle+"╮")
	}

	rightWidth := visibleWidth(parts.right)
	identityWidth := min(visibleWidth(identity), max(rightWidth-1, 0))
	renderedIdentity := truncateCells(identity, identityWidth)
	suffixWidth := max(rightWidth-1-visibleWidth(renderedIdentity), 0)
	suffix := truncateCells(" ─", suffixWidth)
	return Paint(profile, borderRole, "╭"+parts.left+parts.middle+" ") +
		Paint(profile, RoleNeutral3, renderedIdentity) +
		Paint(profile, borderRole, suffix+"╮")
}

func paneTopBorderLayout(width int, title, identity string) paneTopBorderParts {
	if width <= 0 {
		return paneTopBorderParts{}
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
			return paneTopBorderParts{left: padCells(truncateCells(left, width), width)}
		}
		rightWidth := min(visibleWidth(right), max(width/2, 1))
		right = truncateCells(right, rightWidth)
		left = truncateCells(left, width-visibleWidth(right))
	}
	middle := strings.Repeat("─", max(width-visibleWidth(left)-visibleWidth(right), 0))
	return paneTopBorderParts{left: left, middle: middle, right: right}
}
