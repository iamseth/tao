package tui

// PageID identifies a top-level dashboard page.
type PageID string

const (
	PagePlans    PageID = "plans"
	PageNotes    PageID = "notes"
	PageSettings PageID = "settings"
	PageDebug    PageID = "debug"
	PageReview   PageID = "review"
)

// Tab describes one visible top-level dashboard destination.
type Tab struct {
	ID    PageID
	Label string
}

var dashboardTabs = []Tab{
	{ID: PageNotes, Label: "Backlog"},
	{ID: PagePlans, Label: "WIP"},
	{ID: PageReview, Label: "Review"},
}

func normalizePage(page PageID) PageID {
	switch page {
	case PageNotes, PagePlans, PageReview, PageSettings, PageDebug:
		return page
	}
	return PagePlans
}

func adjacentPage(page PageID, delta int) PageID {
	page = normalizePage(page)
	index := 0
	for candidate, tab := range dashboardTabs {
		if tab.ID == page {
			index = candidate
			break
		}
	}
	index = (index + delta) % len(dashboardTabs)
	if index < 0 {
		index += len(dashboardTabs)
	}
	return dashboardTabs[index].ID
}
