package tui

import (
	"slices"
	"testing"

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
