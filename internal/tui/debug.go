package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
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
	lines = append(lines, "", "UI")
	appendDebugValue(&lines, "viewport", fmt.Sprintf("%dx%d", model.Width, model.Height))
	appendDebugValue(&lines, "color", fmt.Sprintf("%t", model.UseColor))
	appendDebugValue(&lines, "repository focus", debugFocusLabel(model))
	appendDebugValue(&lines, "search", displayValue(normalizedSearchQuery(model.SearchQuery)))
	appendDebugValue(&lines, "completed visible", fmt.Sprintf("%t", !model.HideCompleted))
	appendDebugValue(&lines, "plan rows", fmt.Sprintf("%d", len(model.Snapshot.Rows)))
	appendDebugValue(&lines, "open notes", fmt.Sprintf("%d", len(model.NoteSnapshot.Notes)))
	appendDebugValue(&lines, "repositories", fmt.Sprintf("%d", debugRepositoryCount(model.Snapshot, model.NoteSnapshot)))
	lines = appendDebugTime(lines, "monitor collected", model.Snapshot.CollectedAt)
	lines = appendDebugTime(lines, "diagnostics collected", model.DebugSnapshot.CollectedAt)

	if len(model.DebugSnapshot.System) > 0 {
		lines = append(lines, "", "SYSTEM")
		for _, value := range model.DebugSnapshot.System {
			appendDebugValue(&lines, value.Label, value.Value)
		}
	}

	lines = append(lines, "", "DOCTOR")
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

	lines = append(lines, "", "RUNTIME DEFAULTS")
	if len(model.DebugSnapshot.RuntimeDefaults) == 0 {
		lines = append(lines, "  Runtime defaults unavailable.")
	} else {
		nameWidth := utf8.RuneCountInString("NAME")
		valueWidth := utf8.RuneCountInString("VALUE")
		for _, row := range model.DebugSnapshot.RuntimeDefaults {
			nameWidth = max(nameWidth, utf8.RuneCountInString(row.Name))
			valueWidth = max(valueWidth, utf8.RuneCountInString(row.Value))
		}
		lines = append(lines, "  "+padRunes("NAME", nameWidth)+"  "+padRunes("VALUE", valueWidth)+"  SOURCE")
		for _, row := range model.DebugSnapshot.RuntimeDefaults {
			lines = append(lines, "  "+padRunes(singleLineDetail(row.Name), nameWidth)+"  "+padRunes(singleLineDetail(row.Value), valueWidth)+"  "+singleLineDetail(row.Source))
			if row.Warning != "" {
				lines = append(lines, "    warning: "+singleLineDetail(row.Warning))
			}
		}
	}

	warnings := debugCollectorWarnings(model.Snapshot, model.NoteSnapshot)
	if len(warnings) > 0 {
		lines = append(lines, "", "COLLECTOR WARNINGS")
		for _, warning := range warnings {
			lines = append(lines, "  ⚠ "+warning)
		}
	}
	return lines
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
	bodyHeight := max(model.Height-1, 0)
	return max(len(renderDebugPage(model))-bodyHeight, 0)
}
