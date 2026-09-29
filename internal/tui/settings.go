package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

// SettingsSnapshot is the read-only projection rendered by the Settings tab.
type SettingsSnapshot struct {
	CollectedAt          time.Time
	RuntimeDefaults      []SettingsRuntimeDefault
	Repositories         []RepositorySetting
	InheritedPullRequest bool
	// Invalid baselines are diagnostic placeholders, not accepted defaults.
	InheritedPullRequestInvalid bool
	DisplayHome                 string
	CollectionError             string
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
	sourceRole theme.Role
	warning    string
}

type settingsDefaultGroup struct {
	key         string
	title       string
	rows        []SettingsRuntimeDefault
	hasOverride bool
}

// settingsBudgetMetric names one BUDGET row. key is the METRIC part of the
// canonical TAO_BUDGET_<SCOPE>_<METRIC>_WARN/_STOP names; only metrics with an
// enforced slice cap register a STOP key, and plan scope never does.
type settingsBudgetMetric struct {
	label     string
	key       string
	cost      bool
	sliceStop bool
}

// settingsBudgetRow holds the rendered cells and diagnostics for one metric.
type settingsBudgetRow struct {
	label       string
	sliceWarn   string
	sliceStop   string
	planWarn    string
	planStop    string
	diagnostics []string
}

const settingsBudgetTitle = "BUDGET"

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
		lines = append(lines, "", sectionRule(model.Palette(), theme.RoleSettingsSection, "RUNTIME DEFAULTS", 0, sectionWidth), "  Runtime defaults unavailable.")
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

	nameWidth := cells.Width("REPOSITORY")
	healthWidth := cells.Width("HEALTH")
	pullRequestWidth := cells.Width("PR")
	rootWidth := cells.Width("ROOT")
	for _, repository := range model.SettingsSnapshot.Repositories {
		nameWidth = max(nameWidth, cells.Width(settingsRepositoryName(repository)))
		healthWidth = max(healthWidth, cells.Width(settingsRepositoryHealth(model.Palette(), repository.Health)))
		pullRequestWidth = max(pullRequestWidth, cells.Width(pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest)))
		rootWidth = max(rootWidth, cells.Width(settingsRepositoryRoot(repository.Root, model.SettingsSnapshot.DisplayHome)))
	}
	repositoryColumns := settingsRepositoryColumns(nameWidth, healthWidth, pullRequestWidth, rootWidth)
	sectionWidth := dashboardSectionWidth(model, PageSettings, "REPOSITORY DEFAULTS", columnsWidth(repositoryColumns))
	repositoryColumns = fitSettingsSectionColumns("REPOSITORY DEFAULTS", repositoryColumns, sectionWidth)
	lines = append(lines, "", settingsSectionRuleColumns(model.Palette(), theme.RoleSettingsSection, "REPOSITORY DEFAULTS", repositoryColumns, sectionWidth))
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
				cells = append(cells, settingsStyledRepositoryName(model.Palette(), repository))
			case "HEALTH":
				health := settingsRepositoryHealth(model.Palette(), repository.Health)
				if item.width < healthWidth {
					health = settingsRepositoryHealthIndicator(model.Palette(), repository.Health)
				}
				cells = append(cells, health)
			case "PR":
				cells = append(cells, pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest))
			case "ROOT":
				cells = append(cells, model.Palette().Paint(theme.RoleNeutral2, settingsRepositoryRoot(repository.Root, model.SettingsSnapshot.DisplayHome)))
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
			value:      runtimeDisplayValue(row.Value, row.Source),
			source:     "← " + rowlabel.DisplayValue(singleLineDetail(row.Source)),
			sourceRole: theme.RoleNeutral2,
			warning:    runtimeDiagnosticText(row.Source, row.Warning),
		})
	}
	for _, repository := range model.SettingsSnapshot.Repositories {
		if repository.PullRequest == nil || (!model.SettingsSnapshot.InheritedPullRequestInvalid && *repository.PullRequest == model.SettingsSnapshot.InheritedPullRequest) {
			continue
		}
		name := rowlabel.DisplayValue(singleLineDetail(repository.Name))
		if name == "-" {
			name = rowlabel.DisplayValue(singleLineDetail(repository.ID))
		}
		key := strings.TrimSpace(repository.ID)
		if key == "" {
			key = strings.TrimSpace(repository.Name)
		}
		overrides = append(overrides, settingsOverride{
			name:       "TAO_PULL_REQUEST",
			value:      fmt.Sprintf("%t", *repository.PullRequest),
			source:     "← " + name,
			sourceRole: theme.RepoColor(key),
		})
	}
	if len(overrides) == 0 {
		return nil, tableViewportSection{}
	}

	nameWidth := cells.Width("OVERRIDES")
	valueWidth := cells.Width("VALUE")
	sourceWidth := cells.Width("SOURCE")
	for _, row := range overrides {
		nameWidth = max(nameWidth, cells.Width(row.name))
		valueWidth = max(valueWidth, cells.Width(row.value))
		sourceWidth = max(sourceWidth, cells.Width(row.source))
	}
	columns := settingsRuntimeColumnsWithSource(nameWidth, valueWidth, sourceWidth)
	sectionWidth := dashboardSectionWidth(model, PageSettings, "OVERRIDES", columnsWidth(columns))
	columns = fitSettingsSectionColumns("OVERRIDES", columns, sectionWidth)
	lines := []string{"", settingsSectionRuleColumns(model.Palette(), theme.RoleSettingsSection, "OVERRIDES", columns, sectionWidth)}
	section := tableViewportSection{headingLines: []int{1}}
	for _, row := range overrides {
		section.contentLines = append(section.contentLines, len(lines))
		source := model.Palette().Paint(row.sourceRole, row.source)
		cells := make([]string, 0, len(columns))
		for _, item := range columns {
			switch item.name {
			case "NAME":
				cells = append(cells, row.name)
			case "VALUE":
				cells = append(cells, model.Palette().Paint(theme.RoleNeutral5, row.value))
			case "SOURCE":
				cells = append(cells, source)
			}
		}
		lines = append(lines, "  "+joinRow(columns, cells, columnsWidth(columns)))
		for _, line := range runtimeDiagnosticLines(model.Palette(), sectionWidth, row.warning) {
			section.contentLines = append(section.contentLines, len(lines))
			lines = append(lines, line)
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
			labelWidth = max(labelWidth, cells.Width(humanizeSettingsName(row.Name)))
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
		lines = append(lines, "", settingsDefaultGroupRule(model.Palette(), title, sectionWidth))
		section := tableViewportSection{headingLines: []int{len(lines) - 1}}
		for index := 0; index < len(group.rows); index += columns {
			rowEnd := min(index+columns, len(group.rows))
			cellWidth := contentWidth
			if columns == 2 {
				cellWidth = (contentWidth - 4) / 2
			}
			rowCells := make([]string, 0, columns)
			for _, row := range group.rows[index:rowEnd] {
				rowCells = append(rowCells, settingsDefaultPair(model.Palette(), row, labelWidth, true))
			}
			for len(rowCells) < columns {
				rowCells = append(rowCells, "")
			}
			for cellIndex := range rowCells {
				rowCells[cellIndex] = cells.Pad(cells.Truncate(rowCells[cellIndex], cellWidth), cellWidth)
			}
			section.contentLines = append(section.contentLines, len(lines))
			lines = append(lines, "  "+strings.Join(rowCells, "    "))

			if columns == 1 && rowEnd == index+1 && group.rows[index].Warning != "" && settingsDefaultPairWidth(group.rows[index], labelWidth) > cellWidth {
				lines[len(lines)-1] = "  " + cells.Pad(cells.Truncate(settingsDefaultPair(model.Palette(), group.rows[index], labelWidth, false), cellWidth), cellWidth)
				section.contentLines = append(section.contentLines, len(lines))
				lines = append(lines, "    "+model.Palette().Paint(theme.RoleWarn, "warning: "+singleLineDetail(group.rows[index].Warning)))
			}
		}
		sections = append(sections, section)
	}
	return lines, sections
}

func renderSettingsBudgets(model Model) ([]string, tableViewportSection) {
	metrics := []settingsBudgetMetric{
		{label: "Output tokens", key: "OUTPUT_TOKENS", sliceStop: true},
		{label: "Cost", key: "COST", cost: true, sliceStop: true},
		{label: "Tool calls", key: "TOOL_CALLS"},
		{label: "Assistant messages", key: "ASSISTANT_MESSAGES"},
		{label: "Errored messages", key: "ERRORED_MESSAGES"},
	}
	byName := make(map[string]SettingsRuntimeDefault)
	for _, row := range model.SettingsSnapshot.RuntimeDefaults {
		if settingsIsBudgetName(row.Name) {
			byName[row.Name] = row
		}
	}
	if len(byName) == 0 {
		return nil, tableViewportSection{}
	}

	rows := make([]settingsBudgetRow, 0, len(metrics))
	for _, metric := range metrics {
		rows = append(rows, settingsBudgetRowFor(metric, byName))
	}
	columns := []column{
		{name: "METRIC", width: cells.Width("METRIC"), required: true, priority: 30},
		{name: "SLICE WARN", width: cells.Width("SLICE WARN"), required: true, priority: 40},
		{name: "SLICE STOP", width: cells.Width("SLICE STOP"), priority: 20},
		{name: "PLAN WARN", width: cells.Width("PLAN WARN"), required: true, priority: 40},
		{name: "PLAN STOP", width: cells.Width("PLAN STOP"), priority: 10},
	}
	for _, row := range rows {
		for index, value := range row.cells() {
			columns[index].width = max(columns[index].width, cells.Width(value))
		}
	}
	sectionWidth := dashboardSectionWidth(model, PageSettings, settingsBudgetTitle, columnsWidth(columns))
	columns = fitSettingsSectionColumns(settingsBudgetTitle, columns, sectionWidth)
	lines := []string{"", settingsSectionRuleColumns(model.Palette(), theme.RoleSettingsSection, settingsBudgetTitle, columns, sectionWidth)}
	section := tableViewportSection{headingLines: []int{1}}
	for _, row := range rows {
		values := make([]string, 0, len(columns))
		for _, item := range columns {
			switch item.name {
			case "METRIC":
				values = append(values, row.label)
			case "SLICE WARN":
				values = append(values, settingsRightAlignedValue(model.Palette(), row.sliceWarn, item.width))
			case "SLICE STOP":
				values = append(values, settingsRightAlignedValue(model.Palette(), row.sliceStop, item.width))
			case "PLAN WARN":
				values = append(values, settingsRightAlignedValue(model.Palette(), row.planWarn, item.width))
			case "PLAN STOP":
				values = append(values, settingsRightAlignedValue(model.Palette(), row.planStop, item.width))
			}
		}
		section.contentLines = append(section.contentLines, len(lines))
		lines = append(lines, "  "+joinRow(columns, values, columnsWidth(columns)))
		for _, diagnostic := range row.diagnostics {
			for _, line := range runtimeDiagnosticLines(model.Palette(), sectionWidth, diagnostic) {
				section.contentLines = append(section.contentLines, len(lines))
				lines = append(lines, line)
			}
		}
	}
	return lines, section
}

// settingsIsBudgetName reports whether a runtime status row belongs to the
// BUDGET section: a canonical WARN/STOP key or one of its deprecated aliases.
func settingsIsBudgetName(name string) bool {
	return strings.HasPrefix(name, "TAO_BUDGET_") || strings.HasPrefix(name, "TAO_MAX_SLICE_")
}

func (row settingsBudgetRow) cells() []string {
	return []string{row.label, row.sliceWarn, row.sliceStop, row.planWarn, row.planStop}
}

// settingsBudgetRowFor resolves one metric's cells from the canonical status
// rows. A missing slice STOP row is disabled where a cap exists; plan STOP and
// unenforced slice STOP cells stay "-". Set aliases surface as diagnostics
// under the metric instead of as rows of their own.
func settingsBudgetRowFor(metric settingsBudgetMetric, byName map[string]SettingsRuntimeDefault) settingsBudgetRow {
	sliceWarnName := "TAO_BUDGET_SLICE_" + metric.key + "_WARN"
	sliceStopName := "TAO_BUDGET_SLICE_" + metric.key + "_STOP"
	planWarnName := "TAO_BUDGET_PLAN_" + metric.key + "_WARN"
	row := settingsBudgetRow{
		label:     metric.label,
		sliceWarn: settingsBudgetValue(byName[sliceWarnName], metric.cost),
		sliceStop: "-",
		planWarn:  settingsBudgetValue(byName[planWarnName], metric.cost),
		planStop:  "-",
	}
	if metric.sliceStop {
		row.sliceStop = "disabled"
		if stop, ok := byName[sliceStopName]; ok && strings.TrimSpace(stop.Value) != "" {
			row.sliceStop = settingsBudgetValue(stop, metric.cost)
		}
	}
	for _, scoped := range []struct {
		scope string
		name  string
	}{{"slice warn", sliceWarnName}, {"slice stop", sliceStopName}, {"plan warn", planWarnName}} {
		status, ok := byName[scoped.name]
		if diagnostic := runtimeDiagnosticText(status.Source, status.Warning); ok && diagnostic != "" {
			row.diagnostics = append(row.diagnostics, scoped.scope+" "+diagnostic)
		}
	}
	aliases := []string{"TAO_BUDGET_SLICE_" + metric.key, "TAO_BUDGET_PLAN_" + metric.key}
	if metric.sliceStop {
		aliases = append(aliases, "TAO_MAX_SLICE_"+metric.key)
	}
	for _, alias := range aliases {
		if status, ok := byName[alias]; ok {
			row.diagnostics = append(row.diagnostics, "deprecated "+alias+": "+singleLineDetail(status.Warning))
		}
	}
	return row
}

func settingsBudgetValue(row SettingsRuntimeDefault, cost bool) string {
	value := rowlabel.DisplayValue(runtimeDisplayValue(row.Value, row.Source))
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

// Runtime diagnostics are presentation only: rejected rows carry built-in
// placeholders, while warning-only rows carry usable presentation fallbacks.
func runtimeDisplayValue(value, source string) string {
	if source == "invalid" {
		return "(rejected)"
	}
	return singleLineDetail(value)
}

func runtimeDiagnosticText(source, warning string) string {
	if source == "invalid" {
		return "rejected on consumption: " + singleLineDetail(warning)
	}
	if warning != "" {
		return "warning: " + singleLineDetail(warning)
	}
	return ""
}

func runtimeDiagnosticLines(palette theme.Palette, width int, diagnostic string) []string {
	var lines []string
	for _, line := range wrapDetailWords(singleLineDetail(diagnostic), max(width-4, 1)) {
		lines = append(lines, "    "+palette.Paint(theme.RoleWarn, line))
	}
	return lines
}

func settingsRightAlignedValue(palette theme.Palette, value string, width int) string {
	value = singleLineDetail(value)
	return palette.Paint(theme.RoleNeutral4, strings.Repeat(" ", max(width-cells.Width(value), 0))+value)
}

func settingsDefaultGroupRule(palette theme.Palette, title string, width int) string {
	if width <= 0 {
		return ""
	}
	lead := "▌ " + title + " "
	if cells.Width(lead) >= width {
		return cells.Truncate(palette.Paint(theme.RoleSettingsSection, lead), width)
	}
	return palette.Paint(theme.RoleSettingsSection, lead) + palette.Paint(theme.RoleNeutral0, strings.Repeat("─", width-cells.Width(lead)))
}

func settingsDefaultGroups(rows []SettingsRuntimeDefault) []settingsDefaultGroup {
	groups := []settingsDefaultGroup{
		{key: settingsGroupExecution, title: "Execution"},
		{key: settingsGroupWorkflow, title: "Workflow"},
		{key: settingsGroupSafety, title: "Safety / update"},
		{key: settingsGroupOther, title: "Other"},
	}
	for _, row := range rows {
		if settingsIsBudgetName(row.Name) {
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
	case "TAO_COMMIT_POLICY", "TAO_EXECUTION_MODE", "TAO_AGENT", "TAO_SESSION_TIMEOUT",
		"TAO_MODEL", "TAO_RUN_MODEL", "TAO_REVIEW_MODEL", "TAO_MERGE_REVIEW_MODEL", "TAO_RESOLVER_MODEL", "TAO_REWORK_ESCALATION_MODEL":
		return settingsGroupExecution, true
	case "TAO_PULL_REQUEST", "TAO_REVIEW", "TAO_AUTO_REWORK", "TAO_MAX_REWORK_ATTEMPTS", "TAO_REWORK_ESCALATION_FROM_ATTEMPT",
		"TAO_PLANNER_ROUTING", "TAO_PLANNER_ROUTING_ARMS", "TAO_PLANNER_ROUTING_FLOOR":
		return settingsGroupWorkflow, true
	case "TAO_UPDATE", "TAO_DANGEROUSLY_SKIP_PERMISSIONS":
		return settingsGroupSafety, true
	case "TAO_MERGE_VERIFY_COMMAND", "TAO_AGGREGATE_REVIEW_CONVERGENCE_WINDOW", "TAO_APPROVED_BY", "TAO_RUN_HEADER", "TAO_THEME":
		return settingsGroupOther, true
	default:
		return settingsGroupOther, false
	}
}

func humanizeSettingsName(name string) string {
	switch name {
	case "TAO_PLANNER_ROUTING":
		return "Planner routing mode"
	case "TAO_DANGEROUSLY_SKIP_PERMISSIONS":
		return "Skip permissions"
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
	width := labelWidth + 1 + cells.Width(singleLineDetail(row.Value))
	if row.Warning != "" {
		width += cells.Width("  warning: " + singleLineDetail(row.Warning))
	}
	return width
}

func settingsDefaultPair(palette theme.Palette, row SettingsRuntimeDefault, labelWidth int, includeWarning bool) string {
	label := palette.Paint(theme.RoleNeutral2, cells.Pad(humanizeSettingsName(row.Name), labelWidth))
	value := singleLineDetail(row.Value)
	role := theme.RoleNeutral4
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "on":
		role = theme.RoleSuccess
	case "false", "off", "none":
		role = theme.RoleNeutral2
	}
	pair := label + " " + palette.Paint(role, value)
	if includeWarning && row.Warning != "" {
		pair += "  " + palette.Paint(theme.RoleWarn, "warning: "+singleLineDetail(row.Warning))
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
	columns[0].minimum = max(columns[0].minimum, cells.Width(title))
	return fitColumns(columns, width-2) // The rule marker and row cursor occupy two cells.
}

func settingsSectionRuleColumns(palette theme.Palette, role theme.Role, title string, columns []column, width int) string {
	if width <= 0 || len(columns) == 0 {
		return ""
	}
	line := palette.Paint(role, "▌ "+title+" ")
	position := cells.Width("▌ " + title + " ")
	for index := 1; index < len(columns); index++ {
		target := 2 + columnsWidth(columns[:index]) + columnGapWidth
		line += settingsSectionRuleGap(palette, target-position, true)
		line += palette.Paint(role, columns[index].name)
		position = target + cells.Width(columns[index].name)
	}
	line += settingsSectionRuleGap(palette, width-position, false)
	return line
}

func settingsSectionRuleGap(palette theme.Palette, width int, beforeHeader bool) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return palette.Paint(theme.RoleNeutral0, " ")
	}
	if beforeHeader {
		return strings.Repeat(" ", width)
	}
	return palette.Paint(theme.RoleNeutral0, " "+strings.Repeat("─", width-1))
}

func settingsRepositoryName(repository RepositorySetting) string {
	name := rowlabel.DisplayValue(singleLineDetail(repository.Name))
	if name != "-" {
		return name
	}
	return rowlabel.DisplayValue(singleLineDetail(repository.ID))
}

func settingsStyledRepositoryName(palette theme.Palette, repository RepositorySetting) string {
	key := strings.TrimSpace(repository.ID)
	if key == "" {
		key = strings.TrimSpace(repository.Name)
	}
	return palette.Paint(theme.RepoColor(key), settingsRepositoryName(repository))
}

func settingsRepositoryHealth(palette theme.Palette, health string) string {
	status := strings.TrimSpace(singleLineDetail(health))
	role := settingsRepositoryHealthRole(status)
	if status == "" {
		status = "unknown"
	}
	text := strings.ReplaceAll(status, "_", " ")
	return palette.Paint(role, "●") + " " + text
}

func settingsRepositoryHealthIndicator(palette theme.Palette, health string) string {
	return palette.Paint(settingsRepositoryHealthRole(strings.TrimSpace(singleLineDetail(health))), "●")
}

func settingsRepositoryHealthRole(status string) theme.Role {
	if status == "ok" {
		return theme.RoleSuccess
	}
	return theme.RoleWarn
}

func settingsRepositoryRoot(root, displayHome string) string {
	rawRoot := rowlabel.DisplayValue(singleLineDetail(root))
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
