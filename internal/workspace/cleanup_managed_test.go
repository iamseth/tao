package workspace

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitOnBranch creates branch from base, adds one commit holding file=content,
// then drops the scratch worktree while keeping the branch.
func commitOnBranch(t *testing.T, repoPath string, branch string, file string, content string) {
	t.Helper()
	scratch := filepath.Join(t.TempDir(), "scratch-"+branch)
	runGit(t, repoPath, "worktree", "add", "-b", branch, scratch, "master")
	if err := os.WriteFile(filepath.Join(scratch, file), []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write %s: %v", file, err)
	}
	runGit(t, scratch, "add", ".")
	runGit(t, scratch, "commit", "-m", "work on "+branch)
	runGit(t, repoPath, "worktree", "remove", scratch)
}

func TestPlanManagedCleanupExcludesIntegrationWorktrees(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	start := strings.TrimSpace(runGit(t, repo.path, "rev-parse", "master"))
	integration, err := manager.CreateIntegration(context.Background(), "batch-a", start)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := manager.PlanManagedCleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plans {
		if item.Branch == integration.Branch {
			t.Fatalf("ordinary managed cleanup classified integration workspace: %#v", item)
		}
	}
}

func TestPlanManagedCleanupIncludesOnlyExactOwnedAndLegacyBranches(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	for _, branch := range []string{
		"feature/native-pr-format",
		"feature/native-pr-format-copy",
		"fix/native-pr-format",
		"docs/native-pr-format",
		"tao/legacy-plan",
		integrationBranchPrefix + "batch-a",
	} {
		runGit(t, repo.path, "branch", branch, "master")
	}

	plans, err := manager.PlanManagedCleanup(
		context.Background(),
		"feature/native-pr-format",
		"feature/native-pr-format",
		integrationBranchPrefix+"batch-a",
	)
	if err != nil {
		t.Fatal(err)
	}
	byBranch := make(map[string]ManagedCleanup, len(plans))
	for _, item := range plans {
		byBranch[item.Branch] = item
	}
	for _, want := range []string{"feature/native-pr-format", "tao/legacy-plan"} {
		if item, ok := byBranch[want]; !ok || item.Status != ManagedStatusClean || !item.CanRemove {
			t.Errorf("owned branch %q should be eligible, got %#v", want, item)
		}
	}
	for _, unrelated := range []string{
		"feature/native-pr-format-copy",
		"fix/native-pr-format",
		"docs/native-pr-format",
		integrationBranchPrefix + "batch-a",
	} {
		if _, ok := byBranch[unrelated]; ok {
			t.Errorf("unowned branch %q must be invisible, plans=%#v", unrelated, plans)
		}
	}
	if len(plans) != 2 {
		t.Fatalf("cleanup candidates = %#v, want one exact typed branch and one legacy branch", plans)
	}
}

func TestPlanManagedCleanupDecidesByGitState(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	ctx := context.Background()

	// tao/merged: fast-forward merged into master, with a live worktree.
	wtMerged := filepath.Join(repo.path, ".tao", "workspaces", "merged")
	runGit(t, repo.path, "worktree", "add", "-b", "tao/merged", wtMerged, "master")
	if err := os.WriteFile(filepath.Join(wtMerged, "merged.txt"), []byte("merged\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write merged.txt: %v", err)
	}
	runGit(t, wtMerged, "add", ".")
	runGit(t, wtMerged, "commit", "-m", "merged work")
	runGit(t, repo.path, "merge", "--ff-only", "tao/merged")

	// tao/squash: squash-merged into master (branch tip is not an ancestor).
	commitOnBranch(t, repo.path, "tao/squash", "squash.txt", "squashed\n")
	runGit(t, repo.path, "merge", "--squash", "tao/squash")
	runGit(t, repo.path, "commit", "-m", "squash merge tao/squash")

	// tao/wip: a real unmerged branch.
	commitOnBranch(t, repo.path, "tao/wip", "wip.txt", "wip\n")

	// tao/dirty: a live worktree with uncommitted changes.
	wtDirty := filepath.Join(repo.path, ".tao", "workspaces", "dirty")
	runGit(t, repo.path, "worktree", "add", "-b", "tao/dirty", wtDirty, "master")
	if err := os.WriteFile(filepath.Join(wtDirty, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write dirty.txt: %v", err)
	}

	// feature/keep: a non-Tao branch that must never be touched.
	runGit(t, repo.path, "branch", "feature/keep", "master")

	plans, err := manager.PlanManagedCleanup(ctx)
	if err != nil {
		t.Fatalf("PlanManagedCleanup failed: %v", err)
	}
	byBranch := map[string]ManagedCleanup{}
	for _, item := range plans {
		byBranch[item.Branch] = item
	}

	if _, ok := byBranch["feature/keep"]; ok {
		t.Fatalf("non-Tao branch must not be considered, got %#v", plans)
	}
	if _, ok := byBranch["master"]; ok {
		t.Fatalf("protected default branch must not be considered, got %#v", plans)
	}

	if got := byBranch["tao/merged"]; !got.CanRemove || got.Status != ManagedStatusClean || got.WorktreePath == "" {
		t.Fatalf("tao/merged should be removable with a worktree, got %#v", got)
	}
	if got := byBranch["tao/squash"]; !got.CanRemove || got.Status != ManagedStatusClean {
		t.Fatalf("tao/squash should be detected as merged, got %#v", got)
	}
	if got := byBranch["tao/wip"]; got.CanRemove || got.Status != ManagedStatusUnmerged {
		t.Fatalf("tao/wip should be unmerged, got %#v", got)
	}
	if got := byBranch["tao/dirty"]; got.CanRemove || got.Status != ManagedStatusDirty {
		t.Fatalf("tao/dirty should be skipped as dirty, got %#v", got)
	}

	// Remove the two safe candidates and confirm git state.
	for _, branch := range []string{"tao/merged", "tao/squash"} {
		if err := manager.CleanManaged(ctx, byBranch[branch], CleanOptions{}); err != nil {
			t.Fatalf("CleanManaged(%s) failed: %v", branch, err)
		}
	}
	if _, err := os.Stat(wtMerged); !os.IsNotExist(err) {
		t.Fatalf("expected tao/merged worktree removed, stat err=%v", err)
	}

	remaining, err := manager.git.cleanup.ListBranches(ctx, "")
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	present := map[string]bool{}
	for _, branch := range remaining {
		present[branch] = true
	}
	for _, gone := range []string{"tao/merged", "tao/squash"} {
		if present[gone] {
			t.Fatalf("expected %s deleted, branches=%v", gone, remaining)
		}
	}
	for _, kept := range []string{"tao/wip", "tao/dirty", "feature/keep", "master"} {
		if !present[kept] {
			t.Fatalf("expected %s kept, branches=%v", kept, remaining)
		}
	}
}

func TestCleanManagedRechecksWorktreeCleanliness(t *testing.T) {
	setup := func(t *testing.T) (*Manager, ManagedCleanup, string) {
		t.Helper()
		repo := newTestRepo(t)
		manager := newTestManager(t, repo.path)
		worktreePath := filepath.Join(repo.path, ".tao", "workspaces", "merged")
		runGit(t, repo.path, "worktree", "add", "-b", "tao/merged", worktreePath, "master")
		if err := os.WriteFile(filepath.Join(worktreePath, "merged.txt"), []byte("merged\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
			t.Fatal(err)
		}
		runGit(t, worktreePath, "add", ".")
		runGit(t, worktreePath, "commit", "-m", "merged work")
		runGit(t, repo.path, "merge", "--ff-only", "tao/merged")

		plans, err := manager.PlanManagedCleanup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(plans) != 1 || !plans[0].CanRemove {
			t.Fatalf("expected removable cleanup decision, got %#v", plans)
		}
		return manager, plans[0], worktreePath
	}

	t.Run("dirty after decision is refused", func(t *testing.T) {
		manager, item, worktreePath := setup(t)
		if err := os.WriteFile(filepath.Join(worktreePath, "late-dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
			t.Fatal(err)
		}
		if err := manager.CleanManaged(context.Background(), item, CleanOptions{}); err == nil {
			t.Fatal("expected fresh dirty check to refuse cleanup")
		}
		if _, err := os.Stat(worktreePath); err != nil {
			t.Fatalf("dirty worktree should remain: %v", err)
		}
		exists, err := manager.git.cleanup.BranchExists(context.Background(), item.Branch)
		if err != nil || !exists {
			t.Fatalf("branch should remain, exists=%v err=%v", exists, err)
		}
	})

	t.Run("merge evidence does not bypass dirty check", func(t *testing.T) {
		manager, item, worktreePath := setup(t)
		item.MergedNonAncestral = true
		if err := os.WriteFile(filepath.Join(worktreePath, "late-dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
			t.Fatal(err)
		}
		options := CleanOptions{AllowNonAncestralBranch: true}
		if err := manager.CleanManaged(context.Background(), item, options); err == nil {
			t.Fatal("expected merge evidence to preserve fresh dirty refusal")
		}
		if _, err := os.Stat(worktreePath); err != nil {
			t.Fatalf("dirty worktree should remain: %v", err)
		}
	})

	t.Run("force removes worktree dirtied after decision", func(t *testing.T) {
		manager, item, worktreePath := setup(t)
		if err := os.WriteFile(filepath.Join(worktreePath, "late-dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
			t.Fatal(err)
		}
		if err := manager.CleanManaged(context.Background(), item, CleanOptions{Force: true}); err != nil {
			t.Fatalf("forced cleanup failed: %v", err)
		}
		if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
			t.Fatalf("forced cleanup should remove worktree, stat err=%v", err)
		}
	})
}

func TestCleanManagedSelectsBranchDeleteMode(t *testing.T) {
	tests := []struct {
		name        string
		item        ManagedCleanup
		options     CleanOptions
		wantCommand string
	}{
		{
			name:        "ancestry merged uses guarded delete",
			item:        ManagedCleanup{Branch: "tao/ancestral", Status: ManagedStatusClean, CanRemove: true},
			wantCommand: "branch --delete tao/ancestral",
		},
		{
			name:        "non-ancestral merge evidence uses force delete",
			item:        ManagedCleanup{Branch: "tao/squashed", Status: ManagedStatusClean, CanRemove: true, MergedNonAncestral: true},
			wantCommand: "branch --delete --force tao/squashed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var commands []string
			manager, err := NewManager(Options{RepoRoot: t.TempDir(), Runner: func(_ context.Context, _ string, _ string, args []string, _ io.Writer, _ io.Writer) error {
				commands = append(commands, workspaceGitKey(args))
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.CleanManaged(context.Background(), tt.item, tt.options); err != nil {
				t.Fatal(err)
			}
			if len(commands) != 1 || commands[0] != tt.wantCommand {
				t.Fatalf("git commands = %#v, want %q", commands, tt.wantCommand)
			}
		})
	}
}

func TestCleanManagedRefusesUnmergedBeforeGitMutation(t *testing.T) {
	var commands []string
	manager, err := NewManager(Options{RepoRoot: t.TempDir(), Runner: func(_ context.Context, _ string, _ string, args []string, _ io.Writer, _ io.Writer) error {
		commands = append(commands, workspaceGitKey(args))
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	item := ManagedCleanup{Branch: "tao/wip", Status: ManagedStatusUnmerged, Reason: "not merged into master"}
	if err := manager.CleanManaged(context.Background(), item, CleanOptions{}); err == nil {
		t.Fatal("expected unmerged cleanup refusal")
	}
	if len(commands) != 0 {
		t.Fatalf("refused cleanup mutated git: %#v", commands)
	}
}

func TestPlanManagedCleanupForceRemovesUnmerged(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	ctx := context.Background()

	commitOnBranch(t, repo.path, "tao/wip", "wip.txt", "wip\n")

	plans, err := manager.PlanManagedCleanup(ctx)
	if err != nil {
		t.Fatalf("PlanManagedCleanup failed: %v", err)
	}
	if len(plans) != 1 || plans[0].CanRemove {
		t.Fatalf("expected one unmerged candidate, got %#v", plans)
	}
	if err := manager.CleanManaged(ctx, plans[0], CleanOptions{Force: true}); err != nil {
		t.Fatalf("forced CleanManaged failed: %v", err)
	}
	remaining, err := manager.git.cleanup.ListBranches(ctx, "tao/*")
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected forced removal of unmerged branch, branches=%v", remaining)
	}
}

func TestManagedBranchPrefix(t *testing.T) {
	manager := newTestManager(t, newTestRepo(t).path)
	if got := manager.ManagedBranchPrefix(); got != "tao/" {
		t.Fatalf("expected default prefix tao/, got %q", got)
	}

	config := DefaultConfig()
	config.BranchNameTemplate = "{plan_id}"
	bare, err := NewManager(Options{RepoRoot: t.TempDir(), Config: config})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if _, err := bare.PlanManagedCleanup(context.Background()); err == nil {
		t.Fatal("expected cleanup to refuse a template without a static prefix")
	}
}
