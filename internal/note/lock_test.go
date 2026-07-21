package note

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPromotionLockContentionAndOwnedRelease(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	locker := &PromotionLocker{Dir: dir, Now: func() time.Time { return now }, PID: func() int { return 42 }, Token: func() string { return "token-a" }, ProcessLive: func(pid int) bool { return pid == 42 }}
	lock, err := locker.Acquire(context.Background(), "note-1", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if lock.Owner().Owner != "plan" || lock.Owner().PID != 42 {
		t.Fatalf("owner = %#v", lock.Owner())
	}
	_, err = locker.Acquire(context.Background(), "note-1", "run")
	var contention *PromotionLockError
	if !errors.Is(err, ErrPromotionLocked) || !errors.As(err, &contention) || contention.Holder.Owner != "plan" {
		t.Fatalf("contention = %T %v", err, err)
	}
	path := filepath.Join(dir, ".note-1.promotion.lock")
	original, err := os.ReadFile(path) //nolint:gosec // G304: path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"owner":"replacement","pid":42,"created_at":"2026-07-13T12:00:00Z","token":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("release removed another owner's lock: %v", err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil { //nolint:gosec // G703: path is rooted in t.TempDir.
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned lock remains: %v", err)
	}
}

func TestPromotionLockReclaimsDeadProcessButKeepsLiveOldProcess(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	live := true
	locker := &PromotionLocker{Dir: dir, Now: func() time.Time { return now }, PID: func() int { return 99 }, Token: func() string { return "new" }, ProcessLive: func(int) bool { return live }}
	old := []byte(`{"owner":"old","pid":10,"created_at":"2020-01-01T00:00:00Z","token":"old"}`)
	path := filepath.Join(dir, ".note-1.promotion.lock")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Acquire(context.Background(), "note-1", "new"); !errors.Is(err, ErrPromotionLocked) {
		t.Fatalf("old live lock reclaimed: %v", err)
	}
	live = false
	lock, err := locker.Acquire(context.Background(), "note-1", "new")
	if err != nil {
		t.Fatalf("dead lock not reclaimed: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestPromotionLockMalformedRecoveryAndCancellation(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	locker := &PromotionLocker{Dir: dir, Now: func() time.Time { return now }, StaleAfter: time.Hour, PID: func() int { return 1 }}
	path := filepath.Join(dir, ".note-1.promotion.lock")
	if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Acquire(context.Background(), "note-1", "owner"); !errors.Is(err, ErrPromotionLocked) {
		t.Fatalf("fresh malformed lock = %v", err)
	}
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lock, err := locker.Acquire(context.Background(), "note-1", "owner")
	if err != nil {
		t.Fatalf("stale malformed lock = %v", err)
	}
	_ = lock.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := locker.Acquire(ctx, "note-2", "owner"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Acquire = %v", err)
	}
	if _, err := locker.Acquire(context.Background(), "../note", "owner"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("unsafe id = %v", err)
	}
}
