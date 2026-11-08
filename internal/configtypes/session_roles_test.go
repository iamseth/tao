package configtypes

import (
	"reflect"
	"testing"
)

func TestSessionRoles(t *testing.T) {
	want := []SessionRoleInfo{
		{"run", ModelRoleRun, "execution", "execution"},
		{"rework", ModelRoleRun, "rework", "rework"},
		{"review", ModelRoleReview, "review", "review"},
		{"pr", ModelRoleDefault, "pull_request", "pull_request"},
		{"merge_review", ModelRoleMergeReview, "merge", "merge"},
		{"resolver", ModelRoleResolver, "merge", "merge"},
		{"proposal", ModelRoleDefault, "merge", "merge"},
		{"triage", ModelRoleDefault, "rework", "rework"},
		{"planning", ModelRoleDefault, "planning", "planning"},
	}
	got := SessionRoles()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roles = %#v", got)
	}
	seen := map[SessionRole]bool{}
	for _, info := range want {
		if seen[info.Role] {
			t.Fatalf("duplicate %s", info.Role)
		}
		seen[info.Role] = true
		found, ok := LookupSessionRole(info.Role)
		if !ok || found != info {
			t.Fatalf("lookup %s = %+v, %v", info.Role, found, ok)
		}
	}
	for _, role := range []SessionRole{"", "unknown", "execution"} {
		if info, ok := LookupSessionRole(role); ok || info != (SessionRoleInfo{}) {
			t.Fatalf("unknown lookup = %+v, %v", info, ok)
		}
	}
	got[0].Role = "changed"
	info, _ := LookupSessionRole("run")
	info.CaptureRole = "changed"
	if !reflect.DeepEqual(SessionRoles(), want) {
		t.Fatal("catalog mutated")
	}
}
