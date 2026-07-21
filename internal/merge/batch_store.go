package merge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const activeBatchSchema = "tao.active-merge-batch.v1"

// activeBatchMu avoids unnecessary lock-file contention between BatchStore
// instances in this process; the file lock provides cross-process exclusion.
var activeBatchMu sync.Mutex

// BatchStore persists merge batches outside the normal plan store.
type BatchStore struct {
	mu         sync.Mutex
	batchesDir string
	activePath string
}

// NewBatchStore creates a repository-scoped store. batchesDir contains one
// directory per batch; activePath is the repository's single active identity.
func NewBatchStore(batchesDir, activePath string) *BatchStore {
	return &BatchStore{batchesDir: batchesDir, activePath: activePath}
}

func (s *BatchStore) snapshotPath(id string) string {
	return filepath.Join(s.batchesDir, id, "state.json")
}

func (s *BatchStore) logPath(id string) string {
	return filepath.Join(s.batchesDir, id, "transitions.jsonl")
}

// WriteAggregateReview atomically stores full agent output in the batch
// directory, separate from every source plan's review artifact.
func (s *BatchStore) WriteAggregateReview(id string, attempt int, output string) (string, error) {
	name := fmt.Sprintf("aggregate-review-%03d.md", attempt)
	if err := atomicWriteBatchFile(filepath.Join(s.batchesDir, id, name), []byte(output)); err != nil {
		return "", err
	}
	return name, nil
}

// Load returns the newest complete state represented by the snapshot and log.
// A partial/corrupt snapshot or unterminated final log record is ignored.
func (s *BatchStore) Load(id string) (BatchState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(id)
}

func (s *BatchStore) loadLocked(id string) (BatchState, error) {
	state, _ := readBatchSnapshot(s.snapshotPath(id))
	if state.ID != "" && state.ID != id {
		state = BatchState{}
	}
	file, err := os.Open(s.logPath(id)) // #nosec G304 -- path is rooted in Tao's repository data directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return BatchState{}, fmt.Errorf("open merge batch transition log: %w", err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		// An append is durable only after its newline and fsync. In particular,
		// do not apply syntactically-valid bytes from a torn final append.
		if readErr == nil {
			transition, ok := decodeBatchTransition(line)
			if !ok {
				// A complete corrupt record breaks the chain. Applying records after
				// it could invent progress that was never durably ordered.
				return state, nil
			}
			if transition.Sequence > state.LogSequence {
				if transition.Sequence != state.LogSequence+1 || transition.State.ID != id || transition.From != state.Status {
					return state, nil
				}
				state = transition.State
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return state, nil
			}
			return BatchState{}, fmt.Errorf("read merge batch transition log: %w", readErr)
		}
	}
}

// SaveSnapshot atomically records state. Usually callers snapshot after one or
// more durable transitions to shorten replay; the transition log is retained.
func (s *BatchStore) SaveSnapshot(state BatchState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := state.validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBatchFile(s.snapshotPath(state.ID), append(encoded, '\n'))
}

// AppendTransition appends and fsyncs one complete transition record.
func (s *BatchStore) AppendTransition(transition BatchTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadLocked(transition.State.ID)
	if err != nil {
		return err
	}
	if transition.Sequence != current.LogSequence+1 || transition.From != current.Status {
		return fmt.Errorf("merge batch transition does not continue durable state at sequence %d", current.LogSequence)
	}
	return s.appendTransitionLocked(transition)
}

func (s *BatchStore) appendTransitionLocked(transition BatchTransition) error {
	if transition.Schema == "" {
		transition.Schema = BatchTransitionSchema
	}
	if transition.Schema != BatchTransitionSchema {
		return fmt.Errorf("unsupported merge batch transition schema %q", transition.Schema)
	}
	if err := transition.State.validate(); err != nil {
		return err
	}
	if transition.Sequence == 0 || transition.State.LogSequence != transition.Sequence {
		return fmt.Errorf("merge batch transition sequence must match state log_sequence")
	}
	if transition.To != transition.State.Status {
		return fmt.Errorf("merge batch transition target %q does not match state status %q", transition.To, transition.State.Status)
	}
	if err := ValidateBatchTransition(transition.From, transition.To); err != nil {
		return err
	}
	encoded, err := json.Marshal(transition)
	if err != nil {
		return err
	}
	return appendBatchLogLine(s.logPath(transition.State.ID), append(encoded, '\n'))
}

// Transition derives and durably appends the next sequence from current state.
func (s *BatchStore) Transition(next BatchState, at string) (BatchState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadLocked(next.ID)
	if err != nil {
		return BatchState{}, err
	}
	if current.ID != "" && current.ID != next.ID {
		return BatchState{}, fmt.Errorf("active merge batch id changed from %q to %q", current.ID, next.ID)
	}
	if next.Schema == "" {
		next.Schema = BatchStateSchema
	}
	next.LogSequence = current.LogSequence + 1
	transition := BatchTransition{Schema: BatchTransitionSchema, Sequence: next.LogSequence, At: at, From: current.Status, To: next.Status, State: next}
	if err := s.appendTransitionLocked(transition); err != nil {
		return BatchState{}, err
	}
	return next, nil
}

type activeBatchIdentity struct {
	Schema  string `json:"schema"`
	BatchID string `json:"batch_id"`
}

// ActiveID returns the one active batch identity. Missing or partial identity
// files select no batch rather than guessing from directories.
func (s *BatchStore) ActiveID() (string, error) {
	activeBatchMu.Lock()
	defer activeBatchMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, err := s.readActiveLocked()
	return identity.BatchID, err
}

// Initialize durably records the initial state before publishing its active
// identity. The active lock preserves single-batch exclusion across both
// writes, so a crash can leave an unselected state but never an active identity
// that points at missing state.
func (s *BatchStore) Initialize(state BatchState, at string) (BatchState, error) {
	activeBatchMu.Lock()
	defer activeBatchMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireActiveBatchLock(s.activePath)
	if err != nil {
		return BatchState{}, err
	}
	defer func() { _ = lock.release() }()
	state.ID = strings.TrimSpace(state.ID)
	if state.ID == "" {
		return BatchState{}, errors.New("active merge batch id is required")
	}
	current, err := s.readActiveLocked()
	if err != nil {
		return BatchState{}, err
	}
	if current.BatchID != "" {
		return BatchState{}, fmt.Errorf("merge batch %s is already active", current.BatchID)
	}
	if state.Schema == "" {
		state.Schema = BatchStateSchema
	}
	state.LogSequence = 1
	if durable, loadErr := s.loadLocked(state.ID); loadErr != nil {
		return BatchState{}, loadErr
	} else if durable.ID != "" {
		wanted, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return BatchState{}, marshalErr
		}
		got, marshalErr := json.Marshal(durable)
		if marshalErr != nil {
			return BatchState{}, marshalErr
		}
		if !bytes.Equal(got, wanted) {
			return BatchState{}, fmt.Errorf("merge batch %s already has different durable state", state.ID)
		}
		if err := s.writeActiveLocked(state.ID); err != nil {
			return BatchState{}, err
		}
		return durable, nil
	}
	transition := BatchTransition{Schema: BatchTransitionSchema, Sequence: 1, At: at, To: state.Status, State: state}
	if err := s.appendTransitionLocked(transition); err != nil {
		return BatchState{}, err
	}
	if err := s.writeActiveLocked(state.ID); err != nil {
		return BatchState{}, err
	}
	return state, nil
}

// SetActive atomically selects id and refuses to replace another active batch.
func (s *BatchStore) SetActive(id string) error {
	activeBatchMu.Lock()
	defer activeBatchMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireActiveBatchLock(s.activePath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.release() }()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("active merge batch id is required")
	}
	current, err := s.readActiveLocked()
	if err != nil {
		return err
	}
	if current.BatchID != "" && current.BatchID != id {
		return fmt.Errorf("merge batch %s is already active", current.BatchID)
	}
	return s.writeActiveLocked(id)
}

func (s *BatchStore) writeActiveLocked(id string) error {
	identity := activeBatchIdentity{Schema: activeBatchSchema, BatchID: id}
	encoded, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBatchFile(s.activePath, append(encoded, '\n'))
}

// ClearActive removes the identity only when it still names id.
func (s *BatchStore) ClearActive(id string) error {
	activeBatchMu.Lock()
	defer activeBatchMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireActiveBatchLock(s.activePath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.release() }()
	current, err := s.readActiveLocked()
	if err != nil || current.BatchID == "" {
		return err
	}
	if current.BatchID != id {
		return fmt.Errorf("merge batch %s is active, not %s", current.BatchID, id)
	}
	if err := os.Remove(s.activePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncBatchDirBestEffort(filepath.Dir(s.activePath))
}

func (s *BatchStore) readActiveLocked() (activeBatchIdentity, error) {
	content, err := os.ReadFile(s.activePath) // #nosec G304 -- path is rooted in Tao's repository data directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return activeBatchIdentity{}, nil
		}
		return activeBatchIdentity{}, err
	}
	identity, valid := decodeActiveBatchIdentity(content)
	if !valid {
		return activeBatchIdentity{}, nil
	}
	return identity, nil
}

func decodeActiveBatchIdentity(content []byte) (activeBatchIdentity, bool) {
	var identity activeBatchIdentity
	valid := json.Unmarshal(content, &identity) == nil && identity.Schema == activeBatchSchema && strings.TrimSpace(identity.BatchID) != ""
	return identity, valid
}

type activeBatchLock struct {
	file *os.File
}

func acquireActiveBatchLock(activePath string) (*activeBatchLock, error) {
	if err := os.MkdirAll(filepath.Dir(activePath), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(activePath+".lock", os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- path is rooted in Tao's repository data directory.
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock active merge batch: %w", err)
	}
	return &activeBatchLock{file: file}, nil
}

func (l *activeBatchLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	return errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close())
}

func readBatchSnapshot(path string) (BatchState, bool) {
	content, err := os.ReadFile(path) // #nosec G304 -- path is rooted in Tao's repository data directory.
	if err != nil {
		return BatchState{}, false
	}
	var state BatchState
	if json.Unmarshal(content, &state) != nil || state.validate() != nil {
		return BatchState{}, false
	}
	return state, true
}

func decodeBatchTransition(line []byte) (BatchTransition, bool) {
	var transition BatchTransition
	if json.Unmarshal(bytes.TrimSpace(line), &transition) != nil || transition.Schema != BatchTransitionSchema || transition.Sequence == 0 {
		return BatchTransition{}, false
	}
	if transition.State.validate() != nil || transition.State.LogSequence != transition.Sequence || transition.State.Status != transition.To {
		return BatchTransition{}, false
	}
	if ValidateBatchTransition(transition.From, transition.To) != nil {
		return BatchTransition{}, false
	}
	return transition, true
}

func appendBatchLogLine(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- path is rooted in Tao's repository data directory.
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func atomicWriteBatchFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*") // #nosec G304 -- path is rooted in Tao's repository data directory.
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncBatchDirBestEffort(dir)
}

func syncBatchDirBestEffort(dir string) error {
	file, err := os.Open(dir) // #nosec G304 -- path is rooted in Tao's repository data directory.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.ENOSYS) {
		return err
	}
	return nil
}
