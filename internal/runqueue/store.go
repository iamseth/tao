package runqueue

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	queueSnapshotFilename = "queue.json"
	queueEventLogFilename = "queue.jsonl"
)

// Store persists queue snapshots and the transition log needed to recover them.
type Store interface {
	Load() (QueueSnapshot, error)
	SaveSnapshot(QueueSnapshot) error
	AppendTransition(QueueTransition) error
}

// QueueTransition records one durable queue entry update. Removed transitions
// delete the matching entry from the recovered snapshot.
type QueueTransition struct {
	PlanID  string      `json:"plan_id"`
	Entry   *QueueEntry `json:"entry,omitempty"`
	Removed bool        `json:"removed,omitempty"`
}

// FileStore stores queue state in queue.json plus an append-only queue.jsonl log.
type FileStore struct {
	mu           sync.Mutex
	snapshotPath string
	logPath      string
}

// NewFileStore creates a file-backed store rooted at dir.
func NewFileStore(dir string) *FileStore {
	return NewFileStorePaths(filepath.Join(dir, queueSnapshotFilename), filepath.Join(dir, queueEventLogFilename))
}

// NewFileStorePaths creates a file-backed store with explicit artifact paths.
func NewFileStorePaths(snapshotPath string, logPath string) *FileStore {
	return &FileStore{snapshotPath: snapshotPath, logPath: logPath}
}

type persistedQueueSnapshot struct {
	Entries   []QueueEntry `json:"entries"`
	LogOffset int64        `json:"log_offset,omitempty"`
}

// Load reads queue.json, then replays queue.jsonl transitions appended after the
// snapshot watermark. Missing or partially-written JSON artifacts are treated as
// empty/incomplete data so a later valid transition can still recover the queue.
func (s *FileStore) Load() (QueueSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, logOffset, err := s.loadSnapshotLocked()
	if err != nil {
		return QueueSnapshot{}, err
	}
	return s.replayTransitionsLocked(snapshot, logOffset)
}

// SaveSnapshot atomically writes queue.json with a watermark of the current event
// log size. Future loads replay only transitions appended after that offset.
func (s *FileStore) SaveSnapshot(snapshot QueueSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	logOffset, err := s.logOffsetLocked()
	if err != nil {
		return err
	}
	entries := cloneQueueEntries(snapshot.Entries)
	for i := range entries {
		if entries[i].RunOptions == nil && entries[i].request.Input != "" {
			entries[i].prepareForPersistence()
		}
	}
	persisted := persistedQueueSnapshot{Entries: entries, LogOffset: logOffset}
	encoded, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return atomicWriteQueueFile(s.snapshotPath, encoded, 0o600)
}

// AppendTransition appends one fsynced transition record to queue.jsonl.
func (s *FileStore) AppendTransition(transition QueueTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized, err := normalizeQueueTransition(transition)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return appendQueueLogLine(s.logPath, encoded, 0o600)
}

func (s *FileStore) loadSnapshotLocked() (QueueSnapshot, int64, error) {
	file, err := os.Open(s.snapshotPath) // #nosec G304 -- queue artifacts are local files selected by Tao data-home/repo configuration.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return QueueSnapshot{}, 0, nil
		}
		return QueueSnapshot{}, 0, err
	}
	defer func() { _ = file.Close() }()

	var persisted persistedQueueSnapshot
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&persisted); err != nil {
		return QueueSnapshot{}, 0, nil
	}
	if persisted.LogOffset < 0 {
		persisted.LogOffset = 0
	}
	return QueueSnapshot{Entries: cloneQueueEntries(persisted.Entries)}, persisted.LogOffset, nil
}

func (s *FileStore) replayTransitionsLocked(snapshot QueueSnapshot, logOffset int64) (QueueSnapshot, error) {
	file, err := os.Open(s.logPath) // #nosec G304 -- queue artifacts are local files selected by Tao data-home/repo configuration.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, nil
		}
		return QueueSnapshot{}, err
	}
	defer func() { _ = file.Close() }()

	if logOffset < 0 {
		logOffset = 0
	}
	if _, err := file.Seek(logOffset, io.SeekStart); err != nil {
		return QueueSnapshot{}, err
	}

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if transition, ok := decodeQueueTransition(line); ok {
				applyQueueTransition(&snapshot, transition)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return snapshot, nil
			}
			return QueueSnapshot{}, err
		}
	}
}

func (s *FileStore) logOffsetLocked() (int64, error) {
	info, err := os.Stat(s.logPath) // #nosec G304 -- queue artifacts are local files selected by Tao data-home/repo configuration.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

func normalizeQueueTransition(transition QueueTransition) (QueueTransition, error) {
	if transition.Entry != nil {
		entry := *transition.Entry
		if transition.PlanID == "" {
			transition.PlanID = entry.PlanID
		}
		if entry.PlanID == "" {
			entry.PlanID = transition.PlanID
		}
		if entry.PlanID != transition.PlanID {
			return QueueTransition{}, fmt.Errorf("queue transition plan_id %q does not match entry plan_id %q", transition.PlanID, entry.PlanID)
		}
		transition.Entry = &entry
	}
	if transition.PlanID == "" {
		return QueueTransition{}, errors.New("queue transition missing plan_id")
	}
	if !transition.Removed && transition.Entry == nil {
		return QueueTransition{}, errors.New("queue transition missing entry")
	}
	return transition, nil
}

func decodeQueueTransition(line []byte) (QueueTransition, bool) {
	line = bytes.TrimSpace(line)
	var transition QueueTransition
	if err := json.Unmarshal(line, &transition); err == nil {
		if normalized, err := normalizeQueueTransition(transition); err == nil {
			return normalized, true
		}
	}

	var entry QueueEntry
	if err := json.Unmarshal(line, &entry); err == nil && entry.PlanID != "" {
		transition := QueueTransition{PlanID: entry.PlanID, Entry: &entry}
		if normalized, err := normalizeQueueTransition(transition); err == nil {
			return normalized, true
		}
	}
	return QueueTransition{}, false
}

func applyQueueTransition(snapshot *QueueSnapshot, transition QueueTransition) {
	index := findTransitionEntryIndex(snapshot.Entries, transition)
	if transition.Removed {
		if index >= 0 {
			snapshot.Entries = append(snapshot.Entries[:index], snapshot.Entries[index+1:]...)
		}
		return
	}
	if transition.Entry == nil {
		return
	}
	entry := *transition.Entry
	if index >= 0 {
		snapshot.Entries[index] = entry
		return
	}
	snapshot.Entries = append(snapshot.Entries, entry)
}

func findTransitionEntryIndex(entries []QueueEntry, transition QueueTransition) int {
	planID := transition.PlanID
	queuedAt := transitionEntryQueuedAt(transition)
	queuedAtZero := queuedAt.IsZero()
	match := -1
	for i, entry := range entries {
		if entry.PlanID != planID {
			continue
		}
		if !queuedAtZero && !entry.QueuedAt.Equal(queuedAt) {
			continue
		}
		match = i
	}
	return match
}

func transitionEntryQueuedAt(transition QueueTransition) time.Time {
	if transition.Entry == nil {
		return time.Time{}
	}
	return transition.Entry.QueuedAt
}

func cloneQueueEntries(entries []QueueEntry) []QueueEntry {
	if entries == nil {
		return nil
	}
	cloned := make([]QueueEntry, len(entries))
	copy(cloned, entries)
	return cloned
}

func appendQueueLogLine(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, perm) // #nosec G304 -- queue artifacts are local files selected by Tao data-home/repo configuration.
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

func atomicWriteQueueFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil { // #nosec G304 -- queue artifacts are local files selected by Tao data-home/repo configuration.
		perm = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*") // #nosec G304 -- queue artifacts are local files selected by Tao data-home/repo configuration.
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
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
	removeTemp = false
	return syncQueueDirBestEffort(dir)
}

func syncQueueDirBestEffort(dir string) error {
	file, err := os.Open(dir) // #nosec G304 -- queue artifact parent directories are selected by Tao data-home/repo configuration.
	if err != nil {
		if isUnsupportedQueueDirSyncError(err) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil && !isUnsupportedQueueDirSyncError(err) {
		return err
	}
	return nil
}

func isUnsupportedQueueDirSyncError(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}
