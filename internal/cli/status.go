package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

var statusCommand = commandMetadata{
	name:                  "status",
	minPrefix:             "st",
	usageLines:            []string{"status (st) [--json]"},
	completionDescription: "Show runtime defaults and plan rollups",
	long:                  "Show Tao runtime defaults and current repository plan rollups. Use --json for automation.",
	examples: "  tao status\n" +
		"  tao status --json",
	registerFlags: registerStatusFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.status(c.ctx, c.repo, c.args)
	},
}

type statusPayload struct {
	RuntimeEnv  []runtimeconfig.EnvVarStatus  `json:"runtime_env"`
	Plans       plan.PlanRollup               `json:"plans"`
	Settings    []runtimeconfig.SettingStatus `json:"settings"`
	Paths       []settingsPathRow             `json:"paths"`
	Diagnostics []string                      `json:"diagnostics,omitempty"`
}

func registerStatusFlags(fs *flag.FlagSet) {
	fs.Bool("json", false, "write JSON")
}

func (a App) status(ctx context.Context, repo planLister, args []string) error {
	fs, positional, err := a.parseArgs("status", args, registerStatusFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao status [--json]"); err != nil {
		return err
	}
	effective, settingsErr := a.settingsForStatus(ctx)
	payload := statusPayload{RuntimeEnv: settingsRuntimeRows(effective.envSnapshot()), Settings: effective.envSnapshot().SettingsStatus(), Paths: a.settingsPaths(), Plans: statusPlanRollup(ctx, repo)}
	if settingsErr != nil {
		payload.Diagnostics = append(payload.Diagnostics, settingsErr.Error())
	}
	if a.settingsGlobal != nil && a.settingsGlobal.LoadError != nil {
		payload.Diagnostics = append(payload.Diagnostics, a.settingsGlobal.LoadError.Error())
	}
	if flagBoolValue(fs, "json") {
		encoder := json.NewEncoder(a.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	return a.writeStatus(payload)
}

// settingsRuntimeRows preserves the runtime_env compatibility shape while
// retaining admission diagnostics from every saved layer.
func settingsRuntimeRows(snapshot runtimeconfig.EnvSnapshot) []runtimeconfig.EnvVarStatus {
	rows := snapshot.Status()
	settings := make(map[string]runtimeconfig.SettingStatus)
	for _, row := range snapshot.SettingsStatus() {
		settings[row.Key] = row
	}
	byEnv := make(map[string]runtimeconfig.SettingStatus)
	for _, definition := range runtimeconfig.SettingDefinitions() {
		if definition.EnvKey != "" {
			byEnv[definition.EnvKey] = settings[definition.Key]
		}
	}
	for i := range rows {
		if row, ok := byEnv[rows[i].Name]; ok {
			rows[i].Value, rows[i].Source, rows[i].Warning = row.Value, row.Source, row.Warning
		}
	}
	return rows
}

// applyRepositoryRunDefaultsToStatus is a legacy same-package adapter. Effective
// presentation uses the scoped resolver instead.
func applyRepositoryRunDefaultsToStatus(rows []runtimeconfig.EnvVarStatus, repository runtimeconfig.RunOptionsPatch) []runtimeconfig.EnvVarStatus {
	values := map[string]string{
		runtimeconfig.EnvReviewAgent:           repository.ReviewAgent.String(),
		runtimeconfig.EnvModel:                 repository.Base,
		runtimeconfig.EnvEffort:                repository.Effort,
		runtimeconfig.EnvRunEffort:             repository.RunEffort,
		runtimeconfig.EnvReviewEffort:          repository.ReviewEffort,
		runtimeconfig.EnvMergeReviewEffort:     repository.MergeReviewEffort,
		runtimeconfig.EnvResolverEffort:        repository.ResolverEffort,
		runtimeconfig.EnvRunModel:              repository.Run,
		runtimeconfig.EnvReviewModel:           repository.Review,
		runtimeconfig.EnvMergeReviewModel:      repository.MergeReview,
		runtimeconfig.EnvResolverModel:         repository.Resolver,
		runtimeconfig.EnvReworkEscalationModel: repository.ReworkEscalation,
	}
	if repository.PullRequest != nil {
		values[runtimeconfig.EnvPullRequest] = fmt.Sprintf("%t", *repository.PullRequest)
	}
	for i := range rows {
		if value := values[rows[i].Name]; value != "" {
			rows[i].Value = value
			rows[i].Source = "repository"
		}
	}
	return rows
}

func statusPlanRollup(ctx context.Context, repo planLister) plan.PlanRollup {
	if repo == nil {
		return plan.SummarizePlans(nil)
	}
	summaries, err := repo.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return plan.SummarizePlans(nil)
	}
	return plan.SummarizePlans(summaries)
}

func (a App) writeStatus(payload statusPayload) error {
	if err := writeln(a.Out, "Runtime defaults:"); err != nil {
		return err
	}
	if err := writeln(a.Out, "Precedence: built-in → global → repository → environment → flags"); err != nil {
		return err
	}
	byEnv := make(map[string]runtimeconfig.SettingStatus)
	byKey := make(map[string]runtimeconfig.SettingStatus)
	for _, row := range payload.Settings {
		byKey[row.Key] = row
	}
	for _, definition := range runtimeconfig.SettingDefinitions() {
		if definition.EnvKey != "" {
			byEnv[definition.EnvKey] = byKey[definition.Key]
			delete(byKey, definition.Key)
		}
	}
	width := len("TAO_DANGEROUSLY_SKIP_PERMISSIONS")
	for _, row := range payload.RuntimeEnv {
		if len(row.Name) > width {
			width = len(row.Name)
		}
		value := emptyDash(row.Value)
		if row.Source == "invalid" {
			value = "(rejected on consumption)"
		}
		if err := writef(a.Out, "  %-*s  %-8s  %s\n", width, row.Name, value, row.Source); err != nil {
			return err
		}
		if setting, ok := byEnv[row.Name]; ok {
			if err := a.writeSavedSetting(setting); err != nil {
				return err
			}
		}
		if commands := runtimeconfig.ApplicableCommands(row.Name); len(commands) > 0 {
			if err := writef(a.Out, "    applies to: %s\n", strings.Join(commands, ", ")); err != nil {
				return err
			}
		}
		if row.Warning != "" {
			if err := writef(a.Out, "    warning: %s\n", row.Warning); err != nil {
				return err
			}
		}
	}
	if err := writeln(a.Out, ""); err != nil {
		return err
	}
	for _, row := range payload.Settings {
		if _, ok := byKey[row.Key]; !ok {
			continue
		}
		if err := writef(a.Out, "  %s  %s  %s\n", row.Key, emptyDash(row.Value), row.Source); err != nil {
			return err
		}
		if err := a.writeSavedSetting(row); err != nil {
			return err
		}
		if row.Warning != "" {
			if err := writef(a.Out, "    warning: %s\n", row.Warning); err != nil {
				return err
			}
		}
	}
	for _, diagnostic := range payload.Diagnostics {
		if err := writef(a.Out, "warning: %s\n", diagnostic); err != nil {
			return err
		}
	}
	if err := writeln(a.Out, "Paths (read-only):"); err != nil {
		return err
	}
	for _, row := range payload.Paths {
		if err := writef(a.Out, "  %s: %s (%s)\n", row.Name, emptyDash(row.Value), row.Source); err != nil {
			return err
		}
		if row.Warning != "" {
			if err := writef(a.Out, "    warning: %s\n", row.Warning); err != nil {
				return err
			}
		}
	}
	return a.writePlanRollup(payload.Plans)
}

func (a App) writeSavedSetting(row runtimeconfig.SettingStatus) error {
	if row.GlobalValue == "" && row.RepositoryValue == "" {
		return nil
	}
	return writef(a.Out, "    saved global=%s repository=%s (higher precedence values mask saved values)\n", emptyDash(row.GlobalValue), emptyDash(row.RepositoryValue))
}

func (a App) writePlanRollup(rollup plan.PlanRollup) error {
	if err := writeln(a.Out, "Plans:"); err != nil {
		return err
	}
	if err := writef(a.Out, "  total      %d\n", rollup.Total); err != nil {
		return err
	}
	if err := writef(a.Out, "  statuses   %d planned, %d in_progress, %d in_review, %d changes_requested, %d reviewed, %d completed, %d abandoned, %d blocked\n", rollup.Statuses.Planned, rollup.Statuses.InProgress, rollup.Statuses.InReview, rollup.Statuses.ChangesRequested, rollup.Statuses.Reviewed, rollup.Statuses.Completed, rollup.Statuses.Abandoned, rollup.Statuses.Blocked); err != nil {
		return err
	}
	if err := writef(a.Out, "  done       %d complete, %d reviewed\n", rollup.Completed, rollup.Reviewed); err != nil {
		return err
	}
	if err := writef(a.Out, "  abandoned  %d\n", rollup.Abandoned); err != nil {
		return err
	}
	if len(rollup.Verdicts) == 0 {
		return writeln(a.Out, "  verdicts   -")
	}
	return writef(a.Out, "  verdicts   %s\n", formatVerdictCounts(rollup.Verdicts))
}

func formatVerdictCounts(verdicts map[string]int) string {
	keys := make([]string, 0, len(verdicts))
	for key := range verdicts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, verdicts[key]))
	}
	return strings.Join(parts, ", ")
}
