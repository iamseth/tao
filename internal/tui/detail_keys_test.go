package tui

import (
	"context"
	"reflect"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
)

func TestDetailKeyIsolation(t *testing.T) {
	for _, sliceOpen := range []bool{false, true} {
		t.Run(map[bool]string{false: "plan", true: "slice"}[sliceOpen], func(t *testing.T) {
			launcher := &recordingActionLauncher{}
			app := App{Actions: newTestActions(t, launcher, func(string) (plan.RunLock, error) { return plan.RunLock{}, nil }, nil)}
			d := &detailState{row: testActionRow(), plan: &plan.PlanDetail{}, activeTab: detailTabOverview, sliceOpen: sliceOpen, sliceOffset: 3, selectedSliceID: "selected"}
			state := loopState{detail: d, snapshot: monitor.Snapshot{Rows: []monitor.Row{{PlanID: "different-list-selection"}}}, size: term.Size{Width: 80, Height: 24}}
			press := func(k term.KeyEvent) {
				t.Helper()
				if app.handleKey(context.Background(), &state, k) {
					t.Fatal("unexpected quit")
				}
			}
			for _, r := range []rune{'/', 'a', 'm', 'S', 'X'} {
				before := *d
				press(term.KeyEvent{Key: term.KeyRune, Rune: r})
				if !reflect.DeepEqual(before, *d) || state.searchActive || state.confirm != nil {
					t.Fatalf("unknown detail key %q escaped", r)
				}
			}
			press(term.KeyEvent{Key: term.KeyRune, Rune: '?'})
			press(term.KeyEvent{Key: term.KeyArrowRight})
			press(term.KeyEvent{Key: term.KeyRune, Rune: 'r'})
			if !state.showShortcuts || state.detail != d || len(launcher.calls) != 0 {
				t.Fatal("overlay leaked input")
			}
			press(term.KeyEvent{Key: term.KeyBackspace})
			if state.showShortcuts || d.sliceOpen != sliceOpen {
				t.Fatal("overlay dismissal changed nesting")
			}
			press(term.KeyEvent{Key: term.KeyRune, Rune: 'r'})
			if sliceOpen {
				before := *d
				for _, key := range []term.KeyEvent{{Key: term.KeyTab}, {Key: term.KeyShiftTab}, {Key: term.KeyEnter}, {Key: term.KeyRune, Rune: 'R'}, {Key: term.KeyRune, Rune: 'e'}, {Key: term.KeyRune, Rune: 'E'}} {
					press(key)
				}
				if !reflect.DeepEqual(before, *d) || len(launcher.calls) != 0 {
					t.Fatal("slice accepted parent keys")
				}
			} else if len(launcher.calls) != 1 || !reflect.DeepEqual(launcher.calls[0].Args, []string{"run", d.row.PlanID}) {
				t.Fatalf("run did not target displayed plan: %+v", launcher.calls)
			}
			for _, key := range []term.Key{term.KeyEsc, term.KeyBackspace} {
				state.detail = d
				d.sliceOpen = true
				d.sliceOffset = 3
				press(term.KeyEvent{Key: key})
				if state.detail != d || d.sliceOpen || d.sliceOffset != 0 || d.selectedSliceID != "selected" {
					t.Fatal("nested close lost parent selection")
				}
				press(term.KeyEvent{Key: key})
				if state.detail != nil {
					t.Fatal("parent did not close")
				}
			}
		})
	}
}

func TestDetailChangesKeyPrecedence(t *testing.T) {
	for _, key := range []term.Key{term.KeyEsc, term.KeyBackspace} {
		d := &detailState{plan: &plan.PlanDetail{}, activeTab: detailTabChanges}
		d.changes.Focus = DetailChangesFocusDiff
		d.changes.Zoom = true
		state := loopState{detail: d, size: term.Size{Width: 80, Height: 24}}
		app := App{}
		press := func(k term.KeyEvent) {
			t.Helper()
			if app.handleKey(context.Background(), &state, k) {
				t.Fatal("unexpected quit")
			}
		}
		press(term.KeyEvent{Key: key})
		if state.detail != d || d.changes.Focus != DetailChangesFocusFiles || d.changes.Zoom {
			t.Fatal("Changes did not consume nested escape")
		}
		press(term.KeyEvent{Key: term.KeyRune, Rune: 'h'})
		if state.detail != d {
			t.Fatal("Changes key escaped")
		}
		press(term.KeyEvent{Key: key})
		if state.detail != nil {
			t.Fatal("Changes did not fall back to parent escape")
		}
		state.detail = d
		d.sliceOpen = true
		d.changes.Focus = DetailChangesFocusDiff
		press(term.KeyEvent{Key: key})
		if d.sliceOpen || d.changes.Focus != DetailChangesFocusDiff {
			t.Fatal("Changes consumed slice escape")
		}
		press(term.KeyEvent{Key: term.KeyTab})
		if d.activeTab == detailTabChanges {
			t.Fatal("Changes swallowed tab fallback")
		}
	}
}
