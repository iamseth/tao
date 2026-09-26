package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/workspace"
)

func TestCleanupRemovesMergedBranchesAndSkipsRest(t *testing.T) {
	var out bytes.Buffer
	root := t.TempDir()
	manager := &fakeWorkspaceManager{
		list: []workspace.Metadata{{PlanID: "typed-live", Branch: "fix/typed-live"}},
		managedPlans: []workspace.ManagedCleanup{
			{Branch: "tao/done", WorktreePath: "/repo/.tao/workspaces/done", Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into master"},
			{Branch: "tao/wip", Status: workspace.ManagedStatusUnmerged, Reason: "not merged into master"},
			{Branch: "tao/dirty", WorktreePath: "/repo/.tao/workspaces/dirty", Status: workspace.ManagedStatusDirty, Reason: "worktree has uncommitted changes"},
			{Branch: "tao/cur", Status: workspace.ManagedStatusCurrent, Reason: "branch is currently checked out"},
		},
	}
	repo := fakeRepository{
		summaries: []plan.PlanSummary{
			{ID: "typed-plan", Workspace: &plan.Workspace{Branch: "feature/typed-plan"}},
			{ID: "typed-pr", PullRequest: &plan.PullRequest{Branch: "docs/typed-pr"}},
		},
		details: map[string]*plan.PlanDetail{
			"typed-plan": {State: plan.State{Repo: plan.Repo{Root: root}, Workspace: &plan.Workspace{Branch: "feature/typed-plan"}}},
			"typed-pr":   {State: plan.State{Repo: plan.Repo{Root: root}, Plan: plan.PlanState{PullRequest: &plan.PullRequest{Branch: "docs/typed-pr"}}}},
		},
	}
	app := App{Out: &out, Err: &out, CommandRunner: cleanupTopLevelRunner(root), WorkspaceManager: func(gotRoot string) (WorkspaceManager, error) {
		if gotRoot != root {
			t.Fatalf("expected repo root %q, got %q", root, gotRoot)
		}
		return manager, nil
	}}

	if err := app.cleanup(context.Background(), repo, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(manager.managedOwnedBranches, ","), "feature/typed-plan,docs/typed-pr,fix/typed-live"; got != want {
		t.Fatalf("cleanup ownership = %q, want %q", got, want)
	}
	text := out.String()
	for _, want := range []string{
		"removed " + root + " tao/done (worktree /repo/.tao/workspaces/done): merged into master",
		"skipped " + root + " tao/wip: not merged into master",
		"skipped " + root + " tao/dirty (worktree /repo/.tao/workspaces/dirty): worktree has uncommitted changes",
		"skipped " + root + " tao/cur: branch is currently checked out",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
	if len(manager.cleanedManaged) != 1 || manager.cleanedManaged[0].Branch != "tao/done" {
		t.Fatalf("expected only tao/done removed, got %#v", manager.cleanedManaged)
	}
	if manager.cleanManagedOptions[0].Force {
		t.Fatalf("ordinary cleanup should not force removal, options=%#v", manager.cleanManagedOptions)
	}
}

func TestCleanupPlansDirCannotClaimBranchFromAnotherRepository(t *testing.T) {
	var out bytes.Buffer
	currentRoot := t.TempDir()
	foreignRoot := t.TempDir()
	plansDir := t.TempDir()
	const branch = "feature/shared-name"
	manager := &fakeWorkspaceManager{}
	repo := fakeRepository{
		summaries: []plan.PlanSummary{{ID: "foreign-plan", Workspace: &plan.Workspace{Branch: branch}}},
		details: map[string]*plan.PlanDetail{
			"foreign-plan": {State: plan.State{Repo: plan.Repo{Root: foreignRoot}, Workspace: &plan.Workspace{Branch: branch}}},
		},
	}
	app := App{
		Out:           &out,
		Err:           &out,
		CommandRunner: cleanupTopLevelRunner(currentRoot),
		Repository: func(gotPlansDir string) Repository {
			if gotPlansDir != plansDir {
				t.Fatalf("plans dir = %q, want %q", gotPlansDir, plansDir)
			}
			return repo
		},
		WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
	}

	if err := app.Run(context.Background(), []string{"--plans-dir", plansDir, "cleanup"}); err != nil {
		t.Fatal(err)
	}
	if len(manager.managedOwnedBranches) != 0 {
		t.Fatalf("foreign plan claimed current-repository branch ownership: %v", manager.managedOwnedBranches)
	}
	if len(manager.cleanedManaged) != 0 {
		t.Fatalf("foreign plan caused current-repository cleanup: %#v", manager.cleanedManaged)
	}
}

func TestCleanupForceRemovesUnmergedButNotCurrent(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{managedPlans: []workspace.ManagedCleanup{
		{Branch: "tao/wip", Status: workspace.ManagedStatusUnmerged, Reason: "not merged into master"},
		{Branch: "tao/cur", Status: workspace.ManagedStatusCurrent, Reason: "branch is currently checked out"},
	}}
	app := App{Out: &out, Err: &out, CommandRunner: cleanupTopLevelRunner("/repo"), WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil }}

	if err := app.cleanup(context.Background(), fakeRepository{}, []string{"--force"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed /repo tao/wip") {
		t.Fatalf("expected forced removal of unmerged branch, got:\n%s", out.String())
	}
	if len(manager.cleanedManaged) != 1 || manager.cleanedManaged[0].Branch != "tao/wip" {
		t.Fatalf("force must not remove the current branch, got %#v", manager.cleanedManaged)
	}
	if !manager.cleanManagedOptions[0].Force {
		t.Fatalf("--force should map to managed cleanup force, options=%#v", manager.cleanManagedOptions)
	}
}

func TestCleanupDryRunDoesNotRemove(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{managedPlans: []workspace.ManagedCleanup{
		{Branch: "tao/done", Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into master"},
	}}
	app := App{Out: &out, Err: &out, CommandRunner: cleanupTopLevelRunner("/repo"), WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil }}

	if err := app.cleanup(context.Background(), fakeRepository{}, []string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would remove /repo tao/done: merged into master") {
		t.Fatalf("expected dry-run preview, got:\n%s", out.String())
	}
	if len(manager.cleanedManaged) != 0 {
		t.Fatalf("dry-run must not remove anything, got %#v", manager.cleanedManaged)
	}
}

func TestCleanupContinuesAfterFailures(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{
		managedPlans: []workspace.ManagedCleanup{
			{Branch: "tao/bad", Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into master"},
			{Branch: "tao/good", Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into master"},
		},
		cleanManagedErr: map[string]error{"tao/bad": errors.New("worktree locked")},
	}
	app := App{Out: &out, Err: &out, CommandRunner: cleanupTopLevelRunner("/repo"), WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil }}

	err := app.cleanup(context.Background(), fakeRepository{}, nil)
	if err == nil || !strings.Contains(err.Error(), "cleanup failed for 1 branch") {
		t.Fatalf("expected aggregate failure, got %v", err)
	}
	text := out.String()
	for _, want := range []string{"failed /repo tao/bad: worktree locked", "removed /repo tao/good"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
	if len(manager.cleanedManaged) != 1 || manager.cleanedManaged[0].Branch != "tao/good" {
		t.Fatalf("expected cleanup to continue after failure, got %#v", manager.cleanedManaged)
	}
}

func TestCleanupReportsRepositoryLocateFailure(t *testing.T) {
	var out bytes.Buffer
	runner := func(ctx context.Context, _ string, _ string, _ []string, _ io.Writer, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "not a git repository")
		return errors.New("exit status 128")
	}
	app := App{Out: &out, Err: &out, CommandRunner: runner, WorkspaceManager: func(string) (WorkspaceManager, error) {
		t.Fatal("workspace manager must not be built when repo root is unknown")
		return nil, nil
	}}
	err := app.cleanup(context.Background(), fakeRepository{}, nil)
	if err == nil || !strings.Contains(err.Error(), "locate repository") {
		t.Fatalf("expected locate repository error, got %v", err)
	}
}

func TestCleanupFromLinkedWorktreeExcludesControlActiveBatch(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, dryRun := range []bool{false, true} {
			t.Run("force="+strconv.FormatBool(force)+"/dry-run="+strconv.FormatBool(dryRun), func(t *testing.T) {
				root := newCLICommitRepo(t)
				linked := filepath.Join(root, ".tao", "workspaces", "plan")
				active := filepath.Join(root, ".tao", "integrations", "active")
				runCLICommitGit(t, root, "worktree", "add", "-b", "feature/plan", linked)
				runCLICommitGit(t, root, "worktree", "add", "-b", "tao/integration/active", active)
				runCLICommitGit(t, root, "branch", "tao/integration/orphan")
				if force {
					writeCLICommitFile(t, active, "progress.txt", "in progress\n")
					runCLICommitGit(t, active, "add", "progress.txt")
					runCLICommitGit(t, active, "commit", "-m", "chore(test): advance integration")
				}
				registry := taodata.NewRegistry(t.TempDir())
				owner, err := registry.RepoForRoot(root)
				if err != nil {
					t.Fatal(err)
				}
				store := mergepkg.NewBatchStore(registry.MergeBatchesDir(owner), registry.ActiveMergeBatchPath(owner))
				if err := store.SetActive("active"); err != nil {
					t.Fatal(err)
				}
				t.Chdir(linked)
				var out bytes.Buffer
				app := App{Out: &out, Registry: func() NoteRegistry { return registry }}
				var args []string
				if force {
					args = append(args, "--force")
				}
				if dryRun {
					args = append(args, "--dry-run")
				}
				if err := app.cleanup(context.Background(), fakeRepository{}, args); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(out.String(), "tao/integration/active") {
					t.Fatalf("active integration selected from linked checkout:\n%s", out.String())
				}
				action := "removed"
				if dryRun {
					action = "would remove"
				}
				if !strings.Contains(out.String(), action+" ") || !strings.Contains(out.String(), "tao/integration/orphan") || strings.Contains(out.String(), "integration namespaces:") {
					t.Fatalf("expected orphan cleanup, not a blanket skip:\n%s", out.String())
				}
				runCLICommitGit(t, root, "show-ref", "--verify", "refs/heads/tao/integration/active")
				if _, err := os.Stat(filepath.Join(active, ".git")); err != nil {
					t.Fatalf("active worktree was removed: %v", err)
				}
			})
		}
	}
}

// Use the real coordinator, store, ownership lock, and Git worktree boundary;
// pause only the integration phase so cleanup races are deterministic.
func TestCleanupSkipsLiveBatchOwnership(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		for _, force := range []bool{false, true} {
			t.Run("batch-dry-run="+strconv.FormatBool(dryRun)+"/force="+strconv.FormatBool(force), func(t *testing.T) {
				root := newCLICommitRepo(t)
				linked := filepath.Join(root, ".tao", "workspaces", "caller")
				runCLICommitGit(t, root, "worktree", "add", "-b", "feature/caller", linked)
				runCLICommitGit(t, root, "branch", "tao/done")
				t.Chdir(linked)
				registry := taodata.NewRegistry(t.TempDir())
				repo, err := registry.RepoForRoot(root)
				if err != nil {
					t.Fatal(err)
				}
				batchesDir := registry.MergeBatchesDir(repo)
				owner, err := mergepkg.NewBatchWorkspace(root, batchesDir, nil)
				if err != nil {
					t.Fatal(err)
				}
				store := mergepkg.NewBatchStore(batchesDir, registry.ActiveMergeBatchPath(repo))
				head := strings.TrimSpace(runCLICommitGit(t, root, "rev-parse", "HEAD"))
				snapshot := cleanupBatchSnapshot{mergepkg.BatchPreflightResult{
					RepoRoot: root, DefaultBranch: "main", DefaultStartSHA: head,
					Candidates: []mergepkg.BatchCandidate{{
						PlanID: "candidate", PlanDir: t.TempDir(), RepoRoot: root,
						Branch: "feature/caller", SourceTip: head, ReviewHead: head, ReviewBase: head,
						DefaultBranch: "main", DefaultStartSHA: head,
					}},
				}}
				paused := make(chan mergepkg.BatchState)
				ctx, cancel := context.WithCancel(context.Background())
				coordinator := mergepkg.NewBatchCoordinator(mergepkg.BatchCoordinatorSeams{
					Store: store, Workspace: owner, Discovery: snapshot, Planner: snapshot,
					Integrator: cleanupPausedIntegrator{paused: paused},
				})
				done := make(chan error, 1)
				go func() {
					_, err := coordinator.Run(ctx, mergepkg.BatchCoordinatorOptions{DryRun: dryRun})
					done <- err
				}()
				t.Cleanup(func() {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Errorf("paused batch returned %v", err)
					}
				})
				var state mergepkg.BatchState
				select {
				case state = <-paused:
				case err := <-done:
					done <- err
					t.Fatalf("batch failed before pause: %v", err)
				case <-time.After(10 * time.Second):
					t.Fatal("batch did not reach integration pause")
				}
				activeID, err := store.ActiveID()
				if err != nil || (dryRun && activeID != "") || (!dryRun && activeID != state.ID) {
					t.Fatalf("active ID=%q, dry run=%t, error=%v", activeID, dryRun, err)
				}
				path := filepath.Join(root, ".tao", "integrations", state.ID)
				if force {
					writeCLICommitFile(t, path, "progress.txt", "live integration\n")
				}
				var out bytes.Buffer
				app := App{Out: &out, Registry: func() NoteRegistry { return registry }}
				var args []string
				if force {
					args = []string{"--force"}
				}
				if err := app.cleanup(context.Background(), fakeRepository{}, args); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
					t.Fatalf("live integration worktree removed: %v\n%s", err, out.String())
				}
				runCLICommitGit(t, root, "show-ref", "--verify", "refs/heads/tao/integration/"+state.ID)
				if !strings.Contains(out.String(), "ownership is held") || !strings.Contains(out.String(), " tao/done: merged into main") {
					t.Fatalf("expected integration skip and ordinary cleanup:\n%s", out.String())
				}
			})
		}
	}
}

type cleanupBatchSnapshot struct {
	mergepkg.BatchPreflightResult
}

func (s cleanupBatchSnapshot) Discover(context.Context) (mergepkg.BatchPreflightResult, error) {
	return s.BatchPreflightResult, nil
}

func (s cleanupBatchSnapshot) PlanBatchCandidatesWithGit(context.Context, []mergepkg.BatchCandidate) (mergepkg.BatchPlanningResult, error) {
	return mergepkg.BatchPlanningResult{Ordered: s.Candidates}, nil
}

type cleanupPausedIntegrator struct {
	mergepkg.BatchCoordinatorIntegrator
	paused chan<- mergepkg.BatchState
}

func (i cleanupPausedIntegrator) Integrate(ctx context.Context, state mergepkg.BatchState, _ string, _ mergepkg.BatchIntegrateOptions) (mergepkg.BatchIntegrateResult, error) {
	select {
	case i.paused <- state:
	case <-ctx.Done():
	}
	<-ctx.Done()
	return mergepkg.BatchIntegrateResult{State: state}, ctx.Err()
}

func TestCleanupHoldsBatchOwnershipThroughEnumerationAndRemoval(t *testing.T) {
	root := cleanupRepositoryRoot(t)
	registry := taodata.NewRegistry(t.TempDir())
	registry.Runner = cleanupTopLevelRunner(root)
	repo, err := registry.RepoForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := mergepkg.NewBatchWorkspace(root, registry.MergeBatchesDir(repo), registry.Runner)
	if err != nil {
		t.Fatal(err)
	}
	assertOwned := func(phase string) {
		ownership, err := owner.AcquireOwnership(mergepkg.BatchState{}, time.Now())
		if err == nil {
			_ = ownership.Release()
			t.Errorf("fresh batch could acquire ownership during %s", phase)
		} else if !strings.Contains(err.Error(), "ownership is held") {
			t.Errorf("unexpected ownership error during %s: %v", phase, err)
		}
	}
	manager := &cleanupOwnershipManager{
		WorkspaceManager: &fakeWorkspaceManager{integrationPlans: []workspace.ManagedCleanup{{
			Branch: "tao/integration/orphan", Status: workspace.ManagedStatusClean, CanRemove: true,
		}}},
		assertOwned: assertOwned,
	}
	app := App{Out: io.Discard, CommandRunner: registry.Runner,
		Registry:         func() NoteRegistry { return registry },
		WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
	}
	if err := app.cleanup(context.Background(), fakeRepository{}, nil); err != nil {
		t.Fatal(err)
	}
	if !manager.enumerated || !manager.removed {
		t.Fatalf("enumerated=%t removed=%t", manager.enumerated, manager.removed)
	}
	ownership, err := owner.AcquireOwnership(mergepkg.BatchState{}, time.Now())
	if err != nil {
		t.Fatalf("cleanup leaked ownership: %v", err)
	}
	_ = ownership.Release()
}

type cleanupOwnershipManager struct {
	WorkspaceManager
	assertOwned func(string)
	enumerated  bool
	removed     bool
}

func (m *cleanupOwnershipManager) PlanIntegrationCleanup(ctx context.Context, activeID string) ([]workspace.ManagedCleanup, error) {
	m.enumerated = true
	m.assertOwned("integration enumeration")
	return m.WorkspaceManager.PlanIntegrationCleanup(ctx, activeID)
}

func (m *cleanupOwnershipManager) CleanManaged(ctx context.Context, item workspace.ManagedCleanup, options workspace.CleanOptions) error {
	m.removed = true
	m.assertOwned("integration removal")
	return m.WorkspaceManager.CleanManaged(ctx, item, options)
}

func TestCleanupIntegrationNamespaces(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(strconv.FormatBool(dryRun), func(t *testing.T) {
			var out bytes.Buffer
			root := cleanupRepositoryRoot(t)
			registry := taodata.NewRegistry(t.TempDir())
			registry.Runner = cleanupTopLevelRunner(root)
			current, err := registry.Current(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			store := mergepkg.NewBatchStore(registry.MergeBatchesDir(current), registry.ActiveMergeBatchPath(current))
			if err := store.SetActive("batch-active"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, ".tao", "integrations", "orphan")
			manager := &fakeWorkspaceManager{integrationPlans: []workspace.ManagedCleanup{
				{Branch: "tao/integration/orphan", WorktreePath: path, Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into main"},
			}}
			app := App{Out: &out, CommandRunner: registry.Runner,
				Registry:         func() NoteRegistry { return registry },
				WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
			}
			var args []string
			if dryRun {
				args = []string{"--dry-run"}
			}
			if err := app.cleanup(context.Background(), fakeRepository{}, args); err != nil {
				t.Fatal(err)
			}
			if !manager.integrationCalled || manager.activeBatchID != "batch-active" {
				t.Fatalf("integration called=%t active ID=%q", manager.integrationCalled, manager.activeBatchID)
			}
			action := "removed"
			if dryRun {
				action = "would remove"
			}
			want := action + " " + root + " tao/integration/orphan (worktree " + path + "): merged into main\n"
			if out.String() != want {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
			if dryRun {
				if len(manager.cleanedManaged) != 0 {
					t.Fatalf("dry-run removed items: %#v", manager.cleanedManaged)
				}
			} else if len(manager.cleanedManaged) != 1 || manager.cleanedManaged[0].Branch != "tao/integration/orphan" || manager.cleanManagedOptions[0].Force {
				t.Fatalf("expected non-forced orphan removal: %#v, %#v", manager.cleanedManaged, manager.cleanManagedOptions)
			}
		})
	}
}

func TestCleanupIntegrationInactiveIdentity(t *testing.T) {
	for _, content := range []string{"missing", "", "{}", `{"batch_id":""}`, "malformed"} {
		t.Run(content, func(t *testing.T) {
			var out bytes.Buffer
			root := cleanupRepositoryRoot(t)
			dataHome := t.TempDir()
			t.Setenv("TAO_DATA_HOME", dataHome)
			registry := taodata.NewRegistry(dataHome)
			registry.Runner = cleanupTopLevelRunner(root)
			current, err := registry.Current(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if content != "missing" {
				path := registry.ActiveMergeBatchPath(current)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manager := &fakeWorkspaceManager{activeBatchID: "not called"}
			app := App{Out: &out, CommandRunner: registry.Runner,
				WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
			}
			if err := app.cleanup(context.Background(), fakeRepository{}, nil); err != nil {
				t.Fatal(err)
			}
			if !manager.integrationCalled || manager.activeBatchID != "" || out.Len() != 0 {
				t.Fatalf("integration called=%t active ID=%q output=%q", manager.integrationCalled, manager.activeBatchID, out.String())
			}
		})
	}
}

func TestCleanupUnknownControlIdentitySkipsIntegrations(t *testing.T) {
	for _, commonDir := range []string{"", "missing/.git", ".", "git-error"} {
		t.Run(commonDir, func(t *testing.T) {
			root := t.TempDir()
			var out bytes.Buffer
			manager := &fakeWorkspaceManager{managedPlans: []workspace.ManagedCleanup{
				{Branch: "tao/done", Status: workspace.ManagedStatusClean, CanRemove: true},
			}}
			registry := taodata.NewRegistry(t.TempDir())
			registry.Runner = cleanupTopLevelRunner(root)
			runner := func(ctx context.Context, dir, name string, args []string, stdout, stderr io.Writer) error {
				if cleanupCommandKey(args) == "rev-parse --git-common-dir" {
					if commonDir == "git-error" {
						return errors.New("common directory unavailable")
					}
					_, err := io.WriteString(stdout, commonDir+"\n")
					return err
				}
				return registry.Runner(ctx, dir, name, args, stdout, stderr)
			}
			app := App{Out: &out, CommandRunner: runner,
				Registry:         func() NoteRegistry { return registry },
				WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
			}
			if err := app.cleanup(context.Background(), fakeRepository{}, []string{"--force"}); err != nil {
				t.Fatal(err)
			}
			if manager.integrationCalled || !strings.Contains(out.String(), "integration namespaces:") {
				t.Fatalf("unknown identity did not fail closed: called=%t output=%s", manager.integrationCalled, out.String())
			}
			if len(manager.cleanedManaged) != 1 || manager.cleanedManaged[0].Branch != "tao/done" {
				t.Fatalf("ordinary plan cleanup did not continue: %#v", manager.cleanedManaged)
			}
		})
	}
}

type cleanupFailingRegistry struct {
	taodata.Registry
}

func (cleanupFailingRegistry) RepoForRoot(string) (taodata.Repo, error) {
	return taodata.Repo{}, errors.New("registry unavailable")
}

func TestCleanupRegistryFailureStillRemovesPlanItems(t *testing.T) {
	var out bytes.Buffer
	root := cleanupRepositoryRoot(t)
	manager := &fakeWorkspaceManager{managedPlans: []workspace.ManagedCleanup{
		{Branch: "tao/done", Status: workspace.ManagedStatusClean, CanRemove: true},
	}}
	app := App{Out: &out, CommandRunner: cleanupTopLevelRunner(root),
		Registry:         func() NoteRegistry { return cleanupFailingRegistry{} },
		WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
	}
	if err := app.cleanup(context.Background(), fakeRepository{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skipped "+root+" integration namespaces:") || !strings.Contains(out.String(), "registry unavailable\n") || !strings.Contains(out.String(), "removed "+root+" tao/done\n") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if manager.integrationCalled || len(manager.cleanedManaged) != 1 || manager.cleanedManaged[0].Branch != "tao/done" {
		t.Fatalf("integration called=%t removed=%#v", manager.integrationCalled, manager.cleanedManaged)
	}
}

func TestCleanupForceSkipsUnregisteredIntegration(t *testing.T) {
	var out bytes.Buffer
	root := cleanupRepositoryRoot(t)
	registry := taodata.NewRegistry(t.TempDir())
	registry.Runner = cleanupTopLevelRunner(root)
	manager := &fakeWorkspaceManager{integrationPlans: []workspace.ManagedCleanup{
		{Branch: "tao/integration/unregistered", Status: workspace.ManagedStatusUnregistered, Reason: "directory is not a registered worktree"},
	}}
	app := App{Out: &out, CommandRunner: registry.Runner,
		Registry:         func() NoteRegistry { return registry },
		WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil },
	}
	if err := app.cleanup(context.Background(), fakeRepository{}, []string{"--force"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skipped "+root+" tao/integration/unregistered: directory is not a registered worktree\n") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if len(manager.cleanedManaged) != 0 {
		t.Fatalf("force removed unregistered directory: %#v", manager.cleanedManaged)
	}
}

func cleanupRepositoryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// cleanupTopLevelRunner answers Git identity queries for a control checkout and
// ignores other commands, so cleanup tests can drive a fixed repository root.
func cleanupTopLevelRunner(root string) CommandRunner {
	return func(ctx context.Context, _ string, _ string, args []string, stdout io.Writer, _ io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch cleanupCommandKey(args) {
		case "rev-parse --show-toplevel":
			_, _ = io.WriteString(stdout, root+"\n")
		case "rev-parse --git-common-dir":
			_, _ = io.WriteString(stdout, ".git\n")
		}
		return nil
	}
}

func cleanupCommandKey(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	return strings.Join(args, " ")
}
