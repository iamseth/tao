package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
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

type fakeNoteCollector struct {
	mu        sync.Mutex
	snapshots []note.Snapshot
	calls     int
}

func (c *fakeNoteCollector) Collect(context.Context) (note.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	index := c.calls
	if index >= len(c.snapshots) {
		index = len(c.snapshots) - 1
	}
	c.calls++
	return c.snapshots[index], nil
}

func (c *fakeNoteCollector) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
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
	for _, frame := range frames {
		if !strings.Contains(frame, "one") || !strings.Contains(frame, "two") {
			t.Fatalf("complete frame write lost plan rows: %q", frames)
		}
	}
	entered, restored := terminal.state()
	if !entered || !restored {
		t.Fatalf("terminal state entered=%t restored=%t, want both true", entered, restored)
	}
	if !ticker.wasStopped() {
		t.Fatal("refresh ticker was not stopped")
	}
}

func TestRunCollectsNotesInitiallyAndOnRefresh(t *testing.T) {
	terminal := &fakeTerminal{size: term.Size{Width: 100, Height: 20}, resizes: make(chan struct{})}
	ticker := &fakeTicker{channel: make(chan time.Time, 1)}
	notes := &fakeNoteCollector{snapshots: []note.Snapshot{
		{Notes: []note.CatalogNote{{RepositoryID: "repo", RepositoryName: "repo", ID: "note-first", Text: "first"}}},
		{Notes: []note.CatalogNote{{RepositoryID: "repo", RepositoryName: "repo", ID: "note-refreshed", Text: "refreshed"}}},
	}}
	output := &recordingWriter{writes: make(chan string, 16)}
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- (App{
			Input: reader, Output: output, Terminal: terminal, Ticker: ticker,
			Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}}, Notes: notes,
		}).Run(context.Background())
	}()

	_ = waitForFrame(t, output.writes)
	if _, err := io.WriteString(writer, "\x1b[Z"); err != nil {
		t.Fatal(err)
	}
	initialNotes := waitForFrame(t, output.writes)
	if !strings.Contains(initialNotes, "first") || notes.callCount() != 1 {
		t.Fatalf("initial notes frame=%q calls=%d", initialNotes, notes.callCount())
	}
	ticker.channel <- time.Now()
	refreshed := waitForFrame(t, output.writes)
	if !strings.Contains(refreshed, "refreshed") || notes.callCount() != 2 {
		t.Fatalf("refreshed notes frame=%q calls=%d", refreshed, notes.callCount())
	}
	if _, err := io.WriteString(writer, "q"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
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
		if cells.Width(line) > 10 {
			t.Fatalf("resized frame line %q exceeds new width", line)
		}
	}
	if !strings.Contains(resized, "RUN") || !strings.Contains(resized, "1 plan") {
		t.Fatalf("resized frame lost plan context: %q", resized)
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

func TestTopLevelTabNavigationPreservesPlanSelectionAcrossRefresh(t *testing.T) {
	state := loopState{
		snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{RepositoryID: "repo-a", PlanID: "first", Status: "planned"},
			{RepositoryID: "repo-b", PlanID: "target", Status: "planned"},
		}},
		selected: 1,
	}
	if state.activePage() != PagePlans {
		t.Fatalf("initial page = %q, want plans", state.activePage())
	}

	state.handleKey(term.KeyEvent{Key: term.KeyShiftTab})
	if state.activePage() != PageNotes || state.selected != 0 {
		t.Fatalf("Shift+Tab page=%q selection=%d, want notes selection 0", state.activePage(), state.selected)
	}
	state.replaceSnapshot(monitor.Snapshot{Rows: []monitor.Row{
		{RepositoryID: "repo-b", PlanID: "target", Status: "planned"},
		{RepositoryID: "repo-a", PlanID: "first", Status: "planned"},
	}})
	state.handleKey(term.KeyEvent{Key: term.KeyTab})
	row, ok := state.selectedRow()
	if state.activePage() != PagePlans || !ok || state.selected != 0 || row.PlanID != "target" {
		t.Fatalf("Tab page=%q selection=%d row=%+v ok=%t, want preserved target", state.activePage(), state.selected, row, ok)
	}
	for _, want := range []PageID{PageSettings, PageDebug, PageNotes, PagePlans} {
		state.handleKey(term.KeyEvent{Key: term.KeyArrowRight})
		if state.activePage() != want {
			t.Fatalf("right navigation page=%q, want %q", state.activePage(), want)
		}
	}
	state.handleKey(term.KeyEvent{Key: term.KeyShiftTab})
	if state.activePage() != PageNotes {
		t.Fatalf("Shift+Tab from plans page=%q, want notes", state.activePage())
	}
}

func TestNoteSelectionRefreshFocusDetailAndPlanActionIsolation(t *testing.T) {
	state := loopState{
		page: PageNotes,
		noteSnapshot: note.Snapshot{Notes: []note.CatalogNote{
			{RepositoryID: "repo-a", RepositoryName: "alpha", RepositoryRoot: "/alpha", ID: "first", Text: "first"},
			{RepositoryID: "repo-b", RepositoryName: "beta", RepositoryRoot: "/beta", ID: "target", Text: "old"},
		}},
		selected:      1,
		showCompleted: true,
	}
	state.replaceNoteSnapshot(note.Snapshot{Notes: []note.CatalogNote{
		{RepositoryID: "repo-b", RepositoryName: "beta", RepositoryRoot: "/beta", ID: "target", Text: "updated"},
		{RepositoryID: "repo-a", RepositoryName: "alpha", RepositoryRoot: "/alpha", ID: "first", Text: "first"},
	}})
	if item, ok := state.selectedNote(); !ok || state.selected != 0 || item.ID != "target" {
		t.Fatalf("refresh selection index=%d item=%+v ok=%t", state.selected, item, ok)
	}

	var requests []CommandRequest
	actions, err := NewActions(ActionOptions{Executable: "tao", Launcher: func(_ context.Context, request CommandRequest) error {
		requests = append(requests, request)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	app := App{Actions: actions}
	for _, r := range []rune{'r', 'R', 'a', 'A', 'm', 'M', 'c', 'C'} {
		if app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: r}) {
			t.Fatalf("plan action key %q quit Notes", r)
		}
	}
	if len(requests) != 0 || !state.showCompleted || state.confirm != nil {
		t.Fatalf("plan keys requests=%+v completed=%t confirm=%#v", requests, state.showCompleted, state.confirm)
	}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
	if state.focusRepositoryID != "repo-b" || len(state.visibleNotes()) != 1 {
		t.Fatalf("note focus=%q visible=%+v", state.focusRepositoryID, state.visibleNotes())
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEnter})
	if state.noteDetail == nil || state.noteDetail.ID != "target" || state.noteDetail.Text != "updated" {
		t.Fatalf("opened note detail = %+v", state.noteDetail)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyBackspace})
	if state.noteDetail != nil || state.activePage() != PageNotes {
		t.Fatalf("Backspace did not return to Notes: detail=%+v page=%q", state.noteDetail, state.activePage())
	}
}

func TestNoteDetailNavigationAndViewportClamping(t *testing.T) {
	item := note.CatalogNote{RepositoryID: "repo", ID: "note", Text: "one\ntwo\nthree\nfour\nfive"}
	state := loopState{
		noteDetail: &item,
		size:       term.Size{Width: 80, Height: 10},
	}
	app := App{}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	if state.noteDetailOffset != 4 {
		t.Fatalf("end offset = %d, want 4", state.noteDetailOffset)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyArrowUp})
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'j'})
	if state.noteDetailOffset != 4 {
		t.Fatalf("up/down offset = %d, want 4", state.noteDetailOffset)
	}

	state.size.Height = 12
	state.clampNoteDetailOffset()
	if state.noteDetailOffset != 2 {
		t.Fatalf("resized offset = %d, want 2", state.noteDetailOffset)
	}

	state.noteSnapshot = note.Snapshot{Notes: []note.CatalogNote{{RepositoryID: "repo", ID: "note", Text: "short"}}}
	state.refreshNoteDetail()
	if state.noteDetailOffset != 0 || state.noteDetail.Text != "short" {
		t.Fatalf("refreshed detail offset=%d note=%+v, want short note at top", state.noteDetailOffset, state.noteDetail)
	}
}

func TestNoteDetailClosesWhenRefreshedNoteDisappears(t *testing.T) {
	tests := []struct {
		name     string
		snapshot note.Snapshot
	}{
		{
			name: "archived",
			snapshot: note.Snapshot{Notes: []note.CatalogNote{
				{RepositoryID: "repo", ID: "other", Text: "still open"},
			}},
		},
		{name: "removed", snapshot: note.Snapshot{}},
		{
			name: "malformed record",
			snapshot: note.Snapshot{Warnings: []note.CatalogWarning{{
				Kind:         note.CatalogWarningRecord,
				RepositoryID: "repo",
				Path:         "note.json",
				Err:          errors.New("invalid note"),
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := note.CatalogNote{RepositoryID: "repo", ID: "note", Text: "stale"}
			state := loopState{
				noteSnapshot:     test.snapshot,
				noteDetail:       &item,
				noteDetailOffset: 3,
			}

			state.refreshNoteDetail()

			if state.noteDetail != nil || state.noteDetailOffset != 0 {
				t.Fatalf("refreshed missing detail=%+v offset=%d, want closed detail at top", state.noteDetail, state.noteDetailOffset)
			}
		})
	}
}

func TestNotesTabKeepsSharedFocusAndRejectsPlanOnlyKeys(t *testing.T) {
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
	state := loopState{
		snapshot: monitor.Snapshot{Rows: []monitor.Row{{
			Kind: monitor.RowKindPlan, RepositoryID: "repo-a", RepositoryName: "alpha", RepositoryRoot: "/alpha", PlanID: "plan", PlanDir: "/plans/plan", Status: "planned",
		}}},
		showCompleted: true,
	}
	app := App{Actions: actions}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyShiftTab})
	if state.activePage() != PageNotes || state.focusRepositoryID != "repo-a" {
		t.Fatalf("notes page=%q focus=%q, want shared repo-a focus", state.activePage(), state.focusRepositoryID)
	}

	for _, key := range []term.KeyEvent{
		{Key: term.KeyRune, Rune: 'r'},
		{Key: term.KeyRune, Rune: 'a'},
		{Key: term.KeyRune, Rune: 'm'},
		{Key: term.KeyRune, Rune: 'M'},
		{Key: term.KeyRune, Rune: 'c'},
		{Key: term.KeyEnter},
	} {
		if quit := app.handleKey(context.Background(), &state, key); quit {
			t.Fatalf("notes key %+v unexpectedly quit", key)
		}
	}
	if len(requests) != 0 || !state.showCompleted || state.detail != nil {
		t.Fatalf("notes plan isolation requests=%+v completed=%t detail=%#v", requests, state.showCompleted, state.detail)
	}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'f'})
	if state.focusRepositoryID != "" {
		t.Fatalf("shared repository focus was not cleared from Notes: %q", state.focusRepositoryID)
	}
	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'q'}); !quit {
		t.Fatal("q did not quit from Notes")
	}
}

func TestPlanDetailTabsUseLifecycleDefaultsAndPreserveReloadSelection(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "plan-a"}}}
	repository := &fakeDetailRepository{detail: detail, tail: "one\ntwo\nthree\nfour\n"}
	app := App{Details: repository}

	for _, test := range []struct {
		name string
		row  monitor.Row
		want detailTab
	}{
		{name: "planned overview", row: monitor.Row{PlanID: "plan-a", PlanDir: "/plan", Status: plan.StatusPlanned}, want: detailTabOverview},
		{name: "live activity", row: monitor.Row{PlanID: "plan-a", PlanDir: "/plan", Status: plan.StatusInProgress, Liveness: monitor.LivenessLive}, want: detailTabActivity},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state := loopState{size: term.Size{Width: 60, Height: 8}}
			app.openDetail(ctx, &state, test.row)
			if state.detail == nil || state.detail.activeTab != test.want {
				t.Fatalf("opened detail tab = %#v, want %v", state.detail, test.want)
			}
			state.detail.activeTab = detailTabSlices
			state.detail.overviewOffset = 1
			app.reloadDetail(ctx, &state)
			if state.detail.activeTab != detailTabSlices || state.detail.overviewOffset != 1 {
				t.Fatalf("reload reset detail navigation: %#v", state.detail)
			}
			state.closeDetail()
		})
	}
}

func TestPlanDetailLocalNavigationBoundsEachTab(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusPlanned,
			Plan: plan.PlanState{PendingSlices: []string{"001-a", "002-b"}, Decision: &plan.Decision{
				Problem: strings.Repeat("problem ", 20), ExpectedBenefit: strings.Repeat("benefit ", 20),
			}},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a"}, {ID: "002-b"}}},
	}
	state := loopState{detail: &detailState{plan: detail, selectedSliceID: "001-a", log: "one\ntwo\nthree\nfour\nfive\n"}, size: term.Size{Width: 30, Height: 8}}
	app := App{}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	if state.detail.overviewOffset != state.detail.maxOffset(detailTabOverview, state.size) || state.detail.overviewOffset == 0 {
		t.Fatalf("Overview G offset = %d", state.detail.overviewOffset)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyTab})
	if state.detail.activeTab != detailTabSlices || state.activePage() != PagePlans {
		t.Fatalf("Tab leaked to root page: tab=%v page=%v", state.detail.activeTab, state.activePage())
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	if state.detail.selectedSliceID != "002-b" {
		t.Fatalf("Slices G selected %q", state.detail.selectedSliceID)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyArrowRight})
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	if state.detail.activeTab != detailTabActivity || state.detail.activityOffset != state.detail.maxOffset(detailTabActivity, state.size) {
		t.Fatalf("Activity navigation = tab %v offset %d", state.detail.activeTab, state.detail.activityOffset)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'g'})
	if state.detail.activityOffset != 0 {
		t.Fatalf("Activity g offset = %d", state.detail.activityOffset)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyArrowRight})
	if state.detail.activeTab != detailTabOverview {
		t.Fatalf("right did not wrap to Overview: %v", state.detail.activeTab)
	}
}

func TestPlanDetailOverviewBottomIncludesCompletedInspectionFindings(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Status: plan.StatusPlanned, Plan: plan.PlanState{ID: "plan-a"}}}
	state := loopState{
		detail: &detailState{
			plan:      detail,
			activeTab: detailTabOverview,
			inspection: detailInspectionView{status: detailInspectionReady, findings: []DetailFinding{
				{Severity: "warning", Message: strings.Repeat("first wrapped finding ", 5)},
				{Severity: "warning", Message: strings.Repeat("second wrapped finding ", 5)},
				{Severity: "warning", Message: "last-finding"},
			}},
		},
		size: term.Size{Width: 32, Height: 10},
	}

	(App{}).handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	frame := RenderDetail(DetailModel{
		Plan:           detail,
		ActiveTab:      detailTabOverview,
		OverviewOffset: state.detail.overviewOffset,
		Inspection:     state.detail.inspection,
		Width:          state.size.Width,
		Height:         state.size.Height,
	})
	if !strings.Contains(frame, "last-finding") {
		t.Fatalf("Overview G did not reach final inspection finding at offset %d:\n%s", state.detail.overviewOffset, frame)
	}
}

func TestNestedSliceDetailScrollPreservesSliceSelectionAndReturnsToSlices(t *testing.T) {
	tasks := make([]string, 20)
	for index := range tasks {
		tasks[index] = fmt.Sprintf("task-%02d", index+1)
	}
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{PendingSlices: []string{"001-work", "002-next"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "001-work", Title: "Work", Goal: "goal", Context: "context", Tasks: tasks},
			{ID: "002-next", Title: "Next"},
		}},
	}
	state := loopState{
		detail: &detailState{plan: detail, selectedSliceID: "001-work", activeTab: detailTabSlices, sliceOpen: true},
		size:   term.Size{Width: 40, Height: 12},
	}
	app := App{}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	if state.detail.sliceOffset == 0 || state.detail.selectedSliceID != "001-work" {
		t.Fatalf("nested G offset=%d selected=%q", state.detail.sliceOffset, state.detail.selectedSliceID)
	}
	state.size.Height = 30
	state.detail.clampOffsets(state.size)
	if state.detail.sliceOffset != state.detail.sliceMaxOffset(state.size) {
		t.Fatalf("resized nested offset=%d max=%d", state.detail.sliceOffset, state.detail.sliceMaxOffset(state.size))
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyBackspace})
	if state.detail.sliceOpen || state.detail.sliceOffset != 0 || state.detail.activeTab != detailTabSlices || state.detail.selectedSliceID != "001-work" {
		t.Fatalf("nested return changed Slices state: %#v", state.detail)
	}
}

func TestTabNavigationDoesNotEscapeConfirmationsOrDetails(t *testing.T) {
	state := loopState{}
	state.beginConfirm("Continue?", nil)
	(App{}).handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyTab})
	if state.activePage() != PagePlans || state.confirm == nil {
		t.Fatalf("Tab changed page or confirmation: page=%q confirm=%#v", state.activePage(), state.confirm)
	}

	state.confirm = nil
	state.detail = &detailState{}
	(App{}).handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyArrowRight})
	if state.activePage() != PagePlans || state.detail == nil {
		t.Fatalf("right changed page or detail: page=%q detail=%#v", state.activePage(), state.detail)
	}
}

func TestShortcutLegendToggleEscapeAndModalKeys(t *testing.T) {
	state := loopState{snapshot: monitor.Snapshot{Rows: []monitor.Row{{PlanID: "one"}, {PlanID: "two"}}}}
	app := App{}

	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'}); quit || !state.showShortcuts {
		t.Fatalf("? toggle quit=%t show=%t, want open legend", quit, state.showShortcuts)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyArrowDown})
	if state.selected != 0 {
		t.Fatalf("modal shortcut legend allowed background selection to move to %d", state.selected)
	}
	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEsc}); quit || state.showShortcuts {
		t.Fatalf("Esc close quit=%t show=%t, want closed legend", quit, state.showShortcuts)
	}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'})
	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyBackspace}); quit || state.showShortcuts {
		t.Fatalf("Backspace close quit=%t show=%t, want closed legend", quit, state.showShortcuts)
	}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'})
	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'}); quit || state.showShortcuts {
		t.Fatalf("second ? quit=%t show=%t, want toggled closed", quit, state.showShortcuts)
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
	if quit := state.handleKey(term.KeyEvent{Key: term.KeyBackspace}); quit {
		t.Fatal("root Backspace unexpectedly quit")
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

func TestOutputSupportsColorResolvesProfileFromEnvironmentAndTerminal(t *testing.T) {
	writer := terminalRecordingWriter{terminal: true}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")
	if got := outputSupportsColor(writer); got != ProfileANSI256 {
		t.Fatalf("terminal profile = %s, want %s", got, ProfileANSI256)
	}
	t.Setenv("NO_COLOR", "1")
	if got := outputSupportsColor(writer); got != ProfileNone {
		t.Fatalf("NO_COLOR profile = %s, want none", got)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if got := outputSupportsColor(writer); got != ProfileNone {
		t.Fatalf("dumb terminal profile = %s, want none", got)
	}
	t.Setenv("TERM", "xterm-256color")
	if got := outputSupportsColor(&recordingWriter{writes: make(chan string, 1)}); got != ProfileNone {
		t.Fatalf("non-terminal profile = %s, want none", got)
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
		{name: "backspace cancels", key: term.KeyEvent{Key: term.KeyBackspace}, want: false},
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
