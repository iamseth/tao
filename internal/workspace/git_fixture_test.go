package workspace

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func assertGitMaintenanceDisabled(t *testing.T, dir string) {
	t.Helper()
	for _, setting := range []struct{ key, value string }{
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
	} {
		if got := strings.TrimSpace(runGit(t, dir, "config", "--get", setting.key)); got != setting.value {
			t.Errorf("git config %s in %s = %q, want %q", setting.key, dir, got, setting.value)
		}
	}
}

func TestNewTestRepoDisablesMaintenance(t *testing.T) {
	testenv.SkipIfShort(t)
	t.Parallel()
	repo := newTestRepo(t)
	assertGitMaintenanceDisabled(t, repo.path)
}
