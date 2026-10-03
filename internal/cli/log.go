package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/agent/logrecord"
	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptcapture"
	"github.com/iamseth/tao/internal/taodata"
)

var logCommand = commandMetadata{
	name:                  "log",
	minPrefix:             "lo",
	usageLines:            []string{"log (lo) [--follow | --prompts | --prompt VALUE] <plan-id-or-slug>", "log (lo) --batch [--follow | --prompts | --prompt VALUE] [batch-id]"},
	completionDescription: "Show or follow agent run log",
	long:                  "Show the captured agent run log for a Tao plan, or use --batch to show transitions for the active or named merge batch. Pass --follow (or -f) to stream appended output. Use --prompts to list local captured prompts or --prompt VALUE to print a raw prompt by 1-based index or exact file name; neither supports --follow.",
	examples: "  tao log my-plan\n" +
		"  tao log --follow my-plan\n" +
		"  tao log -f 20260628-1618-kubectl-style-help\n" +
		"  tao log --batch --follow\n" +
		"  tao log --prompts my-plan\n" +
		"  tao log --prompt 1 my-plan\n" +
		"  tao log --batch --prompts",
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
	fs.Bool("prompts", false, "list local captured prompts")
	fs.String("prompt", "", "print a captured prompt by 1-based index or exact file name")
	fs.Bool("batch", false, "show active or named merge-batch transitions")
	fs.BoolVar(&follow, "follow", false, "follow appended output")
	fs.BoolVar(&follow, "f", false, "follow appended output")
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
	selector := fs.Lookup("prompt").Value.String()
	promptSelected := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prompt" {
			promptSelected = true
		}
	})
	prompts := flagBoolValue(fs, "prompts") || promptSelected
	if promptSelected && selector == "" {
		return errors.New("usage: --prompt requires an index or exact file name")
	}
	if prompts && flagBoolValue(fs, "follow") {
		return errors.New("usage: --prompts and --prompt cannot be combined with --follow")
	}
	if flagBoolValue(fs, "batch") {
		if len(positional) > 1 {
			return errors.New("usage: tao log --batch [--follow | --prompts | --prompt VALUE] [batch-id]")
		}
		id := ""
		if len(positional) == 1 {
			id = positional[0]
		}
		return a.logBatch(ctx, id, flagBoolValue(fs, "follow"), prompts, selector)
	}
	if err := requirePositionals(positional, 1, "usage: tao log [--follow | --prompts | --prompt VALUE] <plan-id-or-slug>"); err != nil {
		return err
	}
	id := positional[0]
	detail, err := repo.GetPlan(ctx, id)
	if err != nil {
		return err
	}
	if prompts {
		return renderCapturedPrompts(a.Out, promptcapture.Dir(detail.Dir), "plan", detail.State.Plan.ID, selector)
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

func (a App) logBatch(ctx context.Context, id string, follow, prompts bool, selector string) error {
	var registry mergeBatchRegistry
	if a.Registry != nil {
		var ok bool
		registry, ok = a.Registry().(mergeBatchRegistry)
		if !ok {
			return errors.New("log --batch requires merge-batch registry paths")
		}
	} else {
		defaultRegistry := taodata.NewRegistry("")
		defaultRegistry.Runner = a.mergeRunner()
		registry = defaultRegistry
	}
	current, err := registry.Current(ctx)
	if err != nil {
		return fmt.Errorf("resolve current repository for merge batch: %w", err)
	}
	store := mergepkg.NewBatchStore(registry.MergeBatchesDir(current), registry.ActiveMergeBatchPath(current))
	if id == "" {
		id, err = store.ActiveID()
		if err != nil {
			return err
		}
	}
	if id == "" {
		return errors.New("no active merge batch; provide a batch-id to view a previous batch")
	}
	if prompts {
		return renderCapturedPrompts(a.Out, store.PromptDir(id), "batch", id, selector)
	}
	if err := store.RenderTransitions(ctx, id, a.Out, follow); err != nil {
		return fmt.Errorf("read merge batch %s transitions: %w", id, err)
	}
	return nil
}

func renderCapturedPrompts(out io.Writer, dir, kind, id, selector string) error {
	if strings.ContainsAny(selector, `/\`) {
		return errors.New("usage: --prompt requires an index or exact file name without path separators")
	}
	entries, err := promptcapture.List(dir)
	if err != nil {
		return fmt.Errorf("list captured prompts: %w", err)
	}
	if selector != "" {
		var selected *promptcapture.Entry
		if index, err := strconv.Atoi(selector); err == nil {
			if index < 1 || index > len(entries) {
				return fmt.Errorf("usage: prompt index %d is out of range (1-%d)", index, len(entries))
			}
			selected = &entries[index-1]
		} else {
			for i := range entries {
				if entries[i].Name == selector {
					selected = &entries[i]
					break
				}
			}
		}
		if selected == nil {
			return fmt.Errorf("usage: captured prompt %q not found", selector)
		}
		_, body, err := promptcapture.Read(selected.Path)
		if err != nil {
			return fmt.Errorf("read captured prompt: %w", err)
		}
		_, err = io.WriteString(out, body)
		return err
	}
	if len(entries) == 0 {
		return writef(out, "No captured prompts for %s %s\n", kind, id)
	}
	if err := writeln(out, "INDEX  STARTED_AT  ROLE  TEMPLATE  MODEL  BYTES  SHA256  FILE"); err != nil {
		return err
	}
	for i, entry := range entries {
		h := entry.Header
		marker := ""
		if h.Truncated {
			marker = " (truncated)"
		}
		if err := writef(out, "%d  %s  %s  %s  %s  %d%s  %.12s  %s\n", i+1, h.StartedAt, h.Role, h.Template, h.Model, h.Bytes, marker, h.SHA256, entry.Name); err != nil {
			return err
		}
	}
	return nil
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
