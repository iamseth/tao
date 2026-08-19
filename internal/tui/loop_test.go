package tui

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/term"
)

type fakeTerminal struct {
	mu       sync.Mutex
	size     term.Size
	resizes  chan struct{}
	entered  bool
	restored bool
}

func (t *fakeTerminal) EnterRaw() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entered = true
	return nil
}

func (t *fakeTerminal) Restore() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.restored = true
	return nil
}

func (t *fakeTerminal) Size() (term.Size, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size, nil
}

func (t *fakeTerminal) ResizeEvents(context.Context) <-chan struct{} { return t.resizes }

func (t *fakeTerminal) setSize(size term.Size) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.size = size
}

func (t *fakeTerminal) state() (bool, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.entered, t.restored
}

type fakeTicker struct {
	channel chan time.Time
	mu      sync.Mutex
	stopped bool
}

func (t *fakeTicker) C() <-chan time.Time { return t.channel }
func (t *fakeTicker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
}

func (t *fakeTicker) wasStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

type fakeCollector struct {
	mu        sync.Mutex
	snapshots []monitor.Snapshot
	calls     int
	panic     bool
}

func (c *fakeCollector) Collect(context.Context) (monitor.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.panic {
		panic("collector panic")
	}
	index := c.calls
	if index >= len(c.snapshots) {
		index = len(c.snapshots) - 1
	}
	c.calls++
	return c.snapshots[index], nil
}

type recordingWriter struct {
	writes chan string
}

func (w *recordingWriter) Write(value []byte) (int, error) {
	copyValue := string(append([]byte(nil), value...))
	w.writes <- copyValue
	return len(value), nil
}

func TestRunMovesSelectionAndRestoresTerminal(t *testing.T) {
	terminal := &fakeTerminal{size: term.Size{Width: 120, Height: 30}, resizes: make(chan struct{})}
	ticker := &fakeTicker{channel: make(chan time.Time)}
	collector := &fakeCollector{snapshots: []monitor.Snapshot{{Rows: []monitor.Row{
		{RepositoryName: "alpha", PlanID: "one", Status: "planned"},
		{RepositoryName: "beta", PlanID: "two", Status: "planned"},
	}}}}
	output := &recordingWriter{writes: make(chan string, 16)}

	err := (App{
		Input:     strings.NewReader("jq"),
		Output:    output,
		Terminal:  terminal,
		Ticker:    ticker,
		Collector: collector,
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var frames []string
	for len(output.writes) > 0 {
		value := <-output.writes
		if strings.HasPrefix(value, clearScreenSequence) {
			frames = append(frames, value)
		}
	}
	if len(frames) != 2 {
		t.Fatalf("frame writes = %d, want 2; frames=%q", len(frames), frames)
	}
	if !strings.Contains(frames[0], "> alpha") || !strings.Contains(frames[1], "> beta") {
		t.Fatalf("selection did not move in complete frame writes: %q", frames)
	}
	entered, restored := terminal.state()
	if !entered || !restored {
		t.Fatalf("terminal state entered=%t restored=%t, want both true", entered, restored)
	}
	if !ticker.wasStopped() {
		t.Fatal("refresh ticker was not stopped")
	}
}

func TestRunRefreshesAndHandlesResize(t *testing.T) {
	terminal := &fakeTerminal{size: term.Size{Width: 80, Height: 24}, resizes: make(chan struct{}, 1)}
	ticker := &fakeTicker{channel: make(chan time.Time, 1)}
	collector := &fakeCollector{snapshots: []monitor.Snapshot{
		{Rows: []monitor.Row{{RepositoryName: "alpha", PlanID: "one", Status: "planned"}}},
		{Rows: []monitor.Row{{RepositoryName: "beta", PlanID: "two", Status: "in_progress"}}},
	}}
	output := &recordingWriter{writes: make(chan string, 32)}
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()

	done := make(chan error, 1)
	go func() {
		done <- (App{Input: reader, Output: output, Terminal: terminal, Ticker: ticker, Collector: collector}).Run(context.Background())
	}()

	initial := waitForFrame(t, output.writes)
	if !strings.Contains(initial, "alpha") {
		t.Fatalf("initial frame = %q, want alpha row", initial)
	}
	ticker.channel <- time.Now()
	refreshed := waitForFrame(t, output.writes)
	if !strings.Contains(refreshed, "beta") {
		t.Fatalf("refreshed frame = %q, want beta row", refreshed)
	}

	terminal.setSize(term.Size{Width: 10, Height: 6})
	terminal.resizes <- struct{}{}
	resized := waitForFrame(t, output.writes)
	body := strings.TrimPrefix(resized, clearScreenSequence)
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) > 6 {
		t.Fatalf("resized frame has %d lines, want at most 6: %q", len(lines), resized)
	}
	for _, line := range lines {
		if len([]rune(line)) > 10 {
			t.Fatalf("resized frame line %q exceeds new width", line)
		}
	}
	if !strings.Contains(resized, "> beta") {
		t.Fatalf("resized frame lost selected row: %q", resized)
	}

	if _, err := writer.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event loop did not quit")
	}
}

func TestRefreshPreservesSelectedPlanBeforeAction(t *testing.T) {
	tests := []struct {
		name       string
		initial    monitor.Snapshot
		selected   int
		refreshed  monitor.Snapshot
		wantIndex  int
		wantRepoID string
		wantCWD    string
		wantPlanID string
	}{
		{
			name: "collector reordering",
			initial: monitor.Snapshot{Rows: []monitor.Row{
				{RepositoryID: "repo-a", RepositoryRoot: "/repos/alpha", PlanID: "shared", Status: "planned"},
				{RepositoryID: "repo-b", RepositoryRoot: "/repos/beta", PlanID: "shared", Status: "planned"},
			}},
			refreshed: monitor.Snapshot{Rows: []monitor.Row{
				{RepositoryID: "repo-b", RepositoryRoot: "/repos/beta", PlanID: "shared", Status: "planned"},
				{RepositoryID: "repo-a", RepositoryRoot: "/repos/alpha", PlanID: "shared", Status: "planned"},
			}},
			wantIndex:  1,
			wantRepoID: "repo-a",
			wantCWD:    "/repos/alpha",
			wantPlanID: "shared",
		},
		{
			name: "section membership change",
			initial: monitor.Snapshot{Rows: []monitor.Row{
				{RepositoryID: "repo-target", RepositoryRoot: "/repos/target", PlanID: "target", Status: "planned"},
				{RepositoryID: "repo-other", RepositoryRoot: "/repos/other", PlanID: "other", Status: "planned", AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
			}},
			selected: 1,
			refreshed: monitor.Snapshot{Rows: []monitor.Row{
				{RepositoryID: "repo-target", RepositoryRoot: "/repos/target", PlanID: "target", Status: "planned", AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
				{RepositoryID: "repo-other", RepositoryRoot: "/repos/other", PlanID: "other", Status: "planned"},
			}},
			wantIndex:  0,
			wantRepoID: "repo-target",
			wantCWD:    "/repos/target",
			wantPlanID: "target",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests []CommandRequest
			actions, err := NewActions(ActionOptions{
				Executable: "tao",
				Launcher: func(_ context.Context, request CommandRequest) error {
					requests = append(requests, request)
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			state := loopState{snapshot: test.initial, selected: test.selected, showCompleted: true}

			state.replaceSnapshot(test.refreshed)
			row, ok := state.selectedRow()
			if !ok || state.selected != test.wantIndex || row.RepositoryID != test.wantRepoID || row.PlanID != test.wantPlanID {
				t.Fatalf("selection after refresh index=%d row=%+v ok=%t, want index=%d repo=%q plan=%q", state.selected, row, ok, test.wantIndex, test.wantRepoID, test.wantPlanID)
			}
			if quit := (App{Actions: actions}).handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'r'}); quit {
				t.Fatal("run action unexpectedly quit")
			}
			if len(requests) != 1 {
				t.Fatalf("action requests = %+v, want one", requests)
			}
			request := requests[0]
			if request.CWD != test.wantCWD || len(request.Args) != 2 || request.Args[0] != "run" || request.Args[1] != test.wantPlanID {
				t.Fatalf("action request = %+v, want cwd=%q args=[run %s]", request, test.wantCWD, test.wantPlanID)
			}
		})
	}
}

func TestRunRestoresTerminalOnContextCancellation(t *testing.T) {
	terminal := &fakeTerminal{size: term.Size{Width: 80, Height: 24}, resizes: make(chan struct{})}
	ticker := &fakeTicker{channel: make(chan time.Time)}
	collector := &fakeCollector{snapshots: []monitor.Snapshot{{}}}
	output := &recordingWriter{writes: make(chan string, 16)}
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (App{Input: reader, Output: output, Terminal: terminal, Ticker: ticker, Collector: collector}).Run(ctx)
	}()
	_ = waitForFrame(t, output.writes)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event loop did not exit after cancellation")
	}
	_ = writer.Close()
	entered, restored := terminal.state()
	if !entered || !restored {
		t.Fatalf("terminal state entered=%t restored=%t after cancellation", entered, restored)
	}
}

func TestRunRestoresTerminalBeforeResumingPanic(t *testing.T) {
	terminal := &fakeTerminal{size: term.Size{Width: 80, Height: 24}, resizes: make(chan struct{})}
	ticker := &fakeTicker{channel: make(chan time.Time)}
	output := &recordingWriter{writes: make(chan string, 16)}

	defer func() {
		if recovered := recover(); recovered != "collector panic" {
			t.Fatalf("recovered %v, want collector panic", recovered)
		}
		entered, restored := terminal.state()
		if !entered || !restored {
			t.Fatalf("terminal state entered=%t restored=%t after panic", entered, restored)
		}
		if !ticker.wasStopped() {
			t.Fatal("refresh ticker was not stopped after panic")
		}
	}()

	_ = (App{
		Input:     strings.NewReader(""),
		Output:    output,
		Terminal:  terminal,
		Ticker:    ticker,
		Collector: &fakeCollector{panic: true},
	}).Run(context.Background())
}

func TestLoopStateMovesAcrossSectionsAndTogglesCompleted(t *testing.T) {
	state := loopState{
		snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{PlanID: "running", Liveness: monitor.LivenessLive},
			{PlanID: "attention", AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
			{PlanID: "planned"},
			{PlanID: "completed", Status: "completed"},
		}},
		showCompleted: true,
	}
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyArrowDown}); quit || state.selected != 1 {
		t.Fatalf("down across section boundary quit=%t selected=%d, want false, 1", quit, state.selected)
	}
	state.selected = 3
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'c'}); quit {
		t.Fatal("completed toggle unexpectedly quit")
	}
	if state.showCompleted || state.selected != 2 {
		t.Fatalf("hidden completed state show=%t selected=%d, want false, 2", state.showCompleted, state.selected)
	}
	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'C'})
	if !state.showCompleted || len(visibleRows(state.snapshot.Rows, state.showCompleted, state.focusRepositoryID)) != 4 {
		t.Fatalf("shown completed state show=%t rows=%d, want true, 4", state.showCompleted, len(visibleRows(state.snapshot.Rows, state.showCompleted, state.focusRepositoryID)))
	}
}

func TestRepositoryFocusComposesWithWarningsCompletedAndRefresh(t *testing.T) {
	state := loopState{
		snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{Kind: monitor.RowKindRepositoryWarning, RepositoryID: "repo-a", RepositoryName: "alpha", Status: "invalid"},
			{Kind: monitor.RowKindPlan, RepositoryID: "repo-b", RepositoryName: "beta", PlanID: "other", Status: "planned"},
			{Kind: monitor.RowKindPlan, RepositoryID: "repo-a", RepositoryName: "alpha", PlanID: "target", Status: "planned"},
			{Kind: monitor.RowKindPlan, RepositoryID: "repo-a", RepositoryName: "alpha", PlanID: "done", Status: "completed"},
		}},
		selected: 2,
	}

	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
	row, ok := state.selectedRow()
	if !ok || row.PlanID != "target" || state.focusRepositoryID != "repo-a" || state.selected != 1 {
		t.Fatalf("focused selection index=%d row=%+v ok=%t focus=%q", state.selected, row, ok, state.focusRepositoryID)
	}
	rows := state.visibleRows()
	if len(rows) != 2 || rows[0].Kind != monitor.RowKindRepositoryWarning || rows[1].PlanID != "target" {
		t.Fatalf("focused rows = %+v, want repository warning and target", rows)
	}

	state.showCompleted = true
	state.selected = 2
	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'c'})
	if row, ok = state.selectedRow(); !ok || row.PlanID != "target" {
		t.Fatalf("completed toggle did not clamp safely: index=%d row=%+v ok=%t", state.selected, row, ok)
	}

	state.replaceSnapshot(monitor.Snapshot{Rows: []monitor.Row{
		{Kind: monitor.RowKindPlan, RepositoryID: "repo-b", RepositoryName: "beta", PlanID: "other", Status: "planned"},
	}})
	if len(state.visibleRows()) != 0 || state.selected != 0 || state.focusRepositoryName != "alpha" {
		t.Fatalf("empty focused refresh rows=%+v selected=%d name=%q", state.visibleRows(), state.selected, state.focusRepositoryName)
	}

	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
	if state.focusRepositoryID != "" || len(state.visibleRows()) != 1 || state.visibleRows()[0].PlanID != "other" {
		t.Fatalf("restored all-repository rows=%+v focus=%q", state.visibleRows(), state.focusRepositoryID)
	}
}

func TestRepositoryFocusIgnoresWarningRows(t *testing.T) {
	state := loopState{snapshot: monitor.Snapshot{Rows: []monitor.Row{{
		Kind: monitor.RowKindRepositoryWarning, RepositoryID: "repo-a", RepositoryName: "alpha", Status: "invalid",
	}}}}
	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
	if state.focusRepositoryID != "" {
		t.Fatalf("warning row established repository focus %q", state.focusRepositoryID)
	}
}

func TestRootQuitKeysAndCompletedDefault(t *testing.T) {
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	now := base
	state := loopState{
		snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{PlanID: "active", Status: "planned"},
			{PlanID: "done", Status: "completed"},
		}},
		now: func() time.Time { return now },
	}
	if rows := visibleRows(state.snapshot.Rows, state.showCompleted, state.focusRepositoryID); len(rows) != 1 || rows[0].PlanID != "active" {
		t.Fatalf("initial visible rows = %+v, want only active plan", rows)
	}
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyEsc}); quit {
		t.Fatal("first root Escape unexpectedly quit")
	}
	now = base.Add(500 * time.Millisecond)
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyEsc}); !quit {
		t.Fatal("second root Escape within one second did not quit")
	}

	state.lastRootEscape = time.Time{}
	now = base
	state.handleKey(term.KeyEvent{Key: term.KeyEsc})
	now = base.Add(time.Second + time.Nanosecond)
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyEsc}); quit {
		t.Fatal("stale Escape sequence unexpectedly quit")
	}
	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'j'})
	now = now.Add(100 * time.Millisecond)
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyEsc}); quit {
		t.Fatal("interrupted Escape sequence unexpectedly quit")
	}
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'q'}); !quit {
		t.Fatal("q did not quit at root")
	}
}

func TestQQuitsGloballyButDeclinesConfirmation(t *testing.T) {
	state := loopState{detail: &detailState{}}
	if quit := (App{}).handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'Q'}); !quit {
		t.Fatal("q did not quit from detail")
	}

	state = loopState{}
	called := false
	state.beginConfirm("Continue?", func(accepted bool) {
		called = true
		if accepted {
			t.Fatal("q accepted confirmation")
		}
	})
	if quit := (App{}).handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'q'}); quit {
		t.Fatal("q quit instead of declining confirmation")
	}
	if !called || state.confirm != nil {
		t.Fatalf("q confirmation decline called=%t prompt=%#v", called, state.confirm)
	}
}

func TestOutputSupportsColorHonorsEnvironmentAndTerminal(t *testing.T) {
	writer := terminalRecordingWriter{terminal: true}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if !outputSupportsColor(writer) {
		t.Fatal("terminal output should support color")
	}
	t.Setenv("NO_COLOR", "1")
	if outputSupportsColor(writer) {
		t.Fatal("NO_COLOR should disable color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if outputSupportsColor(writer) {
		t.Fatal("TERM=dumb should disable color")
	}
	if outputSupportsColor(&recordingWriter{writes: make(chan string, 1)}) {
		t.Fatal("non-terminal output should disable color")
	}
}

type terminalRecordingWriter struct {
	terminal bool
}

func (w terminalRecordingWriter) Write(value []byte) (int, error) { return len(value), nil }
func (w terminalRecordingWriter) IsTerminal() bool                { return w.terminal }

func TestConfirmPromptHandlesYesNoAndEscape(t *testing.T) {
	tests := []struct {
		name string
		key  term.KeyEvent
		want bool
	}{
		{name: "yes", key: term.KeyEvent{Key: term.KeyRune, Rune: 'y'}, want: true},
		{name: "uppercase no", key: term.KeyEvent{Key: term.KeyRune, Rune: 'N'}, want: false},
		{name: "escape cancels", key: term.KeyEvent{Key: term.KeyEsc}, want: false},
		{name: "q cancels", key: term.KeyEvent{Key: term.KeyRune, Rune: 'q'}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := loopState{}
			called := false
			state.beginConfirm("Continue?", func(value bool) {
				called = true
				if value != test.want {
					t.Fatalf("response = %t, want %t", value, test.want)
				}
			})
			if quit := state.handleKey(test.key); quit {
				t.Fatal("confirm key unexpectedly quit the loop")
			}
			if !called || state.confirm != nil {
				t.Fatalf("confirm resolution called=%t prompt=%#v", called, state.confirm)
			}
		})
	}
}

func waitForFrame(t *testing.T, writes <-chan string) string {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case value := <-writes:
			if strings.HasPrefix(value, clearScreenSequence) {
				return value
			}
		case <-timer.C:
			t.Fatal("timed out waiting for frame write")
		}
	}
}
