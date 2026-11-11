package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestParkCommandsRoundTripAndContention(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked)
	repo := plan.NewFileRepository(fixture.root)
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out})
	ctx := context.Background()
	for _, args := range [][]string{{fixture.id, "--reason", " owner decision "}, {"--reason", "replacement", fixture.id}} {
		if err := app.setParking(ctx, repo, args, true); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := repo.ResolvePlan(ctx, fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.Parking == nil || detail.State.Plan.Parking.Reason != "owner decision" || detail.State.Status != plan.StatusBlocked {
		t.Fatalf("state = %+v", detail.State)
	}
	lock, err := plan.AcquireRunLock(detail.Dir, fixture.id, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	err = app.setParking(ctx, repo, []string{fixture.id}, false)
	if !errors.Is(err, plan.ErrRunLocked) {
		t.Fatalf("contention = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := app.setParking(ctx, repo, []string{fixture.id}, false); err != nil {
			t.Fatal(err)
		}
	}
	detail, err = repo.ResolvePlan(ctx, fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.Parking != nil || detail.State.Status != plan.StatusBlocked {
		t.Fatalf("state = %+v", detail.State)
	}
	counts := map[string]int{}
	for _, event := range detail.Events {
		if event.Type == plan.EventTypePlanParked || event.Type == plan.EventTypePlanUnparked {
			counts[event.Type]++
			if event.MutationID == "" || event.PlanID != fixture.id || event.Timestamp.IsZero() {
				t.Fatalf("event = %+v", event)
			}
		}
	}
	if counts[plan.EventTypePlanParked] != 1 || counts[plan.EventTypePlanUnparked] != 1 {
		t.Fatalf("events = %v", counts)
	}
}

func TestParkCommandsResolvePaths(t *testing.T) {
	for _, name := range []string{"relative", "absolute outside plans directory", "absolute outside with duplicate ID"} {
		t.Run(name, func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked)
			root, input := fixture.root, fixture.dir
			if name == "relative" {
				t.Chdir(fixture.root)
				input = "./" + fixture.id
			} else {
				root = t.TempDir()
				if name == "absolute outside with duplicate ID" {
					writeRunPlan(t, root, fixture.id, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
				}
			}
			repo := plan.NewFileRepository(root)
			app := newTestApp(App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
			ctx := context.Background()
			for _, parked := range []bool{true, false} {
				args := []string{input}
				if parked {
					args = append(args, "--reason", "owner decision")
				}
				if err := app.setParking(ctx, repo, args, parked); err != nil {
					t.Fatal(err)
				}
				detail, err := repo.ResolvePlan(ctx, fixture.dir)
				if err != nil {
					t.Fatal(err)
				}
				if (detail.State.Plan.Parking != nil) != parked || detail.State.Status != plan.StatusBlocked {
					t.Fatalf("parked=%v state=%+v", parked, detail.State)
				}
				if name == "absolute outside with duplicate ID" {
					other, err := repo.ResolvePlan(ctx, fixture.id)
					if err != nil {
						t.Fatal(err)
					}
					if other.State.Plan.Parking != nil {
						t.Fatal("parked the unrelated plan with the same ID")
					}
				}
			}
		})
	}
}

func TestParkReloadsUnderRunLock(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	inner := plan.NewFileRepository(fixture.root)
	repo := &abandonReloadRepository{inner: inner}
	repo.beforeReload = func() {
		detail, err := inner.ResolvePlan(context.Background(), fixture.id)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := plan.AcquireRunLock(detail.Dir, fixture.id, time.Now())
		if err == nil {
			_ = lock.Release()
			t.Fatal("reload outside run lock")
		}
		if !errors.Is(err, plan.ErrRunLocked) {
			t.Fatal(err)
		}
		detail.State.Status = plan.StatusCompleted
		writeAbandonDetail(t, detail)
	}
	app := newTestApp(App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err := app.setParking(context.Background(), repo, []string{fixture.id, "--reason", "decision"}, true); err != nil {
		t.Fatal(err)
	}
	detail, err := inner.ResolvePlan(context.Background(), fixture.id)
	if err != nil || repo.calls != 2 || detail.State.Status != plan.StatusCompleted || detail.State.Plan.Parking == nil {
		t.Fatalf("reload calls=%d detail=%+v err=%v", repo.calls, detail, err)
	}
}

func TestParkInvalidArguments(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	repo := plan.NewFileRepository(fixture.root)
	app := newTestApp(App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	for _, args := range [][]string{{}, {fixture.id}, {fixture.id, "--reason", " "}, {fixture.id, "extra", "--reason", "valid"}, {fixture.id, "--reason", strings.Repeat("x", 1001)}} {
		if err := app.setParking(context.Background(), repo, args, true); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
