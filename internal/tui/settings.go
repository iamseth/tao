package tui

import (
	"fmt"
	"strings"
	"time"
)

// SettingsSnapshot is the read-only projection rendered by the Settings tab.
type SettingsSnapshot struct {
	CollectedAt          time.Time
	RuntimeDefaults      []SettingsRuntimeDefault
	Repositories         []RepositorySetting
	InheritedPullRequest bool
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

func renderSettingsPage(model Model) ([]string, int, tableViewportMetadata) {
	var lines []string
	var metadata tableViewportMetadata
	if len(model.SettingsSnapshot.RuntimeDefaults) == 0 {
		sectionWidth := dashboardSectionWidth(model, PageSettings, "GLOBAL RUNTIME DEFAULTS", 1)
		lines = append(lines, "", sectionRule(model.Profile, RoleAccent, "GLOBAL RUNTIME DEFAULTS", 0, sectionWidth), "  Runtime defaults unavailable.")
		metadata.sections = append(metadata.sections, tableViewportSection{headingLines: []int{1}, contentLines: []int{2}})
	} else {
		nameWidth := visibleWidth("NAME")
		valueWidth := visibleWidth("VALUE")
		sourceWidth := visibleWidth("SOURCE")
		for _, row := range model.SettingsSnapshot.RuntimeDefaults {
			nameWidth = max(nameWidth, visibleWidth(row.Name))
			valueWidth = max(valueWidth, visibleWidth(row.Value))
			sourceWidth = max(sourceWidth, visibleWidth(row.Source))
		}
		columns := settingsRuntimeColumnsWithSource(nameWidth, valueWidth, sourceWidth)
		sectionWidth := dashboardSectionWidth(model, PageSettings, "GLOBAL RUNTIME DEFAULTS", columnsWidth(columns))
		columns = fitSettingsSectionColumns("GLOBAL RUNTIME DEFAULTS", columns, sectionWidth)
		lines = append(lines, "", settingsSectionRuleColumns(model.Profile, RoleAccent, "GLOBAL RUNTIME DEFAULTS", columns, sectionWidth))
		section := tableViewportSection{headingLines: []int{len(lines) - 1}}
		for _, row := range model.SettingsSnapshot.RuntimeDefaults {
			section.contentLines = append(section.contentLines, len(lines))
			cells := []string{singleLineDetail(row.Name), singleLineDetail(row.Value), singleLineDetail(row.Source)}
			lines = append(lines, "  "+joinRow(columns, cells, columnsWidth(columns)))
			if row.Warning != "" {
				section.contentLines = append(section.contentLines, len(lines))
				lines = append(lines, "    warning: "+singleLineDetail(row.Warning))
			}
		}
		metadata.sections = append(metadata.sections, section)
	}

	nameWidth := visibleWidth("REPOSITORY")
	healthWidth := visibleWidth("HEALTH")
	pullRequestWidth := visibleWidth("PULL_REQUEST")
	rootWidth := visibleWidth("ROOT")
	for _, repository := range model.SettingsSnapshot.Repositories {
		nameWidth = max(nameWidth, visibleWidth(settingsRepositoryName(repository)))
		healthWidth = max(healthWidth, visibleWidth(displayValue(repository.Health)))
		pullRequestWidth = max(pullRequestWidth, visibleWidth(pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest)))
		rootWidth = max(rootWidth, visibleWidth(displayValue(repository.Root)))
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
		cells := []string{
			settingsRepositoryName(repository),
			displayValue(singleLineDetail(repository.Health)),
			pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest),
			displayValue(singleLineDetail(repository.Root)),
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

func settingsRuntimeColumns(nameWidth, valueWidth int) []column {
	return settingsRuntimeColumnsWithSource(nameWidth, valueWidth, visibleWidth("SOURCE"))
}

func settingsRuntimeColumnsWithSource(nameWidth, valueWidth, sourceWidth int) []column {
	return []column{
		{name: "NAME", width: nameWidth},
		{name: "VALUE", width: valueWidth},
		{name: "SOURCE", width: sourceWidth},
	}
}

func settingsRepositoryColumns(nameWidth, healthWidth, pullRequestWidth, rootWidth int) []column {
	return []column{
		{name: "REPOSITORY", width: nameWidth},
		{name: "HEALTH", width: healthWidth},
		{name: "PULL_REQUEST", width: pullRequestWidth},
		{name: "ROOT", width: rootWidth},
	}
}

// fitSettingsSectionColumns uses the section title as the leading column
// header. This lets the rule and rows share one left-aligned column layout.
func fitSettingsSectionColumns(title string, columns []column, width int) []column {
	if len(columns) == 0 || width <= 2 {
		return columns
	}
	available := width - 2 // The rule marker and row cursor occupy two cells.
	fitted := make([]column, 0, len(columns))
	used := 0
	for index, item := range columns {
		gap := 0
		if index > 0 {
			gap = columnGapWidth
		}
		remaining := available - used - gap
		headerWidth := visibleWidth(item.name)
		if index == 0 {
			headerWidth = visibleWidth(title)
		}
		if remaining < headerWidth {
			break
		}
		item.width = min(max(item.width, headerWidth), remaining)
		fitted = append(fitted, item)
		used += gap + item.width
	}
	if len(fitted) == 0 {
		return columns[:1]
	}
	return fitted
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
	id := displayValue(singleLineDetail(repository.ID))
	if name == "-" {
		return id
	}
	return fmt.Sprintf("%s (%s)", name, id)
}

func pullRequestSetting(value *bool, inherited bool) string {
	if value == nil {
		return fmt.Sprintf("inherit (%t)", inherited)
	}
	return fmt.Sprintf("explicit %t", *value)
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
