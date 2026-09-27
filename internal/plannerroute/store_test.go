package plannerroute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/filelock"
)

func unlinkedRecord() Record {
	r := testRecord()
	r.Entries = r.Entries[:1]
	return r
}

func TestStoreCreateLoad(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "planner-routes")
	s := NewStore(dir)
	r := unlinkedRecord()
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, r.ID)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, r)
	}
	if err := s.Create(ctx, r); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate Create = %v", err)
	}
	for path, perm := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, r.ID+".json"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != perm {
			t.Fatalf("permissions for %s: %v, %v", path, info, err)
		}
	}
	missing := "20260926-235959-" + strings.Repeat("f", 32)
	if _, err := s.Load(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Load = %v", err)
	}
}

func TestStoreCapacityBoundary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := NewStore(dir)
	first := unlinkedRecord()
	// Seed directly so the boundary test does not need thousands of lock/scan cycles.
	for i := range MaxRecords - 1 {
		r := unlinkedRecord()
		r.ID = fmt.Sprintf("20260926-235959-%032x", i)
		if i == 0 {
			first = r
		}
		data, err := encodeRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.path(r.ID), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Like the persistent lock file, non-JSON files do not consume capacity.
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		record Record
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i := range 2 {
		go func() {
			<-start
			r := unlinkedRecord()
			r.ID = fmt.Sprintf("20260926-235959-%032x", MaxRecords+i)
			results <- result{r, NewStore(dir).Create(ctx, r)}
		}()
	}
	close(start)
	var admitted, rejected Record
	for range 2 {
		got := <-results
		if got.err == nil {
			if admitted.ID != "" {
				t.Fatal("concurrent Create admitted a record beyond MaxRecords")
			}
			admitted = got.record
		} else {
			for _, want := range []string{dir, fmt.Sprint(MaxRecords), "capacity", "archive"} {
				if !strings.Contains(got.err.Error(), want) {
					t.Fatalf("capacity error = %v; want %q", got.err, want)
				}
			}
			rejected = got.record
		}
	}
	if admitted.ID == "" || rejected.ID == "" {
		t.Fatal("expected exactly one admission into the last available slot")
	}
	if err := s.Create(ctx, rejected); err == nil {
		t.Fatal("retry at capacity succeeded")
	}
	if _, err := s.Load(ctx, rejected.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected Create persisted record: %v", err)
	}
	if err := s.Create(ctx, admitted); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate Create at capacity = %v", err)
	}
	records, warnings, err := s.List(ctx)
	if err != nil || len(warnings) != 0 || len(records) != MaxRecords {
		t.Fatalf("List at capacity: %d records, %v, %v", len(records), warnings, err)
	}
	entry := Entry{Kind: EntryAttempt, Attempt: &Attempt{Stage: "planning", Outcome: "succeeded"}}
	if _, err := s.Append(ctx, admitted.ID, entry); err != nil {
		t.Fatalf("Append at capacity: %v", err)
	}
	for i, r := range []Record{first, admitted} {
		planID := fmt.Sprintf("plan-%d", i)
		linked, err := s.Link(ctx, r.ID, planID, "/plans/"+planID)
		if err != nil || linked.LinkedPlanID() != planID {
			t.Fatalf("Link admitted record at capacity: %+v, %v", linked, err)
		}
	}
}

func TestStoreMissingAndInvalid(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "missing")
	s := NewStore(dir)
	records, warnings, err := s.List(ctx)
	if err != nil || len(records) != 0 || len(warnings) != 0 {
		t.Fatalf("missing List = %v, %v, %v", records, warnings, err)
	}
	for _, id := range []string{"", "../escape", "/absolute", "invalid"} {
		if _, err := s.Load(ctx, id); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid Load(%q) = %v", id, err)
		}
		if _, err := s.Append(ctx, id, Entry{}); err == nil {
			t.Fatalf("invalid Append(%q) succeeded", id)
		}
		if _, err := s.Link(ctx, id, "plan", "/plans/plan"); err == nil {
			t.Fatalf("invalid Link(%q) succeeded", id)
		}
	}
	r := unlinkedRecord()
	r.Schema = "wrong"
	if err := s.Create(ctx, r); err == nil {
		t.Fatal("invalid Create succeeded")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read/invalid operations created directory: %v", err)
	}
}

func TestStoreListWarningsAndOrder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := NewStore(dir)
	for _, suffix := range []string{"e", "a"} {
		r := unlinkedRecord()
		r.ID = "20260926-235959-" + strings.Repeat(suffix, 32)
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	bad := unlinkedRecord()
	bad.Schema = "future-schema"
	encoded, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	for suffix, data := range map[string][]byte{
		"b": []byte("{"), "c": []byte(strings.Repeat("x", MaxRecordBytes+1)), "d": encoded,
	} {
		path := filepath.Join(dir, "20260926-235959-"+strings.Repeat(suffix, 32)+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unreadable := "20260926-235959-" + strings.Repeat("f", 32) + ".json"
	if err := os.Mkdir(filepath.Join(dir, unreadable), 0o700); err != nil {
		t.Fatal(err)
	}
	records, warnings, err := s.List(ctx)
	if err != nil || len(records) != 2 || records[0].ID >= records[1].ID || len(warnings) != 4 {
		t.Fatalf("List = %v, %v, %v", records, warnings, err)
	}
	for _, suffix := range []string{"b", "c", "d", "f"} {
		if !strings.Contains(strings.Join(warnings, "\n"), strings.Repeat(suffix, 32)+".json") {
			t.Errorf("missing per-file warning for %s: %v", suffix, warnings)
		}
	}
	// An incomplete scan must not authorize a possibly duplicate plan link.
	if _, err := s.Link(ctx, records[0].ID, "plan-1", "/plans/plan-1"); err == nil {
		t.Fatal("Link accepted incomplete ledger scan")
	}
}

func TestStoreLink(t *testing.T) {
	ctx := context.Background()
	s := NewStore(t.TempDir())
	r := unlinkedRecord()
	other := unlinkedRecord()
	other.ID = "20260926-235959-" + strings.Repeat("f", 32)
	for _, record := range []Record{r, other} {
		if err := s.Create(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	linked, err := s.Link(ctx, r.ID, "plan-1", "/plans/plan-1")
	if err != nil || linked.LinkedPlanID() != "plan-1" {
		t.Fatalf("Link = %+v, %v", linked, err)
	}
	again, err := s.Link(ctx, r.ID, "plan-1", "/different/path")
	if err != nil || !reflect.DeepEqual(again, linked) {
		t.Fatalf("idempotent Link = %+v, %v", again, err)
	}
	if _, err := s.Link(ctx, r.ID, "plan-2", "/plans/plan-2"); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("relink = %v", err)
	}
	if _, err := s.Link(ctx, other.ID, "plan-1", "/plans/plan-1"); !errors.Is(err, ErrPlanAlreadyRouted) {
		t.Fatalf("duplicate plan = %v", err)
	}
	entry := linked.Entries[len(linked.Entries)-1]
	if _, err := s.Append(ctx, other.ID, entry); !errors.Is(err, ErrPlanAlreadyRouted) {
		t.Fatalf("Append bypassed link uniqueness: %v", err)
	}
	third := other
	third.ID = "20260926-235959-" + strings.Repeat("a", 32)
	third.Entries = append(third.Entries, entry)
	if err := s.Create(ctx, third); !errors.Is(err, ErrPlanAlreadyRouted) {
		t.Fatalf("Create bypassed link uniqueness: %v", err)
	}
}

func TestStoreAppendOrderAndBounds(t *testing.T) {
	ctx := context.Background()
	s := NewStore(t.TempDir())
	r := unlinkedRecord()
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"first", "second"} {
		entry := Entry{Kind: EntryAttempt, At: r.CreatedAt, Attempt: &Attempt{Stage: stage, Outcome: "failed"}}
		r.Entries = append(r.Entries, entry)
		got, err := s.Append(ctx, r.ID, entry)
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("Append = %+v, %v; want %+v", got, err, r)
		}
	}
	huge := Entry{Kind: EntryAttempt, Attempt: &Attempt{Stage: strings.Repeat("x", MaxRecordBytes)}}
	if _, err := s.Append(ctx, r.ID, huge); err == nil {
		t.Fatal("oversized Append succeeded")
	}
	got, err := s.Load(ctx, r.ID)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("failed Append changed record: %+v, %v", got, err)
	}
	r.ID = "20260926-235959-" + strings.Repeat("f", 32)
	r.Entries = append(r.Entries, huge)
	if err := s.Create(ctx, r); err == nil {
		t.Fatal("oversized Create succeeded")
	}
}

func TestStoreHeldLock(t *testing.T) {
	for _, operation := range []string{"create", "append", "link"} {
		t.Run(operation, func(t *testing.T) {
			s := NewStore(t.TempDir())
			r := unlinkedRecord()
			if operation != "create" {
				if err := s.Create(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(s.dir, ".routes.lock")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- test-owned lock under t.TempDir.
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if err := filelock.TryLock(file); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = filelock.Unlock(file) }()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			// Post-planning recording deliberately detaches cancellation.
			ctx = context.WithoutCancel(ctx)
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "create":
					done <- s.Create(ctx, r)
				case "append":
					_, err := s.Append(ctx, r.ID, Entry{Kind: EntryAttempt, Attempt: &Attempt{Outcome: "failed"}})
					done <- err
				case "link":
					_, err := s.Link(ctx, r.ID, "plan-1", "/plans/plan-1")
					done <- err
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), path) {
					t.Fatalf("held-lock %s = %v; want bounded contention with lock path", operation, err)
				}
			case <-time.After(2 * time.Second):
				_ = filelock.Unlock(file)
				<-done
				t.Fatal("held lock blocked cancellation-detached write")
			}
			if operation == "create" {
				if _, err := s.Load(context.Background(), r.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("failed Create wrote record: %v", err)
				}
			} else if got, err := s.Load(context.Background(), r.ID); err != nil || !reflect.DeepEqual(got, r) {
				t.Fatalf("failed write changed record: %+v, %v", got, err)
			}
		})
	}
}

func TestStoreHeldLockCancellation(t *testing.T) {
	s := NewStore(t.TempDir())
	unlock, err := s.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Create(ctx, unlinkedRecord()) }()
	// Cancel while contended, not just before entering Create.
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), filepath.Join(s.dir, ".routes.lock")) {
			t.Fatalf("canceled lock = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		unlock()
		<-done
		t.Fatal("held lock did not observe cancellation promptly")
	}
}

func TestStoreConcurrentAppend(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r := unlinkedRecord()
	if err := NewStore(dir).Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			entry := Entry{Kind: EntryAttempt, Attempt: &Attempt{Stage: fmt.Sprint(i), Outcome: "failed"}}
			// Separate stores exercise the repository file lock, not a shared mutex.
			if _, err := NewStore(dir).Append(ctx, r.ID, entry); err != nil {
				t.Errorf("Append: %v", err)
			}
		})
	}
	wg.Wait()
	got, err := NewStore(dir).Load(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, entry := range got.Entries {
		if entry.Kind == EntryAttempt {
			seen[entry.Attempt.Stage]++
		}
	}
	for i := range 16 {
		if seen[fmt.Sprint(i)] != 1 {
			t.Errorf("entry %d count = %d", i, seen[fmt.Sprint(i)])
		}
	}
}

// Cancel deterministically after List has begun checking files, without sleeps.
type cancelAfterChecks struct {
	context.Context
	checks int
	cancel context.CancelFunc
}

func (c *cancelAfterChecks) Err() error {
	c.checks++
	if c.checks == 4 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestStoreListCancellationAndLimit(t *testing.T) {
	dir := t.TempDir()
	for i := range MaxRecords + 1 {
		path := filepath.Join(dir, fmt.Sprintf("20260926-235959-%032x.json", i))
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checking := &cancelAfterChecks{Context: ctx, cancel: cancel}
	if _, _, err := s.List(checking); !errors.Is(err, context.Canceled) || checking.checks != 4 {
		t.Fatalf("List cancellation = %v after %d checks", err, checking.checks)
	}
	_, warnings, err := s.List(context.Background())
	if err != nil || len(warnings) != MaxRecords+1 || !strings.Contains(warnings[len(warnings)-1], "5000") {
		t.Fatalf("List limit: %d warnings, %v", len(warnings), err)
	}
	// Damaged documents consume the same scan budget as valid records.
	if err := s.Create(context.Background(), unlinkedRecord()); err == nil {
		t.Fatal("Create accepted an already over-capacity damaged ledger")
	}
	for i := range 2 {
		if err := os.Remove(s.path(fmt.Sprintf("20260926-235959-%032x", i))); err != nil {
			t.Fatal(err)
		}
	}
	// Even an invalid name or non-regular JSON entry consumes List's budget.
	if err := os.Mkdir(filepath.Join(dir, "invalid.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(context.Background(), unlinkedRecord()); err == nil {
		t.Fatal("Create accepted a damaged ledger at exactly MaxRecords entries")
	}
}
