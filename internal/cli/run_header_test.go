package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runheader"
	"github.com/iamseth/tao/internal/runqueue"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/term"
)

type fakeRunHeaderTerminalWriter struct {
	mu      sync.Mutex
	output  bytes.Buffer
	size    term.Size
	resizes chan struct{}
}

func newFakeRunHeaderTerminalWriter(size term.Size) *fakeRunHeaderTerminalWriter {
	return &fakeRunHeaderTerminalWriter{size: size, resizes: make(chan struct{}, 1)}
}

func (w *fakeRunHeaderTerminalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.Write(p)
}

func (*fakeRunHeaderTerminalWriter) IsTerminal() bool { return true }

func (w *fakeRunHeaderTerminalWriter) Size() (term.Size, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size, nil
}

func (w *fakeRunHeaderTerminalWriter) ResizeEvents(context.Context) <-chan struct{} {
	return w.resizes
}

func (w *fakeRunHeaderTerminalWriter) SetSize(size term.Size) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.size = size
}

func (w *fakeRunHeaderTerminalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func TestInstallRunHeaderLeavesNonTerminalOutputByteIdentical(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	var got bytes.Buffer
	out, reporter, closeHeader := installRunHeader(context.Background(), &got, false)
	if reporter != nil || out != io.Writer(&got) {
		t.Fatal("non-terminal output activated the run header")
	}
	for _, value := range []string{"first line\n", "partial", " line\n"} {
		if _, err := io.WriteString(out, value); err != nil {
			t.Fatal(err)
		}
	}
	closeHeader()
	if want := "first line\npartial line\n"; got.String() != want {
		t.Fatalf("non-terminal output = %q, want %q", got.String(), want)
	}
}

func TestRunHeaderWriterPinsHeaderAroundLogOutput(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	terminal := newFakeRunHeaderTerminalWriter(term.Size{Width: 80, Height: 24})
	out, reporter, closeHeader := installRunHeader(context.Background(), terminal, false)
	if reporter == nil || out == io.Writer(terminal) {
		t.Fatal("fake terminal did not activate the run header")
	}
	reporter.ReportHeader(run.HeaderState{
		RepoName: "tao", PlanID: "20260812-185523-run-header", PlanTitle: "Pinned header",
		Agent: "pi", ExecutionMode: "isolated", Branch: "feature", TotalCount: 1,
		Slices:         []run.HeaderSlice{{ID: "005-cli-run-header-writer", Status: plan.StatusPending}},
		CurrentSliceID: "005-cli-run-header-writer", CurrentSliceTitle: "Wire header",
	})
	if _, err := io.WriteString(out, "ordinary log output\n"); err != nil {
		t.Fatal(err)
	}
	closeHeader()

	text := terminal.String()
	for _, want := range []string{"\x1b[8;24r", "repo tao", "Pinned header", "ordinary log output\n", "\x1b[r"} {
		if !strings.Contains(text, want) {
			t.Errorf("terminal output missing %q: %q", want, text)
		}
	}
}

func TestRunHeaderResizeReappliesMinimumSizePolicy(t *testing.T) {
	tests := []struct {
		name       string
		undersized term.Size
	}{
		{name: "rows", undersized: term.Size{Width: 80, Height: minRunHeaderRows - 1}},
		{name: "columns", undersized: term.Size{Width: minRunHeaderColumns - 1, Height: 24}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := newFakeRunHeaderTerminalWriter(term.Size{Width: 80, Height: 24})
			header := &runHeaderOutput{out: terminal, terminal: terminal, size: term.Size{Width: 80, Height: 24}}
			header.ReportHeader(run.HeaderState{RepoName: "tao", PlanTitle: "Pinned header"})
			if err := header.install(); err != nil {
				t.Fatal(err)
			}

			beforeShrink := len(terminal.String())
			terminal.SetSize(test.undersized)
			header.tryResize()
			shrinkOutput := terminal.String()[beforeShrink:]
			if !strings.Contains(shrinkOutput, "\x1b[r") {
				t.Fatalf("undersized resize did not reset scroll region: %q", shrinkOutput)
			}
			if header.pinned {
				t.Fatal("header remained pinned after undersized resize")
			}

			beforeWrite := len(terminal.String())
			if _, err := io.WriteString(header, "log while undersized\n"); err != nil {
				t.Fatal(err)
			}
			if got := terminal.String()[beforeWrite:]; got != "log while undersized\n" {
				t.Fatalf("undersized write repainted header: %q", got)
			}

			beforeGrow := len(terminal.String())
			terminal.SetSize(term.Size{Width: 80, Height: 24})
			header.tryResize()
			growOutput := terminal.String()[beforeGrow:]
			if !strings.Contains(growOutput, "\x1b[8;24r") {
				t.Fatalf("eligible resize did not reinstall scroll region: %q", growOutput)
			}
			if !strings.Contains(growOutput, "repo tao") {
				t.Fatalf("eligible resize did not repaint header: %q", growOutput)
			}
			if !header.pinned {
				t.Fatal("header was not pinned after eligible resize")
			}
		})
	}
}

func TestRunHeaderResizePositionsLogsBelowHeaderAfterRegrow(t *testing.T) {
	terminal := newFakeRunHeaderTerminalWriter(term.Size{Width: 80, Height: 24})
	header := &runHeaderOutput{out: terminal, terminal: terminal, size: term.Size{Width: 80, Height: 24}}
	header.ReportHeader(run.HeaderState{RepoName: "tao", PlanTitle: "Pinned header"})
	if err := header.install(); err != nil {
		t.Fatal(err)
	}

	terminal.SetSize(term.Size{Width: 80, Height: runheader.LineCount - 1})
	header.tryResize()
	if header.pinned {
		t.Fatal("header remained pinned after terminal shrank below the header height")
	}

	beforeGrow := len(terminal.String())
	terminal.SetSize(term.Size{Width: 80, Height: 24})
	header.tryResize()
	growOutput := terminal.String()[beforeGrow:]
	wantPosition := fmt.Sprintf("\x1b[%d;1H", runheader.LineCount+1)
	if !strings.HasSuffix(growOutput, wantPosition) {
		t.Fatalf("regrow did not position cursor below header: %q", growOutput)
	}

	beforeWrite := len(terminal.String())
	if _, err := io.WriteString(header, "log after regrow\n"); err != nil {
		t.Fatal(err)
	}
	if got := terminal.String()[beforeWrite:]; !strings.HasPrefix(got, "log after regrow\n") {
		t.Fatalf("post-regrow log was not written from the safe cursor position: %q", got)
	}
}

func TestQueueDrainPlanIDsExcludesDurableHistory(t *testing.T) {
	snapshot := runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "old-success", Status: runqueue.QueueStatusSucceeded},
		{PlanID: "plan-a", Status: runqueue.QueueStatusPending},
		{PlanID: "old-failure", Status: runqueue.QueueStatusFailed},
		{PlanID: "plan-b", Status: runqueue.QueueStatusRunning},
	}}
	if got := strings.Join(queueDrainPlanIDs(snapshot), ","); got != "plan-a,plan-b" {
		t.Fatalf("queueDrainPlanIDs() = %q, want %q", got, "plan-a,plan-b")
	}
}

func TestRunAllHeaderTracksBatchAndRestoresRegionOnce(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	plansRoot := t.TempDir()
	planA := "20260812-1900-plan-a"
	planB := "20260812-1901-plan-b"
	writeQueuePlan(t, plansRoot, planA)
	writeQueuePlan(t, plansRoot, planB)

	oldExecutor := newQueueExecutor
	newQueueExecutor = func(_ run.Repository, out io.Writer, options run.Options) runqueue.Executor {
		return func(_ context.Context, request run.Request) error {
			run.ReportHeader(options.HeaderReporter, run.HeaderState{RepoName: "tao", PlanID: request.Input, PlanTitle: request.Input, ReworkRound: 2})
			_, err := fmt.Fprintf(out, "running %s\n", request.Input)
			return err
		}
	}
	t.Cleanup(func() { newQueueExecutor = oldExecutor })

	terminal := newFakeRunHeaderTerminalWriter(term.Size{Width: 100, Height: 24})
	app := queueTestApp(plansRoot, terminal)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "run", "--all"}); err != nil {
		t.Fatal(err)
	}

	text := terminal.String()
	for _, want := range []string{planA, planB, "plan 1/2", "plan 2/2", "rework 2/5"} {
		if !strings.Contains(text, want) {
			t.Errorf("run --all header output missing %q: %q", want, text)
		}
	}
	if got := strings.Count(text, "\x1b[r"); got != 1 {
		t.Fatalf("run --all reset scroll region %d times, want once: %q", got, text)
	}
}

func TestExecuteResolvedRunHeaderShowsResolvedReworkLimit(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	terminal := newFakeRunHeaderTerminalWriter(term.Size{Width: 80, Height: 24})
	planID := "20260812-185523-run-header"
	detail := &plan.PlanDetail{
		Dir:    t.TempDir(),
		State:  plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Name: "tao", Branch: "feature"}, Plan: plan.PlanState{ID: planID, Title: "Pinned header", PendingSlices: []string{"005-header"}}},
		Slices: plan.SlicesFile{PlanID: planID, Slices: []plan.Slice{{ID: "005-header", Title: "Header", Status: plan.StatusPending}}},
		Events: []plan.Event{{Type: plan.EventTypeReworkRound, Round: 2}},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(run.Service, context.Context, run.Request) error { return nil }
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	request := run.Request{Input: planID, ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Agent: run.AgentPi, ExecutionMode: run.ExecutionModeIsolated, CommitPolicy: run.CommitPolicySlice, ReviewEnabled: true}}
	policy := runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 7}
	if err := (App{Out: terminal}).executeResolvedRun(context.Background(), repo, planID, request, false, policy, false, false); err != nil {
		t.Fatal(err)
	}
	if text := terminal.String(); !strings.Contains(text, "rework 2/7") {
		t.Fatalf("single-plan header omitted resolved rework limit: %q", text)
	}
}

func TestExecuteResolvedRunRestoresRegionWhenRunErrors(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	terminal := newFakeRunHeaderTerminalWriter(term.Size{Width: 80, Height: 24})
	planID := "20260812-185523-run-header"
	detail := &plan.PlanDetail{
		Dir:    t.TempDir(),
		State:  plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Name: "tao", Branch: "feature"}, Plan: plan.PlanState{ID: planID, Title: "Pinned header", PendingSlices: []string{"005-header"}}},
		Slices: plan.SlicesFile{PlanID: planID, Slices: []plan.Slice{{ID: "005-header", Title: "Header", Status: plan.StatusPending}}},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}
	failure := errors.New("wrapped run failed")
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(run.Service, context.Context, run.Request) error { return failure }
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	request := run.Request{Input: planID, ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Agent: run.AgentPi, ExecutionMode: run.ExecutionModeIsolated, CommitPolicy: run.CommitPolicySlice, ReviewEnabled: true}}
	err := (App{Out: terminal}).executeResolvedRun(context.Background(), repo, planID, request, false, runtimeconfig.AutoReworkPolicy{}, false, false)
	if !errors.Is(err, failure) {
		t.Fatalf("executeResolvedRun() error = %v, want %v", err, failure)
	}
	text := terminal.String()
	if !strings.Contains(text, "\x1b[r") {
		t.Fatalf("error teardown omitted scroll-region reset: %q", text)
	}
	reset := strings.LastIndex(text, "\x1b[r")
	if !strings.Contains(text[reset:], "repo tao") {
		t.Fatalf("teardown did not leave a static header after reset: %q", text[reset:])
	}
}
