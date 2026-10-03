package configtypes

import (
	"encoding/json"
	"testing"
)

func TestModelSelectionFor(t *testing.T) {
	for _, tt := range []struct {
		role ModelRole
		want string
	}{
		{ModelRoleDefault, "base"},
		{ModelRoleRun, "run"},
		{ModelRoleReview, "review"},
		{ModelRoleMergeReview, "merge"},
		{ModelRoleResolver, "resolver"},
		{"unknown", "base"},
		{"rework_escalation", "base"},
		{"", "base"},
	} {
		t.Run(string(tt.role), func(t *testing.T) {
			models := ModelSelection{Base: "base", Run: "run", Review: "review", MergeReview: "merge", Resolver: "resolver", ReworkEscalation: "strong"}
			if got := models.For(tt.role); got != tt.want {
				t.Fatalf("For(%q) = %q, want %q", tt.role, got, tt.want)
			}
			for _, fallback := range []string{"", "base"} {
				models = ModelSelection{Base: fallback, ReworkEscalation: "strong"}
				if got := models.For(tt.role); got != fallback {
					t.Fatalf("For(%q) = %q, want fallback %q", tt.role, got, fallback)
				}
			}
		})
	}
}

func TestEffortFor(t *testing.T) {
	m := ModelSelection{Effort: "base", RunEffort: "run", ReviewEffort: "review", MergeReviewEffort: "merge", ResolverEffort: "resolver", ReworkEscalation: "strong"}
	for role, want := range map[ModelRole]string{ModelRoleDefault: "base", ModelRoleRun: "run", ModelRoleReview: "review", ModelRoleMergeReview: "merge", ModelRoleResolver: "resolver", "unknown": "base", "rework_escalation": "base"} {
		if got := m.EffortFor(role); got != want {
			t.Fatalf("%s: %q != %q", role, got, want)
		}
		for _, base := range []string{"", "fallback"} {
			if got := (ModelSelection{Effort: base, ReworkEscalation: "strong"}).EffortFor(role); got != base {
				t.Fatalf("%s: fallback %q != %q", role, got, base)
			}
		}
	}
}

func TestModelSelectionJSON(t *testing.T) {
	for _, tt := range []struct {
		models ModelSelection
		want   string
	}{
		{ModelSelection{}, `{}`},
		{ModelSelection{ReworkEscalation: "strong"}, `{"rework_escalation_model":"strong"}`},
		{ModelSelection{Base: "base", Run: "run", Review: "review", MergeReview: "merge", Resolver: "resolver", ReworkEscalation: "strong"}, `{"model":"base","run_model":"run","review_model":"review","merge_review_model":"merge","resolver_model":"resolver","rework_escalation_model":"strong"}`},
	} {
		got, err := json.Marshal(tt.models)
		if err != nil || string(got) != tt.want {
			t.Fatalf("Marshal() = %s, %v; want %s", got, err, tt.want)
		}
		var decoded ModelSelection
		if err := json.Unmarshal([]byte(tt.want), &decoded); err != nil || decoded != tt.models {
			t.Fatalf("Unmarshal() = %+v, %v; want %+v", decoded, err, tt.models)
		}
	}
}
