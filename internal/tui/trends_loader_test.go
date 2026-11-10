package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/term"
)

type blockingTrendsCollector struct {
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (c *blockingTrendsCollector) Collect(ctx context.Context) (TrendsSnapshot, error) {
	c.started <- struct{}{}
	select {
	case <-ctx.Done():
		close(c.canceled)
		return TrendsSnapshot{}, ctx.Err()
	case <-c.release:
		return TrendsSnapshot{RepositoryName: "fixture"}, nil
	}
}
func awaitTrends(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("collector did not respond")
	}
}
func TestTrendsLoaderLazyNonoverlappingAndCancellation(t *testing.T) {
	c := &blockingTrendsCollector{started: make(chan struct{}, 10), release: make(chan struct{}), canceled: make(chan struct{})}
	a := App{Trends: c}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := loopState{}
	a.refreshTrends(ctx, &s)
	if s.trends.model.Loading {
		t.Fatal("eager scan")
	}
	s.page = PageTrends
	a.refreshTrends(ctx, &s)
	awaitTrends(t, c.started)
	for range 5 {
		a.refreshTrends(ctx, &s)
	}
	select {
	case <-c.started:
		t.Fatal("overlapping scan")
	default:
	}
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyTab})
	if s.activePage() != PageHistory {
		t.Fatal("navigation blocked")
	}
	a.refreshTrends(ctx, &s)
	cancel()
	awaitTrends(t, c.canceled)
}
func TestTrendsLoaderRefreshAfterSuccess(t *testing.T) {
	c := &blockingTrendsCollector{started: make(chan struct{}, 10), release: make(chan struct{}), canceled: make(chan struct{})}
	close(c.release)
	a := App{Trends: c}
	s := loopState{page: PageTrends}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 2 {
		a.refreshTrends(ctx, &s)
		select {
		case result := <-s.trends.updates:
			s.acceptTrends(result)
		case <-time.After(time.Second):
			t.Fatal("missing refresh")
		}
		if s.trends.model.Loading || !s.trends.good || s.trends.model.Snapshot.RepositoryName != "fixture" {
			t.Fatal("refresh did not settle")
		}
	}
	if s.trends.generation != 2 {
		t.Fatal("refresh not restarted")
	}
}

func TestTrendsRunResponsiveWhileLoading(t *testing.T) {
	c := &blockingTrendsCollector{started: make(chan struct{}, 10), release: make(chan struct{}), canceled: make(chan struct{})}
	input, keys := io.Pipe()
	defer func() { _ = input.Close() }()
	defer func() { _ = keys.Close() }()
	var output bytes.Buffer
	a := App{Input: input, Output: &output, Terminal: &fakeTerminal{size: term.Size{Width: 40, Height: 12}}, Ticker: &fakeTicker{channel: make(chan time.Time)}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}}, Trends: c}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	if _, err := keys.Write([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	awaitTrends(t, c.started)
	if _, err := keys.Write([]byte("\tcq")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("input blocked on collection")
	}
	awaitTrends(t, c.canceled)
	if !strings.Contains(output.String(), "Loading trends") {
		t.Fatal("loading state absent")
	}
}

func TestTrendsLoaderResultsAndNil(t *testing.T) {
	s := loopState{page: PageTrends, size: term.Size{Width: 40, Height: 10}}
	(App{}).refreshTrends(context.Background(), &s)
	if !strings.Contains(RenderTrends(*s.trendsModel()), "unavailable") {
		t.Fatal("nil collector not readable")
	}
	s.trends.generation = 2
	s.trends.model.Loading = true
	s.acceptTrends(trendsResult{generation: 1, snapshot: TrendsSnapshot{RepositoryName: "obsolete"}})
	if !s.trends.model.Loading || s.trends.model.Snapshot.RepositoryName != "" {
		t.Fatal("stale result accepted")
	}
	s.acceptTrends(trendsResult{generation: 2, err: errors.New("first failure")})
	if s.trends.model.Snapshot.Unavailable == "" || s.trends.model.Stale {
		t.Fatal("first failure")
	}
	s.acceptTrends(trendsResult{generation: 2, snapshot: TrendsSnapshot{RepositoryName: "last good"}})
	s.acceptTrends(trendsResult{generation: 2, err: errors.New("later failure")})
	if !s.trends.model.Stale || s.trends.model.Snapshot.RepositoryName != "last good" || !strings.Contains(RenderTrends(*s.trendsModel()), "later failure") {
		t.Fatal("lost last good evidence")
	}
	s.acceptTrends(trendsResult{generation: 2, snapshot: TrendsSnapshot{RepositoryName: "recovered"}})
	if s.trends.model.Stale || s.trends.model.Failure != "" {
		t.Fatal("failed to recover")
	}
}
func TestTrendsCohortSelectionScrollAndInertActions(t *testing.T) {
	s := loopState{page: PagePlans, selected: 1, snapshot: monitor.Snapshot{Rows: []monitor.Row{testActionRow(), testActionRow()}}, size: term.Size{Width: 30, Height: 4}}
	a := App{Actions: &Actions{}}
	ctx := context.Background()
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyTab})
	for _, want := range []string{"pi", "claude", "overall", "pi"} {
		a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'c'})
		if s.trends.model.Cohort != want {
			t.Fatal(s.trends.model.Cohort)
		}
	}
	s.trends.model.Loading = true
	for _, r := range "raAmMxfs/" {
		a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: r})
	}
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyEnter})
	if s.detail != nil || s.confirm != nil || s.filterMenu != nil || s.sortMenu != nil || s.searchActive {
		t.Fatal("Trends enabled plan interaction")
	}
	if handled, err := a.fixSelectedPlan(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'x'}); handled || err != nil {
		t.Fatal("fix enabled")
	}
	for range 100 {
		a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyArrowDown})
	}
	if s.trends.model.ScrollOffset != s.trendsMaxOffset() {
		t.Fatal("unbounded scroll")
	}
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: 'g'})
	if s.trends.model.ScrollOffset != 0 {
		t.Fatal("top")
	}
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyRune, Rune: '?'})
	if !s.showShortcuts {
		t.Fatal("help")
	}
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyEsc})
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyShiftTab})
	if s.activePage() != PagePlans || s.selected != 1 {
		t.Fatal("plan selection lost")
	}
	a.handleKey(ctx, &s, term.KeyEvent{Key: term.KeyTab})
	if s.trends.model.Cohort != "pi" {
		t.Fatal("cohort lost")
	}
}
