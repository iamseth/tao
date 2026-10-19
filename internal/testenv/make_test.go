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
	SkipIfShort(t)
	for _, mode := range []string{"success", "explicit", "test-failure", "setup-failure", "termination", "build", "contended", "full"} {
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
			// A held stable scratch must be left alone and not shared.
			held := filepath.Join(root, "bin", "test-env")
			if mode == "contended" {
				mustMakeDir(t, held)
				mustWriteMakeFile(t, filepath.Join(held, "owner"), "other run\n", 0600)
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
printf '%s\n' "$PATH" > path
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
				case "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GOCACHE", "GOPATH", "GOMODCACHE", "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "GO_TEST_FLAGS", "TEST_TIER_FLAGS", "SHELL":
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
				switch mode {
				case "build":
					target = "build"
				case "full":
					target = "test-full"
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
					assertMakeTestToolchainPath(t, strings.TrimSuffix(readMakeFile(t, filepath.Join(root, "path")), "\n"), tools)
					args := readMakeFile(t, filepath.Join(root, "arguments"))
					// make test runs the fast tier; test-full runs every test.
					tier := "-short\n"
					if mode == "full" {
						tier = ""
					}
					if args != "test\n"+tier+"-count=1\n-run\nTest With Spaces\n-coverprofile=coverage.out\n./...\n" {
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
						// Only the uncontended stable path keeps the Go test cache valid.
						if stable := root + "/bin/test-env/home"; mode == "contended" {
							if observed[0] == stable || next[0] == stable || next[0] == observed[0] {
								t.Errorf("contended scratch shared: %q then %q", observed[0], next[0])
							}
							if !strings.Contains(out, "will not be cached") {
								t.Errorf("missing contention notice: %s", out)
							}
						} else if observed[0] != stable || next[0] != stable {
							t.Errorf("scratch not stable across invocations: %q then %q", observed[0], next[0])
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
			if mode == "contended" {
				if got := readMakeFile(t, filepath.Join(held, "owner")); got != "other run\n" {
					t.Errorf("held scratch changed: %q", got)
				}
				if len(entries) != 1 || entries[0].Name() != "test-env" {
					t.Errorf("scratch not cleaned: %v", entries)
				}
			} else if len(entries) != 0 {
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

// On a macOS host whose git is the xcrun shim, make test resolves /usr/bin
// tools from the real toolchain directory while earlier entries keep
// precedence; elsewhere PATH is unchanged.
func assertMakeTestToolchainPath(t *testing.T, path, tools string) {
	t.Helper()
	entries := strings.Split(path, string(os.PathListSeparator))
	if entries[0] != tools {
		t.Errorf("PATH lost earlier entry precedence: %q", path)
	}
	shim, err := exec.LookPath("git")
	if err != nil || shim != "/usr/bin/git" {
		return
	}
	out, err := exec.Command("xcrun", "--find", "git").Output()
	if err != nil {
		return
	}
	toolchain := filepath.Dir(strings.TrimSpace(string(out)))
	for i, entry := range entries {
		if entry == "/usr/bin" {
			if i == 0 || entries[i-1] != toolchain {
				t.Errorf("PATH does not resolve /usr/bin tools from %s first: %q", toolchain, path)
			}
			return
		}
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
