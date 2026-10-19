package commandrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/testenv"
)

const cacheEnv = "GOLANGCI_LINT_CACHE"
const testCacheNamespace = "v1-1c1af473682d878505342e160c108ab6c37ee862031b064b336e0d63075da2d9"

//nolint:gosec // G306: fixture tools require private executable permissions.
func setupIdentityTools(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	for name, output := range map[string]string{"go": "go1.26.2", "golangci-lint": "golangci-lint has version 2.1 built with go1.26.2"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

//nolint:gosec // G306: fixture tools require private executable permissions.
func TestVerificationCacheVersionIdentity(t *testing.T) {
	testenv.SkipIfShort(t)
	setupCacheChild(t)
	bin := t.TempDir()
	for name, output := range map[string]string{"go": "go1.26.2", "golangci-lint": "golangci-lint has version 2.1 built with go1.26.2"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	root := t.TempDir()
	ctx := WithVerificationCache(context.Background(), root)
	cache := func() string {
		got, err := cacheChild(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range got.Environment {
			if value, ok := strings.CutPrefix(entry, cacheEnv+"="); ok {
				return value
			}
		}
		t.Fatal("missing cache")
		return ""
	}
	first := cache()
	if filepath.Dir(first) != filepath.Join(root, ".tao", "cache", "golangci-lint") {
		t.Fatalf("not namespaced: %s", first)
	}
	if cache() != first {
		t.Fatal("identical identity not reused")
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\necho go1.27.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if cache() == first {
		t.Fatal("Go change reused cache")
	}
}

type cacheObservation struct {
	Environment []string
	CWD         string
	Args        []string
}

func TestVerificationCacheIdentityFailures(t *testing.T) {
	testenv.SkipIfShort(t)
	// This test covers the production probe deadline that TestMain relaxes.
	previous := verificationIdentityTimeout
	verificationIdentityTimeout = time.Second
	t.Cleanup(func() { verificationIdentityTimeout = previous })
	for _, tool := range []string{"go", "golangci-lint"} {
		t.Run(tool, func(t *testing.T) {
			for _, script := range []string{"missing", "exit 1", "true", "echo malformed", "i=0; while [ $i -lt 9000 ]; do printf x; i=$((i+1)); done", "/bin/sleep 30"} {
				t.Run(script, func(t *testing.T) {
					bin, root := t.TempDir(), t.TempDir()
					for name, output := range map[string]string{"go": "go1.26.2", "golangci-lint": "golangci-lint has version 2.1 built with go1.26.2"} {
						if name != tool {
							if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho '"+output+"'\n"), 0700); err != nil { //nolint:gosec // G306: private executable test fixture.
								t.Fatal(err)
							}
						}
					}
					if script != "missing" {
						if err := os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil { //nolint:gosec // G306: private executable test fixture.
							t.Fatal(err)
						}
					}
					env := []string{"PATH=" + bin, SliceCompletionOwnerEnv + "=secret"}
					ctx := WithVerificationCache(context.Background(), root)
					var previous string
					for range 2 {
						child, err := verificationCacheEnvironmentAt(ctx, root, env)
						if err != nil {
							t.Fatal(err)
						}
						cache := strings.TrimPrefix(child[len(child)-1], cacheEnv+"=")
						if filepath.Dir(cache) != filepath.Join(root, ".tao", "cache", "golangci-lint") || !strings.HasPrefix(filepath.Base(cache), "fresh-") || cache == previous {
							t.Fatalf("unsafe fallback: %q", cache)
						}
						previous = cache
					}
				})
			}
		})
	}
}

func TestVerificationCacheGenericDoesNotProbe(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "probed")
	for _, name := range []string{"go", "golangci-lint"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho probed > \"$PROBE_MARKER\"\n"), 0700); err != nil { //nolint:gosec // G306: private executable test fixture.
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("PROBE_MARKER", marker)
	if err := DefaultLocal(context.Background(), bin, "/bin/sh", []string{"-c", "true"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generic command probed tools: %v", err)
	}
}

func TestVerificationCacheDispatchIdentity(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "cwd with spaces")
	bin := filepath.Join(cwd, "tools")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"go":            `test -z "$TAO_SLICE_COMPLETION_OWNER" || exit 1; test -f marker || exit 1; printf '%s\n' "$GO_ID"`,
		"golangci-lint": `printf 'golangci-lint has version %s built with %s\n' "$LINT_ID" "$BUILD_ID"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil { //nolint:gosec // G306: private executable test fixture.
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "marker"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	identity := func(goID, lintID, buildID string) string {
		return verificationCacheNamespace(context.Background(), cwd, []string{"PATH=tools", "GO_ID=" + goID, "LINT_ID=" + lintID, "BUILD_ID=" + buildID, SliceCompletionOwnerEnv + "=secret"})
	}
	first := identity("go1.26.2", "2.1", "go1.26.2")
	if !strings.HasPrefix(first, "v1-") || len(first) != 67 || identity("go1.26.2", "2.1", "go1.26.2") != first {
		t.Fatalf("unstable identity: %q", first)
	}
	for _, changed := range []string{identity("go1.27.0", "2.1", "go1.26.2"), identity("go1.26.2", "2.2", "go1.26.2"), identity("go1.26.2", "2.1", "go1.27.0"), identity("go1.26.2", "../../unsafe with spaces", "build")} {
		if changed == first || !strings.HasPrefix(changed, "v1-") || filepath.Base(changed) != changed {
			t.Fatalf("unsafe or unchanged key: %q", changed)
		}
	}
	legacy := filepath.Join(root, ".tao", "cache", "golangci-lint", "legacy")
	if _, err := prepareVerificationCache(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{first, identity("go1.27.0", "2.1", "go1.26.2")} {
		if _, err := prepareVerificationCacheNamespace(root, key); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(legacy); err != nil || string(data) != "retain" { //nolint:gosec // G304: legacy entry in a test-owned temporary directory.
		t.Fatalf("legacy changed: %q %v", data, err)
	}
}

func TestVerificationCacheChild(t *testing.T) {
	if os.Getenv("TAO_CACHE_TEST_CHILD") != "1" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if marker := os.Getenv("TAO_CACHE_TEST_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("launched"), 0600); err != nil { //nolint:gosec // G703: helper receives a test-owned temporary marker path.
			panic(err)
		}
	}
	if os.Getenv("TAO_CACHE_TEST_WRITE") == "1" {
		if err := os.WriteFile(filepath.Join(os.Getenv(cacheEnv), "entry"), []byte("cached"), 0600); err != nil { //nolint:gosec // G703: exercise the injected cache in a test-owned temporary root.
			panic(err)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(cacheObservation{os.Environ(), cwd, os.Args[1:]}); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func cacheChild(ctx context.Context, cwd string) (cacheObservation, error) {
	executable, err := os.Executable()
	if err != nil {
		return cacheObservation{}, err
	}
	var stdout, stderr bytes.Buffer
	err = DefaultLocal(ctx, cwd, executable, []string{"-test.run=^TestVerificationCacheChild$", "--", "argument with spaces"}, &stdout, &stderr)
	if err != nil {
		return cacheObservation{}, fmt.Errorf("child: %w: %s", err, stderr.String())
	}
	var got cacheObservation
	err = json.Unmarshal(stdout.Bytes(), &got)
	return got, err
}

func assertCacheChild(t *testing.T, ctx context.Context, cwd, want string) {
	t.Helper()
	if _, managed := ctx.Value(verificationCacheKey{}).(verificationCachePolicy); managed {
		want = filepath.Join(want, testCacheNamespace)
	}
	got, err := cacheChild(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}
	var caches []string
	for _, entry := range got.Environment {
		if strings.HasPrefix(entry, cacheEnv+"=") {
			caches = append(caches, entry)
		}
		if strings.HasPrefix(entry, SliceCompletionOwnerEnv+"=") {
			t.Fatal("completion owner token leaked")
		}
	}
	if len(caches) != 1 || caches[0] != cacheEnv+"="+want {
		t.Fatalf("cache entries = %q, want exactly %q", caches, want)
	}
	bytecode, present := os.LookupEnv("PYTHONDONTWRITEBYTECODE")
	if _, gate := ctx.Value(verificationCacheKey{}).(verificationCachePolicy); gate && !present {
		bytecode, present = "1", true
	}
	var bytecodeEntries []string
	for _, entry := range got.Environment {
		if strings.HasPrefix(entry, "PYTHONDONTWRITEBYTECODE=") {
			bytecodeEntries = append(bytecodeEntries, entry)
		}
	}
	var wantBytecode []string
	if present {
		wantBytecode = []string{"PYTHONDONTWRITEBYTECODE=" + bytecode}
	}
	if !slices.Equal(bytecodeEntries, wantBytecode) {
		t.Fatalf("bytecode entries = %q, want %q", bytecodeEntries, wantBytecode)
	}
	if !slices.Contains(got.Environment, "TAO_CACHE_TEST_KEEP=kept value") {
		t.Fatal("unrelated environment lost")
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got.CWD != resolved || len(got.Args) != 3 || got.Args[2] != "argument with spaces" {
		t.Fatalf("command CWD/args changed: %+v", got)
	}
}

func setupCacheChild(t *testing.T) {
	t.Helper()
	setupIdentityTools(t)
	t.Setenv("TAO_CACHE_TEST_CHILD", "1")
	t.Setenv("TAO_CACHE_TEST_KEEP", "kept value")
	t.Setenv(SliceCompletionOwnerEnv, "must-not-leak")
	t.Setenv(cacheEnv, "inherited shared cache")
}

func TestVerificationCacheContextsAndReuse(t *testing.T) {
	testenv.SkipIfShort(t)
	setupCacheChild(t)
	parent := context.Background()
	root := filepath.Join(t.TempDir(), "execution root with spaces")
	subdir := filepath.Join(root, "subdirectory")
	if err := os.MkdirAll(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	ctx := WithVerificationCache(parent, root)
	sibling := WithVerificationCache(parent, other)
	if _, err := os.Lstat(filepath.Join(root, ".tao")); !os.IsNotExist(err) {
		t.Fatalf("context construction prepared storage: %v", err)
	}
	for range 2 {
		assertCacheChild(t, ctx, subdir, filepath.Join(root, ".tao", "cache", "golangci-lint"))
		assertCacheChild(t, sibling, other, filepath.Join(other, ".tao", "cache", "golangci-lint"))
		assertCacheChild(t, parent, root, "inherited shared cache")
	}
	if got := os.Getenv(cacheEnv); got != "inherited shared cache" {
		t.Fatalf("parent environment mutated: %q", got)
	}
	ordinary := t.TempDir()
	assertCacheChild(t, parent, ordinary, "inherited shared cache")
	if _, err := os.Lstat(filepath.Join(ordinary, ".tao")); !os.IsNotExist(err) {
		t.Fatalf("ordinary call prepared storage: %v", err)
	}
}

func TestVerificationCacheEnvironmentReplacesEveryEntry(t *testing.T) {
	setupIdentityTools(t)
	root := t.TempDir()
	inherited := []string{"PATH=" + os.Getenv("PATH"), "KEEP=one", cacheEnv + "=first", "OTHER=two", cacheEnv + "=second"}
	original := slices.Clone(inherited)
	got, err := verificationCacheEnvironment(WithVerificationCache(context.Background(), root), inherited)
	want := []string{"PATH=" + os.Getenv("PATH"), "KEEP=one", "OTHER=two", "PYTHONDONTWRITEBYTECODE=1", cacheEnv + "=" + filepath.Join(root, ".tao", "cache", "golangci-lint", testCacheNamespace)}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(inherited, original) {
		t.Fatalf("environment = %q, error = %v, original = %q", got, err, inherited)
	}
	got, err = verificationCacheEnvironment(context.Background(), inherited)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("ordinary environment changed: %q %v", got, err)
	}
}

func TestVerificationCacheBytecodeEnvironment(t *testing.T) {
	testenv.SkipIfShort(t)
	for _, value := range []string{"absent", "", "0", "custom"} {
		t.Run(value, func(t *testing.T) {
			setupCacheChild(t)
			t.Setenv("PYTHONDONTWRITEBYTECODE", value)
			inherited := make([]string, 2, 8)
			inherited[0] = "KEEP=one"
			inherited[1] = "PATH=" + os.Getenv("PATH")
			if value == "absent" {
				if err := os.Unsetenv("PYTHONDONTWRITEBYTECODE"); err != nil {
					t.Fatal(err)
				}
			} else {
				inherited = append(inherited, "PYTHONDONTWRITEBYTECODE="+value)
			}
			backing := inherited[:cap(inherited)]
			original := slices.Clone(backing)
			root := t.TempDir()
			for _, gate := range []bool{false, true} {
				ctx := context.Background()
				want := slices.Clone(inherited)
				cache := "inherited shared cache"
				if gate {
					ctx = WithVerificationCache(ctx, root)
					if value == "absent" {
						want = append(want, "PYTHONDONTWRITEBYTECODE=1")
					}
					cache = filepath.Join(root, ".tao", "cache", "golangci-lint")
					want = append(want, cacheEnv+"="+filepath.Join(cache, testCacheNamespace))
				}
				got, err := verificationCacheEnvironment(ctx, inherited)
				if err != nil || !slices.Equal(got, want) {
					t.Fatalf("gate=%v: environment = %q, want %q, error = %v", gate, got, want, err)
				}
				if !slices.Equal(backing, original) {
					t.Fatal("inherited backing array mutated")
				}
				assertCacheChild(t, ctx, root, cache)
			}
		})
	}
}

func TestVerificationCacheInvalidRoots(t *testing.T) {
	setupCacheChild(t)
	root := t.TempDir()
	marker := filepath.Join(root, "launched")
	t.Setenv("TAO_CACHE_TEST_MARKER", marker)
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", filepath.Join(root, "missing"), file} {
		if _, err := cacheChild(WithVerificationCache(context.Background(), invalid), root); err == nil {
			t.Fatalf("invalid root %q accepted", invalid)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command launched with invalid root: %v", err)
	}
}

func TestVerificationCacheRelativeRoot(t *testing.T) {
	setupCacheChild(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	assertCacheChild(t, WithVerificationCache(context.Background(), "."), filepath.Join(root, "sub"), filepath.Join(root, ".tao", "cache", "golangci-lint"))
}

func TestVerificationCacheConcurrent(t *testing.T) {
	setupCacheChild(t)
	roots := []string{t.TempDir(), t.TempDir()}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			root := roots[i%len(roots)]
			assertCacheChild(t, WithVerificationCache(context.Background(), root), root, filepath.Join(root, ".tao", "cache", "golangci-lint"))
		})
	}
	wg.Wait()
}

func TestVerificationCacheRejectsUnsafeStorage(t *testing.T) {
	testenv.SkipIfShort(t)
	setupCacheChild(t)
	for _, component := range []string{".tao", ".tao/cache", ".tao/cache/golangci-lint", ".tao/cache/golangci-lint/.gitignore", ".tao/cache/golangci-lint/" + testCacheNamespace} {
		for _, kind := range []string{"symlink", "conflict"} {
			t.Run(component+"/"+kind, func(t *testing.T) {
				root, outside := t.TempDir(), t.TempDir()
				target := filepath.Join(root, filepath.FromSlash(component))
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(outside, target)
				} else {
					err = os.WriteFile(target, []byte("do not replace\n"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(root, "launched")
				t.Setenv("TAO_CACHE_TEST_MARKER", marker)
				_, err = cacheChild(WithVerificationCache(context.Background(), root), root)
				if err == nil || !strings.Contains(err.Error(), "verification cache") {
					t.Fatalf("want setup error, got %v", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("command launched after setup failure: %v", err)
				}
				entries, err := os.ReadDir(outside)
				if err != nil || len(entries) != 0 {
					t.Fatalf("escaped storage changed: %v %v", entries, err)
				}
				if kind == "conflict" {
					data, err := os.ReadFile(target) //nolint:gosec // G304: target is a test-owned temporary conflict fixture.
					if err != nil || string(data) != "do not replace\n" {
						t.Fatalf("conflict replaced: %q %v", data, err)
					}
				}
			})
		}
	}
}

func TestVerificationCacheGitIsolation(t *testing.T) {
	testenv.SkipIfShort(t)
	setupCacheChild(t)
	t.Setenv("TAO_CACHE_TEST_WRITE", "1")
	roots := []string{t.TempDir(), t.TempDir()}
	git := func(root string, args ...string) string {
		t.Helper()
		var output bytes.Buffer
		args = append([]string{"-c", "core.excludesFile=" + os.DevNull}, args...)
		if err := DefaultLocal(context.Background(), root, "git", args, &output, io.Discard); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(output.String())
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, root := range roots {
		git(root, "init", "--quiet", "--template=")
		assertCacheChild(t, WithVerificationCache(context.Background(), root), root, filepath.Join(root, ".tao", "cache", "golangci-lint"))
		if status := git(root, "status", "--porcelain", "--untracked-files=all"); status != "" {
			t.Fatalf("cache dirtied repository: %s", status)
		}
		for _, path := range []string{"unrelated", ".tao/unrelated", ".tao/cache/unrelated"} {
			if err := os.WriteFile(filepath.Join(root, path), []byte("visible"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		status := git(root, "status", "--porcelain", "--untracked-files=all")
		for _, path := range []string{"unrelated", ".tao/unrelated", ".tao/cache/unrelated"} {
			if !strings.Contains(status, "?? "+path) {
				t.Fatalf("unrelated dirt hidden: %s", status)
			}
		}
	}
	if err := os.RemoveAll(roots[0]); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(roots[1], ".tao", "cache", "golangci-lint", testCacheNamespace, "entry"))
	if err != nil || string(data) != "cached" {
		t.Fatalf("other root cache changed: %q %v", data, err)
	}
}
