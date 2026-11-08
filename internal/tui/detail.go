package tui

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

const planDetailFixedLines = 6 // header, two gaps, tabs, and pane borders

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
