package theme

import "hash/fnv"

// Role identifies a semantic palette slot.
type Role uint8

// Semantic color roles select shared palette slots for CLI and TUI rendering.
const (
	RoleAccent Role = iota
	RoleWarn
	RoleSuccess
	RoleRepo
	RoleRepoSelected
	RoleInfo
	RolePlanNow
	RolePlanNext
	RolePlanHistory
	RolePlanNowBackground
	RolePlanNextBackground
	RolePlanHistoryBackground
	RolePlanHistoryText
	RolePlanSelectionBackground
	RolePlanSelectionText
	RoleSettingsSection
	RoleDebugSection
	RoleDetailBackground
	RoleDetailPrimary
	RoleDetailSecondary
	RoleDetailMuted
	RoleDetailBody
	RoleDetailSuccess
	RoleDetailInfo
	RoleDetailWarning
	RoleDetailError
	RoleDetailDivider
	RoleNeutral0
	RoleNeutral1
	RoleNeutral2
	RoleNeutral3
	RoleNeutral4
	RoleNeutral5
	RoleSelectionBackground
	roleCount
)

var repoColorRoles = [...]Role{RoleAccent, RoleWarn, RoleSuccess, RoleRepo}

// RepoColor maps a repository ID to one of the fixed hue slots with FNV-1a.
// Returning the role keeps the assignment independent of terminal profile.
func RepoColor(id string) Role {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id))
	return repoColorRoles[hash.Sum32()%uint32(len(repoColorRoles))]
}
