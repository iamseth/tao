package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/term"
)

func (a App) handleListKey(ctx context.Context, state *loopState, key term.KeyEvent) bool {
	if normalizedSearchQuery(state.searchQuery) != "" && (key.Key == term.KeyEsc || key.Key == term.KeyBackspace) {
		state.clearSearch()
		return false
	}
	if (isPlanPage(state.activePage()) || state.activePage() == PageNotes) && key.Key == term.KeyRune && key.Rune == '/' {
		state.setSearchQuery("")
		state.searchActive = true
		state.interruptEscape()
		return false
	}
	if key.Key != term.KeyEsc {
		state.interruptEscape()
	}
	if state.activePage() == PageTrends && state.handleTrendsKey(key) {
		return false
	}
	if key.Key == term.KeyPageUp || key.Key == term.KeyPageDown {
		direction := 1
		if key.Key == term.KeyPageUp {
			direction = -1
		}
		state.movePage(direction)
		return false
	}
	if state.activePage() == PageSettings && key.Key == term.KeyRune && (key.Rune == 'p' || key.Rune == 'P') {
		repository, ok := state.selectedRepositorySetting()
		if !ok || a.Settings == nil {
			return false
		}
		next := nextPullRequestSetting(repository.PullRequest)
		currentLabel := pullRequestSetting(repository.PullRequest, state.settingsSnapshot.InheritedPullRequest)
		nextLabel := pullRequestSetting(next, state.settingsSnapshot.InheritedPullRequest)
		state.beginConfirm(fmt.Sprintf("Set %s pull_request from %s to %s?", rowlabel.DisplayValue(repository.Name), currentLabel, nextLabel), func(accepted bool) {
			if !accepted {
				return
			}
			if err := a.Settings.SetPullRequestDefault(ctx, repository.ID, next); err != nil {
				state.settingsMessage = "settings update failed: " + err.Error()
				return
			}
			state.settingsSnapshot = a.collectSettings(ctx)
			state.restoreSettingsSelection(repository.ID)
			state.settingsMessage = "Updated " + rowlabel.DisplayValue(repository.Name) + " pull_request to " + nextLabel + "."
		})
		return false
	}
	row, selected := state.selectedRow()
	if state.activePage() == PageNotes && key.Key == term.KeyRune {
		if item, ok := state.selectedNote(); ok && a.handleNoteActionKey(ctx, state, item, key) {
			return false
		}
	}
	if state.activePage() == PageNotes && key.Key == term.KeyEnter {
		if item, ok := state.selectedNote(); ok {
			state.noteDetail = &item
			state.noteDetailOffset = 0
		}
		return false
	}
	if isPlanPage(state.activePage()) && key.Key == term.KeyEnter && selected && row.PlanDir != "" {
		a.openDetail(ctx, state, row)
		return false
	}
	if isPlanPage(state.activePage()) && key.Key == term.KeyRune && key.Rune == 'c' {
		if selected && row.PlanDir != "" {
			a.openDetailTab(ctx, state, row, detailTabChanges)
		}
		return false
	}
	if state.activePage() == PagePlans && a.Actions != nil && key.Key == term.KeyRune {
		if key.Rune == 'M' {
			if repository, ok := state.mergeRepositoryRow(); ok {
				if message, prompt := a.Actions.MergeAllPrompt(repository); prompt {
					state.beginConfirm(message, func(accepted bool) {
						if accepted {
							a.Actions.MergeAll(ctx, repository)
						}
					})
				}
			}
			return false
		}
		if !selected {
			return state.handleKey(key)
		}
		switch key.Rune {
		case 'r', 'R':
			a.Actions.RunPlan(ctx, row)
			return false
		case 'a', 'A':
			if message, ok := a.Actions.ApprovalPrompt(row); ok {
				state.beginConfirm(message, func(accepted bool) {
					if accepted {
						a.Actions.ApproveSlice(ctx, row)
					}
				})
			}
			return false
		case 'm':
			if message, ok := a.Actions.MergePlanPrompt(row); ok {
				state.beginConfirm(message, func(accepted bool) {
					if accepted {
						a.Actions.MergePlan(ctx, row)
					}
				})
			}
			return false
		}
	}
	return state.handleKey(key)
}

func (s *loopState) handlePageKey(key term.KeyEvent) bool {
	if key.Key != term.KeyEsc {
		s.interruptEscape()
	}
	if s.activePage() == PageDebug {
		handled := true
		switch key.Key {
		case term.KeyArrowUp:
			s.debugOffset = max(s.debugOffset-1, 0)
		case term.KeyArrowDown:
			s.debugOffset = min(s.debugOffset+1, s.debugPageMaxOffset())
		case term.KeyRune:
			switch key.Rune {
			case 'k':
				s.debugOffset = max(s.debugOffset-1, 0)
			case 'j':
				s.debugOffset = min(s.debugOffset+1, s.debugPageMaxOffset())
			case 'g':
				s.debugOffset = 0
			case 'G':
				s.debugOffset = s.debugPageMaxOffset()
			default:
				handled = false
			}
		default:
			handled = false
		}
		if handled {
			return false
		}
	}
	if s.activePage() == PageSettings && s.settingsEditor == nil {
		// Global values and paths are not selectable; scroll them like the Debug
		// page. Repository selection keeps the arrow keys while repositories exist.
		handled := true
		switch {
		case key.Key == term.KeyRune && key.Rune == 'g':
			s.settingsOffset = 0
		case key.Key == term.KeyRune && key.Rune == 'G':
			s.settingsOffset = s.settingsPageMaxOffset()
		case s.pageRowCount() == 0 && (key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k')):
			s.settingsOffset = max(s.settingsOffset-1, 0)
		case s.pageRowCount() == 0 && (key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j')):
			s.settingsOffset = min(s.settingsOffset+1, s.settingsPageMaxOffset())
		default:
			handled = false
		}
		if handled {
			return false
		}
	}
	switch {
	case key.Key == term.KeyEsc:
		now := s.currentTime()
		if !s.lastRootEscape.IsZero() {
			elapsed := now.Sub(s.lastRootEscape)
			if elapsed >= 0 && elapsed <= time.Second {
				return true
			}
		}
		s.lastRootEscape = now
	case key.Key == term.KeyTab || key.Key == term.KeyArrowRight:
		s.switchPage(1)
	case key.Key == term.KeyShiftTab || key.Key == term.KeyArrowLeft:
		s.switchPage(-1)
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		if s.selected > 0 {
			s.selected--
			s.settingsOffset = 0
		}
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		if s.selected+1 < s.pageRowCount() {
			s.selected++
			s.settingsOffset = 0
		}
	case (isPlanPage(s.activePage()) || s.activePage() == PageNotes) && key.Key == term.KeyRune && key.Rune == 's':
		s.sortMenu = newSortMenu(s.activePage(), s.sorts.For(s.activePage()))
	case (isPlanPage(s.activePage()) || s.activePage() == PageNotes) && key.Key == term.KeyRune && (key.Rune == 'f' || key.Rune == 'F'):
		s.filterMenu = newPageFilterMenu(s.filter, s.snapshot, s.noteSnapshot, s.activePage())
		s.listTopPending = false
	}
	return false
}
