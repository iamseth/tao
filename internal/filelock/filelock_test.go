package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func openLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestLockUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	file := openLockFile(t, path)
	other := openLockFile(t, path)
	if err := Lock(file); err != nil {
		t.Fatal(err)
	}
	if err := Unlock(file); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(other); err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	if err := Unlock(other); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("caller-owned descriptor no longer usable: %v", err)
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("lock file no longer exists: %v", err)
	}
	if !os.SameFile(info, pathInfo) {
		t.Fatal("lock file inode changed")
	}
}

func TestTryLockContended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	holder := openLockFile(t, path)
	waiter := openLockFile(t, path)
	if err := Lock(holder); err != nil {
		t.Fatal(err)
	}
	err := TryLock(waiter)
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("TryLock = %v, want raw contention error", err)
	}
}

func TestLockPollContended(t *testing.T) {
	for _, cancelWait := range []bool{true, false} {
		name := "acquire_after_unlock"
		if cancelWait {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lock")
			holder := openLockFile(t, path)
			waiter := openLockFile(t, path)
			if err := Lock(holder); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- LockPoll(ctx, waiter, time.Millisecond) }()
			select {
			case err := <-result:
				t.Fatalf("LockPoll returned while contended: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if cancelWait {
				cancel()
			} else if err := Unlock(holder); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if cancelWait {
					if !errors.Is(err, ctx.Err()) {
						t.Fatalf("LockPoll = %v, want %v", err, ctx.Err())
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if err := Unlock(waiter); err != nil {
						t.Fatal(err)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("LockPoll did not return")
			}
		})
	}
}

func TestLockPollClosedFile(t *testing.T) {
	file := openLockFile(t, filepath.Join(t.TempDir(), "lock"))
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := LockPoll(ctx, file, time.Millisecond); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("LockPoll = %v, want EBADF", err)
	}
}
