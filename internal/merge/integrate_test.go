package merge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

func TestMergeSquashesPlanBranchByDefault(t *testing.T) {
	fixture := newRealGitWorktree(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "checkpoint one")
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "commit", "-am", "checkpoint two")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	preMergeDefaultSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)

	detail := mergeReadyDetail(preMergeDefaultSHA)
	detail.State.Plan.Title = "Transactional Slice Commits"
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch

	if err := (Service{Git: gitops.NewClient(fixture.repoRoot, nil)}).IntegrateSquash(ctx, detail); err != nil {
		t.Fatal(err)
	}

	mergedSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if mergedSHA == sourceHead {
		t.Fatal("squash merge must create a distinct default-branch commit")
	}
	if parent := realGitOutput(t, fixture.repoRoot, "rev-parse", mergedSHA+"^"); parent != preMergeDefaultSHA {
		t.Fatalf("squash commit parent = %s, want %s", parent, preMergeDefaultSHA)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
		t.Fatalf("source branch moved: got %s want %s", got, sourceHead)
	}
	message := realGitOutput(t, fixture.repoRoot, "show", "-s", "--format=%B", mergedSHA)
	for _, want := range []string{"Transactional Slice Commits", "Tao-Plan: plan-a", "Tao-Source-Head: " + sourceHead} {
		if !strings.Contains(message, want) {
			t.Fatalf("squash message missing %q: %q", want, message)
		}
	}
	if got := realGitOutput(t, fixture.repoRoot, "show", mergedSHA+":feature.txt"); got != "two" {
		t.Fatalf("squash tree has feature.txt %q, want %q", got, "two")
	}
}

func TestIntegrateSquashCommitFailureRestoresDefault(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "pre123", "tao/plan-a": "source456"},
		changedFiles:  []string{"feature.txt"},
		commitErr:     errors.New("identity missing"),
	}

	err := (Service{Git: git}).IntegrateSquash(context.Background(), mergeReadyDetail("base123"))
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("expected typed integration failure, got %v", err)
	}
	wantSuffix := []string{"status", "reset-hard pre123", "checkout main"}
	if got := git.calls[len(git.calls)-len(wantSuffix):]; !reflect.DeepEqual(got, wantSuffix) {
		t.Fatalf("rollback calls mismatch\nwant: %#v\n got: %#v", wantSuffix, git.calls)
	}
}

func TestIntegrateSquashConflictRestoresCleanDefault(t *testing.T) {
	fixture := newRealGitWorktree(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "commit", "-am", "change on plan")
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "commit", "-am", "change on default")
	preMergeDefaultSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)

	detail := mergeReadyDetail(preMergeDefaultSHA)
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch

	err := (Service{Git: gitops.NewClient(fixture.repoRoot, nil)}).IntegrateSquash(ctx, detail)
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("expected typed squash conflict, got %v", err)
	}
	var conflict *MergeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected MergeConflictError, got %T", err)
	}
	if !reflect.DeepEqual(conflict.Files, []string{"README.md"}) {
		t.Fatalf("conflict files = %v, want only the unmerged path [README.md]", conflict.Files)
	}
	if got := realGitOutput(t, fixture.repoRoot, "status", "--porcelain"); got != "" {
		t.Fatalf("default worktree remains dirty after squash conflict: %q", got)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD"); got != preMergeDefaultSHA {
		t.Fatalf("HEAD after squash conflict = %s, want %s", got, preMergeDefaultSHA)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != preMergeDefaultSHA {
		t.Fatalf("default branch after squash conflict = %s, want %s", got, preMergeDefaultSHA)
	}
}

func TestMergeRebasesPlanWorktreeWhenDefaultAdvanced(t *testing.T) {
	fixture := newRealGitWorktree(t)
	ctx := context.Background()
	baseSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)

	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "feature")
	originalPlanSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)

	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "default.txt"), []byte("default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "default.txt")
	runRealGit(t, fixture.repoRoot, "commit", "-m", "advance default")
	preMergeDefaultSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if preMergeDefaultSHA == baseSHA {
		t.Fatal("default branch did not advance")
	}

	detail := mergeReadyDetail(baseSHA)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = fixture.worktreePath
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch

	service := Service{
		Git: gitops.NewClient(fixture.repoRoot, nil),
		NewGit: func(dir string) GitClient {
			if dir != fixture.worktreePath {
				t.Fatalf("NewGit dir mismatch: got %q want %q", dir, fixture.worktreePath)
			}
			return gitops.NewClient(dir, nil)
		},
		Cleaner: successfulCleanup(),
		Events:  &fakeEventAppender{},
	}

	if err := service.Merge(ctx, detail, Options{NoVerify: true, NoSquash: true}); err != nil {
		t.Fatal(err)
	}

	mergedDefaultSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	rebasedPlanSHA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	if mergedDefaultSHA != rebasedPlanSHA {
		t.Fatalf("default was not fast-forwarded to plan tip: default %s plan %s", mergedDefaultSHA, rebasedPlanSHA)
	}
	if rebasedPlanSHA == originalPlanSHA {
		t.Fatal("plan branch was not rebased")
	}
	if parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch+"^"); parent != preMergeDefaultSHA {
		t.Fatalf("merged tip parent mismatch: got %s want %s", parent, preMergeDefaultSHA)
	}
	logSubjects := realGitOutput(t, fixture.repoRoot, "log", "--format=%s", "--reverse", fixture.defaultBranch)
	if want := "initial\nadvance default\nfeature"; logSubjects != want {
		t.Fatalf("linear history mismatch\nwant:\n%s\n got:\n%s", want, logSubjects)
	}
}

// TestIntegrateWorktreePlanMutatesOnlyWorktreeRoot is the regression test for
// the incident where Integrate issued rebase/checkout mutations against the
// repo root instead of the plan worktree. It asserts that, for a worktree-
// strategy plan, every rebase call lands on the worktree-bound client and none
// land on the repo-root client.
func TestIntegrateWorktreePlanMutatesOnlyWorktreeRoot(t *testing.T) {
	// Real directories: hasSeparatePlanWorktree only trusts a worktree that
	// exists on disk.
	repoRoot := t.TempDir()
	worktreePath := t.TempDir()

	reg := newFakeGitRegistry()
	// Repo-root client: knows the default branch and pre-merge SHA; IsAncestor
	// returns false (ancestors map nil) to force the rebase path.
	reg.seed(repoRoot, &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "pre123"},
	})
	// Worktree client: zero state; Rebase and other mutations return nil.
	reg.seed(worktreePath, &fakeGitClient{})

	detail := mergeReadyDetail("base123")
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = worktreePath

	service := Service{
		Git:    reg.client(repoRoot),
		NewGit: reg.newGit,
	}

	if err := service.Integrate(context.Background(), detail); err != nil {
		t.Fatal(err)
	}

	repoCalls := reg.client(repoRoot).calls
	worktreeCalls := reg.client(worktreePath).calls

	// Rebase must be issued against the worktree root, never the repo root.
	for _, call := range repoCalls {
		if strings.HasPrefix(call, "rebase") {
			t.Fatalf("rebase call leaked to repo root: %q\nrepo calls:      %#v\nworktree calls:  %#v",
				call, repoCalls, worktreeCalls)
		}
	}
	if !hasGitCall(worktreeCalls, "rebase main") {
		t.Fatalf("expected 'rebase main' in worktree calls\nrepo calls:      %#v\nworktree calls:  %#v",
			repoCalls, worktreeCalls)
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
