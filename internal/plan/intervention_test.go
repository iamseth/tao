package plan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestInterventionAttribution(t *testing.T) {
	original := Event{Type: EventTypeSliceAmended, PlanID: "plan"}
	attributed := WithInterventionAttribution(original, "operator", "edit_amend")
	if original.Actor != "" || attributed.Actor != "operator" || attributed.Trigger != "edit_amend" || attributed.AttributionVersion != 1 {
		t.Fatalf("bad copy: %+v", attributed)
	}
	data, _ := json.Marshal(original)
	if strings.Contains(string(data), "actor") || strings.Contains(string(data), "trigger") || strings.Contains(string(data), "attribution_version") {
		t.Fatal("legacy fields not omitted")
	}
	data, _ = json.Marshal(attributed)
	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(attributed, decoded) {
		t.Fatalf("roundtrip: %v", err)
	}
	// Unknown future fields remain readable, without being interpreted by lifecycle code.
	if err := json.Unmarshal([]byte(`{"type":"intervention_observed","actor":"future","trigger":"future","attribution_version":99}`), &decoded); err != nil {
		t.Fatal(err)
	}
	unknown := WithInterventionAttribution(original, "operator", "unsupported")
	if unknown.Actor != "unknown" || unknown.Trigger != "unknown" {
		t.Fatal(unknown)
	}
	if !semanticEventsWereRecorded([]Event{original}, []Event{attributed}) || !semanticEventsWereRecorded([]Event{attributed}, []Event{original}) {
		t.Fatal("historical semantic retry mismatch")
	}
	if eventsWereRecorded([]Event{original}, []Event{attributed}) {
		t.Fatal("exact replay ignored diagnostic bytes")
	}
	changed := attributed
	changed.Reason = "different"
	if semanticEventsWereRecorded([]Event{original}, []Event{changed}) {
		t.Fatal("authority mismatch ignored")
	}
	detail := blockedAmendDetail()
	before := AnalyzeLifecycle(detail)
	action := DeriveNextAction(detail)
	detail.Events = append(detail.Events, WithInterventionAttribution(Event{Type: EventTypeInterventionObserved}, "operator", "run_restart"), attributed, unknown, decoded)
	if !reflect.DeepEqual(before, AnalyzeLifecycle(detail)) || !reflect.DeepEqual(action, DeriveNextAction(detail)) {
		t.Fatal("diagnostics changed lifecycle/action")
	}
}

func TestWithVia(t *testing.T) {
	base := WithInterventionAttribution(Event{Type: EventTypeSliceApproved, PlanID: "plan"}, "operator", "approve")

	labeled := WithVia(base, "  claude-code-workflow wf_43dba409-ce8 ")
	if labeled.Via != "claude-code-workflow wf_43dba409-ce8" {
		t.Fatalf("expected trimmed label, got %q", labeled.Via)
	}
	if base.Via != "" {
		t.Fatal("WithVia mutated its input")
	}
	for name, raw := range map[string]string{
		"empty":     "",
		"oversized": strings.Repeat("x", 129),
		"newline":   "relay\nbot",
		"tab":       "relay\tbot",
	} {
		got := WithVia(base, raw)
		if got.Via != "" {
			t.Fatalf("%s: expected empty via, got %q", name, got.Via)
		}
		if got.Actor != "operator" || got.Trigger != "approve" || got.AttributionVersion != 1 {
			t.Fatalf("%s: attribution changed: %+v", name, got)
		}
	}
	if labeled.Actor != "operator" || labeled.Trigger != "approve" || labeled.AttributionVersion != 1 {
		t.Fatalf("attribution changed: %+v", labeled)
	}
	data, _ := json.Marshal(base)
	if strings.Contains(string(data), "via") {
		t.Fatal("empty via not omitted")
	}
}
