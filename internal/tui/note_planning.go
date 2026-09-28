package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

// NotePlanningLauncher runs an interactive planning agent synchronously. The
// caller owns terminal handoff; returning conveys no note or plan lifecycle state.
type NotePlanningLauncher interface {
	Launch(context.Context, note.CatalogNote) error
}

// Called only while readInput is parked on its resume handshake. No collector
// or lifecycle mutation runs here: the configured refresh discovers any changes.
func (a App) planSelectedNote(ctx context.Context, state *loopState, key term.KeyEvent) (bool, error) {
	if key.Key != term.KeyRune || key.Rune != 'p' || state.showShortcuts || state.searchActive || state.confirm != nil || state.detail != nil || state.notePicker != nil || state.filterMenu != nil {
		return false, nil
	}
	var item note.CatalogNote
	var ok bool
	if state.noteDetail != nil {
		item, ok = *state.noteDetail, true
	} else if state.activePage() == PageNotes {
		item, ok = state.selectedNote()
	}
	if !ok {
		return false, nil
	}
	state.setNoteEditMessage("")
	if a.NotePlanningLauncher == nil {
		state.setNoteEditMessage("Note planning is unavailable.")
		return true, nil
	}
	launchErr, restoreErr := a.launchNotePlanning(ctx, item)
	if restoreErr != nil {
		return true, restoreErr
	}
	if err := enterTerminalState(a.Terminal, a.Output); err != nil {
		return true, fmt.Errorf("resume dashboard after note planning: %w", err)
	}
	size, err := a.Terminal.Size()
	if err != nil {
		return true, fmt.Errorf("read terminal size after note planning: %w", err)
	}
	state.size = size
	state.clampNoteDetailOffset()
	if launchErr != nil {
		state.setNoteEditMessage("Note planning failed: " + cells.Truncate(singleLineDetail(launchErr.Error()), 240))
	}
	return true, nil
}

func (a App) launchNotePlanning(ctx context.Context, item note.CatalogNote) (launchErr, terminalErr error) {
	// Restore clears the terminal's saved state. Keep an independent baseline
	// so a killed or panicking child cannot supply the next EnterRaw baseline.
	restoreBaseline := a.Terminal.PreserveBaseline()
	defer func() {
		if err := restoreBaseline(); err != nil {
			terminalErr = errors.Join(terminalErr, fmt.Errorf("restore terminal after note planning: %w", err))
		}
	}()
	if err := restoreTerminalState(a.Terminal, a.Output); err != nil {
		return nil, fmt.Errorf("suspend dashboard for note planning: %w", err)
	}
	return a.NotePlanningLauncher.Launch(ctx, item), nil
}
