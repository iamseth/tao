package plandelta

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestSnapshotLocalGitReadOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...) // #nosec G204 -- fixed local fixture commands and test-owned root.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	write(filepath.Join(root, "a"), "base\n")
	git("add", "a")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	tree := filepath.Join(t.TempDir(), "tree")
	git("worktree", "add", "-b", "feature/a", tree)
	write(filepath.Join(tree, "a"), "base\nnew\n")
	// Setup commands are local fixture mutations, not collector probes.
	cmd := exec.Command("git", "-C", tree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-am", "change") // #nosec G204 -- test-owned local worktree.
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture commit: %v %s", err, out)
	}
	d := &plan.PlanDetail{}
	d.State.Plan.ID = "plan-a"
	d.State.Repo.Root = root
	d.State.Repo.BaseCommit = base
	d.State.Workspace = &plan.Workspace{Branch: "feature/a", Path: tree, BaseBranch: "main", BaseSHA: base, Strategy: plan.WorkspaceStrategyWorktree}
	c := NewCollector(nil)
	first, err := c.Snapshot(context.Background(), d, ScopeBranch)
	if err != nil || first.Availability != AvailabilityReady || first.Total != (Stat{1, 1, 0}) || first.Base.Source != BaseSourceLiveMergeBase || first.Merged == nil || *first.Merged {
		t.Fatalf("%+v %v", first, err)
	}
	write(filepath.Join(tree, "a"), "uncommitted\n")
	write(filepath.Join(tree, ".tao", "noise"), "ignored\n")
	live, err := c.Snapshot(context.Background(), d, "")
	if err != nil || live.Scope != ScopeWorktree || live.Availability != AvailabilityReady || !live.Dirty || len(live.Files) != 1 || !live.Files[0].Uncommitted {
		t.Fatalf("worktree: %+v %v", live, err)
	}
	write(filepath.Join(root, ".git", "index.lock"), "held")
	// Compare every fixture byte and mtime, including indexes and Git metadata.
	before := snapshotTreeState(t, root)
	beforeTree := snapshotTreeState(t, tree)
	second, err := c.Snapshot(context.Background(), d, ScopeBranch)
	if err != nil || second.Signature != first.Signature {
		t.Fatalf("branch changed with dirty tree: %+v %v", second, err)
	}
	if !reflect.DeepEqual(before, snapshotTreeState(t, root)) || !reflect.DeepEqual(beforeTree, snapshotTreeState(t, tree)) {
		t.Fatal("collector wrote fixture")
	}
	if err := os.Remove(filepath.Join(root, ".git", "index.lock")); err != nil {
		t.Fatal(err)
	}
	// A lost worktree never hides the surviving branch.
	if err := os.RemoveAll(tree); err != nil {
		t.Fatal(err)
	}
	missing, err := c.Snapshot(context.Background(), d, ScopeWorktree)
	if err != nil || missing.Scope != ScopeBranch || !missing.WorktreeMissing || missing.Availability != AvailabilityReady || missing.Signature != first.Signature {
		t.Fatalf("missing tree %+v %v", missing, err)
	}
	// Current strategy follows the control checkout HEAD, not the plan branch.
	d.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
	current, err := c.Snapshot(context.Background(), d, ScopeBranch)
	if err != nil || current.Head != base || len(current.Files) != 0 || current.Merged != nil {
		t.Fatalf("current %+v %v", current, err)
	}
}

type snapshotDiskEntry struct {
	Hash     [32]byte
	Modified int64
	Mode     fs.FileMode
}

func snapshotTreeState(t *testing.T, root string) map[string]snapshotDiskEntry {
	t.Helper()
	result := map[string]snapshotDiskEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 G122 -- test-owned fixture with no concurrent writers or symlinks.
		if err != nil {
			return err
		}
		result[path] = snapshotDiskEntry{sha256.Sum256(data), info.ModTime().UnixNano(), info.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
