package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iamseth/tao/internal/plan"
)

var logCommand = commandMetadata{
	name:                  "log",
	minPrefix:             "lo",
	usageLines:            []string{"log (lo) [--follow] <plan-id-or-slug>"},
	completionDescription: "Show or follow agent run log",
	long:                  "Show the captured agent run log for a Tao plan. Pass --follow (or -f) to stream appended output while a run is still active.",
	examples: "  tao log my-plan\n" +
		"  tao log --follow my-plan\n" +
		"  tao log -f 20260628-1618-kubectl-style-help",
	registerFlags: registerLogFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.log(c.ctx, c.repo, c.args)
	},
}

func registerLogFlags(fs *flag.FlagSet) {
	var follow bool
	fs.BoolVar(&follow, "follow", false, "follow appended agent log output")
	fs.BoolVar(&follow, "f", false, "follow appended agent log output")
}

func (a App) log(ctx context.Context, repo interface {
	plan.Repository
	plan.LogReader
	plan.LogFollower
}, args []string) error {
	fs, positional, err := a.parseArgs("log", args, registerLogFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao log [--follow] <plan-id-or-slug>"); err != nil {
		return err
	}
	id := positional[0]
	detail, err := repo.GetPlan(ctx, id)
	if err != nil {
		return err
	}
	if flagBoolValue(fs, "follow") {
		if err := followPlanLog(ctx, repo, detail.Dir, a.Out); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("agent log not found for plan %s: %s", detail.State.Plan.ID, plan.LogPath(detail.Dir))
			}
			return fmt.Errorf("read agent log: %w", err)
		}
		return nil
	}
	text, err := repo.ReadLog(detail.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent log not found for plan %s: %s", detail.State.Plan.ID, plan.LogPath(detail.Dir))
		}
		return fmt.Errorf("read agent log: %w", err)
	}
	_, err = fmt.Fprint(a.Out, text)
	return err
}

func followPlanLog(ctx context.Context, repo plan.LogFollower, dir string, out io.Writer) error {
	return repo.FollowLog(ctx, dir, out)
}
