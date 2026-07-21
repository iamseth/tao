package view

import (
	"fmt"
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
