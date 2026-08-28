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

func renderSettingsPage(model Model) ([]string, int) {
	var lines []string
	lines = append(lines, "", "GLOBAL RUNTIME DEFAULTS")
	if len(model.SettingsSnapshot.RuntimeDefaults) == 0 {
		lines = append(lines, "  Runtime defaults unavailable.")
	} else {
		nameWidth := visibleWidth("NAME")
		valueWidth := visibleWidth("VALUE")
		for _, row := range model.SettingsSnapshot.RuntimeDefaults {
			nameWidth = max(nameWidth, visibleWidth(row.Name))
			valueWidth = max(valueWidth, visibleWidth(row.Value))
		}
		lines = append(lines, "  "+padCells("NAME", nameWidth)+"  "+padCells("VALUE", valueWidth)+"  SOURCE")
		for _, row := range model.SettingsSnapshot.RuntimeDefaults {
			lines = append(lines, "  "+padCells(singleLineDetail(row.Name), nameWidth)+"  "+padCells(singleLineDetail(row.Value), valueWidth)+"  "+singleLineDetail(row.Source))
			if row.Warning != "" {
				lines = append(lines, "    warning: "+singleLineDetail(row.Warning))
			}
		}
	}

	lines = append(lines, "", "REPOSITORY DEFAULTS")
	if model.SettingsSnapshot.CollectionError != "" {
		lines = append(lines, "  ⚠ "+singleLineDetail(model.SettingsSnapshot.CollectionError))
	}
	if len(model.SettingsSnapshot.Repositories) == 0 {
		lines = append(lines, "  No registered repositories.")
		return lines, -1
	}

	nameWidth := visibleWidth("REPOSITORY")
	healthWidth := visibleWidth("HEALTH")
	pullRequestWidth := visibleWidth("PULL_REQUEST")
	for _, repository := range model.SettingsSnapshot.Repositories {
		nameWidth = max(nameWidth, visibleWidth(settingsRepositoryName(repository)))
		healthWidth = max(healthWidth, visibleWidth(displayValue(repository.Health)))
		pullRequestWidth = max(pullRequestWidth, visibleWidth(pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest)))
	}
	lines = append(lines, "  "+padCells("REPOSITORY", nameWidth)+"  "+padCells("HEALTH", healthWidth)+"  "+padCells("PULL_REQUEST", pullRequestWidth)+"  ROOT")
	selectedLine := -1
	for index, repository := range model.SettingsSnapshot.Repositories {
		cursor := "  "
		if index == model.Selected {
			cursor = "> "
			selectedLine = len(lines)
		}
		line := cursor + padCells(settingsRepositoryName(repository), nameWidth) + "  " +
			padCells(displayValue(singleLineDetail(repository.Health)), healthWidth) + "  " +
			padCells(pullRequestSetting(repository.PullRequest, model.SettingsSnapshot.InheritedPullRequest), pullRequestWidth) + "  " +
			displayValue(singleLineDetail(repository.Root))
		lines = append(lines, line)
		finding := strings.TrimSpace(repository.Finding)
		if index == model.Selected && finding != "" && finding != "ok" {
			lines = append(lines, "    finding: "+singleLineDetail(finding))
		}
	}
	return lines, selectedLine
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

func settingsCountLabel(count int) string {
	if count == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", count)
}
