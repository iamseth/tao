package tui

import (
	"strings"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/plan"
)

// Section is a page's table of monitor rows.
type Section struct {
	Title string
	Rows  []monitor.Row
}

// filterRows preserves order without mutating the collector snapshot.
func filterRows(rows []monitor.Row, filter Filter) []monitor.Row {
	filtered := make([]monitor.Row, 0, len(rows))
	for _, row := range rows {
		if filter.MatchesRow(row) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func planNextAction(row monitor.Row) string {
	if row.MergeInProgress {
		return "MERGING"
	}
	switch row.RecommendedAction.Kind {
	case plan.PlanActionRestartMerge:
		return "RESTART MERGE"
	case plan.PlanActionRecoverMerge:
		if strings.TrimSpace(row.RecommendedAction.Command) != "" {
			return "SETTLE MERGE"
		}
		return "INSPECT MERGE"
	case plan.PlanActionRebaseAndReview:
		return "REBASE AND REVIEW"
	}
	if strings.TrimSpace(row.NextAction) != "" && row.Status != plan.StatusAbandoned {
		return row.NextAction
	}
	return monitor.DeriveNextAction(row)
}

func planNextActionDisplay(row monitor.Row) string {
	if row.MergeInProgress {
		if phase := rowlabel.PhaseLabel(row); phase != "-" {
			return "MERGING · " + phase
		}
		return "MERGING"
	}
	action := row.RecommendedAction
	if action.Kind != plan.PlanActionRestartMerge && action.Kind != plan.PlanActionRecoverMerge && action.Kind != plan.PlanActionRebaseAndReview {
		return planNextAction(row)
	}
	guidance := strings.TrimSpace(action.Command)
	if guidance == "" {
		guidance = strings.TrimSpace(action.Instruction)
	}
	if guidance == "" {
		return planNextAction(row)
	}
	if reason := strings.TrimSpace(action.Reason); reason != "" {
		return guidance + " — " + reason
	}
	return guidance
}

func hasVisibleRun(row monitor.Row) bool {
	return row.Liveness == monitor.LivenessLive || rowlabel.IsStalled(row)
}
