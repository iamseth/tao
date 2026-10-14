package tui

import (
	"reflect"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
)

func TestSortMissingLast(t *testing.T) {
	now := time.Now()
	for _, column := range []SortColumn{SortAge, SortPriority, SortRun, SortSlices} {
		for _, descending := range []bool{false, true} {
			rows := []monitor.Row{{PlanID: "missing"}, {PlanID: "present", UpdatedAt: &now, Liveness: monitor.LivenessLive, OriginalTotalCount: 2, Overview: plan.DecisionOverview{Priority: &plan.Priority{}}}}
			got := SortPlanRows(rows, Sort{column, descending})
			if got[0].PlanID != "present" {
				t.Fatalf("%s descending=%v: %v", column, descending, got)
			}
		}
	}
	for _, column := range []SortColumn{SortCreated, SortUpdated} {
		for _, descending := range []bool{false, true} {
			got := SortNotes([]note.CatalogNote{{ID: "missing"}, {ID: "present", CreatedAt: now, UpdatedAt: now}}, Sort{column, descending})
			if got[0].ID != "present" {
				t.Fatalf("%s descending=%v: %v", column, descending, got)
			}
		}
	}
}

func TestSortPageContract(t *testing.T) {
	var sorts PageSorts
	for _, page := range []PageID{PagePlans, PageHistory, PageNotes} {
		wanted := []SortColumn{SortDefault, SortRepo, SortNext, SortPlan, SortPriority, SortSlices, SortRun, SortAge}
		if page == PageNotes {
			wanted = []SortColumn{SortDefault, SortRepo, SortPreview, SortTags, SortTier, SortCreated, SortUpdated}
		}
		options := SortOptionsForPage(page)
		if len(options) != len(wanted) {
			t.Fatalf("options: %v", options)
		}
		for i, option := range options {
			descending := option.Column == SortSlices || option.Column == SortRun || option.Column == SortAge || option.Column == SortCreated || option.Column == SortUpdated
			if option.Column != wanted[i] || option.Label == "" || option.DescendingDefault != descending {
				t.Fatalf("option: %+v", option)
			}
			selected := Sort{option.Column, descending}
			sorts.Set(page, selected)
			if sorts.For(page) != selected {
				t.Fatal("round trip")
			}
			if NormalizeSort(page, selected) != selected {
				t.Fatal("normalization")
			}
		}
		cross := SortPreview
		if page == PageNotes {
			cross = SortAge
		}
		for _, column := range []SortColumn{"unknown", cross, "", SortDefault} {
			if got := NormalizeSort(page, Sort{column, true}); got != (Sort{Column: SortDefault}) {
				t.Fatalf("normalize %s: %+v", column, got)
			}
		}
	}
	if !(Sort{}).IsDefault() || !(Sort{Column: SortDefault}).IsDefault() || (Sort{Column: SortRepo}).IsDefault() {
		t.Fatal("default detection")
	}
}

func TestSortWarningPositionsAndDefaultTies(t *testing.T) {
	rows := []monitor.Row{{PlanID: "z", PlanTitle: "same", RepositoryName: "z"}, {Kind: monitor.RowKindRepositoryWarning, RepositoryID: "warning"}, {PlanID: "a", PlanTitle: "same", RepositoryName: "a"}}
	got := BuildPageSections(rows, Filter{}, PagePlans, Sort{Column: SortRepo})[0].Rows
	if got[0].PlanID != "a" || got[1].RepositoryID != "warning" || got[2].PlanID != "z" {
		t.Fatalf("warning slots: %v", got)
	}
	if !reflect.DeepEqual(SortPlanRows(rows, Sort{}), rows) {
		t.Fatal("default order")
	}
	for _, descending := range []bool{false, true} {
		tied := SortPlanRows(rows, Sort{SortAge, descending})
		if !reflect.DeepEqual(tied, rows) {
			t.Fatal("tie order")
		}
	}
}

func TestSortColumnValues(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)
	rows := []monitor.Row{
		{PlanID: "b", PlanTitle: "z", NextAction: "z", RepositoryName: "same", RepositoryID: "z", UpdatedAt: &later, Liveness: monitor.LivenessLive, InvocationDuration: time.Minute, OriginalCompletedCount: 2, OriginalTotalCount: 2, Overview: plan.DecisionOverview{Priority: &plan.Priority{Impact: plan.PriorityLevelHigh}}},
		{PlanID: "a", PlanTitle: "a", NextAction: "a", RepositoryName: "same", RepositoryID: "a", UpdatedAt: &now, Liveness: monitor.LivenessLive, OriginalCompletedCount: 1, OriginalTotalCount: 2, Overview: plan.DecisionOverview{Priority: &plan.Priority{Impact: plan.PriorityLevelLow}}},
	}
	for _, column := range []SortColumn{SortRepo, SortPlan, SortNext, SortAge, SortRun, SortSlices, SortPriority} {
		for _, descending := range []bool{false, true} {
			want := "a"
			if descending {
				want = "b"
			}
			if got := SortPlanRows(rows, Sort{column, descending}); got[0].PlanID != want {
				t.Fatalf("%s descending=%v: %v", column, descending, got)
			}
		}
	}
	items := []note.CatalogNote{{ID: "b", RepositoryName: "z", Text: "z", Tags: []string{"tier2", "z"}, CreatedAt: later, UpdatedAt: later}, {ID: "a", RepositoryName: "a", Text: "a", Tags: []string{"tier1", "a"}, CreatedAt: now, UpdatedAt: now}}
	for _, column := range []SortColumn{SortRepo, SortPreview, SortTags, SortTier, SortCreated, SortUpdated} {
		for _, descending := range []bool{false, true} {
			want := "a"
			if descending {
				want = "b"
			}
			if got := SortNotes(items, Sort{column, descending}); got[0].ID != want {
				t.Fatalf("%s descending=%v: %v", column, descending, got)
			}
		}
	}
}

func TestSortCopiesAndStability(t *testing.T) {
	rows := []monitor.Row{{PlanID: "b", RepositoryName: "z"}, {PlanID: "a", RepositoryName: "a"}, {PlanID: "c", RepositoryName: "a"}}
	before := append([]monitor.Row(nil), rows...)
	got := SortPlanRows(rows, Sort{Column: SortRepo})
	if got[0].PlanID != "a" || got[1].PlanID != "c" || !reflect.DeepEqual(rows, before) {
		t.Fatalf("sort or mutation: %v", got)
	}
	items := []note.CatalogNote{{ID: "b", Text: "z"}, {ID: "a", Text: "a"}, {ID: "c", Text: "a"}}
	prior := append([]note.CatalogNote(nil), items...)
	sorted := SortNotes(items, Sort{Column: SortPreview})
	if sorted[0].ID != "a" || sorted[1].ID != "c" || !reflect.DeepEqual(items, prior) {
		t.Fatalf("note sort or mutation: %v", sorted)
	}
}
