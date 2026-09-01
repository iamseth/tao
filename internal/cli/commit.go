package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	taocommit "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/workspace"
)

var commitCommand = commandMetadata{
	name:                  "commit",
	usageLines:            []string{"commit --context [--repo-root DIR]", "commit --proposal-file FILE [--repo-root DIR]", "commit --message MESSAGE [--repo-root DIR]"},
	completionDescription: "Safely commit outside managed plan worktrees",
	long:                  "Build drift-bound standalone commit context or finalize a centrally validated commit. Context generation is read-only. Finalization rechecks the repository snapshot, stages only allowed paths, and lets Tao create the commit. Active Tao-managed plan worktrees refuse standalone context and commits; follow the reported plan continuation, safe restart, completion-recovery, verification-repair, or rework path instead.",
	examples: "  tao commit --context > /tmp/commit-context.json\n" +
		"  tao commit --proposal-file /tmp/commit-proposal.json\n" +
		"  tao commit --message $'feat(cli): add safe commit boundary\\n\\nWhat:\\nRoute standalone commits through Tao.\\n\\nWhy:\\nKeep staging and validation authoritative.'",
	registerFlags: registerCommitFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"message":       {kind: completionValueText, label: "message"},
		"proposal-file": {kind: completionValuePath, label: "path"},
		"repo-root":     {kind: completionValuePath, label: "path"},
	}},
	execute: func(c commandContext) error {
		return c.app.commit(c.ctx, c.args)
	},
}

func registerCommitFlags(fs *flag.FlagSet) {
	fs.Bool("context", false, "print bounded safety-filtered commit context as JSON")
	fs.String("proposal-file", "", "JSON file containing a structured commit proposal")
	fs.String("message", "", "explicit full canonical commit message")
	fs.String("repo-root", ".", "repository path")
}

func (a App) commit(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("commit", args, registerCommitFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao commit (--context | --proposal-file FILE | --message MESSAGE) [--repo-root DIR]"); err != nil {
		return err
	}
	contextMode := flagBoolValue(fs, "context")
	proposalFile := flagStringValue(fs, "proposal-file")
	message := flagStringValue(fs, "message")
	modes := 0
	if contextMode {
		modes++
	}
	if proposalFile != "" {
		modes++
	}
	if message != "" {
		modes++
	}
	if modes != 1 {
		return errors.New("usage: tao commit (--context | --proposal-file FILE | --message MESSAGE) [--repo-root DIR]")
	}

	var proposal taocommit.StandaloneProposal
	if proposalFile != "" {
		proposal, err = taocommit.ReadStandaloneProposal(proposalFile)
		if err != nil {
			return err
		}
	}

	rootClient := gitops.NewClient(flagStringValue(fs, "repo-root"), a.CommandRunner)
	repoRoot, err := rootClient.TopLevel(ctx)
	if err != nil {
		return fmt.Errorf("resolve standalone commit repository: %w", err)
	}
	git := gitops.NewClient(repoRoot, a.CommandRunner)
	if err := a.guardStandaloneCommit(ctx, git, repoRoot); err != nil {
		return err
	}
	if contextMode {
		commitContext, err := taocommit.BuildStandaloneContext(ctx, git, repoRoot)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(a.Out)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(commitContext); err != nil {
			return fmt.Errorf("write standalone commit context: %w", err)
		}
		return nil
	}

	var result taocommit.Result
	if proposalFile != "" {
		result, err = taocommit.FinalizeStandaloneProposal(ctx, git, repoRoot, proposal)
	} else {
		result, err = taocommit.FinalizeStandaloneMessage(ctx, git, repoRoot, message)
	}
	if errors.Is(err, taocommit.ErrNoAllowedChanges) {
		return writeln(a.Out, "Nothing to commit: no allowed changes.")
	}
	if err != nil {
		return err
	}
	return writef(a.Out, "Created local commit %s %s.\n", abbreviateCommitSHA(result.SHA), result.Subject)
}

func (a App) guardStandaloneCommit(ctx context.Context, git gitops.Client, worktreeRoot string) error {
	commonDir, err := git.RevParse(ctx, "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve standalone commit repository identity: %w", err)
	}
	repoRoot, err := workspace.RepositoryRootFromGitCommonDir(worktreeRoot, commonDir)
	if err != nil {
		return fmt.Errorf("resolve standalone commit repository identity: %w", err)
	}
	canonicalWorktree, err := workspace.PhysicalPath(worktreeRoot)
	if err != nil {
		return fmt.Errorf("resolve standalone commit worktree: %w", err)
	}
	if canonicalWorktree == repoRoot {
		return nil
	}
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("resolve standalone commit branch: %w", err)
	}
	repo := taodata.Repo{ID: taodata.RepoID(repoRoot), Root: repoRoot}
	plans := a.repository(a.registry().PlansDir(repo))
	summaries, err := plans.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return fmt.Errorf("inspect managed worktree plans: %w", err)
	}
	unresolved, err := workspace.UnresolvedInvalidManagedWorktreeOwners(repoRoot, canonicalWorktree, branch, summaries)
	if err != nil {
		return fmt.Errorf("inspect invalid managed worktree plans: %w", err)
	}
	if len(unresolved) > 0 {
		return fmt.Errorf("refuse standalone commit: managed worktree ownership cannot be safely resolved because plan(s) %s are invalid", strings.Join(unresolved, ", "))
	}
	details := make([]*plan.PlanDetail, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Status == plan.StatusInvalid {
			continue
		}
		detail, loadErr := plans.GetPlan(ctx, summary.ID)
		if loadErr != nil {
			return fmt.Errorf("inspect managed worktree plan %s: %w", summary.ID, loadErr)
		}
		details = append(details, detail)
	}
	owner, err := workspace.ResolveManagedWorktreeOwnership(repoRoot, canonicalWorktree, branch, details)
	if err != nil {
		return fmt.Errorf("refuse standalone commit: %w", err)
	}
	if owner != nil {
		if strings.TrimSpace(owner.Command) != "" {
			return fmt.Errorf("refuse standalone commit in active Tao-managed worktree for plan %s; use `%s`", owner.PlanID, owner.Command)
		}
		if strings.TrimSpace(owner.Instruction) != "" {
			return fmt.Errorf("refuse standalone commit in active Tao-managed worktree for plan %s; %s", owner.PlanID, owner.Instruction)
		}
		return fmt.Errorf("refuse standalone commit in active Tao-managed worktree for plan %s; inspect the plan's recovery guidance", owner.PlanID)
	}
	return nil
}

func abbreviateCommitSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
