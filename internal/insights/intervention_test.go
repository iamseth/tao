package insights

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/intervention"
	"github.com/iamseth/tao/internal/plan"
)

func TestInterventionPairs(t *testing.T) {
	for eventType, triggers := range interventionTriggers {
		for _, trigger := range triggers {
			actor := "automatic"
			if a, _ := intervention.Normalize("operator", trigger); a == "operator" {
				actor = a
			}
			t.Run(eventType+"/"+trigger, func(t *testing.T) {
				event := plan.WithInterventionAttribution(plan.Event{Type: eventType, PlanID: "p"}, actor, trigger)
				p := interventionProjection{planID: "p"}
				p.consume(event)
				p.consume(event)
				if p.invalid || p.operator != (actor == "operator") {
					t.Fatalf("projection: %+v", p)
				}
				event.AttributionVersion = 2
				p.consume(event)
				if !p.invalid {
					t.Fatal("unsupported version accepted")
				}
			})
		}
	}
	for _, trigger := range []string{"run_resume", "run_continue", "run_restart", "edit_amend", "edit_remove", "edit_skip", "edit_move", "approve", "rework_manual", "rework_force", "rework_pr", "rework_restart", "verification_repair_requested", "verification_reverify", "merge_restart", "merge_force", "merge_resolution_resume"} {
		p := interventionProjection{planID: "p"}
		p.consume(plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeInterventionObserved, PlanID: "p"}, "operator", trigger))
		if p.invalid || !p.operator {
			t.Fatalf("operator observation %s: %+v", trigger, p)
		}
	}
	for _, event := range []plan.Event{
		{Type: plan.EventTypeSliceResumeAttempted},
		{Type: plan.EventTypePlanMerged, Actor: "operator", Trigger: "approve", AttributionVersion: 1},
		{Type: plan.EventTypeInterventionObserved, Actor: strings.Repeat("hostile\x1b", 10000), Trigger: "approve", AttributionVersion: 1},
		{Type: plan.EventTypePlanReopened, Actor: "automatic", Trigger: "rework", AttributionVersion: 9},
	} {
		event.PlanID = "p"
		p := interventionProjection{planID: "p"}
		p.consume(event)
		if !p.invalid || p.operator {
			t.Fatalf("invalid pair: %+v", p)
		}
	}
	for _, typ := range []string{"future_event", plan.EventTypeSliceBlocked, plan.EventTypePlanAbandoned, plan.EventTypeSessionTimeout} {
		p := interventionProjection{planID: "p"}
		p.consume(plan.Event{Type: typ, PlanID: "p", Actor: "operator", Trigger: "approve", AttributionVersion: 1, Message: "a human fixed this"})
		if p.invalid || p.operator {
			t.Fatalf("inferred actor from outcome: %+v", p)
		}
	}
}

func TestInterventionHistory(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, marker, extra               string
		omitCreated, omitContext, noMerge bool
		unknown, unattended               bool
	}{
		{name: "clean", unattended: true},
		{name: "future unused merge force", unattended: true},
		{name: "future used merge force", extra: `{"type":"intervention_observed","plan_id":"ID","actor":"operator","trigger":"merge_force","attribution_version":1}`},
		{name: "future unknown merge force", extra: `{"type":"intervention_observed","plan_id":"ID","actor":"unknown","trigger":"unknown","attribution_version":1}`, unknown: true},
		{name: "historical flag-only merge force remains operator", extra: `{"type":"intervention_observed","plan_id":"ID","actor":"operator","trigger":"merge_force","attribution_version":1}`},
		{name: "manual uncommitted work cannot be identified", unattended: true},
		{name: "refusal and no-op emit nothing", unattended: true},
		{name: "old silence", marker: "absent", unknown: true},
		{name: "unsupported marker", marker: `{"schema":"future","version":2}`, unknown: true},
		{name: "oversized marker", marker: strings.Repeat("x", 4097), unknown: true},
		{name: "hostile marker", marker: "\x1b[31mnot-json", unknown: true},
		{name: "foreign marker", marker: `{"schema":"tao.intervention.coverage.v1","version":1,"plan_id":"other"}`, unknown: true},
		{name: "malformed", extra: "{", unknown: true},
		{name: "truncated", extra: strings.Repeat("x", 1024*1024+1), unknown: true},
		{name: "foreign history", extra: `{"type":"future","plan_id":"other"}`, unknown: true},
		{name: "missing creation", omitCreated: true, unknown: true},
		{name: "missing context", omitContext: true, unknown: true},
		{name: "legacy recovery", extra: `{"type":"slice_resume_failed","plan_id":"ID"}`, unknown: true},
		{name: "legacy endpoint", extra: `{"type":"plan_merged","plan_id":"ID"}`, unknown: true},
		{name: "legacy context", extra: `{"type":"run_context","plan_id":"ID"}`, unknown: true},
		{name: "automatic stop", extra: `{"type":"rework_stopped","plan_id":"ID","actor":"automatic","trigger":"rework_stop","attribution_version":1}`, unattended: true},
		{name: "operator restart", extra: `{"type":"intervention_observed","plan_id":"ID","actor":"operator","trigger":"rework_restart","attribution_version":1}`},
		{name: "unknown unrelated", extra: `{"type":"future","plan_id":"ID"}`, unattended: true},
		{name: "reopened", extra: `{"type":"plan_reopened","plan_id":"ID","actor":"automatic","trigger":"rework","attribution_version":1}`},
		{name: "never merged", noMerge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			id := filepath.Base(dir)
			if tc.marker == "" {
				if err := intervention.WriteCoverage(dir, id); err != nil {
					t.Fatal(err)
				}
			} else if tc.marker != "absent" {
				if err := os.WriteFile(filepath.Join(dir, "intervention-coverage.json"), []byte(tc.marker), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var events []plan.Event
			if !tc.omitCreated {
				events = append(events, plan.Event{Type: "plan_created", PlanID: id, Timestamp: now.Add(-30 * 24 * time.Hour)})
			}
			if !tc.omitContext {
				events = append(events, plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeRunContext, PlanID: id}, "automatic", "run_context"))
			}
			events = append(events, plan.Event{Type: plan.EventTypeSliceStarted, PlanID: id})
			if !tc.noMerge {
				events = append(events, plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypePlanMerged, PlanID: id}, "automatic", "merge_complete"))
			}
			var body []byte
			for _, event := range events {
				b, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				body = append(body, b...)
				body = append(body, '\n')
			}
			body = append(body, strings.ReplaceAll(tc.extra, "ID", id)...)
			path := filepath.Join(dir, "events.jsonl")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := readPlanEvents(context.Background(), dir)
			if readErr != nil && tc.name != "truncated" {
				t.Fatal(readErr)
			}
			o := observePlan(sourceIdentity{}, plan.PlanSummary{ID: id, Status: plan.StatusCompleted}, data, now)
			if o.attributionComplete == tc.unknown || o.unattended != tc.unattended {
				t.Fatalf("complete=%v unattended=%v", o.attributionComplete, o.unattended)
			}
			after, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("reader mutated directory")
			}
			unchanged, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary fixture.
			if err != nil {
				t.Fatal(err)
			}
			if string(unchanged) != string(body) {
				t.Fatal("reader mutated history")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := readPlanEvents(ctx, dir); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
		})
	}
}

func TestInterventionLifetimeAndMaturity(t *testing.T) {
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for _, operator := range []bool{false, true} {
		for _, status := range []string{plan.StatusCompleted, plan.StatusAbandoned, plan.StatusPlanned, plan.StatusInProgress} {
			data := planData{historyPresent: true, intervention: interventionProjection{planID: "p", marker: true}}
			events := []plan.Event{{Type: "plan_created", PlanID: "p", Timestamp: now.Add(-14 * 24 * time.Hour)}}
			if status != plan.StatusPlanned {
				events = append(events, plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeRunContext, PlanID: "p"}, "automatic", "run_context"))
				events = append(events, plan.Event{Type: plan.EventTypeSliceStarted, PlanID: "p"})
			}
			if operator {
				events = append(events, plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeInterventionObserved, PlanID: "p"}, "operator", "run_restart"))
			}
			if status == plan.StatusCompleted {
				for _, e := range []struct{ typ, trigger string }{{plan.EventTypePlanMerged, "merge_complete"}, {plan.EventTypePlanReopened, "rework"}, {plan.EventTypePlanMerged, "merge_complete"}} {
					events = append(events, plan.WithInterventionAttribution(plan.Event{Type: e.typ, PlanID: "p"}, "automatic", e.trigger))
				}
			}
			for i, e := range events {
				consumeEvent(&data, e, i)
			}
			o := observePlan(sourceIdentity{}, plan.PlanSummary{ID: "p", Status: status}, data, now.In(time.FixedZone("west", -7*3600)))
			if !o.attributionComplete || o.unattended != (status == plan.StatusCompleted && !operator) {
				t.Fatalf("status %s operator %v: %+v", status, operator, o)
			}
			outcome := buildCohortOutcomes([]planObservation{o})
			if outcome.Quality.Unattended.Denominator != outcome.Matured || !outcome.Quality.Unattended.Sparse {
				t.Fatalf("rate: %+v", outcome)
			}
		}
	}
	empty := buildCohortOutcomes(nil)
	if empty.Quality.Unattended != EstimateRate(0, 0, scorecardMinimumSamples) {
		t.Fatalf("empty rate: %+v", empty)
	}
}

func TestInterventionCleanMerged(t *testing.T) {
	dir := t.TempDir()
	id := filepath.Base(dir)
	if err := intervention.WriteCoverage(dir, id); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	events := []plan.Event{
		{Type: "plan_created", PlanID: id, Timestamp: now.Add(-30 * 24 * time.Hour)},
		plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeRunContext, PlanID: id}, "automatic", "run_context"),
		{Type: plan.EventTypeSliceStarted, PlanID: id},
		plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypePlanMerged, PlanID: id}, "automatic", "merge_complete"),
	}
	var contents []byte
	for _, event := range events {
		b, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		contents = append(contents, b...)
		contents = append(contents, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readPlanEvents(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	o := observePlan(sourceIdentity{}, plan.PlanSummary{ID: id, Status: plan.StatusCompleted}, data, now)
	if !o.attributionComplete || !o.unattended {
		t.Fatalf("expected covered unattended merge: %+v", o)
	}
	got := buildCohortOutcomes([]planObservation{o})
	if got.Quality.Unattended.Numerator != 1 || got.Quality.Unattended.Denominator != 1 {
		t.Fatalf("rate: %+v", got.Quality.Unattended)
	}
}
