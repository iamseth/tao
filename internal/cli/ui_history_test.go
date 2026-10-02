package cli

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
)

func TestUIHistoryCollectorLoadsOldAndUndatedPlansWithoutChangingMonitorDefault(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-1, 0, 0)
	recent := now.Add(-time.Hour)
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}, PlansDir: t.TempDir(), RuntimeStatusDir: t.TempDir()}
	app := App{
		Now:      func() time.Time { return now },
		Registry: func() NoteRegistry { return monitorRegistryStub{entries: []taodata.RepoInventoryEntry{entry}} },
		Repository: func(string) Repository {
			return fakeRepository{summaries: []plan.PlanSummary{
				{ID: "active", Status: plan.StatusPlanned},
				{ID: "old", Status: plan.StatusCompleted, CompletedAt: &old},
				{ID: "recent", Status: plan.StatusCompleted, CompletedAt: &recent},
				{ID: "undated", Status: plan.StatusCompleted},
			}}
		},
	}
	for _, tt := range []struct {
		name   string
		all    bool
		window time.Duration
		want   []string
	}{
		{"all", true, 0, []string{"active", "old", "recent", "undated"}},
		{"bounded", false, 24 * time.Hour, []string{"active", "recent"}},
		{"none", false, 0, []string{"active"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			collector, err := app.newMonitorCollectorWithHistory(false, tt.window, tt.all)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := collector.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, row := range snapshot.Rows {
				ids = append(ids, row.PlanID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tt.want) {
				t.Fatalf("plans = %v, want %v", ids, tt.want)
			}
		})
	}
	collector, err := app.monitorCollector(false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := collector.Collect(context.Background())
	if err != nil || len(snapshot.Rows) != 1 || snapshot.Rows[0].PlanID != "active" {
		t.Fatalf("monitor default changed: %+v, %v", snapshot, err)
	}
}
