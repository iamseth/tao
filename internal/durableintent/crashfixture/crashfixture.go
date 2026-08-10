// Package crashfixture provides real-Git test fixtures for durable-intent
// recovery tests. It deliberately models only Git state; callers own their
// site-specific intent and settlement records.
package crashfixture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/gitops"
)

const (
	DefaultBranch = "main"
	SourceBranch  = "tao/crash-source"

	MutationMessage = "test: apply durable intent mutation"
)

// Point identifies a durable-intent operation boundary.
type Point string

const (
	BeforeIntent     Point = "before_intent"
	AfterIntent      Point = "after_intent"
	AfterGitMutation Point = "after_git_mutation"
	AfterSettlement  Point = "after_settlement"
)

// Target selects the branch on which the operation's commit is applied.
// Merge tests normally use DefaultTarget; slice-completion tests use SourceTarget.
type Target string

const (
	DefaultTarget Target = "default"
	SourceTarget  Target = "source"
)

// Fixture is a repository with a default branch and a linked source worktree.
// BaseSHA is the default-branch head and SourceSHA is the source-branch head
// before the operation under test.
type Fixture struct {
	RepoRoot       string
	SourceWorktree string
	BaseSHA        string
	SourceSHA      string
	Git            gitops.Client
	SourceGit      gitops.Client
}

// State describes which non-Git records a caller should layer onto a fixture.
// IntentRecorded and Settled are instructions to the caller, not records kept
// by this Git-only package.
type State struct {
	Point          Point
	IntentRecorded bool
	Settled        bool
	MutationSHA    string
}

// New creates a real repository whose source branch contains one committed
// change relative to the default branch. Both worktrees are clean.
func New(t testing.TB) *Fixture {
	t.Helper()
	baseDir := t.TempDir()
	repoRoot := filepath.Join(baseDir, "repo")
	sourceWorktree := filepath.Join(baseDir, "source-worktree")
	if err := os.MkdirAll(repoRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoRoot, "init", "-b", DefaultBranch)
	runGit(t, repoRoot, "config", "user.name", "Tao Test")
	runGit(t, repoRoot, "config", "user.email", "tao@example.invalid")
	writeFile(t, filepath.Join(repoRoot, "README.md"), "initial\n")

	ctx := context.Background()
	git := gitops.NewClient(repoRoot, nil)
	must(t, git.Add(ctx, "README.md"))
	must(t, git.Commit(ctx, "test: initialize crash fixture"))
	baseSHA := mustRev(t, git, "HEAD")
	must(t, git.AddWorktree(ctx, sourceWorktree, SourceBranch, DefaultBranch, true))

	sourceGit := gitops.NewClient(sourceWorktree, nil)
	writeFile(t, filepath.Join(sourceWorktree, "source.txt"), "source change\n")
	must(t, sourceGit.Add(ctx, "source.txt"))
	must(t, sourceGit.Commit(ctx, "test: prepare source branch"))

	return &Fixture{
		RepoRoot: repoRoot, SourceWorktree: sourceWorktree,
		BaseSHA: baseSHA, SourceSHA: mustRev(t, sourceGit, "HEAD"),
		Git: git, SourceGit: sourceGit,
	}
}

// BeforeIntent returns the initial Git state, before an intent is recorded.
func (f *Fixture) BeforeIntent() State {
	return State{Point: BeforeIntent}
}

// AfterIntent returns the unchanged Git state and tells the caller to record
// its site-specific intent.
func (f *Fixture) AfterIntent() State {
	return State{Point: AfterIntent, IntentRecorded: true}
}

// AfterGitMutation applies the intended commit but leaves settlement to the caller.
func (f *Fixture) AfterGitMutation(t testing.TB, target Target) State {
	t.Helper()
	return f.applyMutation(t, target, AfterGitMutation, false)
}

// AfterSettlement applies the intended commit and tells the caller to record
// its site-specific settlement evidence.
func (f *Fixture) AfterSettlement(t testing.TB, target Target) State {
	t.Helper()
	return f.applyMutation(t, target, AfterSettlement, true)
}

func (f *Fixture) applyMutation(t testing.TB, target Target, point Point, settled bool) State {
	t.Helper()
	ctx := context.Background()
	var client gitops.Client
	switch target {
	case DefaultTarget:
		client = f.Git
		must(t, client.MergeSquash(ctx, SourceBranch))
	case SourceTarget:
		client = f.SourceGit
		writeFile(t, filepath.Join(f.SourceWorktree, "mutation.txt"), "intended mutation\n")
		must(t, client.Add(ctx, "mutation.txt"))
	default:
		t.Fatalf("unknown mutation target %q", target)
	}
	must(t, client.Commit(ctx, MutationMessage))
	return State{
		Point: point, IntentRecorded: true, Settled: settled,
		MutationSHA: mustRev(t, client, "HEAD"),
	}
}

func mustRev(t testing.TB, client gitops.Client, rev string) string {
	t.Helper()
	sha, err := client.RevParse(context.Background(), rev)
	must(t, err)
	return sha
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeFile(t testing.TB, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t testing.TB, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // Test helper invokes fixed git with test-owned arguments.
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
