package tui

import "github.com/iamseth/tao/internal/term"

func (a App) handleSliceDetailKey(state *loopState, key term.KeyEvent) bool {
	state.interruptEscape()
	switch {
	case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
		state.detail.sliceOpen = false
		state.detail.sliceOffset = 0
	case key.Key == term.KeyArrowRight:
		state.detail.moveSlice(1)
	case key.Key == term.KeyArrowLeft:
		state.detail.moveSlice(-1)
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		state.detail.moveSliceDetail(-1, state.size)
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		state.detail.moveSliceDetail(1, state.size)
	case key.Key == term.KeyPageUp:
		state.detail.moveSliceDetail(-detailPageStep(state.size, 6), state.size)
	case key.Key == term.KeyPageDown:
		state.detail.moveSliceDetail(detailPageStep(state.size, 6), state.size)
	case key.Key == term.KeyRune && key.Rune == 'g':
		state.detail.sliceOffset = 0
	case key.Key == term.KeyRune && key.Rune == 'G':
		state.detail.sliceOffset = state.detail.sliceMaxOffset(state.size)
	}
	return false
}
