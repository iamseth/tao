package tui

import (
	"context"

	"github.com/iamseth/tao/internal/term"
)

func (a App) handlePlanDetailKey(ctx context.Context, state *loopState, key term.KeyEvent) bool {
	state.interruptEscape()
	switch {
	case state.detail.activeTab == detailTabChanges && a.handleChangesKey(state, key):
	case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
		state.closeDetail()
	case a.Actions != nil && key.Key == term.KeyRune && (key.Rune == 'r' || key.Rune == 'R'):
		a.Actions.RunPlan(ctx, state.detail.row)
	case key.Key == term.KeyTab:
		state.detail.moveTab(1, state.size)
		a.syncDetailChanges(state)
	case key.Key == term.KeyShiftTab:
		state.detail.moveTab(-1, state.size)
		a.syncDetailChanges(state)
	case key.Key == term.KeyArrowRight:
		a.movePlanDetail(ctx, state, 1)
	case key.Key == term.KeyArrowLeft:
		a.movePlanDetail(ctx, state, -1)
	case state.detail.activeTab == detailTabSlices && key.Key == term.KeyEnter:
		if _, ok := findDetailSlice(state.detail.plan, state.detail.selectedSliceID); ok {
			state.detail.sliceOpen = true
			state.detail.sliceOffset = 0
		}
	case state.detail.activeTab == detailTabOverview && key.Key == term.KeyRune && (key.Rune == 'e' || key.Rune == 'E'):
		state.detail.scopeExpanded = !state.detail.scopeExpanded
		state.detail.clampOffsets(state.size)
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		state.detail.moveVertical(-1, state.size)
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		state.detail.moveVertical(1, state.size)
	case key.Key == term.KeyPageUp:
		state.detail.moveVertical(-state.detail.pageStep(state.size), state.size)
	case key.Key == term.KeyPageDown:
		state.detail.moveVertical(state.detail.pageStep(state.size), state.size)
	case key.Key == term.KeyRune && key.Rune == 'g':
		state.detail.jumpVertical(false, state.size)
	case key.Key == term.KeyRune && key.Rune == 'G':
		state.detail.jumpVertical(true, state.size)
	}
	return false
}
