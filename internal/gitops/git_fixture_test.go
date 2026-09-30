package gitops

import (
	"path/filepath"
	"testing"
)

// Persist suppression in common repository config so linked worktrees inherit it.
func disableGitFixtureMaintenance(t *testing.T, root string) {
	t.Helper()
	runGitCommand(t, root, "config", "--local", "maintenance.auto", "false")
	runGitCommand(t, root, "config", "--local", "gc.auto", "0")
}

func assertGitFixtureConfig(t *testing.T, root, maintenance, gc string) {
	t.Helper()
	for _, setting := range []struct{ key, want string }{
		{"maintenance.auto", maintenance},
		{"gc.auto", gc},
	} {
		for _, scope := range [][]string{nil, {"--local"}} {
			args := append([]string{"config"}, scope...)
			args = append(args, "--get", setting.key)
			if got := gitOutput(t, root, args...); got != setting.want {
				t.Errorf("%s config %v = %q, want %q", root, args, got, setting.want)
			}
		}
	}
}

func TestGitFixtureExplicitDestinationConfig(t *testing.T) {
	for _, bare := range []bool{false, true} {
		name := "ordinary"
		if bare {
			name = "bare"
		}
		t.Run(name, func(t *testing.T) {
			launch := t.TempDir()
			runGitCommand(t, launch, "init", "-b", "main")
			disableGitFixtureMaintenance(t, launch)
			// Distinct local values expose accidentally configuring the launch repo.
			runGitCommand(t, launch, "config", "--local", "maintenance.auto", "true")
			runGitCommand(t, launch, "config", "--local", "gc.auto", "123")
			destination := filepath.Join(t.TempDir(), "destination")
			args := []string{"init", "-b", "main"}
			if bare {
				args = append(args, "--bare")
			}
			runGitCommand(t, launch, append(args, destination)...)
			disableGitFixtureMaintenance(t, destination)
			assertGitFixtureConfig(t, destination, "false", "0")
			assertGitFixtureConfig(t, launch, "true", "123")
		})
	}
}

func TestGitFixtureMaintenanceConfig(t *testing.T) {
	root, _, _, _ := commitSeriesRepository(t)
	assertGitFixtureConfig(t, root, "false", "0")
	linked := filepath.Join(t.TempDir(), "linked")
	runGitCommand(t, root, "worktree", "add", "--detach", linked, "HEAD")
	assertGitFixtureConfig(t, linked, "false", "0")
}
