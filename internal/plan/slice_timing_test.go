package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSliceTimingCompletionRefusalIsReadOnly(t *testing.T) {
	for _, reason := range []string{"absent", "conflict", "zero", "missing intent", "conflicting outcome", "abandoned"} {
		t.Run(reason, func(t *testing.T) {
			d := startSliceDetail("")
			start := editTime()
			event := Event{Type: EventTypeSliceStarted, PlanID: d.State.Plan.ID, SliceID: "001-a", Timestamp: start}
			d.Events = []Event{event}
			var outcome *SliceCompletionOutcome
			switch reason {
			case "absent":
				d.Events = nil
			case "conflict":
				event.Timestamp = start.Add(time.Second)
				d.Events = append(d.Events, event)
			case "zero":
				d.Events[0].Timestamp = time.Time{}
			case "missing intent":
				outcome = &SliceCompletionOutcome{Outcome: SliceCompletionCommitted}
			case "conflicting outcome":
				outcome = &SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "new"}
				d.Slices.Slices[0].CommitIntent = &SliceCommitIntent{}
				d.Slices.Slices[0].Completion = &SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "old"}
			case "abandoned":
				d.State.Status = StatusAbandoned
			}
			before := clonePlanDetail(d)
			if _, _, err := markSliceCompletedWithOutcome(d, "001-a", "done", nil, outcome, start.Add(time.Minute)); err == nil {
				t.Fatal("invalid completion accepted")
			}
			if !reflect.DeepEqual(d, before) {
				t.Fatal("failed completion mutated detail")
			}
		})
	}
}

func TestRepairSliceStartedAtJournalRetry(t *testing.T) {
	dir := t.TempDir()
	d := startSliceDetail(dir)
	start := editTime()
	writeStartSliceArtifacts(t, dir, d)
	record := testRecord(dir, d)
	if err := record.StartSlice("001-a", SliceStartRequest{StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "slices.json")
	var raw map[string]any
	readJSONFile(t, path, &raw)
	raw["slices"].([]any)[0].(map[string]any)["timing"].(map[string]any)["started_at"] = nil
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "slices"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	broken, err := newPlanRecord(store, dir, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.RepairSliceStartedAt("001-a"); err == nil {
		t.Fatal("expected injected failure")
	}
	reloaded := testRecord(dir, startSliceDetail(dir))
	if err := reloaded.RepairSliceStartedAt("001-a"); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Detail().Slices.Slices[0].Timing.StartedAt; got == nil || !got.Equal(start) {
		t.Fatalf("recovered start = %v", got)
	}
}

func TestResolveSliceStartedAt(t *testing.T) {
	start := editTime()
	for _, tc := range []struct {
		name     string
		events   []Event
		existing *time.Time
		wantErr  bool
	}{
		{name: "absent", wantErr: true},
		{name: "matching", events: []Event{{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a", Timestamp: start}}},
		{name: "foreign plan", events: []Event{{Type: EventTypeSliceStarted, PlanID: "other", SliceID: "001-a", Timestamp: start}}, wantErr: true},
		{name: "foreign slice", events: []Event{{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "other", Timestamp: start}}, wantErr: true},
		{name: "wrong type", events: []Event{{Type: EventTypeSliceCompleted, PlanID: "plan-a", SliceID: "001-a", Timestamp: start}}, wantErr: true},
		{name: "zero", events: []Event{{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a"}}, wantErr: true},
		{name: "duplicates", events: []Event{{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a", Timestamp: start}, {Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a", Timestamp: start.In(time.FixedZone("offset", 3600))}}},
		{name: "conflict", events: []Event{{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a", Timestamp: start}, {Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a", Timestamp: start.Add(time.Second)}}, wantErr: true},
		{name: "zero and valid", events: []Event{{Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a"}, {Type: EventTypeSliceStarted, PlanID: "plan-a", SliceID: "001-a", Timestamp: start}}, wantErr: true},
		{name: "existing", existing: &start},
		{name: "existing zero", existing: new(time.Time)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startSliceDetail("")
			d.State.Plan.ID = "plan-a"
			d.Events = tc.events
			d.Slices.Slices[0].Timing.StartedAt = tc.existing
			before := clonePlanDetail(d)
			got, recovered, err := ResolveSliceStartedAt(d, "001-a")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v", err)
			}
			if err == nil {
				want := start
				if tc.existing != nil {
					want = *tc.existing
				}
				if !got.Equal(want) || recovered != (tc.existing == nil) {
					t.Fatalf("got %v, recovered %v", got, recovered)
				}
			}
			if !reflect.DeepEqual(d, before) {
				t.Fatal("resolver mutated detail")
			}
		})
	}
	if _, _, err := ResolveSliceStartedAt(nil, "001-a"); err == nil {
		t.Fatal("nil accepted")
	}
	if _, _, err := ResolveSliceStartedAt(startSliceDetail(""), "unknown"); err == nil {
		t.Fatal("unknown accepted")
	}
}

func TestRepairSliceStartedAtDurableAndCompletion(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(map[bool]string{false: "repair", true: "completion"}[completion], func(t *testing.T) {
			dir := t.TempDir()
			d := startSliceDetail(dir)
			start := editTime()
			writeStartSliceArtifacts(t, dir, d)
			record, err := newPlanRecord(fileArtifactStore{}, dir, d)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.StartSlice("001-a", SliceStartRequest{StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			// Simulate a late rewrite after the record has cached a valid start.
			path := filepath.Join(dir, "slices.json")
			var raw map[string]any
			readJSONFile(t, path, &raw)
			slice := raw["slices"].([]any)[0].(map[string]any)
			timing := slice["timing"].(map[string]any)
			delete(timing, "started_at")
			timing["future_clock"] = "preserve"
			payload, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			eventsBefore, err := os.ReadFile(filepath.Join(dir, "events.jsonl")) // #nosec G304 -- internally constructed temporary test path
			if err != nil {
				t.Fatal(err)
			}
			if completion {
				err = record.CompleteSlice("001-a", "done", nil, start.Add(90*time.Second))
			} else {
				err = record.RepairSliceStartedAt("001-a")
			}
			if err != nil {
				t.Fatal(err)
			}
			files, err := loadPlanFiles(dir)
			if err != nil {
				t.Fatal(err)
			}
			got := files.slices.Slices[0]
			if got.Timing.StartedAt == nil || !got.Timing.StartedAt.Equal(start) {
				t.Fatalf("start = %v", got.Timing.StartedAt)
			}
			if completion && (got.Timing.DurationSeconds == nil || *got.Timing.DurationSeconds != 90) {
				t.Fatalf("duration = %v", got.Timing.DurationSeconds)
			}
			readJSONFile(t, path, &raw)
			if raw["slices"].([]any)[0].(map[string]any)["timing"].(map[string]any)["future_clock"] != "preserve" {
				t.Fatal("lost unknown timing field")
			}
			before, err := os.ReadFile(path) // #nosec G304 -- internally constructed temporary test path
			if err != nil {
				t.Fatal(err)
			}
			if err := record.RepairSliceStartedAt("001-a"); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path) // #nosec G304 -- internally constructed temporary test path
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("retry rewrote slices")
			}
			if !completion {
				eventsAfter, err := os.ReadFile(filepath.Join(dir, "events.jsonl")) // #nosec G304 -- internally constructed temporary test path
				if err != nil {
					t.Fatal(err)
				}
				if string(eventsBefore) != string(eventsAfter) {
					t.Fatal("repair changed events")
				}
			}
		})
	}
}
