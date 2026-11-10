package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/insights"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/tui"
)

func TestUITrendsProjectionParity(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	rate := insights.EstimateRate(0, 20, 5)
	median := insights.EstimateMedian([]float64{0, 2, 4}, 5)
	report := insights.TrendReport{AsOf: now, MaturityDays: 14, MinimumSamples: 5, Coverage: insights.TrendCoverage{Plans: 25, MissingEvidence: 3}, Buckets: []insights.TrendBucket{{Month: "2026-11", Cohort: "pi", Provisional: true, AgeImmature: true, Metrics: []insights.TrendMetric{{Key: "rate", Direction: "higher", Availability: "incomplete", Reason: "missing", EligiblePlans: 25, EvidencePlans: 20, Rate: &rate, Comparison: "inconclusive"}, {Key: "median_cost_per_plan", Direction: "lower", Availability: "incomplete", PartialSamples: 2, Median: &median, Comparison: "unavailable"}}}}}
	got := uiTrendsSnapshot(report, "repo")
	if got.AsOf != now || got.MaturityDays != 14 || got.MinimumSamples != 5 || got.Coverage != tui.TrendCoverage(report.Coverage) {
		t.Fatal(got)
	}
	for i, m := range report.Buckets[0].Metrics {
		g := got.Buckets[0].Metrics[i]
		if g.Key != m.Key || g.Direction != m.Direction || g.Availability != m.Availability || g.Reason != m.Reason || g.Comparison != m.Comparison || g.PartialSamples != m.PartialSamples || g.EligiblePlans != m.EligiblePlans || g.EvidencePlans != m.EvidencePlans {
			t.Fatal(g)
		}
	}
	if !reflect.DeepEqual(*got.Buckets[0].Metrics[0].Rate, tui.TrendRate(rate)) || !reflect.DeepEqual(*got.Buckets[0].Metrics[1].Median, tui.TrendMedian(median)) {
		t.Fatal(got)
	}
	got.Buckets[0].Metrics[0].Rate.Rate = 99
	if rate.Rate != 0 {
		t.Fatal("aliased estimate")
	}
}

type trendsWiringRegistry struct {
	monitorRegistryStub
	dir string
}

func (r trendsWiringRegistry) Current(context.Context) (taodata.Repo, error) {
	return taodata.Repo{ID: "fixture", Name: "isolated-trends"}, nil
}
func (r trendsWiringRegistry) PlansDir(taodata.Repo) string { return r.dir }

type trendsWiringOutput struct {
	bytes.Buffer
	ready chan struct{}
}

func (w *trendsWiringOutput) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Contains(string(p), "isolated-trends") {
		select {
		case w.ready <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestUITrendsProductionWiring(t *testing.T) {
	registry := trendsWiringRegistry{dir: t.TempDir()}
	input, keys := io.Pipe()
	defer func() { _ = input.Close() }()
	defer func() { _ = keys.Close() }()
	output := &trendsWiringOutput{ready: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = keys.Write([]byte("\t"))
		select {
		case <-output.ready:
		case <-ctx.Done():
		}
		_, _ = keys.Write([]byte("q"))
	}()
	ticker := &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
	app := newTestApp(App{
		In: input, Out: output, Err: output,
		MonitorCollector:  collectingMonitorFunc(func(context.Context) error { return nil }),
		MonitorIsTerminal: func(io.Writer) bool { return true },
		MonitorTicker:     func(time.Duration) MonitorTicker { return ticker },
		UITerminal:        &uiTerminalStub{size: term.Size{Width: 120, Height: 30}, resizes: make(chan struct{})},
		Registry:          func() NoteRegistry { return registry },
		Repository:        func(dir string) Repository { return plan.NewFileRepository(dir) },
	})
	if err := app.ui(ctx, nil); err != nil {
		t.Fatal(err)
	}
	_ = input.Close()
	<-done
	if !strings.Contains(output.String(), "isolated-trends") {
		t.Fatalf("production collector not rendered: %s", output.String())
	}
}

type trendsRegistry struct {
	*fakeNoteRegistry
	err error
}

func (r trendsRegistry) Current(context.Context) (taodata.Repo, error) { return r.current, r.err }

func TestUITrendsCollectorLazyClockAndErrors(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	calls := 0
	registry := trendsRegistry{fakeNoteRegistry: &fakeNoteRegistry{current: taodata.Repo{Name: "repo", ID: "id"}}}
	app := App{Now: func() time.Time { return now }, Registry: func() NoteRegistry { calls++; return registry }, Repository: func(string) Repository { return fakeRepository{} }}
	collector := newUITrendsCollector(app)
	if calls != 0 {
		t.Fatal("eager resolution")
	}
	got, err := collector.Collect(context.Background())
	if err != nil || got.AsOf != now || got.RepositoryName != "repo" {
		t.Fatalf("%+v %v", got, err)
	}
	report, err := insights.AggregateWithOptions(context.Background(), fakeRepository{}, insights.Options{IncludeTrend: true, Now: now})
	if err != nil || !reflect.DeepEqual(got, uiTrendsSnapshot(*report.Trend, "repo")) {
		t.Fatal("aggregate parity", err)
	}
	registry.err = errors.New("registration missing")
	got, err = collector.Collect(context.Background())
	if err != nil || got.Unavailable == "" {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = collector.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	registry.err = context.DeadlineExceeded
	if _, err = collector.Collect(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
