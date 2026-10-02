package tui

import (
	"strings"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/plan"
)

// SectionKind identifies one table-page plan group.
type SectionKind string

const (
	SectionNow     SectionKind = "now"
	SectionNext    SectionKind = "next"
	SectionHistory SectionKind = "history"
)

// Section is one stable partition of monitor rows. Rows retain the collector's
// urgency order within each section.
type Section struct {
	Kind  SectionKind
	Title string
	Rows  []monitor.Row
}

// BuildSections groups monitor rows for the table page without re-sorting them.
func BuildSections(rows []monitor.Row) []Section {
	return BuildRepositorySections(rows, "")
}

// BuildRepositorySections groups rows after optionally restricting them to one
// repository. Filtering never mutates or reorders the collector snapshot.
func BuildRepositorySections(rows []monitor.Row, repositoryID string) []Section {
	return BuildFilteredSections(rows, repositoryFilter(repositoryID))
}

// BuildFilteredSections filters before grouping without mutating the collector snapshot.
func BuildFilteredSections(rows []monitor.Row, filter Filter) []Section {
	sections := []Section{
		{Kind: SectionNow, Title: "NOW"},
		{Kind: SectionNext, Title: "NEXT"},
		{Kind: SectionHistory, Title: "DONE"},
	}
	for _, row := range rows {
		if !filter.MatchesRow(row) {
			continue
		}
		kind := sectionKind(row)
		for index := range sections {
			if sections[index].Kind == kind {
				sections[index].Rows = append(sections[index].Rows, row)
				break
			}
		}
	}
	for index := range sections {
		if sections[index].Kind == SectionNext {
			orderNextRows(sections[index].Rows)
		}
	}
	return sections
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

func sectionKind(row monitor.Row) SectionKind {
	if row.Status == plan.StatusCompleted || row.Status == plan.StatusAbandoned {
		return SectionHistory
	}
	if row.Status == plan.StatusInProgress || row.Status == plan.StatusBlocked || row.Status == plan.StatusReviewed {
		return SectionNow
	}
	switch planNextAction(row) {
	case "RUN", "CHECK", "WAIT", "SKIP":
		return SectionNext
	default:
		return SectionNow
	}
}

func hasVisibleRun(row monitor.Row) bool {
	return row.Liveness == monitor.LivenessLive || rowlabel.IsStalled(row)
}
