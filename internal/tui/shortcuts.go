package tui

import (
	"strings"
	"unicode/utf8"
)

type shortcut struct {
	key    string
	action string
}

func shortcutsForPage(page PageID) []shortcut {
	if normalizePage(page) == PageSettings {
		return []shortcut{
			{key: "↑ / ↓ / j / k", action: "Select repository"},
			{key: "p", action: "Cycle pull-request default"},
			{key: "Tab / ← / →", action: "Switch tabs"},
			{key: "q / Esc Esc", action: "Quit"},
			{key: "? / Esc", action: "Close shortcuts"},
		}
	}
	if normalizePage(page) == PageDebug {
		return []shortcut{
			{key: "↑ / ↓ / j / k", action: "Scroll diagnostics"},
			{key: "g / G", action: "Jump to top / bottom"},
			{key: "Tab / ← / →", action: "Switch tabs"},
			{key: "q / Esc Esc", action: "Quit"},
			{key: "? / Esc", action: "Close shortcuts"},
		}
	}
	common := []shortcut{
		{key: "↑ / ↓ / j / k", action: "Move selection"},
		{key: "Tab / ← / →", action: "Switch tabs"},
		{key: "Enter", action: "Open selected item"},
		{key: "f", action: "Cycle repository filter"},
	}
	if normalizePage(page) == PagePlans {
		common = append(common,
			shortcut{key: "c", action: "Toggle completed plans"},
			shortcut{key: "r", action: "Run selected plan"},
			shortcut{key: "a", action: "Approve selected slice"},
			shortcut{key: "m", action: "Merge selected plan"},
			shortcut{key: "M", action: "Merge all eligible plans"},
		)
	}
	return append(common,
		shortcut{key: "/", action: "Search plans and notes"},
		shortcut{key: "Backspace", action: "Go back / clear search"},
		shortcut{key: "q / Esc Esc", action: "Quit"},
		shortcut{key: "? / Esc", action: "Close shortcuts"},
	)
}

func planDetailShortcuts() []shortcut {
	return []shortcut{
		{key: "↑ / ↓ / j / k", action: "Move slice selection"},
		{key: "Enter", action: "Open selected slice"},
		{key: "Backspace / Esc", action: "Return to plans"},
		{key: "q", action: "Quit"},
		{key: "?", action: "Close shortcuts"},
	}
}

func sliceDetailShortcuts() []shortcut {
	return []shortcut{
		{key: "Backspace / Esc", action: "Return to plan"},
		{key: "q", action: "Quit"},
		{key: "?", action: "Close shortcuts"},
	}
}

func overlayShortcutLegend(background []string, page PageID, width, height int, useColor bool) []string {
	return overlayShortcutTable(background, shortcutsForPage(page), width, height, useColor)
}

func overlayPlanDetailShortcuts(background []string, width, height int, useColor bool) []string {
	return overlayShortcutTable(background, planDetailShortcuts(), width, height, useColor)
}

func overlaySliceDetailShortcuts(background []string, width, height int, useColor bool) []string {
	return overlayShortcutTable(background, sliceDetailShortcuts(), width, height, useColor)
}

func overlayShortcutTable(background []string, entries []shortcut, width, height int, useColor bool) []string {
	canvasWidth := width
	if canvasWidth <= 0 {
		for _, line := range background {
			canvasWidth = max(canvasWidth, utf8.RuneCountInString(stripANSISequences(line)))
		}
		canvasWidth = max(canvasWidth, 60)
	}
	canvasHeight := height
	if canvasHeight <= 0 {
		canvasHeight = max(len(background), 20)
	}

	legend := renderShortcutLegend(entries, canvasWidth, canvasHeight, useColor)
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
		lineWidth := utf8.RuneCountInString(stripANSISequences(line))
		left := max(0, (canvasWidth-lineWidth)/2)
		background[start+index] = strings.Repeat(" ", left) + line
	}
	return background
}

func renderShortcutLegend(entries []shortcut, maxWidth, maxHeight int, useColor bool) []string {
	if maxWidth <= 0 || maxHeight <= 0 {
		return nil
	}
	if maxWidth < 12 || maxHeight < 4 {
		return []string{truncateANSI("[? shortcuts]", maxWidth)}
	}

	if capacity := maxHeight - 6; capacity < len(entries) {
		if capacity <= 0 {
			return []string{truncateANSI("[Keyboard shortcuts]", maxWidth)}
		}
		closeEntry := entries[len(entries)-1]
		entries = append(append([]shortcut(nil), entries[:max(0, capacity-1)]...), closeEntry)
	}

	keyWidth := utf8.RuneCountInString("KEY")
	actionWidth := utf8.RuneCountInString("ACTION")
	for _, entry := range entries {
		keyWidth = max(keyWidth, utf8.RuneCountInString(entry.key))
		actionWidth = max(actionWidth, utf8.RuneCountInString(entry.action))
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
	title := truncatePlain("Keyboard shortcuts", tableWidth-4)
	title = padRunes(title, tableWidth-4)
	if useColor {
		title = "\x1b[1m" + title + "\x1b[0m"
	}
	lines := []string{
		"┌" + horizontal + "┐",
		"│ " + title + " │",
		"├" + strings.Repeat("─", keyWidth+2) + "┬" + strings.Repeat("─", actionWidth+2) + "┤",
		shortcutTableRow("KEY", "ACTION", keyWidth, actionWidth, useColor),
		"├" + strings.Repeat("─", keyWidth+2) + "┼" + strings.Repeat("─", actionWidth+2) + "┤",
	}
	for _, entry := range entries {
		lines = append(lines, shortcutTableRow(entry.key, entry.action, keyWidth, actionWidth, false))
	}
	lines = append(lines, "└"+strings.Repeat("─", keyWidth+2)+"┴"+strings.Repeat("─", actionWidth+2)+"┘")
	return lines
}

func shortcutTableRow(key, action string, keyWidth, actionWidth int, bold bool) string {
	key = truncatePlain(key, keyWidth)
	action = truncatePlain(action, actionWidth)
	key = padRunes(key, keyWidth)
	action = padRunes(action, actionWidth)
	if bold {
		key = "\x1b[1m" + key + "\x1b[0m"
		action = "\x1b[1m" + action + "\x1b[0m"
	}
	return "│ " + key + " │ " + action + " │"
}

func truncatePlain(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width])
}

func stripANSISequences(value string) string {
	var result strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			for index < len(value) {
				final := value[index] >= '@' && value[index] <= '~'
				index++
				if final {
					break
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		result.WriteRune(r)
		index += size
	}
	return result.String()
}
