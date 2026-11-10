package tui

import (
	"context"

	"github.com/iamseth/tao/internal/term"
)

// handleModalKey preserves the guards that precede ordinary detail/list dispatch.
// handled distinguishes a consumed key from a request to quit.
func (a App) handleModalKey(ctx context.Context, state *loopState, key term.KeyEvent) (handled, quit bool) {
	if state.sortMenu != nil {
		action, changed := state.sortMenu.handleKey(key, state.size)
		if changed {
			state.applySort(state.sortMenu.page, state.sortMenu.sort)
			if a.SortStore != nil {
				if err := a.SortStore.Save(ctx, state.sorts); err != nil {
					state.filterMessage = "Save sorts: " + err.Error()
				} else {
					state.filterMessage = ""
				}
			}
		}
		if action == sortMenuClosed {
			state.sortMenu = nil
		}
		state.interruptEscape()
		return true, action == sortMenuQuit
	}
	if state.filterMenu != nil {
		action, change := state.filterMenu.handleKey(key, state.size)
		if action == filterMenuClosed || change.EnabledChanged || change.Cleared {
			state.applyPageFilter(state.filterMenu.filter)
			if a.FilterStore != nil {
				if err := a.FilterStore.Save(ctx, state.filter); err != nil {
					state.filterMessage = "Save filters: " + err.Error()
				} else {
					state.filterMessage = ""
				}
			}
		}
		if action == filterMenuClosed {
			state.filterMenu = nil
		}
		state.interruptEscape()
		return true, action == filterMenuQuit
	}
	if key.Key == term.KeyCtrlC {
		return true, true
	}
	if state.showShortcuts {
		switch {
		case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
			state.showShortcuts = false
			state.interruptEscape()
		case key.Key == term.KeyRune && key.Rune == '?':
			state.showShortcuts = false
			state.interruptEscape()
		case quitKey(key):
			return true, true
		}
		return true, false
	}
	if state.confirm != nil {
		return true, state.handleKey(key)
	}
	if state.settingsEditor != nil {
		a.handleSettingsEditor(ctx, state, key)
		return true, false
	}
	if state.detail == nil && state.noteDetail == nil && !state.searchActive && key.Key == term.KeyRune && key.Rune == 'S' {
		state.page = PageSettings
		state.selected = 0
		state.settingsSnapshot = a.collectSettings(ctx)
		return true, false
	}
	if state.activePage() == PageSettings && key.Key == term.KeyRune && key.Rune == 'e' {
		if repo, ok := state.selectedRepositorySetting(); ok {
			state.settingsEditor = &settingsEditorState{repositoryID: repo.ID}
		}
		return true, false
	}
	if state.searchActive {
		state.handleSearchKey(key)
		return true, false
	}
	return false, false
}

func (s *loopState) handleConfirmKey(key term.KeyEvent) {
	switch {
	case key.Key == term.KeyRune && (key.Rune == 'y' || key.Rune == 'Y'):
		s.resolveConfirm(true)
	case key.Key == term.KeyRune && (key.Rune == 'n' || key.Rune == 'N'):
		s.resolveConfirm(false)
	case key.Key == term.KeyEsc || key.Key == term.KeyBackspace || quitKey(key):
		s.resolveConfirm(false)
	}
}
