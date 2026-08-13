package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

var workspaceCommand = commandMetadata{
	name:                  "workspace",
	minPrefix:             "w",
	usageLines:            []string{"workspace (w) list  (default strategy: worktree)", "workspace (w) prepare <plan-id-or-slug-or-path>", "workspace (w) status <plan-id-or-slug-or-path>", "workspace (w) clean [--force] [--force-active] [--force-dirty] <plan-id-or-slug-or-path>"},
	completionDescription: "Manage worktree workspaces",
	long:                  "Manage isolated worktree workspaces for Tao plans. Prepare a plan workspace, inspect its metadata, list known workspaces, or preview and remove a completed workspace with explicit safety flags.",
	examples: "  tao workspace list\n" +
		"  tao workspace prepare my-plan\n" +
		"  tao workspace status my-plan\n" +
		"  tao workspace clean --force --force-dirty my-plan",
	subcommands: []commandSubcommand{
		{name: "list", description: "List workspaces for the current repository"},
		{name: "prepare", description: "Create or reuse a workspace for a plan", completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
		{name: "status", description: "Show workspace metadata for a plan", completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
		{name: "clean", description: "Preview or remove a plan workspace", registerFlags: registerWorkspaceCleanFlags, completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
	},
	registerFlags: registerWorkspaceCleanFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.workspace(c.ctx, c.repo, c.args)
	},
}

const workspaceUsage = "usage: tao workspace list|prepare|status|clean"

func registerWorkspaceCleanFlags(fs *flag.FlagSet) {
	fs.Bool("force", false, "remove the workspace after passing safety checks")
	fs.Bool("force-active", false, "allow cleaning an active plan workspace")
	fs.Bool("force-dirty", false, "allow cleaning a dirty or unmerged workspace")
}

func (a App) workspace(ctx context.Context, repo plan.Resolver, args []string) error {
	if len(args) == 0 {
		return errors.New(workspaceUsage)
	}
	switch args[0] {
	case "list":
		return a.workspaceList(ctx, args[1:])
	case "prepare":
		return a.workspacePrepare(ctx, repo, args[1:])
	case "status":
		return a.workspaceStatus(ctx, repo, args[1:])
	case "clean":
		return a.workspaceClean(ctx, repo, args[1:])
	default:
		return fmt.Errorf("unknown workspace command %q", args[0])
	}
}

func (a App) workspaceList(ctx context.Context, args []string) error {
	if err := requirePositionals(args, 0, "usage: tao workspace list"); err != nil {
		return err
	}
	manager, err := a.workspaceManagerFromCWD()
	if err != nil {
		return err
	}
	workspaces, err := manager.List(ctx)
	if err != nil {
		return err
	}
	return renderWorkspaceList(a.Out, workspaces)
}

func (a App) workspacePrepare(ctx context.Context, repo plan.Resolver, args []string) error {
	if err := requirePositionals(args, 1, "usage: tao workspace prepare <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	detail, manager, err := a.resolveWorkspacePlan(ctx, repo, args[0]) //nolint:gosec // G602: args[0] guarded by requirePositionals(args, 1) above
	if err != nil {
		return err
	}
	identity, err := workspace.ResolvePlanBranch(detail, workspace.DefaultConfig())
	if err != nil {
		return err
	}
	metadata, err := manager.Prepare(ctx, workspace.PrepareOptions{PlanID: detail.State.Plan.ID, BaseBranch: detail.State.Repo.Branch, Branch: identity.Name, RequireNewBranch: identity.RequireNew})
	if err != nil {
		return err
	}
	if err := renderWorkspaceMetadata(a.Out, metadata); err != nil {
		return err
	}
	return writef(a.Out, "Plan Dir: %s\n", detail.Dir)
}

func (a App) workspaceStatus(ctx context.Context, repo plan.Resolver, args []string) error {
	if err := requirePositionals(args, 1, "usage: tao workspace status <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	detail, manager, err := a.resolveWorkspacePlan(ctx, repo, args[0]) //nolint:gosec // G602: args[0] guarded by requirePositionals(args, 1) above
	if err != nil {
		return err
	}
	identity, err := workspace.ResolvePlanBranch(detail, workspace.DefaultConfig())
	if err != nil {
		return err
	}
	metadata, err := manager.Status(ctx, detail.State.Plan.ID, identity.Name)
	if err != nil {
		return err
	}
	if detail.State.Workspace != nil {
		metadata.RecordedBranch = detail.State.Workspace.Branch
		metadata.RecordedHeadSHA = detail.State.Workspace.HeadSHA
	}
	return renderWorkspaceMetadata(a.Out, metadata)
}

func (a App) workspaceClean(ctx context.Context, repo plan.Resolver, args []string) error {
	force, forceActive, forceDirty, input, err := parseWorkspaceCleanArgs(args)
	if err != nil {
		return err
	}
	detail, manager, err := a.resolveWorkspacePlan(ctx, repo, input)
	if err != nil {
		return err
	}
	active := detail.State.Status == plan.StatusInProgress || detail.State.Plan.CurrentSlice != nil
	if active && !forceActive {
		return fmt.Errorf("refusing to clean active plan %s; pass --force-active to override", detail.State.Plan.ID)
	}
	cleanPlan, err := manager.PlanClean(ctx, detail.State.Plan.ID)
	if err != nil {
		return err
	}
	if cleanPlan.Missing {
		return fmt.Errorf("refusing to clean missing workspace %s", detail.State.Plan.ID)
	}
	if cleanPlan.ProtectedBranch {
		return fmt.Errorf("refusing to clean protected branch %s for workspace %s", cleanPlan.Branch, detail.State.Plan.ID)
	}
	if force && cleanPlan.Status == "unmerged" && !forceDirty {
		return fmt.Errorf("refusing to clean unmerged workspace %s; pass --force-dirty to override", detail.State.Plan.ID)
	}
	if force && cleanPlan.Dirty && !forceDirty {
		return fmt.Errorf("refusing to clean dirty workspace %s; pass --force-dirty to override", detail.State.Plan.ID)
	}
	if !force {
		return renderWorkspaceCleanPlan(a.Out, cleanPlan, false)
	}
	removed, err := manager.Clean(ctx, detail.State.Plan.ID, workspace.CleanOptions{Force: force, ForceDirty: forceDirty})
	if err != nil {
		return err
	}
	return renderWorkspaceCleanPlan(a.Out, removed, true)
}

func parseWorkspaceCleanArgs(args []string) (force bool, forceActive bool, forceDirty bool, input string, err error) {
	fs := flag.NewFlagSet("workspace clean", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerWorkspaceCleanFlags(fs)
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") && !strings.Contains(arg, "=") {
			name := strings.TrimPrefix(arg, "--")
			if fs.Lookup(name) != nil {
				if err := fs.Set(name, "true"); err != nil {
					return false, false, false, "", err
				}
				continue
			}
		}
		if strings.HasPrefix(arg, "-") {
			return false, false, false, "", fmt.Errorf("unknown flag %q", arg)
		}
		if input != "" {
			return false, false, false, "", errors.New("usage: tao workspace clean [--force] [--force-active] [--force-dirty] <plan-id-or-slug-or-path>")
		}
		input = arg
	}
	if input == "" {
		return false, false, false, "", errors.New("usage: tao workspace clean [--force] [--force-active] [--force-dirty] <plan-id-or-slug-or-path>")
	}
	return flagBoolValue(fs, "force"), flagBoolValue(fs, "force-active"), flagBoolValue(fs, "force-dirty"), input, nil
}

func (a App) resolveWorkspacePlan(ctx context.Context, repo plan.Resolver, input string) (*plan.PlanDetail, WorkspaceManager, error) {
	detail, err := repo.ResolvePlan(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	repoRoot := detail.State.Repo.Root
	if repoRoot == "" {
		return nil, nil, fmt.Errorf("plan %s does not record a repo root", detail.State.Plan.ID)
	}
	manager, err := a.workspaceManager(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	return detail, manager, nil
}

func (a App) workspaceManagerFromCWD() (WorkspaceManager, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	repoRoot, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	return a.workspaceManager(repoRoot)
}

func renderWorkspaceList(out io.Writer, workspaces []workspace.Metadata) error {
	if err := writef(out, "PLAN ID  BRANCH  STATE  PATH\n"); err != nil {
		return err
	}
	for _, metadata := range workspaces {
		if err := writef(out, "%s  %s  %s  %s\n", metadata.PlanID, metadata.Branch, workspaceState(metadata), metadata.Path); err != nil {
			return err
		}
	}
	return nil
}

func renderWorkspaceMetadata(out io.Writer, metadata workspace.Metadata) error {
	lines := []string{
		"Plan ID: " + metadata.PlanID,
		"Path: " + metadata.Path,
	}
	if metadata.RecordedBranch != metadata.Branch || metadata.RecordedHeadSHA != metadata.HeadSHA {
		lines = append(lines,
			"Durable Recorded Branch: "+durableWorkspaceValue(metadata.RecordedBranch),
			"Durable Recorded HEAD: "+durableWorkspaceValue(metadata.RecordedHeadSHA),
			"Live Actual Branch: "+metadata.Branch,
			"Live Actual HEAD: "+metadata.HeadSHA,
		)
	} else {
		lines = append(lines, "Branch: "+metadata.Branch)
	}
	lines = append(lines, "State: "+workspaceState(metadata))
	if metadata.BaseBranch != "" {
		lines = append(lines, "Base Branch: "+metadata.BaseBranch)
	}
	if metadata.BaseSHA != "" {
		lines = append(lines, "Base SHA: "+metadata.BaseSHA)
	}
	if metadata.DependencyStatus != "" {
		lines = append(lines, "Dependency Preparation: "+metadata.DependencyStatus)
	}
	if metadata.DependencyCommand != "" {
		lines = append(lines, "Dependency Command: "+metadata.DependencyCommand)
	}
	return writeLines(out, lines...)
}

func durableWorkspaceValue(value string) string {
	if value == "" {
		return "(missing)"
	}
	return value
}

func renderWorkspaceCleanPlan(out io.Writer, plan workspace.CleanPlan, removed bool) error {
	action := "would clean"
	if removed {
		action = "cleaned"
	}
	return writeLines(out,
		"Plan ID: "+plan.PlanID,
		"Path: "+plan.Path,
		"Branch: "+plan.Branch,
		"Status: "+plan.Status,
		"Clean: "+action,
		"Reason: "+plan.Reason,
		"Actions: "+strings.Join(plan.Actions, "; "),
	)
}

func workspaceState(metadata workspace.Metadata) string {
	switch {
	case metadata.Missing:
		return "missing"
	case metadata.Dirty:
		return "dirty"
	case metadata.Created:
		return "created"
	case metadata.Reused:
		return "reused"
	default:
		return "clean"
	}
}
