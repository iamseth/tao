package tui

import (
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

type frameSummary struct {
	primary        string
	attentionCount int
	attentionNoun  string
	extra          string
}

func renderFrame(model Model, page PageID) []string {
	strip, _ := renderTabStrip(model.Palette(), page)
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
	strip, _ := renderTabStrip(model.Palette(), page)
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

func dashboardSectionRuleColumns(palette theme.Palette, role theme.Role, title string, columns []column, width int) string {
	return sectionRuleColumns(palette, role, title, fitDashboardSectionColumns(title, columns, width), width)
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

func renderTabStrip(palette theme.Palette, page PageID) (string, int) {
	page = normalizePage(page)
	var strip strings.Builder
	strip.WriteString(palette.Paint(theme.RoleNeutral5, "tao"))
	strip.WriteString(" ")
	strip.WriteString(palette.Paint(theme.RoleNeutral1, "│"))
	activeEnd := cells.Width(strip.String())
	for index, tab := range dashboardTabs {
		if index > 0 {
			strip.WriteString(" ")
		}
		role := theme.RoleNeutral2
		if tab.ID == page {
			role = theme.RoleAccent
			strip.WriteString(palette.Paint(theme.RoleAccent, "▸"))
		} else {
			strip.WriteString(" ")
		}
		strip.WriteString(palette.Paint(role, tab.Label))
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
	repository := filterRepositoryLabel(model)
	agent := rowlabel.DisplayValue(singleLineDetail(model.DebugSnapshot.SelectedAgent))
	suffix := "  agent " + agent + "  "
	if maxWidth > 0 {
		repositoryWidth := maxWidth - cells.Width(suffix) - cells.Width("●")
		if repositoryWidth <= 0 {
			return ""
		}
		repository = truncateFrameRepository(repository, repositoryWidth)
	}
	healthRole := theme.RoleSuccess
	if frameNeedsAttention(model) {
		healthRole = theme.RoleWarn
	}
	return model.Palette().Paint(theme.RoleNeutral2, repository+suffix) + model.Palette().Paint(healthRole, "●")
}

func filterRepositoryLabel(model Model) string {
	filter := model.Filter
	if !filter.Enabled && !filter.IsEmpty() {
		return "filter off"
	}
	if !filter.Enabled || len(filter.Repositories) == 0 {
		return "all repos"
	}
	if len(filter.Repositories) > 1 {
		return fmt.Sprintf("%d repos", len(filter.Repositories))
	}
	id := filter.Repositories[0]
	name := id
	for _, option := range DiscoverRepositories(model.Snapshot, model.NoteSnapshot, filter.Repositories) {
		if option.ID == id {
			name = option.Name
			break
		}
	}
	return "repo " + rowlabel.DisplayValue(singleLineDetail(name))
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

func renderFrameSummary(palette theme.Palette, summary frameSummary) string {
	line := palette.Paint(theme.RoleNeutral2, summary.primary)
	if summary.attentionCount > 0 {
		noun := summary.attentionNoun
		if noun == "" {
			noun = "need attention"
		} else if summary.attentionCount == 1 {
			noun = strings.TrimSuffix(noun, "s")
		}
		line += palette.Paint(theme.RoleNeutral2, "  ·  ") + palette.Paint(theme.RoleWarn, fmt.Sprintf("%d %s", summary.attentionCount, noun))
	}
	if summary.extra != "" {
		line += palette.Paint(theme.RoleNeutral2, "  ·  "+summary.extra)
	}
	return line
}
