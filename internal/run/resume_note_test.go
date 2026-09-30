package run

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
)

func resumeNoteTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func resumeNoteFixture(t *testing.T) (ResumeNoteStore, *plan.PlanDetail, *plan.PlanRecord) {
	t.Helper()
	dir, root := resumeNoteTempDir(t), resumeNoteTempDir(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{Dir: dir,
		State:  plan.State{Status: plan.StatusInProgress, CreatedAt: now, Repo: plan.Repo{Root: root}, Plan: plan.PlanState{ID: "same-plan", PendingSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{PlanID: "same-plan", Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPending}}},
	}
	for name, value := range map[string]any{"state.json": detail.State, "slices.json": detail.Slices} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record, err := plan.NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice("001-a", plan.SliceStartRequest{ExecutionRoot: root, Run: &plan.SliceRunStart{CommitPolicy: "slice"}, Boundary: &plan.SliceExecutionStart{Branch: "feature/test", Head: "old-head", CommitPolicy: "slice"}, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := record.BlockSlice("001-a", "waiting", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return ResumeNoteStore{DataHome: resumeNoteTempDir(t)}, detail, record
}

func TestResumeNoteRoundTrip(t *testing.T) {
	store, detail, _ := resumeNoteFixture(t)
	before, _ := json.Marshal(detail)
	if err := store.Save(detail, "001-a", "  next: inspect café 世界  "); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(detail, "001-a"); err != nil || got != "next: inspect café 世界" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	after, _ := json.Marshal(detail)
	if string(before) != string(after) {
		t.Fatal("cache changed plan detail")
	}
	_, other, _ := resumeNoteFixture(t)
	if got, err := store.Load(other, "001-a"); err != nil || got != "" {
		t.Fatalf("other directory = %q, %v", got, err)
	}
	if err := store.Save(detail, "001-a", "replacement"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(detail, "001-a"); err != nil || got != "replacement" {
		t.Fatalf("replacement = %q, %v", got, err)
	}
	files := 0
	err := filepath.WalkDir(filepath.Join(store.DataHome, "run-resume"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if entry.IsDir() {
			want = 0o700
		} else {
			files++
		}
		if info.Mode().Perm() != want {
			t.Errorf("permissions %s: %o", path, info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("cache files = %d", files)
	}
	if err := store.Clear(detail.Dir, "001-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(detail.Dir, "001-a"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(detail, "001-a"); err != nil || got != "" {
		t.Fatalf("cleared = %q, %v", got, err)
	}
}

func TestResumeNoteFreshness(t *testing.T) {
	store, detail, record := resumeNoteFixture(t)
	if err := store.Save(detail, "001-a", "first block"); err != nil {
		t.Fatal(err)
	}
	blockEvents := func() []plan.Event {
		var events []plan.Event
		for _, event := range detail.Events {
			if event.Type == plan.EventTypeSliceBlocked {
				events = append(events, event)
			}
		}
		return events
	}
	before := blockEvents()
	now := detail.Slices.Slices[0].Timing.UpdatedAt.Add(time.Minute)
	if err := record.BlockSlice("001-a", "waiting", now); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, blockEvents()) {
		t.Fatal("idempotent block unexpectedly changed block events")
	}
	if got, err := store.Load(detail, "001-a"); err != nil || got != "" {
		t.Fatalf("repeated block = %q, %v", got, err)
	}
	if err := store.Save(detail, "001-a", "second block"); err != nil {
		t.Fatal(err)
	}
	snapshot := cloneResumeNoteDetail(t, detail)
	if err := record.ContinueBlocked(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(detail, "001-a"); err != nil || got != "" {
		t.Fatalf("continued live detail = %q, %v", got, err)
	}
	if got, err := store.Load(snapshot, "001-a"); err != nil || got != "second block" {
		t.Fatalf("immutable snapshot = %q, %v", got, err)
	}
	if err := record.BlockSlice("001-a", "waiting", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(detail, "001-a"); err != nil || got != "" {
		t.Fatalf("new block without note = %q, %v", got, err)
	}
}

func cloneResumeNoteDetail(t *testing.T, detail *plan.PlanDetail) *plan.PlanDetail {
	t.Helper()
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var clone plan.PlanDetail
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func TestReadResumeNoteFileBounds(t *testing.T) {
	for _, test := range []struct {
		name, text string
		valid      bool
	}{
		{"empty", " \n", false}, {"runes", strings.Repeat("a", agentinput.MaxTextRunes+1), false},
		{"invalid-utf8", "\xff", false},
		{"unicode", strings.Repeat("😀", agentinput.MaxTextRunes), true},
		{"bytes", strings.Repeat("😀", agentinput.MaxTextRunes) + " ", false},
		{"escaped", strings.Repeat("\x00", agentinput.MaxTextRunes), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(resumeNoteTempDir(t), "note")
			if err := os.WriteFile(path, []byte(test.text), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadResumeNoteFile(path)
			if (err == nil) != test.valid {
				t.Fatalf("read valid=%v: %v", test.valid, err)
			}
			store, detail, _ := resumeNoteFixture(t)
			err = store.Save(detail, "001-a", test.text)
			if (err == nil) != test.valid {
				t.Fatalf("save valid=%v: %v", test.valid, err)
			}
			if test.valid {
				loaded, err := store.Load(detail, "001-a")
				if err != nil || loaded != got {
					t.Fatalf("round trip = %q, %v", loaded, err)
				}
			}
		})
	}
	dir := resumeNoteTempDir(t)
	if _, err := ReadResumeNoteFile(dir); err == nil {
		t.Fatal("read directory")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResumeNoteFile(link); err == nil {
		t.Fatal("read symlink")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil { // #nosec G204 -- fixed command creates a FIFO in the test-owned temporary directory.
		t.Fatal(err)
	}
	if _, err := ReadResumeNoteFile(fifo); err == nil {
		t.Fatal("read FIFO")
	}
}

func resumeNoteEntryPath(t *testing.T, store ResumeNoteStore) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(store.DataHome, "run-resume", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("cache paths = %v, %v", paths, err)
	}
	return paths[0]
}

func TestResumeNoteCorruptCache(t *testing.T) {
	for _, kind := range []string{"json", "oversized", "empty", "text-bound", "version", "unknown", "trailer", "unreadable", "directory", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			store, detail, _ := resumeNoteFixture(t)
			if err := store.Save(detail, "001-a", "context"); err != nil {
				t.Fatal(err)
			}
			path := resumeNoteEntryPath(t, store)
			data, err := os.ReadFile(path) // #nosec G304 -- cache entry discovered exclusively inside the test-owned temporary directory.
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "json":
				data = []byte("{")
			case "oversized":
				data = []byte(strings.Repeat("x", int(maxResumeNoteCacheBytes)+1))
			case "trailer":
				data = append(data, []byte(" {}")...)
			case "empty", "text-bound", "version", "unknown":
				switch kind {
				case "empty":
					fields["text"] = " "
				case "text-bound":
					fields["text"] = strings.Repeat("a", agentinput.MaxTextRunes+1)
				case "version":
					fields["version"] = 999
				case "unknown":
					fields["extra"] = true
				}
				data, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, data, 0o600); err != nil { // #nosec G703 -- deliberately corrupt the test-owned cache entry, never an external path.
				t.Fatal(err)
			}
			switch kind {
			case "unreadable":
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
			case "directory", "symlink", "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "directory":
					err = os.Mkdir(path, 0o700)
				case "symlink":
					err = os.Symlink(filepath.Join(store.DataHome, "absent"), path)
				case "fifo":
					err = exec.Command("mkfifo", path).Run() // #nosec G204 -- fixed command replaces the test-owned cache entry with a FIFO fixture.
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before, _ := json.Marshal(detail)
			got, err := store.Load(detail, "001-a")
			if err == nil || got != "" || len(err.Error()) > 200 {
				t.Fatalf("corrupt load = %q, %v", got, err)
			}
			after, _ := json.Marshal(detail)
			if string(before) != string(after) {
				t.Fatal("corrupt read mutated plan")
			}
		})
	}
}

func TestResumeNoteUnsafeStorage(t *testing.T) {
	for _, kind := range []string{"control", "execution", "workspace", "git-ancestor", "home-link", "ancestor-link", "cache-link", "entry-link", "public-cache", "parent-file"} {
		t.Run(kind, func(t *testing.T) {
			store, detail, _ := resumeNoteFixture(t)
			target := resumeNoteTempDir(t)
			var err error
			switch kind {
			case "control":
				store.DataHome = filepath.Join(detail.State.Repo.Root, "data")
			case "execution":
				detail.Slices.Slices[0].ExecutionRoot = target
				store.DataHome = filepath.Join(target, "data")
			case "workspace":
				detail.State.Workspace = &plan.Workspace{Path: target}
				store.DataHome = filepath.Join(target, "data")
			case "git-ancestor":
				err = os.WriteFile(filepath.Join(target, ".git"), []byte("gitdir: elsewhere"), 0o600)
				store.DataHome = filepath.Join(target, "data")
			case "home-link":
				store.DataHome = filepath.Join(store.DataHome, "alias")
				err = os.Symlink(target, store.DataHome)
			case "ancestor-link":
				alias := filepath.Join(store.DataHome, "alias")
				err = os.Symlink(target, alias)
				store.DataHome = filepath.Join(alias, "data")
			case "cache-link":
				err = os.Symlink(target, filepath.Join(store.DataHome, "run-resume"))
			case "entry-link":
				if err := store.Save(detail, "001-a", "original"); err != nil {
					t.Fatal(err)
				}
				path := resumeNoteEntryPath(t, store)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Join(target, "victim"), path)
			case "public-cache":
				path := filepath.Join(store.DataHome, "run-resume")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				err = os.Chmod(path, 0o755) //nolint:gosec // Deliberately unsafe permissions exercise rejection.
			case "parent-file":
				file := filepath.Join(store.DataHome, "file")
				err = os.WriteFile(file, nil, 0o600)
				store.DataHome = filepath.Join(file, "data")
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(detail)
			if err := store.Save(detail, "001-a", "must not publish"); err == nil {
				t.Fatal("unsafe storage accepted")
			}
			after, _ := json.Marshal(detail)
			if string(before) != string(after) {
				t.Fatal("failed save mutated plan")
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != ".git" {
					t.Fatalf("wrote into target: %s", entry.Name())
				}
			}
		})
	}
}

func TestResumeNoteEventFreshness(t *testing.T) {
	store, detail, _ := resumeNoteFixture(t)
	if err := store.Save(detail, "001-a", "context"); err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{plan.EventTypeAgentMetrics, plan.EventTypeSessionTimeout,
		plan.EventTypeSliceStarted, plan.EventTypeSliceBlocked, plan.EventTypeSliceRestarted,
		plan.EventTypeSliceResumeAttempted, plan.EventTypeSliceCompleted} {
		for _, delay := range []time.Duration{0, time.Second} {
			t.Run(fmt.Sprintf("%s/%s", eventType, delay), func(t *testing.T) {
				clone := cloneResumeNoteDetail(t, detail)
				clone.Events = append(clone.Events, plan.Event{Type: eventType, PlanID: detail.State.Plan.ID,
					SliceID: "001-a", Timestamp: detail.Slices.Slices[0].Timing.UpdatedAt.Add(delay)})
				want := ""
				if eventType == plan.EventTypeAgentMetrics || eventType == plan.EventTypeSessionTimeout {
					want = "context"
				}
				if got, err := store.Load(clone, "001-a"); err != nil || got != want {
					t.Fatalf("load = %q, %v; want %q", got, err, want)
				}
			})
		}
	}
}

func TestResumeNoteAmbiguousFreshness(t *testing.T) {
	store, detail, _ := resumeNoteFixture(t)
	if err := store.Save(detail, "001-a", "context"); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*plan.PlanDetail){
		"no-current":    func(d *plan.PlanDetail) { d.State.Plan.CurrentSlice = nil },
		"other-current": func(d *plan.PlanDetail) { d.State.Plan.CurrentSlice = new("other") },
		"completed":     func(d *plan.PlanDetail) { d.State.Status = plan.StatusCompleted },
		"duplicate":     func(d *plan.PlanDetail) { d.Slices.Slices = append(d.Slices.Slices, d.Slices.Slices[0]) },
		"two-blocked": func(d *plan.PlanDetail) {
			other := d.Slices.Slices[0]
			other.ID = "002-b"
			d.Slices.Slices = append(d.Slices.Slices, other)
		},
		"no-start":    func(d *plan.PlanDetail) { d.Slices.Slices[0].Timing.StartedAt = nil },
		"no-update":   func(d *plan.PlanDetail) { d.Slices.Slices[0].Timing.UpdatedAt = time.Time{} },
		"no-activity": func(d *plan.PlanDetail) { d.Slices.Slices[0].Timing.LastActivityAt = nil },
		"no-events":   func(d *plan.PlanDetail) { d.Events = nil },
		"wrong-event-plan": func(d *plan.PlanDetail) {
			for i := range d.Events {
				d.Events[i].PlanID = "other"
			}
		},
		"intent":        func(d *plan.PlanDetail) { d.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{} },
		"completion":    func(d *plan.PlanDetail) { d.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{} },
		"missing-path":  func(d *plan.PlanDetail) { d.Dir = filepath.Join(d.Dir, "absent") },
		"relative-path": func(d *plan.PlanDetail) { d.Dir = "relative" },
	} {
		t.Run(name, func(t *testing.T) {
			clone := cloneResumeNoteDetail(t, detail)
			mutate(clone)
			if got, err := store.Load(clone, "001-a"); err != nil || got != "" {
				t.Fatalf("ambiguous load = %q, %v", got, err)
			}
			if err := store.Save(clone, "001-a", "invalid"); err == nil {
				t.Fatal("ambiguous save accepted")
			}
		})
	}
	if got, err := store.Load(nil, "001-a"); err != nil || got != "" {
		t.Fatalf("nil load = %q, %v", got, err)
	}
}

func TestResumeNoteRestartCompletionAndFailedDeletion(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint("restart=", restart), func(t *testing.T) {
			store, detail, record := resumeNoteFixture(t)
			if err := store.Save(detail, "001-a", "old context"); err != nil {
				t.Fatal(err)
			}
			path := resumeNoteEntryPath(t, store)
			original, err := os.ReadFile(path) // #nosec G304 -- cache entry under the test-owned data home.
			if err != nil {
				t.Fatal(err)
			}
			// Refuse deletion through an unsafe alias; the real cache remains intact.
			alias := filepath.Join(resumeNoteTempDir(t), "alias")
			if err := os.Symlink(store.DataHome, alias); err != nil {
				t.Fatal(err)
			}
			if err := (ResumeNoteStore{DataHome: alias}).Clear(detail.Dir, "001-a"); err == nil {
				t.Fatal("unsafe clear succeeded")
			}
			now := detail.Slices.Slices[0].Timing.UpdatedAt.Add(time.Minute)
			if restart {
				slice := detail.Slices.Slices[0]
				err = record.RestartBlockedSlice(plan.BlockedSliceRestartRequest{SliceID: slice.ID, PriorRoot: slice.ExecutionRoot, PriorBoundary: *slice.ExecutionStart, BaselineBranch: "main", BaselineHead: "new-head", RestartedAt: now})
			} else {
				err = record.ContinueBlocked(now)
				if err == nil {
					err = record.CompleteSlice("001-a", "done", nil, now.Add(time.Minute))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := store.Load(detail, "001-a"); err != nil || got != "" {
				t.Fatalf("settled load = %q, %v", got, err)
			}
			if restart {
				if err := record.StartSlice("001-a", plan.SliceStartRequest{ExecutionRoot: detail.State.Repo.Root, Boundary: &plan.SliceExecutionStart{Branch: "feature/test", Head: "new-head"}, StartedAt: now.Add(time.Minute)}); err != nil {
					t.Fatal(err)
				}
				if err := record.BlockSlice("001-a", "waiting", now.Add(2*time.Minute)); err != nil {
					t.Fatal(err)
				}
				if got, err := store.Load(detail, "001-a"); err != nil || got != "" {
					t.Fatalf("restarted block = %q, %v", got, err)
				}
			}
			retained, err := os.ReadFile(path) // #nosec G304 -- same test-owned entry inspected before deletion failure.
			if err != nil || string(retained) != string(original) {
				t.Fatalf("cache was not retained: %v", err)
			}
		})
	}
}

func TestResumeNoteFailedReplacementRetainsPreviousEntry(t *testing.T) {
	store, detail, _ := resumeNoteFixture(t)
	if err := store.Save(detail, "001-a", "previous"); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]string)
	for _, name := range []string{"state.json", "slices.json", "events.jsonl"} {
		data, err := os.ReadFile(filepath.Join(detail.Dir, name)) // #nosec G304 -- fixed artifact names in the test-owned plan directory.
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(data)
	}
	if err := store.Save(detail, "001-a", strings.Repeat("a", agentinput.MaxTextRunes+1)); err == nil {
		t.Fatal("invalid replacement succeeded")
	}
	dir := filepath.Join(store.DataHome, "run-resume")
	if err := os.Chmod(dir, 0o500); err != nil { // #nosec G302 -- owner-only directory traversal with writes disabled for this failure test.
		t.Fatal(err)
	}
	if err := store.Save(detail, "001-a", "unwritable"); err == nil {
		t.Fatal("non-writable cache accepted")
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- restore private directory traversal and write access.
		t.Fatal(err)
	}
	if got, err := store.Load(detail, "001-a"); err != nil || got != "previous" {
		t.Fatalf("previous = %q, %v", got, err)
	}
	for name, want := range before {
		data, err := os.ReadFile(filepath.Join(detail.Dir, name)) // #nosec G304 -- same fixed artifact names captured above in the test-owned directory.
		if err != nil || string(data) != want {
			t.Fatalf("changed plan artifact %s: %v", name, err)
		}
	}
}

func TestResumeNoteDigestKeysAndStaleEnvelope(t *testing.T) {
	store, detail, _ := resumeNoteFixture(t)
	hostileID := "../../escape/世界"
	detail.State.Plan.CurrentSlice = &hostileID
	detail.State.Plan.PendingSlices = []string{hostileID}
	detail.Slices.Slices[0].ID = hostileID
	for i := range detail.Events {
		if detail.Events[i].SliceID == "001-a" {
			detail.Events[i].SliceID = hostileID
		}
	}
	if err := store.Save(detail, hostileID, "context"); err != nil {
		t.Fatal(err)
	}
	path := resumeNoteEntryPath(t, store)
	if len(filepath.Base(path)) != 64+len(".json") {
		t.Fatalf("unsafe key %s", path)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- hashed cache entry within the test-owned temporary data home.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), detail.Dir) || strings.Contains(string(data), hostileID) {
		t.Fatal("raw identity leaked into envelope")
	}
	for _, field := range []string{"identity", "revision"} {
		var envelope map[string]any
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope[field] = strings.Repeat("0", 64)
		stale, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, stale, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := store.Load(detail, hostileID); err != nil || got != "" {
			t.Fatalf("stale %s = %q, %v", field, got, err)
		}
	}
}

func TestResumeNoteCanonicalIdentityAndDefaultHome(t *testing.T) {
	store, detail, _ := resumeNoteFixture(t)
	t.Setenv("TAO_DATA_HOME", store.DataHome)
	if err := (ResumeNoteStore{}).Save(detail, "001-a", "context"); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(resumeNoteTempDir(t), "plan-alias")
	if err := os.Symlink(detail.Dir, alias); err != nil {
		t.Fatal(err)
	}
	detail.Dir = alias
	if got, err := store.Load(detail, "001-a"); err != nil || got != "context" {
		t.Fatalf("canonical alias = %q, %v", got, err)
	}
	if err := store.Clear(alias, "001-a"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(alias)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "resume") {
			t.Fatal("cache leaked into plan directory")
		}
	}
}
