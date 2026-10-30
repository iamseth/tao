package merge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"

	"github.com/iamseth/tao/internal/testenv"
)

type realGitWorktree struct {
	repoRoot      string
	worktreePath  string
	defaultBranch string
	planBranch    string
}

func newRealGitWorktree(t *testing.T) realGitWorktree {
	t.Helper()
	baseDir := t.TempDir()
	repoRoot := filepath.Join(baseDir, "repo")
	worktreePath := filepath.Join(baseDir, "plan-worktree")
	defaultBranch := "main"
	planBranch := "tao/plan-a"

	copied, err := realGitTemplate.CopyTo(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		if err := os.MkdirAll(repoRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := buildRealGitRepo(repoRoot); err != nil {
			t.Fatal(err)
		}
	}
	runRealGit(t, repoRoot, "worktree", "add", "-b", planBranch, worktreePath, defaultBranch)

	return realGitWorktree{repoRoot: repoRoot, worktreePath: worktreePath, defaultBranch: defaultBranch, planBranch: planBranch}
}

// realGitTemplate holds the committed main-branch repository every
// newRealGitWorktree fixture starts from; see TestMain.
var realGitTemplate *testenv.RepoTemplate

func buildRealGitRepo(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("initial\n"), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "README.md"}, {"commit", "-m", "initial"}} {
		cmd := exec.Command("git", args...) //nolint:gosec // G204: fixed fixture Git commands.
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func runRealGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: test invokes fixed git command with test-controlled args.
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func realGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: test invokes fixed git command with test-controlled args.
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// realGitOutputAllowFailure returns untrimmed output and leaves error handling to the caller.
func realGitOutputAllowFailure(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: test invokes fixed git command with test-controlled args.
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestWorktreeGitUsesPlanWorktreeBoundGitClientAndFallback(t *testing.T) {
	testenv.SkipIfShort(t)
	t.Parallel()
	fixture := newRealGitWorktree(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "worktree-only.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("worktree plan uses worktree-bound git client", func(t *testing.T) {
		var gotDir string
		service := newTestService(Service{
			Git: gitops.NewClient(fixture.repoRoot, nil),
			NewGit: func(dir string) GitClient {
				gotDir = dir
				return gitops.NewClient(dir, nil)
			},
		})

		git, err := service.worktreeGit(realGitWorktreeDetail(fixture, plan.WorkspaceStrategyWorktree))
		if err != nil {
			t.Fatal(err)
		}
		if gotDir != fixture.worktreePath {
			t.Fatalf("NewGit dir mismatch: got %q want %q", gotDir, fixture.worktreePath)
		}
		status, err := git.StatusPorcelain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(status, "?? worktree-only.txt") {
			t.Fatalf("expected worktree status from linked worktree, got %q", status)
		}
	})

	t.Run("current mode falls back to repo-root git client", func(t *testing.T) {
		fallback := &fakeGitClient{status: "repo-root status\n"}
		calledNewGit := false
		service := newTestService(Service{
			Git: fallback,
			NewGit: func(dir string) GitClient {
				calledNewGit = true
				return gitops.NewClient(dir, nil)
			},
		})

		git, err := service.worktreeGit(realGitWorktreeDetail(fixture, plan.WorkspaceStrategyCurrent))
		if err != nil {
			t.Fatal(err)
		}
		if calledNewGit {
			t.Fatal("current-mode plans should not call NewGit")
		}
		if git != fallback {
			t.Fatalf("expected fallback git client, got %#v", git)
		}
		status, err := git.StatusPorcelain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status != fallback.status {
			t.Fatalf("fallback status mismatch: got %q want %q", status, fallback.status)
		}
	})
}

func realGitWorktreeDetail(fixture realGitWorktree, strategy string) *plan.PlanDetail {
	return &plan.PlanDetail{
		State: plan.State{
			Repo: plan.Repo{Root: fixture.repoRoot},
			Workspace: &plan.Workspace{
				Strategy:   strategy,
				Path:       fixture.worktreePath,
				Branch:     fixture.planBranch,
				BaseBranch: fixture.defaultBranch,
			},
		},
	}
}
