package testenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real recipe without recursively running the Go test suite.
func TestMakeTestEnvironment(t *testing.T) {
	for _, mode := range []string{"success", "explicit", "test-failure", "setup-failure", "termination", "build"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "fixture with spaces")
			mustMakeDir(t, root)
			root, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile("../../Makefile")
			if err != nil {
				t.Fatal(err)
			}
			mustWriteMakeFile(t, filepath.Join(root, "Makefile"), string(data), 0600)
			tools := filepath.Join(root, "tools")
			home := filepath.Join(root, "developer home")
			xdg := filepath.Join(root, "developer config")
			for _, dir := range []string{tools, home, xdg} {
				mustMakeDir(t, dir)
			}
			sentinel := filepath.Join(home, ".gitconfig")
			mustWriteMakeFile(t, sentinel, "hostile developer config\n", 0600)
			mustWriteMakeFile(t, filepath.Join(xdg, "sentinel"), "untouched\n", 0600)
			mustWriteMakeFile(t, filepath.Join(tools, "go"), `#!/bin/sh
set -eu
if [ "$1" = env ]; then
 [ "$HOME" = "$ORIGINAL_HOME" ] || exit 81
 [ "$XDG_CONFIG_HOME" = "$ORIGINAL_XDG" ] || exit 82
 [ "$GIT_CONFIG_GLOBAL" = "$ORIGINAL_HOME/.gitconfig" ] || exit 83
 [ "$MODE" != setup-failure ] || exit 42
 case "$2" in
 GOCACHE) printf '%s\n' "${GOCACHE:-$HOME/default cache}" ;;
 GOPATH) printf '%s\n' "${GOPATH:-$HOME/default path}" ;;
 GOMODCACHE) printf '%s\n' "${GOMODCACHE:-${GOPATH:-$HOME/default path}/pkg/mod}" ;;
 *) exit 84 ;;
 esac
 exit
fi
printf '%s\n' "$HOME" "$XDG_CONFIG_HOME" "$GIT_CONFIG_GLOBAL" "$GIT_CONFIG_NOSYSTEM" "${GOCACHE-}" "${GOPATH-}" "${GOMODCACHE-}" > observed
printf '%s\n' "$@" > arguments
[ -d "$HOME" ] && [ -d "$XDG_CONFIG_HOME" ] || exit 85
case "$MODE" in
 test-failure) exit 43 ;;
 termination) kill -TERM "$PPID"; exit 0 ;;
esac
if [ "$1" = test ]; then
 printf 'mode: set\nexample/z/file.go:1.1,2.2 2 1\nexample/a/file.go:1.1,2.2 2 0\n' > coverage.out
fi
`, 0700)
			env := []string{}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				switch key {
				case "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GOCACHE", "GOPATH", "GOMODCACHE", "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "GO_TEST_FLAGS", "SHELL":
					continue
				}
				env = append(env, entry)
			}
			env = append(env, "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+home, "XDG_CONFIG_HOME="+xdg, "GIT_CONFIG_GLOBAL="+sentinel, "GIT_CONFIG_NOSYSTEM=0", "ORIGINAL_HOME="+home, "ORIGINAL_XDG="+xdg, "MODE="+mode)
			wantPaths := []string{home + "/default cache", home + "/default path", home + "/default path/pkg/mod"}
			if mode == "explicit" {
				wantPaths = []string{root + "/explicit cache", root + "/explicit path", root + "/explicit modules"}
				for i, key := range []string{"GOCACHE", "GOPATH", "GOMODCACHE"} {
					env = append(env, key+"="+wantPaths[i])
				}
			}
			run := func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				target := "test"
				if mode == "build" {
					target = "build"
				}
				cmd := exec.CommandContext(ctx, "make", target, "GO_TEST_FLAGS=-count=1 -run 'Test With Spaces'")
				cmd.Dir, cmd.Env = root, env
				cmd.WaitDelay = time.Second
				out, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("make timed out: %s", out)
				}
				return string(out), err
			}
			out, err := run()
			failed := mode == "test-failure" || mode == "setup-failure" || mode == "termination"
			if (err != nil) != failed {
				t.Fatalf("make error = %v, want failure %v: %s", err, failed, out)
			}
			if mode != "setup-failure" {
				observed := strings.Split(strings.TrimSuffix(readMakeFile(t, filepath.Join(root, "observed")), "\n"), "\n")
				if len(observed) != 7 {
					t.Fatalf("observed: %q", observed)
				}
				if mode == "build" {
					if observed[0] != home || observed[1] != xdg || observed[2] != sentinel || observed[3] != "0" {
						t.Fatalf("build environment changed: %q", observed)
					}
				} else {
					for _, path := range observed[:2] {
						if !filepath.IsAbs(path) || !strings.HasPrefix(path, root+"/bin/") {
							t.Errorf("not absolute scratch path: %q", path)
						}
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Errorf("scratch remains: %s (%v)", path, err)
						}
					}
					if observed[0] == observed[1] || observed[2] != "/dev/null" || observed[3] != "1" {
						t.Errorf("isolation environment: %q", observed)
					}
					for i, want := range wantPaths {
						if observed[i+4] != want {
							t.Errorf("Go path = %q, want %q", observed[i+4], want)
						}
					}
					args := readMakeFile(t, filepath.Join(root, "arguments"))
					if args != "test\n-count=1\n-run\nTest With Spaces\n-coverprofile=coverage.out\n./...\n" {
						t.Errorf("arguments: %q", args)
					}
					if !failed {
						if !strings.Contains(out, "   0.0%  example/a\n 100.0%  example/z\n") {
							t.Errorf("summary: %q", out)
						}
						if out, err := run(); err != nil {
							t.Fatalf("second invocation: %v: %s", err, out)
						}
						next := strings.Split(readMakeFile(t, filepath.Join(root, "observed")), "\n")
						if next[0] == observed[0] {
							t.Error("scratch reused across invocations")
						}
					} else if strings.Contains(out, "example/a") {
						t.Errorf("summary ran after failure: %s", out)
					}
				}
			} else if _, err := os.Stat(filepath.Join(root, "observed")); !os.IsNotExist(err) {
				t.Errorf("test ran after setup failure: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "bin"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("scratch not cleaned: %v", entries)
			}
			if got := readMakeFile(t, sentinel); got != "hostile developer config\n" {
				t.Errorf("developer config changed: %q", got)
			}
			if got := readMakeFile(t, filepath.Join(xdg, "sentinel")); got != "untouched\n" {
				t.Errorf("XDG config changed: %q", got)
			}
		})
	}
}

func mustMakeDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}
func mustWriteMakeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil { // #nosec G703 -- paths are test-owned fixtures under t.TempDir, not external input.
		t.Fatal(err)
	}
}
func readMakeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- paths are test-owned fixture outputs under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
