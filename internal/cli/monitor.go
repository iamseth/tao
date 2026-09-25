package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

const (
	defaultMonitorInterval = 2 * time.Second
	monitorClearScreen     = "\x1b[H\x1b[2J"
)

var monitorCommand = commandMetadata{
	name:                  "monitor",
	minPrefix:             "mon",
	usageLines:            []string{"monitor (mon) [--once] [--interval DURATION] [--show-invalid]"},
	completionDescription: "Monitor non-completed plans across repositories",
	long:                  "Continuously monitor valid, non-completed plans across registered repositories. Use --show-invalid to include damaged plans for diagnostics. Interactive terminal output refreshes in place; --once and redirected output render one plain snapshot. Heartbeats report process liveness, not semantic progress or success.",
	examples: "  tao monitor\n" +
		"  tao monitor --once\n" +
		"  tao monitor --interval 5s\n" +
		"  tao monitor --show-invalid",
	registerFlags: registerMonitorFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"interval": {kind: completionValueText, label: "duration"},
	}},
	execute: func(c commandContext) error {
		return c.app.monitor(c.ctx, c.args)
	},
}

// MonitorSnapshotCollector is the read-only refresh boundary used by monitor.
type MonitorSnapshotCollector interface {
	Collect(context.Context) (monitor.Snapshot, error)
}

// MonitorTicker is the refresh timer boundary used by monitor.
type MonitorTicker interface {
	C() <-chan time.Time
	Stop()
}

type wallMonitorTicker struct{ *time.Ticker }

func (t wallMonitorTicker) C() <-chan time.Time { return t.Ticker.C }

func registerMonitorFlags(fs *flag.FlagSet) {
	fs.Bool("once", false, "render one snapshot and exit")
	fs.Duration("interval", defaultMonitorInterval, "interactive refresh interval")
	fs.Bool("show-invalid", false, "include invalid plans for diagnostics")
}

func (a App) monitor(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("monitor", args, registerMonitorFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao monitor [--once] [--interval DURATION] [--show-invalid]"); err != nil {
		return err
	}
	interval := flagDurationValue(fs, "interval")
	if interval <= 0 {
		return errors.New("--interval must be greater than zero")
	}

	terminal := a.monitorOutputIsTerminal(a.Out)
	interactive := !flagBoolValue(fs, "once") && terminal
	collector, err := a.monitorCollector(flagBoolValue(fs, "show-invalid"))
	if err != nil {
		return err
	}
	if !interactive {
		return writeMonitorSnapshot(ctx, a.Out, collector, false, false)
	}

	ctx, cancel := newCommandSignalContext(ctx)
	defer cancel()
	if err := writeMonitorSnapshot(ctx, a.Out, collector, true, monitorColorEnabled(terminal)); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	ticker := a.newMonitorTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
			if err := writeMonitorSnapshot(ctx, a.Out, collector, true, monitorColorEnabled(terminal)); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
		}
	}
}

func (a App) monitorCollector(showInvalid bool) (MonitorSnapshotCollector, error) {
	return a.newMonitorCollector(showInvalid, 0)
}

func (a App) newMonitorCollector(showInvalid bool, completedWindow time.Duration) (MonitorSnapshotCollector, error) {
	if a.MonitorCollector != nil {
		return a.MonitorCollector, nil
	}
	inventory, ok := a.registry().(monitor.RepositoryInventory)
	if !ok {
		return nil, errors.New("monitor repository inventory is unavailable")
	}
	collector := monitor.NewCollector(inventory)
	collector.Now = a.now
	collector.ShowInvalid = showInvalid
	collector.IncludeCompletedWithin = completedWindow
	collector.NewPlanLister = func(entry taodata.RepoInventoryEntry) monitor.PlanLister {
		return a.repository(entry.PlansDir)
	}
	return collector, nil
}

func (a App) newMonitorTicker(interval time.Duration) MonitorTicker {
	if a.MonitorTicker != nil {
		return a.MonitorTicker(interval)
	}
	return wallMonitorTicker{Ticker: time.NewTicker(interval)}
}

func (a App) monitorOutputIsTerminal(out io.Writer) bool {
	if a.MonitorIsTerminal != nil {
		return a.MonitorIsTerminal(out)
	}
	return outputIsTerminal(out)
}

func monitorColorEnabled(isTerminal bool) bool {
	return term.ColorEnabled(isTerminal, os.Getenv)
}

func flagDurationValue(fs *flag.FlagSet, name string) time.Duration {
	fl := fs.Lookup(name)
	if fl == nil {
		return 0
	}
	if getter, ok := fl.Value.(flag.Getter); ok {
		if value, ok := getter.Get().(time.Duration); ok {
			return value
		}
	}
	value, _ := time.ParseDuration(fl.Value.String())
	return value
}

func writeMonitorSnapshot(ctx context.Context, out io.Writer, collector MonitorSnapshotCollector, redraw, useColor bool) error {
	snapshot, err := collector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("refresh monitor: %w", err)
	}
	var rendered bytes.Buffer
	if err := renderMonitorSnapshot(&rendered, snapshot, useColor); err != nil {
		return err
	}
	if redraw {
		if _, err := io.WriteString(out, monitorClearScreen); err != nil {
			return err
		}
	}
	_, err = io.Copy(out, &rendered)
	return err
}

func renderMonitorSnapshot(out io.Writer, snapshot monitor.Snapshot, useColor bool) error {
	if len(snapshot.Rows) == 0 {
		return writeln(out, "No non-completed plans.")
	}
	headers := []string{"LIVE", "STATUS", "REPO", "PLAN ID/name", "PHASE", "RUN", "SLICES", "UPDATED"}
	rows := make([][]string, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		rows = append(rows, monitorRowValues(row, snapshot.CollectedAt).columns())
	}
	widths := cells.ColumnWidths(headers, rows)
	if err := writef(out, "%s  %s  %s  %s  %s  %s  %s  %s\n",
		cells.Pad(headers[0], widths[0]),
		cells.Pad(headers[1], widths[1]),
		cells.Pad(headers[2], widths[2]),
		cells.Pad(headers[3], widths[3]),
		cells.Pad(headers[4], widths[4]),
		cells.Pad(headers[5], widths[5]),
		cells.Pad(headers[6], widths[6]),
		cells.Pad(headers[7], widths[7]),
	); err != nil {
		return err
	}

	for index, values := range rows {
		row := snapshot.Rows[index]
		live := cells.Pad(values[0], widths[0])
		status := cells.Pad(values[1], widths[1])
		slices := cells.Pad(values[6], widths[6])
		if useColor {
			live = colorMonitorLiveness(live, row.Liveness)
			status = colorStatus(status, row.Status)
			slices = colorDone(
				slices,
				row.OriginalCompletedCount+row.ReworkCompletedCount,
				row.OriginalTotalCount+row.ReworkTotalCount,
			)
		}
		if err := writef(out, "%s  %s  %s  %s  %s  %s  %s  %s\n",
			live,
			status,
			cells.Pad(values[2], widths[2]),
			cells.Pad(values[3], widths[3]),
			cells.Pad(values[4], widths[4]),
			cells.Pad(values[5], widths[5]),
			slices,
			cells.Pad(values[7], widths[7]),
		); err != nil {
			return err
		}
	}
	for _, row := range snapshot.Rows {
		for _, warning := range row.Warnings {
			if err := writef(out, "warning: %s: %s\n", monitorWarningLabel(row), warning); err != nil {
				return err
			}
		}
	}
	return nil
}

type monitorValues struct {
	live    string
	status  string
	repo    string
	plan    string
	phase   string
	run     string
	slices  string
	updated string
}

func (v monitorValues) columns() []string {
	return []string{v.live, v.status, v.repo, v.plan, v.phase, v.run, v.slices, v.updated}
}

func monitorRowValues(row monitor.Row, now time.Time) monitorValues {
	runtime := "-"
	if row.Liveness == monitor.LivenessLive || row.Liveness == monitor.LivenessStale {
		runtime = rowlabel.DurationLabel(row.InvocationDuration)
	}
	return monitorValues{
		live:    monitorLivenessLabel(row.Liveness),
		status:  rowlabel.DisplayValue(row.Status),
		repo:    rowlabel.DisplayValue(row.RepositoryName),
		plan:    rowlabel.PlanLabel(row),
		phase:   rowlabel.PhaseLabel(row),
		run:     runtime,
		slices:  rowlabel.SlicesLabel(row),
		updated: plan.FormatHumanTime(row.UpdatedAt, now),
	}
}

func monitorLivenessLabel(liveness monitor.Liveness) string {
	switch liveness {
	case monitor.LivenessLive:
		return "LIVE"
	case monitor.LivenessStale:
		return "STALE"
	default:
		return "-"
	}
}

func monitorWarningLabel(row monitor.Row) string {
	repo := rowlabel.DisplayValue(row.RepositoryName)
	if row.PlanID == "" {
		return repo
	}
	return repo + "/" + row.PlanID
}

func colorMonitorLiveness(value string, liveness monitor.Liveness) string {
	switch liveness {
	case monitor.LivenessLive:
		return color(value, "36")
	case monitor.LivenessStale:
		return color(value, "33")
	default:
		return value
	}
}
