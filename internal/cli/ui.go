package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/staleness"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/tui"
	"github.com/iamseth/tao/internal/uistate"
)

const defaultUICompletedWindow = "all"

var uiCommand = commandMetadata{
	name:                  "ui",
	usageLines:            []string{"ui [--interval DURATION] [--completed-window DURATION|all]"},
	completionDescription: "Open the cross-repository interactive dashboard",
	long: "Open a keyboard-driven dashboard for plans and open notes across registered repositories. WIP is the initial tab, alongside Backlog and History.\n" +
		"Press ? in the dashboard for keyboard shortcuts. See docs/usage-guide.md for workflow guidance.\n" +
		"tao ui requires a terminal. Use tao monitor --once for non-interactive plan output.",
	examples: "  tao ui\n" +
		"  tao ui --interval 5s\n" +
		"  tao ui --completed-window 24h\n" +
		"  tao ui --completed-window 0",
	registerFlags: registerUIFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"completed-window": {kind: completionValueText, label: "duration or all"},
		"interval":         {kind: completionValueText, label: "duration"},
	}},
	execute: func(c commandContext) error {
		return c.app.ui(c.ctx, c.args)
	},
}

func registerUIFlags(fs *flag.FlagSet) {
	fs.Duration("interval", defaultMonitorInterval, "dashboard refresh interval (must be greater than zero)")
	fs.String("completed-window", defaultUICompletedWindow, "completed-plan lookback window (all includes all history; 0 omits completed plans)")
}

func (a App) ui(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("ui", args, registerUIFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao ui [--interval DURATION] [--completed-window DURATION|all]"); err != nil {
		return err
	}
	interval := flagDurationValue(fs, "interval")
	if interval <= 0 {
		return errors.New("--interval must be greater than zero")
	}
	completedValue := flagStringValue(fs, "completed-window")
	allCompleted := completedValue == "all"
	var completedWindow time.Duration
	if !allCompleted {
		completedWindow, err = time.ParseDuration(completedValue)
		if err != nil {
			return fmt.Errorf("--completed-window must be all or a duration: %w", err)
		}
	}
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
	collector, err := a.newMonitorCollectorWithHistory(false, completedWindow, allCompleted)
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
	noteCreator := a.UINoteCreator
	if noteCreator == nil {
		noteCreator = newUINoteEditor(a, input, a.Out)
	}
	noteRepositories := a.UINoteRepositories
	if noteRepositories == nil {
		noteRepositories = newUINoteEditor(a, input, a.Out)
	}
	noteEditor := a.UINoteEditor
	if noteEditor == nil {
		noteEditor = newUINoteEditor(a, input, a.Out)
	}
	noteActions := a.UINoteActions
	if noteActions == nil {
		if actions, ok := noteEditor.(tui.NoteActions); ok {
			noteActions = actions
		} else {
			noteActions = newUINoteEditor(a, input, a.Out)
		}
	}
	clipboard := a.UIClipboard
	if clipboard == nil {
		clipboard = newUIClipboard(a)
	}

	signals := newUIPlanningSignals(ctx)
	defer signals.Close()
	notePlanning := a.uiNotePlanningLauncher(input, signals)
	return (tui.App{
		Theme:                a.outputTheme(),
		Input:                input,
		Output:               a.Out,
		Terminal:             terminal,
		Ticker:               a.newMonitorTicker(interval),
		Collector:            collector,
		Notes:                noteCollector,
		NoteCreator:          noteCreator,
		NoteRepositories:     noteRepositories,
		NoteEditor:           noteEditor,
		NotePlanningLauncher: notePlanning,
		PlanFixLauncher:      a.uiPlanFixLauncher(input, signals),
		NoteActions:          noteActions,
		Clipboard:            clipboard,
		Debug:                newUIDebugCollector(a, executable),
		Settings:             newUISettingsService(a),
		FilterStore:          newUIFilterStore(),
		SortStore:            newUISortStore(),
		Actions:              actions,
		Inspector:            newUIDetailInspector(a.uiCommandRunner()),
		Changes:              newUIDetailChangesLoader(a.uiCommandRunner()),
		Now:                  a.now,
	}).Run(signals.Context())
}

func (a App) uiNotePlanningLauncher(input io.Reader, signals *uiPlanningSignals) tui.NotePlanningLauncher {
	launcher := a.UINotePlanningLauncher
	if launcher == nil {
		launcher = newUINotePlanningLauncher(a, input, a.Out)
	}
	return scopedNotePlanningLauncher{signals: signals, launcher: launcher}
}

func (a App) uiPlanFixLauncher(input io.Reader, signals *uiPlanningSignals) tui.PlanFixLauncher {
	launcher := a.UIPlanFixLauncher
	if launcher == nil {
		launcher = newUIPlanFixLauncher(a, input, a.Out)
	}
	return scopedPlanFixLauncher{signals: signals, launcher: launcher}
}

type uiFilterStore struct {
	store uistate.Store
}

func newUIFilterStore() tui.FilterStore {
	return uiFilterStore{store: uistate.Store{DataHome: taodata.DataHome()}}
}

func (s uiFilterStore) Load(ctx context.Context) (tui.Filter, error) {
	if err := ctx.Err(); err != nil {
		return tui.Filter{}, err
	}
	filters, err := s.store.Load()
	if err != nil {
		return tui.Filter{}, err
	}
	return tui.Filter{
		Enabled:      filters.Enabled,
		Repositories: filters.Repositories,
		Statuses:     filters.Statuses,
	}, nil
}

func (s uiFilterStore) Save(ctx context.Context, filter tui.Filter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.store.Load()
	if err != nil {
		return err
	}
	current.Version = 1
	current.Enabled = filter.Enabled
	current.Repositories = filter.Repositories
	current.Statuses = filter.Statuses
	return s.store.Save(current)
}

type uiSortStore struct{ store uistate.Store }

func newUISortStore() tui.SortStore {
	return uiSortStore{store: uistate.Store{DataHome: taodata.DataHome()}}
}

func (s uiSortStore) Load(ctx context.Context) (tui.PageSorts, error) {
	if err := ctx.Err(); err != nil {
		return tui.PageSorts{}, err
	}
	current, err := s.store.Load()
	if err != nil {
		return tui.PageSorts{}, err
	}
	setting := func(value *uistate.SortSetting) tui.Sort {
		if value == nil {
			return tui.Sort{Column: tui.SortDefault}
		}
		return tui.Sort{Column: tui.SortColumn(value.Column), Descending: value.Descending}
	}
	sorts := current.Sort
	if sorts == nil {
		sorts = &uistate.Sorts{}
	}
	return tui.PageSorts{Plans: setting(sorts.Plans), History: setting(sorts.History), Notes: setting(sorts.Notes)}, nil
}

func (s uiSortStore) Save(ctx context.Context, sorts tui.PageSorts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.store.Load()
	if err != nil {
		return err
	}
	setting := func(value tui.Sort) *uistate.SortSetting {
		return &uistate.SortSetting{Column: string(value.Column), Descending: value.Descending}
	}
	current.Version = 1
	current.Sort = &uistate.Sorts{Plans: setting(sorts.Plans), History: setting(sorts.History), Notes: setting(sorts.Notes)}
	return s.store.Save(current)
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

func newUIDetailInspector(runner commandrunner.Runner) tui.DetailInspector {
	return tui.DetailInspectorFunc(func(ctx context.Context, detail *plan.PlanDetail) (tui.DetailInspection, error) {
		findings := staleness.Findings(ctx, detail, runner)
		result := tui.DetailInspection{Findings: make([]tui.DetailFinding, 0, len(findings))}
		for _, finding := range findings {
			result.Findings = append(result.Findings, tui.DetailFinding{Severity: finding.Severity, Message: finding.Message})
		}
		return result, nil
	})
}

func (a App) uiCommandRunner() commandrunner.Runner {
	if a.CommandRunner != nil {
		return a.CommandRunner
	}
	return commandrunner.DefaultLocal
}

func (a App) uiCommandLauncher() tui.CommandLauncher {
	if a.UICommandLauncher != nil {
		return a.UICommandLauncher
	}
	runner := a.uiCommandRunner()
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
	if request.StderrPath != "" {
		file, err := os.OpenFile(request.StderrPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 G703 -- path is the Tao-owned per-plan launch log supplied by dashboard actions.
		if err == nil {
			cmd.Stderr = file
			defer func() { _ = file.Close() }()
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
