package tui

import (
	"slices"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runqueue"
)

func TestBuildSectionsGroupsEveryRowAndPreservesOrder(t *testing.T) {
	rows := []monitor.Row{
		{PlanID: "running-first", Liveness: monitor.LivenessLive},
		{PlanID: "attention-first", AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
		{PlanID: "queued-pending", QueueStatus: runqueue.QueueStatusPending},
		{PlanID: "queued-running", QueueStatus: runqueue.QueueStatusRunning},
		{PlanID: "planned", Status: plan.StatusInReview},
		{PlanID: "completed", Status: plan.StatusCompleted, QueueStatus: runqueue.QueueStatusSucceeded},
		{PlanID: "stalled", Liveness: monitor.LivenessStale, RunLockPresent: true, RunLockProcessAlive: true, AttentionReasons: []monitor.AttentionReason{monitor.AttentionApprovalRequired}},
		{PlanID: "attention-second", Liveness: monitor.LivenessStale, AttentionReasons: []monitor.AttentionReason{monitor.AttentionRunCrashed}},
	}

	sections := BuildSections(rows, true)
	want := map[SectionKind][]string{
		SectionAttention: {"attention-first", "attention-second"},
		SectionRunning:   {"running-first", "stalled"},
		SectionQueued:    {"queued-pending", "queued-running"},
		SectionPlanned:   {"planned"},
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

func TestBuildSectionsTreatsTerminalQueueEntriesAsHistory(t *testing.T) {
	rows := []monitor.Row{
		{PlanID: "succeeded", Status: plan.StatusInReview, QueueStatus: runqueue.QueueStatusSucceeded},
		{PlanID: "failed", Status: plan.StatusInReview, QueueStatus: runqueue.QueueStatusFailed},
		{PlanID: "skipped", Status: plan.StatusInReview, QueueStatus: runqueue.QueueStatusSkipped},
	}

	sections := BuildSections(rows, true)
	for _, section := range sections {
		switch section.Kind {
		case SectionQueued:
			if len(section.Rows) != 0 {
				t.Errorf("queued rows = %v, want none", section.Rows)
			}
		case SectionPlanned:
			var got []string
			for _, row := range section.Rows {
				got = append(got, row.PlanID)
			}
			want := []string{"succeeded", "failed", "skipped"}
			if !slices.Equal(got, want) {
				t.Errorf("planned rows = %v, want %v", got, want)
			}
		}
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
			if len(sections) != 5 {
				t.Fatalf("section count = %d, want 5", len(sections))
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
