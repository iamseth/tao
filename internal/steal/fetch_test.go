package steal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/gitops"
)

func copyFixture(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755) // #nosec G301 -- test fixture directory.
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target) // #nosec G122 -- both trees are test-owned, with no concurrent writers.
		}
		data, err := os.ReadFile(path) // #nosec G304,G122 -- test-owned fixture tree with no concurrent writers.
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644) // #nosec G306,G703 -- relative paths from a test-owned tree; fixture modes before hardening.
	})
}

func fixtureRunner(t *testing.T, fixture, scratch string, fail string) gitops.Runner {
	t.Helper()
	return func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name != "git" {
			return fmt.Errorf("unexpected command: %s", name)
		}
		if slices.Contains(args, "clone") {
			if cwd != filepath.Dir(scratch) || args[len(args)-1] != scratch {
				t.Fatalf("clone cwd/dir: %s %v", cwd, args)
			}
			if err := copyFixture(fixture, scratch); err != nil {
				return err
			}
			if fail == "clone" {
				return errors.New("injected clone failure")
			}
			return nil
		}
		if cwd != scratch {
			t.Fatalf("command cwd = %s", cwd)
		}
		if strings.Join(args, " ") == fail {
			return errors.New("injected runner failure")
		}
		if !slices.Equal(args, []string{"ls-tree", "-r", "-z", "--format=%(objecttype) %(objectsize)", "HEAD"}) && !slices.Equal(args, []string{"checkout", "--quiet"}) && !slices.Equal(args, []string{"rev-parse", "--abbrev-ref", "HEAD"}) && !slices.Equal(args, []string{"rev-parse", "HEAD"}) {
			t.Fatalf("unexpected argv: %v", args)
		}
		return defaultRunner(ctx, cwd, name, args, stdout, stderr)
	}
}

// The clone seam copies local objects only; materialization uses the production
// runner so this regression cannot hide inherited configuration from checkout.
func TestFetchDoesNotExecuteInheritedHelpers(t *testing.T) {
	fixture := snapshotFixture(t)
	writeFixtureFile(t, fixture, ".gitattributes", []byte("payload filter=scout\n"))
	writeFixtureFile(t, fixture, "payload", []byte("unfiltered source\n"))
	localGit(t, fixture, "add", ".gitattributes", "payload")
	localGit(t, fixture, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "attributes")
	unmaterialized := filepath.Join(t.TempDir(), "unmaterialized")
	localGit(t, fixture, "clone", "--quiet", "--no-checkout", "--", fixture, unmaterialized)
	fixture = unmaterialized
	source, err := ValidateSourceURL("https://example.com/repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"global", "system", "environment", "parameters"} {
		for _, helper := range []string{"smudge", "process", "fsmonitor"} {
			t.Run(origin+"/"+helper, func(t *testing.T) {
				home := t.TempDir()
				marker := filepath.Join(home, "helper-invoked")
				key := "filter.scout." + helper
				command := "printf invoked > '" + marker + "'; cat"
				if helper == "process" {
					command = "printf invoked > '" + marker + "'; exit 1"
				}
				if helper == "fsmonitor" {
					key = "core.fsmonitor"
					command = "printf invoked > '" + marker + "'; :"
				}
				config := filepath.Join(home, "gitconfig")
				localGit(t, home, "config", "--file", config, key, command)
				t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
				t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
				switch origin {
				case "global":
					t.Setenv("GIT_CONFIG_GLOBAL", config)
				case "system":
					t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
					t.Setenv("GIT_CONFIG_SYSTEM", config)
				case "environment":
					t.Setenv("GIT_CONFIG_COUNT", "1")
					t.Setenv("GIT_CONFIG_KEY_0", key)
					t.Setenv("GIT_CONFIG_VALUE_0", command)
				case "parameters":
					t.Setenv("GIT_CONFIG_PARAMETERS", "'"+key+"'='"+strings.ReplaceAll(command, "'", "'\\''")+"'")
				}
				runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if slices.Contains(args, "clone") {
						return copyFixture(fixture, args[len(args)-1])
					}
					return defaultRunner(ctx, cwd, name, args, stdout, stderr)
				}
				result, fetchErr := Fetch(context.Background(), Options{DataHome: home, Source: source, Runner: runner})
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("inherited %s helper executed: %v (fetch: %v)", helper, err, fetchErr)
				}
				if fetchErr != nil {
					t.Fatal(fetchErr)
				}
				data, err := os.ReadFile(filepath.Join(result.SnapshotPath, "payload"))
				if err != nil || string(data) != "unfiltered source\n" {
					t.Fatalf("materialized content = %q, %v", data, err)
				}
			})
		}
	}
}

func TestFetch(t *testing.T) {
	fixture := snapshotFixture(t)
	source, err := ValidateSourceURL("https://example.com/Team/Repo.git")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	scratch := ScratchDir(home, source, now)
	result, err := Fetch(context.Background(), Options{DataHome: home, Source: source, Now: func() time.Time { return now }, Runner: fixtureRunner(t, fixture, scratch, "")})
	if err != nil {
		t.Fatal(err)
	}
	if result.SnapshotPath != scratch || result.DefaultBranch != "main" || result.Commit != localGit(t, fixture, "rev-parse", "HEAD") || result.DeclaredVersion != "1.2.3" || result.CampaignTag != "steal-example-com-team-repo-2026-09-26" || result.SizeBytes <= 0 {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Contains(result.Omitted, Omission{"link", "symlink"}) {
		t.Fatalf("omissions: %+v", result.Omitted)
	}
	// A same-second collision must neither overwrite nor remove an existing snapshot.
	_, err = Fetch(context.Background(), Options{DataHome: home, Source: source, Now: func() time.Time { return now }, Runner: fixtureRunner(t, fixture, scratch, "clone")})
	if err == nil {
		t.Fatal("accepted existing scratch directory")
	}
	if _, err := os.Stat(filepath.Join(scratch, "VERSION")); err != nil {
		t.Fatal(err)
	}
}

func TestFetchFailureCleanup(t *testing.T) {
	fixture := snapshotFixture(t)
	source, err := ValidateSourceURL("git@example.com:team/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, fail := range []string{"clone", "ls-tree -r -z --format=%(objecttype) %(objectsize) HEAD", "checkout --quiet", "rev-parse --abbrev-ref HEAD", "rev-parse HEAD", "object-cap", "snapshot-cap"} {
		t.Run(fail, func(t *testing.T) {
			home := t.TempDir()
			scratch := ScratchDir(home, source, now)
			opts := Options{DataHome: home, Source: source, Now: func() time.Time { return now }, Runner: fixtureRunner(t, fixture, scratch, fail)}
			if fail == "object-cap" {
				opts.MaxBytes = 1
			}
			if fail == "snapshot-cap" {
				opts.MaxBytes = 1 << 20
				baseRunner := opts.Runner
				opts.Runner = func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if err := baseRunner(ctx, cwd, name, args, stdout, stderr); err != nil {
						return err
					}
					if slices.Contains(args, "clone") {
						writeFixtureFile(t, scratch, "large", []byte(strings.Repeat("x", 2<<20)))
					}
					return nil
				}
			}
			clones := 0
			runner := opts.Runner
			opts.Runner = func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if slices.Contains(args, "clone") {
					clones++
				}
				return runner(ctx, cwd, name, args, stdout, stderr)
			}
			_, err := Fetch(context.Background(), opts)
			if clones != 1 {
				t.Fatalf("clone attempts = %d", clones)
			}
			if err == nil {
				t.Fatal("expected failure")
			}
			if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
				t.Fatalf("scratch left behind: %v", err)
			}
		})
	}
}

func TestFetchReportsCleanupFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	home := t.TempDir()
	source, err := ValidateSourceURL("https://example.com/repo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	scratch := ScratchDir(home, source, now)
	locked := filepath.Join(scratch, "locked")
	t.Cleanup(func() { _ = os.Chmod(locked, 0700) }) // #nosec G302 -- restore test cleanup access.
	failure := errors.New("injected clone failure")
	runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		writeFixtureFile(t, scratch, "locked/file", []byte("leftover"))
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		return failure
	}
	_, err = Fetch(context.Background(), Options{DataHome: home, Source: source, Now: func() time.Time { return now }, Runner: runner})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "cleanup failed") || !strings.Contains(err.Error(), scratch) {
		t.Fatalf("cleanup error = %v", err)
	}
}

func TestTransportConfig(t *testing.T) {
	home := t.TempDir()
	// Apple Git can supply a built-in system credential helper even with both
	// config files redirected. Retain that baseline without host config.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	baseline, err := transportConfig(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, home, "included", []byte("[credential]\n\thelper = trusted-helper\n\thelper =\n\thelper = second-helper\n[http \"https://example.com\"]\n\textraHeader = Authorization: fixture\n\tsslCAInfo = /trusted/ca\n[filter \"scout\"]\n\tprocess = forbidden\n"))
	writeFixtureFile(t, home, "global", []byte("[include]\n\tpath = included\n[core]\n\tsshCommand = trusted-ssh\n\tfsmonitor = forbidden\n[init]\n\ttemplateDir = forbidden\n[url \"ext::forbidden\"]\n\tinsteadOf = https://\n"))
	writeFixtureFile(t, home, "system", []byte("[http]\n\tproxy = http://trusted-proxy\n[core]\n\thooksPath = forbidden\n"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(home, "system"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "global"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "injected-helper")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.sshCommand'='injected-ssh'")
	t.Setenv("GIT_DIR", filepath.Join(home, "not-a-repo"))
	got, err := transportConfig(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	want := baseline
	want = append(want, []gitSetting{
		{"http.proxy", "http://trusted-proxy"},
		{"credential.helper", "trusted-helper"}, {"credential.helper", ""}, {"credential.helper", "second-helper"},
		{"http.https://example.com.extraheader", "Authorization: fixture"},
		{"http.https://example.com.sslcainfo", "/trusted/ca"},
		{"core.sshcommand", "trusted-ssh"},
	}...)
	if !slices.Equal(got, want) {
		t.Fatalf("transport config = %q, want %q", got, want)
	}
	// Broken config must stop before clone, not retry with inherited policy.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "included"))
	writeFixtureFile(t, home, "included", []byte("[invalid\n"))
	if _, err := transportConfig(context.Background(), home); err == nil {
		t.Fatal("accepted invalid config")
	}
}

func TestIsolatedGitEnv(t *testing.T) {
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_TEMPLATE_DIR", "GIT_EXEC_PATH",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_PARAMETERS",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_ATTR_NOSYSTEM",
		"GIT_TERMINAL_PROMPT", "GIT_NO_LAZY_FETCH", "GIT_ALLOW_PROTOCOL",
	} {
		t.Setenv(key, "forbidden")
	}
	auth := []string{"GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "GIT_ASKPASS", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH"}
	for _, key := range auth {
		t.Setenv(key, "trusted-auth")
	}
	t.Setenv("SSH_AUTH_SOCK", "trusted-agent")
	t.Setenv("HTTPS_PROXY", "trusted-proxy")
	for _, cloning := range []bool{false, true} {
		env := isolatedGitEnv(cloning)
		for _, entry := range env {
			if strings.HasPrefix(entry, "GIT_") && strings.HasSuffix(entry, "=forbidden") {
				t.Errorf("cloning=%v leaked %s", cloning, entry)
			}
		}
		for _, key := range auth {
			if slices.Contains(env, key+"=trusted-auth") != cloning {
				t.Errorf("cloning=%v: unexpected auth environment for %s", cloning, key)
			}
		}
		for _, entry := range []string{"SSH_AUTH_SOCK=trusted-agent", "HTTPS_PROXY=trusted-proxy", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1"} {
			if !slices.Contains(env, entry) {
				t.Errorf("cloning=%v: missing %s", cloning, entry)
			}
		}
	}
}

func TestDefaultRunnerDisablesTemplates(t *testing.T) {
	fixture := snapshotFixture(t)
	home := t.TempDir()
	template := filepath.Join(home, "template")
	writeFixtureFile(t, template, "config", []byte("[filter \"scout\"]\n\tsmudge = forbidden\n[core]\n\tfsmonitor = forbidden\n"))
	writeFixtureFile(t, template, "template-marker", []byte("must not be copied"))
	global := filepath.Join(home, "global")
	localGit(t, home, "config", "--file", global, "init.templateDir", template)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_TEMPLATE_DIR", template)
	clone := filepath.Join(home, "clone")
	// Only a local fixture is cloned, never the network. Exercise the production
	// runner's clone-specific config projection and template isolation.
	var stderr strings.Builder
	if err := defaultRunner(context.Background(), home, "git", []string{"clone", "--no-checkout", "--", fixture, clone}, io.Discard, &stderr); err != nil {
		t.Fatalf("clone: %v: %s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(clone, ".git", "template-marker")); !os.IsNotExist(err) {
		t.Fatalf("inherited template was copied: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(clone, ".git", "config")) // #nosec G304 -- test-owned local clone config.
	if err != nil || strings.Contains(string(config), "forbidden") {
		t.Fatalf("clone local config = %q, %v", config, err)
	}
}

func TestDefaultRunner(t *testing.T) {
	if os.Getenv("TAO_STEAL_TEST_RUNNER") == "1" {
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(1)
		}
		fmt.Printf("%s|%s|%s", cwd, os.Getenv("GIT_TERMINAL_PROMPT"), os.Getenv("GIT_NO_LAZY_FETCH"))
		os.Exit(0)
	}
	t.Setenv("TAO_STEAL_TEST_RUNNER", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_NO_LAZY_FETCH", "0")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := defaultRunner(context.Background(), cwd, exe, []string{"-test.run=^TestDefaultRunner$"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != cwd+"|0|1" {
		t.Fatalf("runner environment = %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := defaultRunner(ctx, cwd, exe, []string{"-test.run=^TestDefaultRunner$"}, io.Discard, io.Discard); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestFetchRefusesInvalidOptionsAndCheckoutDataHome(t *testing.T) {
	source, err := ValidateSourceURL("https://example.com/repo")
	if err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0755); err != nil { // #nosec G301 -- test checkout marker.
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	if err := os.Symlink(checkout, filepath.Join(linkParent, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []Options{
		{Source: source},
		{DataHome: t.TempDir(), Source: Source{URL: "file:///local"}},
		{DataHome: filepath.Join(checkout, "data"), Source: source},
		{DataHome: filepath.Join(linkParent, "linked", "data"), Source: source},
	} {
		opts.Runner = func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			t.Fatal("runner called")
			return nil
		}
		if _, err := Fetch(context.Background(), opts); err == nil {
			t.Fatal("accepted invalid options")
		}
	}
}
