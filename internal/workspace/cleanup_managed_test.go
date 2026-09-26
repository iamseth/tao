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

func TestPlanIntegrationCleanupDecidesByGitState(t *testing.T) {
	repo := newTestRepo(t)
	physicalRoot, err := filepath.EvalSymlinks(repo.path)
	if err != nil {
		t.Fatal(err)
	}
	repo.path = physicalRoot
	manager := newTestManager(t, repo.path)
	ctx := context.Background()
	start := strings.TrimSpace(runGit(t, repo.path, "rev-parse", "master"))
	create := func(id string) IntegrationWorkspace {
		t.Helper()
		item, err := manager.CreateIntegration(ctx, id, start)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	commit := func(item IntegrationWorkspace, file string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(item.Path, file), []byte(file+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, item.Path, "add", file)
		runGit(t, item.Path, "commit", "-m", "integration work")
	}

	active := create("active")
	unmerged := create("unmerged")
	commit(unmerged, "wip.txt")
	squashed := create("squashed")
	commit(squashed, "squash.txt")
	runGit(t, repo.path, "merge", "--squash", squashed.Branch)
	runGit(t, repo.path, "commit", "-m", "apply integration squash")
	branchOnly := create("branch-only")
	runGit(t, repo.path, "worktree", "remove", branchOnly.Path)
	dirty := create("dirty")
	if err := os.WriteFile(filepath.Join(dirty.Path, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := create("outside")
	outsidePath := filepath.Join(t.TempDir(), "moved")
	runGit(t, repo.path, "worktree", "move", outside.Path, outsidePath)
	outsidePath, err = filepath.EvalSymlinks(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	outside.Path = outsidePath
	commit(outside, "outside.txt")
	unregisteredPath := filepath.Join(repo.path, ".tao", "integrations", "unregistered")
	if err := os.MkdirAll(unregisteredPath, 0o750); err != nil {
		t.Fatal(err)
	}
	// A directory left behind for a branch-only namespace is not a worktree.
	if err := os.MkdirAll(branchOnly.Path, 0o750); err != nil {
		t.Fatal(err)
	}

	integrations, err := manager.ListIntegrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"active", "branch-only", "dirty", "outside", "squashed", "unmerged"}
	if len(integrations) != len(wantIDs) {
		t.Fatalf("integrations = %#v", integrations)
	}
	for i, item := range integrations {
		if item.BatchID != wantIDs[i] || item.Branch != integrationBranchPrefix+item.BatchID {
			t.Fatalf("integration %d = %#v, want %s", i, item, wantIDs[i])
		}
		if item.BatchID == branchOnly.BatchID {
			if !item.Missing || item.HeadSHA != "" || item.Dirty {
				t.Fatalf("branch-only integration = %#v", item)
			}
			continue
		}
		if item.Missing || item.HeadSHA == "" || item.Dirty != (item.BatchID == dirty.BatchID) {
			t.Fatalf("registered integration = %#v", item)
		}
		if item.BatchID == outside.BatchID && item.Path != outsidePath {
			t.Fatalf("moved integration path = %q, want %q", item.Path, outsidePath)
		}
	}

	items, err := manager.PlanIntegrationCleanup(ctx, "  "+active.BatchID+" \n")
	if err != nil {
		t.Fatal(err)
	}
	byBranch := make(map[string]ManagedCleanup)
	var unregistered ManagedCleanup
	for _, item := range items {
		if !strings.HasPrefix(item.Reason, "unreferenced integration namespace: ") {
			t.Errorf("missing integration reason: %#v", item)
		}
		if item.Status == ManagedStatusUnregistered {
			unregistered = item
			continue
		}
		byBranch[item.Branch] = item
	}
	if len(items) != 6 {
		t.Fatalf("cleanup items = %#v", items)
	}
	if _, ok := byBranch[active.Branch]; ok {
		t.Fatal("active batch must be excluded")
	}
	for _, want := range []struct {
		integration IntegrationWorkspace
		status      string
		path        string
	}{
		{unmerged, ManagedStatusUnmerged, unmerged.Path},
		{squashed, ManagedStatusClean, squashed.Path},
		{branchOnly, ManagedStatusClean, ""},
		{dirty, ManagedStatusDirty, dirty.Path},
		{outside, ManagedStatusUnmerged, outsidePath},
	} {
		got, ok := byBranch[want.integration.Branch]
		if !ok || got.Status != want.status || got.WorktreePath != want.path || got.CanRemove != (want.status == ManagedStatusClean) {
			t.Errorf("%s cleanup = %#v, want %s path=%q", want.integration.BatchID, got, want.status, want.path)
		}
	}
	if !byBranch[squashed.Branch].MergedNonAncestral {
		t.Fatal("squash cleanup must retain non-ancestral merge evidence")
	}
	if unregistered.WorktreePath != unregisteredPath || unregistered.CanRemove {
		t.Fatalf("unregistered directory = %#v", unregistered)
	}
	for _, options := range []CleanOptions{{}, {Force: true}, {AllowNonAncestralBranch: true}} {
		if err := manager.CleanManaged(ctx, unregistered, options); err == nil || !strings.Contains(err.Error(), "unregistered") {
			t.Fatalf("unregistered cleanup should be refused: %v", err)
		}
	}
	if _, err := os.Stat(unregisteredPath); err != nil {
		t.Fatalf("unregistered directory must remain: %v", err)
	}
	for _, item := range []IntegrationWorkspace{dirty, unmerged} {
		if err := manager.CleanManaged(ctx, byBranch[item.Branch], CleanOptions{}); err == nil {
			t.Fatalf("unsafe cleanup accepted: %s", item.Branch)
		}
	}
	if err := manager.CleanManaged(ctx, byBranch[squashed.Branch], CleanOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(squashed.Path); !os.IsNotExist(err) {
		t.Fatalf("clean integration worktree should be removed: %v", err)
	}
	if exists, err := manager.git.branches.LocalBranchExists(ctx, squashed.Branch); err != nil || exists {
		t.Fatalf("clean integration branch should be removed: exists=%t err=%v", exists, err)
	}
}

func TestListIntegrationsDiscoversRegisteredNamespacePath(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	ctx := context.Background()
	path := filepath.Join(repo.path, ".tao", "integrations", "detached")
	runGit(t, repo.path, "worktree", "add", "--detach", path, "master")
	items, err := manager.ListIntegrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	physicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].BatchID != "detached" || items[0].Path != physicalPath || items[0].Missing || items[0].HeadSHA == "" {
		t.Fatalf("path-only integration = %#v", items)
	}
}

func TestPlanIntegrationCleanupPreservesCurrentBranch(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	ctx := context.Background()
	start := strings.TrimSpace(runGit(t, repo.path, "rev-parse", "master"))
	integration, err := manager.CreateIntegration(ctx, "current", start)
	if err != nil {
		t.Fatal(err)
	}
	manager = newTestManager(t, integration.Path)
	items, err := manager.PlanIntegrationCleanup(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != ManagedStatusCurrent || items[0].CanRemove {
		t.Fatalf("current integration cleanup = %#v", items)
	}
	if err := manager.CleanManaged(ctx, items[0], CleanOptions{Force: true}); err == nil {
		t.Fatal("force must not remove the current integration branch")
	}
}

func TestPlanIntegrationCleanupRefusesMismatchedWorktreeBranch(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	ctx := context.Background()
	start := strings.TrimSpace(runGit(t, repo.path, "rev-parse", "master"))
	integration, err := manager.CreateIntegration(ctx, "mismatch", start)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, integration.Path, "checkout", "-b", "main")
	if _, err := manager.PlanIntegrationCleanup(ctx, ""); err == nil {
		t.Fatal("must not authorize removal of a protected worktree using a stale integration branch")
	}
}

func TestPlanIntegrationCleanupExcludesActiveUnregisteredDirectory(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	path := filepath.Join(repo.path, ".tao", "integrations", "active")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	items, err := manager.PlanIntegrationCleanup(context.Background(), " active ")
	if err != nil || len(items) != 0 {
		t.Fatalf("active directory must be excluded: items=%#v err=%v", items, err)
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
