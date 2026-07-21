package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
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
	execute: func(c commandContext) error {
		return c.app.cleanup(c.ctx, c.args)
	},
}

func registerCleanupFlags(fs *flag.FlagSet) {
	fs.Bool("dry-run", false, "show branches and worktrees that would be removed")
	fs.Bool("force", false, "also remove unmerged branches and dirty worktrees")
}

// cleanup removes Tao-managed branches and worktrees in the current repository.
// It enumerates live git state (branches matching the workspace branch prefix and
// their worktrees) rather than plan records, so it never reports branches that no
// longer exist. By default it removes only branches that are merged into the
// default branch and whose worktree is clean; --force also removes unmerged
// branches and dirty worktrees. Protected and current branches are never touched.
func (a App) cleanup(ctx context.Context, args []string) error {
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
	plans, err := manager.PlanManagedCleanup(ctx)
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
