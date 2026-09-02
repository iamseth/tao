package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SettingsSnapshot is the read-only projection rendered by the Settings tab.
type SettingsSnapshot struct {
	CollectedAt          time.Time
	RuntimeDefaults      []SettingsRuntimeDefault
	Repositories         []RepositorySetting
	InheritedPullRequest bool
	DisplayHome          string
	CollectionError      string
}

// SettingsRuntimeDefault is one environment/built-in runtime baseline.
type SettingsRuntimeDefault struct {
	Name    string
	Value   string
	Source  string
	Warning string
}

// RepositorySetting is one registered repository and its explicit run default.
type RepositorySetting struct {
	ID          string
	Name        string
	Root        string
	Health      string
	Finding     string
	PullRequest *bool
}

type settingsOverride struct {
	name       string
	value      string
	source     string
	sourceRole Role
	warning    string
}

type settingsDefaultGroup struct {
	key         string
	title       string
	rows        []SettingsRuntimeDefault
	hasOverride bool
}

type settingsBudgetMetric struct {
	label     string
	sliceName string
	planName  string
	cost      bool
}

const (
	settingsGroupExecution = "execution"
	settingsGroupWorkflow  = "workflow"
	settingsGroupSafety    = "safety-update"
	settingsGroupOther     = "other"
)

func renderSettingsPage(model Model) ([]string, int, tableViewportMetadata) {
	var lines []string
	var metadata tableViewportMetadata
	if overrideLines, overrideSection := renderSettingsOverrides(model); len(overrideLines) > 0 {
		lines = append(lines, overrideLines...)
		metadata.sections = append(metadata.sections, overrideSection)
	}
	if len(model.SettingsSnapshot.RuntimeDefaults) == 0 {
		offset := len(lines)
		sectionWidth := dashboardSectionWidth(model, PageSettings, "RUNTIME DEFAULTS", 1)
		lines = append(lines, "", sectionRule(model.Profile, RoleAccent, "RUNTIME DEFAULTS", 0, sectionWidth), "  Runtime defaults unavailable.")
		metadata.sections = append(metadata.sections, tableViewportSection{headingLines: []int{offset + 1}, contentLines: []int{offset + 2}})
	} else {
		defaultLines, defaultSections := renderSettingsDefaultGroups(model)
		lines = append(lines, defaultLines...)
		for _, section := range defaultSections {
			for index := range section.headingLines {
				section.headingLines[index] += len(lines) - len(defaultLines)
			}
			for index := range section.contentLines {
				section.contentLines[index] += len(lines) - len(defaultLines)
			}
			metadata.sections = append(metadata.sections, section)
		}
		budgetLines, budgetSection := renderSettingsBudgets(model)
		if len(budgetLines) > 0 {
			offset := len(lines)
			lines = append(lines, budgetLines...)
			for index := range budgetSection.headingLines {
				budgetSection.headingLines[index] += offset
			}
			for index := range budgetSection.contentLines {
				budgetSection.contentLines[index] += offset
			}
			metadata.sections = append(metadata.sections, budgetSection)
		}
	}

	nameWidth := visibleWidth("REPOSITORY")
	healthWidth := visibleWidth("HEALTH")
	pullRequestWidth := visibleWidth("PR")
	rootWidth := visibleWidth("ROOT")
	for _, repository := range model.SettingsSnapshot.Repositories {
		nameWidth = max(nameWidth, visibleWidth(settingsRepositoryName(repository)))
		healthWidth = max(healthWidth, visibleWidth(settingsRepositoryHealth(model.Profile, repository.Health)))
		pullRequestWidth = max(pullRequestWidth, visibleWidth(pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest)))
		rootWidth = max(rootWidth, visibleWidth(settingsRepositoryRoot(repository.Root, model.SettingsSnapshot.DisplayHome)))
	}
	repositoryColumns := settingsRepositoryColumns(nameWidth, healthWidth, pullRequestWidth, rootWidth)
	repositoryRole := RoleAccent
	if settingsNeedsAttention(model.SettingsSnapshot) {
		repositoryRole = RoleWarn
	}
	sectionWidth := dashboardSectionWidth(model, PageSettings, "REPOSITORY DEFAULTS", columnsWidth(repositoryColumns))
	repositoryColumns = fitSettingsSectionColumns("REPOSITORY DEFAULTS", repositoryColumns, sectionWidth)
	lines = append(lines, "", settingsSectionRuleColumns(model.Profile, repositoryRole, "REPOSITORY DEFAULTS", repositoryColumns, sectionWidth))
	repositorySection := tableViewportSection{headingLines: []int{len(lines) - 1}}
	if model.SettingsSnapshot.CollectionError != "" {
		repositorySection.contentLines = append(repositorySection.contentLines, len(lines))
		lines = append(lines, "  ⚠ "+singleLineDetail(model.SettingsSnapshot.CollectionError))
	}
	if len(model.SettingsSnapshot.Repositories) == 0 {
		repositorySection.contentLines = append(repositorySection.contentLines, len(lines))
		lines = append(lines, "  No registered repositories.")
		metadata.sections = append(metadata.sections, repositorySection)
		return lines, -1, metadata
	}
	selectedLine := -1
	for index, repository := range model.SettingsSnapshot.Repositories {
		cursor := "  "
		if index == model.Selected {
			cursor = "> "
			selectedLine = len(lines)
		}
		cells := make([]string, 0, len(repositoryColumns))
		for _, item := range repositoryColumns {
			switch item.name {
			case "REPOSITORY":
				cells = append(cells, settingsStyledRepositoryName(model.Profile, repository))
			case "HEALTH":
				health := settingsRepositoryHealth(model.Profile, repository.Health)
				if item.width < healthWidth {
					health = settingsRepositoryHealthIndicator(model.Profile, repository.Health)
				}
				cells = append(cells, health)
			case "PR":
				cells = append(cells, pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest))
			case "ROOT":
				cells = append(cells, Paint(model.Profile, RoleNeutral2, settingsRepositoryRoot(repository.Root, model.SettingsSnapshot.DisplayHome)))
			}
		}
		repositorySection.contentLines = append(repositorySection.contentLines, len(lines))
		lines = append(lines, cursor+joinRow(repositoryColumns, cells, columnsWidth(repositoryColumns)))
		finding := strings.TrimSpace(repository.Finding)
		if index == model.Selected && finding != "" && finding != "ok" {
			repositorySection.contentLines = append(repositorySection.contentLines, len(lines))
			lines = append(lines, "    finding: "+singleLineDetail(finding))
		}
	}
	metadata.sections = append(metadata.sections, repositorySection)
	return lines, selectedLine, metadata
}

func renderSettingsOverrides(model Model) ([]string, tableViewportSection) {
	overrides := make([]settingsOverride, 0)
	for _, row := range model.SettingsSnapshot.RuntimeDefaults {
		if !settingsRuntimeIsOverride(row) {
			continue
		}
		overrides = append(overrides, settingsOverride{
			name:       singleLineDetail(row.Name),
			value:      singleLineDetail(row.Value),
			source:     "← " + displayValue(singleLineDetail(row.Source)),
			sourceRole: RoleNeutral2,
			warning:    row.Warning,
		})
	}
	for _, repository := range model.SettingsSnapshot.Repositories {
		if repository.PullRequest == nil || *repository.PullRequest == model.SettingsSnapshot.InheritedPullRequest {
			continue
		}
		name := displayValue(singleLineDetail(repository.Name))
		if name == "-" {
			name = displayValue(singleLineDetail(repository.ID))
		}
		key := strings.TrimSpace(repository.ID)
		if key == "" {
			key = strings.TrimSpace(repository.Name)
		}
		overrides = append(overrides, settingsOverride{
			name:       "TAO_PULL_REQUEST",
			value:      fmt.Sprintf("%t", *repository.PullRequest),
			source:     "← " + name,
			sourceRole: RepoColor(key),
		})
	}
	if len(overrides) == 0 {
		return nil, tableViewportSection{}
	}

	nameWidth := visibleWidth("OVERRIDES")
	valueWidth := visibleWidth("VALUE")
	sourceWidth := visibleWidth("SOURCE")
	for _, row := range overrides {
		nameWidth = max(nameWidth, visibleWidth(row.name))
		valueWidth = max(valueWidth, visibleWidth(row.value))
		sourceWidth = max(sourceWidth, visibleWidth(row.source))
	}
	columns := settingsRuntimeColumnsWithSource(nameWidth, valueWidth, sourceWidth)
	sectionWidth := dashboardSectionWidth(model, PageSettings, "OVERRIDES", columnsWidth(columns))
	columns = fitSettingsSectionColumns("OVERRIDES", columns, sectionWidth)
	lines := []string{"", settingsSectionRuleColumns(model.Profile, RoleAccent, "OVERRIDES", columns, sectionWidth)}
	section := tableViewportSection{headingLines: []int{1}}
	for _, row := range overrides {
		section.contentLines = append(section.contentLines, len(lines))
		source := Paint(model.Profile, row.sourceRole, row.source)
		cells := make([]string, 0, len(columns))
		for _, item := range columns {
			switch item.name {
			case "NAME":
				cells = append(cells, row.name)
			case "VALUE":
				cells = append(cells, Paint(model.Profile, RoleNeutral5, row.value))
			case "SOURCE":
				cells = append(cells, source)
			}
		}
		lines = append(lines, "  "+joinRow(columns, cells, columnsWidth(columns)))
		if row.warning != "" {
			section.contentLines = append(section.contentLines, len(lines))
			lines = append(lines, "    "+Paint(model.Profile, RoleWarn, "warning: "+singleLineDetail(row.warning)))
		}
	}
	return lines, section
}

func renderSettingsDefaultGroups(model Model) ([]string, []tableViewportSection) {
	groups := settingsDefaultGroups(model.SettingsSnapshot.RuntimeDefaults)
	var lines []string
	sections := make([]tableViewportSection, 0, len(groups))
	for _, group := range groups {
		labelWidth := 0
		pairWidth := 0
		for _, row := range group.rows {
			labelWidth = max(labelWidth, visibleWidth(humanizeSettingsName(row.Name)))
		}
		for _, row := range group.rows {
			pairWidth = max(pairWidth, settingsDefaultPairWidth(row, labelWidth))
		}

		title := strings.ToUpper(group.title)
		if settingsGroupAllDefault(group) {
			title += " · all default"
		}
		sectionWidth := dashboardSectionWidth(model, PageSettings, title, pairWidth*2+4)
		contentWidth := max(sectionWidth-2, 1)
		columns := 1
		if contentWidth >= pairWidth*2+4 {
			columns = 2
		}
		lines = append(lines, "", settingsDefaultGroupRule(model.Profile, title, sectionWidth))
		section := tableViewportSection{headingLines: []int{len(lines) - 1}}
		for index := 0; index < len(group.rows); index += columns {
			rowEnd := min(index+columns, len(group.rows))
			cellWidth := contentWidth
			if columns == 2 {
				cellWidth = (contentWidth - 4) / 2
			}
			cells := make([]string, 0, columns)
			for _, row := range group.rows[index:rowEnd] {
				cells = append(cells, settingsDefaultPair(model.Profile, row, labelWidth, true))
			}
			for len(cells) < columns {
				cells = append(cells, "")
			}
			for cellIndex := range cells {
				cells[cellIndex] = padCells(truncateCells(cells[cellIndex], cellWidth), cellWidth)
			}
			section.contentLines = append(section.contentLines, len(lines))
			lines = append(lines, "  "+strings.Join(cells, "    "))

			if columns == 1 && rowEnd == index+1 && group.rows[index].Warning != "" && settingsDefaultPairWidth(group.rows[index], labelWidth) > cellWidth {
				lines[len(lines)-1] = "  " + padCells(truncateCells(settingsDefaultPair(model.Profile, group.rows[index], labelWidth, false), cellWidth), cellWidth)
				section.contentLines = append(section.contentLines, len(lines))
				lines = append(lines, "    "+Paint(model.Profile, RoleWarn, "warning: "+singleLineDetail(group.rows[index].Warning)))
			}
		}
		sections = append(sections, section)
	}
	return lines, sections
}

func renderSettingsBudgets(model Model) ([]string, tableViewportSection) {
	metrics := []settingsBudgetMetric{
		{label: "Output tokens", sliceName: "TAO_BUDGET_SLICE_OUTPUT_TOKENS", planName: "TAO_BUDGET_PLAN_OUTPUT_TOKENS"},
		{label: "Cost", sliceName: "TAO_BUDGET_SLICE_COST", planName: "TAO_BUDGET_PLAN_COST", cost: true},
		{label: "Tool calls", sliceName: "TAO_BUDGET_SLICE_TOOL_CALLS", planName: "TAO_BUDGET_PLAN_TOOL_CALLS"},
		{label: "Assistant messages", sliceName: "TAO_BUDGET_SLICE_ASSISTANT_MESSAGES", planName: "TAO_BUDGET_PLAN_ASSISTANT_MESSAGES"},
		{label: "Errored messages", sliceName: "TAO_BUDGET_SLICE_ERRORED_MESSAGES", planName: "TAO_BUDGET_PLAN_ERRORED_MESSAGES"},
	}
	byName := make(map[string]SettingsRuntimeDefault)
	for _, row := range model.SettingsSnapshot.RuntimeDefaults {
		if strings.HasPrefix(row.Name, "TAO_BUDGET_") {
			byName[row.Name] = row
		}
	}
	if len(byName) == 0 {
		return nil, tableViewportSection{}
	}

	labelWidth := visibleWidth("METRIC")
	sliceWidth := visibleWidth("SLICE")
	planWidth := visibleWidth("PLAN")
	for _, metric := range metrics {
		labelWidth = max(labelWidth, visibleWidth(metric.label))
		sliceWidth = max(sliceWidth, visibleWidth(settingsBudgetValue(byName[metric.sliceName], metric.cost)))
		planWidth = max(planWidth, visibleWidth(settingsBudgetValue(byName[metric.planName], metric.cost)))
	}
	columns := []column{
		{name: "METRIC", width: labelWidth, required: true, priority: 30},
		{name: "SLICE", width: sliceWidth, required: true, priority: 40},
		{name: "PLAN", width: planWidth, required: true, priority: 40},
	}
	sectionWidth := dashboardSectionWidth(model, PageSettings, "BUDGET WARNINGS", columnsWidth(columns))
	columns = fitSettingsSectionColumns("BUDGET WARNINGS", columns, sectionWidth)
	if len(columns) > 1 {
		sliceWidth = columns[1].width
	}
	if len(columns) > 2 {
		planWidth = columns[2].width
	}
	lines := []string{"", settingsSectionRuleColumns(model.Profile, RoleAccent, "BUDGET WARNINGS", columns, sectionWidth)}
	section := tableViewportSection{headingLines: []int{1}}
	for _, metric := range metrics {
		sliceRow, sliceOK := byName[metric.sliceName]
		planRow, planOK := byName[metric.planName]
		sliceValue := settingsBudgetValue(sliceRow, metric.cost)
		planValue := settingsBudgetValue(planRow, metric.cost)
		cells := []string{
			metric.label,
			settingsRightAlignedValue(model.Profile, sliceValue, sliceWidth),
			settingsRightAlignedValue(model.Profile, planValue, planWidth),
		}
		section.contentLines = append(section.contentLines, len(lines))
		lines = append(lines, "  "+joinRow(columns, cells, columnsWidth(columns)))
		for _, warning := range []struct {
			scope string
			row   SettingsRuntimeDefault
			ok    bool
		}{{"slice", sliceRow, sliceOK}, {"plan", planRow, planOK}} {
			if warning.ok && warning.row.Warning != "" {
				section.contentLines = append(section.contentLines, len(lines))
				lines = append(lines, "    "+Paint(model.Profile, RoleWarn, warning.scope+" warning: "+singleLineDetail(warning.row.Warning)))
			}
		}
	}
	return lines, section
}

func settingsBudgetValue(row SettingsRuntimeDefault, cost bool) string {
	value := displayValue(singleLineDetail(row.Value))
	if value == "-" || cost {
		return value
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return value
	}
	digits := strconv.FormatInt(number, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", strings.TrimPrefix(digits, "-")
	}
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + " " + digits[index:]
	}
	return sign + digits
}

func settingsRightAlignedValue(profile Profile, value string, width int) string {
	value = singleLineDetail(value)
	return Paint(profile, RoleNeutral4, strings.Repeat(" ", max(width-visibleWidth(value), 0))+value)
}

func settingsDefaultGroupRule(profile Profile, title string, width int) string {
	if width <= 0 {
		return ""
	}
	lead := "▌ " + title + " "
	if visibleWidth(lead) >= width {
		return truncateCells(Paint(profile, RoleAccent, lead), width)
	}
	return Paint(profile, RoleAccent, lead) + Paint(profile, RoleNeutral0, strings.Repeat("─", width-visibleWidth(lead)))
}

func settingsDefaultGroups(rows []SettingsRuntimeDefault) []settingsDefaultGroup {
	groups := []settingsDefaultGroup{
		{key: settingsGroupExecution, title: "Execution"},
		{key: settingsGroupWorkflow, title: "Workflow"},
		{key: settingsGroupSafety, title: "Safety / update"},
		{key: settingsGroupOther, title: "Other"},
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Name, "TAO_BUDGET_") {
			continue
		}
		key, _ := settingsDefaultGroupForName(row.Name)
		for index := range groups {
			if groups[index].key != key {
				continue
			}
			if settingsRuntimeIsOverride(row) {
				groups[index].hasOverride = true
			} else {
				groups[index].rows = append(groups[index].rows, row)
			}
			break
		}
	}
	result := groups[:0]
	for _, group := range groups {
		if len(group.rows) > 0 {
			result = append(result, group)
		}
	}
	return result
}

func settingsRuntimeIsOverride(row SettingsRuntimeDefault) bool {
	return strings.TrimSpace(row.Source) != "default" || row.Warning != ""
}

func settingsDefaultGroupForName(name string) (string, bool) {
	switch name {
	case "TAO_COMMIT_POLICY", "TAO_EXECUTION_MODE", "TAO_AGENT", "TAO_SESSION_TIMEOUT":
		return settingsGroupExecution, true
	case "TAO_PULL_REQUEST", "TAO_REVIEW", "TAO_AUTO_REWORK", "TAO_MAX_REWORK_ATTEMPTS":
		return settingsGroupWorkflow, true
	case "TAO_UPDATE", "TAO_DANGEROUSLY_SKIP_PERMISSIONS", "TAO_MAX_SLICE_OUTPUT_TOKENS", "TAO_MAX_SLICE_COST":
		return settingsGroupSafety, true
	default:
		return settingsGroupOther, false
	}
}

func humanizeSettingsName(name string) string {
	switch name {
	case "TAO_DANGEROUSLY_SKIP_PERMISSIONS":
		return "Skip permissions"
	case "TAO_MAX_SLICE_OUTPUT_TOKENS":
		return "Slice output cap"
	case "TAO_MAX_SLICE_COST":
		return "Slice cost cap"
	}
	words := strings.Fields(strings.ReplaceAll(strings.TrimPrefix(name, "TAO_"), "_", " "))
	label := strings.ToLower(strings.Join(words, " "))
	if label == "" {
		return "Setting"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func settingsGroupAllDefault(group settingsDefaultGroup) bool {
	return !group.hasOverride
}

func settingsDefaultPairWidth(row SettingsRuntimeDefault, labelWidth int) int {
	width := labelWidth + 1 + visibleWidth(singleLineDetail(row.Value))
	if row.Warning != "" {
		width += visibleWidth("  warning: " + singleLineDetail(row.Warning))
	}
	return width
}

func settingsDefaultPair(profile Profile, row SettingsRuntimeDefault, labelWidth int, includeWarning bool) string {
	label := Paint(profile, RoleNeutral2, padCells(humanizeSettingsName(row.Name), labelWidth))
	value := singleLineDetail(row.Value)
	role := RoleNeutral4
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "on":
		role = RoleSuccess
	case "false", "off", "none":
		role = RoleNeutral2
	}
	pair := label + " " + Paint(profile, role, value)
	if includeWarning && row.Warning != "" {
		pair += "  " + Paint(profile, RoleWarn, "warning: "+singleLineDetail(row.Warning))
	}
	return pair
}

func settingsRuntimeColumnsWithSource(nameWidth, valueWidth, sourceWidth int) []column {
	return []column{
		{name: "NAME", width: nameWidth, required: true, priority: 30},
		{name: "VALUE", width: valueWidth, required: true, priority: 40},
		{name: "SOURCE", width: sourceWidth, priority: 10},
	}
}

func settingsRepositoryColumns(nameWidth, healthWidth, pullRequestWidth, rootWidth int) []column {
	return []column{
		{name: "REPOSITORY", width: nameWidth, required: true, priority: 30},
		{name: "HEALTH", width: healthWidth, required: true, priority: 10},
		{name: "PR", width: pullRequestWidth, required: true, priority: 40, minimum: len("pr=inherit")},
		{name: "ROOT", width: rootWidth, priority: 5},
	}
}

// fitSettingsSectionColumns uses the section title as the leading column
// header. This lets the rule and rows share one left-aligned column layout.
func fitSettingsSectionColumns(title string, columns []column, width int) []column {
	if len(columns) == 0 || width <= 2 {
		return append([]column(nil), columns...)
	}
	columns = append([]column(nil), columns...)
	columns[0].minimum = max(columns[0].minimum, visibleWidth(title))
	return fitColumns(columns, width-2) // The rule marker and row cursor occupy two cells.
}

func settingsSectionRuleColumns(profile Profile, role Role, title string, columns []column, width int) string {
	if width <= 0 || len(columns) == 0 {
		return ""
	}
	line := Paint(profile, role, "▌ "+title+" ")
	position := visibleWidth("▌ " + title + " ")
	for index := 1; index < len(columns); index++ {
		target := 2 + columnsWidth(columns[:index]) + columnGapWidth
		line += settingsSectionRuleGap(profile, target-position, true)
		line += Paint(profile, role, columns[index].name)
		position = target + visibleWidth(columns[index].name)
	}
	line += settingsSectionRuleGap(profile, width-position, false)
	return line
}

func settingsSectionRuleGap(profile Profile, width int, beforeHeader bool) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return Paint(profile, RoleNeutral0, " ")
	}
	if beforeHeader {
		return strings.Repeat(" ", width)
	}
	return Paint(profile, RoleNeutral0, " "+strings.Repeat("─", width-1))
}

func settingsNeedsAttention(snapshot SettingsSnapshot) bool {
	if snapshot.CollectionError != "" {
		return true
	}
	for _, repository := range snapshot.Repositories {
		health := strings.TrimSpace(repository.Health)
		if health != "" && health != "ok" {
			return true
		}
	}
	return false
}

func settingsRepositoryName(repository RepositorySetting) string {
	name := displayValue(singleLineDetail(repository.Name))
	if name != "-" {
		return name
	}
	return displayValue(singleLineDetail(repository.ID))
}

func settingsStyledRepositoryName(profile Profile, repository RepositorySetting) string {
	key := strings.TrimSpace(repository.ID)
	if key == "" {
		key = strings.TrimSpace(repository.Name)
	}
	return Paint(profile, RepoColor(key), settingsRepositoryName(repository))
}

func settingsRepositoryHealth(profile Profile, health string) string {
	status := strings.TrimSpace(singleLineDetail(health))
	role := settingsRepositoryHealthRole(status)
	if status == "" {
		status = "unknown"
	}
	text := strings.ReplaceAll(status, "_", " ")
	return Paint(profile, role, "●") + " " + text
}

func settingsRepositoryHealthIndicator(profile Profile, health string) string {
	return Paint(profile, settingsRepositoryHealthRole(strings.TrimSpace(singleLineDetail(health))), "●")
}

func settingsRepositoryHealthRole(status string) Role {
	if status == "ok" {
		return RoleSuccess
	}
	return RoleWarn
}

func settingsRepositoryRoot(root, displayHome string) string {
	rawRoot := displayValue(singleLineDetail(root))
	home := strings.TrimSpace(displayHome)
	if rawRoot == "-" || home == "" || !filepath.IsAbs(rawRoot) || !filepath.IsAbs(home) {
		return rawRoot
	}
	cleanRoot := filepath.Clean(rawRoot)
	cleanHome := filepath.Clean(home)
	relative, err := filepath.Rel(cleanHome, cleanRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return rawRoot
	}
	if relative == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + relative
}

func pullRequestSetting(value *bool, _ bool) string {
	if value == nil {
		return "pr=inherit"
	}
	if *value {
		return "pr=on"
	}
	return "pr=off"
}

func nextPullRequestSetting(value *bool) *bool {
	if value == nil {
		next := true
		return &next
	}
	if *value {
		next := false
		return &next
	}
	return nil
}
