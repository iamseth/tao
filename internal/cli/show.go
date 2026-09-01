package cli

import (
	"context"
	"encoding/json"
	"flag"
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
	usageLines:            []string{"show (s) [--json] <plan-id-or-slug>"},
	completionDescription: "Show plan details and the recommended next action",
	long:                  "Show detailed status for a Tao plan and the single safest recommended next action. Use --json for an explicit structured projection.",
	examples: "  tao show my-plan\n" +
		"  tao show --json 20260628-1618-kubectl-style-help",
	registerFlags: registerShowFlags,
	completion: completionContext{
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.show(c.ctx, c.repo, c.args)
	},
}

func registerShowFlags(fs *flag.FlagSet) {
	fs.Bool("json", false, "write JSON")
}

func (a App) show(ctx context.Context, repo plan.Repository, args []string) error {
	fs, positional, err := a.parseArgs("show", args, registerShowFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao show [--json] <plan-id-or-slug>"); err != nil {
		return err
	}
	loaded, err := planview.LoadPlan(ctx, repo, positional[0], planview.Options{})
	if err != nil {
		return err
	}
	if flagBoolValue(fs, "json") {
		encoder := json.NewEncoder(a.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(loaded.ShowPayload())
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
	lifecycleStatus := plan.PlanLifecycleStatus(detail)
	useColor := outputSupportsColor(out)
	if err := writef(out, "%s\n", state.Plan.Title); err != nil {
		return err
	}
	if err := writef(out, "ID: %s\n", state.Plan.ID); err != nil {
		return err
	}
	statusText := lifecycleStatus
	if lifecycleStatus == plan.StatusBlocked {
		statusText += " (waiting for outside action)"
	}
	if useColor {
		statusText = colorStatus(statusText, lifecycleStatus)
	}
	if err := writef(out, "Status: %s\n", statusText); err != nil {
		return err
	}
	if abandonment := loaded.ShowPayload().Abandonment; abandonment != nil {
		abandonedAt := "-"
		if abandonment.AbandonedAt != nil {
			abandonedAt = abandonment.AbandonedAt.Format(time.RFC3339)
		}
		if err := writef(out, "Abandoned: %s\nAbandonment reason: %s\n", abandonedAt, abandonment.Reason); err != nil {
			return err
		}
	}
	if err := renderNextAction(out, loaded.DisplayNextAction()); err != nil {
		return err
	}
	if recovery := derived.FinalizationRecovery; recovery != nil {
		if err := writef(out, "Finalization failure: %s (%s)\n", recovery.Phase, recovery.Category); err != nil {
			return err
		}
		if err := writef(out, "Failed at: %s\n", recovery.FailedAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
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
		if err := renderShowSlice(out, slice, now, useColor); err != nil {
			return err
		}
	}

	if len(detail.Events) > 0 {
		if err := writeln(out, "\nRecent Events:"); err != nil {
			return err
		}
		start := max(len(detail.Events)-5, 0)
		for _, event := range detail.Events[start:] {
			message := event.Message
			switch event.Type {
			case plan.EventTypeSliceBlocked:
				message = planview.FormatBlockerText(event.Reason).Concise
			case plan.EventTypeFinalizationFailed, plan.EventTypeFinalizationFailureCleared:
				if recovery := plan.FinalizationRecoveryFromFailure(event.FinalizationFailure); recovery != nil {
					message = string(recovery.Phase) + " " + recovery.Category + "; recovery: " + recovery.RecoveryAction
				}
			}
			if err := writef(out, "- %s %s %s\n", event.Timestamp.Format(time.RFC3339), event.Type, message); err != nil {
				return err
			}
		}
	}

	return nil
}

func renderNextAction(out io.Writer, next plan.PlanNextAction) error {
	if err := renderPrimaryNextAction(out, next); err != nil {
		return err
	}
	for _, alternative := range next.Alternatives {
		if err := writef(out, "  Alternative (%s): %s — %s\n", alternative.Class, alternative.Command, alternative.Reason); err != nil {
			return err
		}
	}
	return nil
}

// renderPrimaryNextAction keeps ordinary command guidance aligned with the
// shared read-only lifecycle projection without promoting administrative
// alternatives alongside the safest recommendation.
func renderPrimaryNextAction(out io.Writer, next plan.PlanNextAction) error {
	guidance := next.Primary.Command
	if guidance == "" {
		guidance = next.Primary.Instruction
	}
	if guidance == "" {
		guidance = "No action"
	}
	if err := writef(out, "Next: %s\n", guidance); err != nil {
		return err
	}
	return writef(out, "Reason: %s\n", next.Primary.Reason)
}

func renderShowSlice(out io.Writer, slice plan.Slice, now time.Time, useColor bool) error {
	const labelWidth = len("Completed:")
	const summaryWidth = 72

	statusText := slice.Status
	if useColor {
		statusText = colorStatus(statusText, slice.Status)
	}
	if err := writef(out, "%s  %s  %s\n", statusText, slice.ID, planview.Empty(slice.Title)); err != nil {
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
	if slice.Status == plan.StatusBlocked {
		blockerLines := wrapText(planview.FormatBlockerText(slice.BlockerNote).Detailed, summaryWidth)
		if err := writef(out, "  Blocker Reason: %s\n", blockerLines[0]); err != nil {
			return err
		}
		blockerIndent := strings.Repeat(" ", len("  Blocker Reason: "))
		for _, line := range blockerLines[1:] {
			if err := writef(out, "%s%s\n", blockerIndent, line); err != nil {
				return err
			}
		}
	}
	return nil
}
