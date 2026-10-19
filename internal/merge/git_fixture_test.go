package merge

import (
	"path/filepath"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func assertEffectiveGitSettings(t *testing.T, root string) {
	t.Helper()
	for key, want := range map[string]string{
		"maintenance.auto": "false", "gc.auto": "0",
		"commit.gpgsign": "false", "init.defaultBranch": "main",
	} {
		if got := realGitOutput(t, root, "config", "--get", key); got != want {
			t.Errorf("%s: %s = %q, want %q", root, key, got, want)
		}
	}
}

func TestRealGitFixtureDisablesAutomaticMaintenance(t *testing.T) {
	testenv.SkipIfShort(t)
	t.Parallel()
	fixture := newRealGitWorktree(t)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/maintenance", integrationRoot, fixture.defaultBranch)

	agentRoot, _ := singleMergeAgentTestBoundary(t)
	for name, root := range map[string]string{
		"main":        fixture.repoRoot,
		"plan":        fixture.worktreePath,
		"integration": integrationRoot,
		"agent":       agentRoot,
	} {
		t.Run(name, func(t *testing.T) {
			assertEffectiveGitSettings(t, root)
		})
	}
}
