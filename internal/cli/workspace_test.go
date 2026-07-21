package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type fakeWorkspaceManager struct {
	prepareOptions      workspace.PrepareOptions
	prepared            workspace.Metadata
	status              workspace.Metadata
	list                []workspace.Metadata
	cleanPlan           workspace.CleanPlan
	cleanOptions        workspace.CleanOptions
	cleanCalled         bool
	managedPlans        []workspace.ManagedCleanup
	managedErr          error
	cleanedManaged      []workspace.ManagedCleanup
	cleanManagedOptions []workspace.CleanOptions
	cleanManagedErr     map[string]error
}

func (f *fakeWorkspaceManager) Prepare(ctx context.Context, options workspace.PrepareOptions) (workspace.Metadata, error) {
	f.prepareOptions = options
	return f.prepared, ctx.Err()
}

func (f *fakeWorkspaceManager) Status(ctx context.Context, planID string) (workspace.Metadata, error) {
	metadata := f.status
	metadata.PlanID = planID
	return metadata, ctx.Err()
}

func (f *fakeWorkspaceManager) List(ctx context.Context) ([]workspace.Metadata, error) {
	return f.list, ctx.Err()
}

func (f *fakeWorkspaceManager) PlanClean(ctx context.Context, planID string) (workspace.CleanPlan, error) {
	plan := f.cleanPlan
	plan.PlanID = planID
	return plan, ctx.Err()
}

func (f *fakeWorkspaceManager) Clean(ctx context.Context, planID string, options workspace.CleanOptions) (workspace.CleanPlan, error) {
	f.cleanCalled = true
	f.cleanOptions = options
	plan := f.cleanPlan
	plan.PlanID = planID
	return plan, ctx.Err()
}

func (f *fakeWorkspaceManager) PlanManagedCleanup(ctx context.Context) ([]workspace.ManagedCleanup, error) {
	if f.managedErr != nil {
		return nil, f.managedErr
	}
	return f.managedPlans, ctx.Err()
}

func (f *fakeWorkspaceManager) CleanManaged(ctx context.Context, item workspace.ManagedCleanup, options workspace.CleanOptions) error {
	if err := f.cleanManagedErr[item.Branch]; err != nil {
		return err
	}
	f.cleanedManaged = append(f.cleanedManaged, item)
	f.cleanManagedOptions = append(f.cleanManagedOptions, options)
	return ctx.Err()
}

func TestWorkspaceListRendersManagerWorkspaces(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{list: []workspace.Metadata{
		{PlanID: "plan-a", Branch: "tao/plan-a", Path: "/repo/.tao/workspaces/plan-a", Dirty: true},
		{PlanID: "plan-b", Branch: "tao/plan-b", Path: "/repo/.tao/workspaces/plan-b", Missing: true},
	}}
	app := workspaceTestApp(&out, manager)
	if err := app.workspace(context.Background(), fakeRepository{}, []string{"list"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PLAN ID  BRANCH  STATE  PATH", "plan-a  tao/plan-a  dirty", "plan-b  tao/plan-b  missing"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in workspace list output %q", want, out.String())
		}
	}
}

func TestWorkspaceStatusRendersOptionalMetadata(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{status: workspace.Metadata{Path: "/repo/.tao/workspaces/plan-a", Branch: "tao/plan-a", BaseBranch: "main", BaseSHA: "abc123", DependencyStatus: "ready", DependencyCommand: "npm ci", Reused: true}}
	app := workspaceTestApp(&out, manager)
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", plan.StatusPlanned)}}
	if err := app.workspace(context.Background(), repo, []string{"status", "plan-a"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Plan ID: plan-a", "State: reused", "Base Branch: main", "Base SHA: abc123", "Dependency Preparation: ready", "Dependency Command: npm ci"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in workspace status output %q", want, out.String())
		}
	}
}

func TestWorkspaceRejectsUsageErrors(t *testing.T) {
	app := workspaceTestApp(&bytes.Buffer{}, &fakeWorkspaceManager{})
	for _, args := range [][]string{nil, {"wat"}, {"list", "extra"}, {"status"}, {"prepare"}, {"clean", "--bad", "plan-a"}, {"clean", "a", "b"}} {
		if err := app.workspace(context.Background(), fakeRepository{}, args); err == nil {
			t.Fatalf("expected usage error for args %#v", args)
		}
	}
}

func TestWorkspacePrepareUsesPlanRepoRootAndBaseBranch(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{prepared: workspace.Metadata{PlanID: "plan-a", Path: "/repo/.tao/workspaces/plan-a", Branch: "tao/plan-a", BaseBranch: "master", Created: true}}
	var gotRepoRoot string
	app := App{Out: &out, Err: &out, WorkspaceManager: func(repoRoot string) (WorkspaceManager, error) {
		gotRepoRoot = repoRoot
		return manager, nil
	}}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", plan.StatusPlanned)}}

	if err := app.workspace(context.Background(), repo, []string{"prepare", "plan-a"}); err != nil {
		t.Fatal(err)
	}
	if gotRepoRoot != "/repo" {
		t.Fatalf("expected manager to use control repo root, got %q", gotRepoRoot)
	}
	if manager.prepareOptions.PlanID != "plan-a" || manager.prepareOptions.BaseBranch != "master" {
		t.Fatalf("unexpected prepare options: %#v", manager.prepareOptions)
	}
	if !strings.Contains(out.String(), "State: created") || !strings.Contains(out.String(), "Plan Dir: /repo/.tao/plans/plan-a") {
		t.Fatalf("unexpected prepare output: %q", out.String())
	}
}

func TestWorkspaceCleanPreviewsByDefault(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{cleanPlan: workspace.CleanPlan{Path: "/repo/.tao/workspaces/plan-a", Branch: "tao/plan-a", Status: "clean", CanRemove: true, Reason: "workspace is clean", Actions: []string{"git worktree remove /repo/.tao/workspaces/plan-a"}}}
	app := workspaceTestApp(&out, manager)
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", plan.StatusCompleted)}}

	if err := app.workspace(context.Background(), repo, []string{"clean", "plan-a"}); err != nil {
		t.Fatal(err)
	}
	if manager.cleanCalled {
		t.Fatal("clean without --force should not remove workspace")
	}
	for _, want := range []string{"Branch: tao/plan-a", "Status: clean", "Clean: would clean", "Actions: git worktree remove /repo/.tao/workspaces/plan-a"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in preview output, got %q", want, out.String())
		}
	}
}

func TestWorkspaceCleanRefusesActiveMissingProtectedAndUnmerged(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    string
		cleanPlan workspace.CleanPlan
		args      []string
		want      string
	}{
		{name: "active", status: plan.StatusInProgress, cleanPlan: workspace.CleanPlan{Path: "/repo/.tao/workspaces/plan-a"}, args: []string{"clean", "plan-a"}, want: "--force-active"},
		{name: "missing", status: plan.StatusCompleted, cleanPlan: workspace.CleanPlan{Path: "/repo/.tao/workspaces/plan-a", Missing: true, Status: "missing", Reason: "workspace is missing"}, args: []string{"clean", "plan-a"}, want: "missing workspace"},
		{name: "protected", status: plan.StatusCompleted, cleanPlan: workspace.CleanPlan{Path: "/repo", Branch: "master", ProtectedBranch: true, Status: "protected-branch", Reason: "workspace branch is protected"}, args: []string{"clean", "plan-a"}, want: "protected branch"},
		{name: "unmerged", status: plan.StatusCompleted, cleanPlan: workspace.CleanPlan{Path: "/repo/.tao/workspaces/plan-a", Branch: "tao/plan-a", Status: "unmerged", Reason: "workspace branch is not merged"}, args: []string{"clean", "--force", "plan-a"}, want: "--force-dirty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeWorkspaceManager{cleanPlan: test.cleanPlan}
			app := workspaceTestApp(&bytes.Buffer{}, manager)
			repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", test.status)}}

			err := app.workspace(context.Background(), repo, test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
			if manager.cleanCalled {
				t.Fatal("refused cleanup should not remove workspace")
			}
		})
	}
}

func TestWorkspaceCleanPreviewsDirtyWithoutRemoving(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{cleanPlan: workspace.CleanPlan{Path: "/repo/.tao/workspaces/plan-a", Branch: "tao/plan-a", Status: "dirty", Dirty: true, Reason: "workspace has uncommitted changes", Actions: []string{"git worktree remove --force /repo/.tao/workspaces/plan-a"}}}
	app := workspaceTestApp(&out, manager)
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", plan.StatusCompleted)}}

	if err := app.workspace(context.Background(), repo, []string{"clean", "plan-a"}); err != nil {
		t.Fatal(err)
	}
	if manager.cleanCalled {
		t.Fatal("dirty preview should not remove workspace")
	}
	if !strings.Contains(out.String(), "Status: dirty") || !strings.Contains(out.String(), "git worktree remove --force") {
		t.Fatalf("expected preview output, got %q", out.String())
	}
}

func TestWorkspaceCleanRequiresExplicitForceForDirtyAndActive(t *testing.T) {
	manager := &fakeWorkspaceManager{cleanPlan: workspace.CleanPlan{Path: "/repo/.tao/workspaces/plan-a", Dirty: true, Reason: "workspace has uncommitted changes"}}
	app := workspaceTestApp(&bytes.Buffer{}, manager)
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", plan.StatusCompleted)}}

	err := app.workspace(context.Background(), repo, []string{"clean", "--force", "plan-a"})
	if err == nil || !strings.Contains(err.Error(), "--force-dirty") {
		t.Fatalf("expected dirty force error, got %v", err)
	}

	activeRepo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", plan.StatusInProgress)}}
	err = app.workspace(context.Background(), activeRepo, []string{"clean", "--force", "--force-dirty", "plan-a"})
	if err == nil || !strings.Contains(err.Error(), "--force-active") {
		t.Fatalf("expected active force error, got %v", err)
	}
}

func TestWorkspaceCleanForceRemovesWithDirtyOverride(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{cleanPlan: workspace.CleanPlan{Path: "/repo/.tao/workspaces/plan-a", Dirty: true, Reason: "workspace has uncommitted changes"}}
	app := workspaceTestApp(&out, manager)
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": workspacePlanDetail("plan-a", plan.StatusCompleted)}}

	if err := app.workspace(context.Background(), repo, []string{"clean", "--force", "--force-dirty", "plan-a"}); err != nil {
		t.Fatal(err)
	}
	if !manager.cleanCalled || !manager.cleanOptions.ForceDirty {
		t.Fatalf("expected dirty forced clean, called=%v options=%#v", manager.cleanCalled, manager.cleanOptions)
	}
	if !strings.Contains(out.String(), "Clean: cleaned") {
		t.Fatalf("expected cleaned output, got %q", out.String())
	}
}

func workspaceTestApp(out *bytes.Buffer, manager *fakeWorkspaceManager) App {
	return App{Out: out, Err: out, WorkspaceManager: func(repoRoot string) (WorkspaceManager, error) { return manager, nil }}
}

func workspacePlanDetail(id string, status string) *plan.PlanDetail { //nolint:unparam // id kept for fixture flexibility
	return &plan.PlanDetail{
		Dir: "/repo/.tao/plans/" + id,
		State: plan.State{
			Status: status,
			Repo:   plan.Repo{Root: "/repo", Branch: "master"},
			Plan:   plan.PlanState{ID: id},
		},
	}
}
