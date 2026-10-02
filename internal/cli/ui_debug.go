package cli

import (
	"context"
	"os"
	"runtime"
	"strings"

	"github.com/iamseth/tao/internal/build"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/tui"
)

type uiDebugCollector struct {
	app        App
	executable string
}

func (c uiDebugCollector) Collect(ctx context.Context) (tui.DebugSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tui.DebugSnapshot{}, err
	}
	snapshot := tui.DebugSnapshot{CollectedAt: c.app.now()}
	snapshot.System = append(snapshot.System,
		tui.DebugValue{Label: "version", Value: build.Version()},
		tui.DebugValue{Label: "commit", Value: build.Commit()},
		tui.DebugValue{Label: "build age", Value: build.BuildAge()},
		tui.DebugValue{Label: "go", Value: runtime.Version()},
		tui.DebugValue{Label: "platform", Value: runtime.GOOS + "/" + runtime.GOARCH},
		tui.DebugValue{Label: "executable", Value: c.executable},
	)
	if cwd, err := os.Getwd(); err == nil {
		snapshot.System = append(snapshot.System, tui.DebugValue{Label: "working directory", Value: cwd})
	} else {
		snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{Category: "system", Name: "working directory", Status: "unavailable", Detail: err.Error()})
	}

	effective, repoErr := c.app.settingsForStatus(ctx)
	rows := settingsRuntimeRows(effective.envSnapshot())
	if effective.settingsGlobal != nil && effective.settingsGlobal.LoadError != nil {
		snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{Category: "settings", Name: "global config", Status: "invalid", Detail: effective.settingsGlobal.LoadError.Error()})
	}
	if repoErr != nil {
		snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{Category: "repository", Name: "run defaults", Status: "unavailable", Detail: repoErr.Error()})
	}
	for _, row := range rows {
		snapshot.RuntimeDefaults = append(snapshot.RuntimeDefaults, tui.DebugRuntimeDefault{Name: row.Name, Value: row.Value, Source: row.Source, Warning: row.Warning})
	}

	envKeys := make(map[string]bool)
	for _, definition := range runtimeconfig.SettingDefinitions() {
		if definition.EnvKey != "" {
			envKeys[definition.Key] = true
		}
	}
	for _, row := range effective.envSnapshot().SettingsStatus() {
		if !envKeys[row.Key] {
			snapshot.RuntimeDefaults = append(snapshot.RuntimeDefaults, tui.DebugRuntimeDefault{Name: row.Key, Value: row.Value, Source: row.Source, Warning: row.Warning})
		}
		if row.GlobalValue != "" || row.RepositoryValue != "" {
			snapshot.System = append(snapshot.System, tui.DebugValue{Label: row.Key + " saved (global / repository)", Value: emptyDash(row.GlobalValue) + " / " + emptyDash(row.RepositoryValue)})
		}
	}
	for _, row := range c.app.settingsPaths() {
		snapshot.System = append(snapshot.System, tui.DebugValue{Label: row.Name, Value: row.Value + " (" + row.Source + ")"})
		if row.Warning != "" {
			snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{Category: "path", Name: row.Name, Status: "unavailable", Detail: row.Warning})
		}
	}
	report, reportErr := effective.collectDoctorReport()
	appendUIDoctorReport(&snapshot, report, reportErr)
	return snapshot, nil
}

func appendUIDoctorReport(snapshot *tui.DebugSnapshot, report doctorReport, err error) {
	if err != nil {
		snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{Category: "doctor", Name: "checks", Status: "failed", Detail: err.Error()})
		return
	}
	snapshot.SelectedAgent = string(report.selectedAgent)
	for _, descriptor := range report.agents {
		snapshot.InstalledAgents = append(snapshot.InstalledAgents, descriptor.Label)
	}
	if len(report.agents) == 0 {
		snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{Category: "agent", Name: "supported runtime", Status: "missing", Detail: "no supported agents found in PATH"})
	}
	for _, result := range report.prompts {
		if result.Status == "current" {
			continue
		}
		snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{
			Category: "prompt " + string(result.Agent), Name: result.Name, Status: result.Status, Detail: result.Path,
		})
	}
	for _, category := range report.tools {
		for _, result := range category.results {
			if result.status == "ok" {
				continue
			}
			detail := result.found
			if strings.TrimSpace(detail) == "" {
				detail = strings.Join(result.tool.executables, " or ")
			}
			snapshot.DoctorProblems = append(snapshot.DoctorProblems, tui.DebugProblem{
				Category: "tool " + category.name, Name: result.tool.name, Status: result.status, Detail: detail,
			})
		}
	}
}

func newUIDebugCollector(a App, executable string) tui.DebugSnapshotCollector {
	if a.settingsPathFacts == nil {
		a = a.captureSettingsPaths("")
	}
	return uiDebugCollector{app: a, executable: executable}
}
