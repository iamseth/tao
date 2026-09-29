package run

import (
	"fmt"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestSliceCompletionLockProcesses(t *testing.T) {
	dir := t.TempDir()
	driver, err := plan.AcquireRunLock(dir, "plan", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = driver.Release() }()
	owner := startCompletionHelper(t, dir, "lock", "")
	if got := owner.line(t); got != "locked" {
		t.Fatal(got)
	}
	contender := startCompletionHelper(t, dir, "lock", "")
	if got := contender.line(t); got != "contended" {
		t.Fatal(got)
	}
	if err := contender.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := owner.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.cmd.Wait()
	// Kernel stale-owner release works without replacing/unlinking the inode.
	next := startCompletionHelper(t, dir, "lock", "")
	if got := next.line(t); got != "locked" {
		t.Fatal(got)
	}
	_, _ = fmt.Fprintln(next.input, "release")
	if err := next.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lock, err := plan.ReadRunLock(dir)
	if err != nil || !lock.ProcessAlive {
		t.Fatalf("parent run lock changed: %+v %v", lock, err)
	}
}

func TestSliceCompletionLock(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireSliceCompletionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireSliceCompletionLock(dir); err == nil {
		t.Fatal("concurrent completion admitted")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	next, err := acquireSliceCompletionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next() }()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireSliceCompletionLock(dir); err == nil {
		t.Fatal("old release unlocked new writer")
	}
}
