// Package intervention encodes bounded diagnostic attribution. Its values and
// coverage markers never authorize lifecycle transitions or recovery.
package intervention

// Normalize recognizes only version 1 actor/trigger pairs, without retaining
// unsupported input. Unknown attribution is incomplete, not evidence of absence.
func Normalize(actor, trigger string) (string, string) {
	if len(actor) > 9 || len(trigger) > 32 {
		return "unknown", "unknown"
	}
	switch actor {
	case "operator":
		switch trigger {
		case "run_resume", "run_continue", "run_restart", "edit_amend", "edit_remove", "edit_skip", "edit_move", "approve", "rework_manual", "rework_force", "rework_pr", "rework_restart", "verification_repair_requested", "verification_reverify", "merge_restart", "merge_force", "merge_resolution_resume":
			return actor, trigger
		}
	case "automatic":
		switch trigger {
		case "run_context", "transport_retry", "timeout_continue", "rework", "rework_stop", "verification_repair", "verification_repair_stop", "finalization_recovery", "merge_resolution", "merge_rollback", "merge_rearm", "merge_complete":
			return actor, trigger
		}
	}
	return "unknown", "unknown"
}
