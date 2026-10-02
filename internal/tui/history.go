package tui

import (
	"sort"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
)

func isPlanPage(page PageID) bool {
	return page == PagePlans || page == PageHistory
}

func historyStatus(status string) bool {
	return status == plan.StatusCompleted || status == plan.StatusAbandoned
}

// BuildPageSections applies page membership independently of filters. Unknown
// and invalid states stay in WIP so work needing attention remains visible.
func BuildPageSections(rows []monitor.Row, filter Filter, page PageID) []Section {
	page = normalizePage(page)
	filtered := make([]monitor.Row, 0, len(rows))
	for _, row := range rows {
		if historyStatus(row.Status) == (page == PageHistory) {
			filtered = append(filtered, row)
		}
	}
	if page == PageHistory {
		sort.SliceStable(filtered, func(i, j int) bool {
			left, right := filtered[i].UpdatedAt, filtered[j].UpdatedAt
			if left == nil {
				return false
			}
			return right == nil || left.After(*right)
		})
	}
	return BuildFilteredSections(filtered, filterForPage(filter, page))
}

// The persisted status set holds both pages' selections. Projecting it keeps
// each page independent and migrates legacy combined filters without new storage.
func filterForPage(filter Filter, page PageID) Filter {
	statuses := make([]string, 0, len(filter.Statuses))
	for _, status := range filter.Statuses {
		if status != plan.StatusSkipped && historyStatus(status) == (page == PageHistory) {
			statuses = append(statuses, status)
		}
	}
	filter.Statuses = statuses
	return filter
}

func newPageFilterMenu(filter Filter, plans monitor.Snapshot, notes note.Snapshot, page PageID) *filterMenu {
	menu := newFilterMenu(filterForPage(filter, page), plans, notes)
	statuses := menu.statuses[:0]
	for _, option := range menu.statuses {
		if option.ID != plan.StatusSkipped && historyStatus(option.ID) == (page == PageHistory) {
			statuses = append(statuses, option)
		}
	}
	menu.statuses = statuses
	return menu
}

// Preserve the inactive plan page by identity as well as the active page when
// snapshots, search, or shared repository filters change.
func (s *loopState) preserveOtherPlanSelection() func() {
	page := PageHistory
	if s.planPage() == PageHistory {
		page = PagePlans
	}
	view := *s
	view.page = page
	view.selected = s.pageSelections[page]
	selected, preserve := view.selectedRow()
	return func() {
		view.snapshot, view.filter, view.searchQuery = s.snapshot, s.filter, s.searchQuery
		view.restorePlanSelection(selected, preserve)
		if s.pageSelections == nil {
			s.pageSelections = make(map[PageID]int)
		}
		s.pageSelections[page] = view.selected
	}
}

func (s *loopState) applyPageFilter(filter Filter) {
	// Editing one page must not discard the other page's status selection.
	other := PageHistory
	if s.activePage() == PageHistory {
		other = PagePlans
	}
	filter.Statuses = append(append([]string(nil), filter.Statuses...), filterForPage(s.filter, other).Statuses...)
	s.applyFilter(filter)
}
