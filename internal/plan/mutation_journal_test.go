package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

func TestMutationJournalDecodeValidation(t *testing.T) {
	valid := testMutationJournal(t)
	validBytes, err := encodeMutationJournal(valid)
	if err != nil {
		t.Fatal(err)
	}

	wrongHash := valid
	wrongHash.State.SHA256 = strings.Repeat("0", 64)
	wrongHashBytes, err := json.Marshal(wrongHash)
	if err != nil {
		t.Fatal(err)
	}
	unsupported := valid
	unsupported.Schema = "tao.plan.mutation.v2"
	unsupportedBytes, err := json.Marshal(unsupported)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		data           []byte
		expectedPlanID string
		wantError      string
	}{
		{name: "valid", data: validBytes, expectedPlanID: "plan-a"},
		{name: "malformed", data: []byte(`{"schema":`), expectedPlanID: "plan-a", wantError: "decode mutation journal"},
		{name: "wrong plan", data: validBytes, expectedPlanID: "plan-b", wantError: `plan_id "plan-a" does not match plan "plan-b"`},
		{name: "hash mismatched", data: wrongHashBytes, expectedPlanID: "plan-a", wantError: "state sha256 mismatch"},
		{name: "unsupported schema", data: unsupportedBytes, expectedPlanID: "plan-a", wantError: `unsupported mutation journal schema "tao.plan.mutation.v2"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeMutationJournal(test.data, test.expectedPlanID)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("decode valid journal: %v", err)
				}
				if got.MutationID != valid.MutationID || string(got.State.Payload) != string(valid.State.Payload) || string(got.Events[0].Payload) != string(valid.Events[0].Payload) {
					t.Fatalf("unexpected decoded journal: %+v", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("decode error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestMutationJournalPreservesUnknownFields(t *testing.T) {
	journal := testMutationJournal(t)
	encoded, err := encodeMutationJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	raw["future_root"] = map[string]any{"enabled": true}
	raw["state"].(map[string]any)["future_target"] = "state-value"
	raw["events"].([]any)[0].(map[string]any)["future_event"] = float64(7)
	withUnknown, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := decodeMutationJournal(withUnknown, "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := encodeMutationJournal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(roundTrip, &got); err != nil {
		t.Fatal(err)
	}
	if got["future_root"].(map[string]any)["enabled"] != true {
		t.Fatalf("root unknown field was not preserved: %#v", got)
	}
	if got["state"].(map[string]any)["future_target"] != "state-value" {
		t.Fatalf("target unknown field was not preserved: %#v", got["state"])
	}
	if got["events"].([]any)[0].(map[string]any)["future_event"] != float64(7) {
		t.Fatalf("event unknown field was not preserved: %#v", got["events"])
	}
}

func TestMutationJournalRejectsEventMutationIDMismatch(t *testing.T) {
	journal := testMutationJournal(t)
	var event Event
	if err := json.Unmarshal(journal.Events[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	event.MutationID = "another-mutation"
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	journal.Events[0] = newMutationJournalEvent(payload)

	if _, err := encodeMutationJournal(journal); err == nil || !strings.Contains(err.Error(), "mutation_id") {
		t.Fatalf("encode error = %v, want event mutation ID mismatch", err)
	}
}

func TestMutationJournalSettlementFailurePrefixesAreRecoverable(t *testing.T) {
	tests := []struct {
		name            string
		failOperation   string
		journalExpected bool
	}{
		{name: "journal install", failOperation: "journal", journalExpected: false},
		{name: "state replay", failOperation: "state", journalExpected: true},
		{name: "slices replay", failOperation: "slices", journalExpected: true},
		{name: "first event replay", failOperation: "event-1", journalExpected: true},
		{name: "second event replay", failOperation: "event-2", journalExpected: true},
		{name: "event sync", failOperation: "events-sync", journalExpected: true},
		{name: "plan directory sync", failOperation: "plan-dir-sync", journalExpected: true},
		{name: "journal removal", failOperation: "remove", journalExpected: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "plan-a")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			originalState := []byte(`{"schema":"tao.plan.state.v1","plan":{"id":"plan-a"},"old":"state"}`)
			originalSlices := []byte(`{"schema":"tao.plan.slices.v1","plan_id":"plan-a","old":"slices"}`)
			if err := os.WriteFile(filepath.Join(dir, "state.json"), originalState, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "slices.json"), originalSlices, 0o600); err != nil {
				t.Fatal(err)
			}

			journal := testMutationJournalWithTwoEvents(t)
			store := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: test.failOperation}
			err := installAndSettleMutation(store, dir, journal)
			if err == nil || !strings.Contains(err.Error(), "injected "+test.failOperation+" failure") {
				t.Fatalf("settlement error = %v, want injected failure", err)
			}

			_, journalErr := os.Stat(filepath.Join(dir, mutationJournalFile))
			if test.journalExpected && journalErr != nil {
				t.Fatalf("journal missing after incomplete settlement: %v", journalErr)
			}
			if !test.journalExpected && !errors.Is(journalErr, os.ErrNotExist) {
				t.Fatalf("journal unexpectedly installed: %v", journalErr)
			}
			if test.failOperation == "journal" {
				assertMutationFile(t, filepath.Join(dir, "state.json"), originalState)
				assertMutationFile(t, filepath.Join(dir, "slices.json"), originalSlices)
				if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("event target changed before journal installation: %v", err)
				}
				return
			}

			found, err := settlePendingMutation(fileMutationJournalIO{}, dir, "plan-a")
			if err != nil || !found {
				t.Fatalf("recover pending journal: found=%t err=%v", found, err)
			}
			found, err = settlePendingMutation(fileMutationJournalIO{}, dir, "plan-a")
			if err != nil || found {
				t.Fatalf("repeat settlement should be a no-op: found=%t err=%v", found, err)
			}
			assertMutationFile(t, filepath.Join(dir, "state.json"), journal.State.Payload)
			assertMutationFile(t, filepath.Join(dir, "slices.json"), journal.Slices.Payload)
			events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
			if err != nil || len(warnings) != 0 {
				t.Fatalf("read settled events: warnings=%v err=%v", warnings, err)
			}
			if len(events) != 2 || events[0].MutationID != journal.MutationID || events[1].MutationID != journal.MutationID {
				t.Fatalf("settled events were not appended exactly once: %#v", events)
			}
		})
	}
}

func TestMutationJournalRecoverySyncsVisibleEventBeforeRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := testMutationJournal(t)
	store := &visibleAppendSyncFailureMutationJournalIO{delegate: fileMutationJournalIO{}}

	err := installAndSettleMutation(store, dir, journal)
	if err == nil || !strings.Contains(err.Error(), "injected event sync failure after visible append") {
		t.Fatalf("settlement error = %v, want visible append sync failure", err)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); err != nil {
		t.Fatalf("journal missing after event sync failure: %v", err)
	}
	assertMutationEvents(t, dir, journal, 1)

	found, err := settlePendingMutation(store, dir, journal.PlanID)
	if err != nil || !found {
		t.Fatalf("recover visible event: found=%t err=%v", found, err)
	}
	if !store.syncedAfterFailure {
		t.Fatal("recovery did not sync the deduplicated visible event")
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after durable event recovery: %v", err)
	}
	assertMutationEvents(t, dir, journal, 1)
}

func TestMutationJournalPostUnlinkSyncFailureCommitsNewEventAfterDirectorySync(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("events.jsonl unexpectedly exists before settlement: %v", err)
	}
	journal := testMutationJournal(t)
	store := &postUnlinkSyncFailureMutationJournalIO{delegate: fileMutationJournalIO{}}

	if err := installAndSettleMutation(store, dir, journal); err != nil {
		t.Fatalf("settle after successful journal unlink: %v", err)
	}
	if !store.failed {
		t.Fatal("directory sync failure was not injected")
	}
	if !store.planDirSyncedBeforeRemove {
		t.Fatal("plan directory was not synced before journal unlink")
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after successful unlink: %v", err)
	}
	assertMutationFile(t, filepath.Join(dir, "state.json"), journal.State.Payload)
	assertMutationFile(t, filepath.Join(dir, "slices.json"), journal.Slices.Payload)
	events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 || len(events) != 1 || events[0].MutationID != journal.MutationID {
		t.Fatalf("settled events = %#v warnings=%v err=%v", events, warnings, err)
	}
}

func TestMutationJournalSettlementRejectsConflictingEvent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := testMutationJournal(t)
	encoded, err := encodeMutationJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutationJournalFile), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	conflict, err := json.Marshal(Event{Type: EventTypeSliceCompleted, Timestamp: journal.CreatedAt, PlanID: journal.PlanID, MutationID: journal.MutationID, SliceID: "001-a", Message: "different payload"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), append(conflict, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	found, err := settlePendingMutation(fileMutationJournalIO{}, dir, "plan-a")
	if !found || err == nil || !strings.Contains(err.Error(), "conflicting event payload") {
		t.Fatalf("conflict settlement = found %t, error %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); err != nil {
		t.Fatalf("conflicting journal should remain: %v", err)
	}
}

func TestMutationJournalLegacyReadOnlyPlanWithoutJournalLoads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"schema":"tao.plan.state.v1","plan":{"id":"plan-a","title":"Plan A"}}`)
	slices := []byte(`{"schema":"tao.plan.slices.v1","plan_id":"plan-a","slices":[]}`)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), slices, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: directory needs execute permission for read-only inspection.
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: restore owner access for temporary-directory cleanup.
			t.Errorf("restore plan directory permissions: %v", err)
		}
	})
	probePath := filepath.Join(dir, "write-probe")
	if err := os.WriteFile(probePath, []byte("probe"), 0o600); err == nil {
		_ = os.Remove(probePath)
		t.Skip("filesystem permissions do not enforce a read-only plan directory")
	}

	gotState, err := ReadState(dir)
	if err != nil {
		t.Fatalf("read legacy state from read-only directory: %v", err)
	}
	if gotState.Plan.ID != "plan-a" {
		t.Fatalf("read state plan ID = %q, want plan-a", gotState.Plan.ID)
	}
	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatalf("load legacy plan from read-only directory: %v", err)
	}
	if files.state.Plan.ID != "plan-a" || files.slices.PlanID != "plan-a" {
		t.Fatalf("unexpected loaded artifacts: state=%#v slices=%#v", files.state, files.slices)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationPersistenceLock)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy read created %s: %v", mutationPersistenceLock, err)
	}
}

func TestMutationJournalLoadBoundarySettlesPendingIntent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := testMutationJournal(t)
	encoded, err := encodeMutationJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutationJournalFile), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files.state.Plan.ID != "plan-a" || files.slices.PlanID != "plan-a" || len(files.events) != 1 || files.events[0].MutationID != journal.MutationID {
		t.Fatalf("load did not observe settled journal: state=%#v slices=%#v events=%#v", files.state, files.slices, files.events)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled journal remains after load: %v", err)
	}
}

func TestMutationJournalConcurrentReadersRecoverEventExactlyOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := testMutationJournal(t)
	encoded, err := encodeMutationJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutationJournalFile), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	// Widen the pre-append event-scan window so the test exercises readers that
	// begin recovery together rather than relying on scheduler timing alone.
	legacyLine := strings.Repeat("x", 4*maxEventJSONLLineBytes) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(legacyLine), 0o600); err != nil {
		t.Fatal(err)
	}

	const readers = 8
	start := make(chan struct{})
	errs := make(chan error, readers)
	var ready sync.WaitGroup
	ready.Add(readers)
	for i := range readers {
		go func(stateOnly bool) {
			ready.Done()
			<-start
			if stateOnly {
				_, readErr := ReadState(dir)
				errs <- readErr
				return
			}
			_, loadErr := loadPlanFiles(dir)
			errs <- loadErr
		}(i%2 == 0)
	}
	ready.Wait()
	close(start)
	for range readers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent recovery: %v", err)
		}
	}

	events, _, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	matching := 0
	for _, event := range events {
		if event.MutationID == journal.MutationID {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("journal event appended %d times, want exactly once", matching)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled journal remains after concurrent recovery: %v", err)
	}
}

func TestMutationJournalLoadBoundarySurfacesInvalidIntentWithoutChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plan-a")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"schema":"tao.plan.state.v1","plan":{"id":"plan-a"}}`)
	slices := []byte(`{"schema":"tao.plan.slices.v1","plan_id":"plan-a"}`)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slices.json"), slices, 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := []byte(`{"schema":"tao.plan.mutation.v1","plan_id":"plan-a"}`)
	if err := os.WriteFile(filepath.Join(dir, mutationJournalFile), invalid, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadPlanFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "invalid .mutation.json") {
		t.Fatalf("load error = %v, want invalid journal", err)
	}
	assertMutationFile(t, filepath.Join(dir, "state.json"), state)
	assertMutationFile(t, filepath.Join(dir, "slices.json"), slices)
	assertMutationFile(t, filepath.Join(dir, mutationJournalFile), invalid)
}

type postUnlinkSyncFailureMutationJournalIO struct {
	delegate                  mutationJournalIO
	failed                    bool
	planDirSyncedBeforeRemove bool
}

func (s *postUnlinkSyncFailureMutationJournalIO) readFile(path string) ([]byte, error) {
	return s.delegate.readFile(path)
}

func (s *postUnlinkSyncFailureMutationJournalIO) installJournal(path string, data []byte) error {
	return s.delegate.installJournal(path, data)
}

func (s *postUnlinkSyncFailureMutationJournalIO) installTarget(path string, data []byte) error {
	return s.delegate.installTarget(path, data)
}

func (s *postUnlinkSyncFailureMutationJournalIO) appendEvent(path string, payload []byte) error {
	return s.delegate.appendEvent(path, payload)
}

func (s *postUnlinkSyncFailureMutationJournalIO) syncEvents(path string) error {
	return s.delegate.syncEvents(path)
}

func (s *postUnlinkSyncFailureMutationJournalIO) syncPlanDir(path string) error {
	if err := s.delegate.syncPlanDir(path); err != nil {
		return err
	}
	s.planDirSyncedBeforeRemove = true
	return nil
}

func (s *postUnlinkSyncFailureMutationJournalIO) removeJournal(path string) error {
	if s.failed {
		return s.delegate.removeJournal(path)
	}
	s.failed = true
	return atomicfile.Remove(path, atomicfile.RemoveOptions{SyncDir: func(string) error {
		return errors.New("injected post-unlink directory sync failure")
	}})
}

type visibleAppendSyncFailureMutationJournalIO struct {
	delegate           mutationJournalIO
	appendFailed       bool
	syncedAfterFailure bool
}

func (s *visibleAppendSyncFailureMutationJournalIO) readFile(path string) ([]byte, error) {
	return s.delegate.readFile(path)
}

func (s *visibleAppendSyncFailureMutationJournalIO) installJournal(path string, data []byte) error {
	return s.delegate.installJournal(path, data)
}

func (s *visibleAppendSyncFailureMutationJournalIO) installTarget(path string, data []byte) error {
	return s.delegate.installTarget(path, data)
}

func (s *visibleAppendSyncFailureMutationJournalIO) appendEvent(path string, payload []byte) error {
	if err := s.delegate.appendEvent(path, payload); err != nil {
		return err
	}
	if !s.appendFailed {
		s.appendFailed = true
		return errors.New("injected event sync failure after visible append")
	}
	return nil
}

func (s *visibleAppendSyncFailureMutationJournalIO) syncEvents(path string) error {
	if s.appendFailed {
		s.syncedAfterFailure = true
	}
	return s.delegate.syncEvents(path)
}

func (s *visibleAppendSyncFailureMutationJournalIO) syncPlanDir(path string) error {
	return s.delegate.syncPlanDir(path)
}

func (s *visibleAppendSyncFailureMutationJournalIO) removeJournal(path string) error {
	return s.delegate.removeJournal(path)
}

type failingMutationJournalIO struct {
	delegate      mutationJournalIO
	failOperation string
	eventNumber   int
	failed        bool
}

func (s *failingMutationJournalIO) fail(operation string) error {
	if !s.failed && s.failOperation == operation {
		s.failed = true
		return fmt.Errorf("injected %s failure", operation)
	}
	return nil
}

func (s *failingMutationJournalIO) readFile(path string) ([]byte, error) {
	return s.delegate.readFile(path)
}

func (s *failingMutationJournalIO) installJournal(path string, data []byte) error {
	if err := s.fail("journal"); err != nil {
		return err
	}
	return s.delegate.installJournal(path, data)
}

func (s *failingMutationJournalIO) installTarget(path string, data []byte) error {
	operation := strings.TrimSuffix(filepath.Base(path), ".json")
	if err := s.fail(operation); err != nil {
		return err
	}
	return s.delegate.installTarget(path, data)
}

func (s *failingMutationJournalIO) appendEvent(path string, payload []byte) error {
	s.eventNumber++
	if err := s.fail(fmt.Sprintf("event-%d", s.eventNumber)); err != nil {
		return err
	}
	return s.delegate.appendEvent(path, payload)
}

func (s *failingMutationJournalIO) syncEvents(path string) error {
	if err := s.fail("events-sync"); err != nil {
		return err
	}
	return s.delegate.syncEvents(path)
}

func (s *failingMutationJournalIO) syncPlanDir(path string) error {
	if err := s.fail("plan-dir-sync"); err != nil {
		return err
	}
	return s.delegate.syncPlanDir(path)
}

func (s *failingMutationJournalIO) removeJournal(path string) error {
	if err := s.fail("remove"); err != nil {
		return err
	}
	return s.delegate.removeJournal(path)
}

func testMutationJournalWithTwoEvents(t *testing.T) mutationJournal {
	t.Helper()
	journal := testMutationJournal(t)
	payload, err := json.Marshal(Event{
		Type:       EventTypeSliceCompleted,
		Timestamp:  journal.CreatedAt.Add(time.Minute),
		PlanID:     journal.PlanID,
		MutationID: journal.MutationID,
		SliceID:    "001-a",
		Message:    "Work completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	journal.Events = append(journal.Events, newMutationJournalEvent(payload))
	return journal
}

func assertMutationEvents(t *testing.T, dir string, journal mutationJournal, want int) {
	t.Helper()
	events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("read mutation events: warnings=%v err=%v", warnings, err)
	}
	matching := 0
	for _, event := range events {
		if event.MutationID == journal.MutationID {
			matching++
		}
	}
	if matching != want {
		t.Fatalf("mutation events = %d, want %d", matching, want)
	}
}

func assertMutationFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path) //nolint:gosec // Test path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), got, want)
	}
}

func testMutationJournal(t *testing.T) mutationJournal {
	t.Helper()
	statePayload := []byte(`{"schema":"tao.plan.state.v1","plan":{"id":"plan-a"}}`)
	slicesPayload := []byte(`{"schema":"tao.plan.slices.v1","plan_id":"plan-a"}`)
	eventPayload, err := json.Marshal(Event{
		Type:       EventTypeSliceStarted,
		Timestamp:  time.Date(2026, 7, 20, 16, 30, 0, 0, time.UTC),
		PlanID:     "plan-a",
		MutationID: "mutation-a",
		SliceID:    "001-a",
		Message:    "Work started on slice",
	})
	if err != nil {
		t.Fatal(err)
	}
	return mutationJournal{
		Schema:     mutationJournalSchema,
		MutationID: "mutation-a",
		PlanID:     "plan-a",
		CreatedAt:  time.Date(2026, 7, 20, 16, 30, 0, 0, time.UTC),
		State:      newMutationJournalPayload(statePayload),
		Slices:     newMutationJournalPayload(slicesPayload),
		Events:     []mutationJournalEvent{newMutationJournalEvent(eventPayload)},
	}
}
