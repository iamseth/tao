package tui

// PageID identifies a top-level dashboard page.
type PageID string

const (
	PagePlans    PageID = "plans"
	PageHistory  PageID = "history"
	PageNotes    PageID = "notes"
	PageSettings PageID = "settings"
	PageDebug    PageID = "debug"
)

// Tab describes one visible top-level dashboard destination.
type Tab struct {
	ID    PageID
	Label string
}

var dashboardTabs = []Tab{
	{ID: PageHistory, Label: "History"},
	{ID: PageNotes, Label: "Backlog"},
	{ID: PagePlans, Label: "WIP"},
}

func normalizePage(page PageID) PageID {
	switch page {
	case PageNotes, PagePlans, PageHistory, PageSettings, PageDebug:
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
