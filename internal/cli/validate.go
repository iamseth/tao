package cli

import (
	"context"
	"errors"
	"io"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/view"
)

var validateCommand = commandMetadata{
	name:                  "validate",
	minPrefix:             "v",
	usageLines:            []string{"validate (v) <plan-id-or-slug-or-path>"},
	completionDescription: "Validate plan artifacts and verification commands",
	long:                  "Validate a plan's artifacts before running or completing it. Tao checks artifact consistency, verification command declarations, and agent budget warnings.",
	examples: "  tao validate my-plan\n" +
		"  tao validate 20260628-1618-kubectl-style-help",
	completion: completionContext{
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.validate(c.ctx, c.repo, c.args)
	},
}

var errPlanValidationFailed = errors.New("plan validation failed")

func (a App) validate(ctx context.Context, repo plan.Resolver, args []string) error {
	if err := requirePositionals(args, 1, "usage: tao validate <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	detail, err := repo.ResolvePlan(ctx, args[0])
	if err != nil {
		return err
	}
	result := plan.ValidatePlanVerification(detail)
	if err := renderPlanValidationWithThresholds(a.Out, detail, result, runtimeconfig.RuntimeAgentBudgetThresholds()); err != nil {
		return err
	}
	if result.HasErrors() {
		return errPlanValidationFailed
	}
	return nil
}

func renderPlanValidationWithThresholds(out io.Writer, detail *plan.PlanDetail, result plan.VerificationValidationResult, thresholds plan.AgentBudgetThresholds) error {
	if err := writef(out, "Validation: %s\n", detail.State.Plan.ID); err != nil {
		return err
	}
	if len(detail.Warnings) > 0 {
		if err := writeln(out, "Artifact Warnings:"); err != nil {
			return err
		}
		for _, warning := range detail.Warnings {
			if err := writef(out, "- %s\n", warning); err != nil {
				return err
			}
		}
	}
	if len(result.Findings) > 0 {
		if err := view.RenderVerificationFindings(out, result.Findings); err != nil {
			return err
		}
	}
	budgetWarnings := plan.AgentBudgetWarnings(detail, thresholds)
	if err := view.RenderAgentBudgetWarnings(out, budgetWarnings); err != nil {
		return err
	}
	if len(detail.Warnings) == 0 && len(result.Findings) == 0 && len(budgetWarnings) == 0 {
		return writeln(out, "No validation findings.")
	}
	return nil
}
