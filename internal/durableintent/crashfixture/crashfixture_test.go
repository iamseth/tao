package crashfixture

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrashPointsExposeExpectedDefaultTargetGitState(t *testing.T) {
	tests := []struct {
		name           string
		build          func(*testing.T, *Fixture) State
		wantPoint      Point
		wantIntent     bool
		wantSettlement bool
		wantMutation   bool
	}{
		{
			name: "before intent", build: func(_ *testing.T, f *Fixture) State { return f.BeforeIntent() },
			wantPoint: BeforeIntent,
		},
		{
			name: "after intent", build: func(_ *testing.T, f *Fixture) State { return f.AfterIntent() },
			wantPoint: AfterIntent, wantIntent: true,
		},
		{
			name: "after git mutation", build: func(t *testing.T, f *Fixture) State { return f.AfterGitMutation(t, DefaultTarget) },
			wantPoint: AfterGitMutation, wantIntent: true, wantMutation: true,
		},
		{
			name: "after settlement", build: func(t *testing.T, f *Fixture) State { return f.AfterSettlement(t, DefaultTarget) },
			wantPoint: AfterSettlement, wantIntent: true, wantSettlement: true, wantMutation: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := New(t)
			state := test.build(t, fixture)

			if state.Point != test.wantPoint || state.IntentRecorded != test.wantIntent || state.Settled != test.wantSettlement {
				t.Fatalf("state = %#v, want point=%q intent=%t settled=%t", state, test.wantPoint, test.wantIntent, test.wantSettlement)
			}
			assertWorktrees(t, fixture)
			if got := realGitOutput(t, fixture.RepoRoot, "rev-parse", SourceBranch); got != fixture.SourceSHA {
				t.Fatalf("source branch moved: got %s want %s", got, fixture.SourceSHA)
			}

			defaultHead := realGitOutput(t, fixture.RepoRoot, "rev-parse", DefaultBranch)
			if !test.wantMutation {
				if defaultHead != fixture.BaseSHA {
					t.Fatalf("default HEAD = %s, want base %s", defaultHead, fixture.BaseSHA)
				}
				if state.MutationSHA != "" {
					t.Fatalf("mutation SHA before mutation = %q", state.MutationSHA)
				}
				return
			}
			if defaultHead != state.MutationSHA {
				t.Fatalf("default HEAD = %s, want mutation %s", defaultHead, state.MutationSHA)
			}
			if parent := realGitOutput(t, fixture.RepoRoot, "rev-parse", defaultHead+"^"); parent != fixture.BaseSHA {
				t.Fatalf("mutation parent = %s, want %s", parent, fixture.BaseSHA)
			}
			if message := realGitOutput(t, fixture.RepoRoot, "show", "-s", "--format=%B", defaultHead); message != MutationMessage {
				t.Fatalf("mutation message = %q, want %q", message, MutationMessage)
			}
		})
	}
}

func TestSourceTargetMutationCommitsOnSourceWorktree(t *testing.T) {
	fixture := New(t)
	state := fixture.AfterGitMutation(t, SourceTarget)

	if got := realGitOutput(t, fixture.RepoRoot, "rev-parse", DefaultBranch); got != fixture.BaseSHA {
		t.Fatalf("default branch moved: got %s want %s", got, fixture.BaseSHA)
	}
	if got := realGitOutput(t, fixture.SourceWorktree, "rev-parse", "HEAD"); got != state.MutationSHA {
		t.Fatalf("source worktree HEAD = %s, want %s", got, state.MutationSHA)
	}
	if parent := realGitOutput(t, fixture.SourceWorktree, "rev-parse", "HEAD^"); parent != fixture.SourceSHA {
		t.Fatalf("source mutation parent = %s, want %s", parent, fixture.SourceSHA)
	}
	assertWorktrees(t, fixture)
}

func assertWorktrees(t *testing.T, fixture *Fixture) {
	t.Helper()
	if branch := realGitOutput(t, fixture.RepoRoot, "branch", "--show-current"); branch != DefaultBranch {
		t.Fatalf("repository branch = %q, want %q", branch, DefaultBranch)
	}
	if branch := realGitOutput(t, fixture.SourceWorktree, "branch", "--show-current"); branch != SourceBranch {
		t.Fatalf("source worktree branch = %q, want %q", branch, SourceBranch)
	}
	for _, dir := range []string{fixture.RepoRoot, fixture.SourceWorktree} {
		if status := realGitOutput(t, dir, "status", "--porcelain"); status != "" {
			t.Fatalf("worktree %s is dirty: %q", dir, status)
		}
	}
	worktrees := realGitOutput(t, fixture.RepoRoot, "worktree", "list", "--porcelain")
	repoRoot, err := filepath.EvalSymlinks(fixture.RepoRoot)
	if err != nil {
		t.Fatal(err)
	}
	sourceWorktree, err := filepath.EvalSymlinks(fixture.SourceWorktree)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"worktree " + repoRoot, "branch refs/heads/" + DefaultBranch, "worktree " + sourceWorktree, "branch refs/heads/" + SourceBranch} {
		if !strings.Contains(worktrees, want) {
			t.Fatalf("worktree state missing %q:\n%s", want, worktrees)
		}
	}
}

func realGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // Test invokes fixed git with test-controlled arguments.
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
