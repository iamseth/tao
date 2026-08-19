package tui

import (
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runqueue"
)

// SectionKind identifies one table-page plan group.
type SectionKind string

const (
	SectionAttention SectionKind = "attention"
	SectionRunning   SectionKind = "running"
	SectionQueued    SectionKind = "queued"
	SectionPlanned   SectionKind = "planned"
	SectionCompleted SectionKind = "completed"
)

// Section is one stable partition of monitor rows. Rows retain the collector's
// urgency order within each section.
type Section struct {
	Kind  SectionKind
	Title string
	Rows  []monitor.Row
}

// BuildSections groups monitor rows for the table page without re-sorting them.
func BuildSections(rows []monitor.Row, showCompleted bool) []Section {
	return BuildRepositorySections(rows, showCompleted, "")
}

// BuildRepositorySections groups rows after optionally restricting them to one
// repository. Filtering never mutates or reorders the collector snapshot.
func BuildRepositorySections(rows []monitor.Row, showCompleted bool, repositoryID string) []Section {
	sections := []Section{
		{Kind: SectionAttention, Title: "NEEDS ATTENTION"},
		{Kind: SectionRunning, Title: "RUNNING"},
		{Kind: SectionQueued, Title: "QUEUED"},
		{Kind: SectionPlanned, Title: "PLANNED / IN REVIEW"},
		{Kind: SectionCompleted, Title: "COMPLETED"},
	}
	for _, row := range rows {
		if repositoryID != "" && row.RepositoryID != repositoryID {
			continue
		}
		kind := sectionKind(row)
		if kind == SectionCompleted && !showCompleted {
			continue
		}
		for index := range sections {
			if sections[index].Kind == kind {
				sections[index].Rows = append(sections[index].Rows, row)
				break
			}
		}
	}
	return sections
}

func sectionKind(row monitor.Row) SectionKind {
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
	if row.QueueStatus == runqueue.QueueStatusPending || row.QueueStatus == runqueue.QueueStatusRunning {
		return SectionQueued
	}
	return SectionPlanned
}

func isStalled(row monitor.Row) bool {
	return row.Liveness == monitor.LivenessStale && row.RunLockPresent && row.RunLockProcessAlive
}

func visibleRows(rows []monitor.Row, showCompleted bool, repositoryID string) []monitor.Row {
	var visible []monitor.Row
	for _, section := range BuildRepositorySections(rows, showCompleted, repositoryID) {
		visible = append(visible, section.Rows...)
	}
	return visible
}
