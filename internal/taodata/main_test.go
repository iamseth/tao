package taodata

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func TestMain(m *testing.M) {
	cleanup, err := testenv.SetupGit()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func TestGitEnvironment(t *testing.T) {
	home, xdg, config := os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("GIT_CONFIG_GLOBAL")
	if home == "" || xdg == "" || config == "" || os.Getenv("GIT_CONFIG_NOSYSTEM") != "1" {
		t.Fatal("shared Git bootstrap environment is missing")
	}
	root := filepath.Dir(config)
	if home != filepath.Join(root, "home") || xdg != filepath.Join(root, "xdg") {
		t.Fatal("home paths are not isolated alongside the generated Git config")
	}
	for _, path := range []string{home, xdg} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() { // #nosec G703 -- read-only check of suite-owned bootstrap directories.
			t.Fatalf("isolated directory %s: %v", path, err)
		}
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...) // #nosec G204 -- fixed test arguments, no shell.
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	for key, want := range map[string]string{
		"gc.auto": "0", "maintenance.auto": "false", "commit.gpgsign": "false", "init.defaultBranch": "main",
	} {
		if got := git("config", "--get", key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := git("symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("default branch = %q, want main", got)
	}
}
