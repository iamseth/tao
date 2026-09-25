package view

import (
	"fmt"
	"io"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

// RenderShowTelemetry uses the same bounded projection as JSON inspection.
func RenderShowTelemetry(out io.Writer, telemetry ShowTelemetry) error {
	if _, err := fmt.Fprintln(out, "\nAgent telemetry (recorded usage):"); err != nil {
		return err
	}
	if err := renderShowTelemetryTotals(out, "Total", telemetry.Totals); err != nil {
		return err
	}
	for _, group := range telemetry.ByRole {
		if err := renderShowTelemetryTotals(out, string(group.Role.Normalized()), group.Totals); err != nil {
			return err
		}
	}
	for _, limitation := range telemetry.Limitations {
		if _, err := fmt.Fprintln(out, "- "+limitation); err != nil {
			return err
		}
	}
	return nil
}

func renderShowTelemetryTotals(out io.Writer, label string, t ShowTelemetryTotals) error {
	coverage := "reported"
	if t.PartialRecordedTotals {
		coverage = "partial recorded totals"
	}
	if t.Attempts == 0 {
		coverage = "unavailable/unknown"
	}
	cost := "unavailable/unknown"
	if t.Cost != nil {
		cost = fmt.Sprintf("$%.4f", *t.Cost)
	}
	_, err := fmt.Fprintf(out, "%s: %s; sessions %d; attempts %d; failed %d\n"+
		"  Tokens: %s; input %s; output %s; reasoning %s; cache read %s; cache write %s; cost %s\n"+
		"  Availability: reported %d; partial %d; unavailable %d; unknown/legacy %d\n",
		label, coverage, t.Sessions, t.Attempts, t.FailedAttempts,
		showTokenText(t.TotalTokens), showTokenText(t.InputTokens), showTokenText(t.OutputTokens),
		showTokenText(t.ReasoningTokens), showTokenText(t.CacheReadTokens), showTokenText(t.CacheWriteTokens), cost,
		t.Availability.Reported, t.Availability.Partial, t.Availability.Unavailable, t.Availability.Unknown)
	return err
}

func showTokenText(value *int64) string {
	if value == nil {
		return "unavailable/unknown"
	}
	return fmt.Sprint(*value)
}

func Empty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func ShortPlanID(id string) string {
	parts := strings.SplitN(id, "-", 3)
	if len(parts) < 2 {
		return id
	}
	return parts[0] + "-" + parts[1]
}

func DoneLabel(summary plan.PlanSummary) string {
	return fmt.Sprintf("%d/%d", summary.CompletedCount, summary.TotalCount)
}

// RenderShowRework writes the rework section shared by human-readable plan
// inspection. Empty facts remain explicit rather than hiding the section.
func RenderShowRework(out io.Writer, rework ShowRework) error {
	if _, err := fmt.Fprintf(out, "\nRework:\nRounds: %d\n", rework.Rounds); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Current stop: %s\n", Empty(rework.CurrentStopClassification)); err != nil {
		return err
	}
	if len(rework.RecurringFiles) == 0 {
		_, err := fmt.Fprintln(out, "Recurring files: -")
		return err
	}
	if _, err := fmt.Fprintln(out, "Recurring files:"); err != nil {
		return err
	}
	for _, file := range rework.RecurringFiles {
		if _, err := fmt.Fprintf(out, "- %s\n", file); err != nil {
			return err
		}
	}
	return nil
}
