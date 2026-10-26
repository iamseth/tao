// Package repoadmission coordinates participating repository operations, not
// lifecycle authority. Acquire admission before batch ownership and plan locks.
package repoadmission

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/iamseth/tao/internal/filelock"
)

type Mode string

const (
	Run   Mode = "run"
	Merge Mode = "merge"
)

type Request struct {
	Root     string
	PlanID   string
	PlanDir  string
	Mode     Mode
	PlansDir string
	Warn     func(error)
	Observe  func(context.Context, Request, string) []Owner
}

// Guard is the injection boundary for consumers; nil consumers must use With.
type Guard func(context.Context, Request, func(context.Context) error) error

// ContentionError indicates another participating operation owns admission.
// Diagnostic observations must never override this refusal.
type ContentionError struct {
	Repository string
	Mode       Mode
	Owners     []Owner
}

func (e *ContentionError) Error() string {
	var details []string
	for _, owner := range e.Owners {
		text := fmt.Sprintf("plan %q", owner.PlanID)
		if owner.PID > 0 {
			text += fmt.Sprintf(" PID %d", owner.PID)
		}
		if owner.Phase != "" {
			text += fmt.Sprintf(" phase %q", owner.Phase)
		}
		details = append(details, text)
	}
	extra := ""
	if len(details) > 0 {
		extra = "; observed " + strings.Join(details, ", ")
	}
	return fmt.Sprintf("repository admission busy for %s (%s)%s: wait for the active operation to finish, then retry", e.Mode, e.Repository, extra)
}

type leaseKey struct{}
type lease struct {
	mu         sync.Mutex
	children   sync.WaitGroup
	repository string
	mode       Mode
	active     bool
}

// With holds a nonblocking repository lease through callback completion. The
// callback must observe cancellation; cancellation cannot safely release a lease
// while its operation is still running. Files are persistent and never unlinked.
func With(ctx context.Context, request Request, operation func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Mode != Run && request.Mode != Merge {
		return fmt.Errorf("repository admission: invalid mode %q", request.Mode)
	}
	repository, err := identity(ctx, request.Root)
	if err != nil {
		return err
	}
	if parent, ok := ctx.Value(leaseKey{}).(*lease); ok {
		parent.mu.Lock()
		if !parent.active {
			parent.mu.Unlock()
			return fmt.Errorf("repository admission: expired nested ownership")
		}
		if parent.repository != repository || parent.mode != request.Mode {
			parent.mu.Unlock()
			return fmt.Errorf("repository admission: incompatible nested repository or mode (upgrades are not allowed)")
		}
		parent.children.Add(1)
		parent.mu.Unlock()
		defer parent.children.Done()
		return operation(ctx)
	}
	file, err := os.OpenFile(filepath.Join(repository, "tao-admission.lock"), os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- canonical Git common directory and fixed Tao-owned filename.
	if err != nil {
		return fmt.Errorf("repository admission: open lock: %w", err)
	}
	// os.OpenFile sets close-on-exec, so spawned agents cannot retain ownership.
	defer func() { _ = file.Close() }()
	if request.Mode == Run {
		err = filelock.TrySharedLock(file)
	} else {
		err = filelock.TryLock(file)
	}
	observe := request.Observe
	if observe == nil {
		observe = observeSiblings
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		diagnosticRequest := request
		// Under authoritative contention, unclassified live siblings may still
		// explain ownership. Only legacy run refusal requires a merge phase.
		diagnosticRequest.Mode = Merge
		return &ContentionError{Repository: repository, Mode: request.Mode, Owners: observe(ctx, diagnosticRequest, repository)}
	}
	if err != nil {
		return fmt.Errorf("repository admission: acquire lock: %w", err)
	}
	defer func() { _ = filelock.Unlock(file) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if owners := observe(ctx, request, repository); len(owners) > 0 {
		return &ContentionError{Repository: repository, Mode: request.Mode, Owners: owners}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner := &lease{repository: repository, mode: request.Mode, active: true}
	defer func() { owner.mu.Lock(); owner.active = false; owner.mu.Unlock(); owner.children.Wait() }()
	return operation(context.WithValue(ctx, leaseKey{}, owner))
}
