package plan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testSliceVerificationSnapshot() SliceVerificationSnapshot {
	return SliceVerificationSnapshot{
		Version: 1, AttemptID: "attempt-1", ExecutionRoot: "/workspace", StartingBranch: "feature/test",
		StartingHead: strings.Repeat("a", 40), WorktreeFingerprint: strings.Repeat("b", 64), DeclarationDigest: strings.Repeat("c", 64),
		Runs:       []VerificationRun{{Source: "tao", CommandIndex: 1, Command: "go test ./...", CWD: "/workspace", Result: "failed", Details: "test failed", ExitCode: new(1), DurationMilliseconds: new(int64(0)), OutputDigest: strings.Repeat("d", 64), FailureKind: FinalVerificationFailureKindCode}},
		RecordedAt: time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC),
	}
}

func sliceVerificationRecord(t *testing.T) *PlanRecord {
	t.Helper()
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	snapshot := testSliceVerificationSnapshot()
	if err := record.StartSlice("001-a", SliceStartRequest{ExecutionRoot: snapshot.ExecutionRoot, Boundary: &SliceExecutionStart{Branch: snapshot.StartingBranch, Head: snapshot.StartingHead}, StartedAt: snapshot.RecordedAt.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRecordSliceVerificationPreservesLifecycleAndRejectsStaleWrites(t *testing.T) {
	record := sliceVerificationRecord(t)
	before := clonePlanDetail(record.Detail())
	snapshot := testSliceVerificationSnapshot()
	if err := record.RecordSliceVerification("001-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if err := record.RecordSliceVerification("001-a", snapshot); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	persisted := readSlicesFile(t, record.Dir()).Slices[0]
	if !reflect.DeepEqual(persisted.VerificationAttempt, &snapshot) || persisted.Completion != nil || persisted.CommitIntent != nil {
		t.Fatalf("attempt = %#v", persisted)
	}
	if !reflect.DeepEqual(record.Detail().State, before.State) || !reflect.DeepEqual(persisted.Timing, before.Slices.Slices[0].Timing) {
		t.Fatal("verification changed lifecycle or timing")
	}
	for _, event := range record.Detail().Events {
		if event.Type == EventTypeSliceCompleted {
			t.Fatal("attempt completed the slice")
		}
	}
	staleDetail := clonePlanDetail(record.Detail())
	stale := testRecord(record.Dir(), staleDetail)
	next := *cloneSliceVerificationSnapshot(&snapshot)
	next.AttemptID = "attempt-2"
	next.RecordedAt = next.RecordedAt.Add(time.Second)
	if err := record.RecordSliceVerification("001-a", next); err != nil {
		t.Fatal(err)
	}
	newer := *cloneSliceVerificationSnapshot(&next)
	newer.AttemptID = "attempt-3"
	newer.RecordedAt = newer.RecordedAt.Add(time.Second)
	if err := stale.RecordSliceVerification("001-a", newer); err == nil {
		t.Fatal("stale writer replaced a newer attempt")
	}
	if err := record.RecordSliceVerification("001-a", snapshot); err == nil {
		t.Fatal("older attempt replaced newer evidence")
	}
	changed := *cloneSliceVerificationSnapshot(&next)
	changed.Runs[0].Details = "changed"
	if err := record.RecordSliceVerification("001-a", changed); err == nil {
		t.Fatal("changed attempt reused identity")
	}
	*next.Runs[0].ExitCode = 99
	if *record.Detail().Slices.Slices[0].VerificationAttempt.Runs[0].ExitCode != 1 {
		t.Fatal("caller mutated persisted attempt")
	}
}

func TestSliceVerificationSnapshotValidation(t *testing.T) {
	tests := map[string]func(*SliceVerificationSnapshot){
		"version":       func(s *SliceVerificationSnapshot) { s.Version = 2 },
		"attempt":       func(s *SliceVerificationSnapshot) { s.AttemptID = "" },
		"attempt bound": func(s *SliceVerificationSnapshot) { s.AttemptID = strings.Repeat("a", 129) },
		"root":          func(s *SliceVerificationSnapshot) { s.ExecutionRoot = "relative" },
		"head":          func(s *SliceVerificationSnapshot) { s.StartingHead = "bad" },
		"fingerprint":   func(s *SliceVerificationSnapshot) { s.WorktreeFingerprint = "sha256:bad" },
		"declaration":   func(s *SliceVerificationSnapshot) { s.DeclarationDigest = strings.Repeat("G", 64) },
		"time":          func(s *SliceVerificationSnapshot) { s.RecordedAt = time.Time{} },
		"source":        func(s *SliceVerificationSnapshot) { s.Runs[0].Source = "agent" },
		"index":         func(s *SliceVerificationSnapshot) { s.Runs[0].CommandIndex = 0 },
		"command":       func(s *SliceVerificationSnapshot) { s.Runs[0].Command = "" },
		"command bound": func(s *SliceVerificationSnapshot) { s.Runs[0].Command = strings.Repeat("x", 4097) },
		"details bound": func(s *SliceVerificationSnapshot) { s.Runs[0].Details = strings.Repeat("x", 16385) },
		"output digest": func(s *SliceVerificationSnapshot) { s.Runs[0].OutputDigest = strings.Repeat("A", 64) },
		"duration":      func(s *SliceVerificationSnapshot) { s.Runs[0].DurationMilliseconds = new(int64(-1)) },
		"failure kind":  func(s *SliceVerificationSnapshot) { s.Runs[0].FailureKind = "unknown" },
		"false success": func(s *SliceVerificationSnapshot) { s.Runs[0].Result = "passed" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := testSliceVerificationSnapshot()
			mutate(&snapshot)
			if err := snapshot.Validate(); err == nil {
				t.Fatal("accepted malformed snapshot")
			}
		})
	}
	snapshot := testSliceVerificationSnapshot()
	snapshot.Runs[0].ExitCode = nil
	snapshot.Runs[0].DurationMilliseconds = nil
	snapshot.Runs[0].FailureKind = FinalVerificationFailureKindCancelled
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("unknown exit/duration: %v", err)
	}
}

func TestRecordSliceVerificationAdmission(t *testing.T) {
	tests := map[string]func(*PlanDetail){
		"wrong selected slice": func(d *PlanDetail) { d.State.Plan.CurrentSlice = new("002-b") },
		"pending":              func(d *PlanDetail) { d.Slices.Slices[0].Status = StatusPending },
		"blocked plan":         func(d *PlanDetail) { d.State.Status = StatusBlocked },
		"intent":               func(d *PlanDetail) { d.Slices.Slices[0].CommitIntent = &SliceCommitIntent{Hash: "legacy"} },
		"completion": func(d *PlanDetail) {
			d.Slices.Slices[0].Completion = &SliceCompletionOutcome{Outcome: SliceCompletionCommitted}
		},
		"approval":         func(d *PlanDetail) { d.Slices.Slices[0].Approval = &Approval{Required: true} },
		"dependency":       func(d *PlanDetail) { d.Slices.Slices[0].DependsOn = []string{"missing"} },
		"changed boundary": func(d *PlanDetail) { d.Slices.Slices[0].ExecutionStart.Head = strings.Repeat("f", 40) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := sliceVerificationRecord(t)
			mutate(record.Detail())
			writeStartSliceArtifacts(t, record.Dir(), record.Detail())
			if err := record.RecordSliceVerification("001-a", testSliceVerificationSnapshot()); err == nil {
				t.Fatal("accepted unsafe attempt")
			}
		})
	}
}

func TestRecordSliceVerificationPersistenceFailure(t *testing.T) {
	record := sliceVerificationRecord(t)
	before := clonePlanDetail(record.Detail())
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "slices"}
	record.store = journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	snapshot := testSliceVerificationSnapshot()
	if err := record.RecordSliceVerification("001-a", snapshot); err == nil {
		t.Fatal("expected write failure")
	}
	if !reflect.DeepEqual(record.Detail(), before) {
		t.Fatal("failed write published postimage")
	}
	ioStore.failOperation = ""
	if err := record.RecordSliceVerification("001-a", snapshot); err != nil {
		t.Fatalf("journal recovery: %v", err)
	}
	if !reflect.DeepEqual(readSlicesFile(t, record.Dir()).Slices[0].VerificationAttempt, &snapshot) {
		t.Fatal("retry lost evidence")
	}
}

func TestSliceVerificationIntentSnapshotIsImmutable(t *testing.T) {
	record := sliceVerificationRecord(t)
	snapshot := testSliceVerificationSnapshot()
	snapshot.Runs[0].Result = "passed"
	snapshot.Runs[0].ExitCode = new(0)
	snapshot.Runs[0].FailureKind = ""
	if err := record.RecordSliceVerification("001-a", snapshot); err != nil {
		t.Fatal(err)
	}
	intent := SliceCommitIntent{Hash: "hash", Policy: "slice", Message: "exact historical message", CreatedAt: snapshot.RecordedAt, Verification: &snapshot}
	if err := record.RecordSliceCommitIntent("001-a", intent); err != nil {
		t.Fatal(err)
	}
	exact := *cloneSliceCommitIntent(&intent)
	if err := record.RecordSliceCommitIntent("001-a", exact); err != nil {
		t.Fatalf("deep equal retry: %v", err)
	}
	snapshot.Runs[0].Details = "mutated"
	*snapshot.Runs[0].ExitCode = 1
	if err := record.RecordSliceCommitIntent("001-a", intent); err == nil {
		t.Fatal("accepted changed frozen snapshot")
	}
	if !reflect.DeepEqual(record.Detail().Slices.Slices[0].CommitIntent, &exact) {
		t.Fatal("intent aliases caller")
	}
	if err := record.RecordSliceVerification("001-a", *exact.Verification); err == nil {
		t.Fatal("post-intent attempt accepted")
	}
}

func TestSliceVerificationWritersRejectMalformedEvidence(t *testing.T) {
	record := sliceVerificationRecord(t)
	snapshot := testSliceVerificationSnapshot()
	snapshot.Runs[0].OutputDigest = "malformed"
	before := clonePlanDetail(record.Detail())
	if err := record.RecordSliceVerification("001-a", snapshot); err == nil {
		t.Fatal("attempt writer accepted malformed digest")
	}
	if err := record.RecordSliceCommitIntent("001-a", SliceCommitIntent{Hash: "hash", Verification: &snapshot}); err == nil {
		t.Fatal("intent writer accepted malformed digest")
	}
	if !reflect.DeepEqual(record.Detail(), before) {
		t.Fatal("invalid evidence mutated detail")
	}
	if got := readSlicesFile(t, record.Dir()).Slices[0]; got.VerificationAttempt != nil || got.CommitIntent != nil {
		t.Fatal("invalid evidence was persisted")
	}
}

func TestVerificationClaimMismatchFieldsAreBounded(t *testing.T) {
	event := Event{Type: EventTypeVerificationClaimMismatch, ClaimedResult: "passed", VerificationAttemptID: "attempt-1"}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != event.Type || got.ClaimedResult != "passed" || got.VerificationAttemptID != "attempt-1" {
		t.Fatalf("event = %#v", got)
	}
	event.ClaimedResult = strings.Repeat("x", 129)
	if _, err := json.Marshal(event); err == nil {
		t.Fatal("unbounded claimed result")
	}
	event.ClaimedResult = "passed"
	event.VerificationAttemptID = strings.Repeat("x", 129)
	if _, err := json.Marshal(event); err == nil {
		t.Fatal("unbounded attempt ID")
	}
}
