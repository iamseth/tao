package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/staleness"
)

var stalenessCommand = commandMetadata{
	name:                  "staleness",
	minPrefix:             "stale",
	usageLines:            []string{"staleness (stale) <plan-id-or-slug-or-path>"},
	completionDescription: "Check whether a plan is stale against its recorded base commit",
	long:                  "Check whether a plan is stale compared with the repository base commit captured during planning. Tao reports changed history and pending-slice file overlaps as warnings.",
	examples: "  tao staleness my-plan\n" +
		"  tao stale 20260628-1618-kubectl-style-help",
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.staleness(c.ctx, c.repo, c.args)
	},
}

func (a App) staleness(ctx context.Context, repo plan.Resolver, args []string) error {
	if err := requirePositionals(args, 1, "usage: tao staleness <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	detail, err := repo.ResolvePlan(ctx, args[0])
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found", args[0])
	}
	findings := staleness.Findings(ctx, detail, a.stalenessRunner())
	return renderPlanStaleness(a.Out, detail, findings)
}

func renderPlanStaleness(out io.Writer, detail *plan.PlanDetail, findings []staleness.Finding) error {
	if err := writef(out, "Staleness: %s\n", detail.State.Plan.ID); err != nil {
		return err
	}
	if detail.State.Repo.BaseCommit != "" {
		if err := writef(out, "Base Commit: %s\n", detail.State.Repo.BaseCommit); err != nil {
			return err
		}
	}
	if len(findings) == 0 {
		return writeln(out, "No staleness findings.")
	}
	if err := writeln(out, "Findings:"); err != nil {
		return err
	}
	for _, finding := range findings {
		if err := writef(out, "- %s: %s\n", finding.Severity, finding.Message); err != nil {
			return err
		}
	}
	return nil
}

func (a App) stalenessRunner() commandrunner.Runner {
	if a.CommandRunner != nil {
		return a.CommandRunner
	}
	return commandrunner.DefaultLocal
}
