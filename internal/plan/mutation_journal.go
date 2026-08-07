package plan

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

const (
	mutationJournalFile     = ".mutation.json"
	mutationPersistenceLock = ".mutation.lock"
	mutationJournalSchema   = "tao.plan.mutation.v1"
)

// mutationJournal is the durable intent for one state/slices/review/events mutation.
// Payload bytes are the exact bytes to install or append after the journal is
// durable; their hashes detect malformed or accidentally altered journals.
type mutationJournal struct {
	Schema     string                     `json:"schema"`
	MutationID string                     `json:"mutation_id"`
	PlanID     string                     `json:"plan_id"`
	CreatedAt  time.Time                  `json:"created_at"`
	State      *mutationJournalPayload    `json:"state,omitempty"`
	Slices     *mutationJournalPayload    `json:"slices,omitempty"`
	Review     *mutationJournalPayload    `json:"review,omitempty"`
	Events     []mutationJournalEvent     `json:"events"`
	Extra      map[string]json.RawMessage `json:"-"`
}

type mutationJournalPayload struct {
	Payload []byte                     `json:"payload"`
	SHA256  string                     `json:"sha256"`
	Extra   map[string]json.RawMessage `json:"-"`
}

type mutationJournalEvent struct {
	Payload []byte                     `json:"payload"`
	SHA256  string                     `json:"sha256"`
	Extra   map[string]json.RawMessage `json:"-"`
}

func newMutationJournalPayload(payload []byte) *mutationJournalPayload {
	return &mutationJournalPayload{Payload: append([]byte(nil), payload...), SHA256: mutationPayloadHash(payload)}
}

func newMutationJournalEvent(payload []byte) mutationJournalEvent {
	return mutationJournalEvent{Payload: append([]byte(nil), payload...), SHA256: mutationPayloadHash(payload)}
}

// encodeMutationJournal validates journal semantics and returns its durable,
// newline-terminated representation. Unknown fields retained during decoding
// are emitted again.
func encodeMutationJournal(journal mutationJournal) ([]byte, error) {
	if err := validateMutationJournal(journal, journal.PlanID); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode mutation journal: %w", err)
	}
	return append(encoded, '\n'), nil
}

// decodeMutationJournal parses and validates a journal for the plan directory
// identified by expectedPlanID. It does not inspect or mutate filesystem state.
func decodeMutationJournal(data []byte, expectedPlanID string) (mutationJournal, error) {
	var journal mutationJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return mutationJournal{}, fmt.Errorf("decode mutation journal: %w", err)
	}
	if err := validateMutationJournal(journal, expectedPlanID); err != nil {
		return mutationJournal{}, err
	}
	return journal, nil
}

func validateMutationJournal(journal mutationJournal, expectedPlanID string) error {
	if journal.Schema != mutationJournalSchema {
		return fmt.Errorf("unsupported mutation journal schema %q", journal.Schema)
	}
	if journal.MutationID == "" {
		return fmt.Errorf("mutation journal mutation_id is required")
	}
	if journal.PlanID == "" {
		return fmt.Errorf("mutation journal plan_id is required")
	}
	if expectedPlanID != "" && journal.PlanID != expectedPlanID {
		return fmt.Errorf("mutation journal plan_id %q does not match plan %q", journal.PlanID, expectedPlanID)
	}
	if journal.CreatedAt.IsZero() {
		return fmt.Errorf("mutation journal created_at is required")
	}
	if journal.State == nil && journal.Slices == nil && journal.Review == nil && len(journal.Events) == 0 {
		return fmt.Errorf("mutation journal requires at least one target")
	}
	if journal.State != nil {
		if err := validateMutationPayload("state", journal.State.Payload, journal.State.SHA256); err != nil {
			return err
		}
		var state State
		if err := json.Unmarshal(journal.State.Payload, &state); err != nil {
			return fmt.Errorf("mutation journal state payload: %w", err)
		}
		if state.Plan.ID != journal.PlanID {
			return fmt.Errorf("mutation journal state plan id %q does not match journal plan %q", state.Plan.ID, journal.PlanID)
		}
	}
	if journal.Slices != nil {
		if err := validateMutationPayload("slices", journal.Slices.Payload, journal.Slices.SHA256); err != nil {
			return err
		}
		var slices SlicesFile
		if err := json.Unmarshal(journal.Slices.Payload, &slices); err != nil {
			return fmt.Errorf("mutation journal slices payload: %w", err)
		}
		if slices.PlanID != journal.PlanID {
			return fmt.Errorf("mutation journal slices plan id %q does not match journal plan %q", slices.PlanID, journal.PlanID)
		}
	}
	if journal.Review != nil {
		if err := validateMutationBytes("review", journal.Review.Payload, journal.Review.SHA256, false); err != nil {
			return err
		}
	}

	eventHashes := make(map[string]struct{}, len(journal.Events))
	for i, entry := range journal.Events {
		name := fmt.Sprintf("events[%d]", i)
		if err := validateMutationPayload(name, entry.Payload, entry.SHA256); err != nil {
			return err
		}
		if _, duplicate := eventHashes[entry.SHA256]; duplicate {
			return fmt.Errorf("mutation journal %s duplicates an earlier event payload", name)
		}
		eventHashes[entry.SHA256] = struct{}{}
		var event Event
		if err := json.Unmarshal(entry.Payload, &event); err != nil {
			return fmt.Errorf("mutation journal %s payload: %w", name, err)
		}
		if event.PlanID != journal.PlanID {
			return fmt.Errorf("mutation journal %s plan_id %q does not match journal plan %q", name, event.PlanID, journal.PlanID)
		}
		if event.MutationID != journal.MutationID {
			return fmt.Errorf("mutation journal %s mutation_id %q does not match journal mutation %q", name, event.MutationID, journal.MutationID)
		}
	}
	return nil
}

func validateMutationPayload(name string, payload []byte, wantHash string) error {
	if err := validateMutationBytes(name, payload, wantHash, true); err != nil {
		return err
	}
	if !json.Valid(payload) {
		return fmt.Errorf("mutation journal %s payload is not valid JSON", name)
	}
	return nil
}

func validateMutationBytes(name string, payload []byte, wantHash string, requireContent bool) error {
	if requireContent && len(payload) == 0 {
		return fmt.Errorf("mutation journal %s payload is required", name)
	}
	decodedHash, err := hex.DecodeString(wantHash)
	if err != nil || len(decodedHash) != sha256.Size {
		return fmt.Errorf("mutation journal %s sha256 is invalid", name)
	}
	if got := mutationPayloadHash(payload); got != wantHash {
		return fmt.Errorf("mutation journal %s sha256 mismatch: got %s, want %s", name, got, wantHash)
	}
	return nil
}

func mutationPayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// mutationJournalIO is the failure-injection seam for journal installation and
// settlement. Each mutating method is one durable protocol step.
type mutationJournalIO interface {
	readFile(path string) ([]byte, error)
	installJournal(path string, data []byte) error
	installTarget(path string, data []byte) error
	appendEvent(path string, payload []byte) error
	syncEvents(path string) error
	syncPlanDir(path string) error
	removeJournal(path string) error
}

type fileMutationJournalIO struct{}

func (fileMutationJournalIO) readFile(path string) ([]byte, error) {
	return os.ReadFile(path) // #nosec G304 -- paths are plan artifacts selected by the caller.
}

func (fileMutationJournalIO) installJournal(path string, data []byte) error {
	return atomicfile.Write(path, data, atomicfile.Options{Exclusive: true})
}

func (fileMutationJournalIO) installTarget(path string, data []byte) error {
	return atomicfile.Write(path, data, atomicfile.Options{})
}

func (fileMutationJournalIO) appendEvent(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600) // #nosec G304 -- path is an internally constructed plan artifact path.
	if err != nil {
		return err
	}
	entry := make([]byte, 0, len(payload)+2)
	if info, statErr := file.Stat(); statErr != nil {
		_ = file.Close()
		return statErr
	} else if info.Size() > 0 {
		last := []byte{0}
		if _, readErr := file.ReadAt(last, info.Size()-1); readErr != nil {
			_ = file.Close()
			return readErr
		}
		if last[0] != '\n' {
			entry = append(entry, '\n')
		}
	}
	entry = append(entry, payload...)
	entry = append(entry, '\n')
	if _, err := file.Write(entry); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (fileMutationJournalIO) syncEvents(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- path is an internally constructed plan artifact path.
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (fileMutationJournalIO) syncPlanDir(path string) error {
	return atomicfile.SyncDir(path)
}

func (fileMutationJournalIO) removeJournal(path string) error {
	return atomicfile.Remove(path, atomicfile.RemoveOptions{})
}

// withMutationPersistenceLock serializes all journal inspection and settlement
// for one plan across both goroutines and processes. The lock file is retained:
// unlinking it could let waiters lock different inodes and enter concurrently.
func withMutationPersistenceLock[T any](planDir string, operation func() (T, error)) (result T, err error) {
	path := filepath.Join(planDir, mutationPersistenceLock)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the lock is an internal plan artifact.
	if err != nil {
		return result, fmt.Errorf("open %s: %w", mutationPersistenceLock, err)
	}
	return withOpenMutationPersistenceLock(file, operation)
}

// withMutationPersistenceReadBoundary avoids creating recovery metadata merely
// to inspect a legacy plan. Once a writer has created the persistent lock,
// readers use it for recovery and their artifact snapshot. If a writer creates
// it during an unlocked legacy read, the read is repeated under that lock.
func withMutationPersistenceReadBoundary[T any](planDir string, operation func(recover bool) (T, error)) (result T, err error) {
	lockPath := filepath.Join(planDir, mutationPersistenceLock)
	file, err := os.Open(lockPath) // #nosec G304 -- the lock is an internal plan artifact.
	if err == nil {
		return withOpenMutationPersistenceLock(file, func() (T, error) { return operation(true) })
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("open %s: %w", mutationPersistenceLock, err)
	}

	journalPath := filepath.Join(planDir, mutationJournalFile)
	if _, err := os.Stat(journalPath); err == nil {
		return withMutationPersistenceLock(planDir, func() (T, error) { return operation(true) })
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("inspect %s: %w", mutationJournalFile, err)
	}

	result, operationErr := operation(false)
	file, err = os.Open(lockPath) // #nosec G304 -- the lock is an internal plan artifact.
	if err == nil {
		return withOpenMutationPersistenceLock(file, func() (T, error) { return operation(true) })
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, errors.Join(operationErr, fmt.Errorf("open %s: %w", mutationPersistenceLock, err))
	}
	return result, operationErr
}

func withOpenMutationPersistenceLock[T any](file *os.File, operation func() (T, error)) (result T, err error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return result, errors.Join(fmt.Errorf("lock %s: %w", mutationPersistenceLock, err), file.Close())
	}
	defer func() {
		releaseErr := errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
		if releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock %s: %w", mutationPersistenceLock, releaseErr))
		}
	}()
	return operation()
}

// installAndSettleMutation recovers any earlier valid intent, installs the new
// intent exclusively, and then rolls it forward under the per-plan persistence
// lock. No new target bytes are written unless journal installation succeeds.
func installAndSettleMutation(store mutationJournalIO, planDir string, journal mutationJournal) error {
	_, err := withMutationPersistenceLock(planDir, func() (struct{}, error) {
		return struct{}{}, installAndSettleMutationLocked(store, planDir, journal)
	})
	return err
}

// installAndSettleMutationLocked requires mutationPersistenceLock for planDir.
func installAndSettleMutationLocked(store mutationJournalIO, planDir string, journal mutationJournal) error {
	if _, err := settlePendingMutationLocked(store, planDir, journal.PlanID); err != nil {
		return err
	}
	encoded, err := encodeMutationJournal(journal)
	if err != nil {
		return err
	}
	journalPath := filepath.Join(planDir, mutationJournalFile)
	if err := store.installJournal(journalPath, encoded); err != nil {
		return fmt.Errorf("install %s: %w", mutationJournalFile, err)
	}
	return settleMutationJournal(store, planDir, journal)
}

// settlePendingMutation validates and replays a pending journal while holding
// the same persistence lock used by writers. It reports whether one was found.
func settlePendingMutation(store mutationJournalIO, planDir, expectedPlanID string) (bool, error) {
	return withMutationPersistenceLock(planDir, func() (bool, error) {
		return settlePendingMutationLocked(store, planDir, expectedPlanID)
	})
}

// settlePendingMutationLocked performs the complete scan/replay/removal
// sequence. Callers must hold mutationPersistenceLock for planDir.
func settlePendingMutationLocked(store mutationJournalIO, planDir, expectedPlanID string) (bool, error) {
	journalPath := filepath.Join(planDir, mutationJournalFile)
	data, err := store.readFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("read %s: %w", mutationJournalFile, err)
	}
	journal, err := decodeMutationJournal(data, expectedPlanID)
	if err != nil {
		return true, fmt.Errorf("invalid %s: %w", mutationJournalFile, err)
	}
	if err := settleMutationJournal(store, planDir, journal); err != nil {
		return true, err
	}
	return true, nil
}

// settleMutationJournal requires mutationPersistenceLock for planDir.
func settleMutationJournal(store mutationJournalIO, planDir string, journal mutationJournal) error {
	eventPath := filepath.Join(planDir, "events.jsonl")
	present, err := mutationEventHashes(store, eventPath, journal)
	if err != nil {
		return fmt.Errorf("settle %s events.jsonl: %w", mutationJournalFile, err)
	}
	if journal.State != nil {
		if err := installMutationTarget(store, filepath.Join(planDir, "state.json"), *journal.State); err != nil {
			return fmt.Errorf("settle %s state.json: %w", mutationJournalFile, err)
		}
	}
	if journal.Slices != nil {
		if err := installMutationTarget(store, filepath.Join(planDir, "slices.json"), *journal.Slices); err != nil {
			return fmt.Errorf("settle %s slices.json: %w", mutationJournalFile, err)
		}
	}
	if journal.Review != nil {
		if err := installMutationTarget(store, filepath.Join(planDir, ReviewFile), *journal.Review); err != nil {
			return fmt.Errorf("settle %s %s: %w", mutationJournalFile, ReviewFile, err)
		}
	}

	for _, event := range journal.Events {
		if _, ok := present[event.SHA256]; ok {
			continue
		}
		if err := store.appendEvent(eventPath, event.Payload); err != nil {
			return fmt.Errorf("settle %s events.jsonl: %w", mutationJournalFile, err)
		}
		present[event.SHA256] = struct{}{}
	}
	if len(journal.Events) > 0 {
		// A prior append may have made an event visible before its Sync failed.
		// Always sync after deduplication so replay cannot remove the journal
		// based only on a visible, but potentially non-durable, event.
		if err := store.syncEvents(eventPath); err != nil {
			return fmt.Errorf("settle %s events.jsonl: %w", mutationJournalFile, err)
		}
	}
	// Sync after every target installation so a newly created events.jsonl
	// directory entry is durable before unlinking the recovery intent. This
	// makes a post-unlink sync failure safe to treat as committed.
	if err := store.syncPlanDir(planDir); err != nil {
		return fmt.Errorf("settle %s plan directory: %w", mutationJournalFile, err)
	}
	if err := store.removeJournal(filepath.Join(planDir, mutationJournalFile)); err != nil {
		// Once unlink succeeds, this process has crossed the transaction commit
		// point even if the parent sync fails. The pre-unlink directory sync
		// established every target entry, while a crash may only resurrect the
		// valid journal; its replay is idempotent.
		if atomicfile.IsPostRemoveSyncError(err) {
			return nil
		}
		return fmt.Errorf("remove %s: %w", mutationJournalFile, err)
	}
	return nil
}

func installMutationTarget(store mutationJournalIO, path string, payload mutationJournalPayload) error {
	existing, err := store.readFile(path)
	if err == nil && mutationPayloadHash(existing) == payload.SHA256 {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return store.installTarget(path, payload.Payload)
}

func mutationEventHashes(store mutationJournalIO, path string, journal mutationJournal) (map[string]struct{}, error) {
	data, err := store.readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]struct{}), nil
	}
	if err != nil {
		return nil, err
	}

	expected := make(map[string]struct{}, len(journal.Events))
	for _, event := range journal.Events {
		expected[event.SHA256] = struct{}{}
	}
	present := make(map[string]struct{}, len(journal.Events))
	reader := bufio.NewReader(bytes.NewReader(data))
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			payload := bytes.TrimSuffix(line, []byte{'\n'})
			payload = bytes.TrimSuffix(payload, []byte{'\r'})
			var event Event
			if json.Unmarshal(payload, &event) == nil && event.MutationID == journal.MutationID {
				hash := mutationPayloadHash(payload)
				if _, ok := expected[hash]; !ok {
					return nil, fmt.Errorf("mutation_id %q has conflicting event payload %s", journal.MutationID, hash)
				}
				present[hash] = struct{}{}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return present, nil
}

func (journal *mutationJournal) UnmarshalJSON(data []byte) error {
	type plain mutationJournal
	var decoded plain
	extra, err := decodeMutationExtras(data, &decoded, "schema", "mutation_id", "plan_id", "created_at", "state", "slices", "review", "events")
	if err != nil {
		return err
	}
	*journal = mutationJournal(decoded)
	journal.Extra = extra
	return nil
}

func (journal mutationJournal) MarshalJSON() ([]byte, error) {
	type plain mutationJournal
	return encodeMutationExtras(plain(journal), journal.Extra)
}

func (payload *mutationJournalPayload) UnmarshalJSON(data []byte) error {
	type plain mutationJournalPayload
	var decoded plain
	extra, err := decodeMutationExtras(data, &decoded, "payload", "sha256")
	if err != nil {
		return err
	}
	*payload = mutationJournalPayload(decoded)
	payload.Extra = extra
	return nil
}

func (payload mutationJournalPayload) MarshalJSON() ([]byte, error) {
	type plain mutationJournalPayload
	return encodeMutationExtras(plain(payload), payload.Extra)
}

func (event *mutationJournalEvent) UnmarshalJSON(data []byte) error {
	type plain mutationJournalEvent
	var decoded plain
	extra, err := decodeMutationExtras(data, &decoded, "payload", "sha256")
	if err != nil {
		return err
	}
	*event = mutationJournalEvent(decoded)
	event.Extra = extra
	return nil
}

func (event mutationJournalEvent) MarshalJSON() ([]byte, error) {
	type plain mutationJournalEvent
	return encodeMutationExtras(plain(event), event.Extra)
}

func decodeMutationExtras(data []byte, out any, known ...string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range known {
		delete(fields, name)
	}
	return fields, nil
}

func encodeMutationExtras(known any, extra map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	for name, value := range extra {
		if _, exists := fields[name]; !exists {
			fields[name] = value
		}
	}
	return json.Marshal(fields)
}
