package plan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireRunLocksUsesStableOrderAndRollsBackOnContention(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	probed := false
	t.Cleanup(SetRunLockSettingsForTest(time.Hour, func(pid int) bool {
		probed = true
		if _, err := os.Stat(filepath.Join(dirA, runLockFileName)); err != nil {
			t.Fatalf("plan-a must be acquired before contending on plan-b: %v", err)
		}
		return true
	}))
	held, err := AcquireRunLock(dirB, "plan-b", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })

	_, err = AcquireRunLocks([]RunLockRequest{{PlanID: "plan-b", PlanDir: dirB}, {PlanID: "plan-a", PlanDir: dirA}}, time.Now())
	if !errors.Is(err, ErrRunLocked) || !probed {
		t.Fatalf("expected plan-b contention and liveness probe, got %v, probed=%v", err, probed)
	}
	pathA, pathErr := planRunLockPath(dirA)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(pathA); !os.IsNotExist(statErr) {
		t.Fatalf("plan-a lock was not rolled back: %v", statErr)
	}
}

func TestAcquireRunLocksContendsWithOrdinaryPlanLock(t *testing.T) {
	t.Cleanup(SetRunLockSettingsForTest(time.Hour, func(pid int) bool { return true }))
	dir := t.TempDir()
	batch, err := AcquireRunLocks([]RunLockRequest{{PlanID: "plan-a", PlanDir: dir}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Release() })
	if _, err := AcquireRunLock(dir, "plan-a", time.Now()); !errors.Is(err, ErrRunLocked) {
		t.Fatalf("ordinary lock should contend with batch ownership, got %v", err)
	}
}

func TestWithRunLockIsReentrantAndReleasesAfterError(t *testing.T) {
	t.Cleanup(SetRunLockSettingsForTest(time.Hour, func(pid int) bool { return true }))
	detail := &PlanDetail{Dir: t.TempDir(), State: State{Plan: PlanState{ID: "plan-a"}}}
	operationErr := errors.New("operation failed")
	competitorDone := make(chan error, 1)
	nestedRan := false

	err := WithRunLock(context.Background(), detail, time.Now(), func(ownedCtx context.Context) error {
		if err := WithRunLock(ownedCtx, detail, time.Now(), func(context.Context) error {
			nestedRan = true
			return nil
		}); err != nil {
			return err
		}
		// The competitor deliberately starts a separate lifecycle request rather
		// than inheriting the lock-owning context under test.
		go func() { //nolint:gosec // G118: independent context is the contention condition
			competitorDone <- WithRunLock(context.Background(), detail, time.Now(), func(context.Context) error {
				return errors.New("competitor unexpectedly ran")
			})
		}()
		select {
		case competitorErr := <-competitorDone:
			if !errors.Is(competitorErr, ErrRunLocked) {
				t.Fatalf("competing lock error = %v, want ErrRunLocked", competitorErr)
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
	if err := WithRunLock(context.Background(), detail, time.Now(), func(context.Context) error {
		releasedRan = true
		return nil
	}); err != nil {
		t.Fatalf("lock was not released after callback error: %v", err)
	}
	if !releasedRan {
		t.Fatal("operation after release did not run")
	}
}

func TestAcquireRunLockCreatesLockFile(t *testing.T) {
	planDir := t.TempDir()
	createdAt := time.Date(2026, 6, 28, 3, 0, 0, 123, time.FixedZone("test", -5*60*60))

	lock, err := AcquireRunLock(planDir, "plan-a", createdAt)
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
	want := fmt.Sprintf("pid=%d\ncreated_at=%s\nplan_id=plan-a\ntoken=%s\n", os.Getpid(), createdAt.UTC().Format(time.RFC3339Nano), metadata.Token)
	if string(content) != want {
		t.Fatalf("lock content = %q, want %q", content, want)
	}
	read, err := ReadRunLock(planDir)
	if err != nil || read.PID != metadata.PID || !read.ProcessAlive {
		t.Fatalf("ReadRunLock() = %+v, %v", read, err)
	}
}

func TestAcquireRunLockFailsWhenLiveLockExists(t *testing.T) {
	t.Cleanup(SetRunLockSettingsForTest(time.Hour, func(pid int) bool { return true }))
	planDir := t.TempDir()
	createdAt := time.Date(2026, 6, 28, 3, 5, 0, 0, time.UTC)
	lock, err := AcquireRunLock(planDir, "plan-a", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	_, err = AcquireRunLock(planDir, "plan-a", createdAt.Add(time.Minute))
	if err == nil {
		t.Fatal("expected contended lock acquisition to fail")
	}
	if !errors.Is(err, ErrRunLocked) {
		t.Fatalf("expected plan lock classification, got %v", err)
	}
	if !strings.Contains(err.Error(), "plan plan-a is already running") || !strings.Contains(err.Error(), "pid") {
		t.Fatalf("expected clear contention error, got %v", err)
	}
}

func TestAcquireRunLockTakesOverStaleLocks(t *testing.T) {
	t.Run("dead pid", func(t *testing.T) {
		t.Cleanup(SetRunLockSettingsForTest(time.Hour, func(pid int) bool { return false }))
		planDir := t.TempDir()
		path, err := planRunLockPath(planDir)
		if err != nil {
			t.Fatal(err)
		}
		oldContent := formatPlanRunLockContent("plan-a", 987654, time.Date(2026, 6, 28, 3, 10, 0, 0, time.UTC), "old")
		if err := os.WriteFile(path, oldContent, 0o600); err != nil {
			t.Fatal(err)
		}

		lock, err := AcquireRunLock(planDir, "plan-a", time.Date(2026, 6, 28, 3, 11, 0, 0, time.UTC))
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
		t.Cleanup(SetRunLockSettingsForTest(time.Minute, func(pid int) bool { return true }))
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

		lock, err := AcquireRunLock(planDir, "plan-a", now)
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
	lock, err := AcquireRunLock(planDir, "plan-a", time.Date(2026, 6, 28, 3, 30, 0, 0, time.UTC))
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

func TestReadRunLockParsesPIDAndProbesLiveness(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, runLockFileName), []byte("pid=4242\ncreated_at=2026-08-10T15:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var probed int
	t.Cleanup(SetRunLockSettingsForTest(time.Hour, func(pid int) bool {
		probed = pid
		return false
	}))
	lock, err := ReadRunLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lock.PID != 4242 || lock.ProcessAlive || probed != 4242 {
		t.Fatalf("run lock = %+v, probed pid = %d", lock, probed)
	}
}

func TestReadRunLockRejectsMissingOrInvalidPID(t *testing.T) {
	t.Cleanup(SetRunLockSettingsForTest(time.Hour, func(int) bool {
		t.Fatal("invalid PID must not be probed")
		return true
	}))
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "missing", content: "created_at=2026-08-10T15:00:00Z\n", want: "pid is missing"},
		{name: "invalid", content: "pid=not-a-pid\n", want: `pid "not-a-pid"`},
		{name: "nonpositive", content: "pid=0\n", want: `pid "0"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, runLockFileName), []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadRunLock(dir)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadRunLock() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReadRunLockPreservesMissingFileError(t *testing.T) {
	_, err := ReadRunLock(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadRunLock() error = %v, want os.ErrNotExist", err)
	}
}

func TestRunLockMetadataPreservesDuplicatePIDInterpretations(t *testing.T) {
	for _, test := range []struct {
		content   string
		readPID   int
		stalePID  int
		readError bool
	}{
		{content: "pid=42\npid=43\n", readPID: 42, stalePID: 43},
		{content: "pid=42\npid=bad\n", readPID: 42, stalePID: 42},
		{content: "pid=bad\npid=43\n", stalePID: 43, readError: true},
		{content: "pid=0\npid=43\n", stalePID: 43, readError: true},
	} {
		metadata := parsePlanRunLockMetadata([]byte(test.content))
		if metadata.readPID != test.readPID || metadata.PID != test.stalePID || (metadata.readPIDErr != nil) != test.readError {
			t.Errorf("parse %q = %+v", test.content, metadata)
		}
	}
}

func TestRunLockReleasePreservesReplacement(t *testing.T) {
	lock, err := AcquireRunLock(t.TempDir(), "plan-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	replacement := formatPlanRunLockContent("plan-a", os.Getpid(), time.Now(), "replacement")
	if err := os.WriteFile(lock.path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(lock.path)
	if err != nil || !bytes.Equal(content, replacement) {
		t.Fatalf("release changed replacement: %q, %v", content, err)
	}
}
