package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

var cleanupCommand = commandMetadata{
	name:                  "cleanup",
	minPrefix:             "c",
	usageLines:            []string{"cleanup (c) [--dry-run] [--force]"},
	completionDescription: "Remove merged Tao branches and worktrees",
	long:                  "Remove Tao-managed branches and worktrees in the current repository. Tao previews protected, current, unmerged, and dirty work before removing anything; use --dry-run to inspect and --force to include otherwise unsafe cleanup candidates.",
	examples: "  tao cleanup --dry-run\n" +
		"  tao cleanup\n" +
		"  tao cleanup --force",
	registerFlags: registerCleanupFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.cleanup(c.ctx, c.repo, c.args)
	},
}

func registerCleanupFlags(fs *flag.FlagSet) {
	fs.Bool("dry-run", false, "show branches and worktrees that would be removed")
	fs.Bool("force", false, "also remove unmerged branches and dirty worktrees")
}

// cleanup removes Tao-managed branches and worktrees in the current repository.
// It combines live legacy tao/* discovery with exact branch ownership recorded
// by plans and Tao workspace paths. By default it removes only branches that are
// merged into the default branch and whose worktree is clean; --force also
// removes unmerged branches and dirty worktrees. Protected and current branches
// are never touched.
type cleanupPlanRepository interface {
	planLister
	GetPlan(context.Context, string) (*plan.PlanDetail, error)
}

func (a App) cleanup(ctx context.Context, repo cleanupPlanRepository, args []string) error {
	fs, positional, err := a.parseArgs("cleanup", args, registerCleanupFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao cleanup [--dry-run] [--force]"); err != nil {
		return err
	}

	runner := a.cleanupRunner()
	root, err := gitops.NewClient(".", runner).TopLevel(ctx)
	if err != nil {
		return fmt.Errorf("locate repository: %w", err)
	}

	manager, err := a.workspaceManager(root)
	if err != nil {
		return err
	}
	ownedBranches, err := cleanupOwnedBranches(ctx, repo, manager, root)
	if err != nil {
		return err
	}
	plans, err := manager.PlanManagedCleanup(ctx, ownedBranches...)
	if err != nil {
		return err
	}

	failures := 0
	for _, item := range plans {
		switch {
		case item.Status == workspace.ManagedStatusProtected || item.Status == workspace.ManagedStatusCurrent:
			if err := cleanupItemLine(a.Out, "skipped", root, item, item.Reason); err != nil {
				return err
			}
			continue
		case !item.CanRemove && !flagBoolValue(fs, "force"):
			if err := cleanupItemLine(a.Out, "skipped", root, item, item.Reason); err != nil {
				return err
			}
			continue
		}

		if flagBoolValue(fs, "dry-run") {
			if err := cleanupItemLine(a.Out, "would remove", root, item, item.Reason); err != nil {
				return err
			}
			continue
		}

		if err := manager.CleanManaged(ctx, item, workspace.CleanOptions{Force: flagBoolValue(fs, "force")}); err != nil {
			failures++
			if lineErr := cleanupItemLine(a.Out, "failed", root, item, err.Error()); lineErr != nil {
				return lineErr
			}
			continue
		}
		if err := cleanupItemLine(a.Out, "removed", root, item, item.Reason); err != nil {
			return err
		}
	}

	if failures > 0 {
		return fmt.Errorf("cleanup failed for %d branch(es)", failures)
	}
	return nil
}

func cleanupOwnedBranches(ctx context.Context, repo cleanupPlanRepository, manager WorkspaceManager, root string) ([]string, error) {
	summaries, err := repo.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return nil, fmt.Errorf("list plans for cleanup ownership: %w", err)
	}
	currentRoot := ""
	owned := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Workspace == nil && summary.PullRequest == nil {
			continue
		}
		if currentRoot == "" {
			currentRoot, err = workspace.PhysicalPath(root)
			if err != nil {
				return nil, fmt.Errorf("canonicalize current repository root for cleanup ownership: %w", err)
			}
		}
		detail, err := repo.GetPlan(ctx, summary.ID)
		if err != nil {
			return nil, fmt.Errorf("load plan %s for cleanup ownership: %w", summary.ID, err)
		}
		if detail == nil {
			return nil, fmt.Errorf("load plan %s for cleanup ownership: plan not found", summary.ID)
		}
		recordedRoot := strings.TrimSpace(detail.State.Repo.Root)
		if recordedRoot == "" {
			continue
		}
		planRoot, err := workspace.PhysicalPath(recordedRoot)
		if err != nil || planRoot != currentRoot {
			continue
		}
		if detail.State.Workspace != nil {
			owned = append(owned, detail.State.Workspace.Branch)
		}
		if detail.State.Plan.PullRequest != nil {
			owned = append(owned, detail.State.Plan.PullRequest.Branch)
		}
	}
	workspaces, err := manager.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces for cleanup ownership: %w", err)
	}
	for _, metadata := range workspaces {
		owned = append(owned, metadata.Branch)
	}
	return owned, nil
}

func (a App) cleanupRunner() commandrunner.Runner {
	if a.CommandRunner != nil {
		return a.CommandRunner
	}
	return commandrunner.DefaultLocal
}

func cleanupItemLine(out io.Writer, action string, root string, item workspace.ManagedCleanup, reason string) error {
	target := item.Branch
	if item.WorktreePath != "" {
		target = fmt.Sprintf("%s (worktree %s)", item.Branch, item.WorktreePath)
	}
	if reason != "" {
		_, err := fmt.Fprintf(out, "%s %s %s: %s\n", action, root, target, reason)
		return err
	}
	_, err := fmt.Fprintf(out, "%s %s %s\n", action, root, target)
	return err
}
