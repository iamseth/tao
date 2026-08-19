package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/identity"
	"github.com/iamseth/tao/internal/plan"
)

var approveCommand = commandMetadata{
	name:                  "approve",
	minPrefix:             "a",
	usageLines:            []string{"approve (a) [--slice ID] [--by NAME] <plan-id-or-slug-or-path>"},
	completionDescription: "Approve a gated slice",
	long:                  "Approve a gated pending slice so a blocked plan can continue. When no slice is provided, Tao approves the current or next pending slice for the plan.",
	examples: "  tao approve my-plan\n" +
		"  tao approve --slice 003-review --by Seth my-plan",
	registerFlags: registerApproveFlags,
	completion: completionContext{
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.approve(c.ctx, c.repo, c.args)
	},
}

func registerApproveFlags(fs *flag.FlagSet) {
	fs.String("slice", "", "slice id to approve")
	fs.String("by", "", "approver name")
}

type approvalRepository interface {
	plan.PlanRecordResolver
}

func (a App) approve(ctx context.Context, repo approvalRepository, args []string) error {
	fs, positional, err := a.parseArgs("approve", args, registerApproveFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao approve [--slice ID] [--by NAME] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	input := positional[0]

	record, err := repo.ResolvePlanRecord(ctx, input)
	if err != nil {
		return err
	}
	detail := record.Detail()
	targetID, err := approvalTargetSlice(detail, flagStringValue(fs, "slice"))
	if err != nil {
		return err
	}
	approver := strings.TrimSpace(flagStringValue(fs, "by"))
	if approver == "" {
		approver = identity.Approver()
	}
	if approver == "" {
		return fmt.Errorf("approver is required; pass --by NAME")
	}
	alreadyApproved := false
	if slice := findSlice(detail, targetID); slice != nil && slice.Approval != nil {
		alreadyApproved = slice.Approval.Approved
	}
	if err := record.ApproveSlice(targetID, approver, a.now().UTC()); err != nil {
		return err
	}

	if alreadyApproved {
		if err := writef(a.Out, "Slice already approved: %s\n", targetID); err != nil {
			return err
		}
	} else if err := writef(a.Out, "Slice approved: %s\n", targetID); err != nil {
		return err
	}
	return renderPrimaryNextAction(a.Out, plan.DeriveNextAction(record.Detail()))
}

func approvalTargetSlice(detail *plan.PlanDetail, explicit string) (string, error) {
	if detail == nil {
		return "", fmt.Errorf("plan detail is nil")
	}
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	if detail.State.Plan.CurrentSlice != nil && strings.TrimSpace(*detail.State.Plan.CurrentSlice) != "" {
		return strings.TrimSpace(*detail.State.Plan.CurrentSlice), nil
	}
	if len(detail.State.Plan.PendingSlices) > 0 {
		return detail.State.Plan.PendingSlices[0], nil
	}
	return "", fmt.Errorf("plan %s has no pending slices", detail.State.Plan.ID)
}

func findSlice(detail *plan.PlanDetail, sliceID string) *plan.Slice {
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == sliceID {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}
