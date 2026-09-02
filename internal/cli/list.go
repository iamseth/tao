package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
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
	headers := []string{"STATUS", "PLAN ID", "PLAN", "DONE", "UPDATED", "ABANDONED", "REASON"}
	rows := make([][]string, 0, len(summaries))
	for _, summary := range summaries {
		rows = append(rows, planListRow(summary, now))
	}
	widths := cells.ColumnWidths(headers, rows)
	if err := writef(out, "%s  %s  %s  %s  %s  %s  %s\n",
		cells.Pad(headers[0], widths[0]),
		cells.Pad(headers[1], widths[1]),
		cells.Pad(headers[2], widths[2]),
		cells.Pad(headers[3], widths[3]),
		cells.Pad(headers[4], widths[4]),
		cells.Pad(headers[5], widths[5]),
		cells.Pad(headers[6], widths[6]),
	); err != nil {
		return err
	}
	for _, summary := range summaries {
		row := planListRow(summary, now)
		if err := writef(out, "%s  %s  %s  %s  %s  %s  %s\n",
			colorStatus(cells.Pad(row[0], widths[0]), summary.Status),
			cells.Pad(row[1], widths[1]),
			cells.Pad(row[2], widths[2]),
			colorDone(cells.Pad(row[3], widths[3]), summary.CompletedCount, summary.TotalCount),
			cells.Pad(row[4], widths[4]),
			cells.Pad(row[5], widths[5]),
			cells.Pad(row[6], widths[6]),
		); err != nil {
			return err
		}
	}
	return nil
}

func planListRow(summary plan.PlanSummary, now time.Time) []string {
	abandonedAt, reason := "-", "-"
	if summary.Status == plan.StatusAbandoned && summary.Abandonment != nil {
		if !summary.Abandonment.AbandonedAt.IsZero() {
			at := summary.Abandonment.AbandonedAt
			abandonedAt = plan.FormatHumanTime(&at, now)
		}
		reason = planview.FormatAbandonmentText(summary.Abandonment.Reason)
	}
	return []string{
		summary.Status,
		planview.ShortPlanID(summary.ID),
		listPlanLabel(summary),
		planview.DoneLabel(summary),
		plan.FormatHumanTime(summary.LastActivityAt, now),
		abandonedAt,
		reason,
	}
}
