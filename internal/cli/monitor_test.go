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
				Status: plan.StatusInProgress, Liveness: monitor.LivenessLive, Phase: runstatus.Phase("implement"), InvocationDuration: 5*time.Minute + 4*time.Second,
				Left: 1, OriginalCompletedCount: 3, OriginalTotalCount: 4, ReworkCompletedCount: 1, ReworkTotalCount: 2, UpdatedAt: &updated,
			},
			{
				Kind: monitor.RowKindPlan, RepositoryName: "api", PlanID: "stale-plan", Status: plan.StatusBlocked, Liveness: monitor.LivenessStale,
				Phase: runstatus.Phase("verify"), InvocationDuration: 8 * time.Minute, HeartbeatAge: 25 * time.Second, Left: 2, Warnings: []string{"heartbeat record is old"},
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
	for _, want := range []string{"LIVE", "STATUS", "REPO", "PLAN ID/name", "PHASE", "RUN FOR", "LEFT", "ORIGINAL", "REWORK", "UPDATED", "LIVE", "in_progress", "tao", "20260729-044616-plan-monitor", "implement", "5m04s", "3/4", "1/2", "3m", "STALE", "verify (25s old)", "warning: api/stale-plan: heartbeat record is old"} {
		if !strings.Contains(text, want) {
			t.Fatalf("monitor output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("--once output contained ANSI: %q", text)
	}
	quietLine := lineContaining(text, "quiet")
	if !strings.Contains(quietLine, "-     ") {
		t.Fatalf("missing live state was not neutral: %q", quietLine)
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
