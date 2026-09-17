package view

import (
	"fmt"
	"io"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

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
