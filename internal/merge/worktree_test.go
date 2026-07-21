package merge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
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

	if err := os.MkdirAll(repoRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, repoRoot, "init", "-b", defaultBranch)
	runRealGit(t, repoRoot, "config", "user.name", "Tao Test")
	runRealGit(t, repoRoot, "config", "user.email", "tao@example.invalid")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, repoRoot, "add", "README.md")
	runRealGit(t, repoRoot, "commit", "-m", "initial")
	runRealGit(t, repoRoot, "worktree", "add", "-b", planBranch, worktreePath, defaultBranch)

	return realGitWorktree{repoRoot: repoRoot, worktreePath: worktreePath, defaultBranch: defaultBranch, planBranch: planBranch}
}

func runRealGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: test invokes fixed git command with test-controlled args.
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestWorktreeGitUsesPlanWorktreeBoundGitClientAndFallback(t *testing.T) {
	fixture := newRealGitWorktree(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "worktree-only.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("worktree plan uses worktree-bound git client", func(t *testing.T) {
		var gotDir string
		service := Service{
			Git: gitops.NewClient(fixture.repoRoot, nil),
			NewGit: func(dir string) GitClient {
				gotDir = dir
				return gitops.NewClient(dir, nil)
			},
		}

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
		service := Service{
			Git: fallback,
			NewGit: func(dir string) GitClient {
				calledNewGit = true
				return gitops.NewClient(dir, nil)
			},
		}

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
