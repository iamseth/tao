package run

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

const (
	planRunLockFileName       = ".run.lock"
	defaultPlanRunLockTimeout = 24 * time.Hour
)

var (
	errPlanRunLocked       = errors.New("plan run lock held")
	planRunLockTimeout     = defaultPlanRunLockTimeout
	planRunLockProcessLive = defaultPlanRunLockProcessLive
)

type planRunLock struct {
	path    string
	content []byte
}

// PlanLockRequest identifies one plan whose ordinary run ownership must be
// reserved by a repository-wide operation.
type PlanLockRequest struct {
	PlanID  string
	PlanDir string
}

// PlanLocks owns a set of ordinary plan run locks. Callers must release it.
type PlanLocks struct {
	locks []*planRunLock
}

// AcquirePlanLocks reserves plans in stable plan-ID order. If any acquisition
// fails, locks already acquired by this call are released before returning.
// This is the same primitive used by ordinary runs, so batch ownership cannot
// race a plan runner without changing ordinary run semantics.
func AcquirePlanLocks(requests []PlanLockRequest, timestamp time.Time) (*PlanLocks, error) {
	ordered := append([]PlanLockRequest(nil), requests...)
	slices.SortFunc(ordered, func(a, b PlanLockRequest) int {
		if byID := strings.Compare(a.PlanID, b.PlanID); byID != 0 {
			return byID
		}
		return strings.Compare(a.PlanDir, b.PlanDir)
	})
	owned := &PlanLocks{}
	for _, request := range ordered {
		lock, err := acquirePlanRunLock(request.PlanDir, request.PlanID, timestamp)
		if err != nil {
			return nil, errors.Join(err, owned.Release())
		}
		owned.locks = append(owned.locks, lock)
	}
	return owned, nil
}

// Release relinquishes all locks in reverse acquisition order.
func (l *PlanLocks) Release() error {
	if l == nil {
		return nil
	}
	var err error
	for _, lock := range slices.Backward(l.locks) {
		err = errors.Join(err, lock.Release())
	}
	l.locks = nil
	return err
}

type planRunLockOwnership struct {
	path   string
	planID string
}

type planRunLockOwnershipKey struct{}

type observedPlanRunLock struct {
	path     string
	info     os.FileInfo
	content  []byte
	metadata planRunLockMetadata
}

type planRunLockMetadata struct {
	PID       int
	CreatedAt time.Time
	PlanID    string
	Token     string
}

type planRunLockContendedError struct {
	planID string
	lock   observedPlanRunLock
}

func (e *planRunLockContendedError) Error() string {
	planLabel := strings.TrimSpace(e.planID)
	if planLabel == "" {
		planLabel = "unknown"
	}
	holder := "unknown process"
	if e.lock.metadata.PID > 0 {
		holder = fmt.Sprintf("pid %d", e.lock.metadata.PID)
	}
	since := e.lock.metadata.CreatedAt
	if since.IsZero() && e.lock.info != nil {
		since = e.lock.info.ModTime()
	}
	if since.IsZero() {
		return fmt.Sprintf("plan %s is already running; lock file %s is held by %s", planLabel, e.lock.path, holder)
	}
	return fmt.Sprintf("plan %s is already running; lock file %s is held by %s since %s", planLabel, e.lock.path, holder, since.UTC().Format(time.RFC3339Nano))
}

func (e *planRunLockContendedError) Is(target error) bool {
	return target == ErrCannotStart || target == errPlanRunLocked
}

// WithPlanRunLock runs operation while holding the ordinary per-plan driver
// lock. The callback context carries ownership, so nested lifecycle drivers for
// the same plan are re-entrant and retain the original lock until operation
// returns.
func WithPlanRunLock(ctx context.Context, detail *plan.PlanDetail, timestamp time.Time, operation func(context.Context) error) error {
	if detail == nil {
		return fmt.Errorf("acquire plan run lock: plan detail is nil")
	}
	if operation == nil {
		return fmt.Errorf("plan run lock operation is nil")
	}
	return withPlanRunLock(ctx, detail, timestamp, operation)
}

func withPlanRunLock(ctx context.Context, detail *plan.PlanDetail, timestamp time.Time, operation func(context.Context) error) (err error) {
	path, err := planRunLockPath(detail.Dir)
	if err != nil {
		return err
	}
	if ownership, ok := ctx.Value(planRunLockOwnershipKey{}).(planRunLockOwnership); ok && ownership.path == path && ownership.planID == detail.State.Plan.ID {
		return operation(ctx)
	}
	lock, err := acquirePlanRunLock(detail.Dir, detail.State.Plan.ID, timestamp)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	ownedCtx := context.WithValue(ctx, planRunLockOwnershipKey{}, planRunLockOwnership{path: path, planID: detail.State.Plan.ID})
	return operation(ownedCtx)
}

func acquirePlanRunLock(planDir string, planID string, timestamp time.Time) (*planRunLock, error) {
	path, err := planRunLockPath(planDir)
	if err != nil {
		return nil, err
	}
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	timestamp = timestamp.UTC()
	content := formatPlanRunLockContent(planID, os.Getpid(), timestamp, newPlanRunLockToken())
	for {
		acquired, err := createPlanRunLockFile(path, content)
		if err != nil {
			return nil, err
		}
		if acquired {
			return &planRunLock{path: path, content: content}, nil
		}
		observed, err := readObservedPlanRunLock(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !observed.stale(timestamp) {
			return nil, &planRunLockContendedError{planID: planID, lock: observed}
		}
		if _, err := removeObservedPlanRunLock(observed); err != nil {
			return nil, err
		}
	}
}

func (l *planRunLock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	content, err := os.ReadFile(l.path) // #nosec G304 -- plan lock path is inside the resolved Tao plan data directory.
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read plan run lock for release %s: %w", l.path, err)
	}
	if !bytes.Equal(content, l.content) {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("release plan run lock %s: %w", l.path, err)
	}
	return nil
}

func planRunLockPath(planDir string) (string, error) {
	if strings.TrimSpace(planDir) == "" {
		return "", fmt.Errorf("acquire plan run lock: plan dir is empty")
	}
	abs, err := filepath.Abs(planDir)
	if err != nil {
		return "", fmt.Errorf("resolve plan run lock path for %q: %w", planDir, err)
	}
	return filepath.Join(abs, planRunLockFileName), nil
}

func createPlanRunLockFile(path string, content []byte) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- plan lock path is inside the resolved Tao plan data directory.
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("create plan run lock %s: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("write plan run lock %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("close plan run lock %s: %w", path, err)
	}
	return true, nil
}

func readObservedPlanRunLock(path string) (observedPlanRunLock, error) {
	info, err := os.Stat(path)
	if err != nil {
		return observedPlanRunLock{}, err
	}
	content, err := os.ReadFile(path) // #nosec G304 -- plan lock path is inside the resolved Tao plan data directory.
	if err != nil {
		return observedPlanRunLock{}, err
	}
	return observedPlanRunLock{path: path, info: info, content: content, metadata: parsePlanRunLockMetadata(content)}, nil
}

func (l observedPlanRunLock) stale(now time.Time) bool {
	if planRunLockTimeout > 0 && l.info != nil {
		age := now.Sub(l.info.ModTime())
		if age >= planRunLockTimeout {
			return true
		}
	}
	pid := l.metadata.PID
	if pid <= 0 {
		return false
	}
	return !planRunLockProcessLive(pid)
}

func removeObservedPlanRunLock(observed observedPlanRunLock) (bool, error) {
	current, err := readObservedPlanRunLock(observed.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !os.SameFile(observed.info, current.info) || !bytes.Equal(observed.content, current.content) {
		return false, nil
	}
	if err := os.Remove(observed.path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("remove stale plan run lock %s: %w", observed.path, err)
	}
	return true, nil
}

func formatPlanRunLockContent(planID string, pid int, timestamp time.Time, token string) []byte {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "pid=%d\n", pid)
	_, _ = fmt.Fprintf(&b, "created_at=%s\n", timestamp.UTC().Format(time.RFC3339Nano))
	if planID != "" {
		_, _ = fmt.Fprintf(&b, "plan_id=%s\n", planID)
	}
	if token != "" {
		_, _ = fmt.Fprintf(&b, "token=%s\n", token)
	}
	return []byte(b.String())
}

func parsePlanRunLockMetadata(content []byte) planRunLockMetadata {
	metadata := planRunLockMetadata{}
	for line := range strings.SplitSeq(string(content), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "pid":
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil {
				metadata.PID = pid
			}
		case "created_at":
			createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
			if err == nil {
				metadata.CreatedAt = createdAt.UTC()
			}
		case "plan_id":
			metadata.PlanID = strings.TrimSpace(value)
		case "token":
			metadata.Token = strings.TrimSpace(value)
		}
	}
	return metadata
}

func newPlanRunLockToken() string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err == nil {
		return hex.EncodeToString(token[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func defaultPlanRunLockProcessLive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = process.Release() }()
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return true
}
