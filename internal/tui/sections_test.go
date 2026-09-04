package tui

import (
	"fmt"
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
		{PlanID: "abandoned", Status: plan.StatusAbandoned, Liveness: monitor.LivenessLive, AttentionReasons: []monitor.AttentionReason{monitor.AttentionApprovalRequired}},
		{PlanID: "stalled", Liveness: monitor.LivenessStale, RunLockPresent: true, RunLockProcessAlive: true, AttentionReasons: []monitor.AttentionReason{monitor.AttentionApprovalRequired}},
		{PlanID: "attention-second", Liveness: monitor.LivenessStale, AttentionReasons: []monitor.AttentionReason{monitor.AttentionRunCrashed}},
	}

	sections := BuildSections(rows, true)
	want := map[SectionKind][]string{
		SectionAttention:    {"attention-first", "attention-second"},
		SectionReadyToMerge: nil,
		SectionPlanned:      {"running-first", "planned-first", "planned-second", "stalled"},
		SectionHistory:      {"completed", "abandoned"},
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
		{PlanID: "abandoned", Status: plan.StatusAbandoned, Liveness: monitor.LivenessStale, RunLockPresent: true, RunLockProcessAlive: true, AttentionReasons: []monitor.AttentionReason{monitor.AttentionRunCrashed}},
	}

	want := []SectionKind{
		SectionPlanned,
		SectionRunning,
		SectionAttention,
		SectionAttention,
		SectionCompleted,
		SectionAbandoned,
	}
	for index, row := range rows {
		if got := sectionKind(row); got != want[index] {
			t.Errorf("sectionKind(%s) = %s, want %s", row.PlanID, got, want[index])
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
	if want := []string{"post-second"}; !slices.Equal(got[SectionReadyToMerge], want) {
		t.Fatalf("ready to merge = %v, want %v", got[SectionReadyToMerge], want)
	}
	if want := []string{"completed"}; !slices.Equal(got[SectionHistory], want) {
		t.Fatalf("history = %v, want %v", got[SectionHistory], want)
	}
	wantPlanned := []string{"running", "ready-must", "post-first", "ready-low", "unranked", "deferred", "obsolete"}
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

func TestBuildSectionsRoutesInspectActionsToAttention(t *testing.T) {
	rows := []monitor.Row{
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "warning", Status: plan.StatusInvalid, Warnings: []string{"repository unavailable"}},
		{Kind: monitor.RowKindPlan, RepositoryID: "repo", PlanID: "invalid", Status: plan.StatusInvalid, Warnings: []string{"invalid state.json"}},
	}

	sections := BuildSections(rows, true)
	for _, section := range sections {
		switch section.Kind {
		case SectionAttention:
			if len(section.Rows) != 2 || section.Rows[0].Kind != monitor.RowKindRepositoryWarning || section.Rows[1].PlanID != "invalid" {
				t.Fatalf("attention rows = %+v, want repository warning and visible invalid plan", section.Rows)
			}
		case SectionPlanned:
			if len(section.Rows) != 0 {
				t.Fatalf("planned rows = %+v, want no effective INSPECT rows", section.Rows)
			}
		}
	}
}

func TestBuildSectionsRoutesOnlyMergeActionsToReadyToMerge(t *testing.T) {
	rows := []monitor.Row{
		{PlanID: "done", Status: plan.StatusCompleted},
		{PlanID: "merge", Status: plan.StatusCompleted, NextAction: "MERGE"},
	}

	sections := BuildSections(rows, true)
	got := make(map[SectionKind][]string)
	for _, section := range sections {
		for _, row := range section.Rows {
			got[section.Kind] = append(got[section.Kind], row.PlanID)
		}
	}
	if want := []string{"merge"}; !slices.Equal(got[SectionReadyToMerge], want) {
		t.Fatalf("ready to merge = %v, want only effective MERGE rows %v", got[SectionReadyToMerge], want)
	}
	if want := []string{"done"}; !slices.Equal(got[SectionHistory], want) {
		t.Fatalf("history = %v, want visible DONE row %v", got[SectionHistory], want)
	}
}

func TestBuildSectionsLimitsHistoryToFifteenPlans(t *testing.T) {
	rows := make([]monitor.Row, maxHistoryPlans+2)
	for index := range rows {
		status := plan.StatusCompleted
		if index%2 != 0 {
			status = plan.StatusAbandoned
		}
		rows[index] = monitor.Row{PlanID: fmt.Sprintf("history-%02d", index), Status: status}
	}

	sections := BuildSections(rows, true)
	for _, section := range sections {
		if section.Kind != SectionHistory {
			continue
		}
		if len(section.Rows) != maxHistoryPlans {
			t.Fatalf("history row count = %d, want %d", len(section.Rows), maxHistoryPlans)
		}
		if section.Rows[0].PlanID != "history-00" || section.Rows[maxHistoryPlans-1].PlanID != "history-14" {
			t.Fatalf("history rows did not retain collector order: first=%q last=%q", section.Rows[0].PlanID, section.Rows[maxHistoryPlans-1].PlanID)
		}
		return
	}
	t.Fatal("history section missing")
}

func TestBuildSectionsHandlesEmptyAndHiddenHistorySections(t *testing.T) {
	tests := []struct {
		name        string
		rows        []monitor.Row
		showHistory bool
		wantHistory int
	}{
		{name: "empty", showHistory: true},
		{name: "terminal outcomes shown", rows: []monitor.Row{{Status: plan.StatusCompleted}, {Status: plan.StatusAbandoned}}, showHistory: true, wantHistory: 2},
		{name: "history hidden", rows: []monitor.Row{{Status: plan.StatusCompleted, AttentionReasons: []monitor.AttentionReason{monitor.AttentionFinalizationFailed}}, {Status: plan.StatusAbandoned}}, showHistory: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sections := BuildSections(test.rows, test.showHistory)
			if len(sections) != 4 {
				t.Fatalf("section count = %d, want 4", len(sections))
			}
			for _, section := range sections {
				want := 0
				if section.Kind == SectionHistory {
					want = test.wantHistory
				}
				if len(section.Rows) != want {
					t.Errorf("%s row count = %d, want %d", section.Kind, len(section.Rows), want)
				}
			}
		})
	}
}
