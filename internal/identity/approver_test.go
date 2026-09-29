package identity

import (
	"errors"
	"os/user"
	"testing"
)

func TestApproverFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		current                          *user.User
		err                              error
		approvedBy, user, username, want string
	}{
		{name: "display name", current: &user.User{Name: " Ada Lovelace ", Username: "ada"}, approvedBy: "injected", user: "shell", username: "windows", want: "Ada Lovelace"},
		{name: "login name", current: &user.User{Name: "  ", Username: " ada "}, approvedBy: "injected", user: "shell", username: "windows", want: "ada"},
		{name: "OS error", current: &user.User{Name: "ignored"}, err: errors.New("no user"), approvedBy: " injected ", user: "shell", username: "windows", want: "injected"},
		{name: "nil OS user", approvedBy: " injected ", user: "shell", username: "windows", want: "injected"},
		{name: "empty OS identity", current: &user.User{Name: " ", Username: " "}, approvedBy: " injected ", user: "shell", want: "injected"},
		{name: "USER", approvedBy: "  ", user: " shell ", username: "windows", want: "shell"},
		{name: "USERNAME", user: "  ", username: " windows ", want: "windows"},
		{name: "empty", err: errors.New("no user")},
		{name: "whitespace", current: &user.User{Name: " ", Username: " "}, approvedBy: " ", user: " ", username: " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TAO_APPROVED_BY", "changed-after-capture")
			got := approver(tc.approvedBy,
				func() (*user.User, error) { return tc.current, tc.err },
				func(key string) string {
					switch key {
					case "USER":
						return tc.user
					case "USERNAME":
						return tc.username
					default:
						t.Fatalf("unexpected environment lookup: %s", key)
						return ""
					}
				},
			)
			if got != tc.want {
				t.Fatalf("approver = %q, want %q", got, tc.want)
			}
		})
	}
}
