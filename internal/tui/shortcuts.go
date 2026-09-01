package tui

import "strings"

type shortcut struct {
	key         string
	action      string
	footerLabel string
}

func shortcutsForPage(page PageID) []shortcut {
	if normalizePage(page) == PageSettings {
		return []shortcut{
			{key: "↑ / ↓ / j / k", action: "Select repository", footerLabel: "select"},
			{key: "p", action: "Cycle pull-request default", footerLabel: "pull request"},
			{key: "Tab / ← / →", action: "Switch tabs", footerLabel: "tabs"},
			{key: "q / Esc Esc", action: "Quit", footerLabel: "quit"},
			{key: "? / Esc", action: "Close shortcuts", footerLabel: "shortcuts"},
		}
	}
	if normalizePage(page) == PageDebug {
		return []shortcut{
			{key: "↑ / ↓ / j / k", action: "Scroll diagnostics", footerLabel: "scroll"},
			{key: "g / G", action: "Jump to top / bottom"},
			{key: "Tab / ← / →", action: "Switch tabs", footerLabel: "tabs"},
			{key: "q / Esc Esc", action: "Quit", footerLabel: "quit"},
			{key: "? / Esc", action: "Close shortcuts", footerLabel: "shortcuts"},
		}
	}
	common := []shortcut{
		{key: "↑ / ↓ / j / k", action: "Move selection", footerLabel: "select"},
		{key: "Tab / ← / →", action: "Switch tabs", footerLabel: "tabs"},
		{key: "Enter", action: "Open selected item", footerLabel: "open"},
		{key: "f", action: "Cycle repository filter", footerLabel: "filter"},
	}
	if normalizePage(page) == PagePlans {
		common = append(common,
			shortcut{key: "c", action: "Toggle completed plans", footerLabel: "completed"},
			shortcut{key: "r", action: "Run selected plan", footerLabel: "run"},
			shortcut{key: "a", action: "Approve selected slice"},
			shortcut{key: "m", action: "Merge selected plan", footerLabel: "merge"},
			shortcut{key: "M", action: "Merge all eligible plans"},
		)
	}
	return append(common,
		shortcut{key: "/", action: "Search plans and notes", footerLabel: "search"},
		shortcut{key: "Backspace", action: "Go back / clear search", footerLabel: "back"},
		shortcut{key: "q / Esc Esc", action: "Quit", footerLabel: "quit"},
		shortcut{key: "? / Esc", action: "Close shortcuts", footerLabel: "shortcuts"},
	)
}

func planDetailShortcuts() []shortcut {
	return []shortcut{
		{key: "Tab / ← / →", action: "Switch detail tabs"},
		{key: "↑ / ↓ / j / k", action: "Scroll or select"},
		{key: "g / G", action: "Jump to top / bottom"},
		{key: "Enter", action: "Open slice on Slices tab"},
		{key: "Backspace / Esc", action: "Return to plans"},
		{key: "q", action: "Quit"},
		{key: "?", action: "Close shortcuts"},
	}
}

func sliceDetailShortcuts() []shortcut {
	return []shortcut{
		{key: "↑ / ↓ / j / k", action: "Scroll details"},
		{key: "g / G", action: "Jump to top / bottom"},
		{key: "Backspace / Esc", action: "Return to plan"},
		{key: "q", action: "Quit"},
		{key: "?", action: "Close shortcuts"},
	}
}

func overlayShortcutLegend(background []string, page PageID, width, height int, profile Profile) []string {
	return overlayShortcutTable(background, shortcutsForPage(page), width, height, profile)
}

func overlayPlanDetailShortcuts(background []string, width, height int, useColor bool) []string {
	return overlayShortcutTable(background, planDetailShortcuts(), width, height, profileForEnabledColor(useColor))
}

func overlaySliceDetailShortcuts(background []string, width, height int, useColor bool) []string {
	return overlayShortcutTable(background, sliceDetailShortcuts(), width, height, profileForEnabledColor(useColor))
}

func profileForEnabledColor(enabled bool) Profile {
	if enabled {
		return ProfileANSI16
	}
	return ProfileNone
}

func overlayShortcutTable(background []string, entries []shortcut, width, height int, profile Profile) []string {
	canvasWidth := width
	if canvasWidth <= 0 {
		for _, line := range background {
			canvasWidth = max(canvasWidth, visibleWidth(line))
		}
		canvasWidth = max(canvasWidth, 60)
	}
	canvasHeight := height
	if canvasHeight <= 0 {
		canvasHeight = max(len(background), 20)
	}

	legend := renderShortcutLegend(entries, canvasWidth, canvasHeight, profile)
	if len(legend) == 0 || canvasHeight <= 0 {
		return background
	}
	if len(background) > canvasHeight {
		background = background[:canvasHeight]
	}
	for len(background) < canvasHeight {
		background = append(background, " ")
	}
	start := max(0, (canvasHeight-len(legend))/2)
	for index, line := range legend {
		if start+index >= len(background) {
			break
		}
		lineWidth := visibleWidth(line)
		left := max(0, (canvasWidth-lineWidth)/2)
		background[start+index] = strings.Repeat(" ", left) + line
	}
	return background
}

func renderShortcutLegend(entries []shortcut, maxWidth, maxHeight int, profile Profile) []string {
	if maxWidth <= 0 || maxHeight <= 0 {
		return nil
	}
	if maxWidth < 12 || maxHeight < 4 {
		return []string{truncateCells("[? shortcuts]", maxWidth)}
	}

	if capacity := maxHeight - 6; capacity < len(entries) {
		if capacity <= 0 {
			return []string{truncateCells("[Keyboard shortcuts]", maxWidth)}
		}
		closeEntry := entries[len(entries)-1]
		entries = append(append([]shortcut(nil), entries[:max(0, capacity-1)]...), closeEntry)
	}

	keyWidth := visibleWidth("KEY")
	actionWidth := visibleWidth("ACTION")
	for _, entry := range entries {
		keyWidth = max(keyWidth, visibleWidth(entry.key))
		actionWidth = max(actionWidth, visibleWidth(entry.action))
	}
	const tableOverhead = 7
	if keyWidth+actionWidth+tableOverhead > maxWidth {
		actionWidth = max(1, maxWidth-keyWidth-tableOverhead)
	}
	if keyWidth+actionWidth+tableOverhead > maxWidth {
		keyWidth = max(1, maxWidth-actionWidth-tableOverhead)
	}
	tableWidth := keyWidth + actionWidth + tableOverhead

	horizontal := strings.Repeat("─", tableWidth-2)
	title := truncateCells("Keyboard shortcuts", tableWidth-4)
	title = Paint(profile, RoleNeutral5, padCells(title, tableWidth-4))
	lines := []string{
		"┌" + horizontal + "┐",
		"│ " + title + " │",
		"├" + strings.Repeat("─", keyWidth+2) + "┬" + strings.Repeat("─", actionWidth+2) + "┤",
		shortcutTableRow("KEY", "ACTION", keyWidth, actionWidth, profile),
		"├" + strings.Repeat("─", keyWidth+2) + "┼" + strings.Repeat("─", actionWidth+2) + "┤",
	}
	for _, entry := range entries {
		lines = append(lines, shortcutTableRow(entry.key, entry.action, keyWidth, actionWidth, ProfileNone))
	}
	lines = append(lines, "└"+strings.Repeat("─", keyWidth+2)+"┴"+strings.Repeat("─", actionWidth+2)+"┘")
	return lines
}

func shortcutTableRow(key, action string, keyWidth, actionWidth int, profile Profile) string {
	key = truncateCells(key, keyWidth)
	action = truncateCells(action, actionWidth)
	key = Paint(profile, RoleNeutral5, padCells(key, keyWidth))
	action = Paint(profile, RoleNeutral5, padCells(action, actionWidth))
	return "│ " + key + " │ " + action + " │"
}
