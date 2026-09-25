package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

type removeFailingGit struct {
	workspaceMutationGit
	err error
}

func (g removeFailingGit) RemoveWorktree(context.Context, string, bool) error {
	return g.err
}

type fixedStatusGit struct {
	workspaceStatusGit
	status gitops.WorktreeStatus
}

func (g fixedStatusGit) WorktreeStatus(context.Context, string) (gitops.WorktreeStatus, error) {
	return g.status, nil
}

type fixedCleanupGit struct {
	workspaceCleanupGit
	branchExists bool
	branchMerged bool
}

func (g fixedCleanupGit) BranchExists(context.Context, string) (bool, error) {
	return g.branchExists, nil
}

func (g fixedCleanupGit) BranchMerged(context.Context, string) (bool, error) {
	return g.branchMerged, nil
}

func TestRemoveIntegrationWorktreeCleanupFailurePreservesResources(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	start := strings.TrimSpace(runGit(t, repo.path, "rev-parse", "master"))
	integration, err := manager.CreateIntegration(context.Background(), "batch-a", start)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := manager.git
	capabilities.mutation = removeFailingGit{
		workspaceMutationGit: capabilities.mutation,
		err:                  errors.New("injected worktree cleanup failure"),
	}
	failing := newManager(manager.repoRoot, manager.config, capabilities)
	if err := failing.RemoveIntegration(context.Background(), "batch-a"); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if _, err := os.Stat(integration.Path); err != nil {
		t.Fatalf("failed cleanup removed integration path: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, repo.path, "branch", "--list", integration.Branch)); got == "" {
		t.Fatal("failed cleanup removed integration branch")
	}
}

func TestDecideManagedCleanupRecordsMergeMechanism(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	ctx := context.Background()

	commitOnBranch(t, repo.path, "tao/ancestral", "ancestral.txt", "ancestral\n")
	runGit(t, repo.path, "merge", "--ff-only", "tao/ancestral")
	commitOnBranch(t, repo.path, "tao/squashed", "squashed.txt", "squashed\n")
	runGit(t, repo.path, "merge", "--squash", "tao/squashed")
	runGit(t, repo.path, "commit", "-m", "squash merge")

	ancestral, err := manager.decideManagedCleanup(ctx, "tao/ancestral", "master", "master", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ancestral.CanRemove || ancestral.MergedNonAncestral {
		t.Fatalf("ancestry-merged cleanup = %#v, want removable ancestral evidence", ancestral)
	}

	squashed, err := manager.decideManagedCleanup(ctx, "tao/squashed", "master", "master", "")
	if err != nil {
		t.Fatal(err)
	}
	if !squashed.CanRemove || !squashed.MergedNonAncestral {
		t.Fatalf("squash-merged cleanup = %#v, want removable non-ancestral evidence", squashed)
	}
}

func TestPrepareCreatesWorktreeWithDefaultBranchName(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)

	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "20260528-0140-workspace-manager-refactor", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if metadata.Path != filepath.Join(repo.path, ".tao", "workspaces", "20260528-0140-workspace-manager-refactor") {
		t.Fatalf("unexpected path: %q", metadata.Path)
	}
	if metadata.Branch != "tao/20260528-0140-workspace-manager-refactor" {
		t.Fatalf("unexpected branch: %q", metadata.Branch)
	}
	if metadata.BaseBranch != "master" || metadata.BaseSHA == "" {
		t.Fatalf("base metadata was not recorded: %#v", metadata)
	}
	if !metadata.Created || metadata.Reused || metadata.Dirty || metadata.Missing {
		t.Fatalf("unexpected lifecycle metadata: %#v", metadata)
	}
	if _, err := os.Stat(filepath.Join(metadata.Path, ".git")); err != nil {
		t.Fatalf("expected worktree checkout: %v", err)
	}
}

func TestPrepareReusesUnrecordedMatchingTypedWorktree(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	options := PrepareOptions{
		PlanID: "20260812-183359-native-pr-format", Branch: "feature/native-pr-format", RequireNewBranch: true, BaseBranch: "master",
	}

	first, err := manager.Prepare(context.Background(), options)
	if err != nil {
		t.Fatalf("initial Prepare failed: %v", err)
	}
	second, err := manager.Prepare(context.Background(), options)
	if err != nil {
		t.Fatalf("Prepare recovery failed: %v", err)
	}
	if second.Path != first.Path || second.Branch != first.Branch || !second.Reused || second.Created {
		t.Fatalf("recovered workspace metadata = %#v, initial = %#v", second, first)
	}
}

func TestPrepareRejectsUnownedTypedBranchBeforeWorktreeCreation(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	branch := "feature/native-pr-format"
	runGit(t, repo.path, "branch", branch, "master")

	_, err := manager.Prepare(context.Background(), PrepareOptions{
		PlanID: "20260812-183359-native-pr-format", Branch: branch, RequireNewBranch: true, BaseBranch: "master",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists without durable ownership") {
		t.Fatalf("Prepare collision error = %v", err)
	}
	workspacePath := filepath.Join(repo.path, ".tao", "workspaces", "20260812-183359-native-pr-format")
	if _, statErr := os.Stat(workspacePath); !os.IsNotExist(statErr) {
		t.Fatalf("collision path created or has unexpected status: %v", statErr)
	}
}

func TestPrepareRejectsUnownedTypedRemoteTrackingBranchBeforeWorktreeCreation(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	branch := "feature/native-pr-format"
	runGit(t, repo.path, "update-ref", "refs/remotes/origin/"+branch, "master")
	if got := strings.TrimSpace(runGit(t, repo.path, "branch", "--list", branch)); got != "" {
		t.Fatalf("local branch unexpectedly exists: %q", got)
	}

	_, err := manager.Prepare(context.Background(), PrepareOptions{
		PlanID: "20260812-183359-native-pr-format", Branch: branch, RequireNewBranch: true, BaseBranch: "master",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists without durable ownership") {
		t.Fatalf("Prepare remote collision error = %v", err)
	}
	workspacePath := filepath.Join(repo.path, ".tao", "workspaces", "20260812-183359-native-pr-format")
	if _, statErr := os.Stat(workspacePath); !os.IsNotExist(statErr) {
		t.Fatalf("remote collision path created or has unexpected status: %v", statErr)
	}
}

func TestPrepareRejectsRemoteBranchCreatedAfterLastFetch(t *testing.T) {
	repo := newTestRepo(t)
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare")
	runGit(t, repo.path, "remote", "add", "origin", remote)
	runGit(t, repo.path, "push", "--set-upstream", "origin", "master")
	runGit(t, repo.path, "fetch", "origin")

	branch := "feature/native-pr-format"
	runGit(t, remote, "branch", branch, "master")
	if got := strings.TrimSpace(runGit(t, repo.path, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"+branch)); got != "" {
		t.Fatalf("remote-tracking branch unexpectedly exists before Prepare: %q", got)
	}

	manager := newTestManager(t, repo.path)
	_, err := manager.Prepare(context.Background(), PrepareOptions{
		PlanID: "20260812-183359-native-pr-format", Branch: branch, RequireNewBranch: true, BaseBranch: "master",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists without durable ownership") {
		t.Fatalf("Prepare live remote collision error = %v", err)
	}
	workspacePath := filepath.Join(repo.path, ".tao", "workspaces", "20260812-183359-native-pr-format")
	if _, statErr := os.Stat(workspacePath); !os.IsNotExist(statErr) {
		t.Fatalf("live remote collision path created or has unexpected status: %v", statErr)
	}
	if got := strings.TrimSpace(runGit(t, repo.path, "branch", "--list", branch)); got != "" {
		t.Fatalf("live remote collision created local branch: %q", got)
	}
}

func TestPrepareReusesBranchWithDurableOwnership(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	branch := "tao/existing-recorded-branch"
	runGit(t, repo.path, "branch", branch, "master")

	metadata, err := manager.Prepare(context.Background(), PrepareOptions{
		PlanID: "20260812-183359-native-pr-format", Branch: branch, BaseBranch: "master",
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Branch != branch || !metadata.Reused || metadata.Created {
		t.Fatalf("owned branch metadata = %#v", metadata)
	}
}

func TestStatusUsesExpectedTypedBranchForMissingWorkspace(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)

	metadata, err := manager.Status(context.Background(), "20260812-183359-native-pr-format", "feature/native-pr-format")
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.Missing || metadata.Branch != "feature/native-pr-format" {
		t.Fatalf("typed missing status = %#v", metadata)
	}
}

func TestPrepareReusesExistingMatchingWorktreeAndReportsDirty(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)

	first, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write dirty file: %v", err)
	}

	second, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("Prepare reuse failed: %v", err)
	}
	if !second.Reused || second.Created || !second.Dirty {
		t.Fatalf("expected dirty reuse metadata, got %#v", second)
	}
	if _, err := os.Stat(filepath.Join(second.Path, "dirty.txt")); err != nil {
		t.Fatalf("dirty worktree was modified: %v", err)
	}
}

func TestPrepareRejectsBranchCheckedOutElsewhere(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)

	if _, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", Branch: "tao/shared", BaseBranch: "master"}); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	_, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-b", Branch: "tao/shared", BaseBranch: "master"})
	if err == nil {
		t.Fatalf("expected branch conflict")
	}
}

func TestStatusAndPlanCleanForMissingDirtyAndCleanWorkspaces(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)

	missing, err := manager.Status(context.Background(), "missing-plan")
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !missing.Missing || missing.Path == "" {
		t.Fatalf("expected missing status, got %#v", missing)
	}
	missingClean, err := manager.PlanClean(context.Background(), "missing-plan")
	if err != nil {
		t.Fatalf("PlanClean missing failed: %v", err)
	}
	if missingClean.CanRemove || missingClean.Reason == "" {
		t.Fatalf("unexpected missing clean plan: %#v", missingClean)
	}

	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	clean, err := manager.PlanClean(context.Background(), "plan-a")
	if err != nil {
		t.Fatalf("PlanClean clean failed: %v", err)
	}
	if !clean.CanRemove || clean.Dirty || clean.Path != metadata.Path {
		t.Fatalf("unexpected clean plan: %#v", clean)
	}
	if clean.Branch == "" || clean.Status != "clean" || len(clean.Actions) != 1 {
		t.Fatalf("expected dry-run cleanup details, got %#v", clean)
	}

	if err := os.WriteFile(filepath.Join(metadata.Path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write dirty file: %v", err)
	}
	dirty, err := manager.PlanClean(context.Background(), "plan-a")
	if err != nil {
		t.Fatalf("PlanClean dirty failed: %v", err)
	}
	if dirty.CanRemove || !dirty.Dirty {
		t.Fatalf("unexpected dirty clean plan: %#v", dirty)
	}
}

func TestPlanCleanRefusesProtectedAndUnmergedByDefault(t *testing.T) {
	repo := newTestRepo(t)
	workspacePath := filepath.Join(repo.path, ".tao", "workspaces", "current-plan")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil { //nolint:gosec // G301: test workspace dir
		t.Fatal(err)
	}
	manager := newManager(repo.path, DefaultConfig(), managerGitCapabilities{
		status: fixedStatusGit{status: gitops.WorktreeStatus{Branch: "master", HEAD: "head"}},
		cleanup: fixedCleanupGit{
			branchExists: true,
			branchMerged: true,
		},
	})
	protected, err := manager.PlanClean(context.Background(), "current-plan")
	if err != nil {
		t.Fatalf("PlanClean protected failed: %v", err)
	}
	if protected.CanRemove || !protected.ProtectedBranch || protected.Status != "protected-branch" {
		t.Fatalf("expected protected branch refusal, got %#v", protected)
	}

	manager = newTestManager(t, repo.path)
	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-unmerged", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadata.Path, "done.txt"), []byte("done\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write file: %v", err)
	}
	runGit(t, metadata.Path, "add", "done.txt")
	runGit(t, metadata.Path, "commit", "-m", "workspace change")

	unmerged, err := manager.PlanClean(context.Background(), "plan-unmerged")
	if err != nil {
		t.Fatalf("PlanClean unmerged failed: %v", err)
	}
	if unmerged.CanRemove || unmerged.Status != "unmerged" {
		t.Fatalf("expected unmerged refusal, got %#v", unmerged)
	}
	if _, err := manager.Clean(context.Background(), cleanupPlanDetail("plan-unmerged"), CleanOptions{}); err == nil {
		t.Fatal("expected unmerged workspace clean to require force")
	}
	if _, err := manager.Clean(context.Background(), cleanupPlanDetail("plan-unmerged"), CleanOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "--force-dirty") {
		t.Fatalf("Force-only unmerged clean error = %v", err)
	}
	if _, err := os.Stat(metadata.Path); err != nil {
		t.Fatalf("refused unmerged workspace should remain: %v", err)
	}
	if _, err := manager.Clean(context.Background(), cleanupPlanDetail("plan-unmerged"), CleanOptions{Force: true, ForceDirty: true}); err != nil {
		t.Fatalf("forced unmerged clean failed: %v", err)
	}
	if _, err := os.Stat(metadata.Path); !os.IsNotExist(err) {
		t.Fatalf("expected workspace removal, stat error = %v", err)
	}
}

func TestListReturnsConfiguredWorkspaceWorktrees(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)

	if _, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"}); err != nil {
		t.Fatalf("Prepare plan-a failed: %v", err)
	}
	if _, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-b", BaseBranch: "master"}); err != nil {
		t.Fatalf("Prepare plan-b failed: %v", err)
	}

	workspaces, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %#v", workspaces)
	}
	ids := map[string]bool{}
	for _, workspace := range workspaces {
		ids[workspace.PlanID] = true
		if workspace.Path == "" || workspace.Branch == "" || workspace.Missing {
			t.Fatalf("unexpected workspace metadata: %#v", workspace)
		}
	}
	if !ids["plan-a"] || !ids["plan-b"] {
		t.Fatalf("missing workspace IDs: %#v", ids)
	}
}

func TestDirectChildResolvesSymlinkedRoot(t *testing.T) {
	physicalRoot := filepath.Join(t.TempDir(), "physical")
	child := filepath.Join(physicalRoot, "plan-a")
	if err := os.MkdirAll(child, 0o750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(physicalRoot, alias); err != nil {
		t.Fatal(err)
	}

	planID, ok := directChild(alias, child)
	if !ok || planID != "plan-a" {
		t.Fatalf("directChild = %q, %v; want plan-a, true", planID, ok)
	}
}

func TestCleanRemovesCleanWorkspaceAndRefusesDirtyWithoutForce(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)

	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if _, err := manager.Clean(context.Background(), cleanupPlanDetail("plan-a"), CleanOptions{}); err != nil {
		t.Fatalf("Clean failed: %v", err)
	}
	if _, err := os.Stat(metadata.Path); !os.IsNotExist(err) {
		t.Fatalf("expected workspace to be removed, stat err=%v", err)
	}

	dirty, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-dirty", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("Prepare dirty failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirty.Path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write dirty file: %v", err)
	}
	if _, err := manager.Clean(context.Background(), cleanupPlanDetail("plan-dirty"), CleanOptions{}); err == nil {
		t.Fatal("expected dirty workspace clean to require force")
	}
	if _, err := os.Stat(dirty.Path); err != nil {
		t.Fatalf("dirty workspace should remain: %v", err)
	}
}

func cleanupPlanDetail(id string) *plan.PlanDetail {
	return &plan.PlanDetail{State: plan.State{Status: plan.StatusCompleted, Plan: plan.PlanState{ID: id}}}
}

type recordingCleanupMutation struct {
	calls []string
	path  string
	force bool
}

func (g *recordingCleanupMutation) AddWorktree(context.Context, string, string, string, bool) error {
	g.calls = append(g.calls, "add")
	return nil
}

func (g *recordingCleanupMutation) DeleteBranch(context.Context, string, bool) error {
	g.calls = append(g.calls, "delete")
	return nil
}

func (g *recordingCleanupMutation) RemoveWorktree(_ context.Context, path string, force bool) error {
	g.calls = append(g.calls, "remove")
	g.path, g.force = path, force
	return nil
}

func TestCleanEnforcesPolicyBeforeGitMutation(t *testing.T) {
	all := CleanOptions{Force: true, ForceActive: true, ForceDirty: true}
	for _, tt := range []struct {
		name      string
		status    string
		current   bool
		dirty     bool
		unmerged  bool
		missing   bool
		protected bool
		options   CleanOptions
		wantError string
	}{
		{name: "clean"},
		{name: "clean forced", options: CleanOptions{Force: true}},
		{name: "active status", status: plan.StatusInProgress, wantError: "--force-active"},
		{name: "active force only", status: plan.StatusInProgress, options: CleanOptions{Force: true}, wantError: "--force-active"},
		{name: "active dirty override only", status: plan.StatusInProgress, options: CleanOptions{Force: true, ForceDirty: true}, wantError: "--force-active"},
		{name: "current slice", current: true, wantError: "--force-active"},
		{name: "active override", status: plan.StatusInProgress, options: CleanOptions{ForceActive: true}},
		{name: "current slice override", current: true, options: CleanOptions{ForceActive: true}},
		{name: "abandoned current slice", status: plan.StatusAbandoned, current: true},
		{name: "unmerged", unmerged: true, wantError: "--force-dirty"},
		{name: "unmerged force only", unmerged: true, options: CleanOptions{Force: true}, wantError: "--force-dirty"},
		{name: "unmerged managed override ignored", unmerged: true, options: CleanOptions{Force: true, AllowNonAncestralBranch: true}, wantError: "--force-dirty"},
		{name: "unmerged active override only", unmerged: true, options: CleanOptions{ForceActive: true}, wantError: "--force-dirty"},
		{name: "unmerged override", unmerged: true, options: CleanOptions{ForceDirty: true}},
		{name: "dirty", dirty: true, wantError: "--force-dirty"},
		{name: "dirty force only", dirty: true, options: CleanOptions{Force: true}, wantError: "--force-dirty"},
		{name: "dirty override", dirty: true, options: CleanOptions{ForceDirty: true}},
		{name: "missing", missing: true, wantError: "missing workspace"},
		{name: "missing cannot override", missing: true, options: all, wantError: "missing workspace"},
		{name: "protected", protected: true, wantError: "protected branch"},
		{name: "protected dirty cannot override", protected: true, dirty: true, options: all, wantError: "protected branch"},
		{name: "active dirty", status: plan.StatusInProgress, dirty: true, options: CleanOptions{Force: true}, wantError: "--force-active"},
		{name: "active dirty needs dirty override", status: plan.StatusInProgress, dirty: true, options: CleanOptions{Force: true, ForceActive: true}, wantError: "--force-dirty"},
		{name: "active unmerged needs dirty override", current: true, unmerged: true, options: CleanOptions{Force: true, ForceActive: true}, wantError: "--force-dirty"},
		{name: "active dirty unmerged needs active override", current: true, dirty: true, unmerged: true, options: CleanOptions{Force: true, ForceDirty: true}, wantError: "--force-active"},
		{name: "active dirty unmerged overrides", current: true, dirty: true, unmerged: true, options: all},
		{name: "abandoned still needs dirty override", status: plan.StatusAbandoned, current: true, dirty: true, options: CleanOptions{Force: true}, wantError: "--force-dirty"},
		{name: "abandoned still needs unmerged override", status: plan.StatusAbandoned, current: true, unmerged: true, options: CleanOptions{Force: true}, wantError: "--force-dirty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".tao", "workspaces", "plan-a")
			if !tt.missing {
				if err := os.MkdirAll(path, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			branch := "tao/plan-a"
			if tt.protected {
				branch = "master"
			}
			mutation := &recordingCleanupMutation{}
			manager := newManager(root, DefaultConfig(), managerGitCapabilities{
				status:   fixedStatusGit{status: gitops.WorktreeStatus{Branch: branch, Dirty: tt.dirty}},
				cleanup:  fixedCleanupGit{branchExists: true, branchMerged: !tt.unmerged},
				mutation: mutation,
			})
			detail := cleanupPlanDetail("plan-a")
			if tt.status != "" {
				detail.State.Status = tt.status
			}
			if tt.current {
				current := "001-work"
				detail.State.Plan.CurrentSlice = &current
			}
			preview, err := manager.PlanClean(context.Background(), detail.State.Plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(mutation.calls) != 0 {
				t.Fatalf("PlanClean mutated Git: %v", mutation.calls)
			}
			options := tt.options
			options.Force = true
			previewErr := ValidateClean(detail, preview, options)
			decision, err := manager.Clean(context.Background(), detail, tt.options)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Clean error = %v, want %q", err, tt.wantError)
				}
				if previewErr == nil || previewErr.Error() != err.Error() {
					t.Fatalf("preview/removal validation differs: %v / %v", previewErr, err)
				}
				if len(mutation.calls) != 0 {
					t.Fatalf("refused cleanup mutated Git: %v", mutation.calls)
				}
				return
			}
			if err != nil || previewErr != nil {
				t.Fatalf("Clean error = %v; preview error = %v", err, previewErr)
			}
			if len(mutation.calls) != 1 || mutation.calls[0] != "remove" || mutation.path != path || mutation.force != tt.dirty || decision.PlanID != detail.State.Plan.ID {
				t.Fatalf("unexpected cleanup mutation: %+v, decision: %+v", mutation, decision)
			}
		})
	}
}

func TestCleanRejectsMissingContextBeforeGitAccess(t *testing.T) {
	for _, detail := range []*plan.PlanDetail{nil, {}, cleanupPlanDetail(" ")} {
		manager := &Manager{} // No Git capabilities: missing context must fail first.
		if _, err := manager.Clean(context.Background(), detail, CleanOptions{Force: true, ForceActive: true, ForceDirty: true}); err == nil || !strings.Contains(err.Error(), "explicit plan detail") {
			t.Fatalf("Clean error = %v, want missing context refusal", err)
		}
	}
}

func TestCleanRecomputesPreviewBeforeRemoval(t *testing.T) {
	for _, change := range []string{"dirty", "unmerged", "protected", "missing"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".tao", "workspaces", "plan-a")
			if err := os.MkdirAll(path, 0o750); err != nil {
				t.Fatal(err)
			}
			mutation := &recordingCleanupMutation{}
			manager := newManager(root, DefaultConfig(), managerGitCapabilities{
				status:   fixedStatusGit{status: gitops.WorktreeStatus{Branch: "tao/plan-a"}},
				cleanup:  fixedCleanupGit{branchExists: true, branchMerged: true},
				mutation: mutation,
			})
			preview, err := manager.PlanClean(context.Background(), "plan-a")
			if err != nil || !preview.CanRemove {
				t.Fatalf("initial preview = %+v, %v", preview, err)
			}
			switch change {
			case "dirty":
				manager.git.status = fixedStatusGit{status: gitops.WorktreeStatus{Branch: "tao/plan-a", Dirty: true}}
			case "unmerged":
				manager.git.cleanup = fixedCleanupGit{branchExists: true}
			case "protected":
				manager.git.status = fixedStatusGit{status: gitops.WorktreeStatus{Branch: "master"}}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.Clean(context.Background(), cleanupPlanDetail("plan-a"), CleanOptions{Force: true}); err == nil {
				t.Fatal("Clean reused stale preview")
			}
			if len(mutation.calls) != 0 {
				t.Fatalf("refused cleanup mutated Git: %v", mutation.calls)
			}
		})
	}
}

func TestResolvePlanWorktree(t *testing.T) {
	const repoRoot = "/repo/root"
	const worktreePath = "/repo/worktrees/plan-a"

	tests := []struct {
		name         string
		detail       *plan.PlanDetail
		wantPath     string
		wantSeparate bool
	}{
		{
			name:   "nil detail returns zero value",
			detail: nil,
		},
		{
			name: "nil workspace returns zero value",
			detail: &plan.PlanDetail{
				State: plan.State{Repo: plan.Repo{Root: repoRoot}},
			},
		},
		{
			name: "current strategy returns zero value",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Path: repoRoot},
				},
			},
		},
		{
			name: "worktree strategy with empty path and no config fallback returns zero value",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: ""},
				},
			},
			wantPath:     "",
			wantSeparate: false,
		},
		{
			name: "separate worktree path is cleaned and reports Separate true",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: worktreePath + "/"},
				},
			},
			wantPath:     worktreePath,
			wantSeparate: true,
		},
		{
			name: "worktree path equal to repo root reports Separate false",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: repoRoot},
				},
			},
			wantPath:     repoRoot,
			wantSeparate: false,
		},
		{
			name: "empty repo root with worktree path reports Separate true",
			detail: &plan.PlanDetail{
				State: plan.State{
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: worktreePath},
				},
			},
			wantPath:     worktreePath,
			wantSeparate: true,
		},
		{
			name: "falls back to config-derived path when recorded path is empty",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Plan:      plan.PlanState{ID: "plan-a"},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree},
				},
			},
			wantPath:     repoRoot + "/.tao/workspaces/plan-a",
			wantSeparate: true,
		},
		{
			// The plan-recorded root wins over today's config: a plan created
			// under an older or custom root must resolve to the directory that
			// was actually created, not one derived from the current config.
			name: "recorded relative workspace root wins over config",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Plan:      plan.PlanState{ID: "plan-a"},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: "custom/worktrees"},
				},
			},
			wantPath:     repoRoot + "/custom/worktrees/plan-a",
			wantSeparate: true,
		},
		{
			name: "recorded absolute workspace root wins over config",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Plan:      plan.PlanState{ID: "plan-a"},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: "/srv/worktrees"},
				},
			},
			wantPath:     "/srv/worktrees/plan-a",
			wantSeparate: true,
		},
		{
			name: "recorded path wins over recorded root",
			detail: &plan.PlanDetail{
				State: plan.State{
					Repo:      plan.Repo{Root: repoRoot},
					Plan:      plan.PlanState{ID: "plan-a"},
					Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: worktreePath, Root: "custom/worktrees"},
				},
			},
			wantPath:     worktreePath,
			wantSeparate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolvePlanWorktree(tt.detail, DefaultConfig())
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if got.Separate != tt.wantSeparate {
				t.Errorf("Separate = %v, want %v", got.Separate, tt.wantSeparate)
			}
		})
	}
}

func newTestManager(t *testing.T, repoRoot string) *Manager {
	t.Helper()
	manager, err := NewManager(Options{RepoRoot: repoRoot})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	return manager
}
