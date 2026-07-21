package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/workspace"
)

func TestCleanupRemovesMergedBranchesAndSkipsRest(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{managedPlans: []workspace.ManagedCleanup{
		{Branch: "tao/done", WorktreePath: "/repo/.tao/workspaces/done", Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into master"},
		{Branch: "tao/wip", Status: workspace.ManagedStatusUnmerged, Reason: "not merged into master"},
		{Branch: "tao/dirty", WorktreePath: "/repo/.tao/workspaces/dirty", Status: workspace.ManagedStatusDirty, Reason: "worktree has uncommitted changes"},
		{Branch: "tao/cur", Status: workspace.ManagedStatusCurrent, Reason: "branch is currently checked out"},
	}}
	app := App{Out: &out, Err: &out, CommandRunner: cleanupTopLevelRunner("/repo"), WorkspaceManager: func(root string) (WorkspaceManager, error) {
		if root != "/repo" {
			t.Fatalf("expected repo root /repo, got %q", root)
		}
		return manager, nil
	}}

	if err := app.cleanup(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"removed /repo tao/done (worktree /repo/.tao/workspaces/done): merged into master",
		"skipped /repo tao/wip: not merged into master",
		"skipped /repo tao/dirty (worktree /repo/.tao/workspaces/dirty): worktree has uncommitted changes",
		"skipped /repo tao/cur: branch is currently checked out",
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

func TestCleanupForceRemovesUnmergedButNotCurrent(t *testing.T) {
	var out bytes.Buffer
	manager := &fakeWorkspaceManager{managedPlans: []workspace.ManagedCleanup{
		{Branch: "tao/wip", Status: workspace.ManagedStatusUnmerged, Reason: "not merged into master"},
		{Branch: "tao/cur", Status: workspace.ManagedStatusCurrent, Reason: "branch is currently checked out"},
	}}
	app := App{Out: &out, Err: &out, CommandRunner: cleanupTopLevelRunner("/repo"), WorkspaceManager: func(string) (WorkspaceManager, error) { return manager, nil }}

	if err := app.cleanup(context.Background(), []string{"--force"}); err != nil {
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

	if err := app.cleanup(context.Background(), []string{"--dry-run"}); err != nil {
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

	err := app.cleanup(context.Background(), nil)
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
	err := app.cleanup(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "locate repository") {
		t.Fatalf("expected locate repository error, got %v", err)
	}
}

// cleanupTopLevelRunner answers "git rev-parse --show-toplevel" with root and
// ignores every other command, so cleanup tests can drive a fixed repository root.
func cleanupTopLevelRunner(root string) CommandRunner { //nolint:unparam // root kept for test clarity
	return func(ctx context.Context, _ string, _ string, args []string, stdout io.Writer, _ io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cleanupCommandKey(args) == "rev-parse --show-toplevel" {
			_, _ = io.WriteString(stdout, root+"\n")
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
