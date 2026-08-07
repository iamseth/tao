package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestAcquirePlanLocksUsesStableOrderAndRollsBackOnContention(t *testing.T) {
	withPlanRunLockSettings(t, time.Hour, func(pid int) bool { return true })
	dirA, dirB := t.TempDir(), t.TempDir()
	held, err := acquirePlanRunLock(dirB, "plan-b", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })

	_, err = AcquirePlanLocks([]PlanLockRequest{{PlanID: "plan-b", PlanDir: dirB}, {PlanID: "plan-a", PlanDir: dirA}}, time.Now())
	if err == nil {
		t.Fatal("expected plan-b contention")
	}
	pathA, pathErr := planRunLockPath(dirA)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(pathA); !os.IsNotExist(statErr) {
		t.Fatalf("plan-a lock was not rolled back: %v", statErr)
	}
}

func TestAcquirePlanLocksContendsWithOrdinaryPlanLock(t *testing.T) {
	withPlanRunLockSettings(t, time.Hour, func(pid int) bool { return true })
	dir := t.TempDir()
	batch, err := AcquirePlanLocks([]PlanLockRequest{{PlanID: "plan-a", PlanDir: dir}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Release() })
	if _, err := acquirePlanRunLock(dir, "plan-a", time.Now()); !errors.Is(err, errPlanRunLocked) {
		t.Fatalf("ordinary lock should contend with batch ownership, got %v", err)
	}
}

func TestWithPlanRunLockIsReentrantAndReleasesAfterError(t *testing.T) {
	withPlanRunLockSettings(t, time.Hour, func(pid int) bool { return true })
	detail := &plan.PlanDetail{Dir: t.TempDir(), State: plan.State{Plan: plan.PlanState{ID: "plan-a"}}}
	operationErr := errors.New("operation failed")
	competitorDone := make(chan error, 1)
	nestedRan := false

	err := WithPlanRunLock(context.Background(), detail, time.Now(), func(ownedCtx context.Context) error {
		if err := WithPlanRunLock(ownedCtx, detail, time.Now(), func(context.Context) error {
			nestedRan = true
			return nil
		}); err != nil {
			return err
		}
		// The competitor deliberately starts a separate lifecycle request rather
		// than inheriting the lock-owning context under test.
		go func() { //nolint:gosec // G118: independent context is the contention condition
			competitorDone <- WithPlanRunLock(context.Background(), detail, time.Now(), func(context.Context) error {
				return errors.New("competitor unexpectedly ran")
			})
		}()
		select {
		case competitorErr := <-competitorDone:
			if !errors.Is(competitorErr, ErrCannotStart) {
				t.Fatalf("competing lock error = %v, want ErrCannotStart", competitorErr)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for competing lifecycle driver")
		}
		return operationErr
	})
	if !errors.Is(err, operationErr) {
		t.Fatalf("operation error = %v, want %v", err, operationErr)
	}
	if !nestedRan {
		t.Fatal("nested lifecycle operation did not run re-entrantly")
	}

	releasedRan := false
	if err := WithPlanRunLock(context.Background(), detail, time.Now(), func(context.Context) error {
		releasedRan = true
		return nil
	}); err != nil {
		t.Fatalf("lock was not released after callback error: %v", err)
	}
	if !releasedRan {
		t.Fatal("operation after release did not run")
	}
}

func TestAcquirePlanRunLockCreatesLockFile(t *testing.T) {
	planDir := t.TempDir()
	createdAt := time.Date(2026, 6, 28, 3, 0, 0, 123, time.FixedZone("test", -5*60*60))

	lock, err := acquirePlanRunLock(planDir, "plan-a", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	path, err := planRunLockPath(planDir)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path) //nolint:gosec // G304: test reads a path derived from t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	metadata := parsePlanRunLockMetadata(content)
	if metadata.PID != os.Getpid() {
		t.Fatalf("expected lock pid %d, got %d in %q", os.Getpid(), metadata.PID, string(content))
	}
	if !metadata.CreatedAt.Equal(createdAt.UTC()) {
		t.Fatalf("expected created_at %s, got %s", createdAt.UTC(), metadata.CreatedAt)
	}
	if metadata.PlanID != "plan-a" || metadata.Token == "" {
		t.Fatalf("expected plan id and token in lock metadata, got %+v", metadata)
	}
}

func TestAcquirePlanRunLockFailsWhenLiveLockExists(t *testing.T) {
	withPlanRunLockSettings(t, time.Hour, func(pid int) bool { return true })
	planDir := t.TempDir()
	createdAt := time.Date(2026, 6, 28, 3, 5, 0, 0, time.UTC)
	lock, err := acquirePlanRunLock(planDir, "plan-a", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	_, err = acquirePlanRunLock(planDir, "plan-a", createdAt.Add(time.Minute))
	if err == nil {
		t.Fatal("expected contended lock acquisition to fail")
	}
	if !errors.Is(err, errPlanRunLocked) || !errors.Is(err, ErrCannotStart) {
		t.Fatalf("expected plan lock/cannot-start classification, got %v", err)
	}
	if !strings.Contains(err.Error(), "plan plan-a is already running") || !strings.Contains(err.Error(), "pid") {
		t.Fatalf("expected clear contention error, got %v", err)
	}
}

func TestAcquirePlanRunLockTakesOverStaleLocks(t *testing.T) {
	t.Run("dead pid", func(t *testing.T) {
		withPlanRunLockSettings(t, time.Hour, func(pid int) bool { return false })
		planDir := t.TempDir()
		path, err := planRunLockPath(planDir)
		if err != nil {
			t.Fatal(err)
		}
		oldContent := formatPlanRunLockContent("plan-a", 987654, time.Date(2026, 6, 28, 3, 10, 0, 0, time.UTC), "old")
		if err := os.WriteFile(path, oldContent, 0o600); err != nil {
			t.Fatal(err)
		}

		lock, err := acquirePlanRunLock(planDir, "plan-a", time.Date(2026, 6, 28, 3, 11, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lock.Release() })
		content, err := os.ReadFile(path) //nolint:gosec // G304: test reads a path derived from t.TempDir.
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(content, oldContent) {
			t.Fatal("expected stale lock content to be replaced")
		}
		if metadata := parsePlanRunLockMetadata(content); metadata.PID != os.Getpid() {
			t.Fatalf("expected takeover lock to use current pid, got %+v", metadata)
		}
	})

	t.Run("mtime timeout", func(t *testing.T) {
		withPlanRunLockSettings(t, time.Minute, func(pid int) bool { return true })
		planDir := t.TempDir()
		path, err := planRunLockPath(planDir)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 6, 28, 3, 20, 0, 0, time.UTC)
		oldContent := formatPlanRunLockContent("plan-a", os.Getpid(), now.Add(-2*time.Hour), "old")
		if err := os.WriteFile(path, oldContent, 0o600); err != nil {
			t.Fatal(err)
		}
		oldModTime := now.Add(-2 * time.Minute)
		if err := os.Chtimes(path, oldModTime, oldModTime); err != nil {
			t.Fatal(err)
		}

		lock, err := acquirePlanRunLock(planDir, "plan-a", now)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lock.Release() })
		content, err := os.ReadFile(path) //nolint:gosec // G304: test reads a path derived from t.TempDir.
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(content, oldContent) {
			t.Fatal("expected timed-out lock content to be replaced")
		}
	})
}

func TestPlanRunLockReleaseRemovesLockFile(t *testing.T) {
	planDir := t.TempDir()
	lock, err := acquirePlanRunLock(planDir, "plan-a", time.Date(2026, 6, 28, 3, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := lock.path
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected lock file removed, got err=%v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("expected second release to be a no-op, got %v", err)
	}
}

func TestServiceExecuteReloadsPlanAfterAcquiringRunLock(t *testing.T) {
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

	err := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{
		StatusReporter: reporter,
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
	withPlanRunLockSettings(t, time.Hour, func(pid int) bool { return true })
	planDir := t.TempDir()
	held, err := acquirePlanRunLock(planDir, "plan-a", time.Date(2026, 6, 28, 3, 40, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = t.TempDir()
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
	executor := &countingSliceExecutor{}

	err = NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{SliceExecutor: executor}}).Execute(context.Background(), Request{Input: "plan-a"})
	if err == nil {
		t.Fatal("expected contended lock error")
	}
	if !errors.Is(err, errPlanRunLocked) || !errors.Is(err, ErrCannotStart) {
		t.Fatalf("expected plan lock/cannot-start classification, got %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("expected executor not to run, got %d calls", executor.calls)
	}
}

type callbackStatusReporter func()

func (r callbackStatusReporter) Track(_ string, operation func() error) error {
	r()
	return operation()
}

func withPlanRunLockSettings(t *testing.T, timeout time.Duration, live func(pid int) bool) {
	t.Helper()
	oldTimeout := planRunLockTimeout
	oldLive := planRunLockProcessLive
	planRunLockTimeout = timeout
	planRunLockProcessLive = live
	t.Cleanup(func() {
		planRunLockTimeout = oldTimeout
		planRunLockProcessLive = oldLive
	})
}
