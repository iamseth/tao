package runqueue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestFileStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 6, 28, 2, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	policy := runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 4}
	baseline := 3
	snapshot := QueueSnapshot{Entries: []QueueEntry{{
		PlanID:                     "plan-a",
		Status:                     QueueStatusRunning,
		QueuedAt:                   queuedAt,
		StartedAt:                  &startedAt,
		AutoReworkPolicy:           &policy,
		ReworkBaselineRound:        &baseline,
		ReworkAttempts:             2,
		PreviousFindingFingerprint: "sha256:findings",
		RecoveryPending:            true,
	}}}

	if err := store.SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("unexpected loaded snapshot\n got: %#v\nwant: %#v", got, snapshot)
	}
}

func TestFileStoreLoadRecoversTransitionsAfterSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 6, 28, 2, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	finishedAt := startedAt.Add(2 * time.Minute)

	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: queuedAt}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTransition(QueueTransition{Entry: &QueueEntry{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, StartedAt: &startedAt}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTransition(QueueTransition{Entry: &QueueEntry{PlanID: "plan-a", Status: QueueStatusSucceeded, QueuedAt: queuedAt, StartedAt: &startedAt, FinishedAt: &finishedAt}}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := QueueSnapshot{Entries: []QueueEntry{{PlanID: "plan-a", Status: QueueStatusSucceeded, QueuedAt: queuedAt, StartedAt: &startedAt, FinishedAt: &finishedAt}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected recovered snapshot\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFileStoreLoadToleratesMissingAndCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("expected empty snapshot for missing files, got %+v", got)
	}

	queuedAt := time.Date(2026, 6, 28, 2, 0, 0, 0, time.UTC)
	validLine, err := json.Marshal(QueueTransition{Entry: &QueueEntry{PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: queuedAt}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, queueSnapshotFilename), []byte(`{"entries":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	log := append([]byte("{not json}\n"), validLine...)
	log = append(log, []byte("\n{\"plan_id\":\"partial")...)
	if err := os.WriteFile(filepath.Join(dir, queueEventLogFilename), log, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := QueueSnapshot{Entries: []QueueEntry{{PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: queuedAt}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected recovered snapshot\n got: %#v\nwant: %#v", got, want)
	}
}
