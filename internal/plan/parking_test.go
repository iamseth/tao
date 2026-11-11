package plan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParkingPreservesLifecycleAndRequiresExplicitUnpark(t *testing.T) {
	for _, status := range []string{StatusPlanned, StatusBlocked, StatusReviewed, StatusCompleted, StatusAbandoned} {
		t.Run(status, func(t *testing.T) {
			detail := startSliceDetail("/plans/plan-a")
			detail.State.Status = status
			if status == StatusReviewed {
				detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "approved-head"}
			}
			before := AnalyzeLifecycle(detail)
			review := CurrentReview(detail)
			merged := PlanIsMerged(detail.Events)
			prComplete := PlanIsPullRequestComplete(detail)
			slices := detail.Slices
			store := &recordingArtifactMutationStore{}
			record, err := newPlanRecord(store, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.FixedZone("local", 3600))
			if err := record.Park("  owner decision  ", at); err != nil {
				t.Fatal(err)
			}
			if detail.State.Plan.Parking == nil || detail.State.Plan.Parking.Reason != "owner decision" || detail.State.Plan.Parking.ParkedAt.Location() != time.UTC {
				t.Fatalf("parking = %+v", detail.State.Plan.Parking)
			}
			if err := RequireNotParked(detail); err == nil || !strings.Contains(err.Error(), "tao unpark") {
				t.Fatalf("admission = %v", err)
			}
			if !reflect.DeepEqual(before, AnalyzeLifecycle(detail)) || !reflect.DeepEqual(slices, detail.Slices) {
				t.Fatal("parking changed lifecycle or slices")
			}
			if !reflect.DeepEqual(review, CurrentReview(detail)) || merged != PlanIsMerged(detail.Events) || prComplete != PlanIsPullRequestComplete(detail) {
				t.Fatal("parking changed approval or completion evidence")
			}
			if Summarize(detail, at).Active() || Summarize(detail, at).Runnable() {
				t.Fatal("parked plan is active/runnable")
			}
			if err := record.Park("replacement", at); err != nil {
				t.Fatal(err)
			}
			if detail.State.Plan.Parking.Reason != "owner decision" {
				t.Fatal("replaced reason")
			}
			if err := record.Unpark(at.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := record.Unpark(at.Add(2 * time.Hour)); err != nil {
				t.Fatal(err)
			}
			if detail.State.Plan.Parking != nil || RequireNotParked(detail) != nil {
				t.Fatal("unpark did not clear marker")
			}
			if !reflect.DeepEqual(before, AnalyzeLifecycle(detail)) {
				t.Fatal("unpark changed lifecycle")
			}
			counts := map[string]int{}
			for _, event := range detail.Events {
				if event.Type == EventTypePlanParked || event.Type == EventTypePlanUnparked {
					counts[event.Type]++
					if event.Type == EventTypePlanParked && (event.Reason != "owner decision" || !event.Timestamp.Equal(at)) {
						t.Fatalf("park event = %+v", event)
					}
					if event.Type == EventTypePlanUnparked && (event.Reason != "" || !event.Timestamp.Equal(at.Add(time.Hour))) {
						t.Fatalf("unpark event = %+v", event)
					}
					if event.PlanID != detail.State.Plan.ID || event.MutationID == "" || event.ParkingOperationID == "" {
						t.Fatalf("event = %+v", event)
					}
				}
			}
			if counts[EventTypePlanParked] != 1 || counts[EventTypePlanUnparked] != 1 {
				t.Fatalf("audit events = %v", counts)
			}
		})
	}
}

func TestParkingActiveRepositoryRoundTrip(t *testing.T) {
	root := t.TempDir()
	detail := startSliceDetail(filepath.Join(root, "parked"))
	detail.State.Status = StatusInProgress
	if err := os.MkdirAll(detail.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartSliceArtifacts(t, detail.Dir, detail)
	repo := NewFileRepository(root)
	record, err := repo.ResolvePlanRecord(context.Background(), "parked")
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.ListPlans(context.Background(), PlanFilter{ActiveOnly: true})
	if err != nil || len(before) != 1 {
		t.Fatalf("before=%v err=%v", before, err)
	}
	if err := record.Park("decision", time.Now()); err != nil {
		t.Fatal(err)
	}
	active, err := repo.ListPlans(context.Background(), PlanFilter{ActiveOnly: true})
	if err != nil || len(active) != 0 {
		t.Fatalf("active=%v err=%v", active, err)
	}
	all, err := repo.ListPlans(context.Background(), PlanFilter{})
	if err != nil || len(all) != 1 || all[0].Parking == nil {
		t.Fatalf("all=%v err=%v", all, err)
	}
	if err := record.Unpark(time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := repo.ListPlans(context.Background(), PlanFilter{ActiveOnly: true})
	if err != nil || len(after) != 1 {
		t.Fatalf("after=%v err=%v", after, err)
	}
}

func TestParkingEventAloneHasNoAuthority(t *testing.T) {
	detail := startSliceDetail("/plans/plan-a")
	before := AnalyzeLifecycle(detail)
	detail.Events = append(detail.Events, Event{Type: EventTypePlanParked, PlanID: detail.State.Plan.ID, Reason: "forged", Timestamp: time.Now()})
	if RequireNotParked(detail) != nil || Summarize(detail, time.Now()).Parking != nil || !reflect.DeepEqual(before, AnalyzeLifecycle(detail)) {
		t.Fatal("audit event granted authority")
	}
	detail.State.Plan.Parking = &Parking{Reason: "state", ParkedAt: time.Now()}
	detail.Events = append(detail.Events, Event{Type: EventTypePlanUnparked})
	if RequireNotParked(detail) == nil {
		t.Fatal("unpark event cleared marker")
	}
}

func TestParkingPersistenceAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	if err := writeJSON(filepath.Join(dir, "state.json"), map[string]any{"future_root": "kept", "plan": map[string]any{"future_plan": "kept"}}); err != nil {
		t.Fatal(err)
	}
	record := testRecord(dir, detail)
	at := time.Now().UTC()
	for range 2 {
		if err := record.Park("decision", at); err != nil {
			t.Fatal(err)
		}
		state, err := ReadState(dir)
		if err != nil || state.Plan.Parking == nil {
			t.Fatalf("park reload: %+v %v", state.Plan.Parking, err)
		}
		if err := record.Unpark(at); err != nil {
			t.Fatal(err)
		}
		state, err = ReadState(dir)
		if err != nil || state.Plan.Parking != nil {
			t.Fatalf("unpark reload: %+v %v", state.Plan.Parking, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json")) // #nosec G304 -- path is under a test-owned temporary plan directory.
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["future_root"] != "kept" || raw["plan"].(map[string]any)["future_plan"] != "kept" {
		t.Fatal("unknown fields lost")
	}
}

func TestParkingMutationReplay(t *testing.T) {
	for _, unpark := range []bool{false, true} {
		for _, operation := range []string{"state", "event-1", "events-sync", "remove"} {
			t.Run(fmtParkingCase(unpark, operation), func(t *testing.T) {
				root := t.TempDir()
				writeMinimalPlan(t, root, "parked", "Parked")
				repo := NewFileRepository(root)
				detail, err := repo.GetPlan(context.Background(), "parked")
				if err != nil {
					t.Fatal(err)
				}
				at := time.Now().UTC()
				if unpark {
					if err := testRecord(detail.Dir, detail).Park("decision", at); err != nil {
						t.Fatal(err)
					}
				}
				store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: operation}}
				record, err := newPlanRecord(store, detail.Dir, detail)
				if err != nil {
					t.Fatal(err)
				}
				mutate := func() error {
					if unpark {
						return record.Unpark(at)
					}
					return record.Park("decision", at)
				}
				if err := mutate(); err == nil {
					t.Fatal("expected injected failure")
				}
				if (detail.State.Plan.Parking != nil) != unpark {
					t.Fatal("failed mutation published state")
				}
				if err := mutate(); err != nil {
					t.Fatal(err)
				}
				reloaded, err := repo.GetPlan(context.Background(), "parked")
				if err != nil {
					t.Fatal(err)
				}
				if (reloaded.State.Plan.Parking == nil) != unpark {
					t.Fatal("replay marker mismatch")
				}
				kind := EventTypePlanParked
				if unpark {
					kind = EventTypePlanUnparked
				}
				count := 0
				for _, event := range reloaded.Events {
					if event.Type == kind {
						count++
						if event.MutationID == "" || !event.Timestamp.Equal(at) {
							t.Fatalf("event = %+v", event)
						}
					}
				}
				if count != 1 {
					t.Fatalf("%s count = %d", kind, count)
				}
			})
		}
	}
}

func fmtParkingCase(unpark bool, operation string) string {
	if unpark {
		return "unpark/" + operation
	}
	return "park/" + operation
}

func TestParkingClearRequiresExplicitDeclaration(t *testing.T) {
	baseline := State{Plan: PlanState{Parking: &Parking{Reason: "decision", ParkedAt: time.Now()}}}
	intended := cloneState(baseline)
	intended.Plan.Parking = nil
	if err := validateStateChangeDeclarations(baseline, intended, nil); err == nil {
		t.Fatal("undeclared clear accepted")
	}
	changes := newArtifactChangeSet(&PlanDetail{State: intended})
	changes.ClearPlanParking()
	if err := validateStateChangeDeclarations(baseline, intended, changes); err != nil {
		t.Fatal(err)
	}
	if err := (Parking{Reason: "decision"}).Validate(); err == nil {
		t.Fatal("zero timestamp accepted")
	}
}

func TestParkingRejectsInvalidReasons(t *testing.T) {
	for _, reason := range []string{"", "  ", strings.Repeat("x", MaxAbandonmentReasonBytes+1), "escape\x1b[0m", "line\nfeed", string([]byte{0xff})} {
		detail := startSliceDetail("/plans/plan-a")
		record, err := newPlanRecord(&recordingArtifactMutationStore{}, detail.Dir, detail)
		if err != nil {
			t.Fatal(err)
		}
		if err := record.Park(reason, time.Now()); err == nil {
			t.Errorf("accepted %q", reason)
		}
	}
}
