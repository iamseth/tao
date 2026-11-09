package intervention

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	pairs := map[string]string{
		"operator":  "run_resume run_continue run_restart edit_amend edit_remove edit_skip edit_move approve rework_manual rework_force rework_pr rework_restart verification_repair_requested verification_reverify merge_restart merge_force merge_resolution_resume",
		"automatic": "run_context transport_retry timeout_continue rework rework_stop verification_repair verification_repair_stop finalization_recovery merge_resolution merge_rollback merge_rearm merge_complete",
	}
	for actor, triggers := range pairs {
		for _, trigger := range strings.Fields(triggers) {
			a, b := Normalize(actor, trigger)
			if a != actor || b != trigger {
				t.Fatalf("Normalize(%q,%q) = %q,%q", actor, trigger, a, b)
			}
			other := "operator"
			if actor == other {
				other = "automatic"
			}
			a, b = Normalize(other, trigger)
			if a != "unknown" || b != "unknown" {
				t.Fatal("mismatched pair accepted")
			}
		}
	}
	for _, pair := range [][2]string{{"", ""}, {"unknown", "unknown"}, {"operator", "unknown"}, {"unknown", "merge_force"}, {"operator", "run"}, {"Operator", "approve"}, {"operator", " approve"}, {strings.Repeat("x", 10000), "approve"}, {"operator", strings.Repeat("x", 10000)}} {
		a, b := Normalize(pair[0], pair[1])
		if a != "unknown" || b != "unknown" {
			t.Fatal("unsupported input accepted")
		}
	}
}
