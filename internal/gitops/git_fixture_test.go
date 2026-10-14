package gitops

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func assertGitFixtureConfig(t *testing.T, root, maintenance, gc string) {
	t.Helper()
	for _, setting := range []struct{ key, want string }{
		{"maintenance.auto", maintenance},
		{"gc.auto", gc},
	} {
		if got := gitOutput(t, root, "config", "--get", setting.key); got != setting.want {
			t.Errorf("%s config %s = %q, want %q", root, setting.key, got, setting.want)
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
			// Distinct local values expose accidentally configuring the launch repo.
			runGitCommand(t, launch, "config", "--local", "maintenance.auto", "true")
			runGitCommand(t, launch, "config", "--local", "gc.auto", "123")
			destination := filepath.Join(t.TempDir(), "destination")
			args := []string{"init", "-b", "main"}
			if bare {
				args = append(args, "--bare")
			}
			runGitCommand(t, launch, append(args, destination)...)
			assertGitFixtureConfig(t, destination, "false", "0")
			for _, key := range []string{"gc.auto", "maintenance.auto"} {
				cmd := exec.Command("git", "-C", destination, "config", "--local", "--get", key) // #nosec G204 -- temporary fixture destination and fixed config keys; no shell.
				if out, err := cmd.CombinedOutput(); err == nil || len(out) != 0 {
					t.Fatalf("destination unexpectedly has local %s: %s (%v)", key, out, err)
				}
			}
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
	// Local overrides belong to the common repository, not the suite config.
	runGitCommand(t, root, "config", "--local", "gc.auto", "123")
	assertGitFixtureConfig(t, linked, "false", "123")
	other := t.TempDir()
	runGitCommand(t, other, "init")
	assertGitFixtureConfig(t, other, "false", "0")
}
