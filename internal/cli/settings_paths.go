package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/iamseth/tao/internal/agent/promptfmt"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptinstall"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

type settingsPathRow struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Source  string `json:"source"`
	Warning string `json:"warning,omitempty"`
}

// captureSettingsPaths freezes bootstrap facts, never settings inputs or authority.
func (a App) captureSettingsPaths(plansDir string) App {
	source := func(value, key, fallback string) string {
		if value != "" {
			return key
		}
		return fallback
	}
	home := taodata.DataHome()
	homeSource := source(os.Getenv("TAO_DATA_HOME"), "TAO_DATA_HOME", source(os.Getenv("XDG_DATA_HOME"), "XDG_DATA_HOME", "user home"))
	plansSource := "current repository"
	if plansDir != "" {
		plansSource = "--plans-dir"
	} else {
		plansDir = plan.NewFileRepository("").Dir
	}
	a.settingsPathFacts = []settingsPathRow{
		{Name: "data home", Value: home, Source: homeSource},
		{Name: "global config", Value: filepath.Join(home, "config.json"), Source: "data home"},
		{Name: "plans directory", Value: plansDir, Source: plansSource},
	}
	add := func(name, value, source string, err error) {
		row := settingsPathRow{Name: name, Value: value, Source: source}
		if err != nil {
			row.Warning = err.Error()
		}
		a.settingsPathFacts = append(a.settingsPathFacts, row)
	}
	agentDir, err := promptfmt.PiAgentDir()
	piSource := source(os.Getenv("PI_CODING_AGENT_DIR"), "PI_CODING_AGENT_DIR", "user home")
	add("Pi agent", agentDir, piSource, err)
	pi, piErr := promptinstall.ResolvePaths(runtimeconfig.AgentPi)
	add("Pi prompts", pi.PromptDir, piSource, nil)
	extensionSource := "discovery"
	if override := os.Getenv("TAO_PI_EXTENSION_DIR"); override != "" {
		if pi.ExtensionSource == override {
			extensionSource = "TAO_PI_EXTENSION_DIR (extension source)"
		} else if piErr == nil {
			piErr = fmt.Errorf("TAO_PI_EXTENSION_DIR extension source %q not usable; using discovery", override)
		}
	}
	add("Pi extension source", pi.ExtensionSource, extensionSource, piErr)
	add("Pi extension install destination", pi.ExtensionTarget, piSource, nil)
	claude, err := promptinstall.ResolvePaths(runtimeconfig.AgentClaude)
	add("Claude commands", claude.PromptDir, source(os.Getenv("TAO_CLAUDE_COMMANDS_DIR"), "TAO_CLAUDE_COMMANDS_DIR", "user home"), err)
	for i := range a.settingsPathFacts {
		row := &a.settingsPathFacts[i]
		if row.Warning == "" && row.Value != "" {
			if _, err := os.Stat(row.Value); err != nil {
				row.Warning = err.Error()
			}
		}
	}
	return a
}

func (a App) settingsPaths() []settingsPathRow {
	if a.settingsPathFacts == nil {
		a = a.captureSettingsPaths("")
	}
	return slices.Clone(a.settingsPathFacts)
}

// Diagnostic projections retain usable rows even if storage or repository
// discovery fails. Errors are displayed, never treated as execution admission.
func (a App) settingsForStatus(ctx context.Context) (App, error) {
	if a.settingsGlobal == nil {
		a = a.initializeSettings(ctx)
	}
	repository, err := a.registry().Current(ctx)
	if err != nil || repository.ID == "" {
		return a, err
	}
	repositories, err := a.registry().ListRepos()
	if err != nil {
		return a, err
	}
	for _, registered := range repositories {
		if registered.ID == repository.ID {
			return a.settingsForRepository(ctx, repository)
		}
	}
	return a, nil
}
