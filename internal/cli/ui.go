package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/tui"
)

const defaultUICompletedWindow = 168 * time.Hour

var uiCommand = commandMetadata{
	name:                  "ui",
	usageLines:            []string{"ui [--interval DURATION] [--completed-window DURATION]"},
	completionDescription: "Open the cross-repository interactive plans and notes dashboard",
	long: "Open a keyboard-driven dashboard with Plans and Notes tabs across registered repositories. Plans is the initial tab. Use Tab or the right arrow to advance tabs and the left arrow to move back; j/k or the up/down arrows move within either table. Repository focus is shared across tabs: f focuses the selected plan or note's repository, and f again restores all repositories.\n" +
		"Plans groups work needing attention, running work, planned or in-review work, and recent completions. Heartbeats and the stalled?/crashed? labels are liveness hints, not workflow verdicts. Completed plans are hidden initially; c reveals or hides rows in the configured lookback window. On Plans, r runs, a prompts for approval, m confirms a selected reviewed-plan merge, M confirms a repository-scoped merge --all, and Enter opens plan details. In plan detail, move across slices with j/k or the arrows and press Enter for the full read-only slice page.\n" +
		"Notes lists only repository-owned open notes. Enter opens the selected note's full read-only detail, and Esc returns. Plan actions and the completed toggle do not act on Notes. q and Ctrl-C quit globally except that q safely declines confirmation. Esc returns one page or declines confirmation; at a top-level table, press Esc twice within one second to quit. Run, approval, and merge subprocesses are detached and survive dashboard exit.\n" +
		"tao ui requires a terminal. Use tao monitor --once for non-interactive plan output.",
	examples: "  tao ui\n" +
		"  tao ui --interval 5s\n" +
		"  tao ui --completed-window 24h\n" +
		"  tao ui --completed-window 0",
	registerFlags: registerUIFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"completed-window": {kind: completionValueText, label: "duration"},
		"interval":         {kind: completionValueText, label: "duration"},
	}},
	execute: func(c commandContext) error {
		return c.app.ui(c.ctx, c.args)
	},
}

func registerUIFlags(fs *flag.FlagSet) {
	fs.Duration("interval", defaultMonitorInterval, "dashboard refresh interval (must be greater than zero)")
	fs.Duration("completed-window", defaultUICompletedWindow, "completed-plan lookback window (0 omits completed plans)")
}

func (a App) ui(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("ui", args, registerUIFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao ui [--interval DURATION] [--completed-window DURATION]"); err != nil {
		return err
	}
	interval := flagDurationValue(fs, "interval")
	if interval <= 0 {
		return errors.New("--interval must be greater than zero")
	}
	completedWindow := flagDurationValue(fs, "completed-window")
	if completedWindow < 0 {
		return errors.New("--completed-window must be zero or greater")
	}
	if !a.monitorOutputIsTerminal(a.Out) {
		return errors.New("tao ui requires terminal output; use `tao monitor --once` for non-interactive output")
	}

	terminal := a.UITerminal
	input := a.input()
	if terminal == nil {
		inputFile, ok := input.(*os.File)
		if !ok {
			return errors.New("tao ui requires terminal input")
		}
		terminal = term.NewTerminal(inputFile)
	}
	collector, err := a.newMonitorCollector(false, completedWindow)
	if err != nil {
		return err
	}
	noteCollector, err := a.newUINoteCollector()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("resolve current tao executable: " + err.Error())
	}
	actions, err := tui.NewActions(tui.ActionOptions{
		Executable: executable,
		Launcher:   a.uiCommandLauncher(),
		Now:        a.now,
	})
	if err != nil {
		return err
	}

	signalCtx, cancel := newCommandSignalContext(ctx)
	defer cancel()
	return (tui.App{
		Input:     input,
		Output:    a.Out,
		Terminal:  terminal,
		Ticker:    a.newMonitorTicker(interval),
		Collector: collector,
		Notes:     noteCollector,
		Actions:   actions,
		Now:       a.now,
	}).Run(signalCtx)
}

func (a App) newUINoteCollector() (tui.NoteSnapshotCollector, error) {
	inventory, ok := a.registry().(note.RepositoryInventory)
	if !ok {
		return nil, errors.New("note repository inventory is unavailable")
	}
	collector := note.NewCollector(inventory)
	if a.NoteRepository != nil {
		collector.NewLister = func(entry taodata.RepoInventoryEntry) note.Lister {
			return a.NoteRepository(entry.NotesDir, note.RepoReference{ID: entry.Repo.ID, Root: entry.Repo.Root})
		}
	}
	return collector, nil
}

func (a App) uiCommandLauncher() tui.CommandLauncher {
	if a.UICommandLauncher != nil {
		return a.UICommandLauncher
	}
	runner := a.CommandRunner
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	return func(ctx context.Context, request tui.CommandRequest) error {
		if request.Detached {
			return startDetachedUICommand(request)
		}
		return runner(ctx, request.CWD, request.Executable, request.Args, io.Discard, io.Discard)
	}
}

func startDetachedUICommand(request tui.CommandRequest) error {
	cmd := exec.Command(request.Executable, request.Args...) // #nosec G204 -- executable is the current Tao binary and arguments are structured UI actions.
	cmd.Dir = request.CWD
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Nil child streams are connected to the null device by os/exec. The new
	// session lets runs survive dashboard shutdown.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
