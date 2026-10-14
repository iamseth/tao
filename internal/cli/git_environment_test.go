package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCLISuiteCleanup(t *testing.T) {
	for _, code := range []int{0, 7} {
		var order []int
		got := cleanupCLISuite(code, []func() error{
			func() error { order = append(order, 1); return nil },
			func() error { order = append(order, 2); return errors.New("injected cleanup failure") },
			func() error { order = append(order, 3); return nil },
		})
		want := code
		if want == 0 {
			want = 1
		}
		if got != want || !slices.Equal(order, []int{3, 2, 1}) {
			t.Fatalf("cleanup = %d, %v", got, order)
		}
	}
	if got := cleanupCLISuite(0, []func() error{func() error { return nil }}); got != 0 {
		t.Fatal(got)
	}
}

func TestCLIGitEnvironmentChildCleanup(t *testing.T) {
	const key = "CLI_GIT_ENV_REPORT"
	if report := os.Getenv(key); report != "" {
		pi, err := exec.LookPath("pi")
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("GIT_CONFIG_GLOBAL"), suiteTaoDataHome, filepath.Dir(pi)}
		if err := os.WriteFile(report, []byte(strings.Join(paths, "\n")), 0600); err != nil { // #nosec G703 -- parent supplies a private test report path.
			t.Fatal(err)
		}
		return
	}
	report := filepath.Join(t.TempDir(), "report")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestCLIGitEnvironmentChildCleanup$") // #nosec G204 -- current test executable with fixed test selector.
	cmd.Env = append(os.Environ(), key+"="+report)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v: %s", err, out)
	}
	data, err := os.ReadFile(report) // #nosec G304 -- fixed report filename in this test's private temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Split(string(data), "\n")
	if len(paths) != 5 {
		t.Fatalf("child paths: %q", data)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) { // #nosec G703 -- read-only cleanup check of child-owned temporary paths.
			t.Fatalf("child resource remains %q: %v", path, err)
		}
	}
}

func TestCLIGitEnvironment(t *testing.T) {
	home, xdg, config := os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("GIT_CONFIG_GLOBAL")
	if home == "" || xdg == "" || config == "" || os.Getenv("GIT_CONFIG_NOSYSTEM") != "1" {
		t.Fatal("missing shared Git environment")
	}
	for _, path := range []string{home, xdg, config} {
		if filepath.Dir(path) != filepath.Dir(home) || path == suiteTaoDataHome {
			t.Fatalf("non-isolated suite path %q", path)
		}
		if _, err := os.Stat(path); err != nil { // #nosec G703 -- read-only check of suite-owned paths.
			t.Fatal(err)
		}
	}
	for key, want := range map[string]string{"gc.auto": "0", "maintenance.auto": "false", "commit.gpgsign": "false", "init.defaultBranch": "main"} {
		cmd := exec.Command("git", "config", "--global", "--get", key) // #nosec G204 -- fixed test configuration keys, no shell.
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("inherited child Git %s = %q, %v; want %s", key, out, err, want)
		}
	}
}
