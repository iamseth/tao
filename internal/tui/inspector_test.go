package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
)

type recordingInspector struct {
	mu       sync.Mutex
	calls    int
	started  chan struct{}
	canceled chan struct{}
	block    bool
}

func (i *recordingInspector) Inspect(ctx context.Context, _ *plan.PlanDetail) (DetailInspection, error) {
	i.mu.Lock()
	i.calls++
	i.mu.Unlock()
	if i.started != nil {
		select {
		case i.started <- struct{}{}:
		default:
		}
	}
	if i.block {
		<-ctx.Done()
		if i.canceled != nil {
			select {
			case i.canceled <- struct{}{}:
			default:
			}
		}
		return DetailInspection{}, ctx.Err()
	}
	return DetailInspection{}, nil
}

func (i *recordingInspector) callCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

func TestDashboardCollectionDoesNotInvokeDetailInspector(t *testing.T) {
	inspector := &recordingInspector{}
	ticker := &fakeTicker{channel: make(chan time.Time)}
	app := App{
		Input:     strings.NewReader("q"),
		Output:    &bytes.Buffer{},
		Terminal:  &fakeTerminal{size: term.Size{Width: 80, Height: 20}, resizes: make(chan struct{})},
		Ticker:    ticker,
		Collector: &fakeCollector{snapshots: []monitor.Snapshot{{Rows: []monitor.Row{{PlanID: "plan-a", PlanDir: "/plans/a"}}}}},
		Inspector: inspector,
	}
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inspector.callCount() != 0 {
		t.Fatalf("ordinary dashboard invoked inspector %d time(s)", inspector.callCount())
	}
}

func TestDetailInspectionStartsAfterLoadCancelsOnCloseAndSkipsUnchangedReload(t *testing.T) {
	inspector := &recordingInspector{started: make(chan struct{}, 2), canceled: make(chan struct{}, 1), block: true}
	repository := &fakeDetailRepository{detail: &plan.PlanDetail{State: plan.State{
		Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-work"}},
		Repo: plan.Repo{Root: "/repo", BaseCommit: "base"},
	}}}
	app := App{Details: repository, Inspector: inspector}
	state := loopState{size: term.Size{Width: 80, Height: 20}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app.openDetail(ctx, &state, monitor.Row{PlanID: "plan-a", PlanDir: "/plans/a"})
	select {
	case <-inspector.started:
	case <-time.After(time.Second):
		t.Fatal("detail inspection did not start")
	}
	if state.detail == nil || state.detail.inspection.status != detailInspectionLoading {
		t.Fatalf("inspection state = %#v, want loading", state.detail)
	}
	app.reloadDetail(ctx, &state)
	if inspector.callCount() != 1 {
		t.Fatalf("unchanged detail inspection calls = %d, want 1", inspector.callCount())
	}

	state.closeDetail()
	select {
	case <-inspector.canceled:
	case <-time.After(time.Second):
		t.Fatal("closing detail did not cancel inspection")
	}
}

func TestBoundedDetailInspectionSanitizesAndLimitsFindings(t *testing.T) {
	input := DetailInspection{}
	for index := 0; index < detailInspectionMaxFindings+4; index++ {
		input.Findings = append(input.Findings, DetailFinding{Severity: "warn\x1b[31ming", Message: "message\nwith\tcontrols"})
	}
	got := boundedDetailInspection(input)
	if len(got.Findings) != detailInspectionMaxFindings {
		t.Fatalf("findings = %d, want %d", len(got.Findings), detailInspectionMaxFindings)
	}
	for _, finding := range got.Findings {
		if finding.Severity != "warn ing" || finding.Message != "message with controls" {
			t.Fatalf("finding was not sanitized: %#v", finding)
		}
	}
}
