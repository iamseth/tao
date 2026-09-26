package plantest_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

func repositoryDetail(id string) *plan.PlanDetail {
	return plantest.NewPlanDetail(id).
		WithPendingSlices("001-first").
		AddSlice(plantest.NewSlice("001-first").Build()).Build()
}

func TestRepositoryRecordPersistenceIsOptIn(t *testing.T) {
	t.Parallel()
	for _, persist := range []bool{false, true} {
		name := "default"
		if persist {
			name = "persisting"
		}
		t.Run(name, func(t *testing.T) {
			repo := plantest.NewRepository()
			if persist {
				repo = plantest.NewPersistingRepository()
			}
			const id = "20260926-013635-first"
			original := repositoryDetail(id)
			repo.AddDetail(original)
			// A separate detail ensures the write payload, not shared pointers,
			// is what makes this mutation visible on reload.
			working := repositoryDetail(id)
			working.Dir = original.Dir
			record, err := plan.NewPlanRecordWithStore(repo, working.Dir, working)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC)
			if err := record.StartSlice("001-first", plan.SliceStartRequest{StartedAt: now}); err != nil {
				t.Fatal(err)
			}
			loaded, err := repo.GetPlan(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if !persist {
				if loaded != original || loaded.State.Status != plan.StatusPlanned || loaded.Slices.Slices[0].Status != plan.StatusPending {
					t.Fatalf("default writes changed original detail: %+v", loaded)
				}
				for _, event := range loaded.Events {
					if event.Type == "slice_started" && event.SliceID == "001-first" {
						t.Fatal("default writes persisted slice_started")
					}
				}
				return
			}
			if loaded.State.Status != plan.StatusInProgress || loaded.State.Plan.CurrentSlice == nil || *loaded.State.Plan.CurrentSlice != "001-first" {
				t.Fatalf("state did not persist: %+v", loaded.State)
			}
			if loaded.Slices.Slices[0].Status != plan.StatusInProgress {
				t.Fatalf("slices did not persist: %+v", loaded.Slices)
			}
			found := false
			for _, event := range loaded.Events {
				if event.Type == "slice_started" && event.SliceID == "001-first" {
					found = true
					if event.PlanID != id || !event.Timestamp.Equal(now) {
						t.Fatalf("unexpected start event: %+v", event)
					}
				}
			}
			if !found {
				t.Fatal("reloaded detail is missing slice_started")
			}
		})
	}
}

func TestPersistingRepositoryReloadPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := plantest.NewPersistingRepository()
	const id = "20260926-013635-first"
	original := repositoryDetail(id)
	repo.AddDetail(original)
	other := repositoryDetail("20260926-013636-other")
	repo.AddDetail(other)
	record, err := repo.PlanRecord(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.RecordStartingBranch("test/persisted"); err != nil {
		t.Fatal(err)
	}
	for name, load := range map[string]func() (*plan.PlanDetail, error){
		"get":            func() (*plan.PlanDetail, error) { return repo.GetPlan(ctx, id) },
		"exact":          func() (*plan.PlanDetail, error) { return repo.GetPlanExact(ctx, id) },
		"resolve-id":     func() (*plan.PlanDetail, error) { return repo.ResolvePlan(ctx, id) },
		"resolve-prefix": func() (*plan.PlanDetail, error) { return repo.ResolvePlan(ctx, "20260926-013635") },
		"resolve-slug":   func() (*plan.PlanDetail, error) { return repo.ResolvePlan(ctx, "first") },
		"record": func() (*plan.PlanDetail, error) {
			r, err := repo.PlanRecord(original)
			if err != nil {
				return nil, err
			}
			return r.Detail(), nil
		},
		"resolve-record": func() (*plan.PlanDetail, error) {
			r, err := repo.ResolvePlanRecord(ctx, "first")
			if err != nil {
				return nil, err
			}
			return r.Detail(), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			loaded, err := load()
			if err != nil {
				t.Fatal(err)
			}
			if loaded.State.Repo.Branch != "test/persisted" {
				t.Fatalf("stale state: %+v", loaded.State.Repo)
			}
			loaded.State.Repo.Branch = "unpersisted"
			loaded.Slices.Slices[0].Status = plan.StatusCompleted
		})
	}
	loaded, err := repo.GetPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Repo.Branch != "test/persisted" || loaded.Slices.Slices[0].Status != plan.StatusPending {
		t.Fatal("unpersisted edits leaked into repository")
	}
	untouched, err := repo.GetPlan(ctx, other.State.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(untouched, other) {
		t.Fatal("writes leaked into another plan directory")
	}
	if original.State.Repo.Branch != "" {
		t.Fatal("record mutated the original fixture")
	}
	if found, err := repo.GetPlanExact(ctx, "first"); err != nil || found != nil {
		t.Fatalf("exact lookup resolved slug: %v, %v", found, err)
	}
}

func TestPersistingRepositoryAppendEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := plantest.NewPersistingRepository()
	detail := repositoryDetail("20260926-013635-first")
	detail.Events = []plan.Event{{Type: "plan_created", PlanID: detail.State.Plan.ID, Message: "original"}}
	repo.AddDetail(detail)
	exitCode := 1
	event := plan.Event{Type: "verification_completed", PlanID: detail.State.Plan.ID, SliceID: "001-first", ExitCode: &exitCode}
	if err := repo.AppendEvent(detail.Dir, event); err != nil {
		t.Fatal(err)
	}
	exitCode = 99
	for range 2 {
		loaded, err := repo.GetPlan(ctx, detail.State.Plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, got := range loaded.Events {
			switch got.Type {
			case "plan_created":
				found[got.Type] = true
				if got.Message != "original" {
					t.Fatalf("original event changed: %+v", got)
				}
			case "verification_completed":
				found[got.Type] = true
				if got.PlanID != detail.State.Plan.ID || got.SliceID != "001-first" || got.ExitCode == nil || *got.ExitCode != 1 {
					t.Fatalf("appended event changed: %+v", got)
				}
				*got.ExitCode = 42
			}
		}
		if !found["plan_created"] || !found["verification_completed"] {
			t.Fatalf("missing owned events: %v", found)
		}
	}
}

func TestPersistingRepositoryRejectsInvalidPayloads(t *testing.T) {
	t.Parallel()
	repo := plantest.NewPersistingRepository()
	detail := repositoryDetail("20260926-013635-first")
	repo.AddDetail(detail)
	for name, write := range map[string]func(string, []byte) error{
		"state": repo.WriteState, "slices": repo.WriteSlices,
	} {
		t.Run(name, func(t *testing.T) {
			if err := write(detail.Dir, []byte("{")); err == nil {
				t.Fatal("accepted malformed JSON")
			}
			loaded, err := repo.GetPlan(context.Background(), detail.State.Plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(loaded, detail) {
				t.Fatal("invalid write changed stored detail")
			}
		})
	}
}
