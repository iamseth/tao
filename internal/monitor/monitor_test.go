package monitor

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

type fakeInventory struct {
	entries []taodata.RepoInventoryEntry
	err     error
}

func (f fakeInventory) MetadataInventory() ([]taodata.RepoInventoryEntry, error) {
	return f.entries, f.err
}

type fakePlanLister struct {
	summaries []plan.PlanSummary
	err       error
	calls     int
}

func (f *fakePlanLister) ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error) {
	f.calls++
	return f.summaries, f.err
}

type fakeStatusReader struct {
	records map[string]runstatus.Record
	errors  map[string]error
	reads   []string
}

func (f *fakeStatusReader) Read(planID string) (runstatus.Record, error) {
	f.reads = append(f.reads, planID)
	if err := f.errors[planID]; err != nil {
		return runstatus.Record{}, err
	}
	if record, ok := f.records[planID]; ok {
		return record, nil
	}
	return runstatus.Record{}, os.ErrNotExist
}

func TestCollectorBuildsAndOrdersCrossRepositorySnapshot(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 30, 0, 0, time.UTC)
	activity := func(age time.Duration) *time.Time {
		value := now.Add(-age)
		return &value
	}
	alpha := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo-alpha", Name: "alpha"}, PlansDir: "/data/alpha/plans", RuntimeStatusDir: "/data/alpha/run-status"}
	beta := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo-beta", Name: "beta"}, PlansDir: "/data/beta/plans", RuntimeStatusDir: "/data/beta/run-status"}
	broken := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo-broken"}, MetadataError: errors.New("malformed repository metadata")}
	alphaPlans := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "other", Title: "Other", Status: plan.StatusPlanned, PendingCount: 2, LastActivityAt: activity(time.Minute)},
		{ID: "blocked", Title: "Blocked", Status: plan.StatusBlocked, PendingCount: 1, LastActivityAt: activity(5 * time.Minute)},
		{ID: "completed", Status: plan.StatusCompleted, LastActivityAt: activity(time.Second)},
		{ID: "invalid", Status: plan.StatusInvalid, Warnings: []string{"invalid state.json"}},
		{ID: "live", Title: "Live", Status: plan.StatusInProgress, CurrentSliceID: "001-durable", CurrentSlice: &plan.Slice{ID: "001-durable", Title: "Durable"}, PendingCount: 3, LastActivityAt: activity(time.Hour), OriginalCompletedCount: 1, OriginalTotalCount: 3, ReworkCompletedCount: 2, ReworkTotalCount: 2, Warnings: []string{"artifact warning"}},
	}}
	betaPlans := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "changes", Status: plan.StatusChangesRequested, LastActivityAt: activity(4 * time.Minute)},
		{ID: "stale", Status: plan.StatusInProgress, LastActivityAt: activity(10 * time.Minute)},
		{ID: "old", Status: plan.StatusInReview, LastActivityAt: activity(30 * time.Minute)},
	}}
	alphaStatus := &fakeStatusReader{records: map[string]runstatus.Record{
		"live": runtimeRecord(alpha.Repo.ID, "live", now.Add(-5*time.Minute), now.Add(-runstatus.StaleThreshold+time.Nanosecond), runstatus.Phase("implement"), &runstatus.SliceDetail{ID: "r201-runtime", Title: "Runtime"}),
	}, errors: map[string]error{}}
	betaStatus := &fakeStatusReader{records: map[string]runstatus.Record{
		"stale": runtimeRecord(beta.Repo.ID, "stale", now.Add(-7*time.Minute), now.Add(-runstatus.StaleThreshold), runstatus.Phase("verify"), nil),
	}, errors: map[string]error{}}

	listers := map[string]*fakePlanLister{alpha.Repo.ID: alphaPlans, beta.Repo.ID: betaPlans}
	readers := map[string]*fakeStatusReader{alpha.Repo.ID: alphaStatus, beta.Repo.ID: betaStatus}
	collector := Collector{
		Inventory: fakeInventory{entries: []taodata.RepoInventoryEntry{beta, broken, alpha}},
		NewPlanLister: func(entry taodata.RepoInventoryEntry) PlanLister {
			return listers[entry.Repo.ID]
		},
		NewStatusReader: func(entry taodata.RepoInventoryEntry) RuntimeStatusReader {
			return readers[entry.Repo.ID]
		},
		Now: func() time.Time { return now },
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.CollectedAt.Equal(now) {
		t.Fatalf("CollectedAt = %s, want %s", snapshot.CollectedAt, now)
	}
	gotOrder := make([]string, 0, len(snapshot.Rows))
	byPlan := make(map[string]Row)
	for _, row := range snapshot.Rows {
		if row.PlanID == "" {
			gotOrder = append(gotOrder, "repo:"+row.RepositoryID)
		} else {
			gotOrder = append(gotOrder, row.PlanID)
			byPlan[row.PlanID] = row
		}
	}
	wantOrder := []string{"live", "stale", "changes", "blocked", "other", "old", "invalid", "repo:repo-broken"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("row order = %v, want %v", gotOrder, wantOrder)
	}
	if alphaPlans.calls != 1 || betaPlans.calls != 1 {
		t.Fatalf("ListPlans calls alpha/beta = %d/%d, want one each", alphaPlans.calls, betaPlans.calls)
	}
	if slices.Contains(alphaStatus.reads, "completed") {
		t.Fatalf("completed plan runtime record was read: %v", alphaStatus.reads)
	}
	if slices.Contains(alphaStatus.reads, "invalid") {
		t.Fatalf("invalid warning plan runtime record was read: %v", alphaStatus.reads)
	}

	live := byPlan["live"]
	if live.Liveness != LivenessLive || live.Phase != runstatus.Phase("implement") || live.InvocationDuration != 5*time.Minute {
		t.Fatalf("live runtime detail = %+v", live)
	}
	if live.SliceID != "r201-runtime" || live.SliceTitle != "Runtime" {
		t.Fatalf("live slice detail = %q %q", live.SliceID, live.SliceTitle)
	}
	if live.Left != 3 || live.OriginalCompletedCount != 1 || live.OriginalTotalCount != 3 || live.ReworkCompletedCount != 2 || live.ReworkTotalCount != 2 {
		t.Fatalf("live composition = %+v", live)
	}
	if !slices.Equal(live.Warnings, []string{"artifact warning"}) {
		t.Fatalf("live warnings = %v", live.Warnings)
	}
	if stale := byPlan["stale"]; stale.Liveness != LivenessStale || stale.InvocationDuration != 7*time.Minute {
		t.Fatalf("exact-boundary stale row = %+v", stale)
	}
	if invalid := byPlan["invalid"]; invalid.Kind != RowKindPlan || !slices.Equal(invalid.Warnings, []string{"invalid state.json"}) {
		t.Fatalf("invalid plan row = %+v", invalid)
	}
	warning := snapshot.Rows[len(snapshot.Rows)-1]
	if warning.Kind != RowKindRepositoryWarning || warning.Status != plan.StatusInvalid || len(warning.Warnings) != 1 {
		t.Fatalf("repository warning row = %+v", warning)
	}
}

func TestCollectorPreservesRuntimeWarningsAndDurableSliceForMissingRecord(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}}
	lister := &fakePlanLister{summaries: []plan.PlanSummary{
		{ID: "missing", Status: plan.StatusInProgress, CurrentSliceID: "001-work", CurrentSlice: &plan.Slice{ID: "001-work", Title: "Work"}},
		{ID: "malformed", Status: plan.StatusPlanned, Warnings: []string{"artifact warning"}},
		{ID: "future", Status: plan.StatusInProgress},
	}}
	reader := &fakeStatusReader{
		records: map[string]runstatus.Record{
			"future": runtimeRecord(entry.Repo.ID, "future", now.Add(time.Minute), now, runstatus.Phase("prepare"), nil),
		},
		errors: map[string]error{"malformed": errors.New("decode runtime status: invalid json")},
	}
	collector := Collector{
		Inventory:       fakeInventory{entries: []taodata.RepoInventoryEntry{entry}},
		NewPlanLister:   func(taodata.RepoInventoryEntry) PlanLister { return lister },
		NewStatusReader: func(taodata.RepoInventoryEntry) RuntimeStatusReader { return reader },
		Now:             func() time.Time { return now },
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byPlan := make(map[string]Row)
	for _, row := range snapshot.Rows {
		byPlan[row.PlanID] = row
	}
	missing := byPlan["missing"]
	if missing.Liveness != LivenessMissing || missing.SliceID != "001-work" || missing.SliceTitle != "Work" || len(missing.Warnings) != 0 {
		t.Fatalf("missing runtime row = %+v", missing)
	}
	malformed := byPlan["malformed"]
	if malformed.Liveness != LivenessMissing || !slices.Equal(malformed.Warnings, []string{"artifact warning", "decode runtime status: invalid json"}) {
		t.Fatalf("malformed runtime row = %+v", malformed)
	}
	future := byPlan["future"]
	if future.Liveness != LivenessLive || future.InvocationDuration != 0 {
		t.Fatalf("future-start runtime row = %+v", future)
	}
}

func runtimeRecord(repoID, planID string, startedAt, heartbeatAt time.Time, phase runstatus.Phase, detail *runstatus.SliceDetail) runstatus.Record {
	return runstatus.Record{
		Schema:              runstatus.Schema,
		RepoID:              repoID,
		PlanID:              planID,
		Phase:               phase,
		Slice:               detail,
		InvocationStartedAt: startedAt,
		HeartbeatAt:         heartbeatAt,
	}
}
