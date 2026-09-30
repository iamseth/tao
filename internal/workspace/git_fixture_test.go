package workspace

import (
	"strings"
	"testing"
)

func disableGitMaintenance(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "--local", "maintenance.auto", "false")
	runGit(t, dir, "config", "--local", "gc.auto", "0")
}

func assertGitMaintenanceDisabled(t *testing.T, dir string) {
	t.Helper()
	for _, setting := range []struct{ key, value string }{
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
	} {
		for _, scope := range [][]string{nil, {"--local"}} {
			args := append([]string{"config"}, scope...)
			args = append(args, "--get", setting.key)
			if got := strings.TrimSpace(runGit(t, dir, args...)); got != setting.value {
				t.Errorf("git config %v %s in %s = %q, want %q", scope, setting.key, dir, got, setting.value)
			}
		}
	}
}

func TestNewTestRepoDisablesMaintenance(t *testing.T) {
	repo := newTestRepo(t)
	assertGitMaintenanceDisabled(t, repo.path)
}
