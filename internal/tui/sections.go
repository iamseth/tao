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

	SectionAttention    SectionKind = "attention"
	SectionReadyToMerge SectionKind = "ready_to_merge"
	SectionPlanned      SectionKind = "planned"
	SectionHistory      SectionKind = "history"
	SectionRunning      SectionKind = "running"
	SectionCompleted    SectionKind = "completed"
	SectionAbandoned    SectionKind = "abandoned"
)

// Section is one stable partition of monitor rows. Rows retain the collector's
// urgency order within each section.
type Section struct {
	Kind  SectionKind
	Title string
	Rows  []monitor.Row
}

// BuildSections groups monitor rows for the table page without re-sorting them.
func BuildSections(rows []monitor.Row, showHistory bool) []Section {
	return BuildRepositorySections(rows, showHistory, "")
}

// BuildRepositorySections groups rows after optionally restricting them to one
// repository. Filtering never mutates or reorders the collector snapshot.
func BuildRepositorySections(rows []monitor.Row, showHistory bool, repositoryID string) []Section {
	sections := []Section{
		{Kind: SectionAttention, Title: "NEEDS ATTENTION"},
		{Kind: SectionReadyToMerge, Title: "READY TO MERGE"},
		{Kind: SectionPlanned, Title: "PLANNED"},
		{Kind: SectionHistory, Title: "HISTORY"},
	}
	for _, row := range rows {
		if repositoryID != "" && row.RepositoryID != repositoryID {
			continue
		}
		if !showHistory && (row.Status == plan.StatusCompleted || row.Status == plan.StatusAbandoned) {
			continue
		}
		classification := sectionKind(row)
		kind := nextActionSectionKind(row, classification)
		for index := range sections {
			if sections[index].Kind == kind {
				sections[index].Rows = append(sections[index].Rows, row)
				break
			}
		}
	}
	for index := range sections {
		switch sections[index].Kind {
		case SectionPlanned:
			orderPlannedRows(sections[index].Rows)
		case SectionHistory:
			if len(sections[index].Rows) > maxHistoryPlans {
				sections[index].Rows = sections[index].Rows[:maxHistoryPlans]
			}
		}
	}
	return sections
}

func nextActionSectionKind(row monitor.Row, classification SectionKind) SectionKind {
	if classification == SectionAttention {
		return SectionAttention
	}
	switch planNextAction(row) {
	case "INSPECT":
		return SectionAttention
	case "MERGE":
		return SectionReadyToMerge
	}
	if classification == SectionCompleted || classification == SectionAbandoned {
		return SectionHistory
	}
	return SectionPlanned
}

func planNextAction(row monitor.Row) string {
	if strings.TrimSpace(row.NextAction) != "" && row.Status != plan.StatusAbandoned {
		return row.NextAction
	}
	return monitor.DeriveNextAction(row)
}

func sectionKind(row monitor.Row) SectionKind {
	if row.Status == plan.StatusAbandoned {
		return SectionAbandoned
	}
	// A stale heartbeat is display-only. It identifies a stalled run only while
	// the collector can still observe a live process through the run lock.
	if isStalled(row) {
		return SectionRunning
	}
	if len(row.AttentionReasons) > 0 {
		return SectionAttention
	}
	if row.Status == plan.StatusCompleted {
		return SectionCompleted
	}
	if row.Liveness == monitor.LivenessLive {
		return SectionRunning
	}
	return SectionPlanned
}

func isStalled(row monitor.Row) bool {
	return row.Liveness == monitor.LivenessStale && row.RunLockPresent && row.RunLockProcessAlive
}

func visibleRows(rows []monitor.Row, showHistory bool, repositoryID string) []monitor.Row {
	var visible []monitor.Row
	for _, section := range BuildRepositorySections(rows, showHistory, repositoryID) {
		visible = append(visible, section.Rows...)
	}
	return visible
}
