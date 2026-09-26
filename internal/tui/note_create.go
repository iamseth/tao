package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

// NoteCreator composes and persists a new note for one exact repository ID.
// A false saved result means composition was cancelled without persistence.
type NoteCreator interface {
	Create(context.Context, string) (note.CatalogNote, bool, error)
}

// NoteRepositoryLister supplies metadata-only destinations, including repositories
// without notes or healthy checkouts.
type NoteRepositoryLister interface {
	ListNoteRepositories(context.Context) ([]NoteRepository, error)
}

// handleNoteCreation runs only while the input reader is paused. Picker keys
// never reach ordinary dashboard actions, including the edit shortcut.
func (a App) handleNoteCreation(ctx context.Context, state *loopState, key term.KeyEvent) (handled, quit bool, err error) {
	var target NoteRepository
	if state.notePicker != nil {
		action, selected := state.notePicker.handleKey(key, state.size)
		switch action {
		case notePickerQuit:
			return true, true, nil
		case notePickerCancelled:
			state.notePicker = nil
			state.setNoteEditMessage("Note creation cancelled.")
			return true, false, nil
		case notePickerSelected:
			state.notePicker = nil
			target = selected
		default:
			return true, false, nil
		}
	} else {
		if key.Key != term.KeyRune || key.Rune != 'n' || state.activePage() != PageNotes ||
			state.showShortcuts || state.searchActive || state.confirm != nil || state.detail != nil || state.noteDetail != nil || state.filterMenu != nil {
			return false, false, nil
		}
		state.interruptEscape()
		state.listTopPending = false
		if a.NoteCreator == nil || a.NoteRepositories == nil {
			state.setNoteEditMessage("Note creation is unavailable.")
			return true, false, nil
		}
		repositories, inventoryErr := a.NoteRepositories.ListNoteRepositories(ctx)
		var picker *noteRepositoryPicker
		target, picker, err = noteCreationTarget(state.filter, repositories, inventoryErr)
		if err != nil {
			state.setNoteEditMessage("Note creation unavailable: " + err.Error())
			return true, false, nil //nolint:nilerr // Routing failures are nonfatal dashboard feedback.
		}
		if picker != nil {
			state.notePicker = picker
			return true, false, nil
		}
	}
	return true, false, a.createNote(ctx, state, target)
}

func (a App) createNote(ctx context.Context, state *loopState, target NoteRepository) error {
	if a.NoteCreator == nil {
		state.setNoteEditMessage("Note creation is unavailable.")
		return nil
	}
	if err := restoreTerminalState(a.Terminal, a.Output); err != nil {
		return fmt.Errorf("suspend dashboard for note editor: %w", err)
	}
	created, saved, createErr := a.NoteCreator.Create(ctx, target.ID)
	if err := enterTerminalState(a.Terminal, a.Output); err != nil {
		return fmt.Errorf("resume dashboard after note editor: %w", err)
	}
	if createErr != nil {
		state.setNoteEditMessage("Note creation failed: " + boundedNoteValue(createErr.Error(), 240))
		return nil
	}
	if !saved {
		state.setNoteEditMessage("Note creation cancelled.")
		return nil
	}
	message := "Created note " + singleLineNoteValue(created.ID) + "."
	refreshed, err := a.collectNotes(ctx)
	if err != nil {
		state.setNoteEditMessage(message + " Refresh failed: " + boundedNoteValue(err.Error(), 200))
		return nil
	}
	state.replaceNoteSnapshot(refreshed)
	for _, item := range state.visibleNotes() {
		if noteIdentity(item) == noteIdentity(created) {
			state.restoreNoteSelection(created, true)
			state.setNoteEditMessage(message)
			return nil
		}
	}
	state.setNoteEditMessage(message + " Not visible under current filters; clear search or filters.")
	return nil
}

// NoteRepository is a registered note destination, independent of note rows
// and execution health. The inventory producer supplies canonical, unique IDs.
type NoteRepository struct {
	ID   string
	Name string
}

// noteCreationTarget never infers a destination from CWD or a selected note.
// Even a singleton inventory requires a picker without one enabled repository.
func noteCreationTarget(filter Filter, repositories []NoteRepository, inventoryErr error) (NoteRepository, *noteRepositoryPicker, error) {
	if inventoryErr != nil {
		return NoteRepository{}, nil, errors.New("repository inventory unavailable; retry or inspect tao repo list")
	}
	if filter.Enabled && len(filter.Repositories) == 1 {
		for _, repository := range repositories {
			if repository.ID == filter.Repositories[0] {
				return repository, nil, nil
			}
		}
		return NoteRepository{}, nil, errors.New("filtered repository is no longer available; clear repository filters and try again")
	}
	if len(repositories) == 0 {
		return NoteRepository{}, nil, errors.New("no registered repositories; register a repository with tao init before creating a note")
	}
	items := append([]NoteRepository(nil), repositories...)
	sort.Slice(items, func(i, j int) bool {
		left, right := noteRepositoryName(items[i]), noteRepositoryName(items[j])
		if left != right {
			return left < right
		}
		return items[i].ID < items[j].ID
	})
	return NoteRepository{}, &noteRepositoryPicker{items: items}, nil
}

type noteRepositoryPicker struct {
	items    []NoteRepository
	selected int
	offset   int
}

type notePickerAction uint8

const (
	notePickerContinue notePickerAction = iota
	notePickerSelected
	notePickerCancelled
	notePickerQuit
)

// handleKey reports quit separately from cancellation so the eventual modal
// dispatcher preserves q/Q and Ctrl+C as global quit keys.
func (p *noteRepositoryPicker) handleKey(key term.KeyEvent, size term.Size) (notePickerAction, NoteRepository) {
	p.clamp(size)
	switch {
	case key.Key == term.KeyCtrlC || quitKey(key):
		return notePickerQuit, NoteRepository{}
	case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
		return notePickerCancelled, NoteRepository{}
	case key.Key == term.KeyEnter:
		if len(p.items) > 0 {
			return notePickerSelected, p.items[p.selected]
		}
	case key.Key == term.KeyArrowUp || (key.Key == term.KeyRune && key.Rune == 'k'):
		p.selected--
	case key.Key == term.KeyArrowDown || (key.Key == term.KeyRune && key.Rune == 'j'):
		p.selected++
	case key.Key == term.KeyPageUp:
		p.selected -= notePickerCapacity(size)
	case key.Key == term.KeyPageDown:
		p.selected += notePickerCapacity(size)
	}
	p.clamp(size)
	return notePickerContinue, NoteRepository{}
}

func notePickerCompact(size term.Size) bool {
	return size.Width < 16 || size.Height < 5
}

func notePickerCapacity(size term.Size) int {
	if notePickerCompact(size) {
		return max(1, size.Height-2)
	}
	return max(1, size.Height-4)
}

func (p *noteRepositoryPicker) clamp(size term.Size) {
	p.selected = min(max(0, p.selected), max(0, len(p.items)-1))
	capacity := notePickerCapacity(size)
	p.offset = min(max(0, p.offset), max(0, len(p.items)-capacity))
	if p.selected < p.offset {
		p.offset = p.selected
	}
	if p.selected >= p.offset+capacity {
		p.offset = p.selected - capacity + 1
	}
}

func noteRepositoryName(repository NoteRepository) string {
	return boundedNoteValue(repository.Name, 32)
}

func noteRepositoryLabel(repository NoteRepository, width int) string {
	id := "[" + singleLineNoteValue(repository.ID) + "]"
	// Reserve space for the distinguishing ID before spending cells on a name.
	nameWidth := width - cells.Width(id) - 1
	if nameWidth <= 0 {
		return cells.TruncateEllipsis(id, width)
	}
	return cells.TruncateEllipsis(noteRepositoryName(repository), nameWidth) + " " + id
}

// render is a pure, bounded modal body. Resizes keep the selected destination
// visible without changing it; the live loop can center these lines as an overlay.
func (p noteRepositoryPicker) render(size term.Size, profile Profile) []string {
	if size.Width <= 0 || size.Height <= 0 {
		return nil
	}
	p.clamp(size)
	compact := notePickerCompact(size)
	width := size.Width
	if !compact {
		width -= 4
	}
	row := func(text string) string {
		text = cells.Pad(cells.Truncate(text, width), width)
		if compact {
			return text
		}
		return "│ " + text + " │"
	}
	var lines []string
	if !compact {
		lines = append(lines, "┌"+strings.Repeat("─", size.Width-2)+"┐")
	}
	if !compact || size.Height >= 3 {
		lines = append(lines, row("Choose note repository"))
	}
	if len(p.items) == 0 {
		lines = append(lines, row("No repositories; run tao init"))
	}
	for index := p.offset; index < min(len(p.items), p.offset+notePickerCapacity(size)); index++ {
		cursor := "  "
		if index == p.selected {
			cursor = "> "
		}
		line := row(cursor + noteRepositoryLabel(p.items[index], max(0, width-2)))
		if index == p.selected {
			line = SelectRow(profile, line)
		}
		lines = append(lines, line)
	}
	if !compact || size.Height >= 2 {
		position := 0
		if len(p.items) > 0 {
			position = p.selected + 1
		}
		lines = append(lines, row(fmt.Sprintf("%d/%d · ↑/↓ j/k · Enter choose · Esc cancel · q quit", position, len(p.items))))
	}
	if !compact {
		lines = append(lines, "└"+strings.Repeat("─", size.Width-2)+"┘")
	}
	return lines
}
