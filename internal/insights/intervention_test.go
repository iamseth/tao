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
				if eventType == plan.EventTypeScopeExtension {
					event.ScopeClassification = "judgement"
				}
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

func mechanicalScopeEvents() []plan.Event {
	amendment := plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeSliceAmended, PlanID: "p", SliceID: "s", Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), ScopeClassification: "mechanical", Reason: "update fixture", Paths: []string{"internal/example.golden"}, AmendedFields: []string{"expected_files"}}, "operator", "edit_amend")
	scope := amendment
	scope.Type = plan.EventTypeScopeExtension
	scope.AmendedFields = nil
	return []plan.Event{amendment, scope}
}

func TestMechanicalScopePaths(t *testing.T) {
	for _, path := range []string{"", " ", "../secret", "a/../b", "/absolute", " /absolute ", `C:\secret`, `a\..\b`, "a\x00b"} {
		t.Run(path, func(t *testing.T) {
			e := mechanicalScopeEvents()[0]
			e.Paths = []string{path}
			if validMechanicalScope(e) {
				t.Fatalf("accepted unsafe path %q", path)
			}
		})
	}
}

func TestMechanicalScopeProjection(t *testing.T) {
	for _, tc := range []struct {
		name              string
		mutate            func([]plan.Event) []plan.Event
		invalid, operator bool
	}{
		{name: "paired"},
		{name: "judgement", mutate: func(es []plan.Event) []plan.Event {
			for i := range es {
				es[i].ScopeClassification = "judgement"
			}
			return es
		}, operator: true},
		{name: "legacy", mutate: func(es []plan.Event) []plan.Event { es[0].ScopeClassification = ""; es[0].Paths = nil; return es[:1] }, operator: true},
		{name: "missing pair", mutate: func(es []plan.Event) []plan.Event { return es[:1] }, invalid: true},
		{name: "mixed fields", mutate: func(es []plan.Event) []plan.Event {
			es[0].AmendedFields = append(es[0].AmendedFields, "tasks")
			return es
		}, invalid: true},
		{name: "gate replacement", mutate: func(es []plan.Event) []plan.Event {
			es[0].ReplacedVerificationCommands = []plan.VerificationCommandReplacement{{Old: "test", New: "true"}}
			return es
		}, invalid: true},
		{name: "foreign", mutate: func(es []plan.Event) []plan.Event { es[0].PlanID = "other"; return es }, invalid: true},
		{name: "unknown", mutate: func(es []plan.Event) []plan.Event { es[0].ScopeClassification = "future"; return es }, invalid: true},
		{name: "missing classification", mutate: func(es []plan.Event) []plan.Event { es[1].ScopeClassification = ""; return es }, invalid: true},
		{name: "missing attribution", mutate: func(es []plan.Event) []plan.Event { es[0].Actor = ""; return es }, invalid: true},
		{name: "empty reason", mutate: func(es []plan.Event) []plan.Event { es[0].Reason = " "; return es }, invalid: true},
		{name: "large reason", mutate: func(es []plan.Event) []plan.Event { es[0].Reason = strings.Repeat("x", 16385); return es }, invalid: true},
		{name: "unsafe path", mutate: func(es []plan.Event) []plan.Event { es[0].Paths = []string{"../secret"}; return es }, invalid: true},
		{name: "contradictory pair", mutate: func(es []plan.Event) []plan.Event { es[1].Reason = "different"; return es }, invalid: true},
		{name: "independent intervention", mutate: func(es []plan.Event) []plan.Event {
			return append(es, plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeSliceSkipped, PlanID: "p"}, "operator", "edit_skip"))
		}, operator: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			es := mechanicalScopeEvents()
			if tc.mutate != nil {
				es = tc.mutate(es)
			}
			p := interventionProjection{planID: "p", marker: true, created: true, runContext: true}
			for _, e := range es {
				p.consume(e)
			}
			complete := p.complete("p", planData{historyPresent: true}, true)
			if complete == tc.invalid || (!tc.invalid && p.operator != tc.operator) {
				t.Fatalf("complete=%v projection=%+v", complete, p)
			}
			if p.complete("p", planData{}, true) {
				t.Fatal("missing history accepted")
			}
		})
	}
}

func TestInterventionProvenance(t *testing.T) {
	approved := func(via string) plan.Event {
		return plan.WithVia(plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeSliceApproved, PlanID: "p", SliceID: "s"}, "operator", "approve"), via)
	}
	invalid := approved("")
	invalid.Via = strings.Repeat("x", intervention.MaxViaBytes+1)
	control := approved("")
	control.Via = "line\nbreak"
	forced := plan.WithVia(plan.WithInterventionAttribution(plan.Event{Type: plan.EventTypeInterventionObserved, PlanID: "p"}, "operator", "merge_force"), "orchestrator")
	for _, tc := range []struct {
		name   string
		events []plan.Event
		want   string
	}{
		{"none", nil, ""},
		{"relayed", []plan.Event{approved("orchestrator")}, "relayed"},
		{"direct", []plan.Event{approved("")}, "direct"},
		{"mixed", []plan.Event{approved("orchestrator"), approved("")}, "mixed"},
		{"merge force relayed", []plan.Event{forced}, "relayed"},
		{"oversized label", []plan.Event{invalid}, "mixed"},
		{"control character label", []plan.Event{control}, "mixed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := interventionProjection{planID: "p"}
			for _, e := range tc.events {
				p.consume(e)
			}
			if p.operator != (tc.want != "") {
				t.Fatalf("operator = %v, want %v", p.operator, tc.want != "")
			}
			if got := p.provenance(); got != tc.want {
				t.Fatalf("provenance = %q, want %q", got, tc.want)
			}
		})
	}
	reviewed := interventionProjection{planID: "p"}
	reviewed.consume(plan.Event{Type: plan.EventTypePlanReviewed, PlanID: "p", Review: &plan.PlanReview{Verdict: "approve", Via: "orchestrator"}})
	if reviewed.operator || reviewed.invalid || reviewed.provenance() != "" {
		t.Fatalf("plan_reviewed via must stay audit-only: %+v", reviewed)
	}
}
