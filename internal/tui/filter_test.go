package tui

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
)

func TestFilterMatching(t *testing.T) {
	rows := []monitor.Row{
		{Kind: monitor.RowKindPlan, RepositoryID: "a", Status: plan.StatusPlanned},
		{Kind: monitor.RowKindPlan, RepositoryID: "b", Status: plan.StatusCompleted},
		{Kind: monitor.RowKindPlan, RepositoryID: "c", Status: plan.StatusBlocked},
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "a", Status: plan.StatusInvalid},
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "c", Status: plan.StatusInvalid},
		{Kind: monitor.RowKindPlan, RepositoryID: "a", Status: plan.StatusInvalid},
		{RepositoryID: "a", Status: plan.StatusPlanned}, // Existing callers may omit Kind.
	}
	notes := []note.CatalogNote{
		{RepositoryID: "a", Tags: []string{"bug", "tier1"}},
		{RepositoryID: "b", Tags: []string{"feature"}},
		{RepositoryID: "c", Tags: []string{"bug"}},
		{RepositoryID: "a"},
	}
	for _, test := range []struct {
		name   string
		filter Filter
		rows   []int
		notes  []int
	}{
		{"zero", Filter{}, []int{0, 1, 2, 3, 4, 5, 6}, []int{0, 1, 2, 3}},
		{"disabled", Filter{Repositories: []string{"missing"}, Statuses: []string{"missing"}, Tags: []string{"missing"}}, []int{0, 1, 2, 3, 4, 5, 6}, []int{0, 1, 2, 3}},
		{"empty enabled", Filter{Enabled: true}, []int{0, 1, 2, 3, 4, 5, 6}, []int{0, 1, 2, 3}},
		{"multi repository", Filter{Enabled: true, Repositories: []string{"a", "b"}}, []int{0, 1, 3, 5, 6}, []int{0, 1, 3}},
		{"statuses", Filter{Enabled: true, Statuses: []string{plan.StatusPlanned, plan.StatusCompleted}}, []int{0, 1, 3, 4, 6}, []int{0, 1, 2, 3}},
		{"invalid plans", Filter{Enabled: true, Statuses: []string{plan.StatusInvalid}}, []int{3, 4, 5}, []int{0, 1, 2, 3}},
		{"tags", Filter{Enabled: true, Tags: []string{"bug", "feature"}}, []int{0, 1, 2, 3, 4, 5, 6}, []int{0, 1, 2}},
		{"tier tag", Filter{Enabled: true, Tags: []string{"tier1"}}, []int{0, 1, 2, 3, 4, 5, 6}, []int{0}},
		{"combined", Filter{Enabled: true, Repositories: []string{"a", "b"}, Statuses: []string{plan.StatusCompleted}, Tags: []string{"feature"}}, []int{1, 3}, []int{1}},
		{"unavailable repository", Filter{Enabled: true, Repositories: []string{"missing"}}, nil, nil},
		{"unavailable status", Filter{Enabled: true, Statuses: []string{"missing"}}, []int{3, 4}, []int{0, 1, 2, 3}},
		{"exact tag", Filter{Enabled: true, Tags: []string{"BUG"}}, []int{0, 1, 2, 3, 4, 5, 6}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var matchedRows, matchedNotes []int
			for i, row := range rows {
				if test.filter.MatchesRow(row) {
					matchedRows = append(matchedRows, i)
				}
			}
			for i, item := range notes {
				if test.filter.MatchesNote(item) {
					matchedNotes = append(matchedNotes, i)
				}
			}
			if !slices.Equal(matchedRows, test.rows) || !slices.Equal(matchedNotes, test.notes) {
				t.Fatalf("matched rows %v, notes %v; want rows %v, notes %v", matchedRows, matchedNotes, test.rows, test.notes)
			}
		})
	}
}

func TestFilterActiveAndClear(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, configured := range []Filter{
			{Repositories: []string{"a"}}, {Statuses: []string{plan.StatusPlanned}}, {Tags: []string{"bug"}},
			{Repositories: []string{"a"}, Statuses: []string{plan.StatusPlanned}, Tags: []string{"bug"}},
		} {
			configured.Enabled = enabled
			if configured.IsEmpty() || configured.Active() != enabled {
				t.Fatalf("configured filter state = %+v", configured)
			}
			copy := configured
			configured.Clear()
			if !configured.IsEmpty() || configured.Active() || configured.Enabled != enabled {
				t.Fatalf("cleared filter state = %+v", configured)
			}
			if copy.IsEmpty() {
				t.Fatal("clearing a filter mutated its copy")
			}
		}
	}
}

func TestDiscoverFilterOptions(t *testing.T) {
	plans := monitor.Snapshot{Rows: []monitor.Row{
		{RepositoryID: "b", RepositoryName: "Beta", Status: "custom"},
		{RepositoryID: "a", Status: plan.StatusPlanned},
		{RepositoryID: "b", RepositoryName: "Beta", Status: "custom"},
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "warn", RepositoryName: "Warning", Status: "warning"},
		{},
	}}
	notes := note.Snapshot{
		Notes: []note.CatalogNote{
			{RepositoryID: "a", RepositoryName: "Alpha", Tags: []string{"z", "tier1", "z"}},
			{RepositoryID: "c", Tags: []string{"a", ""}},
		},
		Warnings: []note.CatalogWarning{{RepositoryID: "note-warning", RepositoryName: "Note warning"}},
	}
	persisted := []string{"missing", "a", "missing", ""}
	wantRepos := []FilterOption{
		{ID: "a", Name: "Alpha", Available: true},
		{ID: "b", Name: "Beta", Available: true},
		{ID: "c", Name: "c", Available: true},
		{ID: "missing", Name: "missing"},
		{ID: "note-warning", Name: "Note warning", Available: true},
		{ID: "warn", Name: "Warning", Available: true},
	}
	if got := DiscoverRepositories(plans, notes, persisted); !reflect.DeepEqual(got, wantRepos) {
		t.Fatalf("repositories = %+v, want %+v", got, wantRepos)
	}
	wantTags := []FilterOption{
		{ID: "a", Name: "a", Available: true},
		{ID: "missing", Name: "missing"},
		{ID: "tier1", Name: "tier1", Available: true},
		{ID: "z", Name: "z", Available: true},
	}
	if got := DiscoverTags(notes, persisted); !reflect.DeepEqual(got, wantTags) {
		t.Fatalf("tags = %+v, want %+v", got, wantTags)
	}
	statuses := DiscoverStatuses(plans, []string{"missing", "custom", "missing"})
	wantIDs := []string{"abandoned", "blocked", "changes_requested", "completed", "custom", "in_progress", "in_review", "invalid", "missing", "pending", "planned", "reviewed", "skipped", "verification_failed", "warning"}
	var gotIDs []string
	for _, option := range statuses {
		gotIDs = append(gotIDs, option.ID)
		if option.Name != option.ID || option.Available != (option.ID != "missing") {
			t.Errorf("unexpected status option: %+v", option)
		}
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("statuses = %v, want %v", gotIDs, wantIDs)
	}
	if len(DiscoverStatuses(monitor.Snapshot{}, nil)) != 12 {
		t.Fatal("empty snapshot lost fixed lifecycle statuses")
	}
	if got := DiscoverRepositories(monitor.Snapshot{}, note.Snapshot{}, persisted); !reflect.DeepEqual(got, []FilterOption{{ID: "a", Name: "a"}, {ID: "missing", Name: "missing"}}) {
		t.Fatalf("empty repository discovery = %+v", got)
	}
	if got := DiscoverTags(note.Snapshot{}, []string{"old"}); !reflect.DeepEqual(got, []FilterOption{{ID: "old", Name: "old"}}) {
		t.Fatalf("empty tag discovery = %+v", got)
	}
	if plans.Rows[0].RepositoryID != "b" || !slices.Equal(notes.Notes[0].Tags, []string{"z", "tier1", "z"}) || !slices.Equal(persisted, []string{"missing", "a", "missing", ""}) {
		t.Fatal("discovery mutated inputs")
	}
}

func TestBuildFilteredSectionsPreservesGroupingAndHistoryCap(t *testing.T) {
	rows := []monitor.Row{
		{RepositoryID: "a", PlanID: "now", Status: plan.StatusBlocked},
		{RepositoryID: "b", PlanID: "next", Status: plan.StatusPlanned},
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "b"},
	}
	for i := range 20 {
		rows = append(rows,
			monitor.Row{RepositoryID: "other", PlanID: "excluded", Status: plan.StatusCompleted},
			monitor.Row{RepositoryID: "a", PlanID: fmt.Sprint(i), Status: plan.StatusCompleted},
		)
	}
	before := slices.Clone(rows)
	sections := BuildFilteredSections(rows, Filter{Enabled: true, Repositories: []string{"a", "b"}})
	if len(sections) != 3 || sections[0].Kind != SectionNow || sections[1].Kind != SectionNext || sections[2].Kind != SectionHistory {
		t.Fatalf("section ordering = %+v", sections)
	}
	if len(sections[0].Rows) != 2 || sections[0].Rows[0].PlanID != "now" || sections[0].Rows[1].Kind != monitor.RowKindRepositoryWarning || len(sections[1].Rows) != 1 || sections[1].Rows[0].PlanID != "next" {
		t.Fatalf("filtered groups = %+v", sections)
	}
	if len(sections[2].Rows) != maxHistoryPlans {
		t.Fatalf("DONE count = %d", len(sections[2].Rows))
	}
	for i, row := range sections[2].Rows {
		if row.PlanID != fmt.Sprint(i) {
			t.Fatalf("DONE row %d = %s", i, row.PlanID)
		}
	}
	for _, repo := range []string{"", "a", "missing"} {
		if !reflect.DeepEqual(BuildRepositorySections(rows, repo), BuildFilteredSections(rows, repositoryFilter(repo))) {
			t.Fatalf("repository wrapper differs for %q", repo)
		}
	}
	if !reflect.DeepEqual(BuildSections(rows), BuildFilteredSections(rows, Filter{})) || !reflect.DeepEqual(rows, before) {
		t.Fatal("empty filter changed behavior or filtering mutated snapshot")
	}
}

func TestVisibleFilteredNotesPreservesTierOrderAndSnapshot(t *testing.T) {
	snapshot := note.Snapshot{Notes: []note.CatalogNote{
		{RepositoryID: "a", ID: "untiered", Tags: []string{"bug"}},
		{RepositoryID: "a", ID: "tier2", Tags: []string{"tier2", "bug"}},
		{RepositoryID: "c", ID: "excluded", Tags: []string{"tier1", "bug"}},
		{RepositoryID: "b", ID: "tier1-first", Tags: []string{"tier1", "bug"}},
		{RepositoryID: "a", ID: "tier1-second", Tags: []string{"tier1", "bug"}},
		{RepositoryID: "a", ID: "wrong-tag", Tags: []string{"tier0"}},
	}}
	before := slices.Clone(snapshot.Notes)
	items := visibleFilteredNotes(snapshot, Filter{Enabled: true, Repositories: []string{"a", "b"}, Tags: []string{"bug"}})
	var ids []string
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if !slices.Equal(ids, []string{"tier1-first", "tier1-second", "tier2", "untiered"}) {
		t.Fatalf("visible notes = %v", ids)
	}
	for _, repo := range []string{"", "a", "missing"} {
		if !reflect.DeepEqual(visibleNotes(snapshot, repo), visibleFilteredNotes(snapshot, repositoryFilter(repo))) {
			t.Fatalf("repository wrapper differs for %q", repo)
		}
	}
	if !reflect.DeepEqual(visibleNotes(snapshot, ""), visibleFilteredNotes(snapshot, Filter{})) || !reflect.DeepEqual(snapshot.Notes, before) {
		t.Fatal("empty filter changed behavior or filtering mutated snapshot")
	}
}
