package tui

import (
	"strings"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
)

// FilterPlanRows returns rows whose searchable fields contain query.
func FilterPlanRows(rows []monitor.Row, query string) []monitor.Row {
	query = normalizedSearchQuery(query)
	if query == "" {
		return rows
	}
	filtered := make([]monitor.Row, 0, len(rows))
	for _, row := range rows {
		values := []string{
			row.RepositoryID,
			row.RepositoryName,
			row.RepositoryRoot,
			row.PlanID,
			row.PlanTitle,
			row.Status,
			string(row.Liveness),
			string(row.Phase),
			row.SliceID,
			row.ApprovalSliceID,
			row.ApprovalReason,
			strings.Join(row.Warnings, " "),
		}
		for _, reason := range row.AttentionReasons {
			values = append(values, string(reason))
		}
		if searchMatches(query, values...) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// FilterNoteSnapshot returns notes and warnings whose searchable fields contain query.
func FilterNoteSnapshot(snapshot note.Snapshot, query string) note.Snapshot {
	query = normalizedSearchQuery(query)
	if query == "" {
		return snapshot
	}
	filtered := snapshot
	filtered.Notes = make([]note.CatalogNote, 0, len(snapshot.Notes))
	for _, item := range snapshot.Notes {
		if searchMatches(query,
			item.RepositoryID,
			item.RepositoryName,
			item.RepositoryRoot,
			item.ID,
			item.Text,
			strings.Join(item.Tags, " "),
			"open",
		) {
			filtered.Notes = append(filtered.Notes, item)
		}
	}
	filtered.Warnings = make([]note.CatalogWarning, 0, len(snapshot.Warnings))
	for _, warning := range snapshot.Warnings {
		if searchMatches(query,
			warning.RepositoryID,
			warning.RepositoryName,
			warning.Path,
			warning.Error(),
		) {
			filtered.Warnings = append(filtered.Warnings, warning)
		}
	}
	return filtered
}

func normalizedSearchQuery(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

func searchMatches(query string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func searchHeaderLabel(query string, active bool) string {
	value := singleLineDetail(query)
	if active {
		return "Search: /" + value + "█"
	}
	return "Search: /" + value
}
