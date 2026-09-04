package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term/cells"
)

// DebugSnapshot is a share-safe projection of runtime configuration and local
// diagnostics for the read-only Debug tab.
type DebugSnapshot struct {
	CollectedAt     time.Time
	System          []DebugValue
	SelectedAgent   string
	InstalledAgents []string
	DoctorProblems  []DebugProblem
	RuntimeDefaults []DebugRuntimeDefault
	CollectionError string
}

// DebugValue is one stable label/value diagnostic.
type DebugValue struct {
	Label string
	Value string
}

// DebugProblem is one actionable problem from Tao's doctor checks.
type DebugProblem struct {
	Category string
	Name     string
	Status   string
	Detail   string
}

// DebugRuntimeDefault is one resolved TAO_* runtime setting.
type DebugRuntimeDefault struct {
	Name    string
	Value   string
	Source  string
	Warning string
}

func renderDebugPage(model Model) []string {
	var lines []string
	lines = append(lines, "", debugSectionRule(model, RoleAccent, "UI"))
	appendDebugValue(&lines, "viewport", fmt.Sprintf("%dx%d", model.Width, model.Height))
	appendDebugValue(&lines, "color", model.Profile.String())
	appendDebugValue(&lines, "repository focus", debugFocusLabel(model))
	appendDebugValue(&lines, "search", displayValue(normalizedSearchQuery(model.SearchQuery)))
	appendDebugValue(&lines, "history visible", fmt.Sprintf("%t", !model.HideHistory))
	appendDebugValue(&lines, "plan rows", fmt.Sprintf("%d", len(model.Snapshot.Rows)))
	appendDebugValue(&lines, "open notes", fmt.Sprintf("%d", len(model.NoteSnapshot.Notes)))
	appendDebugValue(&lines, "repositories", fmt.Sprintf("%d active of %d registered", debugRepositoryCount(model.Snapshot, model.NoteSnapshot), len(model.SettingsSnapshot.Repositories)))
	lines = appendDebugTime(lines, "monitor collected", model.Snapshot.CollectedAt)
	lines = appendDebugTime(lines, "diagnostics collected", model.DebugSnapshot.CollectedAt)

	if len(model.DebugSnapshot.System) > 0 {
		lines = append(lines, "", debugSectionRule(model, RoleNeutral5, "SYSTEM"))
		for _, value := range model.DebugSnapshot.System {
			appendDebugValue(&lines, value.Label, value.Value)
		}
	}

	doctorProblemCount := len(model.DebugSnapshot.DoctorProblems)
	if model.DebugSnapshot.CollectionError != "" {
		doctorProblemCount++
	}
	doctorRole := RoleSuccess
	if doctorProblemCount > 0 {
		doctorRole = RoleWarn
	}
	lines = append(lines, "", debugSectionRule(model, doctorRole, "DOCTOR"))
	appendDebugValue(&lines, "selected agent", displayValue(singleLineDetail(model.DebugSnapshot.SelectedAgent)))
	agents := strings.Join(model.DebugSnapshot.InstalledAgents, ", ")
	appendDebugValue(&lines, "installed agents", displayValue(singleLineDetail(agents)))
	if model.DebugSnapshot.CollectionError != "" {
		lines = append(lines, "  ⚠ diagnostics  "+singleLineDetail(model.DebugSnapshot.CollectionError))
	}
	if len(model.DebugSnapshot.DoctorProblems) == 0 && model.DebugSnapshot.CollectionError == "" {
		lines = append(lines, "  ✓ no doctor problems")
	} else {
		for _, problem := range model.DebugSnapshot.DoctorProblems {
			label := strings.TrimSpace(problem.Category + " " + problem.Name)
			line := "  ⚠ " + displayValue(singleLineDetail(label))
			if problem.Status != "" {
				line += "  " + singleLineDetail(problem.Status)
			}
			if problem.Detail != "" {
				line += "  " + singleLineDetail(problem.Detail)
			}
			lines = append(lines, line)
		}
	}

	anomalies := debugRuntimeAnomalies(model.DebugSnapshot.RuntimeDefaults, model.SettingsSnapshot.RuntimeDefaults)
	if len(anomalies) > 0 {
		nameWidth := cells.Width("NAME")
		repositoryWidth := cells.Width("REPOSITORY")
		globalWidth := cells.Width("GLOBAL")
		sourceWidth := cells.Width("SOURCE")
		for _, anomaly := range anomalies {
			nameWidth = max(nameWidth, cells.Width(anomaly.row.Name))
			repositoryWidth = max(repositoryWidth, cells.Width(anomaly.row.Value))
			globalWidth = max(globalWidth, cells.Width(anomaly.globalValue))
			sourceWidth = max(sourceWidth, cells.Width(anomaly.row.Source))
		}
		columns := []column{
			{name: "NAME", width: nameWidth},
			{name: "REPOSITORY", width: repositoryWidth},
			{name: "GLOBAL", width: globalWidth},
			{name: "SOURCE", width: sourceWidth},
		}
		sectionWidth := dashboardSectionWidth(model, PageDebug, "RUNTIME ANOMALIES", columnsWidth(columns))
		lines = append(lines, "", dashboardSectionRuleColumns(model.Profile, RoleWarn, "RUNTIME ANOMALIES", columns, sectionWidth))
		for _, anomaly := range anomalies {
			cells := []string{
				singleLineDetail(anomaly.row.Name),
				singleLineDetail(anomaly.row.Value),
				singleLineDetail(anomaly.globalValue),
				singleLineDetail(anomaly.row.Source),
			}
			lines = append(lines, "  "+joinRow(columns, cells, columnsWidth(columns)))
			if anomaly.row.Warning != "" {
				lines = append(lines, "    warning: "+singleLineDetail(anomaly.row.Warning))
			}
		}
	}

	warnings := debugCollectorWarnings(model.Snapshot, model.NoteSnapshot)
	if len(warnings) > 0 {
		lines = append(lines, "", debugSectionRule(model, RoleWarn, "COLLECTOR WARNINGS"))
		for _, warning := range warnings {
			lines = append(lines, "  ⚠ "+warning)
		}
	}
	return lines
}

type debugRuntimeAnomaly struct {
	row         DebugRuntimeDefault
	globalValue string
}

func debugRuntimeAnomalies(rows []DebugRuntimeDefault, globalRows []SettingsRuntimeDefault) []debugRuntimeAnomaly {
	globalByName := make(map[string]SettingsRuntimeDefault, len(globalRows))
	for _, row := range globalRows {
		globalByName[row.Name] = row
	}

	var anomalies []debugRuntimeAnomaly
	for _, row := range rows {
		global, found := globalByName[row.Name]
		if found && row.Value == global.Value && row.Warning == "" {
			continue
		}
		globalValue := "(missing)"
		if found {
			globalValue = global.Value
		}
		anomalies = append(anomalies, debugRuntimeAnomaly{row: row, globalValue: globalValue})
	}
	return anomalies
}

func debugSectionRule(model Model, role Role, title string) string {
	width := dashboardSectionWidth(model, PageDebug, title, 0)
	return sectionTitleRule(model.Profile, role, title, width)
}

func appendDebugValue(lines *[]string, label, value string) {
	*lines = append(*lines, fmt.Sprintf("  %-20s %s", singleLineDetail(label), displayValue(singleLineDetail(value))))
}

func appendDebugTime(lines []string, label string, value time.Time) []string {
	rendered := "-"
	if !value.IsZero() {
		rendered = value.Format(time.RFC3339)
	}
	appendDebugValue(&lines, label, rendered)
	return lines
}

func debugFocusLabel(model Model) string {
	if model.FocusRepositoryID == "" {
		return "all"
	}
	if strings.TrimSpace(model.FocusRepositoryName) != "" {
		return model.FocusRepositoryName + " (" + model.FocusRepositoryID + ")"
	}
	return model.FocusRepositoryID
}

func debugRepositoryCount(snapshot monitor.Snapshot, notes note.Snapshot) int {
	ids := make(map[string]struct{})
	for _, row := range snapshot.Rows {
		if row.RepositoryID != "" {
			ids[row.RepositoryID] = struct{}{}
		}
	}
	for _, item := range notes.Notes {
		if item.RepositoryID != "" {
			ids[item.RepositoryID] = struct{}{}
		}
	}
	return len(ids)
}

func debugCollectorWarnings(snapshot monitor.Snapshot, notes note.Snapshot) []string {
	var warnings []string
	for _, row := range snapshot.Rows {
		for _, warning := range row.Warnings {
			warnings = append(warnings, singleLineDetail(displayValue(row.RepositoryName))+": "+singleLineDetail(warning))
		}
	}
	for _, warning := range notes.Warnings {
		warnings = append(warnings, singleLineDetail(warning.Error()))
	}
	sort.Strings(warnings)
	return warnings
}

func debugMaxOffset(model Model) int {
	frameHeight := len(renderFrame(model, PageDebug))
	bodyHeight := max(model.Height-frameHeight, 0)
	return max(len(renderDebugPage(model))-bodyHeight, 0)
}
