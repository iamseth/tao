package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGitEnvironment(t *testing.T) {
	const childKey = "TAO_TEST_GIT_ENV_CHILD"
	if os.Getenv(childKey) == "1" {
		if os.Getenv("TAO_TEST_GIT_ENV_SENTINEL") != "retained" || os.Getenv("TAO_TEST_COMPLETION_DIR") != "coordination-sentinel" {
			t.Fatal("bootstrap lost unrelated child environment")
		}
		if os.Getenv("HOME") == os.Getenv("TAO_TEST_GIT_ENV_PARENT_HOME") {
			t.Fatal("re-executed binary did not isolate its home")
		}
		for key, want := range map[string]string{"gc.auto": "0", "maintenance.auto": "false", "commit.gpgsign": "false", "init.defaultBranch": "main"} {
			out, err := exec.Command("git", "config", "--global", "--get", key).CombinedOutput() // #nosec G204 -- fixed test configuration keys, no shell.
			if err != nil || strings.TrimSpace(string(out)) != want {
				t.Fatalf("child Git %s = %q, %v; want %q", key, out, err, want)
			}
		}
		if err := os.WriteFile(os.Getenv("TAO_TEST_GIT_ENV_REPORT"), []byte(os.Getenv("GIT_CONFIG_GLOBAL")), 0600); err != nil { // #nosec G703 -- report path supplied by the parent test inside t.TempDir.
			t.Fatal(err)
		}
		return
	}
	report := filepath.Join(t.TempDir(), "config-path")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRunGitEnvironment$") // #nosec G204 -- re-execute this test binary with a fixed test selector.
	cmd.Env = append(os.Environ(), childKey+"=1", "TAO_TEST_GIT_ENV_SENTINEL=retained", "TAO_TEST_COMPLETION_DIR=coordination-sentinel", "TAO_TEST_GIT_ENV_PARENT_HOME="+os.Getenv("HOME"), "TAO_TEST_GIT_ENV_REPORT="+report)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	config, err := os.ReadFile(report) // #nosec G304 -- report is a fixed filename inside this test's temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(string(config))); !os.IsNotExist(err) { // #nosec G703 -- child reports only its SetupGit-owned temporary config; read-only cleanup check.
		t.Fatalf("child bootstrap directory remains: %v", err)
	}
	if _, err := os.Stat(os.Getenv("GIT_CONFIG_GLOBAL")); err != nil { // #nosec G703 -- TestMain sets this to its SetupGit-owned config; read-only existence check.
		t.Fatalf("child removed parent config: %v", err)
	}
}
