package configtypes

import (
	"reflect"
	"testing"
)

func TestModelSettings(t *testing.T) {
	want := []struct {
		key, suffix, flag, kind string
		role                    ModelRole
		profiles                []string
		selection               ModelSelection
	}{
		{"models.model", "MODEL", "model", "model", ModelRoleDefault, []string{"run", "review", "merge"}, ModelSelection{Base: "value"}},
		{"models.run_model", "RUN_MODEL", "run-model", "model", ModelRoleRun, []string{"run"}, ModelSelection{Run: "value"}},
		{"models.review_model", "REVIEW_MODEL", "review-model", "model", ModelRoleReview, []string{"run", "review"}, ModelSelection{Review: "value"}},
		{"models.merge_review_model", "MERGE_REVIEW_MODEL", "merge-review-model", "model", ModelRoleMergeReview, []string{"merge"}, ModelSelection{MergeReview: "value"}},
		{"models.resolver_model", "RESOLVER_MODEL", "resolver-model", "model", ModelRoleResolver, []string{"merge"}, ModelSelection{Resolver: "value"}},
		{"models.rework_escalation_model", "REWORK_ESCALATION_MODEL", "rework-escalation-model", "model", "", []string{"run"}, ModelSelection{ReworkEscalation: "value"}},
		{"models.effort", "EFFORT", "effort", "effort", ModelRoleDefault, []string{"run", "review", "merge"}, ModelSelection{Effort: "value"}},
		{"models.run_effort", "RUN_EFFORT", "run-effort", "effort", ModelRoleRun, []string{"run"}, ModelSelection{RunEffort: "value"}},
		{"models.review_effort", "REVIEW_EFFORT", "review-effort", "effort", ModelRoleReview, []string{"run", "review"}, ModelSelection{ReviewEffort: "value"}},
		{"models.merge_review_effort", "MERGE_REVIEW_EFFORT", "merge-review-effort", "effort", ModelRoleMergeReview, []string{"merge"}, ModelSelection{MergeReviewEffort: "value"}},
		{"models.resolver_effort", "RESOLVER_EFFORT", "resolver-effort", "effort", ModelRoleResolver, []string{"merge"}, ModelSelection{ResolverEffort: "value"}},
	}
	got := ModelSettings()
	if len(got) != len(want) {
		t.Fatalf("count = %d", len(got))
	}
	keys, suffixes, flags := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, w := range want {
		g := got[i]
		if g.Key != w.key || g.EnvSuffix != w.suffix || g.FlagName != w.flag || g.Kind != w.kind || g.Role != w.role || !reflect.DeepEqual(g.Profiles, w.profiles) {
			t.Fatalf("setting %d = %+v", i, g)
		}
		if keys[g.Key] || suffixes[g.EnvSuffix] || flags[g.FlagName] {
			t.Fatalf("duplicate setting %s", g.Key)
		}
		keys[g.Key], suffixes[g.EnvSuffix], flags[g.FlagName] = true, true, true
		var selection ModelSelection
		*g.Field(&selection) = "value"
		if selection != w.selection {
			t.Fatalf("accessor %s = %+v", g.Key, selection)
		}
		if *g.Field(&w.selection) != "value" {
			t.Fatalf("read accessor %s", g.Key)
		}
	}
	got[0].Key = "changed"
	got[0].Field = nil
	for _, g := range got {
		g.Profiles[0] = "changed"
	}
	fresh := ModelSettings()
	for i, w := range want {
		if fresh[i].Key != w.key || fresh[i].Field == nil || !reflect.DeepEqual(fresh[i].Profiles, w.profiles) {
			t.Fatalf("catalog mutated at %d", i)
		}
	}
}

func TestEmptyAndEscalatedSelection(t *testing.T) {
	for _, role := range []ModelRole{"", "unknown", ModelRoleDefault, ModelRoleRun, ModelRoleReview, ModelRoleMergeReview, ModelRoleResolver, "rework_escalation"} {
		if (ModelSelection{}).For(role) != "" || (ModelSelection{}).EffortFor(role) != "" {
			t.Fatalf("nonempty fallback for %q", role)
		}
	}
	m := ModelSelection{Base: "base", Effort: "base-effort", RunEffort: "run-effort", ReworkEscalation: "strong"}
	if m.For("") != "base" || m.EffortFor("") != "base-effort" || m.EffortFor(ModelRoleRun) != "run-effort" || m.EffortFor("rework_escalation") != "base-effort" {
		t.Fatal("escalation changed fallback or run effort")
	}
}
