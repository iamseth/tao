// Package perm defines the shared permission vocabulary for stream-json agents.
package perm

// PermissionMode is the permission policy accepted by stream-json agent clients.
type PermissionMode string

const (
	PermissionModeAuto              PermissionMode = "auto"
	PermissionModePlan              PermissionMode = "plan"
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
)

// Valid reports whether mode is one of Tao's supported permission policies.
func Valid(mode PermissionMode) bool {
	switch mode {
	case PermissionModeAuto, PermissionModePlan, PermissionModeBypassPermissions:
		return true
	default:
		return false
	}
}
