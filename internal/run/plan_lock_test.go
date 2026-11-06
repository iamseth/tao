package run

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestWithPlanRunLockContentionMatchesPlanAndRunErrors(t *testing.T) {
	t.Cleanup(plan.SetRunLockSettingsForTest(time.Hour, func(int) bool { return true }))
	detail := &plan.PlanDetail{Dir: t.TempDir(), State: plan.State{Plan: plan.PlanState{ID: "plan-a"}}}
	held, err := plan.AcquireRunLock(detail.Dir, detail.State.Plan.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })

	err = WithPlanRunLock(context.Background(), detail, time.Now(), func(context.Context) error {
		t.Fatal("operation ran without lock ownership")
		return nil
	})
	if !errors.Is(err, ErrCannotStart) || !errors.Is(err, plan.ErrRunLocked) {
		t.Fatalf("contention error = %v, want ErrCannotStart and plan.ErrRunLocked", err)
	}
	if !errors.Is(errors.Unwrap(err), plan.ErrRunLocked) {
		t.Fatalf("unwrapped error = %v, want plan.ErrRunLocked", errors.Unwrap(err))
	}
}

func TestServiceExecuteReloadsPlanAfterAcquiringRunLock(t *testing.T) {
	t.Parallel()
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	initial := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	initial.Dir = planDir
	initial.State.Repo.Root = repoRoot
	completed := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completed.Dir = planDir
	completed.State.Repo.Root = repoRoot
	repo := &memoryRunRepository{details: []*plan.PlanDetail{initial}}
	preparerCalls := 0
	executor := &countingSliceExecutor{}
	reporter := callbackStatusReporter(func() {
		// Track begins after the request's initial resolution and before the plan
		// lock is acquired, deterministically simulating a competing lifecycle
		// driver settling authoritative state in that interval.
		repo.details = []*plan.PlanDetail{completed}
	})

	err := newTestService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		RepositoryAdmission: testRepositoryAdmission,
		StatusReporter:      reporter,
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			preparerCalls++
			return repoRoot, nil
		},
		SliceExecutor: executor,
	}}).Execute(context.Background(), Request{Input: "plan-a"})
	if err == nil || !errors.Is(err, ErrCannotStart) || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("execute error = %v, want refreshed completed-plan refusal", err)
	}
	if preparerCalls != 0 || executor.calls != 0 {
		t.Fatalf("workspace preparer calls = %d, executor calls = %d; want no stale execution", preparerCalls, executor.calls)
	}
}

func TestServiceExecuteFailsFastWhenPlanRunLockHeld(t *testing.T) {
	t.Cleanup(plan.SetRunLockSettingsForTest(time.Hour, func(pid int) bool { return true }))
	planDir := t.TempDir()
	held, err := plan.AcquireRunLock(planDir, "plan-a", time.Date(2026, 6, 28, 3, 40, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
	executor := &countingSliceExecutor{}

	err = newTestService(repo, io.Discard, Options{RunDependencies: RunDependencies{RepositoryAdmission: testRepositoryAdmission, SliceExecutor: executor}}).Execute(context.Background(), Request{Input: "plan-a"})
	if err == nil {
		t.Fatal("expected contended lock error")
	}
	if !errors.Is(err, plan.ErrRunLocked) || !errors.Is(err, ErrCannotStart) {
		t.Fatalf("expected plan lock/cannot-start classification, got %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("expected executor not to run, got %d calls", executor.calls)
	}
}

func TestServiceOwnershipAbandonmentRace(t *testing.T) {
	for _, outer := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "outer"}[outer], func(t *testing.T) {
			initial := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
			initial.Dir = t.TempDir()
			refreshed := *initial
			refreshed.State = initial.State
			refreshed.State.Status = plan.StatusAbandoned
			repo := &memoryRunRepository{details: []*plan.PlanDetail{initial}}
			service := newTestService(repo, io.Discard, testOptions(RunDependencies{StatusReporter: callbackStatusReporter(func() {
				repo.details = []*plan.PlanDetail{&refreshed}
			})}))
			var err error
			if outer {
				err = service.WithPlanRunLock(context.Background(), Request{Input: initial.Dir}, func(context.Context) error {
					t.Error("operation reached abandoned plan")
					return nil
				})
			} else {
				err = service.Execute(context.Background(), Request{Input: initial.Dir})
			}
			if !errors.Is(err, ErrCannotStart) || !strings.Contains(err.Error(), "abandoned") {
				t.Fatalf("abandonment classification = %v", err)
			}
		})
	}
}

func TestResolvedPlanPreservesCallbackError(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	service := newTestService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, testOptions(RunDependencies{}))
	want := errors.New("callback failed")
	for _, admission := range []bool{false, true} {
		got := service.withResolvedPlan(context.Background(), Request{Input: detail.Dir}, resolvedPlanOptions{admission: admission}, func(context.Context, *plan.PlanDetail, ExecutionConfig) error { return want })
		if got != want { //nolint:errorlint // Callback identity is part of the boundary contract.
			t.Fatalf("callback error = %v, want original", got)
		}
		lock, err := plan.AcquireRunLock(detail.Dir, detail.State.Plan.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_ = lock.Release()
	}
}

func TestServiceOwnershipCanceledAcquisition(t *testing.T) {
	for _, outer := range []bool{false, true} {
		detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
		detail.Dir = t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
		service := newTestService(repo, io.Discard, testOptions(RunDependencies{StatusReporter: callbackStatusReporter(cancel)}))
		var err error
		if outer {
			err = service.WithPlanRunLock(ctx, Request{Input: detail.Dir}, func(context.Context) error { t.Fatal("canceled callback entered"); return nil })
		} else {
			err = service.Execute(ctx, Request{Input: detail.Dir})
		}
		cancel()
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrCannotStart) {
			t.Fatalf("canceled acquisition = %v", err)
		}
		if repo.calls != 1 {
			t.Fatalf("canceled acquisition reloaded: %d", repo.calls)
		}
		lock, err := plan.AcquireRunLock(detail.Dir, detail.State.Plan.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_ = lock.Release()
	}
}

func TestServiceOuterOwnershipReleasesOnPanic(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	service := newTestService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, testOptions(RunDependencies{}))
	func() {
		defer func() {
			if recover() != "operation panic" {
				t.Error("panic not preserved")
			}
		}()
		_ = service.WithPlanRunLock(context.Background(), Request{Input: detail.Dir}, func(context.Context) error { panic("operation panic") })
	}()
	if err := service.WithPlanRunLock(context.Background(), Request{Input: detail.Dir}, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("panic retained ownership: %v", err)
	}
}

type callbackStatusReporter func()

func (r callbackStatusReporter) Track(_ string, operation func() error) error {
	r()
	return operation()
}
