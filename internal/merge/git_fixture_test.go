package merge

import (
	"path/filepath"
	"testing"
)

// disableGitFixtureMaintenance persists in the common repository config so
// production Git clients and linked worktrees inherit the same suppression.
func disableGitFixtureMaintenance(t *testing.T, root string) {
	t.Helper()
	runRealGit(t, root, "config", "--local", "maintenance.auto", "false")
	runRealGit(t, root, "config", "--local", "gc.auto", "0")
}

func TestRealGitFixtureDisablesAutomaticMaintenance(t *testing.T) {
	t.Parallel()
	fixture := newRealGitWorktree(t)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/maintenance", integrationRoot, fixture.defaultBranch)

	for name, root := range map[string]string{
		"main":        fixture.repoRoot,
		"plan":        fixture.worktreePath,
		"integration": integrationRoot,
	} {
		t.Run(name, func(t *testing.T) {
			for key, want := range map[string]string{"maintenance.auto": "false", "gc.auto": "0"} {
				// Query both persisted local and effective config without command overrides.
				for _, scope := range [][]string{{"--local"}, nil} {
					args := append([]string{"config"}, scope...)
					args = append(args, "--get", key)
					if got := realGitOutput(t, root, args...); got != want {
						t.Errorf("git %v = %q, want %q", args, got, want)
					}
				}
			}
		})
	}
}
