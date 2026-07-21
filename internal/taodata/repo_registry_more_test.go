package taodata

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewRegistryUsesExplicitOrDefaultDataHome(t *testing.T) {
	custom := NewRegistry("/tmp/tao-data")
	if custom.DataHome != "/tmp/tao-data" || custom.Now == nil {
		t.Fatalf("unexpected custom registry: %#v", custom)
	}
	t.Setenv("TAO_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	def := NewRegistry("")
	if def.DataHome != os.Getenv("TAO_DATA_HOME") || def.Now == nil {
		t.Fatalf("unexpected default registry: %#v", def)
	}
}

func TestRegisterCurrentUsesInjectedRunnerForGitMetadata(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %q", name)
		}
		// gitops.Client targets a repo via "git -C <dir>" rather than the runner cwd;
		// normalize to the effective dir so call expectations stay legible.
		dir := cwd
		if len(args) >= 2 && args[0] == "-C" {
			dir = args[1]
			args = args[2:]
		}
		key := dir + "|" + strings.Join(args, " ")
		calls = append(calls, key)
		switch key {
		case "|rev-parse --show-toplevel":
			_, _ = io.WriteString(stdout, root+"\n")
		case root + "|branch --show-current":
			_, _ = io.WriteString(stdout, "main\n")
		case root + "|config --get remote.origin.url":
			_, _ = io.WriteString(stdout, "https://example.com/repo.git\n")
		default:
			t.Fatalf("unexpected git call %q", calls[len(calls)-1])
		}
		return ctx.Err()
	}
	registry := Registry{DataHome: t.TempDir(), Runner: runner, Now: fixedNow}
	repo, err := registry.RegisterCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if repo.Root != root || repo.Branch != "main" || repo.RemoteURL != "https://example.com/repo.git" {
		t.Fatalf("unexpected repo: %#v", repo)
	}
	wantCalls := []string{"|rev-parse --show-toplevel", root + "|branch --show-current", root + "|config --get remote.origin.url"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestRegistryReadListAndRepoForRoot(t *testing.T) {
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome, Now: fixedNow}
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := registry.RepoForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.ID != RepoID(canonical) || fallback.Root != canonical || fallback.Name != filepath.Base(canonical) {
		t.Fatalf("unexpected fallback repo: %#v", fallback)
	}

	stored := fallback
	stored.Schema = RepoSchema
	stored.Branch = "main"
	stored.UpdatedAt = fixedNow().UTC().Format("2006-01-02T15:04:05Z")
	if err := registry.WriteRepo(stored); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataHome, "repos", "not-a-dir"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataHome, "repos", "bad-json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataHome, "repos", "bad-json", "repo.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := registry.ReadRepo(stored.ID)
	if err != nil || got != stored {
		t.Fatalf("ReadRepo() = %#v, %v; want %#v", got, err, stored)
	}
	current, err := registry.RepoForRoot(root)
	if err != nil || current != stored {
		t.Fatalf("RepoForRoot() = %#v, %v; want %#v", current, err, stored)
	}
	repos, err := registry.ListRepos()
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0] != stored {
		t.Fatalf("ListRepos() = %#v", repos)
	}
	if registry.PlansDir(stored) != filepath.Join(dataHome, "repos", stored.ID, "plans") {
		t.Fatalf("unexpected plans dir")
	}
}

func TestListReposMissingRootIsEmpty(t *testing.T) {
	repos, err := (Registry{DataHome: t.TempDir()}).ListRepos()
	if err != nil || repos != nil {
		t.Fatalf("ListRepos() = %#v, %v; want nil, nil", repos, err)
	}
}

func TestAllocatePlanRejectsEmptyCleanSlug(t *testing.T) {
	_, err := (Registry{DataHome: t.TempDir(), Now: fixedNow}).AllocatePlan(Repo{ID: "repo"}, "!!!")
	if err == nil || !strings.Contains(err.Error(), "slug is required") {
		t.Fatalf("expected slug error, got %v", err)
	}
}

func TestAllocatePlanDistinguishesDifferentSeconds(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 7, 0, time.UTC)
	registry := Registry{DataHome: t.TempDir(), Now: func() time.Time { return now }}
	repo := Repo{ID: "repo"}

	first, err := registry.AllocatePlan(repo, "Example Plan")
	if err != nil {
		t.Fatalf("first AllocatePlan() failed: %v", err)
	}
	now = now.Add(time.Second)
	second, err := registry.AllocatePlan(repo, "Example Plan")
	if err != nil {
		t.Fatalf("second AllocatePlan() failed: %v", err)
	}

	if first.ID != "20260531-120007-example-plan" {
		t.Fatalf("first plan ID = %q", first.ID)
	}
	if second.ID != "20260531-120008-example-plan" {
		t.Fatalf("second plan ID = %q", second.ID)
	}
}

func TestCleanSlugNormalizesAndTruncates(t *testing.T) {
	got := cleanSlug("  Hello, Tao_Plans!!!  ")
	if got != "hello-tao-plans" {
		t.Fatalf("cleanSlug normalized to %q", got)
	}
	long := cleanSlug(strings.Repeat("a", 100) + "!!!")
	if len(long) != 80 || strings.HasSuffix(long, "-") {
		t.Fatalf("cleanSlug did not truncate cleanly: len=%d value=%q", len(long), long)
	}
}

func TestSlugRespectsMaxAndTrimsExposedDash(t *testing.T) {
	if got := Slug("  Hello, Tao_Plans!!!  ", 0); got != "hello-tao-plans" {
		t.Fatalf("unbounded Slug = %q", got)
	}
	// A cut that lands on a separator must re-trim so the result never ends in '-'.
	if got := Slug("alpha beta gamma", 6); got != "alpha" {
		t.Fatalf("Slug(_, 6) = %q, want %q", got, "alpha")
	}
	if got := Slug(strings.Repeat("a", 50), 36); len(got) != 36 {
		t.Fatalf("Slug did not cap at 36: len=%d", len(got))
	}
}
