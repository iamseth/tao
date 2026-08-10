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
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/tui"
)

const defaultUICompletedWindow = 168 * time.Hour

var uiCommand = commandMetadata{
	name:                  "ui",
	usageLines:            []string{"ui [--interval DURATION] [--completed-window DURATION]"},
	completionDescription: "Open the cross-repository interactive plan dashboard",
	long: "Open a keyboard-driven dashboard for plans across registered repositories. Sections group plans needing attention, running or queued work, planned or in-review work, and recent completions. Heartbeats and the stalled?/crashed? labels are liveness hints, not workflow verdicts.\n" +
		"Use j/k or the arrow keys to move; r runs, q queues and uses a best-effort check to start a drain, a prompts for approval, c toggles completed rows, and Enter opens plan details. Esc returns from details or quits the table; Ctrl-C also quits. Run, drain, and approval subprocesses are detached and survive dashboard exit.\n" +
		"Use tao monitor --once for non-interactive output.",
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
		Actions:   actions,
	}).Run(signalCtx)
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
