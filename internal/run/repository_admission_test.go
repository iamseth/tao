package run

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"

	"github.com/iamseth/tao/internal/testenv"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/repoadmission"
)

func TestRepositoryAdmissionRefusesRunEntries(t *testing.T) {
	testenv.SkipIfShort(t)
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil { // #nosec G204 -- test-owned temporary repository.
		t.Fatalf("init: %s: %v", out, err)
	}
	detail := &plan.PlanDetail{Dir: t.TempDir()}
	detail.State.Repo.Root = root
	detail.State.Plan.ID = "admission"
	err := repoadmission.With(context.Background(), repoadmission.Request{Root: root, Mode: repoadmission.Merge}, func(context.Context) error {
		for _, outer := range []bool{false, true} {
			repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
			service := NewService(repo, io.Discard, Options{})
			var err error
			if outer {
				err = service.WithPlanRunLock(context.Background(), Request{Input: detail.Dir}, func(context.Context) error { t.Fatal("entered operation"); return nil })
			} else {
				err = service.Execute(context.Background(), Request{Input: detail.Dir})
			}
			var busy *repoadmission.ContentionError
			if !errors.Is(err, ErrCannotStart) || !errors.As(err, &busy) {
				t.Errorf("outer=%v: %v", outer, err)
			}
			if repo.calls != 1 {
				t.Errorf("resolved after refusal: %d", repo.calls)
			}
			if len(detail.Events) != 0 {
				t.Fatal("refusal wrote events")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryAdmissionRetainsOuterOwnership(t *testing.T) {
	testenv.SkipIfShort(t)
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil { // #nosec G204 -- test-owned temporary repository.
		t.Fatalf("init: %s: %v", out, err)
	}
	first := &plan.PlanDetail{Dir: t.TempDir()}
	first.State.Repo.Root = root
	first.State.Plan.ID = "first"
	second := &plan.PlanDetail{Dir: t.TempDir()}
	second.State.Repo.Root = root
	second.State.Plan.ID = "second"
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{first}}, io.Discard, Options{})
	other := NewService(&memoryRunRepository{details: []*plan.PlanDetail{second}}, io.Discard, Options{})
	merge := func() error {
		return repoadmission.With(context.Background(), repoadmission.Request{Root: root, Mode: repoadmission.Merge}, func(context.Context) error { return nil })
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := service.WithPlanRunLock(ctx, Request{Input: first.Dir}, func(owned context.Context) error {
		// Separate owners may run different plans, but not the same plan.
		if err := other.WithPlanRunLock(context.Background(), Request{Input: second.Dir}, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := service.WithPlanRunLock(context.Background(), Request{Input: first.Dir}, func(context.Context) error { t.Fatal("same plan entered"); return nil }); !errors.Is(err, plan.ErrRunLocked) {
			t.Fatalf("same plan: %v", err)
		}
		for range 3 {
			if err := service.WithPlanRunLock(owned, Request{Input: first.Dir}, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			var busy *repoadmission.ContentionError
			if err := merge(); !errors.As(err, &busy) {
				t.Fatalf("between rounds: %v", err)
			}
		}
		cancel()
		var busy *repoadmission.ContentionError
		if err := merge(); !errors.As(err, &busy) {
			t.Fatalf("cancel released live callback: %v", err)
		}
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := merge(); err != nil {
		t.Fatalf("lease not released: %v", err)
	}
}

func TestRepositoryAdmissionGuardErrors(t *testing.T) {
	sentinel := errors.New("identity unavailable")
	detail := &plan.PlanDetail{Dir: t.TempDir()}
	detail.State.Repo.Root = "recorded-root"
	for _, outer := range []bool{false, true} {
		guard := func(_ context.Context, request repoadmission.Request, _ func(context.Context) error) error {
			if request.Root != "recorded-root" || request.Mode != repoadmission.Run {
				t.Fatalf("request: %+v", request)
			}
			return sentinel
		}
		repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
		service := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{RepositoryAdmission: guard}})
		var err error
		if outer {
			err = service.WithPlanRunLock(context.Background(), Request{Input: detail.Dir}, func(context.Context) error { t.Fatal("entered"); return nil })
		} else {
			err = service.Execute(context.Background(), Request{Input: detail.Dir})
		}
		if !errors.Is(err, ErrCannotStart) || !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		if repo.calls != 1 || len(detail.Events) != 0 {
			t.Fatal("refusal crossed mutation boundary")
		}
	}
	if err := WithRepositoryAdmission(context.Background(), detail, testRepositoryAdmission, func(context.Context) error { return sentinel }); !errors.Is(err, sentinel) || errors.Is(err, ErrCannotStart) {
		t.Fatalf("callback reclassified: %v", err)
	}
}
