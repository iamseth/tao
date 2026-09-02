package tui

import (
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/term/cells"
)

type frameSummary struct {
	primary        string
	attentionCount int
	attentionNoun  string
	extra          string
}

func renderFrame(model Model, page PageID) []string {
	strip, _ := renderTabStrip(model.Profile, page)
	width := dashboardFrameWidth(model, page)
	contextWidth := width - cells.Width(strip) - 2
	context := renderGlobalContextWidth(model, contextWidth)

	line := strip
	if context != "" && width > cells.Width(strip) {
		gap := width - cells.Width(strip) - cells.Width(context)
		if gap >= 2 {
			line += strings.Repeat(" ", gap) + context
		}
	}
	line = cells.Truncate(line, width)

	return []string{line}
}

func dashboardFrameWidth(model Model, page PageID) int {
	if model.Width > 0 {
		return model.Width
	}
	strip, _ := renderTabStrip(model.Profile, page)
	context := renderGlobalContext(model)
	return cells.Width(strip) + 2 + cells.Width(context)
}

func dashboardSectionWidth(model Model, page PageID, title string, tailWidth int) int {
	if model.Width > 0 {
		return model.Width
	}
	// Leave enough room for both rule runs when rendering without a bounded
	// terminal width, rather than forcing sectionRuleTail's narrow fallback.
	minimum := cells.Width("▌ "+title+" ") + tailWidth + 5
	return max(dashboardFrameWidth(model, page), minimum)
}

func dashboardSectionRuleColumns(profile Profile, role Role, title string, columns []column, width int) string {
	return sectionRuleColumns(profile, role, title, fitDashboardSectionColumns(title, columns, width), width)
}

func fitDashboardSectionColumns(title string, columns []column, width int) []column {
	// Reserve the title, both spaces around the tail, the final rule cell, and
	// at least one middle-rule cell. Narrow frames keep as many leading headers
	// as can still render as an inline rule instead of degrading to bare text.
	available := width - cells.Width("▌ "+title+" ") - 4
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
		nameWidth := cells.Width(item.name)
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

func renderTabStrip(profile Profile, page PageID) (string, int) {
	page = normalizePage(page)
	var strip strings.Builder
	strip.WriteString(Paint(profile, RoleNeutral5, "tao"))
	strip.WriteString(" ")
	strip.WriteString(Paint(profile, RoleNeutral1, "│"))
	activeEnd := cells.Width(strip.String())
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
			activeEnd = cells.Width(strip.String())
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
		repositoryWidth := maxWidth - cells.Width(suffix) - cells.Width("●")
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
	return cells.TruncateEllipsis(repository, width)
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
