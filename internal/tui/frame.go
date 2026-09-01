package tui

import (
	"fmt"
	"strings"
)

type frameSummary struct {
	primary        string
	attentionCount int
	attentionNoun  string
	extra          string
}

type footerHint struct {
	key   string
	label string
}

func renderFrame(model Model, page PageID, summary *frameSummary) []string {
	strip, activeEnd := renderTabStrip(model.Profile, page)
	width := dashboardFrameWidth(model, page)
	contextWidth := width - visibleWidth(strip) - 2
	context := renderGlobalContextWidth(model, contextWidth)

	line := strip
	if context != "" && width > visibleWidth(strip) {
		gap := width - visibleWidth(strip) - visibleWidth(context)
		if gap >= 2 {
			line += strings.Repeat(" ", gap) + context
		}
	}
	line = truncateCells(line, width)

	ruleWidth := max(width, 0)
	accentWidth := min(activeEnd, ruleWidth)
	rule := Paint(model.Profile, RoleAccent, strings.Repeat("─", accentWidth))
	if neutralWidth := ruleWidth - accentWidth; neutralWidth > 0 {
		rule += Paint(model.Profile, RoleNeutral0, strings.Repeat("─", neutralWidth))
	}

	lines := []string{line, rule}
	if summary != nil {
		lines = append(lines, renderFrameSummary(model.Profile, *summary))
	}
	return lines
}

func dashboardFrameWidth(model Model, page PageID) int {
	if model.Width > 0 {
		return model.Width
	}
	strip, _ := renderTabStrip(model.Profile, page)
	context := renderGlobalContext(model)
	return visibleWidth(strip) + 2 + visibleWidth(context)
}

func dashboardSectionWidth(model Model, page PageID, title string, tailWidth int) int {
	if model.Width > 0 {
		return model.Width
	}
	// Leave enough room for both rule runs when rendering without a bounded
	// terminal width, rather than forcing sectionRuleTail's narrow fallback.
	minimum := visibleWidth("▌ "+title+" ") + tailWidth + 5
	return max(dashboardFrameWidth(model, page), minimum)
}

func dashboardSectionRuleColumns(profile Profile, role Role, title string, columns []column, width int) string {
	return sectionRuleColumns(profile, role, title, fitDashboardSectionColumns(title, columns, width), width)
}

func fitDashboardSectionColumns(title string, columns []column, width int) []column {
	// Reserve the title, both spaces around the tail, the final rule cell, and
	// at least one middle-rule cell. Narrow frames keep as many leading headers
	// as can still render as an inline rule instead of degrading to bare text.
	available := width - visibleWidth("▌ "+title+" ") - 4
	if available <= 0 {
		return columns
	}
	fitted := make([]column, 0, len(columns))
	used := 0
	for _, item := range columns {
		gap := 0
		if len(fitted) > 0 {
			gap = columnGapWidth
		}
		remaining := available - used - gap
		nameWidth := visibleWidth(item.name)
		if remaining < nameWidth {
			break
		}
		item.width = min(max(item.width, nameWidth), remaining)
		fitted = append(fitted, item)
		used += gap + item.width
	}
	if len(fitted) == 0 {
		return columns
	}
	return fitted
}

func renderKeyHintsFooter(profile Profile, page PageID, width int) string {
	hints := footerHintsForPage(page, width)
	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		parts = append(parts,
			Paint(profile, RoleAccent, hint.key)+" "+Paint(profile, RoleNeutral2, hint.label),
		)
	}
	return strings.Join(parts, "  ")
}

func footerHintsForPage(page PageID, width int) []footerHint {
	entries := shortcutsForPage(page)
	regular := make([]footerHint, 0, len(entries))
	persistent := make([]footerHint, 0, 2)
	for _, entry := range entries {
		if entry.footerLabel == "" {
			continue
		}
		hint := footerHint{key: shortcutFooterKey(entry.key), label: entry.footerLabel}
		if hint.key == "q" || hint.key == "?" {
			persistent = appendHintIfFits(persistent, hint, width)
			continue
		}
		regular = append(regular, hint)
	}
	if width <= 0 {
		return append(regular, persistent...)
	}

	fitted := make([]footerHint, 0, len(regular)+len(persistent))
	for _, hint := range regular {
		candidate := append(append(append([]footerHint(nil), fitted...), hint), persistent...)
		if footerHintsWidth(candidate) <= width {
			fitted = append(fitted, hint)
		}
	}
	return append(fitted, persistent...)
}

func appendHintIfFits(hints []footerHint, hint footerHint, width int) []footerHint {
	candidate := append(append([]footerHint(nil), hints...), hint)
	if width > 0 && footerHintsWidth(candidate) > width {
		return hints
	}
	return candidate
}

func shortcutFooterKey(key string) string {
	key, _, _ = strings.Cut(key, " / ")
	return strings.TrimSpace(key)
}

func footerHintsWidth(hints []footerHint) int {
	width := max(len(hints)-1, 0) * 2
	for _, hint := range hints {
		width += visibleWidth(hint.key) + 1 + visibleWidth(hint.label)
	}
	return width
}

func shouldRenderKeyHintsFooter(model Model, frameLines, feedbackLines int) bool {
	if model.Height <= 0 {
		return true
	}
	return model.Height >= 8 && model.Height >= frameLines+feedbackLines+2
}

func renderTabStrip(profile Profile, page PageID) (string, int) {
	page = normalizePage(page)
	var strip strings.Builder
	strip.WriteString(Paint(profile, RoleNeutral5, "tao"))
	strip.WriteString(" ")
	strip.WriteString(Paint(profile, RoleNeutral1, "│"))
	activeEnd := visibleWidth(strip.String())
	for index, tab := range dashboardTabs {
		if index > 0 {
			strip.WriteString(" ")
		}
		role := RoleNeutral2
		if tab.ID == page {
			role = RoleAccent
			strip.WriteString(Paint(profile, RoleAccent, "▸"))
		} else {
			strip.WriteString(" ")
		}
		strip.WriteString(Paint(profile, role, tab.Label))
		if tab.ID == page {
			activeEnd = visibleWidth(strip.String())
		}
	}
	return strip.String(), activeEnd
}

func renderGlobalContext(model Model) string {
	return renderGlobalContextWidth(model, 0)
}

func renderGlobalContextWidth(model Model, maxWidth int) string {
	repository := "all repos"
	if model.FocusRepositoryID != "" {
		name := singleLineDetail(model.FocusRepositoryName)
		if name == "" {
			name = singleLineDetail(model.FocusRepositoryID)
		}
		repository = "repo " + displayValue(name)
	}
	agent := displayValue(singleLineDetail(model.DebugSnapshot.SelectedAgent))
	suffix := "  agent " + agent + "  "
	if maxWidth > 0 {
		repositoryWidth := maxWidth - visibleWidth(suffix) - visibleWidth("●")
		if repositoryWidth <= 0 {
			return ""
		}
		repository = truncateFrameRepository(repository, repositoryWidth)
	}
	healthRole := RoleSuccess
	if frameNeedsAttention(model) {
		healthRole = RoleWarn
	}
	return Paint(model.Profile, RoleNeutral2, repository+suffix) + Paint(model.Profile, healthRole, "●")
}

func truncateFrameRepository(repository string, width int) string {
	if visibleWidth(repository) <= width {
		return repository
	}
	if width <= 1 {
		return truncateCells(repository, width)
	}
	return truncateCells(repository, width-1) + "…"
}

func frameNeedsAttention(model Model) bool {
	if model.DebugSnapshot.CollectionError != "" || model.SettingsSnapshot.CollectionError != "" || len(model.DebugSnapshot.DoctorProblems) > 0 || len(model.NoteSnapshot.Warnings) > 0 {
		return true
	}
	for _, repository := range model.SettingsSnapshot.Repositories {
		health := strings.TrimSpace(repository.Health)
		if health != "" && health != "ok" {
			return true
		}
	}
	for _, row := range model.Snapshot.Rows {
		if len(row.Warnings) > 0 {
			return true
		}
	}
	return false
}

func renderFrameSummary(profile Profile, summary frameSummary) string {
	line := Paint(profile, RoleNeutral2, summary.primary)
	if summary.attentionCount > 0 {
		noun := summary.attentionNoun
		if noun == "" {
			noun = "need attention"
		} else if summary.attentionCount == 1 {
			noun = strings.TrimSuffix(noun, "s")
		}
		line += Paint(profile, RoleNeutral2, "  ·  ") + Paint(profile, RoleWarn, fmt.Sprintf("%d %s", summary.attentionCount, noun))
	}
	if summary.extra != "" {
		line += Paint(profile, RoleNeutral2, "  ·  "+summary.extra)
	}
	return line
}
