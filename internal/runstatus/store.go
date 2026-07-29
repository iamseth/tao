package runstatus

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

var planIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Store persists runtime status records in one repository's status directory.
type Store struct {
	dir string
	now func() time.Time
}

// NewStore creates a runtime status store. A nil clock uses time.Now.
func NewStore(dir string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{dir: dir, now: now}
}

// Path returns the sanitized path for planID without touching the filesystem.
func (s *Store) Path(planID string) (string, error) {
	if planID != strings.TrimSpace(planID) || !planIDPattern.MatchString(planID) || planID == "." || planID == ".." {
		return "", fmt.Errorf("%w: %q", ErrInvalidPlanID, planID)
	}
	return filepath.Join(s.dir, planID+".json"), nil
}

// Create atomically installs a new record and fails if one already exists.
func (s *Store) Create(record Record) error {
	encoded, err := s.prepare(record)
	if err != nil {
		return err
	}
	return s.withMutationLock(record.PlanID, func(path string) error {
		if err := atomicfile.Write(path, encoded, atomicfile.Options{Perm: 0o600, Exclusive: true}); err != nil {
			return fmt.Errorf("create runtime status for %q: %w", record.PlanID, err)
		}
		return nil
	})
}

// Update atomically replaces an existing record.
func (s *Store) Update(record Record) error {
	encoded, err := s.prepare(record)
	if err != nil {
		return err
	}
	return s.withMutationLock(record.PlanID, func(path string) error {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("update runtime status for %q: %w", record.PlanID, err)
		}
		return writeStatus(path, encoded, record.PlanID, "update")
	})
}

// Write atomically creates or replaces a record.
func (s *Store) Write(record Record) error {
	encoded, err := s.prepare(record)
	if err != nil {
		return err
	}
	return s.withMutationLock(record.PlanID, func(path string) error {
		return writeStatus(path, encoded, record.PlanID, "write")
	})
}

// Read loads and validates one record. Missing directories and files remain
// observable through errors.Is(err, os.ErrNotExist).
func (s *Store) Read(planID string) (Record, error) {
	path, err := s.Path(planID)
	if err != nil {
		return Record{}, err
	}
	return readStatus(path, planID)
}

// Remove atomically removes one record and syncs its parent directory.
func (s *Store) Remove(planID string) error {
	return s.withMutationLock(planID, func(path string) error {
		return removeStatus(path, planID)
	})
}

// Heartbeat refreshes an existing record using the store's injected clock.
func (s *Store) Heartbeat(planID string) (record Record, err error) {
	err = s.withMutationLock(planID, func(path string) error {
		loaded, err := readStatus(path, planID)
		if err != nil {
			return err
		}
		loaded.HeartbeatAt = s.now().UTC()
		encoded, err := s.prepare(loaded)
		if err != nil {
			return err
		}
		if err := writeStatus(path, encoded, planID, "update"); err != nil {
			return err
		}
		record = loaded
		return nil
	})
	return record, err
}

// claim transfers publication to record's invocation. Callers must only claim
// after acquiring the plan run lock.
func (s *Store) claim(record Record) error {
	encoded, err := s.prepare(record)
	if err != nil {
		return err
	}
	return s.withMutationLock(record.PlanID, func(path string) error {
		return writeStatus(path, encoded, record.PlanID, "claim")
	})
}

func (s *Store) updateOwned(record Record) error {
	encoded, err := s.prepare(record)
	if err != nil {
		return err
	}
	return s.withMutationLock(record.PlanID, func(path string) error {
		current, err := readStatus(path, record.PlanID)
		if errors.Is(err, os.ErrNotExist) {
			return writeStatus(path, encoded, record.PlanID, "restore")
		}
		if err != nil {
			return err
		}
		if current.InvocationID != record.InvocationID {
			return errInvocationNotOwner
		}
		return writeStatus(path, encoded, record.PlanID, "update")
	})
}

func (s *Store) removeOwned(planID, invocationID string) error {
	return s.withMutationLock(planID, func(path string) error {
		current, err := readStatus(path, planID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.InvocationID != invocationID {
			return errInvocationNotOwner
		}
		return removeStatus(path, planID)
	})
}

func (s *Store) withMutationLock(planID string, operation func(path string) error) error {
	path, err := s.Path(planID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create runtime status directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is confined by plan id validation
	if err != nil {
		return fmt.Errorf("open runtime status lock for %q: %w", planID, err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock runtime status for %q: %w", planID, err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return operation(path)
}

func readStatus(path, planID string) (Record, error) {
	content, err := os.ReadFile(path) //nolint:gosec // caller provides a validated store path
	if err != nil {
		return Record{}, fmt.Errorf("read runtime status for %q: %w", planID, err)
	}
	var record Record
	if err := json.Unmarshal(content, &record); err != nil {
		return Record{}, fmt.Errorf("decode runtime status for %q: %w", planID, err)
	}
	if err := validateRecord(record); err != nil {
		return Record{}, fmt.Errorf("decode runtime status for %q: %w", planID, err)
	}
	if record.PlanID != strings.TrimSpace(planID) {
		return Record{}, fmt.Errorf("decode runtime status for %q: %w: record plan id is %q", planID, ErrInvalidRecord, record.PlanID)
	}
	return record, nil
}

func writeStatus(path string, encoded []byte, planID, operation string) error {
	if err := atomicfile.Write(path, encoded, atomicfile.Options{Perm: 0o600}); err != nil {
		return fmt.Errorf("%s runtime status for %q: %w", operation, planID, err)
	}
	return nil
}

func removeStatus(path, planID string) error {
	if err := atomicfile.Remove(path, atomicfile.RemoveOptions{}); err != nil {
		return fmt.Errorf("remove runtime status for %q: %w", planID, err)
	}
	return nil
}

func (s *Store) prepare(record Record) ([]byte, error) {
	if _, err := s.Path(record.PlanID); err != nil {
		return nil, err
	}
	if err := validateRecord(record); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode runtime status for %q: %w", record.PlanID, err)
	}
	return append(encoded, '\n'), nil
}

func validateRecord(record Record) error {
	if record.Schema != Schema {
		return fmt.Errorf("%w: unsupported schema %q", ErrInvalidRecord, record.Schema)
	}
	if strings.TrimSpace(record.RepoID) == "" {
		return fmt.Errorf("%w: repo id is required", ErrInvalidRecord)
	}
	if strings.TrimSpace(record.PlanID) == "" {
		return fmt.Errorf("%w: plan id is required", ErrInvalidRecord)
	}
	if strings.TrimSpace(record.InvocationID) == "" {
		return fmt.Errorf("%w: invocation id is required", ErrInvalidRecord)
	}
	if strings.TrimSpace(string(record.Phase)) == "" {
		return fmt.Errorf("%w: phase is required", ErrInvalidRecord)
	}
	if record.InvocationStartedAt.IsZero() {
		return fmt.Errorf("%w: invocation start is required", ErrInvalidRecord)
	}
	if record.HeartbeatAt.IsZero() {
		return fmt.Errorf("%w: heartbeat is required", ErrInvalidRecord)
	}
	if record.Slice != nil && strings.TrimSpace(record.Slice.ID) == "" {
		return fmt.Errorf("%w: slice id is required when slice detail is present", ErrInvalidRecord)
	}
	return nil
}

// Publisher owns the mutable status for one invocation. Every reporting method
// returns its storage error so integrations can explicitly choose best-effort
// suppression rather than having the package hide failures.
type Publisher struct {
	mu        sync.Mutex
	store     *Store
	record    Record
	published bool
}

func NewPublisher(store *Store, record Record) *Publisher {
	if strings.TrimSpace(record.InvocationID) == "" {
		record.InvocationID = newInvocationID()
	}
	return &Publisher{store: store, record: record}
}

// Publish records a phase and optional slice, refreshing invocation and
// heartbeat timestamps from the injected clock as needed.
func (p *Publisher) Publish(phase Phase, slice *SliceDetail) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store == nil {
		return errors.New("runtime status publisher requires a store")
	}
	now := p.store.now().UTC()
	if p.record.Schema == "" {
		p.record.Schema = Schema
	}
	if p.record.InvocationStartedAt.IsZero() {
		p.record.InvocationStartedAt = now
	}
	p.record.Phase = phase
	p.record.Slice = cloneSliceDetail(slice)
	p.record.HeartbeatAt = now
	if !p.published {
		p.published = true
		return p.store.Create(p.record)
	}
	return p.store.claim(p.record)
}

// Heartbeat republishes the current phase with a refreshed timestamp.
func (p *Publisher) Heartbeat() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store == nil {
		return errors.New("runtime status publisher requires a store")
	}
	p.record.HeartbeatAt = p.store.now().UTC()
	err := p.store.updateOwned(p.record)
	if errors.Is(err, errInvocationNotOwner) {
		return nil
	}
	return err
}

// Remove clears this invocation's operational record.
func (p *Publisher) Remove() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store == nil {
		return errors.New("runtime status publisher requires a store")
	}
	err := p.store.removeOwned(p.record.PlanID, p.record.InvocationID)
	if errors.Is(err, errInvocationNotOwner) {
		return nil
	}
	return err
}

func newInvocationID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err == nil {
		return hex.EncodeToString(id[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func cloneSliceDetail(detail *SliceDetail) *SliceDetail {
	if detail == nil {
		return nil
	}
	clone := *detail
	return &clone
}
