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
	RuntimeEnv []runtimeconfig.EnvVarStatus `json:"runtime_env"`
	Plans      plan.PlanRollup              `json:"plans"`
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
	env, err := runtimeconfig.RuntimeEnvStatus()
	if err != nil {
		return err
	}
	payload := statusPayload{RuntimeEnv: env, Plans: statusPlanRollup(ctx, repo)}
	if flagBoolValue(fs, "json") {
		encoder := json.NewEncoder(a.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	return a.writeStatus(payload)
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
	width := len("TAO_DANGEROUSLY_SKIP_PERMISSIONS")
	for _, row := range payload.RuntimeEnv {
		if len(row.Name) > width {
			width = len(row.Name)
		}
		if err := writef(a.Out, "  %-*s  %-8s  %s\n", width, row.Name, row.Value, row.Source); err != nil {
			return err
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
	return a.writePlanRollup(payload.Plans)
}

func (a App) writePlanRollup(rollup plan.PlanRollup) error {
	if err := writeln(a.Out, "Plans:"); err != nil {
		return err
	}
	if err := writef(a.Out, "  total      %d\n", rollup.Total); err != nil {
		return err
	}
	if err := writef(a.Out, "  statuses   %d planned, %d in_progress, %d in_review, %d changes_requested, %d reviewed, %d completed, %d blocked\n", rollup.Statuses.Planned, rollup.Statuses.InProgress, rollup.Statuses.InReview, rollup.Statuses.ChangesRequested, rollup.Statuses.Reviewed, rollup.Statuses.Completed, rollup.Statuses.Blocked); err != nil {
		return err
	}
	if err := writef(a.Out, "  done       %d complete, %d reviewed\n", rollup.Completed, rollup.Reviewed); err != nil {
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
