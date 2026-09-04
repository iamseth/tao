package tui

import (
	"strings"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

// SectionKind identifies one table-page plan group.
type SectionKind string

const (
	maxHistoryPlans = 15

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
	sections := []Section{
		{Kind: SectionNow, Title: "NOW"},
		{Kind: SectionNext, Title: "NEXT"},
		{Kind: SectionHistory, Title: "DONE"},
	}
	for _, row := range rows {
		if repositoryID != "" && row.RepositoryID != repositoryID {
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
		switch sections[index].Kind {
		case SectionNext:
			orderNextRows(sections[index].Rows)
		case SectionHistory:
			if len(sections[index].Rows) > maxHistoryPlans {
				sections[index].Rows = sections[index].Rows[:maxHistoryPlans]
			}
		}
	}
	return sections
}

func planNextAction(row monitor.Row) string {
	if strings.TrimSpace(row.NextAction) != "" && row.Status != plan.StatusAbandoned {
		return row.NextAction
	}
	return monitor.DeriveNextAction(row)
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

func isStalled(row monitor.Row) bool {
	return row.Liveness == monitor.LivenessStale && row.RunLockPresent && row.RunLockProcessAlive
}

func visibleRows(rows []monitor.Row, repositoryID string) []monitor.Row {
	var visible []monitor.Row
	for _, section := range BuildRepositorySections(rows, repositoryID) {
		visible = append(visible, section.Rows...)
	}
	return visible
}
