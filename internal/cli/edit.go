package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

var editCommand = commandMetadata{
	name:                  "edit",
	minPrefix:             "e",
	usageLines:            []string{"edit (e) remove <plan-id-or-slug-or-path> <slice-id>", "edit (e) skip <plan-id-or-slug-or-path> <slice-id>", "edit (e) move <plan-id-or-slug-or-path> <slice-id> (--before ID | --after ID)"},
	completionDescription: "Edit pending slices in a plan",
	long:                  "Edit pending slices in a Tao plan without changing application files. Use it to remove, skip, or reorder work before running the remaining queue.",
	examples: "  tao edit remove my-plan 003-old-slice\n" +
		"  tao edit skip my-plan 004-optional\n" +
		"  tao edit move my-plan 005-tests --before 004-docs",
	subcommands: []commandSubcommand{
		{name: "remove", description: "Remove a pending slice from the plan", completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
		{name: "skip", description: "Mark a pending slice skipped", completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
		{name: "move", description: "Move a pending slice before or after another pending slice", registerFlags: registerEditMoveFlags, completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
	},
	registerFlags: registerEditMoveFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.edit(c.ctx, c.repo, c.args)
	},
}

const editUsage = "usage: tao edit remove <plan-id-or-slug-or-path> <slice-id> | tao edit skip <plan-id-or-slug-or-path> <slice-id> | tao edit move <plan-id-or-slug-or-path> <slice-id> (--before ID | --after ID)"

type editRepository interface {
	plan.PlanRecordResolver
}

func (a App) edit(ctx context.Context, repo editRepository, args []string) error {
	if len(args) == 0 {
		return errors.New(editUsage)
	}

	switch args[0] {
	case "remove":
		return a.editRemove(ctx, repo, args[1:])
	case "skip":
		return a.editSkip(ctx, repo, args[1:])
	case "move":
		return a.editMove(ctx, repo, args[1:])
	default:
		return fmt.Errorf("unknown edit subcommand %q", args[0])
	}
}

func (a App) editRemove(ctx context.Context, repo editRepository, args []string) error {
	input, sliceID, err := parseEditSliceArgs(args)
	if err != nil {
		return err
	}
	record, err := repo.ResolvePlanRecord(ctx, input)
	if err != nil {
		return err
	}
	if err := record.RemoveSlice(sliceID, a.now().UTC()); err != nil {
		return err
	}
	return a.writeEditResult(record.Detail(), fmt.Sprintf("Removed pending slice: %s", sliceID))
}

func (a App) editSkip(ctx context.Context, repo editRepository, args []string) error {
	input, sliceID, err := parseEditSliceArgs(args)
	if err != nil {
		return err
	}
	record, err := repo.ResolvePlanRecord(ctx, input)
	if err != nil {
		return err
	}
	if err := record.SkipSlice(sliceID, a.now().UTC()); err != nil {
		return err
	}
	return a.writeEditResult(record.Detail(), fmt.Sprintf("Skipped pending slice: %s", sliceID))
}

func registerEditMoveFlags(fs *flag.FlagSet) {
	fs.String("before", "", "move before slice id")
	fs.String("after", "", "move after slice id")
}

func (a App) editMove(ctx context.Context, repo editRepository, args []string) error {
	fs, positional, err := a.parseArgs("edit move", args, registerEditMoveFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 2, editUsage); err != nil {
		return err
	}
	before := strings.TrimSpace(flagStringValue(fs, "before"))
	after := strings.TrimSpace(flagStringValue(fs, "after"))
	if before == "" && after == "" {
		return errors.New("edit move requires --before or --after")
	}
	if before != "" && after != "" {
		return errors.New("edit move accepts only one of --before or --after")
	}

	record, err := repo.ResolvePlanRecord(ctx, positional[0])
	if err != nil {
		return err
	}
	detail := record.Detail()
	order, err := movedPendingOrder(detail.State.Plan.PendingSlices, positional[1], before, after)
	if err != nil {
		return err
	}
	if err := record.ReorderPendingSlices(order, a.now().UTC()); err != nil {
		return err
	}
	return a.writeEditResult(detail, fmt.Sprintf("Moved pending slice: %s", positional[1]))
}

func parseEditSliceArgs(args []string) (string, string, error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return "", "", fmt.Errorf("unknown flag %q", arg)
		}
	}
	if len(args) != 2 {
		return "", "", errors.New(editUsage)
	}
	return args[0], args[1], nil
}

func movedPendingOrder(pending []string, moveID string, beforeID string, afterID string) ([]string, error) {
	targetID := beforeID
	if targetID == "" {
		targetID = afterID
	}
	if moveID == targetID {
		return nil, errors.New("slice id and move target must differ")
	}

	order := make([]string, 0, len(pending))
	foundMove := false
	foundTarget := false
	for _, id := range pending {
		if id == moveID {
			foundMove = true
			continue
		}
		if id == targetID {
			foundTarget = true
		}
		order = append(order, id)
	}
	if !foundMove {
		return nil, fmt.Errorf("slice %s is not in pending_slices", moveID)
	}
	if !foundTarget {
		return nil, fmt.Errorf("move target %s is not in pending_slices", targetID)
	}

	insertAt := -1
	for i, id := range order {
		if id == targetID {
			insertAt = i
			if afterID != "" {
				insertAt++
			}
			break
		}
	}
	order = append(order, "")
	copy(order[insertAt+1:], order[insertAt:])
	order[insertAt] = moveID
	return order, nil
}

func (a App) writeEditResult(detail *plan.PlanDetail, summary string) error {
	if err := writeln(a.Out, summary); err != nil {
		return err
	}
	if len(detail.State.Plan.PendingSlices) == 0 {
		return writef(a.Out, "Next: tao validate %s\n", detail.State.Plan.ID)
	}
	return renderPrimaryNextAction(a.Out, plan.DeriveNextAction(detail))
}
