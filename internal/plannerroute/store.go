package plannerroute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/filelock"
)

const MaxRecordBytes = 64 * 1024
const MaxRecords = 5000

// Bound contention even when post-planning recording detaches cancellation.
// Brief polling still lets concurrent writers serialize without needless loss.
const lockWaitLimit = time.Second
const lockPollInterval = 10 * time.Millisecond

var (
	ErrNotFound          = errors.New("planner route not found")
	ErrAlreadyLinked     = errors.New("planner route already linked to another plan")
	ErrPlanAlreadyRouted = errors.New("plan already linked to another planner route")
)

// Store owns a repository's non-authoritative routing ledger, outside plan
// directories. Writers serialize through one persistent advisory lock file;
// readers need no lock because each document is replaced atomically.
type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Create(ctx context.Context, record Record) error {
	data, err := encodeRecord(record)
	if err != nil {
		return err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	path := s.path(record.ID)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("planner route %s: %w", record.ID, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.checkCapacity(ctx); err != nil {
		return err
	}
	if err := s.checkPlanLink(ctx, record); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return atomicfile.Write(path, data, atomicfile.Options{Perm: 0o600, Exclusive: true})
}

func (s *Store) Load(ctx context.Context, routeID string) (Record, error) {
	if err := checkRouteID(routeID); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	path := s.path(routeID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, routeID)
	}
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() {
		return Record{}, fmt.Errorf("planner route %s is not a regular file", routeID)
	}
	if info.Size() > MaxRecordBytes {
		return Record{}, fmt.Errorf("planner route %s exceeds %d bytes", routeID, MaxRecordBytes)
	}
	file, err := os.Open(path) // #nosec G304 -- route ID is validated and rooted in the repository ledger.
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, MaxRecordBytes+1))
	if err != nil {
		return Record{}, err
	}
	if len(data) > MaxRecordBytes {
		return Record{}, fmt.Errorf("planner route %s exceeds %d bytes", routeID, MaxRecordBytes)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("decode planner route %s: %w", routeID, err)
	}
	if err := validateStoredRecord(record); err != nil {
		return Record{}, err
	}
	if record.ID != routeID {
		return Record{}, fmt.Errorf("planner route ID %q does not match filename %q", record.ID, routeID)
	}
	return record, nil
}

func (s *Store) Append(ctx context.Context, routeID string, entry Entry) (Record, error) {
	if err := checkRouteID(routeID); err != nil {
		return Record{}, err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	record, err := s.Load(ctx, routeID)
	if err != nil {
		return Record{}, err
	}
	record.Entries = append(record.Entries, entry)
	return s.replace(ctx, record)
}

func (s *Store) Link(ctx context.Context, routeID, planID, planDir string) (Record, error) {
	if err := checkRouteID(routeID); err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(planID) == "" || strings.TrimSpace(planDir) == "" {
		return Record{}, errors.New("planner route link requires plan ID and directory")
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	record, err := s.Load(ctx, routeID)
	if err != nil {
		return Record{}, err
	}
	if linked := record.LinkedPlanID(); linked != "" {
		if linked != planID {
			return Record{}, fmt.Errorf("%w: route %s links plan %s", ErrAlreadyLinked, routeID, linked)
		}
		return record, nil
	}
	record.Entries = append(record.Entries, Entry{
		Kind: EntryLinked, At: time.Now().UTC(), Link: &Link{PlanID: planID, PlanDir: planDir},
	})
	return s.replace(ctx, record)
}

// List is read-only and tolerates individual damaged documents. It never
// creates the directory or lock file, including for historical repositories.
func (s *Store) List(ctx context.Context) ([]Record, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	files, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var records []Record
	var warnings []string
	count := 0
	// ReadDir sorts filenames; Load requires the document ID to match its name.
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return records, warnings, err
		}
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		if count == MaxRecords {
			warnings = append(warnings, fmt.Sprintf("planner route directory %s exceeds %d records; stopped reading", s.dir, MaxRecords))
			break
		}
		count++
		record, err := s.Load(ctx, strings.TrimSuffix(file.Name(), ".json"))
		if ctxErr := ctx.Err(); ctxErr != nil {
			return records, warnings, ctxErr
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", file.Name(), err))
			continue
		}
		records = append(records, record)
	}
	return records, warnings, nil
}

// checkCapacity is called under the repository lock before admitting a new
// record. Count the same entries as List, including damaged documents, so new
// assignments cannot exhaust the scan budget needed to link admitted records.
func (s *Store) checkCapacity(ctx context.Context) error {
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	count := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		count++
		if count >= MaxRecords {
			return fmt.Errorf("planner route ledger %s is at capacity (%d records); archive old route files that no longer need linking outside this directory before retrying", s.dir, MaxRecords)
		}
	}
	return nil
}

// replace and checkPlanLink are called only while holding the repository lock.
func (s *Store) replace(ctx context.Context, record Record) (Record, error) {
	data, err := encodeRecord(record)
	if err != nil {
		return Record{}, err
	}
	if err := s.checkPlanLink(ctx, record); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := atomicfile.Write(s.path(record.ID), data, atomicfile.Options{Perm: 0o600}); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) checkPlanLink(ctx context.Context, record Record) error {
	planID := record.LinkedPlanID()
	if planID == "" {
		return nil
	}
	records, warnings, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, other := range records {
		if other.ID != record.ID && other.LinkedPlanID() == planID {
			return fmt.Errorf("%w: plan %s links route %s", ErrPlanAlreadyRouted, planID, other.ID)
		}
	}
	// A partial scan cannot prove that the plan is unclaimed. This also keeps
	// Create and Append from bypassing Link's bidirectional uniqueness rule.
	if len(warnings) != 0 {
		return fmt.Errorf("cannot establish planner route link uniqueness: %s", warnings[0])
	}
	return nil
}

func (s *Store) lock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, ".routes.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- lock is rooted in the repository ledger.
	if err != nil {
		return nil, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWaitLimit)
	defer cancel()
	if err := filelock.LockPoll(lockCtx, file, lockPollInterval); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock planner routes %s: %w", path, err)
	}
	unlock := func() {
		_ = filelock.Unlock(file)
		_ = file.Close()
	}
	if err := lockCtx.Err(); err != nil {
		unlock()
		return nil, fmt.Errorf("lock planner routes %s: %w", path, err)
	}
	return unlock, nil
}

func (s *Store) path(routeID string) string {
	return filepath.Join(s.dir, routeID+".json")
}

func checkRouteID(routeID string) error {
	if !ValidRouteID(routeID) {
		return fmt.Errorf("invalid planner route ID %q", routeID)
	}
	return nil
}

func validateStoredRecord(record Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	for _, entry := range record.Entries {
		if entry.Kind == EntryLinked && (entry.Link == nil || strings.TrimSpace(entry.Link.PlanID) == "" || strings.TrimSpace(entry.Link.PlanDir) == "") {
			return errors.New("planner route link requires plan ID and directory")
		}
	}
	return nil
}

func encodeRecord(record Record) ([]byte, error) {
	if err := validateStoredRecord(record); err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxRecordBytes {
		return nil, fmt.Errorf("planner route %s exceeds %d bytes", record.ID, MaxRecordBytes)
	}
	return data, nil
}
