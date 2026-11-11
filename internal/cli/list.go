package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
	planview "github.com/iamseth/tao/internal/view"
)

var listCommand = commandMetadata{
	name:                  "list",
	minPrefix:             "l",
	usageLines:            []string{"list (l) [--active] [--limit N] [--json]"},
	completionDescription: "List plans",
	long:                  "List Tao plans for the selected repository. By default Tao shows the most recent plans, with flags to focus on active work or adjust the limit. Use --json for a read-only JSON array.",
	examples: "  tao list\n" +
		"  tao list --active\n" +
		"  tao list --limit 0\n" +
		"  tao list --json",
	registerFlags: registerListFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.list(c.ctx, c.repo, c.args)
	},
}

func registerListFlags(fs *flag.FlagSet) {
	fs.Bool("active", false, "show only active plans")
	fs.Bool("json", false, "output plans as a JSON array")
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

	if flagBoolValue(fs, "json") {
		rows := make([]planListJSONRow, 0, len(summaries))
		for _, summary := range summaries {
			rows = append(rows, projectPlanListJSON(summary))
		}
		return json.NewEncoder(a.Out).Encode(rows)
	}
	return a.renderPlanList(a.Out, summaries, a.now())
}

type planListJSONRow struct {
	ID       string        `json:"id"`
	Slug     string        `json:"slug"`
	Status   string        `json:"status"`
	Parking  *plan.Parking `json:"parking,omitempty"`
	Progress struct {
		Completed int `json:"completed"`
		Total     int `json:"total"`
	} `json:"progress"`
	UpdatedAt   *time.Time                `json:"updated_at"`
	Abandonment *planview.ShowAbandonment `json:"abandonment,omitempty"`
	Warnings    []string                  `json:"warnings"`
	NextAction  *plan.PlanNextAction      `json:"next_action,omitempty"`
}

func projectPlanListJSON(summary plan.PlanSummary) planListJSONRow {
	row := planListJSONRow{ID: summary.ID, Slug: listPlanLabel(summary), Status: summary.Status, UpdatedAt: summary.LastActivityAt, Warnings: append([]string{}, summary.Warnings...)}
	if summary.Parking != nil {
		marker := *summary.Parking
		marker.Reason = planview.FormatAbandonmentText(marker.Reason)
		row.Parking = &marker
	}
	row.Progress.Completed, row.Progress.Total = summary.CompletedCount, summary.TotalCount
	if summary.NextAction.Primary.Kind != "" {
		next := summary.NextAction
		next.Alternatives = append([]plan.PlanAction{}, next.Alternatives...)
		if summary.Status == plan.StatusAbandoned {
			next.Primary.Reason = "the plan was abandoned"
		}
		row.NextAction = &next
	}
	if summary.Status == plan.StatusAbandoned && summary.Abandonment != nil {
		reason := strings.Join(strings.FieldsFunc(summary.Abandonment.Reason, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
		if reason == "" {
			reason = "No abandonment reason was recorded."
		}
		row.Abandonment = &planview.ShowAbandonment{Reason: reason}
		if !summary.Abandonment.AbandonedAt.IsZero() {
			at := summary.Abandonment.AbandonedAt.UTC()
			row.Abandonment.AbandonedAt = &at
		}
	}
	return row
}

func (a App) renderPlanList(out io.Writer, summaries []plan.PlanSummary, now time.Time) error {
	palette := a.outputPalette(out)
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
			colorStatus(palette, cells.Pad(row[0], widths[0]), summary.Status),
			cells.Pad(row[1], widths[1]),
			cells.Pad(row[2], widths[2]),
			colorDone(palette, cells.Pad(row[3], widths[3]), summary.CompletedCount, summary.TotalCount),
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
	status := summary.Status
	if summary.Parking != nil {
		status += " (parked)"
		reason = planview.FormatAbandonmentText(summary.Parking.Reason)
	}
	return []string{
		status,
		planview.ShortPlanID(summary.ID),
		listPlanLabel(summary),
		planview.DoneLabel(summary),
		plan.FormatHumanTime(summary.LastActivityAt, now),
		abandonedAt,
		reason,
	}
}
