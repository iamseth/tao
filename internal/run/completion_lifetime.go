package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/filelock"
	"github.com/iamseth/tao/internal/plan"
)

const sliceCompletionOwnerEnv = commandrunner.SliceCompletionOwnerEnv
const completionOwnerName = ".slice-completion-owner.json"
const completionPollInterval = 25 * time.Millisecond

// completionOwner is transient cancellation coordination, never admission,
// lifecycle, or recovery evidence. Its lease is held only by the driver (Go's
// file descriptors are close-on-exec), not by providers or nested descendants.
// Kernel lease release detects death without PID probes or inherited-pipe EOF.
type completionOwner struct {
	Token    string    `json:"token"`
	SliceID  string    `json:"slice_id"`
	Deadline time.Time `json:"deadline,omitempty"`
}

// SliceCompletionLifetime binds a completion attempt to one invocation forever.
// Check must be called again immediately before mutation; Context cancels work
// while it is running. Neither method grants ordinary durable admission.
type SliceCompletionLifetime struct {
	ctx    context.Context
	cancel context.CancelFunc
	dir    string
	owner  *completionOwner
}

func BindSliceCompletionLifetime(ctx context.Context, planDir, sliceID string) (*SliceCompletionLifetime, error) {
	dir, err := completionDirectory(planDir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(sliceID) == "" {
		return nil, errors.New("slice completion requires a slice identity")
	}
	bound, cancel := context.WithCancel(ctx)
	g := &SliceCompletionLifetime{ctx: bound, cancel: cancel, dir: dir}
	if token, managed := os.LookupEnv(sliceCompletionOwnerEnv); managed {
		owner, err := readCompletionOwner(dir)
		if err != nil || token == "" || owner.Token != token || owner.SliceID != sliceID {
			cancel()
			return nil, errors.New("slice completion managed invocation missing or replaced")
		}
		g.owner = &owner
		if !owner.Deadline.IsZero() {
			deadlineCtx, deadlineCancel := context.WithDeadline(bound, owner.Deadline)
			g.ctx = deadlineCtx
			g.cancel = func() { deadlineCancel(); cancel() }
		}
	}
	if err := g.Check(); err != nil {
		_ = g.Close()
		return nil, err
	}
	go func() {
		ticker := time.NewTicker(completionPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-g.ctx.Done():
				return
			case <-ticker.C:
				if g.Check() != nil {
					return
				}
			}
		}
	}()
	return g, nil
}

func (g *SliceCompletionLifetime) Context() context.Context { return g.ctx }

func (g *SliceCompletionLifetime) Check() error {
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if err := g.checkOwner(); err != nil {
		g.cancel()
		return err
	}
	return g.ctx.Err()
}

func (g *SliceCompletionLifetime) checkOwner() error {
	if g.owner == nil {
		lock, err := plan.ReadRunLock(g.dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("slice completion cannot establish driver ownership: %w", err)
		}
		if lock.ProcessAlive {
			return errors.New("slice completion missing managed invocation for live driver")
		}
		// Also refuse the narrow session-start window and managed test/seam callers
		// that do not own a plan driver lock.
		owner, err := readCompletionOwner(g.dir)
		if err == nil && completionLeaseLive(g.dir, owner.Token) == nil {
			return errors.New("slice completion missing managed invocation")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	owner, err := readCompletionOwner(g.dir)
	if err != nil || owner != *g.owner {
		return errors.New("slice completion invocation ended or was replaced")
	}
	if !owner.Deadline.IsZero() && !time.Now().Before(owner.Deadline) {
		return context.DeadlineExceeded
	}
	return completionLeaseLive(g.dir, owner.Token)
}

func (g *SliceCompletionLifetime) Close() error { g.cancel(); return nil }

func completionLeaseLive(dir, token string) error {
	if !strings.HasPrefix(token, ".slice-session-") || filepath.Base(token) != token {
		return errors.New("invalid completion lease")
	}
	file, err := os.OpenFile(filepath.Join(dir, token), os.O_RDWR, 0) // #nosec G304,G703 -- validated private lease basename.
	if err != nil {
		return fmt.Errorf("slice completion owner unavailable: %w", err)
	}
	defer func() { _ = file.Close() }()
	// All probes take shared locks: a concurrent probe must never look like
	// the exclusive owner after that owner dies.
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("probe slice completion owner: %w", err)
	}
	return errors.New("slice completion owner ended")
}

func removeEndedCompletionLease(dir, token string) {
	if !strings.HasPrefix(token, ".slice-session-") || filepath.Base(token) != token {
		return
	}
	path := filepath.Join(dir, token)
	file, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304,G703 -- validated unique lease basename in private plan metadata.
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	if syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		_ = os.Remove(path) // #nosec G703 -- validated unique lease basename, no exclusive owner.
	}
}

func readCompletionOwner(dir string) (completionOwner, error) {
	var owner completionOwner
	file, err := os.Open(filepath.Join(dir, completionOwnerName)) // #nosec G304,G703 -- private plan-local coordination file.
	if err != nil {
		return owner, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return owner, err
	}
	if info.Size() > 4096 {
		return owner, errors.New("completion owner metadata too large")
	}
	err = json.NewDecoder(file).Decode(&owner)
	return owner, err
}

// Called inside the provider-neutral timeout decorator, after the actual
// session deadline exists. No child-side session budget is created.
func startSliceCompletionLifetime(ctx context.Context, planDir, sliceID string) (context.Context, func() error, error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	dir, err := completionDirectory(planDir)
	if err != nil {
		return ctx, nil, err
	}
	release, err := acquireCompletionFileLock(dir, ".slice-completion-publish.lock")
	if err != nil {
		return ctx, nil, err
	}
	defer func() { _ = release() }()
	lease, err := os.CreateTemp(dir, ".slice-session-")
	if err != nil {
		return ctx, nil, err
	}
	token := filepath.Base(lease.Name())
	cleanupLease := func() error {
		return errors.Join(lease.Close(), os.Remove(lease.Name())) // #nosec G703 -- exclusively owned CreateTemp lease.
	}
	if err := filelock.TryLock(lease); err != nil {
		_ = cleanupLease()
		return ctx, nil, err
	}
	owner := completionOwner{Token: token, SliceID: sliceID}
	owner.Deadline, _ = ctx.Deadline()
	content, err := json.Marshal(owner)
	if err != nil {
		_ = cleanupLease()
		return ctx, nil, err
	}
	// Reap only the prior dead invocation's unique lease. Never unlink a live
	// lease or a serialization inode; a superseded live owner cleans up itself.
	if previous, readErr := readCompletionOwner(dir); readErr == nil {
		removeEndedCompletionLease(dir, previous.Token)
	}
	// Atomic publication also invalidates every previously bound invocation.
	temp, err := os.CreateTemp(dir, ".slice-owner-*")
	if err != nil {
		_ = cleanupLease()
		return ctx, nil, err
	}
	_, writeErr := temp.Write(content)
	err = errors.Join(writeErr, temp.Close())
	if err == nil {
		err = os.Rename(temp.Name(), filepath.Join(dir, completionOwnerName)) // #nosec G703 -- atomic publication inside canonical plan metadata directory.
	}
	if err != nil {
		_ = os.Remove(temp.Name()) // #nosec G703 -- exclusively owned CreateTemp publication file.
		_ = cleanupLease()
		return ctx, nil, err
	}
	// Release publication before registering cancellation, including already
	// expired contexts, so cleanup can remove its marker without contending with
	// its own publisher.
	_ = release()
	liveCtx, cancel := context.WithCancel(ctx)
	cleanup := sync.OnceValue(func() error {
		// Lease invalidation is immediate even if another publisher holds its lock.
		err := cleanupLease()
		if unlock, lockErr := acquireCompletionFileLock(dir, ".slice-completion-publish.lock"); lockErr == nil {
			defer func() { _ = unlock() }()
			if current, readErr := readCompletionOwner(dir); readErr == nil && current.Token == token {
				err = errors.Join(err, os.Remove(filepath.Join(dir, completionOwnerName))) // #nosec G703 -- only remove this invocation's private metadata marker.
			}
		}
		return err
	})
	stop := context.AfterFunc(liveCtx, func() { _ = cleanup() })
	closeOwner := func() error { cancel(); stop(); return cleanup() }
	// Provider exit invalidates nested work without cancelling the provider's
	// own output/result collection (normal EOF must retain its existing meaning).
	liveCtx = process.WithSessionLifetime(liveCtx, []string{sliceCompletionOwnerEnv + "=" + token}, func() { _ = cleanup() })
	return liveCtx, closeOwner, nil
}
