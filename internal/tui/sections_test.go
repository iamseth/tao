package tui

import (
	"slices"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

func TestBuildSectionsGroupsEveryRowAndPreservesOrder(t *testing.T) {
	rows := []monitor.Row{
		{PlanID: "running-first", Liveness: monitor.LivenessLive},
		{PlanID: "attention-first", AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
		{PlanID: "planned-first", Status: plan.StatusPlanned},
		{PlanID: "planned-second", Status: plan.StatusInReview},
		{PlanID: "completed", Status: plan.StatusCompleted},
		{PlanID: "stalled", Liveness: monitor.LivenessStale, RunLockPresent: true, RunLockProcessAlive: true, AttentionReasons: []monitor.AttentionReason{monitor.AttentionApprovalRequired}},
		{PlanID: "attention-second", Liveness: monitor.LivenessStale, AttentionReasons: []monitor.AttentionReason{monitor.AttentionRunCrashed}},
	}

	sections := BuildSections(rows, true)
	want := map[SectionKind][]string{
		SectionAttention: {"attention-first", "attention-second"},
		SectionRunning:   {"running-first", "stalled"},
		SectionPlanned:   {"planned-first", "planned-second"},
		SectionCompleted: {"completed"},
	}
	for _, section := range sections {
		var got []string
		for _, row := range section.Rows {
			got = append(got, row.PlanID)
		}
		if !slices.Equal(got, want[section.Kind]) {
			t.Errorf("%s rows = %v, want %v", section.Kind, got, want[section.Kind])
		}
	}
}

func TestBuildSectionsRequiresLiveRunLockForStalledClassification(t *testing.T) {
	rows := []monitor.Row{
		{PlanID: "missing-lock", Status: plan.StatusInProgress, Liveness: monitor.LivenessStale},
		{PlanID: "live-lock", Status: plan.StatusInProgress, Liveness: monitor.LivenessStale, RunLockPresent: true, RunLockProcessAlive: true},
		{PlanID: "dead-lock", Status: plan.StatusInProgress, Liveness: monitor.LivenessStale, RunLockPresent: true, AttentionReasons: []monitor.AttentionReason{monitor.AttentionRunCrashed}},
		{PlanID: "attention", Status: plan.StatusBlocked, Liveness: monitor.LivenessStale, AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
		{PlanID: "recently-completed", Status: plan.StatusCompleted, Liveness: monitor.LivenessStale},
	}

	sections := BuildSections(rows, true)
	got := make(map[SectionKind][]string)
	for _, section := range sections {
		for _, row := range section.Rows {
			got[section.Kind] = append(got[section.Kind], row.PlanID)
		}
	}
	want := map[SectionKind][]string{
		SectionAttention: {"dead-lock", "attention"},
		SectionRunning:   {"live-lock"},
		SectionPlanned:   {"missing-lock"},
		SectionCompleted: {"recently-completed"},
	}
	for kind, wantIDs := range want {
		if !slices.Equal(got[kind], wantIDs) {
			t.Errorf("%s rows = %v, want %v", kind, got[kind], wantIDs)
		}
	}
}

func TestBuildRepositorySectionsFiltersPlansAndWarnings(t *testing.T) {
	rows := []monitor.Row{
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "repo-a"},
		{Kind: monitor.RowKindPlan, RepositoryID: "repo-b", PlanID: "other"},
		{Kind: monitor.RowKindPlan, RepositoryID: "repo-a", PlanID: "active"},
		{Kind: monitor.RowKindPlan, RepositoryID: "repo-a", PlanID: "done", Status: plan.StatusCompleted},
	}

	var got []monitor.Row
	for _, section := range BuildRepositorySections(rows, false, "repo-a") {
		got = append(got, section.Rows...)
	}
	if len(got) != 2 || got[0].Kind != monitor.RowKindRepositoryWarning || got[1].PlanID != "active" {
		t.Fatalf("focused rows = %+v, want warning and active plan", got)
	}
	if rows[1].PlanID != "other" || rows[3].PlanID != "done" {
		t.Fatalf("repository filtering mutated source rows: %+v", rows)
	}
}

func TestBuildSectionsOrdersOnlyOrdinaryPlannedRows(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	older := now.Add(-time.Hour)
	rows := []monitor.Row{
		{PlanID: "running", Status: plan.StatusPlanned, Liveness: monitor.LivenessLive, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionObsolete}},
		{PlanID: "attention", Status: plan.StatusPlanned, AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady}},
		{PlanID: "deferred", Status: plan.StatusPlanned, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionDeferred}},
		{PlanID: "post-first", Status: plan.StatusInReview},
		{PlanID: "unranked", Status: plan.StatusPlanned},
		{PlanID: "post-second", Status: plan.StatusReviewed},
		{PlanID: "ready-low", Status: plan.StatusPlanned, UpdatedAt: &now, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady, Priority: &plan.Priority{Level: plan.PriorityOverallLevelCould}}},
		{PlanID: "ready-must", Status: plan.StatusPlanned, UpdatedAt: &older, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady, Priority: &plan.Priority{Level: plan.PriorityOverallLevelMust}}},
		{PlanID: "obsolete", Status: plan.StatusPlanned, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionObsolete}},
		{PlanID: "completed", Status: plan.StatusCompleted},
	}

	sections := BuildSections(rows, true)
	got := make(map[SectionKind][]string)
	for _, section := range sections {
		for _, row := range section.Rows {
			got[section.Kind] = append(got[section.Kind], row.PlanID)
		}
	}
	if want := []string{"attention"}; !slices.Equal(got[SectionAttention], want) {
		t.Fatalf("attention = %v, want %v", got[SectionAttention], want)
	}
	if want := []string{"running"}; !slices.Equal(got[SectionRunning], want) {
		t.Fatalf("running = %v, want %v", got[SectionRunning], want)
	}
	wantPlanned := []string{"ready-must", "post-first", "ready-low", "post-second", "unranked", "deferred", "obsolete"}
	if !slices.Equal(got[SectionPlanned], wantPlanned) {
		t.Fatalf("planned = %v, want %v", got[SectionPlanned], wantPlanned)
	}
	if rows[2].PlanID != "deferred" || rows[4].PlanID != "unranked" {
		t.Fatalf("section ordering mutated snapshot identity: %+v", rows)
	}
}

func TestPriorityOrderHonorsValidSequenceAndIgnoresCycles(t *testing.T) {
	row := func(id string) monitor.Row {
		return monitor.Row{RepositoryID: "repo", PlanID: id, Status: plan.StatusPlanned, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady}}
	}
	before := row("before")
	after := row("after")
	after.Relationships = []monitor.ResolvedRelationship{{PlanID: "before", Type: plan.PlanRelationAfter, State: monitor.RelationshipIncomplete}}
	cycleA := row("cycle-a")
	cycleA.Relationships = []monitor.ResolvedRelationship{{PlanID: "cycle-b", Type: plan.PlanRelationAfter, State: monitor.RelationshipCyclic}}
	cycleB := row("cycle-b")
	cycleB.Relationships = []monitor.ResolvedRelationship{{PlanID: "cycle-a", Type: plan.PlanRelationAfter, State: monitor.RelationshipCyclic}}

	ordered := priorityOrder([]monitor.Row{after, cycleB, before, cycleA})
	var got []string
	for _, item := range ordered {
		got = append(got, item.PlanID)
	}
	if want := []string{"cycle-b", "before", "after", "cycle-a"}; !slices.Equal(got, want) {
		t.Fatalf("priority order = %v, want %v", got, want)
	}
}

func TestPriorityOrderIgnoresEveryRepeatedTargetRelationship(t *testing.T) {
	row := func(id string) monitor.Row {
		return monitor.Row{RepositoryID: "repo", PlanID: id, Status: plan.StatusPlanned, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady}}
	}
	before := row("before")
	after := row("after")
	after.Relationships = []monitor.ResolvedRelationship{
		{PlanID: "before", Type: plan.PlanRelationAfter, State: monitor.RelationshipDuplicate},
		{PlanID: "before", Type: plan.PlanRelationBefore, State: monitor.RelationshipDuplicate},
	}

	ordered := priorityOrder([]monitor.Row{after, before})
	got := []string{ordered[0].PlanID, ordered[1].PlanID}
	if want := []string{"after", "before"}; !slices.Equal(got, want) {
		t.Fatalf("priority order = %v, want %v", got, want)
	}
}

func TestBuildSectionsHandlesEmptyAndHiddenCompletedSections(t *testing.T) {
	tests := []struct {
		name          string
		rows          []monitor.Row
		showCompleted bool
		wantCompleted int
	}{
		{name: "empty", showCompleted: true},
		{name: "completed shown", rows: []monitor.Row{{Status: plan.StatusCompleted}}, showCompleted: true, wantCompleted: 1},
		{name: "completed hidden", rows: []monitor.Row{{Status: plan.StatusCompleted}}, showCompleted: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sections := BuildSections(test.rows, test.showCompleted)
			if len(sections) != 4 {
				t.Fatalf("section count = %d, want 4", len(sections))
			}
			for _, section := range sections {
				want := 0
				if section.Kind == SectionCompleted {
					want = test.wantCompleted
				}
				if len(section.Rows) != want {
					t.Errorf("%s row count = %d, want %d", section.Kind, len(section.Rows), want)
				}
			}
		})
	}
}
