// Package identity resolves the human identity Tao records for actions such as
// slice approvals. The OS/user and environment lookups are injectable for
// testing.
package identity

import (
	"os"
	"os/user"
	"strings"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

// Approver resolves the approver identity to record for an approval action. It
// prefers the OS user's display name, then login name, then the
// TAO_APPROVED_BY/USER/USERNAME environment variables, and returns "" when none
// resolve so callers can require an explicit approver.
func Approver() string {
	return approver(user.Current, os.Getenv)
}

// approver is the testable core of Approver with the OS lookups injected.
func approver(currentUser func() (*user.User, error), getenv func(string) string) string {
	if current, err := currentUser(); err == nil && current != nil {
		if name := strings.TrimSpace(current.Name); name != "" {
			return name
		}
		if username := strings.TrimSpace(current.Username); username != "" {
			return username
		}
	}
	for _, key := range []string{runtimeconfig.EnvApprovedBy, "USER", "USERNAME"} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
