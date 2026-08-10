package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/term"
)

func TestRenderSlicesPaneUsesStateQueueOrderAndShowsCurrentMetadata(t *testing.T) {
	current := "002-build"
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{
			CurrentSlice:    &current,
			CompletedSlices: []string{"001-setup"},
			PendingSlices:   []string{"002-build", "003-release"},
		}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "003-release", Title: "Release", Status: plan.StatusBlocked, BlockerNote: "waiting on credentials", Approval: &plan.Approval{Required: true}},
			{ID: "002-build", Title: "Build it", Status: plan.StatusInProgress},
			{ID: "001-setup", Title: "Set up", Status: plan.StatusCompleted},
		}},
	}

	lines := RenderSlicesPane(detail, 120, 10, true)
	text := strings.Join(lines, "\n")
	setup := strings.Index(text, "001-setup")
	build := strings.Index(text, "002-build")
	release := strings.Index(text, "003-release")
	if setup < 0 || build < setup || release < build {
		t.Fatalf("slice order does not follow completed/pending queues:\n%s", text)
	}
	if !strings.Contains(text, "> ") || !strings.Contains(text, "\x1b[36m") {
		t.Fatalf("current colored slice is not highlighted:\n%q", text)
	}
	if !strings.Contains(text, "[approval required]") || !strings.Contains(text, "blocker: waiting on credentials") {
		t.Fatalf("approval or blocker metadata missing:\n%s", text)
	}
}

func TestRenderSlicesPaneKeepsCurrentVisibleAtNarrowSizes(t *testing.T) {
	current := "003-current"
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{CurrentSlice: &current, PendingSlices: []string{"001-a", "002-b", current}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "001-a", Title: "A very long title", Status: plan.StatusPending},
			{ID: "002-b", Title: "Another long title", Status: plan.StatusPending},
			{ID: current, Title: "Current long title", Status: plan.StatusInProgress},
		}},
	}

	lines := RenderSlicesPane(detail, 18, 1, false)
	if len(lines) != 1 || !strings.Contains(lines[0], "> ") {
		t.Fatalf("narrow current slice lines = %q, want highlighted current line", lines)
	}
	if utf8.RuneCountInString(lines[0]) > 18 {
		t.Fatalf("narrow slice line = %q, exceeds width", lines[0])
	}
}

func TestRenderLogPaneRendersFramesPassesPlainLinesAndPinsTail(t *testing.T) {
	var framed bytes.Buffer
	if err := logrecord.Write(&framed, logrecord.Record{Type: logrecord.TypeAssistant, Content: "implemented"}); err != nil {
		t.Fatal(err)
	}
	if err := logrecord.Write(&framed, logrecord.Record{Type: logrecord.TypeToolResult, Name: "test", Content: "passed"}); err != nil {
		t.Fatal(err)
	}
	text := "legacy output\n" + framed.String() + "latest output\n"

	all := strings.Join(RenderLogPane(text, 80, 10), "\n")
	for _, want := range []string{"legacy output", "assistant: implemented", "✓ test", "passed", "latest output"} {
		if !strings.Contains(all, want) {
			t.Fatalf("rendered log missing %q:\n%s", want, all)
		}
	}

	pinned := RenderLogPane("one\ntwo\nthree\nfour\n", 80, 2)
	if got := strings.Join(pinned, "\n"); got != "three\nfour" {
		t.Fatalf("pinned log = %q, want newest two lines", got)
	}
	narrow := RenderLogPane("界界界界\n", 3, 1)
	if len(narrow) != 1 || utf8.RuneCountInString(narrow[0]) != 3 {
		t.Fatalf("narrow log = %q, want three runes", narrow)
	}
}

func TestRenderDetailIncludesHeaderAndFitsTerminal(t *testing.T) {
	current := "001-work"
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusInProgress,
			Repo:   plan.Repo{Name: "alpha"},
			Plan:   plan.PlanState{ID: "plan-a", Title: "Plan A", CurrentSlice: &current, PendingSlices: []string{current}},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: current, Title: "Work", Status: plan.StatusInProgress}}},
	}
	frame := RenderDetail(DetailModel{
		Plan: detail,
		Row: monitor.Row{
			Liveness:     monitor.LivenessLive,
			Phase:        runstatus.Phase("implement"),
			HeartbeatAge: 7 * time.Second,
		},
		Log:   "working\n",
		Width: 100, Height: 10,
	})
	for _, want := range []string{"plan-a", "Plan A", "alpha", "in_progress", "implement", "7s ago", "Esc back"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("detail frame missing %q:\n%s", want, frame)
		}
	}
	body := strings.TrimSuffix(strings.TrimPrefix(frame, clearScreenSequence), "\n")
	lines := strings.Split(body, "\n")
	if len(lines) > 10 {
		t.Fatalf("detail frame has %d lines, want at most 10", len(lines))
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 100 {
			t.Fatalf("detail line %q exceeds width", line)
		}
	}
}

func TestRenderDetailFullHeightOmitsTrailingNewline(t *testing.T) {
	frame := RenderDetail(DetailModel{
		Log:    "one\ntwo\nthree\nfour\n",
		Width:  80,
		Height: 10,
	})
	if lines := renderedLines(frame); len(lines) != 10 {
		t.Fatalf("full-height detail frame has %d lines, want 10:\n%s", len(lines), frame)
	}
	if strings.HasSuffix(frame, "\n") {
		t.Fatal("full-height detail frame ends with a newline that can scroll the terminal")
	}
}

func TestRenderDetailVerticalResizeKeepsFrameInsideTerminal(t *testing.T) {
	model := DetailModel{
		Row:    monitor.Row{PlanID: "plan-a", PlanTitle: "Plan A"},
		Log:    strings.Repeat("output\n", 10),
		Width:  80,
		Height: 20,
	}
	if frame := RenderDetail(model); !strings.HasSuffix(frame, "\n") {
		t.Fatal("under-height detail frame should retain its trailing newline")
	}

	model.Height = 6
	frame := RenderDetail(model)
	lines := renderedLines(frame)
	if len(lines) != 6 {
		t.Fatalf("resized detail frame has %d lines, want 6:\n%s", len(lines), frame)
	}
	if lines[0] != "Tao UI | PLAN DETAIL" || lines[len(lines)-1] != "Esc back" {
		t.Fatalf("resized detail frame lost header or footer: %q", lines)
	}
	if strings.HasSuffix(frame, "\n") {
		t.Fatal("full-height resized detail frame ends with a newline that can scroll the terminal")
	}
}

func TestFollowDetailLogSkipsSeedReplayAndEmitsAppends(t *testing.T) {
	repository := &fakeDetailRepository{
		follow: func(_ context.Context, _ string, out io.Writer) error {
			if _, err := io.WriteString(out, "older\nse"); err != nil {
				return err
			}
			if _, err := io.WriteString(out, "ed\nnew line\n"); err != nil {
				return err
			}
			return nil
		},
	}
	updates := make(chan detailFollowUpdate, 4)
	followDetailLog(context.Background(), repository, "/plans/one", "seed\n", updates)

	var got strings.Builder
	for update := range updates {
		if update.err != nil {
			t.Fatalf("follow update error: %v", update.err)
		}
		got.WriteString(update.text)
	}
	if got.String() != "new line\n" {
		t.Fatalf("follow updates = %q, want only live append", got.String())
	}
}

func TestDetailNavigationEnterAndEscape(t *testing.T) {
	current := "001-work"
	started := make(chan struct{})
	stopped := make(chan struct{})
	var startedOnce sync.Once
	repository := &fakeDetailRepository{
		detail: &plan.PlanDetail{
			State:  plan.State{Plan: plan.PlanState{ID: "plan-a", CurrentSlice: &current, PendingSlices: []string{current}}},
			Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: current, Status: plan.StatusInProgress}}},
		},
		tail: "seed\n",
		follow: func(ctx context.Context, _ string, out io.Writer) error {
			startedOnce.Do(func() { close(started) })
			if _, err := io.WriteString(out, "seed\n"); err != nil {
				return err
			}
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	}
	state := loopState{
		snapshot:      monitor.Snapshot{Rows: []monitor.Row{{RepositoryID: "repo-a", PlanID: "plan-a", PlanDir: "/plans/one"}}},
		showCompleted: true,
	}
	app := App{Details: repository}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEnter}); quit || state.detail == nil {
		t.Fatalf("Enter navigation quit=%t detail=%#v", quit, state.detail)
	}
	if state.detail.plan == nil || state.detail.log != "seed\n" || repository.tailLines != detailLogTailLines {
		t.Fatalf("opened detail = %#v tail lines=%d", state.detail, repository.tailLines)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("detail follower did not start")
	}

	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEsc}); quit || state.detail != nil {
		t.Fatalf("detail Esc quit=%t detail=%#v, want table", quit, state.detail)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("detail follower was not canceled")
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEsc}); !quit {
		t.Fatal("table Esc did not quit")
	}
}

type fakeDetailRepository struct {
	detail    *plan.PlanDetail
	tail      string
	tailLines int
	follow    func(context.Context, string, io.Writer) error
}

func (r *fakeDetailRepository) ResolvePlan(context.Context, string) (*plan.PlanDetail, error) {
	if r.detail == nil {
		return nil, errors.New("plan unavailable")
	}
	return r.detail, nil
}

func (r *fakeDetailRepository) ReadLogTail(_ string, lines int) (string, error) {
	r.tailLines = lines
	return r.tail, nil
}

func (r *fakeDetailRepository) FollowLog(ctx context.Context, dir string, out io.Writer) error {
	if r.follow == nil {
		return nil
	}
	return r.follow(ctx, dir, out)
}
