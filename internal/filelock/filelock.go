// Package filelock provides exclusive advisory locks on caller-owned files.
// It never opens, closes, or unlinks files; callers retain responsibility for
// their lifecycle and must not unlink lock files while they may be in use.
package filelock

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// Lock blocks until an exclusive lock is acquired.
func Lock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

// TryLock attempts an exclusive lock without waiting and returns the raw syscall error.
func TryLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// LockPoll attempts an exclusive lock immediately, then retries contention at
// interval until acquisition or cancellation. Cancellation returns ctx.Err().
func LockPoll(ctx context.Context, file *os.File, interval time.Duration) error {
	for {
		err := TryLock(file)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Unlock releases the file's advisory lock without closing the file.
func Unlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
