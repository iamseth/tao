// Package testenv provides test-only process environment setup.
package testenv

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// SetupGit isolates home and Git configuration for a test process. Call before
// m.Run, never concurrently with tests; call cleanup before os.Exit. The global
// config supplies a default identity (Tao Test <tao@example.invalid>) so fixtures
// need not spend Git processes configuring one; fixtures that assert a
// distinct identity set it locally. Explicit per-test overrides remain possible.
func SetupGit() (cleanup func() error, err error) {
	return setupGit(os.Setenv)
}

func setupGit(setenv func(string, string) error) (func() error, error) {
	root, err := os.MkdirTemp("", "tao-test-git-")
	if err != nil {
		return nil, err
	}
	type saved struct {
		key, value string
		present    bool
	}
	var changed []saved
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			for i := len(changed) - 1; i >= 0; i-- {
				old := changed[i]
				var err error
				if old.present {
					err = os.Setenv(old.key, old.value)
				} else {
					err = os.Unsetenv(old.key)
				}
				cleanupErr = errors.Join(cleanupErr, err)
			}
			cleanupErr = errors.Join(cleanupErr, os.RemoveAll(root))
		})
		return cleanupErr
	}
	fail := func(err error) (func() error, error) { return nil, errors.Join(err, cleanup()) }
	home, xdg, config := filepath.Join(root, "home"), filepath.Join(root, "xdg"), filepath.Join(root, "gitconfig")
	for _, dir := range []string{home, xdg} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return fail(err)
		}
	}
	const defaults = "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n[user]\n\tname = Tao Test\n\temail = tao@example.invalid\n"
	if err := os.WriteFile(config, []byte(defaults), 0600); err != nil {
		return fail(err)
	}
	values := map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": xdg, "GIT_CONFIG_GLOBAL": config, "GIT_CONFIG_NOSYSTEM": "1",
	}
	// Keep read keys literal so the runtime environment boundary guard can
	// verify that this test bootstrap does not consume runtime settings.
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
		value, present := os.LookupEnv(key)
		changed = append(changed, saved{key, value, present})
		if err := setenv(key, values[key]); err != nil {
			return fail(err)
		}
	}
	return cleanup, nil
}

// DisableGitMaintenance is a repository-local fallback only for fixture
// subprocesses that intentionally discard SetupGit's environment.
func DisableGitMaintenance(t testing.TB, root string) {
	t.Helper()
	for _, setting := range []struct{ key, value string }{{"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		cmd := exec.Command("git", "-C", root, "config", "--local", setting.key, setting.value) // #nosec G204 -- fixture root and fixed settings supplied by tests; no shell.
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(fmt.Errorf("disable fixture maintenance: %w: %s", err, out))
		}
	}
}
