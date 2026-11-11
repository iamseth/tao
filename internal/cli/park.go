package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

var parkCommand = commandMetadata{
	name:                  "park",
	usageLines:            []string{"park <plan-id-or-slug-or-path> --reason TEXT"},
	completionDescription: "Park a plan until explicitly unparked",
	long:                  "Park a plan with a bounded reason, excluding it from active work and refusing every run path until explicit unpark. Lifecycle and merge eligibility remain unchanged.",
	examples:              "  tao park my-plan --reason \"awaiting owner decision\"",
	registerFlags:         registerParkFlags,
	completion:            completionContext{flagValues: map[string]completionFlagValue{"reason": {kind: completionValueText, label: "text"}}, positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}},
	repository:            repositoryDefault,
	execute:               func(c commandContext) error { return c.app.setParking(c.ctx, c.repo, c.args, true) },
}

var unparkCommand = commandMetadata{
	name:                  "unpark",
	usageLines:            []string{"unpark <plan-id-or-slug-or-path>"},
	completionDescription: "Remove a plan's explicit parking marker",
	long:                  "Unpark a plan without resolving blockers or changing lifecycle, approval, recovery, or merge gates. Repeated operations are no-ops.",
	examples:              "  tao unpark my-plan",
	completion:            completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}},
	repository:            repositoryDefault,
	execute:               func(c commandContext) error { return c.app.setParking(c.ctx, c.repo, c.args, false) },
}

func registerParkFlags(fs *flag.FlagSet) {
	fs.String("reason", "", "required reason for parking the plan")
}

func (a App) setParking(ctx context.Context, repo plan.PlanRecordResolver, args []string, parked bool) error {
	name := "unpark"
	var flags func(*flag.FlagSet)
	if parked {
		name, flags = "park", registerParkFlags
	}
	fs, positional, err := a.parseArgs(name, args, flags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao "+name+" <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	now := a.now().UTC()
	reason := ""
	if parked {
		reason = strings.TrimSpace(flagStringValue(fs, "reason"))
		if err := (plan.Parking{Reason: reason, ParkedAt: now}).Validate(); err != nil {
			return err
		}
	}
	initial, err := repo.ResolvePlanRecord(ctx, positional[0])
	if err != nil {
		return err
	}
	if initial == nil || initial.Detail() == nil {
		return errors.New("resolved plan record is nil")
	}
	detail := initial.Detail()
	dir, err := filepath.Abs(detail.Dir)
	if err != nil {
		return fmt.Errorf("resolve plan directory: %w", err)
	}
	lock, err := plan.AcquireRunLock(dir, detail.State.Plan.ID, now)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	record, err := repo.ResolvePlanRecord(ctx, dir)
	if err != nil {
		return fmt.Errorf("reload plan while holding run lock: %w", err)
	}
	if record == nil || record.Detail() == nil {
		return errors.New("reload plan while holding run lock: resolved plan record is nil")
	}
	reloadedDir, err := filepath.Abs(record.Detail().Dir)
	if err != nil {
		return fmt.Errorf("resolve reloaded plan directory: %w", err)
	}
	if reloadedDir != dir || record.Detail().State.Plan.ID != detail.State.Plan.ID {
		return errors.New("plan identity changed while acquiring run lock")
	}
	if parked {
		err = record.Park(reason, now)
	} else {
		err = record.Unpark(now)
	}
	if err != nil {
		return err
	}
	return writef(a.Out, "Plan %sed: %s\n", name, detail.State.Plan.ID)
}
