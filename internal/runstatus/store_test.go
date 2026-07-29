package runstatus

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreRoundTripUpdateAndRemove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "run-status")
	now := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	store := NewStore(dir, func() time.Time { return now })
	record := testRecord(now)

	if err := store.Create(record); err != nil {
		t.Fatalf("Create() failed: %v", err)
	}
	if err := store.Create(record); err == nil {
		t.Fatal("second Create() succeeded")
	}
	got, err := store.Read(record.PlanID)
	if err != nil {
		t.Fatalf("Read() failed: %v", err)
	}
	if got.Schema != Schema || got.RepoID != record.RepoID || got.PlanID != record.PlanID || got.Phase != record.Phase || got.Slice == nil || got.Slice.ID != record.Slice.ID || !got.HeartbeatAt.Equal(now) {
		t.Fatalf("Read() = %#v, want %#v", got, record)
	}

	now = now.Add(PublicationInterval)
	got.Phase = Phase("verify")
	got.HeartbeatAt = now
	if err := store.Update(got); err != nil {
		t.Fatalf("Update() failed: %v", err)
	}
	updated, err := store.Read(record.PlanID)
	if err != nil || updated.Phase != Phase("verify") || !updated.HeartbeatAt.Equal(now) {
		t.Fatalf("updated Read() = %#v, %v", updated, err)
	}

	if err := store.Remove(record.PlanID); err != nil {
		t.Fatalf("Remove() failed: %v", err)
	}
	if _, err := store.Read(record.PlanID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read() after Remove error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreMissingDirectoryErrorsRemainExplicit(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "missing"), nil)
	if _, err := store.Read("plan-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read() error = %v, want os.ErrNotExist", err)
	}
	if err := store.Update(testRecord(time.Now())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Update() error = %v, want os.ErrNotExist", err)
	}
	if err := store.Remove("plan-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Remove() error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreRejectsMalformedRecords(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, nil)
	if err := os.WriteFile(filepath.Join(dir, "plan-a.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("plan-a"); err == nil || !strings.Contains(err.Error(), "decode runtime status") {
		t.Fatalf("malformed Read() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan-a.json"), []byte(`{"schema":"other","plan_id":"plan-a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("plan-a"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid Read() error = %v, want ErrInvalidRecord", err)
	}

	invalid := testRecord(time.Now())
	invalid.Phase = ""
	if err := store.Write(invalid); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Write(invalid) error = %v, want ErrInvalidRecord", err)
	}
}

func TestStoreRejectsUnsafePlanIdentity(t *testing.T) {
	store := NewStore(t.TempDir(), nil)
	for _, id := range []string{"", ".", "..", "../escape", "nested/plan", `nested\\plan`, " plan-a", "plan-a ", "plan a"} {
		if _, err := store.Path(id); !errors.Is(err, ErrInvalidPlanID) {
			t.Errorf("Path(%q) error = %v, want ErrInvalidPlanID", id, err)
		}
	}
	path, err := store.Path("20260729-044616-plan-monitor")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != store.dir {
		t.Fatalf("safe path escaped store: %q", path)
	}
}

func TestStoreHeartbeatUsesInjectedClock(t *testing.T) {
	initial := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	now := initial
	store := NewStore(t.TempDir(), func() time.Time { return now })
	if err := store.Create(testRecord(initial)); err != nil {
		t.Fatal(err)
	}
	now = initial.Add(PublicationInterval)
	got, err := store.Heartbeat("plan-a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HeartbeatAt.Equal(now) {
		t.Fatalf("HeartbeatAt = %v, want %v", got.HeartbeatAt, now)
	}
	persisted, err := store.Read("plan-a")
	if err != nil || !persisted.HeartbeatAt.Equal(now) {
		t.Fatalf("persisted heartbeat = %v, %v", persisted.HeartbeatAt, err)
	}
}

func TestDeriveFreshnessBoundaries(t *testing.T) {
	heartbeat := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	record := testRecord(heartbeat)
	for _, tc := range []struct {
		name string
		now  time.Time
		want Freshness
	}{
		{name: "future heartbeat", now: heartbeat.Add(-time.Second), want: FreshnessFresh},
		{name: "just before threshold", now: heartbeat.Add(StaleThreshold - time.Nanosecond), want: FreshnessFresh},
		{name: "at threshold", now: heartbeat.Add(StaleThreshold), want: FreshnessStale},
		{name: "after threshold", now: heartbeat.Add(StaleThreshold + time.Second), want: FreshnessStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveFreshness(record, tc.now); got != tc.want {
				t.Fatalf("DeriveFreshness() = %q, want %q", got, tc.want)
			}
		})
	}
	if record.HeartbeatAt != heartbeat {
		t.Fatal("DeriveFreshness mutated record")
	}
}

func TestPublisherOwnershipSurvivesContentionAndCleanupHandoff(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	store := NewStore(t.TempDir(), func() time.Time { return now })
	activeRecord := testRecord(now)
	activeRecord.InvocationID = "active-invocation"
	active := NewPublisher(store, activeRecord)
	if err := active.Publish(Phase("waiting_for_ownership"), nil); err != nil {
		t.Fatal(err)
	}

	successorRecord := testRecord(now.Add(time.Second))
	successorRecord.InvocationID = "successor-invocation"
	successor := NewPublisher(store, successorRecord)
	if err := successor.Publish(Phase("waiting_for_ownership"), nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("contended Publish() error = %v, want os.ErrExist", err)
	}
	if err := successor.Remove(); err != nil {
		t.Fatalf("contender Remove() failed: %v", err)
	}
	got, err := store.Read("plan-a")
	if err != nil || got.InvocationID != activeRecord.InvocationID {
		t.Fatalf("record after contended cleanup = %#v, %v", got, err)
	}

	claimed := make(chan struct{})
	allowOldCleanup := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := successor.Publish(Phase("preparing_execution"), nil); err != nil {
			t.Errorf("successor Publish() failed: %v", err)
		}
		close(claimed)
	}()
	go func() {
		defer wg.Done()
		<-claimed
		close(allowOldCleanup)
		if err := active.Remove(); err != nil {
			t.Errorf("old invocation Remove() failed: %v", err)
		}
	}()
	<-allowOldCleanup
	wg.Wait()

	got, err = store.Read("plan-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.InvocationID != successorRecord.InvocationID || got.Phase != Phase("preparing_execution") {
		t.Fatalf("record after ownership handoff = %#v", got)
	}
	if err := active.Heartbeat(); err != nil {
		t.Fatalf("old invocation Heartbeat() failed: %v", err)
	}
	got, err = store.Read("plan-a")
	if err != nil || got.InvocationID != successorRecord.InvocationID {
		t.Fatalf("record after old heartbeat = %#v, %v", got, err)
	}
}

func TestPublisherReportsWithInjectedClockAndCleansUp(t *testing.T) {
	started := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	now := started
	store := NewStore(t.TempDir(), func() time.Time { return now })
	publisher := NewPublisher(store, Record{RepoID: "repo-a", PlanID: "plan-a"})
	detail := &SliceDetail{ID: "001-work", Title: "Work"}
	if err := publisher.Publish(Phase("implement"), detail); err != nil {
		t.Fatal(err)
	}
	detail.ID = "mutated"

	now = now.Add(PublicationInterval)
	if err := publisher.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("plan-a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.InvocationStartedAt.Equal(started) || !got.HeartbeatAt.Equal(now) || got.Slice.ID != "001-work" {
		t.Fatalf("published record = %#v", got)
	}
	if err := publisher.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("plan-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read() after publisher cleanup = %v", err)
	}
}

func testRecord(now time.Time) Record {
	return Record{
		Schema:              Schema,
		RepoID:              "repo-a",
		RepoName:            "repo",
		PlanID:              "plan-a",
		PlanTitle:           "Plan A",
		InvocationID:        "invocation-a",
		Phase:               Phase("implement"),
		Slice:               &SliceDetail{ID: "001-work", Title: "Work"},
		InvocationStartedAt: now,
		HeartbeatAt:         now,
	}
}
