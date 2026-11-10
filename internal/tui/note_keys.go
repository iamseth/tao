package tui

import (
	"context"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
)

func (a App) handleNoteDetailKey(ctx context.Context, state *loopState, key term.KeyEvent) bool {
	state.interruptEscape()
	item := *state.noteDetail
	switch {
	case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
		state.noteDetail = nil
		state.noteDetailOffset = 0
	case key.Key == term.KeyArrowLeft:
		state.moveAdjacentNoteDetail(-1)
	case key.Key == term.KeyArrowRight:
		state.moveAdjacentNoteDetail(1)
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		state.moveNoteDetail(-1)
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		state.moveNoteDetail(1)
	case key.Key == term.KeyPageUp:
		state.moveNoteDetail(-state.noteDetailPageStep())
	case key.Key == term.KeyPageDown:
		state.moveNoteDetail(state.noteDetailPageStep())
	case key.Key == term.KeyRune && key.Rune == 'g':
		state.noteDetailOffset = 0
	case key.Key == term.KeyRune && key.Rune == 'G':
		state.noteDetailOffset = state.noteDetailMaxOffset()
	default:
		a.handleNoteActionKey(ctx, state, item, key)
	}
	return false
}

// handleNoteActionKey acts on the caller's selected or displayed note.
func (a App) handleNoteActionKey(ctx context.Context, state *loopState, item note.CatalogNote, key term.KeyEvent) bool {
	if key.Key != term.KeyRune {
		return false
	}
	switch {
	case key.Rune == 'c':
		a.copyNoteID(ctx, state, item)
	case key.Rune == 'd':
		a.requestNoteDelete(ctx, state, item, true)
	case key.Rune == 'D':
		a.requestNoteDelete(ctx, state, item, false)
	case key.Rune >= '0' && key.Rune <= '3':
		a.setSelectedNoteTier(ctx, state, item, int(key.Rune-'0'))
	default:
		return false
	}
	return true
}
