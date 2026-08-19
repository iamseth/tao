package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

type monitorCollectorStub struct {
	mu        sync.Mutex
	snapshots []monitor.Snapshot
	errors    []error
	calls     int
	called    chan int
}

func (s *monitorCollectorStub) Collect(context.Context) (monitor.Snapshot, error) {
	s.mu.Lock()
	index := s.calls
	s.calls++
	var snapshot monitor.Snapshot
	if len(s.snapshots) > 0 {
		snapshot = s.snapshots[min(index, len(s.snapshots)-1)]
	}
	var err error
	if len(s.errors) > 0 {
		err = s.errors[min(index, len(s.errors)-1)]
	}
	s.mu.Unlock()
	if s.called != nil {
		s.called <- index + 1
	}
	return snapshot, err
}

type monitorTickerStub struct {
	ch      chan time.Time
	stopped chan struct{}
}

func (t *monitorTickerStub) C() <-chan time.Time { return t.ch }
func (t *monitorTickerStub) Stop()               { close(t.stopped) }

type monitorRegistryStub struct {
	entries []taodata.RepoInventoryEntry
}

func (s monitorRegistryStub) MetadataInventory() ([]taodata.RepoInventoryEntry, error) {
	return s.entries, nil
}

func (monitorRegistryStub) Current(context.Context) (taodata.Repo, error) { return taodata.Repo{}, nil }
func (monitorRegistryStub) ReadRepo(string) (taodata.Repo, error)         { return taodata.Repo{}, nil }
func (monitorRegistryStub) ListRepos() ([]taodata.Repo, error)            { return nil, nil }
func (monitorRegistryStub) NotesDir(taodata.Repo) string                  { return "" }
func (monitorRegistryStub) PlansDir(taodata.Repo) string                  { return "" }

func TestMonitorUsesRegistryRepositoryOutputAndClockSeams(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	plansDir := t.TempDir()
	statusDir := t.TempDir()
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}, PlansDir: plansDir, RuntimeStatusDir: statusDir}
	var requestedDir string
	var out bytes.Buffer
	app := App{
		Out:      &out,
		Err:      &out,
		Now:      func() time.Time { return now },
		Registry: func() NoteRegistry { return monitorRegistryStub{entries: []taodata.RepoInventoryEntry{entry}} },
		Repository: func(dir string) Repository {
			requestedDir = dir
			return fakeRepository{summaries: []plan.PlanSummary{{ID: "cross-repo", Title: "Cross Repo", Status: plan.StatusPlanned}}}
		},
		MonitorIsTerminal: func(io.Writer) bool { return false },
	}
	if err := app.Run(context.Background(), []string{"monitor"}); err != nil {
		t.Fatal(err)
	}
	if requestedDir != plansDir {
		t.Fatalf("repository dir = %q, want %q", requestedDir, plansDir)
	}
	if !strings.Contains(out.String(), "cross-repo Cross Repo") {
		t.Fatalf("monitor output did not use registry repository: %q", out.String())
	}
}

func TestMonitorShowInvalidControlsInvalidPlanRows(t *testing.T) {
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo", Name: "repo"}, PlansDir: t.TempDir(), RuntimeStatusDir: t.TempDir()}
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "hidden by default", args: []string{"monitor"}},
		{name: "shown for diagnostics", args: []string{"monitor", "--show-invalid"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			app := App{
				Out:      &out,
				Err:      &out,
				Registry: func() NoteRegistry { return monitorRegistryStub{entries: []taodata.RepoInventoryEntry{entry}} },
				Repository: func(string) Repository {
					return fakeRepository{summaries: []plan.PlanSummary{{ID: "damaged-plan", Status: plan.StatusInvalid, Warnings: []string{"invalid state.json"}}}}
				},
				MonitorIsTerminal: func(io.Writer) bool { return false },
			}
			if err := app.Run(context.Background(), test.args); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(out.String(), "damaged-plan"); got != test.want {
				t.Fatalf("invalid plan shown = %t, want %t: %q", got, test.want, out.String())
			}
		})
	}
}

func TestMonitorOnceRendersPlainColumnsLivenessAndWarnings(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	updated := now.Add(-3 * time.Minute)
	collector := &monitorCollectorStub{snapshots: []monitor.Snapshot{{
		CollectedAt: now,
		Rows: []monitor.Row{
			{
				Kind: monitor.RowKindPlan, RepositoryName: "tao", PlanID: "20260729-044616-plan-monitor", PlanTitle: "Cross-Repository Plan Monitor",
				Status: plan.StatusInProgress, Liveness: monitor.LivenessLive, Phase: runstatus.Phase("running_slice"), SliceID: "r102-compact-monitor", InvocationDuration: 5*time.Minute + 4*time.Second,
				Left: 1, OriginalCompletedCount: 1, OriginalTotalCount: 3, UpdatedAt: &updated,
			},
			{
				Kind: monitor.RowKindPlan, RepositoryName: "api", PlanID: "stale-plan", Status: plan.StatusBlocked, Liveness: monitor.LivenessStale,
				Phase: runstatus.Phase("verify"), InvocationDuration: 8 * time.Minute, HeartbeatAge: 25 * time.Second, Left: 2,
				OriginalCompletedCount: 3, OriginalTotalCount: 3, ReworkCompletedCount: 1, ReworkTotalCount: 6, Warnings: []string{"heartbeat record is old"},
			},
			{Kind: monitor.RowKindPlan, RepositoryName: "web", PlanID: "quiet", Status: plan.StatusPlanned, Liveness: monitor.LivenessMissing},
		},
	}}}
	var out bytes.Buffer
	app := App{
		Out: &out, Err: &out, MonitorCollector: collector,
		MonitorIsTerminal: func(io.Writer) bool { return true },
		MonitorTicker:     func(time.Duration) MonitorTicker { t.Fatal("once mode created ticker"); return nil },
	}
	if err := app.Run(context.Background(), []string{"monitor", "--once"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	lines := strings.Split(text, "\n")
	const wantHeader = "LIVE   STATUS       REPO  PLAN ID/name                  PHASE                 RUN  SLICES  UPDATED"
	if got := lines[0]; got != wantHeader {
		t.Fatalf("monitor header = %q, want %q", got, wantHeader)
	}
	for _, removed := range []string{"RUN FOR", "LEFT", "ORIGINAL", "REWORK"} {
		if strings.Contains(lines[0], removed) {
			t.Fatalf("monitor header retained %q: %q", removed, lines[0])
		}
	}
	for _, want := range []string{
		"LIVE", "in_progress", "tao", "20260729-044616-plan-monitor", "r102-compact-monitor", "5m", "1/3", "3m",
		"STALE", "verify (25s old)", "4/3+6", "warning: api/stale-plan: heartbeat record is old",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("monitor output missing %q:\n%s", want, text)
		}
	}
	for _, removed := range []string{"5m04s", "3/4", "1/2"} {
		if strings.Contains(text, removed) {
			t.Fatalf("monitor output retained %q:\n%s", removed, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("--once output contained ANSI: %q", text)
	}
	quietLine := lineContaining(text, "quiet")
	if !strings.Contains(quietLine, "-      planned") {
		t.Fatalf("missing live state was not neutral: %q", quietLine)
	}
}

func TestMonitorSliceProgressNotation(t *testing.T) {
	for _, test := range []struct {
		name string
		row  monitor.Row
		want string
	}{
		{name: "original", row: monitor.Row{OriginalCompletedCount: 1, OriginalTotalCount: 3}, want: "1/3"},
		{name: "with added slices", row: monitor.Row{OriginalCompletedCount: 3, OriginalTotalCount: 3, ReworkCompletedCount: 1, ReworkTotalCount: 6}, want: "4/3+6"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := monitorSlicesLabel(test.row); got != test.want {
				t.Fatalf("monitorSlicesLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMonitorPhaseUsesBoundedActiveSliceID(t *testing.T) {
	for _, test := range []struct {
		name string
		row  monitor.Row
		want string
	}{
		{name: "short active id", row: monitor.Row{Phase: runstatus.Phase("running_slice"), SliceID: "r102-compact-monitor"}, want: "r102-compact-monitor"},
		{name: "long active id", row: monitor.Row{Phase: runstatus.Phase("running_slice"), SliceID: "12345678901234567890tail"}, want: "12345678901234567890"},
		{name: "unicode boundary", row: monitor.Row{Phase: runstatus.Phase("running_slice"), SliceID: strings.Repeat("界", 21)}, want: strings.Repeat("界", 20)},
		{name: "missing active id", row: monitor.Row{Phase: runstatus.Phase("running_slice")}, want: "running_slice"},
		{name: "other phase ignores slice", row: monitor.Row{Phase: runstatus.Phase("verify"), SliceID: "001-work"}, want: "verify"},
		{name: "missing phase", row: monitor.Row{SliceID: "001-work"}, want: "-"},
		{
			name: "stale active slice",
			row:  monitor.Row{Phase: runstatus.Phase("running_slice"), SliceID: "12345678901234567890tail", Liveness: monitor.LivenessStale, HeartbeatAge: 25 * time.Second},
			want: "12345678901234567890 (25s old)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := monitorPhaseLabel(test.row); got != test.want {
				t.Fatalf("monitorPhaseLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMonitorRuntimeUsesMagnitudePrecision(t *testing.T) {
	for _, test := range []struct {
		name     string
		liveness monitor.Liveness
		duration time.Duration
		want     string
	}{
		{name: "no runtime record", duration: 45 * time.Second, want: "-"},
		{name: "just started", liveness: monitor.LivenessLive, want: "0s"},
		{name: "subsecond", liveness: monitor.LivenessLive, duration: 999 * time.Millisecond, want: "0s"},
		{name: "seconds floor", liveness: monitor.LivenessLive, duration: 59*time.Second + 999*time.Millisecond, want: "59s"},
		{name: "minute boundary", liveness: monitor.LivenessLive, duration: time.Minute, want: "1m"},
		{name: "minutes floor", liveness: monitor.LivenessStale, duration: 59*time.Minute + 59*time.Second, want: "59m"},
		{name: "hour boundary", liveness: monitor.LivenessLive, duration: time.Hour, want: "1h"},
		{name: "hours floor", liveness: monitor.LivenessLive, duration: 25*time.Hour + 59*time.Minute, want: "25h"},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := monitor.Row{Liveness: test.liveness, InvocationDuration: test.duration}
			if got := formatMonitorRuntime(row); got != test.want {
				t.Fatalf("formatMonitorRuntime() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMonitorCombinedSliceColorPreservesPlainAlignment(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{
		{PlanID: "complete", OriginalCompletedCount: 3, OriginalTotalCount: 3, ReworkCompletedCount: 2, ReworkTotalCount: 2},
		{PlanID: "partial", OriginalCompletedCount: 3, OriginalTotalCount: 3, ReworkCompletedCount: 1, ReworkTotalCount: 2},
	}}
	var plain, colored bytes.Buffer
	if err := renderMonitorSnapshot(&plain, snapshot, false); err != nil {
		t.Fatal(err)
	}
	if err := renderMonitorSnapshot(&colored, snapshot, true); err != nil {
		t.Fatal(err)
	}
	if got := stripANSI(colored.String()); got != plain.String() {
		t.Fatalf("ANSI changed monitor alignment\ncolored stripped:\n%s\nplain:\n%s", got, plain.String())
	}
	completeLine := lineContaining(colored.String(), "complete")
	if !strings.Contains(completeLine, "\x1b[32m5/3+2") {
		t.Fatalf("combined complete progress was not green: %q", completeLine)
	}
	partialLine := lineContaining(colored.String(), "partial")
	if !strings.Contains(partialLine, "\x1b[36m4/3+2") {
		t.Fatalf("combined partial progress was not cyan: %q", partialLine)
	}
}

func TestMonitorUnicodeColumnsMatchExactPlainAndColoredOutput(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{
		RepositoryName:         "倉庫名前長",
		PlanID:                 "unicode",
		PlanTitle:              "日本語の計画",
		Status:                 plan.StatusInProgress,
		Liveness:               monitor.LivenessLive,
		Phase:                  runstatus.Phase("検証段階中"),
		OriginalCompletedCount: 1,
		OriginalTotalCount:     2,
		Warnings:               []string{"this warning is intentionally wider than the table"},
	}}}
	const want = "LIVE  STATUS       REPO   PLAN ID/name    PHASE  RUN  SLICES  UPDATED\n" +
		"LIVE  in_progress  倉庫名前長  unicode 日本語の計画  検証段階中  0s   1/2     -      \n" +
		"warning: 倉庫名前長/unicode: this warning is intentionally wider than the table\n"

	var plain, colored bytes.Buffer
	if err := renderMonitorSnapshot(&plain, snapshot, false); err != nil {
		t.Fatal(err)
	}
	if err := renderMonitorSnapshot(&colored, snapshot, true); err != nil {
		t.Fatal(err)
	}
	if got := plain.String(); got != want {
		t.Fatalf("plain Unicode monitor output = %q, want %q", got, want)
	}
	if got := stripANSI(colored.String()); got != want {
		t.Fatalf("colored Unicode monitor output stripped = %q, want %q", got, want)
	}
}

func TestMonitorRedirectedOutputIsAutomaticallyOnceAndPlain(t *testing.T) {
	collector := &monitorCollectorStub{snapshots: []monitor.Snapshot{{}}}
	var out bytes.Buffer
	app := App{
		Out: &out, Err: &out, MonitorCollector: collector,
		MonitorIsTerminal: func(io.Writer) bool { return false },
		MonitorTicker:     func(time.Duration) MonitorTicker { t.Fatal("redirected monitor created ticker"); return nil },
	}
	if err := app.Run(context.Background(), []string{"monitor"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "No non-completed plans.\n" {
		t.Fatalf("redirected output = %q", got)
	}
}

func TestMonitorInteractiveRefreshUsesIntervalRedrawsAndCancels(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	collector := &monitorCollectorStub{snapshots: []monitor.Snapshot{{}, {}}, called: make(chan int, 2)}
	ticker := &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
	var gotInterval time.Duration
	var out testTerminalBuffer
	ctx, cancel := context.WithCancel(context.Background())
	app := App{
		Out: &out, Err: &out, MonitorCollector: collector,
		MonitorIsTerminal: func(io.Writer) bool { return true },
		MonitorTicker:     func(interval time.Duration) MonitorTicker { gotInterval = interval; return ticker },
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, []string{"monitor", "--interval", "350ms"}) }()
	if got := <-collector.called; got != 1 {
		t.Fatalf("first collection call = %d", got)
	}
	ticker.ch <- time.Now()
	if got := <-collector.called; got != 2 {
		t.Fatalf("second collection call = %d", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if gotInterval != 350*time.Millisecond {
		t.Fatalf("ticker interval = %s", gotInterval)
	}
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("monitor ticker was not stopped")
	}
	if count := strings.Count(out.String(), monitorClearScreen); count != 2 {
		t.Fatalf("clear sequence count = %d, output %q", count, out.String())
	}
	if count := strings.Count(out.String(), "No non-completed plans."); count != 2 {
		t.Fatalf("empty snapshot count = %d, output %q", count, out.String())
	}
}

func TestMonitorInteractiveColorPolicy(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	snapshot := monitor.Snapshot{CollectedAt: now, Rows: []monitor.Row{{RepositoryName: "tao", PlanID: "plan", Status: plan.StatusInProgress, Liveness: monitor.LivenessLive}}}
	for _, test := range []struct {
		name    string
		noColor string
		want    bool
	}{
		{name: "color", want: true},
		{name: "NO_COLOR", noColor: "1", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("NO_COLOR", test.noColor)
			var out bytes.Buffer
			if err := renderMonitorSnapshot(&out, snapshot, monitorColorEnabled()); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(out.String(), "\x1b[")
			if got != test.want {
				t.Fatalf("colored = %t, want %t: %q", got, test.want, out.String())
			}
		})
	}
}

func TestMonitorRejectsInvalidIntervalsAndArguments(t *testing.T) {
	for _, args := range [][]string{{"monitor", "--interval", "0s"}, {"monitor", "--interval", "-1s"}, {"monitor", "--interval", "later"}, {"monitor", "extra"}} {
		var out bytes.Buffer
		app := App{Out: &out, Err: &out, MonitorCollector: &monitorCollectorStub{}}
		if err := app.Run(context.Background(), args); err == nil {
			t.Fatalf("Run(%v) succeeded", args)
		}
	}
}

func TestMonitorRefreshErrorIsReturnedWithoutPartialRedraw(t *testing.T) {
	collector := &monitorCollectorStub{errors: []error{errors.New("catalog unavailable")}}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, MonitorCollector: collector, MonitorIsTerminal: func(io.Writer) bool { return true }}
	err := app.Run(context.Background(), []string{"monitor", "--once"})
	if err == nil || !strings.Contains(err.Error(), "refresh monitor: catalog unavailable") {
		t.Fatalf("error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("refresh error wrote output %q", out.String())
	}
}

func lineContaining(text, value string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, value) {
			return line
		}
	}
	return ""
}
