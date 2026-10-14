package tui

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

func TestLiveMergeNextAction(t *testing.T) {
	row := monitor.Row{PlanID: "plan-a", Status: plan.StatusReviewed, MergeInProgress: true,
		RecommendedAction: plan.PlanAction{Kind: plan.PlanActionRecoverMerge, Command: "tao merge plan-a", Reason: "the exact intended squash is already present"}}
	if got := planNextAction(row); got != "MERGING" {
		t.Fatalf("next = %q", got)
	}
	if got := planNextActionDisplay(row); got != "MERGING" {
		t.Fatalf("display = %q", got)
	}
	row.Phase = "merge_verifying"
	if got := planNextActionDisplay(row); got != "MERGING · merge: verifying" {
		t.Fatalf("display = %q", got)
	}
	sections := BuildPageSections([]monitor.Row{row}, Filter{}, PagePlans, Sort{})
	if len(sections) != 1 || len(sections[0].Rows) != 1 {
		t.Fatalf("live merge missing: %+v", sections)
	}
	row.MergeInProgress = false
	if got := planNextAction(row); got != "SETTLE MERGE" {
		t.Fatalf("next = %q", got)
	}
	if got := planNextActionDisplay(row); got != "tao merge plan-a — the exact intended squash is already present" {
		t.Fatalf("display = %q", got)
	}
}

func TestPageSectionsPreserveCollectorPositions(t *testing.T) {
	rows := []monitor.Row{
		{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "warning", Status: plan.StatusInvalid},
		{PlanID: "invalid", Status: plan.StatusInvalid},
		{PlanID: "monitor", Status: plan.StatusInProgress, Liveness: monitor.LivenessLive},
		{PlanID: "blocked", Status: plan.StatusBlocked},
		{PlanID: "merge", Status: plan.StatusReviewed},
		{PlanID: "review", Status: plan.StatusInReview},
		{PlanID: "planned", Status: plan.StatusPlanned},
		{PlanID: "done", Status: plan.StatusCompleted},
		{PlanID: "abandoned", Status: plan.StatusAbandoned, Liveness: monitor.LivenessLive},
	}
	before := slices.Clone(rows)
	for page, want := range map[PageID][]monitor.Row{PagePlans: rows[:7], PageHistory: rows[7:]} {
		sections := BuildPageSections(rows, Filter{}, page, Sort{})
		if len(sections) != 1 || !reflect.DeepEqual(sections[0].Rows, want) {
			t.Fatalf("%s = %+v", page, sections)
		}
	}
	if !reflect.DeepEqual(rows, before) {
		t.Fatal("mutated snapshot")
	}
}

func TestOrderPlannedRows(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	older := now.Add(-time.Hour)
	rows := []monitor.Row{
		{PlanID: "running", Status: plan.StatusPlanned, Liveness: monitor.LivenessLive, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionObsolete}},
		{PlanID: "warning", Kind: monitor.RowKindRepositoryWarning, Status: plan.StatusPlanned},
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
	before := slices.Clone(rows)
	sections := BuildPageSections(rows, Filter{}, PagePlans, Sort{})
	var got []string
	for _, row := range sections[0].Rows {
		got = append(got, row.PlanID)
	}
	want := []string{"ready-must", "warning", "ready-low", "attention", "post-first", "unranked", "post-second", "deferred", "running", "obsolete"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(rows, before) {
		t.Fatal("mutated snapshot")
	}
	orderPlannedRows(rows)
	got = nil
	for _, row := range rows {
		got = append(got, row.PlanID)
	}
	if !slices.Equal(got, append(want, "completed")) {
		t.Fatalf("direct order = %v", got)
	}
}

func TestPriorityOrderHonorsValidSequenceAndIgnoresCycles(t *testing.T) {
	row := func(id string) monitor.Row {
		return monitor.Row{RepositoryID: "repo", PlanID: id, Status: plan.StatusPlanned, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady}}
	}
	before, after := row("before"), row("after")
	after.Relationships = []monitor.ResolvedRelationship{{PlanID: "before", Type: plan.PlanRelationAfter, State: monitor.RelationshipIncomplete}}
	cycleA, cycleB := row("cycle-a"), row("cycle-b")
	cycleA.Relationships = []monitor.ResolvedRelationship{{PlanID: "cycle-b", Type: plan.PlanRelationAfter, State: monitor.RelationshipCyclic}}
	cycleB.Relationships = []monitor.ResolvedRelationship{{PlanID: "cycle-a", Type: plan.PlanRelationAfter, State: monitor.RelationshipCyclic}}
	ordered := priorityOrder([]monitor.Row{after, cycleB, before, cycleA})
	var got []string
	for _, item := range ordered {
		got = append(got, item.PlanID)
	}
	if want := []string{"cycle-b", "before", "after", "cycle-a"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestPriorityOrderIgnoresEveryRepeatedTargetRelationship(t *testing.T) {
	row := func(id string) monitor.Row {
		return monitor.Row{RepositoryID: "repo", PlanID: id, Status: plan.StatusPlanned, Overview: plan.DecisionOverview{Disposition: plan.DecisionDispositionReady}}
	}
	before, after := row("before"), row("after")
	after.Relationships = []monitor.ResolvedRelationship{
		{PlanID: "before", Type: plan.PlanRelationAfter, State: monitor.RelationshipDuplicate},
		{PlanID: "before", Type: plan.PlanRelationBefore, State: monitor.RelationshipDuplicate},
	}
	ordered := priorityOrder([]monitor.Row{after, before})
	if got := []string{ordered[0].PlanID, ordered[1].PlanID}; !slices.Equal(got, []string{"after", "before"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestPageSectionsIncludesAllHistory(t *testing.T) {
	rows := make([]monitor.Row, 25)
	for i := range rows {
		status := plan.StatusCompleted
		if i%2 != 0 {
			status = plan.StatusAbandoned
		}
		rows[i] = monitor.Row{PlanID: fmt.Sprintf("history-%02d", i), Status: status}
	}
	sections := BuildPageSections(rows, Filter{}, PageHistory, Sort{})
	if len(sections) != 1 || !reflect.DeepEqual(sections[0].Rows, rows) {
		t.Fatalf("history = %+v", sections)
	}
}
