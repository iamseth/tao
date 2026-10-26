package repoadmission

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

func TestObservationSignals(t *testing.T) {
	now := time.Now()
	record := runstatus.Record{Schema: runstatus.Schema, RepoID: "repo", PlanID: "sibling", InvocationID: "42-1", Phase: runstatus.PhaseMergeIntegrating, InvocationStartedAt: now.Add(-time.Hour), HeartbeatAt: now}
	for _, tt := range []struct {
		name                 string
		lock                 plan.RunLock
		lockErr, errorStatus error
		mutate               func(*runstatus.Record)
		self                 int
		want                 bool
		pid                  int
		phase                runstatus.Phase
	}{
		{name: "fresh without lock", lockErr: os.ErrNotExist, want: true, pid: 42, phase: record.Phase},
		{name: "unknown pid", lockErr: os.ErrNotExist, mutate: func(r *runstatus.Record) { r.InvocationID = "legacy" }, want: true, phase: record.Phase},
		{name: "dead missing", lock: plan.RunLock{PID: 42}, errorStatus: os.ErrNotExist},
		{name: "live missing", lock: plan.RunLock{PID: 42, ProcessAlive: true}, errorStatus: os.ErrNotExist, want: true, pid: 42},
		{name: "live unreadable", lock: plan.RunLock{PID: 42, ProcessAlive: true}, errorStatus: errors.New("read error"), want: true, pid: 42},
		{name: "repo mismatch", lock: plan.RunLock{PID: 42, ProcessAlive: true}, mutate: func(r *runstatus.Record) { r.RepoID = "other" }, want: true, pid: 42},
		{name: "plan mismatch", mutate: func(r *runstatus.Record) { r.PlanID = "other" }},
		{name: "invalid schema", mutate: func(r *runstatus.Record) { r.Schema = "bad" }},
		{name: "missing timestamp", mutate: func(r *runstatus.Record) { r.HeartbeatAt = time.Time{} }},
		{name: "self lock", self: 42, lock: plan.RunLock{PID: 42, ProcessAlive: true}},
		{name: "self status", self: 42},
		{name: "status pid mismatch", lock: plan.RunLock{PID: 43, ProcessAlive: true}, want: true, pid: 43},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := record
			if tt.mutate != nil {
				tt.mutate(&r)
			}
			warnings := 0
			source := ObservationSource{Now: func() time.Time { return now }, SelfPID: tt.self, Warn: func(error) { warnings++ }, ReadLock: func(string, string) (plan.RunLock, error) { return tt.lock, tt.lockErr }, ReadStatus: func(string) (runstatus.Record, error) { return r, tt.errorStatus }}
			owners := source.Observe(context.Background(), Request{Mode: Merge}, "repo", []ObservedPlan{{ID: "sibling", Dir: "sibling"}})
			if (len(owners) > 0) != tt.want {
				t.Fatalf("owners=%+v", owners)
			}
			if tt.want && (owners[0].PID != tt.pid || owners[0].Phase != tt.phase) {
				t.Fatalf("owners=%+v", owners)
			}
			if tt.name == "live unreadable" && warnings != 1 {
				t.Fatalf("warnings=%d", warnings)
			}
		})
	}
}

func TestObservationOrderAndFallback(t *testing.T) {
	source := ObservationSource{ReadLock: func(string, string) (plan.RunLock, error) { return plan.RunLock{PID: 42, ProcessAlive: true}, nil }}
	owners := source.Observe(context.Background(), Request{Mode: Merge}, "repo", []ObservedPlan{{ID: "z", Dir: "z"}, {ID: "a", Dir: "a"}, {ID: "a", Dir: "a"}})
	want := []Owner{{PlanID: "a", PID: 42}, {PlanID: "z", PID: 42}}
	if !reflect.DeepEqual(owners, want) {
		t.Fatalf("owners=%+v", owners)
	}
	message := (&ContentionError{Mode: Merge, Repository: "repo", Owners: owners}).Error()
	if !strings.Contains(message, `plan "a" PID 42, plan "z" PID 42`) || strings.Contains(message, "phase") {
		t.Fatal(message)
	}
	message = (&ContentionError{Mode: Run, Repository: "repo"}).Error()
	if !strings.Contains(message, "wait for the active operation to finish, then retry") || strings.Contains(message, "PID") || strings.Contains(message, "phase") {
		t.Fatal(message)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := source.Observe(ctx, Request{Mode: Merge}, "repo", []ObservedPlan{{ID: "a", Dir: "a"}}); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestObservationArtifactBounds(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name, content string
		valid         bool
	}{
		{"live", fmt.Sprintf("pid=%d\nplan_id=p\n", os.Getpid()), true},
		{"invalid", "pid=no\n", false}, {"missing", "plan_id=p\n", false},
		{"duplicate", "pid=42\npid=43\n", false}, {"wrong plan", "pid=42\nplan_id=other\n", false},
		{"oversized", strings.Repeat("x", 65537), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, ".run.lock"), []byte(tt.content), 0600); err != nil {
				t.Fatal(err)
			}
			lock, err := readObservationLock(dir, "p")
			if (err == nil) != tt.valid {
				t.Fatalf("lock=%+v err=%v", lock, err)
			}
			if tt.valid && !lock.ProcessAlive {
				t.Fatal("current PID not live")
			}
		})
	}
	if _, err := readObservationFile(dir); err == nil {
		t.Fatal("accepted directory")
	}
	if _, err := readObservationFile(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readObservationState(filepath.Join(dir, "state.json")); err == nil {
		t.Fatal("accepted malformed state")
	}
}

func TestSiblingEnumerationAndAdmission(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, ".git")
	if err := os.Mkdir(common, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	old := identityGit
	identityGit = func(_ context.Context, root string) ([]byte, error) {
		if root == "other" {
			return []byte(filepath.Dir(common)), nil
		}
		return []byte(common), nil
	}
	t.Cleanup(func() { identityGit = old })
	registry := taodata.NewRegistry("")
	repo := taodata.Repo{ID: taodata.RepoID(root), Root: root}
	canonical := registry.PlansDir(repo)
	selected := t.TempDir()
	for _, tt := range []struct{ base, id, root string }{{canonical, "a", root}, {selected, "z", root}, {selected, "foreign", "other"}} {
		dir := filepath.Join(tt.base, tt.id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf(`{"plan":{"id":%q},"repo":{"root":%q}}`, tt.id, tt.root)
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".run.lock"), []byte(fmt.Sprintf("pid=%d\nplan_id=%s\n", os.Getppid(), tt.id)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	invalid := filepath.Join(selected, "invalid")
	if err := os.Mkdir(invalid, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, ".run.lock"), []byte("pid=42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, "state.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	warnings := 0
	req := Request{Root: root, Mode: Merge, PlansDir: selected, PlanDir: filepath.Join(selected, "self"), Warn: func(error) { warnings++ }}
	owners := observeSiblings(context.Background(), req, common)
	if len(owners) != 2 || owners[0].PlanID != "a" || owners[1].PlanID != "z" {
		t.Fatalf("owners=%+v", owners)
	}
	if warnings != 1 {
		t.Fatalf("warnings=%d", warnings)
	}
	err = With(context.Background(), req, func(context.Context) error { t.Fatal("legacy merge entered"); return nil })
	var busy *ContentionError
	if !errors.As(err, &busy) {
		t.Fatalf("error=%v", err)
	}
	req.Mode = Run
	if err := With(context.Background(), req, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("unclassified lock blocked run: %v", err)
	}
	// Unreadable/missing observations cannot defeat the participating lease.
	req.Observe = func(context.Context, Request, string) []Owner { return nil }
	if err := With(context.Background(), req, func(context.Context) error {
		other := req
		other.Mode = Merge
		err := With(context.Background(), other, func(context.Context) error { t.Fatal("conflicting lease entered"); return nil })
		if !errors.As(err, &busy) {
			t.Fatalf("missing diagnostics defeated lease: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// No lifecycle/event files were created by refusals.
	for _, dir := range []string{filepath.Join(canonical, "a"), filepath.Join(selected, "z")} {
		if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("events: %v", err)
		}
	}
}

func TestObservationMatching(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name      string
		mode      Mode
		alive     bool
		statusPID string
		phase     runstatus.Phase
		age       time.Duration
		want      bool
	}{
		{"live run blocks merge", Merge, true, "42-1", runstatus.PhaseRunningSlice, 0, true},
		{"concurrent runs", Run, true, "42-1", runstatus.PhaseRunningSlice, 0, false},
		{"fresh merge", Run, true, "42-1", runstatus.PhaseMergeIntegrating, 0, true},
		{"pid mismatch", Run, true, "43-1", runstatus.PhaseMergeIntegrating, 0, false},
		{"dead stale", Merge, false, "42-1", runstatus.PhaseRunningSlice, time.Minute, false},
		{"dead fresh", Merge, false, "42-1", runstatus.PhaseRunningSlice, 0, true},
		{"stale phase", Run, true, "42-1", runstatus.PhaseMergeIntegrating, time.Minute, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := ObservationSource{Now: func() time.Time { return now }, ReadLock: func(string, string) (plan.RunLock, error) { return plan.RunLock{PID: 42, ProcessAlive: tt.alive}, nil }, ReadStatus: func(string) (runstatus.Record, error) {
				return runstatus.Record{Schema: runstatus.Schema, RepoID: "repo", PlanID: "sibling", InvocationID: tt.statusPID, Phase: tt.phase, InvocationStartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-tt.age)}, nil
			}}
			owners := source.Observe(context.Background(), Request{Mode: tt.mode}, "repo", []ObservedPlan{{ID: "sibling", Dir: "sibling"}})
			if (len(owners) > 0) != tt.want {
				t.Fatalf("owners=%+v, want conflict %v", owners, tt.want)
			}
		})
	}
}

func TestLargeSiblingDirectory(t *testing.T) {
	for _, signal := range []string{"lock", "status"} {
		t.Run(signal, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			common := filepath.Join(root, ".git")
			if err := os.Mkdir(common, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TAO_DATA_HOME", t.TempDir())
			old := identityGit
			calls := 0
			identityGit = func(context.Context, string) ([]byte, error) { calls++; return []byte(common), nil }
			t.Cleanup(func() { identityGit = old })
			registry := taodata.NewRegistry("")
			repo := taodata.Repo{ID: taodata.RepoID(root), Root: root}
			plans := registry.PlansDir(repo)
			// Inactive entries need no readable state, and must not consume the cap.
			for i := 0; i < 205; i++ {
				if err := os.MkdirAll(filepath.Join(plans, fmt.Sprintf("inactive-%03d", i)), 0700); err != nil {
					t.Fatal(err)
				}
			}
			store := runstatus.NewStore(registry.RuntimeStatusDir(repo), nil)
			for _, id := range []string{"z-live", "z-sibling"} {
				dir := filepath.Join(plans, id)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				data := fmt.Sprintf(`{"plan":{"id":%q},"repo":{"root":%q}}`, id, root)
				if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				if signal == "lock" {
					if err := os.WriteFile(filepath.Join(dir, ".run.lock"), []byte(fmt.Sprintf("pid=%d\nplan_id=%s\n", os.Getppid(), id)), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					now := time.Now()
					if err := store.Create(runstatus.Record{Schema: runstatus.Schema, RepoID: repo.ID, PlanID: id, InvocationID: fmt.Sprintf("%d-1", os.Getppid()), Phase: runstatus.PhaseRunningSlice, InvocationStartedAt: now, HeartbeatAt: now}); err != nil {
						t.Fatal(err)
					}
				}
			}
			var warnings []error
			req := Request{Mode: Merge, PlansDir: plans, Warn: func(err error) { warnings = append(warnings, err) }}
			owners := observeSiblings(context.Background(), req, common)
			if len(owners) != 2 || owners[0].PlanID != "z-live" || owners[1].PlanID != "z-sibling" {
				t.Fatalf("owners=%+v warnings=%v", owners, warnings)
			}
			if len(warnings) != 0 {
				t.Fatalf("warnings=%v", warnings)
			}
			if calls != 1 {
				t.Fatalf("identity lookups=%d, want one per recorded root", calls)
			}
			req.Root, req.Mode = root, Run
			if err := With(context.Background(), req, func(context.Context) error { return nil }); err != nil {
				t.Fatalf("run admission: %v", err)
			}
			if len(warnings) != 0 {
				t.Fatalf("successful admission warnings=%v", warnings)
			}
		})
	}
}
