package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
)

type planFixFunc func(context.Context, monitor.Row) error

func (f planFixFunc) Launch(ctx context.Context, row monitor.Row) error { return f(ctx, row) }

func TestPlanFixGuardsAndFeedback(t *testing.T) {
	for _, scenario := range []string{"blocked", "verification_failed", "attention", "detail", "shortcuts", "search", "confirm", "picker", "filter", "note", "slice", "changes", "settings", "empty", "wrong-key", "live", "locked", "crashed", "crashed-merge", "merge", "not-stuck", "invalid", "unavailable", "failure"} {
		t.Run(scenario, func(t *testing.T) {
			row := monitor.Row{PlanID: "plan", RepositoryRoot: "/repo", Status: plan.StatusBlocked}
			state := loopState{page: PagePlans, size: term.Size{Width: 80, Height: 24}}
			key := term.KeyEvent{Key: term.KeyRune, Rune: 'x'}
			wantMessage := ""
			ignored := false
			switch scenario {
			case "verification_failed":
				row.Status = plan.StatusVerificationFailed
			case "attention":
				row.Status = plan.StatusPending
				row.AttentionReasons = []monitor.AttentionReason{"needs help"}
			case "detail":
				state.detail = &detailState{row: row}
			case "shortcuts":
				state.showShortcuts = true
				ignored = true
			case "search":
				state.searchActive = true
				ignored = true
			case "confirm":
				state.confirm = &confirmPrompt{}
				ignored = true
			case "picker":
				state.notePicker = &noteRepositoryPicker{}
				ignored = true
			case "filter":
				state.filterMenu = &filterMenu{}
				ignored = true
			case "note":
				state.noteDetail = &note.CatalogNote{}
				ignored = true
			case "slice":
				state.detail = &detailState{row: row, sliceOpen: true}
				ignored = true
			case "changes":
				state.detail = &detailState{row: row, activeTab: detailTabChanges}
				ignored = true
			case "settings":
				state.page = PageSettings
				ignored = true
			case "empty":
				ignored = true
			case "wrong-key":
				key.Rune = 'X'
				ignored = true
			case "live":
				row.Liveness = monitor.LivenessLive
				wantMessage = "Plan has a live run; wait for it to exit."
			case "locked":
				row.Liveness = monitor.LivenessStale
				row.RunLockPresent = true
				row.RunLockProcessAlive = true
				wantMessage = "Plan has a live run; wait for it to exit."
			case "crashed", "crashed-merge":
				row.Status = plan.StatusInProgress
				row.Liveness = monitor.LivenessStale
				row.RunLockPresent = true
				row.RunLockProcessAlive = false
				row.AttentionReasons = []monitor.AttentionReason{monitor.AttentionRunCrashed}
				if scenario == "crashed-merge" {
					row.MergeInProgress = true
					wantMessage = "Plan is in a merge batch."
				}
			case "merge":
				row.MergeInProgress = true
				wantMessage = "Plan is in a merge batch."
			case "not-stuck":
				row.Status = plan.StatusPending
				row.NextAction = "run"
				wantMessage = "Plan is not stuck; next action: run"
			case "invalid":
				row.RepositoryRoot = ""
				wantMessage = "Plan is not actionable."
			case "unavailable":
				wantMessage = "Plan fixing is unavailable."
			case "failure":
				wantMessage = "Plan fixing failed: child failed"
			}
			if scenario != "empty" {
				state.snapshot.Rows = []monitor.Row{row}
			}
			if scenario == "detail" {
				state.snapshot.Rows[0].PlanID = "other"
			}
			terminal := &planningFaultTerminal{fakeTerminal: fakeTerminal{size: term.Size{Width: 42, Height: 15}}}
			calls := 0
			app := App{Terminal: terminal, Output: io.Discard, Actions: &Actions{}, PlanFixLauncher: planFixFunc(func(_ context.Context, got monitor.Row) error {
				calls++
				if got.PlanID != "plan" || terminal.restoreCalls != 1 {
					t.Fatalf("handoff row=%+v restores=%d", got, terminal.restoreCalls)
				}
				if scenario == "failure" {
					return errors.New("child failed")
				}
				return nil
			})}
			if scenario == "unavailable" {
				app.PlanFixLauncher = nil
			}
			handled, err := app.fixSelectedPlan(context.Background(), &state, key)
			if err != nil || handled == ignored {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			wantCall := !ignored && (wantMessage == "" || scenario == "failure")
			if (calls == 1) != wantCall {
				t.Fatalf("calls=%d", calls)
			}
			if app.Actions.statusMessage() != wantMessage {
				t.Fatalf("message=%q want=%q", app.Actions.statusMessage(), wantMessage)
			}
			if wantCall && (terminal.baselineCalls != 1 || terminal.enterCalls != 1 || state.size != terminal.size) {
				t.Fatalf("terminal=%+v size=%+v", terminal, state.size)
			}
		})
	}
}

func TestPlanFixWithoutActions(t *testing.T) {
	row := monitor.Row{PlanID: "plan", RepositoryRoot: "/repo", Status: plan.StatusBlocked}
	state := loopState{page: PagePlans, snapshot: monitor.Snapshot{Rows: []monitor.Row{row}}}
	app := App{}
	handled, err := app.fixSelectedPlan(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'x'})
	if !handled || err != nil || state.planFixFeedback.messageForRow(row) != "Plan fixing is unavailable." {
		t.Fatalf("handled=%v err=%v feedback=%q", handled, err, state.planFixFeedback.statusMessage())
	}
}

func TestPlanFixFailureCleanup(t *testing.T) {
	for _, failure := range []string{"suspend", "resume", "panic", "baseline", "error"} {
		t.Run(failure, func(t *testing.T) {
			terminal := &planningFaultTerminal{fakeTerminal: fakeTerminal{size: term.Size{Width: 80, Height: 24}}, fail: failure}
			app := App{Input: strings.NewReader("xq"), Output: io.Discard, Terminal: terminal, Ticker: &fakeTicker{}, Actions: &Actions{}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{Rows: []monitor.Row{{PlanID: "plan", RepositoryRoot: "/repo", Status: plan.StatusBlocked}}}}}, PlanFixLauncher: planFixFunc(func(context.Context, monitor.Row) error {
				if failure == "panic" {
					panic("child panic")
				}
				return errors.New("child failure")
			})}
			var err error
			var caught any
			func() { defer func() { caught = recover() }(); err = app.Run(context.Background()) }()
			if failure == "panic" && caught != "child panic" {
				t.Fatalf("panic=%v", caught)
			}
			if failure != "panic" && failure != "error" && err == nil {
				t.Fatal("missing terminal error")
			}
			if failure == "error" && (err != nil || app.Actions.statusMessage() != "Plan fixing failed: child failure") {
				t.Fatalf("err=%v message=%q", err, app.Actions.statusMessage())
			}
			if terminal.baselineCalls != 1 || terminal.restoreCalls != 2 {
				t.Fatalf("terminal=%+v", terminal)
			}
		})
	}
}
