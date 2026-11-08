package plan

// IsKnownBlockerCategory recognizes diagnostic labels, never ownership or recovery authority.
func IsKnownBlockerCategory(category string) bool {
	switch category {
	case "contract_conflict", "invalid_verification_command", "verification_environment", "out_of_scope_gate_failure":
		return true
	default:
		return false
	}
}
