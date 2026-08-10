package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBatchStoreAppendsAgentEventsOutsideStateAndTransitions(t *testing.T) {
	store := newTestBatchStore(t)
	at := time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)
	for attempt := 1; attempt <= 2; attempt++ {
		event := BatchAgentEvent{
			Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeMetrics, BatchID: "batch-a", Timestamp: at,
			Operation: BatchAgentOperationAggregateReview, Attempt: attempt, Agent: "pi",
			Outcome: BatchAgentOutcomeCompleted, Metrics: &BatchAgentMetrics{OutputTokens: int64(attempt)},
		}
		if err := store.AppendAgentEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(store.agentEventsPath("batch-a"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("event lines = %d, want 2: %q", len(lines), content)
	}
	for i, line := range lines {
		var event BatchAgentEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != BatchAgentEventTypeMetrics || event.Attempt != i+1 {
			t.Fatalf("event %d = %#v", i, event)
		}
	}
	info, err := os.Stat(store.agentEventsPath("batch-a"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("event file mode = %v, err=%v", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(store.snapshotPath("batch-a")); !os.IsNotExist(err) {
		t.Fatalf("telemetry wrote snapshot: %v", err)
	}
	if _, err := os.Stat(store.logPath("batch-a")); !os.IsNotExist(err) {
		t.Fatalf("telemetry wrote transition log: %v", err)
	}
}

func TestBatchStoreSynchronizesConcurrentAgentEventAppends(t *testing.T) {
	store := newTestBatchStore(t)
	const count = 20
	var wg sync.WaitGroup
	for attempt := 1; attempt <= count; attempt++ {
		wg.Add(1)
		go func(attempt int) {
			defer wg.Done()
			err := store.AppendAgentEvent(BatchAgentEvent{
				Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeMetrics, BatchID: "batch-concurrent", Timestamp: time.Now(),
				Operation: BatchAgentOperationAggregateRework, Attempt: attempt, Agent: "codex",
				Outcome: BatchAgentOutcomeCompleted, Metrics: &BatchAgentMetrics{},
			})
			if err != nil {
				t.Errorf("append %d: %v", attempt, err)
			}
		}(attempt)
	}
	wg.Wait()
	content, err := os.ReadFile(store.agentEventsPath("batch-concurrent"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(content), "\n"); lines != count {
		t.Fatalf("complete event lines = %d, want %d", lines, count)
	}
}

func TestBatchStoreRoundTripPreservesDurableState(t *testing.T) {
	store := newTestBatchStore(t)
	want := testBatchState()
	want.LogSequence = 1
	transition := BatchTransition{
		Schema:   BatchTransitionSchema,
		Sequence: 1,
		At:       "2026-07-15T20:00:00Z",
		To:       BatchStatusPlanned,
		State:    want,
	}
	if err := store.AppendTransition(transition); err != nil {
		t.Fatalf("AppendTransition() failed: %v", err)
	}
	if err := store.SaveSnapshot(want); err != nil {
		t.Fatalf("SaveSnapshot() failed: %v", err)
	}

	got, err := store.Load(want.ID)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	if got.DefaultStartSHA != "00000000000000000000000000000000000000ab" || got.Candidates[0].SourceTip != "ffffffffffffffffffffffffffffffffffffff01" {
		t.Fatalf("source identities changed: default=%q source=%q", got.DefaultStartSHA, got.Candidates[0].SourceTip)
	}
}

func TestBatchStoreReplaysTransitionAfterStaleSnapshot(t *testing.T) {
	store := newTestBatchStore(t)
	planned, err := store.Transition(testBatchState(), "2026-07-15T20:00:00Z")
	if err != nil {
		t.Fatalf("planned Transition() failed: %v", err)
	}
	if err := store.SaveSnapshot(planned); err != nil {
		t.Fatal(err)
	}
	integrating := planned
	integrating.Status = BatchStatusIntegrating
	integrating.IntegrationHead = "1111111111111111111111111111111111111111"
	integrating, err = store.Transition(integrating, "2026-07-15T20:01:00Z")
	if err != nil {
		t.Fatalf("integrating Transition() failed: %v", err)
	}

	got, err := store.Load(planned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, integrating) {
		t.Fatalf("replayed state = %#v, want %#v", got, integrating)
	}
}

func TestBatchStoreToleratesPartialAndCorruptFilesWithoutInventingProgress(t *testing.T) {
	t.Run("partial snapshot recovers from log", func(t *testing.T) {
		store := newTestBatchStore(t)
		state, err := store.Transition(testBatchState(), "now")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.snapshotPath(state.ID), []byte(`{"schema":`), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := store.Load(state.ID)
		if err != nil || !reflect.DeepEqual(got, state) {
			t.Fatalf("Load() = %#v, %v; want %#v", got, err, state)
		}
	})

	t.Run("partial final log line is ignored", func(t *testing.T) {
		store := newTestBatchStore(t)
		state, err := store.Transition(testBatchState(), "now")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(store.logPath(state.ID), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"schema":"tao.merge-batch-transition.v1","sequence":2`); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := store.Load(state.ID)
		if err != nil || !reflect.DeepEqual(got, state) {
			t.Fatalf("Load() = %#v, %v; want %#v", got, err, state)
		}
	})

	t.Run("corrupt complete record stops replay chain", func(t *testing.T) {
		store := newTestBatchStore(t)
		planned, err := store.Transition(testBatchState(), "now")
		if err != nil {
			t.Fatal(err)
		}
		integrating := planned
		integrating.Status = BatchStatusIntegrating
		integrating.LogSequence = 3
		later := BatchTransition{Schema: BatchTransitionSchema, Sequence: 3, From: BatchStatusPlanned, To: BatchStatusIntegrating, State: integrating}
		encoded, err := json.Marshal(later)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(store.logPath(planned.ID), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(append([]byte("not-json\n"), append(encoded, '\n')...))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("write corrupt log: %v / %v", writeErr, closeErr)
		}
		got, err := store.Load(planned.ID)
		if err != nil || !reflect.DeepEqual(got, planned) {
			t.Fatalf("Load() = %#v, %v; want last contiguous state %#v", got, err, planned)
		}
	})
}

func TestBatchStoreIgnoresUnknownForwardCompatibleFields(t *testing.T) {
	store := newTestBatchStore(t)
	state := testBatchState()
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	document["future_batch_field"] = map[string]any{"enabled": true}
	candidates := document["candidates"].([]any)
	candidates[0].(map[string]any)["future_candidate_field"] = "kept-by-newer-tao"
	content, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.snapshotPath(state.ID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.snapshotPath(state.ID), content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("Load() with unknown fields = %#v, want %#v", got, state)
	}
}

func TestBatchStoreInitializePersistsStateBeforeActiveIdentity(t *testing.T) {
	store := newTestBatchStore(t)
	state, err := store.Initialize(testBatchState(), "2026-07-15T20:00:00Z")
	if err != nil {
		t.Fatalf("Initialize() failed: %v", err)
	}
	if state.LogSequence != 1 {
		t.Fatalf("Initialize() log sequence = %d, want 1", state.LogSequence)
	}
	active, err := store.ActiveID()
	if err != nil || active != state.ID {
		t.Fatalf("ActiveID() = %q, %v; want %q", active, err, state.ID)
	}
	durable, err := store.Load(active)
	if err != nil || !reflect.DeepEqual(durable, state) {
		t.Fatalf("Load(active) = %#v, %v; want %#v", durable, err, state)
	}
}

func TestBatchStoreInitializeRecoversStatePersistedBeforeIdentity(t *testing.T) {
	store := newTestBatchStore(t)
	state, err := store.Transition(testBatchState(), "2026-07-15T20:00:00Z")
	if err != nil {
		t.Fatalf("persist interrupted initialization state: %v", err)
	}
	if active, activeErr := store.ActiveID(); activeErr != nil || active != "" {
		t.Fatalf("ActiveID() before recovery = %q, %v", active, activeErr)
	}

	recovered, err := store.Initialize(testBatchState(), "2026-07-15T20:00:00Z")
	if err != nil {
		t.Fatalf("Initialize() recovery failed: %v", err)
	}
	if !reflect.DeepEqual(recovered, state) {
		t.Fatalf("Initialize() recovery = %#v, want %#v", recovered, state)
	}
	if active, activeErr := store.ActiveID(); activeErr != nil || active != state.ID {
		t.Fatalf("ActiveID() after recovery = %q, %v; want %q", active, activeErr, state.ID)
	}
}

func TestBatchStoreInitializeDoesNotPublishInvalidState(t *testing.T) {
	store := newTestBatchStore(t)
	state := testBatchState()
	state.Status = BatchStatus("invalid")
	if _, err := store.Initialize(state, "now"); err == nil {
		t.Fatal("Initialize() succeeded with invalid state")
	}
	if active, err := store.ActiveID(); err != nil || active != "" {
		t.Fatalf("ActiveID() = %q, %v; want no published identity", active, err)
	}
}

func TestBatchStoreInitializeSelectsExactlyOneActiveBatch(t *testing.T) {
	root := t.TempDir()
	batchesDir := filepath.Join(root, "merge-batches")
	activePath := filepath.Join(batchesDir, "active.json")
	first := NewBatchStore(batchesDir, activePath)
	second := NewBatchStore(batchesDir, activePath)
	firstState := testBatchState()
	firstState.ID = "batch-a"
	secondState := testBatchState()
	secondState.ID = "batch-b"

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for store, state := range map[*BatchStore]BatchState{first: firstState, second: secondState} {
		go func(store *BatchStore, state BatchState) {
			ready.Done()
			<-start
			_, err := store.Initialize(state, "now")
			errs <- err
		}(store, state)
	}
	ready.Wait()
	close(start)
	firstErr, secondErr := <-errs, <-errs
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("Initialize errors = %v and %v; want exactly one success", firstErr, secondErr)
	}
	active, err := first.ActiveID()
	if err != nil || (active != "batch-a" && active != "batch-b") {
		t.Fatalf("ActiveID() = %q, %v", active, err)
	}
	if durable, loadErr := first.Load(active); loadErr != nil || durable.ID != active {
		t.Fatalf("Load(active) = %#v, %v; want initialized state", durable, loadErr)
	}
	loser := "batch-a"
	if active == loser {
		loser = "batch-b"
	}
	if durable, loadErr := first.Load(loser); loadErr != nil || durable.ID != "" {
		t.Fatalf("Load(loser) = %#v, %v; losing initializer must not persist", durable, loadErr)
	}
}

func TestBatchStoreSelectsExactlyOneActiveBatch(t *testing.T) {
	root := t.TempDir()
	batchesDir := filepath.Join(root, "merge-batches")
	activePath := filepath.Join(batchesDir, "active.json")
	first := NewBatchStore(batchesDir, activePath)
	second := NewBatchStore(batchesDir, activePath)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for store, id := range map[*BatchStore]string{first: "batch-a", second: "batch-b"} {
		go func(store *BatchStore, id string) {
			ready.Done()
			<-start
			errs <- store.SetActive(id)
		}(store, id)
	}
	ready.Wait()
	close(start)
	firstErr, secondErr := <-errs, <-errs
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("SetActive errors = %v and %v; want exactly one success", firstErr, secondErr)
	}
	active, err := first.ActiveID()
	if err != nil || (active != "batch-a" && active != "batch-b") {
		t.Fatalf("ActiveID() = %q, %v", active, err)
	}
	other := "batch-a"
	if active == other {
		other = "batch-b"
	}
	if err := first.ClearActive(other); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("ClearActive(non-active) error = %v", err)
	}
	if err := second.ClearActive(active); err != nil {
		t.Fatalf("ClearActive(active) failed: %v", err)
	}
	if got, err := first.ActiveID(); err != nil || got != "" {
		t.Fatalf("ActiveID() after clear = %q, %v", got, err)
	}
}

func TestBatchStoreRejectsNonContiguousTransition(t *testing.T) {
	store := newTestBatchStore(t)
	state := testBatchState()
	state.Status = BatchStatusIntegrating
	state.LogSequence = 2
	err := store.AppendTransition(BatchTransition{Schema: BatchTransitionSchema, Sequence: 2, From: BatchStatusPlanned, To: BatchStatusIntegrating, State: state})
	if err == nil || !strings.Contains(err.Error(), "does not continue") {
		t.Fatalf("AppendTransition() error = %v", err)
	}
}

func newTestBatchStore(t *testing.T) *BatchStore {
	t.Helper()
	root := t.TempDir()
	return NewBatchStore(filepath.Join(root, "merge-batches"), filepath.Join(root, "merge-batches", "active.json"))
}

func testBatchState() BatchState {
	return BatchState{
		Schema:          BatchStateSchema,
		ID:              "20260715-merge-all",
		Status:          BatchStatusPlanned,
		RepoRoot:        "/repo",
		DefaultBranch:   "main",
		DefaultStartSHA: "00000000000000000000000000000000000000ab",
		Candidates: []BatchCandidate{{
			PlanID: "plan-a", PlanDir: "/data/plans/plan-a", RepoRoot: "/repo", Branch: "tao/plan-a",
			ReviewBase: "0000000000000000000000000000000000000001", ReviewHead: "ffffffffffffffffffffffffffffffffffffff01",
			SourceTip: "ffffffffffffffffffffffffffffffffffffff01", DefaultBranch: "main", DefaultStartSHA: "00000000000000000000000000000000000000ab",
		}},
		ChosenOrder: []string{"plan-a"},
		Integrations: []BatchIntegration{{
			PlanID: "plan-a", SourceHead: "ffffffffffffffffffffffffffffffffffffff01", IntegrationBaseSHA: "00000000000000000000000000000000000000ab",
			IntegrationSHA: "1111111111111111111111111111111111111111", Attempts: 2, Fingerprint: "conflict-v1",
		}},
		Attempts:        BatchAttempts{ConflictResolution: 2, AggregateRework: 1, ConflictFingerprint: "conflict-v1", ReviewFingerprint: "review-v1"},
		Verification:    &BatchVerification{Command: "make verify", HeadSHA: "1111111111111111111111111111111111111111", Passed: true, StartedAt: "start", CompletedAt: "done"},
		Review:          &BatchReview{Status: "completed", Verdict: "approve", BaseSHA: "00000000000000000000000000000000000000ab", HeadSHA: "1111111111111111111111111111111111111111", Fingerprint: "review-v1", Attempts: 1},
		Settlement:      []BatchSettlement{{PlanID: "plan-a", MergeEvidenceRecorded: true}},
		IntegrationHead: "1111111111111111111111111111111111111111",
		CreatedAt:       "2026-07-15T20:00:00Z",
		UpdatedAt:       "2026-07-15T20:01:00Z",
	}
}
