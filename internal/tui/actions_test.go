package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runqueue"
	"github.com/iamseth/tao/internal/term"
)

type recordingActionLauncher struct {
	calls  []CommandRequest
	failAt int
}

func (l *recordingActionLauncher) launch(_ context.Context, request CommandRequest) error {
	request.Args = append([]string(nil), request.Args...)
	l.calls = append(l.calls, request)
	if l.failAt > 0 && len(l.calls) == l.failAt {
		return errors.New("spawn unavailable")
	}
	return nil
}

func TestRunActionLaunchesExactDetachedCommand(t *testing.T) {
	tests := []struct {
		name   string
		status string
		live   monitor.Liveness
		args   []string
		calls  int
	}{
		{name: "planned", status: plan.StatusPlanned, args: []string{"run", "plan-a"}, calls: 1},
		{name: "blocked continues", status: plan.StatusBlocked, args: []string{"run", "--continue", "plan-a"}, calls: 1},
		{name: "live ignored", status: plan.StatusInProgress, live: monitor.LivenessLive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			launcher := &recordingActionLauncher{}
			actions := newTestActions(t, launcher, nil, nil)
			row := testActionRow()
			row.Status = test.status
			row.Liveness = test.live

			actions.RunPlan(context.Background(), row)

			if len(launcher.calls) != test.calls {
				t.Fatalf("launch calls = %+v, want %d", launcher.calls, test.calls)
			}
			if test.calls == 0 {
				if len(actions.labels()) != 0 {
					t.Fatalf("ignored action feedback = %v", actions.labels())
				}
				return
			}
			assertActionRequest(t, launcher.calls[0], "/repos/alpha", test.args, true)
			if got := actions.labels()[actionRowKey(row)]; got != "starting…" {
				t.Fatalf("run feedback = %q, want starting…", got)
			}
		})
	}
}

func TestQueueActionAddsSynchronouslyAndEnsuresDrain(t *testing.T) {
	launcher := &recordingActionLauncher{}
	actions := newTestActions(t, launcher, nil, nil)
	row := testActionRow()

	actions.QueuePlan(context.Background(), row, monitor.Snapshot{Rows: []monitor.Row{row}})

	if len(launcher.calls) != 2 {
		t.Fatalf("launch calls = %+v, want queue add and queue start", launcher.calls)
	}
	assertActionRequest(t, launcher.calls[0], "/repos/alpha", []string{"queue", "add", "plan-a"}, false)
	assertActionRequest(t, launcher.calls[1], "/repos/alpha", []string{"queue", "start"}, true)
	if got := actions.labels()[actionRowKey(row)]; got != "queueing…" {
		t.Fatalf("queue feedback = %q, want queueing…", got)
	}
}

func TestQueueActionDoesNotStartDrainWhenRunningEntryIsActive(t *testing.T) {
	tests := []struct {
		name     string
		running  monitor.Row
		readLock func(string) (plan.RunLock, error)
	}{
		{
			name: "fresh heartbeat",
			running: monitor.Row{
				RepositoryID: "repo-alpha", QueueStatus: runqueue.QueueStatusRunning, Liveness: monitor.LivenessLive,
			},
		},
		{
			name: "live run lock",
			running: monitor.Row{
				RepositoryID: "repo-alpha", QueueStatus: runqueue.QueueStatusRunning, PlanDir: "/plans/running",
			},
			readLock: func(path string) (plan.RunLock, error) {
				if path != "/plans/running" {
					t.Fatalf("lock path = %q, want /plans/running", path)
				}
				return plan.RunLock{PID: 42, ProcessAlive: true}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			launcher := &recordingActionLauncher{}
			actions := newTestActions(t, launcher, test.readLock, nil)
			row := testActionRow()

			actions.QueuePlan(context.Background(), row, monitor.Snapshot{Rows: []monitor.Row{test.running}})

			if len(launcher.calls) != 1 {
				t.Fatalf("launch calls = %+v, want only queue add", launcher.calls)
			}
			assertActionRequest(t, launcher.calls[0], row.RepositoryRoot, []string{"queue", "add", row.PlanID}, false)
		})
	}
}

func TestApprovalActionUsesConfirmationFlowAndExactCommand(t *testing.T) {
	launcher := &recordingActionLauncher{}
	actions := newTestActions(t, launcher, nil, nil)
	row := testActionRow()
	row.ApprovalSliceID = "005-risk"
	row.ApprovalReason = "security owner sign-off"
	app := App{Actions: actions}
	state := loopState{snapshot: monitor.Snapshot{Rows: []monitor.Row{row}}, showCompleted: true}

	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'a'}); quit {
		t.Fatal("approval key unexpectedly quit")
	}
	if state.confirmMessage() != "Approve slice 005-risk: security owner sign-off?" {
		t.Fatalf("confirmation = %q", state.confirmMessage())
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("approval launched before confirmation: %+v", launcher.calls)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'n'})
	if len(launcher.calls) != 0 {
		t.Fatalf("declined approval launched: %+v", launcher.calls)
	}

	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'a'})
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'y'})
	if len(launcher.calls) != 1 {
		t.Fatalf("confirmed approval calls = %+v, want one", launcher.calls)
	}
	assertActionRequest(t, launcher.calls[0], row.RepositoryRoot, []string{"approve", "--slice", "005-risk", "plan-a"}, true)
	if got := actions.labels()[actionRowKey(row)]; got != "starting…" {
		t.Fatalf("approval feedback = %q, want starting…", got)
	}
}

func TestApprovalKeyIsIgnoredWithoutGate(t *testing.T) {
	launcher := &recordingActionLauncher{}
	actions := newTestActions(t, launcher, nil, nil)
	state := loopState{snapshot: monitor.Snapshot{Rows: []monitor.Row{testActionRow()}}, showCompleted: true}

	App{Actions: actions}.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'a'})

	if state.confirm != nil || len(launcher.calls) != 0 {
		t.Fatalf("ungated approval prompt=%#v calls=%+v", state.confirm, launcher.calls)
	}
}

func TestActionFeedbackTransitionsToObservedOrFailed(t *testing.T) {
	base := time.Date(2026, 8, 10, 17, 0, 0, 0, time.UTC)
	now := base
	launcher := &recordingActionLauncher{}
	actions := newTestActions(t, launcher, nil, func() time.Time { return now })
	row := testActionRow()
	actions.RunPlan(context.Background(), row)
	key := actionRowKey(row)

	now = base.Add(9 * time.Second)
	actions.Reconcile(monitor.Snapshot{Rows: []monitor.Row{row}})
	if got := actions.labels()[key]; got != "starting…" {
		t.Fatalf("pre-timeout feedback = %q, want starting…", got)
	}

	now = base.Add(10 * time.Second)
	actions.Reconcile(monitor.Snapshot{Rows: []monitor.Row{row}})
	if got := actions.labels()[key]; got != "failed to start" {
		t.Fatalf("timeout feedback = %q, want failed to start", got)
	}
	if message := actions.statusMessage(); !strings.Contains(message, "tao log plan-a") || !strings.Contains(message, "no activity observed") {
		t.Fatalf("timeout message = %q", message)
	}

	// A retry replaces failure feedback, and a fresh heartbeat observes startup.
	now = base.Add(11 * time.Second)
	actions.RunPlan(context.Background(), row)
	live := row
	live.Liveness = monitor.LivenessLive
	actions.Reconcile(monitor.Snapshot{Rows: []monitor.Row{live}})
	if _, ok := actions.labels()[key]; ok {
		t.Fatalf("observed run retained feedback: %v", actions.labels())
	}
	if actions.statusMessage() != "" {
		t.Fatalf("observed run retained message %q", actions.statusMessage())
	}
}

func TestRunFeedbackIgnoresPreexistingTerminalQueueStatus(t *testing.T) {
	base := time.Date(2026, 8, 10, 17, 0, 0, 0, time.UTC)
	now := base
	launcher := &recordingActionLauncher{}
	actions := newTestActions(t, launcher, nil, func() time.Time { return now })
	row := testActionRow()
	row.QueueStatus = runqueue.QueueStatusFailed

	actions.RunPlan(context.Background(), row)
	now = base.Add(defaultActionStartupTimeout)
	actions.Reconcile(monitor.Snapshot{Rows: []monitor.Row{row}})

	if got := actions.labels()[actionRowKey(row)]; got != "failed to start" {
		t.Fatalf("feedback with unchanged terminal queue status = %q, want failed to start", got)
	}
	if message := actions.statusMessage(); !strings.Contains(message, "no activity observed") {
		t.Fatalf("timeout message = %q, want no-activity guidance", message)
	}
}

func TestActionLaunchFailureImmediatelyShowsLogGuidance(t *testing.T) {
	launcher := &recordingActionLauncher{failAt: 1}
	actions := newTestActions(t, launcher, nil, nil)
	row := testActionRow()

	actions.RunPlan(context.Background(), row)

	if got := actions.labels()[actionRowKey(row)]; got != "failed to start" {
		t.Fatalf("failure feedback = %q", got)
	}
	for _, want := range []string{"spawn unavailable", "tao log plan-a"} {
		if !strings.Contains(actions.statusMessage(), want) {
			t.Fatalf("failure message %q missing %q", actions.statusMessage(), want)
		}
	}
}

func TestQueueFeedbackClearsWhenEntryAppears(t *testing.T) {
	launcher := &recordingActionLauncher{}
	actions := newTestActions(t, launcher, nil, nil)
	row := testActionRow()
	actions.QueuePlan(context.Background(), row, monitor.Snapshot{})
	queued := row
	queued.QueueStatus = runqueue.QueueStatusPending

	actions.Reconcile(monitor.Snapshot{Rows: []monitor.Row{queued}})

	if len(actions.labels()) != 0 {
		t.Fatalf("observed queue retained feedback: %v", actions.labels())
	}
}

func newTestActions(t *testing.T, launcher *recordingActionLauncher, readLock func(string) (plan.RunLock, error), now func() time.Time) *Actions {
	t.Helper()
	if readLock == nil {
		readLock = func(string) (plan.RunLock, error) { return plan.RunLock{}, errors.New("missing") }
	}
	actions, err := NewActions(ActionOptions{
		Executable:  "/bin/tao",
		Launcher:    launcher.launch,
		ReadRunLock: readLock,
		Now:         now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return actions
}

func testActionRow() monitor.Row {
	return monitor.Row{
		Kind:           monitor.RowKindPlan,
		RepositoryID:   "repo-alpha",
		RepositoryName: "alpha",
		RepositoryRoot: "/repos/alpha",
		PlanID:         "plan-a",
		PlanDir:        "/data/alpha/plans/plan-a",
		Status:         plan.StatusPlanned,
	}
}

func assertActionRequest(t *testing.T, got CommandRequest, cwd string, args []string, detached bool) {
	t.Helper()
	if got.CWD != cwd || got.Executable != "/bin/tao" || !slices.Equal(got.Args, args) || got.Detached != detached {
		t.Fatalf("command request = %+v, want cwd=%q executable=/bin/tao args=%v detached=%t", got, cwd, args, detached)
	}
}
