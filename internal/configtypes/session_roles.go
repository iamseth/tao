package configtypes

// SessionRole identifies an operation independently of model fallback roles.
// Catalog metadata is presentation and configuration, never execution authority.
type SessionRole string

type SessionRoleInfo struct {
	Role          SessionRole
	ModelRole     ModelRole
	TelemetryRole string
	CaptureRole   string
}

var sessionRoles = [...]SessionRoleInfo{
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

// SessionRoles returns an independent catalog in operation order.
func SessionRoles() []SessionRoleInfo {
	return append([]SessionRoleInfo(nil), sessionRoles[:]...)
}

func LookupSessionRole(role SessionRole) (SessionRoleInfo, bool) {
	for _, info := range sessionRoles {
		if info.Role == role {
			return info, true
		}
	}
	return SessionRoleInfo{}, false
}
