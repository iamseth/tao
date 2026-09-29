package plannerroute

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestParseMode(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  Mode
	}{
		{"", ModeOff}, {"off", ModeOff}, {"shadow", ModeShadow}, {"randomized", ModeRandomized},
	} {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseMode(tt.value)
			if err != nil || got != tt.want {
				t.Fatalf("ParseMode(%q) = %q, %v; want %q", tt.value, got, err, tt.want)
			}
		})
	}
	for _, value := range []string{"unknown", "SHADOW", " shadow", "randomized "} {
		if _, err := ParseMode(value); err == nil {
			t.Errorf("ParseMode(%q) accepted invalid mode", value)
		}
	}
}

func TestArmsFromConfig(t *testing.T) {
	configured := []runtimeconfig.PlannerRoutingArm{{Runtime: runtimeconfig.AgentClaude, Probability: 0.75}, {Runtime: runtimeconfig.AgentPi, Probability: 0.25}}
	got := ArmsFromConfig(configured)
	want := []WeightedArm{
		{Arm: Arm{Runtime: runtimeconfig.AgentClaude, Provider: Inherited, Model: Inherited, ReasoningEffort: Inherited}, Probability: 0.75},
		{Arm: Arm{Runtime: runtimeconfig.AgentPi, Provider: Inherited, Model: Inherited, ReasoningEffort: Inherited}, Probability: 0.25},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conversion = %+v, want %+v", got, want)
	}
	got[0].Probability = 1
	got[0].Arm.PromptVersion = "caller-owned"
	if configured[0].Probability != 0.75 || !reflect.DeepEqual(ArmsFromConfig(configured), want) {
		t.Fatal("conversion mutated configured arms")
	}
	if len(ArmsFromConfig(nil)) != 0 {
		t.Fatal("empty configuration gained arms")
	}
}

func TestArmKey(t *testing.T) {
	arm := testArm()
	want := "pi|inherited|inherited|inherited|prompt-v1|default"
	if got := arm.Key(); got != want || arm.Key() != got {
		t.Fatalf("Key() = %q; want stable %q", got, want)
	}
	for _, change := range []func(*Arm){
		func(a *Arm) { a.Runtime = runtimeconfig.AgentClaude },
		func(a *Arm) { a.Provider = "provider" },
		func(a *Arm) { a.Model = "model" },
		func(a *Arm) { a.ReasoningEffort = "effort" },
		func(a *Arm) { a.PromptVersion = "prompt-v2" },
		func(a *Arm) { a.PermissionMode = "skip" },
	} {
		other := arm
		change(&other)
		if other.Key() == arm.Key() {
			t.Errorf("different tuple has same key: %+v", other)
		}
	}
	if got := (Arm{}).Key(); got != "|||||" {
		t.Errorf("zero arm key = %q", got)
	}
}

func TestNoteTextBucket(t *testing.T) {
	for _, tt := range []struct {
		length int
		want   string
	}{
		{-1, "short"}, {0, "short"}, {999, "short"}, {1000, "medium"},
		{3999, "medium"}, {4000, "long"}, {10000, "long"},
	} {
		if got := NoteTextBucket(tt.length); got != tt.want {
			t.Errorf("NoteTextBucket(%d) = %q; want %q", tt.length, got, tt.want)
		}
	}
}

func TestValidRouteID(t *testing.T) {
	valid := "20260926-235959-0123456789abcdef0123456789abcdef"
	if !ValidRouteID(valid) {
		t.Fatal("valid route ID rejected")
	}
	for _, id := range []string{
		"", ".", "..", "../" + valid, valid + "/file", valid + "\\file",
		"prefix" + valid, valid + "\n", valid + "\x00", strings.ToUpper(valid),
		valid[:len(valid)-1], valid + "0", "2026092-235959-" + strings.Repeat("a", 32),
		"20260926-23595-" + strings.Repeat("a", 32), "20260926-235959-" + strings.Repeat("g", 32),
	} {
		if ValidRouteID(id) {
			t.Errorf("accepted invalid route ID %q", id)
		}
	}
}

func TestNewRouteID(t *testing.T) {
	now := time.Date(2026, 9, 26, 23, 59, 59, 0, time.FixedZone("west", -7*60*60))
	first, err := NewRouteID(now)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidRouteID(first) || !strings.HasPrefix(first, "20260927-065959-") {
		t.Fatalf("unexpected route ID %q", first)
	}
	second, err := NewRouteID(now)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidRouteID(second) || first == second {
		t.Fatalf("expected distinct valid IDs, got %q and %q", first, second)
	}
}

func TestLinkedPlanID(t *testing.T) {
	if got := (Record{}).LinkedPlanID(); got != "" {
		t.Fatalf("empty record link = %q", got)
	}
	r := testRecord()
	if got := r.LinkedPlanID(); got != "plan-1" {
		t.Fatalf("LinkedPlanID() = %q", got)
	}
	r.Entries = append(r.Entries, Entry{Kind: EntryLinked, Link: &Link{PlanID: "plan-2"}})
	if got := r.LinkedPlanID(); got != "plan-1" {
		t.Fatalf("expected first link, got %q", got)
	}
	r.Entries = []Entry{{Kind: EntryAttempt, Link: &Link{PlanID: "not-linked"}}, {Kind: EntryLinked}}
	if got := r.LinkedPlanID(); got != "" {
		t.Fatalf("missing linked payload = %q", got)
	}
}

func TestRecordJSONRoundTrip(t *testing.T) {
	r := testRecord()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Fatalf("round trip mismatch: got %+v; want %+v", got, r)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-tripped record invalid: %v", err)
	}
	// Check the wire names independently of unmarshalling into the same types.
	for _, key := range []string{
		"schema", "id", "repo_id", "created_at", "context", "assignment", "entries",
		"feature_schema", "repo_name", "unit_kind", "unit_id", "note_tags", "note_text_bucket",
		"baseline_runtime", "prompt_version", "permission_mode", "build_version",
		"policy_version", "mode", "unit_key", "kind", "eligible", "arm", "probability",
		"selected", "draw", "manual_override", "override_arm", "runtime", "provider", "model",
		"reasoning_effort", "at", "treatment", "attempt", "link", "runtime_label", "provider_id",
		"model_id", "metrics_availability", "failover", "stage", "outcome", "plan_id", "plan_dir",
	} {
		if !strings.Contains(string(data), `"`+key+`":`) {
			t.Errorf("missing JSON field %q in %s", key, data)
		}
	}
	for _, value := range []any{Entry{Kind: EntryAssigned}, Assignment{}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"treatment", "attempt", "link", "override_arm"} {
			if strings.Contains(string(data), `"`+key+`":`) {
				t.Errorf("nil pointer %q not omitted: %s", key, data)
			}
		}
	}
}

func TestRecordValidate(t *testing.T) {
	if err := testRecord().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(*Record)
	}{
		{"bad schema", func(r *Record) { r.Schema = "unknown" }},
		{"invalid ID", func(r *Record) { r.ID = "../bad" }},
		{"missing repo", func(r *Record) { r.RepoID = "" }},
		{"blank repo", func(r *Record) { r.RepoID = " \t" }},
		{"missing policy", func(r *Record) { r.Assignment.PolicyVersion = "" }},
		{"blank policy", func(r *Record) { r.Assignment.PolicyVersion = " \t" }},
		{"two links", func(r *Record) {
			r.Entries = append(r.Entries, Entry{Kind: EntryLinked, Link: &Link{PlanID: "plan-2"}})
		}},
		{"two links without payloads", func(r *Record) {
			r.Entries = []Entry{{Kind: EntryLinked}, {Kind: EntryLinked}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := testRecord()
			tt.change(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	r := testRecord()
	r.Entries = nil
	if err := r.Validate(); err != nil {
		t.Fatalf("unlinked record invalid: %v", err)
	}
}

func testArm() Arm {
	return Arm{
		Runtime: runtimeconfig.AgentPi, Provider: Inherited, Model: Inherited,
		ReasoningEffort: Inherited, PromptVersion: "prompt-v1", PermissionMode: "default",
	}
}

func testRecord() Record {
	now := time.Date(2026, 9, 26, 23, 59, 59, 0, time.UTC)
	arm := testArm()
	return Record{
		Schema: Schema, ID: "20260926-235959-0123456789abcdef0123456789abcdef", RepoID: "repo-1", CreatedAt: now,
		Context: Context{
			FeatureSchema: FeatureSchema, RepoID: "repo-1", RepoName: "example", UnitKind: "note", UnitID: "note-1",
			NoteTags: []string{"planning"}, NoteTextBucket: "short", BaselineRuntime: runtimeconfig.AgentPi,
			PromptVersion: arm.PromptVersion, PermissionMode: arm.PermissionMode, BuildVersion: "dev",
		},
		Assignment: Assignment{
			PolicyVersion: "policy-v1", Mode: ModeShadow, UnitKey: UnitKey{RepoID: "repo-1", Kind: "note", ID: "note-1"},
			Eligible: []WeightedArm{{Arm: arm, Probability: 1}}, Selected: arm, Draw: 0.25,
			ManualOverride: true, OverrideArm: &arm,
		},
		Entries: []Entry{
			{Kind: EntryAssigned, At: now},
			{Kind: EntryTreated, At: now, Treatment: &Treatment{
				RuntimeLabel: "pi", ProviderID: "observed-provider", ModelID: "observed-model", MetricsAvailability: "available",
			}},
			{Kind: EntryAttempt, At: now, Attempt: &Attempt{Stage: "planning", Outcome: "plan_created"}},
			{Kind: EntryLinked, At: now, Link: &Link{PlanID: "plan-1", PlanDir: "/data/plans/plan-1"}},
		},
	}
}
