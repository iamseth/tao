package cli

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	planview "github.com/iamseth/tao/internal/view"
)

var showCommand = commandMetadata{
	name:                  "show",
	minPrefix:             "s",
	usageLines:            []string{"show (s) <plan-id-or-slug>"},
	completionDescription: "Show plan details",
	long:                  "Show detailed status for a Tao plan, including repository metadata, timing, slice summaries, warnings, budget warnings, and recent events.",
	examples: "  tao show my-plan\n" +
		"  tao show 20260628-1618-kubectl-style-help",
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.show(c.ctx, c.repo, c.args)
	},
}

func (a App) show(ctx context.Context, repo plan.Repository, args []string) error {
	if err := requirePositionals(args, 1, "usage: tao show <plan-id-or-slug>"); err != nil {
		return err
	}
	loaded, err := planview.LoadPlan(ctx, repo, args[0], planview.Options{})
	if err != nil {
		return err
	}
	return renderPlanDetailWithThresholds(a.Out, loaded, runtimeconfig.RuntimeAgentBudgetThresholds())
}

func renderPlanDetail(out io.Writer, loaded planview.Plan) error {
	return renderPlanDetailWithThresholds(out, loaded, plan.DefaultAgentBudgetThresholds())
}

func renderPlanDetailWithThresholds(out io.Writer, loaded planview.Plan, thresholds plan.AgentBudgetThresholds) error {
	detail := loaded.Detail
	derived := loaded.Derived
	now := loaded.Now
	state := detail.State
	if err := writef(out, "%s\n", state.Plan.Title); err != nil {
		return err
	}
	if err := writef(out, "ID: %s\n", state.Plan.ID); err != nil {
		return err
	}
	if err := writef(out, "Status: %s\n", colorStatus(state.Status, state.Status)); err != nil {
		return err
	}
	if err := writef(out, "Repo: %s %s\n", state.Repo.Name, state.Repo.Branch); err != nil {
		return err
	}
	if err := writef(out, "Started: %s\n", plan.FormatHumanTime(state.Plan.Timing.StartedAt, now)); err != nil {
		return err
	}
	if err := writef(out, "Completed: %s\n", plan.FormatHumanTime(state.Plan.Timing.CompletedAt, now)); err != nil {
		return err
	}
	if err := writef(out, "Elapsed: %s\n", plan.FormatDuration(derived.Elapsed)); err != nil {
		return err
	}
	if err := writef(out, "Last Activity: %s\n", plan.FormatHumanTime(state.Plan.Timing.LastActivityAt, now)); err != nil {
		return err
	}
	if len(detail.Warnings) > 0 {
		if err := writeln(out, "Warnings:"); err != nil {
			return err
		}
		for _, warning := range detail.Warnings {
			if err := writef(out, "- %s\n", warning); err != nil {
				return err
			}
		}
	}
	if err := planview.RenderAgentBudgetWarnings(out, plan.AgentBudgetWarnings(detail, thresholds)); err != nil {
		return err
	}
	if stats := detail.PlanningSession.Stats; stats != nil {
		planningSummary := plan.SummarizePlanningSessionMetrics(stats, state.CreatedAt)
		if err := writeln(out, "\nPlanning Session:"); err != nil {
			return err
		}
		if !planningSummary.Valid {
			reason := planningSummary.UnavailableReason
			if reason == "" {
				reason = "planning metrics unavailable"
			}
			return writef(out, "Unavailable: %s\n", reason)
		}
		if err := writef(out, "Duration: %s\n", plan.FormatDuration(planningSummary.Duration)); err != nil {
			return err
		}
		if err := writef(out, "Tokens: %d\n", stats.TotalTokens); err != nil {
			return err
		}
		if err := writef(out, "Messages: %d\n", stats.TotalMessages); err != nil {
			return err
		}
		if err := writef(out, "Model: %s\n", planview.Empty(stats.ModelID)); err != nil {
			return err
		}
		if err := writef(out, "Provider: %s\n", planview.Empty(stats.ProviderID)); err != nil {
			return err
		}
		if err := writef(out, "Cost: $%.4f\n", stats.Cost); err != nil {
			return err
		}
		if err := writef(out, "Session ID: %s\n", planview.Empty(stats.SessionID)); err != nil {
			return err
		}
	}

	if err := writeln(out, "\nSlices:"); err != nil {
		return err
	}
	for i, slice := range detail.Slices.Slices {
		if i > 0 {
			if err := writeln(out, ""); err != nil {
				return err
			}
		}
		if err := renderShowSlice(out, slice, now); err != nil {
			return err
		}
	}

	if len(detail.Events) > 0 {
		if err := writeln(out, "\nRecent Events:"); err != nil {
			return err
		}
		start := max(len(detail.Events)-5, 0)
		for _, event := range detail.Events[start:] {
			if err := writef(out, "- %s %s %s\n", event.Timestamp.Format(time.RFC3339), event.Type, event.Message); err != nil {
				return err
			}
		}
	}

	return nil
}

func renderShowSlice(out io.Writer, slice plan.Slice, now time.Time) error {
	const labelWidth = len("Completed:")
	const summaryWidth = 72

	if err := writef(out, "%s  %s  %s\n", colorStatus(slice.Status, slice.Status), slice.ID, planview.Empty(slice.Title)); err != nil {
		return err
	}
	fields := []struct {
		label string
		value string
	}{
		{label: "Duration", value: plan.FormatDuration(plan.SliceDuration(slice, now))},
		{label: "Started", value: plan.FormatHumanTime(slice.Timing.StartedAt, now)},
		{label: "Completed", value: plan.FormatHumanTime(slice.Timing.CompletedAt, now)},
	}
	for _, field := range fields {
		if err := writef(out, "  %-*s %s\n", labelWidth, field.label+":", field.value); err != nil {
			return err
		}
	}

	lines := wrapText(planview.Empty(slice.Goal), summaryWidth)
	if err := writef(out, "  %-*s %s\n", labelWidth, "Summary:", lines[0]); err != nil {
		return err
	}
	indent := "  " + strings.Repeat(" ", labelWidth) + " "
	for _, line := range lines[1:] {
		if err := writef(out, "%s%s\n", indent, line); err != nil {
			return err
		}
	}
	return nil
}
