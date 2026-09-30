package run

import "testing"

// Configure immediately after init so all later Git clients and linked worktrees
// use persistent repository-local settings before any maintenance-triggering work.
func disableRunGitMaintenance(t *testing.T, root string) {
	t.Helper()
	runRebaseRecoveryGit(t, root, "config", "--local", "maintenance.auto", "false")
	runRebaseRecoveryGit(t, root, "config", "--local", "gc.auto", "0")
}

func TestRunGitFixturesDisableAutomaticMaintenance(t *testing.T) {
	t.Run("recovery", func(t *testing.T) {
		repo, _, _, _ := newRebaseRecoveryRepo(t, false)
		assertRunGitMaintenanceDisabled(t, repo)
	})
	t.Run("linked recovery", func(t *testing.T) {
		repo, worktree, _, _, _ := newLinkedRebaseRecoveryRepo(t)
		assertRunGitMaintenanceDisabled(t, repo)
		assertRunGitMaintenanceDisabled(t, worktree)
	})
	t.Run("commit completion", func(t *testing.T) {
		assertRunGitMaintenanceDisabled(t, initSliceCompletionRepo(t))
	})
	t.Run("lifecycle", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, "maintenance-test")
		assertRunGitMaintenanceDisabled(t, fixture.repoRoot)
	})
	t.Run("pull request and bare origin", func(t *testing.T) {
		fixture := newPullRequestOrchestrationFixture(t)
		assertRunGitMaintenanceDisabled(t, fixture.repoRoot)
		assertRunGitMaintenanceDisabled(t, fixture.worktreeRoot)
		assertRunGitMaintenanceDisabled(t, fixture.originRoot)
	})
}

func assertRunGitMaintenanceDisabled(t *testing.T, root string) {
	t.Helper()
	for _, setting := range []struct{ key, value string }{
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
	} {
		// Separate Git processes check both effective and persisted local values.
		for _, scope := range []string{"--local", "--includes"} {
			got := rebaseRecoveryGitOutput(t, root, "config", scope, "--get", setting.key)
			if got != setting.value {
				t.Errorf("%s: git config %s %s = %q, want %q", root, scope, setting.key, got, setting.value)
			}
		}
	}
}
