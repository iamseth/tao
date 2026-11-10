package tui

import (
	"context"

	"github.com/iamseth/tao/internal/term"
)

// dispatchInput routes a key while the input reader is parked. Run owns redraw,
// reader resumption and exit; foreground handlers own terminal handoff.
func (a App) dispatchInput(ctx context.Context, state *loopState, key term.KeyEvent) (quit bool, err error) {
	// List menus consume input before foreground launchers can act on it.
	if state.sortMenu != nil || state.filterMenu != nil {
		return a.handleKey(ctx, state, key), nil
	}
	if handled, quit, err := a.handleNoteCreation(ctx, state, key); err != nil || quit || handled {
		return quit, err
	}
	if handled, err := a.planSelectedNote(ctx, state, key); err != nil {
		return false, err
	} else if handled {
		return ctx.Err() != nil, nil
	}
	if handled, err := a.fixSelectedPlan(ctx, state, key); err != nil {
		return false, err
	} else if handled {
		return ctx.Err() != nil, nil
	}
	if key.Key == term.KeyCtrlG {
		if handled, err := a.editSelectedNote(ctx, state); err != nil || handled {
			return false, err
		}
	}
	previousPage := state.activePage()
	if a.handleKey(ctx, state, key) {
		return true, nil
	}
	if previousPage != state.activePage() {
		a.refreshTrends(ctx, state)
	}
	return false, nil
}
