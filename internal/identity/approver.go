// Package identity resolves the human identity Tao records for actions such as
// slice approvals. The OS/user and environment lookups are injectable for
// testing.
package identity

import (
	"os"
	"os/user"
	"strings"
)

// Approver resolves the approver identity to record for an approval action. It
// prefers the OS user's display name, then login name, then the
// captured approvedBy value, then USER/USERNAME environment variables, and
// returns "" when none resolve so callers can require an explicit approver.
func Approver(approvedBy string) string {
	return approver(approvedBy, user.Current, os.Getenv)
}

// approver is the testable core of Approver with the OS lookups injected.
func approver(approvedBy string, currentUser func() (*user.User, error), getenv func(string) string) string {
	if current, err := currentUser(); err == nil && current != nil {
		if name := strings.TrimSpace(current.Name); name != "" {
			return name
		}
		if username := strings.TrimSpace(current.Username); username != "" {
			return username
		}
	}
	if value := strings.TrimSpace(approvedBy); value != "" {
		return value
	}
	for _, key := range []string{"USER", "USERNAME"} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
