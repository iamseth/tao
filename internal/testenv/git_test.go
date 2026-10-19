package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var keys = []string{"HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- arguments supplied exclusively by test fixtures; no shell.
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSetupGit(t *testing.T) {
	SkipIfShort(t)
	hostile := t.TempDir()
	config := "[gc]\n auto = 987\n[maintenance]\n auto = true\n[commit]\n gpgsign = true\n[init]\n defaultBranch = hostile\n"
	for _, name := range []string{".gitconfig", "system", "global", "git/config"} {
		path := filepath.Join(hostile, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range keys {
		t.Setenv(key, hostile)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(hostile, "global"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(hostile, "system"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	before := map[string]string{}
	for _, key := range keys {
		before[key] = os.Getenv(key)
	}
	cleanup, err := SetupGit()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	home := os.Getenv("HOME")
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL"} {
		path := os.Getenv(key)
		if path == before[key] {
			t.Fatalf("%s not isolated", key)
		}
		info, err := os.Stat(path) // #nosec G703 -- environment paths were just created by SetupGit.
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("%s permissions %v", key, info.Mode())
		}
	}
	root := t.TempDir()
	git(t, root, "init")
	if got := git(t, root, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatal(got)
	}
	git(t, root, "config", "user.name", "Fixture")
	git(t, root, "config", "user.email", "fixture@example.invalid")
	git(t, root, "commit", "--allow-empty", "-m", "initial")
	bare := filepath.Join(t.TempDir(), "bare")
	git(t, root, "init", "--bare", bare)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "--detach", linked)
	for _, dir := range []string{root, bare, linked} {
		for key, want := range map[string]string{"gc.auto": "0", "maintenance.auto": "false", "commit.gpgsign": "false", "init.defaultBranch": "main"} {
			if got := git(t, dir, "config", "--get", key); got != want {
				t.Errorf("%s %s = %s", dir, key, got)
			}
		}
	}
	t.Run("intentional override", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(hostile, "global"))
		if got := git(t, root, "config", "--get", "gc.auto"); got != "987" {
			t.Fatal(got)
		}
	})
	if got := git(t, root, "config", "--get", "gc.auto"); got != "0" {
		t.Fatal(got)
	}
	t.Run("discarded global fallback", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
		DisableGitMaintenance(t, bare)
		if got := git(t, bare, "config", "--local", "--get", "gc.auto"); got != "0" {
			t.Fatal(got)
		}
		if got := git(t, bare, "config", "--local", "--get", "maintenance.auto"); got != "false" {
			t.Fatal(got)
		}
	})
	for i := 0; i < 2; i++ {
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) { // #nosec G703 -- home is the private directory created by SetupGit.
		t.Fatalf("home remains: %v", err)
	}
	for _, key := range keys {
		if os.Getenv(key) != before[key] {
			t.Errorf("%s not restored", key)
		}
	}
	for _, name := range []string{".gitconfig", "system", "global", "git/config"} {
		data, err := os.ReadFile(filepath.Join(hostile, name)) // #nosec G304 -- fixed filenames within this test's temporary directory.
		if err != nil || string(data) != config {
			t.Fatalf("host config changed: %s %v", name, err)
		}
	}
}

func TestSetupGitRestoresUnset(t *testing.T) {
	for _, key := range keys {
		t.Setenv(key, "saved")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	cleanup, err := SetupGit()
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if _, ok := os.LookupEnv(key); ok {
			t.Errorf("%s remains set", key)
		}
	}
}

func TestSetupGitPartialFailure(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	before := os.Environ()
	// Fail after creating private paths and changing one environment key.
	calls := 0
	cleanup, err := setupGit(func(key, value string) error {
		calls++
		if calls == 2 {
			return os.ErrPermission
		}
		return os.Setenv(key, value)
	})
	if err == nil {
		if cleanup != nil {
			_ = cleanup()
		}
		t.Fatal("expected failure")
	}
	for _, entry := range before {
		key, value, _ := strings.Cut(entry, "=")
		if os.Getenv(key) != value {
			t.Errorf("%s changed", key)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("owned paths leaked")
	}
}
