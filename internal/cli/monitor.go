package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	planview "github.com/iamseth/tao/internal/view"
)

const (
	defaultMonitorInterval      = 2 * time.Second
	monitorClearScreen          = "\x1b[H\x1b[2J"
	monitorSliceIDMaxCharacters = 20
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
	if err := writeMonitorSnapshot(ctx, a.Out, collector, true, monitorColorEnabled()); err != nil {
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
			if err := writeMonitorSnapshot(ctx, a.Out, collector, true, monitorColorEnabled()); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
		}
	}
}

func (a App) monitorCollector(showInvalid bool) (MonitorSnapshotCollector, error) {
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

func monitorColorEnabled() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
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

type monitorWidths struct {
	live    int
	status  int
	repo    int
	plan    int
	phase   int
	run     int
	slices  int
	updated int
}

func renderMonitorSnapshot(out io.Writer, snapshot monitor.Snapshot, useColor bool) error {
	if len(snapshot.Rows) == 0 {
		return writeln(out, "No non-completed plans.")
	}
	widths := monitorColumnWidths(snapshot)
	if err := writef(out, "%s  %s  %s  %s  %s  %s  %s  %s\n",
		pad("LIVE", widths.live),
		pad("STATUS", widths.status),
		pad("REPO", widths.repo),
		pad("PLAN ID/name", widths.plan),
		pad("PHASE", widths.phase),
		pad("RUN", widths.run),
		pad("SLICES", widths.slices),
		pad("UPDATED", widths.updated),
	); err != nil {
		return err
	}

	for _, row := range snapshot.Rows {
		values := monitorRowValues(row, snapshot.CollectedAt)
		live := pad(values.live, widths.live)
		status := pad(values.status, widths.status)
		slices := pad(values.slices, widths.slices)
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
			pad(values.repo, widths.repo),
			pad(values.plan, widths.plan),
			pad(values.phase, widths.phase),
			pad(values.run, widths.run),
			slices,
			pad(values.updated, widths.updated),
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

func monitorRowValues(row monitor.Row, now time.Time) monitorValues {
	return monitorValues{
		live:    monitorLivenessLabel(row.Liveness),
		status:  emptyMonitorValue(row.Status),
		repo:    emptyMonitorValue(row.RepositoryName),
		plan:    monitorPlanLabel(row),
		phase:   monitorPhaseLabel(row),
		run:     formatMonitorRuntime(row),
		slices:  monitorSlicesLabel(row),
		updated: plan.FormatHumanTime(row.UpdatedAt, now),
	}
}

func monitorSlicesLabel(row monitor.Row) string {
	completed := row.OriginalCompletedCount + row.ReworkCompletedCount
	value := fmt.Sprintf("%d/%d", completed, row.OriginalTotalCount)
	if row.ReworkTotalCount > 0 {
		value += fmt.Sprintf("+%d", row.ReworkTotalCount)
	}
	return value
}

func formatMonitorRuntime(row monitor.Row) string {
	if row.Liveness != monitor.LivenessLive && row.Liveness != monitor.LivenessStale {
		return "-"
	}
	duration := max(row.InvocationDuration, 0)
	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%ds", duration/time.Second)
	case duration < time.Hour:
		return fmt.Sprintf("%dm", duration/time.Minute)
	default:
		return fmt.Sprintf("%dh", duration/time.Hour)
	}
}

func monitorColumnWidths(snapshot monitor.Snapshot) monitorWidths {
	widths := monitorWidths{
		live: len("LIVE"), status: len("STATUS"), repo: len("REPO"), plan: len("PLAN ID/name"),
		phase: len("PHASE"), run: len("RUN"), slices: len("SLICES"), updated: len("UPDATED"),
	}
	for _, row := range snapshot.Rows {
		values := monitorRowValues(row, snapshot.CollectedAt)
		widths.live = max(widths.live, utf8.RuneCountInString(values.live))
		widths.status = max(widths.status, utf8.RuneCountInString(values.status))
		widths.repo = max(widths.repo, utf8.RuneCountInString(values.repo))
		widths.plan = max(widths.plan, utf8.RuneCountInString(values.plan))
		widths.phase = max(widths.phase, utf8.RuneCountInString(values.phase))
		widths.run = max(widths.run, utf8.RuneCountInString(values.run))
		widths.slices = max(widths.slices, utf8.RuneCountInString(values.slices))
		widths.updated = max(widths.updated, utf8.RuneCountInString(values.updated))
	}
	return widths
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

func monitorPhaseLabel(row monitor.Row) string {
	phase := strings.TrimSpace(string(row.Phase))
	if phase == "" {
		phase = "-"
	}
	if phase == "running_slice" {
		sliceID := strings.TrimSpace(row.SliceID)
		if sliceID != "" {
			runes := []rune(sliceID)
			if len(runes) > monitorSliceIDMaxCharacters {
				runes = runes[:monitorSliceIDMaxCharacters]
			}
			phase = string(runes)
		}
	}
	if row.Liveness == monitor.LivenessStale {
		return fmt.Sprintf("%s (%s old)", phase, plan.FormatDuration(row.HeartbeatAge))
	}
	return phase
}

func monitorPlanLabel(row monitor.Row) string {
	if strings.TrimSpace(row.PlanID) == "" {
		return emptyMonitorValue(row.PlanTitle)
	}
	if _, ok := plan.PlanSlug(row.PlanID); ok {
		return row.PlanID
	}
	name := strings.TrimSpace(row.PlanTitle)
	if name == "" || name == row.PlanID {
		return row.PlanID
	}
	return planview.ShortPlanID(row.PlanID) + " " + name
}

func monitorWarningLabel(row monitor.Row) string {
	repo := emptyMonitorValue(row.RepositoryName)
	if row.PlanID == "" {
		return repo
	}
	return repo + "/" + row.PlanID
}

func emptyMonitorValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
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
