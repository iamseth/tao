package tui

import (
	"context"
	"slices"
	"sort"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
)

// Filter is shared by plans and notes. Values within a criterion are ORed;
// applicable criteria are ANDed. Matching uses exact IDs, statuses, and tags.
// Its zero value matches everything.
type Filter struct {
	Enabled      bool
	Repositories []string
	Statuses     []string
	Tags         []string
}

// FilterStore is the optional persistence boundary for dashboard filters.
// A nil store on App keeps filters in memory only.
type FilterStore interface {
	Load(context.Context) (Filter, error)
	Save(context.Context, Filter) error
}

// IsEmpty reports whether no criteria are configured, independent of Enabled.
func (f Filter) IsEmpty() bool {
	return len(f.Repositories) == 0 && len(f.Statuses) == 0 && len(f.Tags) == 0
}

// Active reports whether the filter is enabled and has configured criteria.
func (f Filter) Active() bool {
	return f.Enabled && !f.IsEmpty()
}

// Clear removes criteria without changing whether filtering is enabled.
func (f *Filter) Clear() {
	f.Repositories = nil
	f.Statuses = nil
	f.Tags = nil
}

// MatchesRow applies repository and plan-status criteria, never note tags.
// Repository warnings (unlike invalid plans) bypass the status criterion.
func (f Filter) MatchesRow(row monitor.Row) bool {
	if !f.Enabled {
		return true
	}
	return matchesFilterValue(f.Repositories, row.RepositoryID) &&
		(row.Kind == monitor.RowKindRepositoryWarning || matchesFilterValue(f.Statuses, row.Status))
}

// MatchesNote applies repository and tag criteria, never plan statuses.
func (f Filter) MatchesNote(item note.CatalogNote) bool {
	if !f.Enabled {
		return true
	}
	if !matchesFilterValue(f.Repositories, item.RepositoryID) {
		return false
	}
	if len(f.Tags) == 0 {
		return true
	}
	for _, tag := range item.Tags {
		if slices.Contains(f.Tags, tag) {
			return true
		}
	}
	return false
}

func matchesFilterValue(values []string, value string) bool {
	return len(values) == 0 || slices.Contains(values, value)
}

func repositoryFilter(repositoryID string) Filter {
	f := Filter{Enabled: true}
	if repositoryID != "" {
		f.Repositories = []string{repositoryID}
	}
	return f
}

// FilterOption is a discoverable criterion value. Name is the display name
// (falling back to ID). Unavailable persisted values remain selectable so they
// can be removed or retained until a later snapshot contains them again.
type FilterOption struct {
	ID        string
	Name      string
	Available bool
}

// DiscoverRepositories returns unique repositories sorted by ID from both
// snapshots, including warnings and persisted IDs absent from the snapshots.
func DiscoverRepositories(plans monitor.Snapshot, notes note.Snapshot, persisted []string) []FilterOption {
	options := make(map[string]FilterOption)
	for _, row := range plans.Rows {
		addFilterOption(options, row.RepositoryID, row.RepositoryName)
	}
	for _, item := range notes.Notes {
		addFilterOption(options, item.RepositoryID, item.RepositoryName)
	}
	for _, warning := range notes.Warnings {
		addFilterOption(options, warning.RepositoryID, warning.RepositoryName)
	}
	return sortedFilterOptions(options, persisted)
}

// DiscoverStatuses includes all lifecycle statuses even in an empty snapshot,
// plus observed statuses and any unavailable persisted values, sorted by ID.
func DiscoverStatuses(snapshot monitor.Snapshot, persisted []string) []FilterOption {
	options := make(map[string]FilterOption)
	for _, status := range []string{
		plan.StatusPlanned, plan.StatusPending, plan.StatusInProgress,
		plan.StatusInReview, plan.StatusReviewed, plan.StatusChangesRequested,
		plan.StatusVerificationFailed, plan.StatusCompleted, plan.StatusAbandoned,
		plan.StatusSkipped, plan.StatusBlocked, plan.StatusInvalid,
	} {
		addFilterOption(options, status, status)
	}
	for _, row := range snapshot.Rows {
		addFilterOption(options, row.Status, row.Status)
	}
	return sortedFilterOptions(options, persisted)
}

// DiscoverTags returns unique note tags sorted by ID, retaining unavailable
// persisted values. Tier tags are included like any other note tag.
func DiscoverTags(snapshot note.Snapshot, persisted []string) []FilterOption {
	options := make(map[string]FilterOption)
	for _, item := range snapshot.Notes {
		for _, tag := range item.Tags {
			addFilterOption(options, tag, tag)
		}
	}
	return sortedFilterOptions(options, persisted)
}

func addFilterOption(options map[string]FilterOption, id, name string) {
	if id == "" {
		return
	}
	// Prefer a real display name; resolve conflicting names deterministically.
	if previous, ok := options[id]; ok && previous.Name != id && (name == "" || name == id || previous.Name < name) {
		return
	}
	if name == "" {
		name = id
	}
	options[id] = FilterOption{ID: id, Name: name, Available: true}
}

func sortedFilterOptions(options map[string]FilterOption, persisted []string) []FilterOption {
	for _, id := range persisted {
		if _, exists := options[id]; !exists && id != "" {
			options[id] = FilterOption{ID: id, Name: id}
		}
	}
	result := make([]FilterOption, 0, len(options))
	for _, option := range options {
		result = append(result, option)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
