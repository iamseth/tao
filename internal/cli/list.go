package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/iamseth/tao/internal/plan"
	planview "github.com/iamseth/tao/internal/view"
)

var listCommand = commandMetadata{
	name:                  "list",
	minPrefix:             "l",
	usageLines:            []string{"list (l) [--active] [--limit N]"},
	completionDescription: "List plans",
	long:                  "List Tao plans for the selected repository. By default Tao shows the most recent plans, with flags to focus on active work or adjust the limit.",
	examples: "  tao list\n" +
		"  tao list --active\n" +
		"  tao list --limit 0",
	registerFlags: registerListFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.list(c.ctx, c.repo, c.args)
	},
}

func registerListFlags(fs *flag.FlagSet) {
	fs.Bool("active", false, "show only active plans")
	fs.Int("limit", 15, "maximum number of plans to show; use 0 for all")
}

func (a App) list(ctx context.Context, repo planLister, args []string) error {
	fs, _, err := a.parseArgs("list", args, registerListFlags)
	if err != nil {
		return err
	}
	limit := flagIntValue(fs, "limit")
	if limit < 0 {
		return errors.New("--limit must be 0 or greater")
	}

	summaries, err := repo.ListPlans(ctx, plan.PlanFilter{ActiveOnly: flagBoolValue(fs, "active")})
	if err != nil {
		return err
	}
	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}

	return renderPlanList(a.Out, summaries, a.now())
}

func renderPlanList(out io.Writer, summaries []plan.PlanSummary, now time.Time) error {
	widths := listWidths(summaries, now)
	if err := writef(out, "%s  %s  %s  %s  %s\n",
		pad("STATUS", widths.status),
		pad("PLAN ID", widths.planID),
		pad("PLAN", widths.plan),
		pad("DONE", widths.done),
		pad("UPDATED", widths.updated),
	); err != nil {
		return err
	}
	for _, summary := range summaries {
		done := planview.DoneLabel(summary)
		updated := plan.FormatHumanTime(summary.LastActivityAt, now)
		if err := writef(out, "%s  %s  %s  %s  %s\n",
			colorStatus(pad(summary.Status, widths.status), summary.Status),
			pad(planview.ShortPlanID(summary.ID), widths.planID),
			pad(listPlanLabel(summary), widths.plan),
			colorDone(pad(done, widths.done), summary.CompletedCount, summary.TotalCount),
			pad(updated, widths.updated),
		); err != nil {
			return err
		}
	}
	return nil
}
