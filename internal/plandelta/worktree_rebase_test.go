package plandelta

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestWorktreeConflictingRebaseFollowsDetachedHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	git := localDeltaGit(t, root)
	put := func(root, path, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	put(root, "conflict", "base\n")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	tree := filepath.Join(t.TempDir(), "linked")
	git("worktree", "add", "-b", "feature/a", tree)
	wt := localDeltaGit(t, tree)
	put(tree, "conflict", "feature\n")
	wt("commit", "-am", "feature")
	tip := wt("rev-parse", "HEAD")
	put(root, "conflict", "upstream\n")
	put(root, "upstream-only", "upstream\n")
	git("add", ".")
	git("commit", "-m", "upstream")
	upstream := git("rev-parse", "HEAD")
	cmd := exec.Command("git", "-C", tree, "rebase", "main") // #nosec G204 -- local test-owned linked worktree.
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected conflict: %s", out)
	}
	d := &plan.PlanDetail{}
	d.State.Plan.ID = "plan-a"
	d.State.Repo.Root, d.State.Repo.BaseCommit = root, base
	d.State.Workspace = &plan.Workspace{Branch: "feature/a", Path: tree, Strategy: plan.WorkspaceStrategyWorktree, BaseBranch: "main", BaseSHA: base}
	plan.SetPersistedReview(d, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: base, Head: tip})
	c := NewCollector(nil)
	index := wt("rev-parse", "--git-path", "index")
	if !filepath.IsAbs(index) {
		index = filepath.Join(tree, index)
	}
	if err := os.WriteFile(index+".lock", []byte("held"), 0600); err != nil {
		t.Fatal(err)
	}
	before, beforeTree := snapshotTreeState(t, root), snapshotTreeState(t, tree)
	live, err := c.Snapshot(context.Background(), d, ScopeWorktree)
	if err != nil || live.Availability != AvailabilityReady || live.ActiveOperation != "rebase" || live.Head != upstream || live.Base.SHA != upstream || live.Base.Source != BaseSource("merge-base (worktree HEAD)") || !live.Dirty || !live.Review.HeadMatches || live.Review.BaseMatches {
		t.Fatalf("%+v %v", live, err)
	}
	for _, f := range live.Files {
		if f.Path == "upstream-only" {
			t.Fatal("upstream-only change attributed to plan")
		}
	}
	branch, err := c.Snapshot(context.Background(), d, ScopeBranch)
	if err != nil || branch.Head != tip || branch.Base.SHA != base {
		t.Fatalf("branch: %+v %v", branch, err)
	}
	if !reflect.DeepEqual(before, snapshotTreeState(t, root)) || !reflect.DeepEqual(beforeTree, snapshotTreeState(t, tree)) {
		t.Fatal("collector modified conflicting worktree or index")
	}
}
