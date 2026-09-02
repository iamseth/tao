package tui

// PageID identifies a top-level dashboard page.
type PageID string

const (
	PagePlans    PageID = "plans"
	PageNotes    PageID = "notes"
	PageSettings PageID = "settings"
	PageDebug    PageID = "debug"
)

// Tab describes one top-level dashboard destination. Adding a page only
// requires registering another tab and implementing its page seams.
type Tab struct {
	ID    PageID
	Label string
}

var dashboardTabs = []Tab{
	{ID: PageNotes, Label: "notes"},
	{ID: PagePlans, Label: "plans"},
	{ID: PageSettings, Label: "settings"},
	{ID: PageDebug, Label: "debug"},
}

func normalizePage(page PageID) PageID {
	for _, tab := range dashboardTabs {
		if tab.ID == page {
			return page
		}
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
