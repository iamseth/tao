package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	runpkg "github.com/iamseth/tao/internal/run"
)

var reworkCommand = commandMetadata{
	name:                  "rework",
	minPrefix:             "rew",
	usageLines:            []string{"rework (rew) [--force] [--run] <plan-id-or-slug-or-path>"},
	completionDescription: "Reopen a reviewed plan with deterministic rework slices",
	long:                  "Reopen a reviewed plan whose persisted review requested changes. Tao converts review findings into pending rework slices on the same plan, records the lifecycle event, and can immediately run the reopened plan with --run.",
	examples: "  tao rework my-plan\n" +
		"  tao rework --run 20260628-1618-kubectl-style-help\n" +
		"  tao rework --force my-plan",
	registerFlags: registerReworkFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.rework(c.ctx, c.repo, c.args)
	},
}

func registerReworkFlags(fs *flag.FlagSet) {
	fs.Bool("force", false, "reopen even when the review gate would refuse")
	fs.Bool("run", false, "run the plan after reopening")
}

func (a App) rework(ctx context.Context, repo queueRepository, args []string) error {
	if repo == nil {
		return fmt.Errorf("rework requires a plan repository")
	}
	fs, positional, err := a.parseArgs("rework", args, registerReworkFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao rework [--force] [--run] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	input := positional[0]
	force := flagBoolValue(fs, "force")
	runAfter := flagBoolValue(fs, "run")

	detail, err := repo.ResolvePlan(ctx, input)
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found", input)
	}

	now := a.now().UTC()
	return runpkg.WithPlanRunLock(ctx, detail, now, func(ownedCtx context.Context) error {
		// Resolve by the exact directory after acquisition so every gate and
		// mutation uses authoritative state rather than pre-lock selection data.
		refreshed, err := repo.ResolvePlan(ownedCtx, detail.Dir)
		if err != nil {
			return err
		}
		if refreshed == nil {
			return fmt.Errorf("plan %q not found", detail.Dir)
		}
		record, err := repo.PlanRecord(refreshed)
		if err != nil {
			return err
		}
		var newSlices []plan.Slice
		if !force {
			newSlices, err = reworkpkg.Reopen(record, now)
			if err != nil {
				return err
			}
		} else {
			findings := reworkpkg.ReviewFindings(refreshed)
			if len(findings) == 0 {
				findings = forcedReworkFindings(refreshed)
			}
			generationDetail := *refreshed
			generationDetail.State.UpdatedAt = now
			newSlices = reworkpkg.GenerateSlices(&generationDetail, findings, nextReworkRound(refreshed))
			if len(newSlices) == 0 {
				return fmt.Errorf("rework refused: plan %s has no review findings to convert", reworkPlanID(refreshed))
			}
			if err := reopenPlanRecord(record, newSlices, now, true); err != nil {
				return err
			}
		}
		if err := a.writeReworkResult(record.Detail(), newSlices, runAfter); err != nil {
			return err
		}
		if runAfter {
			return a.run(ownedCtx, repo, []string{record.Dir()})
		}
		return nil
	})
}

func reopenPlanRecord(record *plan.PlanRecord, newSlices []plan.Slice, now time.Time, force bool) error {
	if record == nil {
		return fmt.Errorf("plan record is nil")
	}
	detail := record.Detail()
	if !force || detail == nil || plan.ReopenableStatus(detail.State.Status) {
		return record.Reopen(newSlices, now)
	}
	return record.ReopenForced(newSlices, now)
}

func (a App) writeReworkResult(detail *plan.PlanDetail, newSlices []plan.Slice, runAfter bool) error {
	id := reworkPlanID(detail)
	if err := writef(a.Out, "Rework slices created for %s:\n", id); err != nil {
		return err
	}
	for _, slice := range newSlices {
		if err := writef(a.Out, "- %s: %s\n", slice.ID, slice.Title); err != nil {
			return err
		}
	}
	if runAfter {
		return writef(a.Out, "Running: tao run %s\n", id)
	}
	return writef(a.Out, "Next: tao run %s\n", id)
}

func nextReworkRound(detail *plan.PlanDetail) int {
	maxRound := 0
	if detail != nil {
		for _, slice := range detail.Slices.Slices {
			if round := reworkpkg.RoundFromSliceID(slice.ID); round > maxRound {
				maxRound = round
			}
		}
	}
	return maxRound + 1
}

func forcedReworkFindings(detail *plan.PlanDetail) []plan.ReviewFinding {
	id := reworkPlanID(detail)
	return []plan.ReviewFinding{{
		Severity:   "forced",
		File:       firstReworkExpectedFile(detail),
		Message:    "Forced rework requested for plan " + id,
		Suggestion: "Inspect the persisted review and address any remaining issues.",
	}}
}

func firstReworkExpectedFile(detail *plan.PlanDetail) string {
	if detail != nil {
		for _, slice := range detail.Slices.Slices {
			for _, file := range slice.ExpectedFiles {
				if strings.TrimSpace(file) != "" {
					return strings.TrimSpace(file)
				}
			}
		}
	}
	return "."
}

func reworkPlanID(detail *plan.PlanDetail) string {
	if detail == nil || strings.TrimSpace(detail.State.Plan.ID) == "" {
		return "plan"
	}
	return strings.TrimSpace(detail.State.Plan.ID)
}
