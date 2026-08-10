package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iamseth/tao/internal/agent/logrecord"
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
	completion: completionContext{
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
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
	return renderPlanLog(a.Out, text)
}

func followPlanLog(ctx context.Context, repo plan.LogFollower, dir string, out io.Writer) error {
	decoder := &planLogDecoder{out: out}
	if err := repo.FollowLog(ctx, dir, decoder); err != nil {
		return err
	}
	return decoder.Flush()
}

func renderPlanLog(out io.Writer, text string) error {
	decoder := &planLogDecoder{out: out}
	if _, err := io.WriteString(decoder, text); err != nil {
		return err
	}
	return decoder.Flush()
}

// planLogDecoder presents framed logs while passing historical unframed lines
// through unchanged. Buffering until a newline keeps records intact when a
// followed file is copied in arbitrary chunks.
type planLogDecoder struct {
	out     io.Writer
	pending []byte
}

func (d *planLogDecoder) Write(p []byte) (int, error) {
	d.pending = append(d.pending, p...)
	for {
		newline := bytes.IndexByte(d.pending, '\n')
		if newline < 0 {
			return len(p), nil
		}
		line := d.pending[:newline]
		d.pending = d.pending[newline+1:]
		if err := d.renderLine(line, true); err != nil {
			return len(p), err
		}
	}
}

func (d *planLogDecoder) Flush() error {
	if len(d.pending) == 0 {
		return nil
	}
	line := d.pending
	d.pending = nil
	return d.renderLine(line, false)
}

func (d *planLogDecoder) renderLine(line []byte, newline bool) error {
	if record, ok := logrecord.Parse(string(line)); ok {
		return logrecord.Render(d.out, record)
	}
	if _, err := d.out.Write(line); err != nil {
		return err
	}
	if newline {
		_, err := io.WriteString(d.out, "\n")
		return err
	}
	return nil
}
