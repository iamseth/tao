package tui

import (
	"strings"

	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

// sortMenu owns a working copy; the caller applies and persists changes.
type sortMenu struct {
	page             PageID
	sort             Sort
	options          []SortOption
	selected, offset int
}

func newSortMenu(page PageID, sort Sort) *sortMenu {
	return &sortMenu{page: page, sort: NormalizeSort(page, sort), options: SortOptionsForPage(page)}
}

type sortMenuAction uint8

const (
	sortMenuContinue sortMenuAction = iota
	sortMenuClosed
	sortMenuQuit
)

func (m *sortMenu) handleKey(key term.KeyEvent, size term.Size) (sortMenuAction, bool) {
	m.clamp(size)
	before := m.sort
	switch {
	case key.Key == term.KeyCtrlC || quitKey(key):
		return sortMenuQuit, false
	case key.Key == term.KeyEsc || key.Key == term.KeyRune && key.Rune == 's':
		return sortMenuClosed, false
	case key.Key == term.KeyArrowUp || key.Key == term.KeyRune && key.Rune == 'k':
		m.selected--
	case key.Key == term.KeyArrowDown || key.Key == term.KeyRune && key.Rune == 'j':
		m.selected++
	case key.Key == term.KeyPageUp:
		m.selected -= sortMenuCapacity(size)
	case key.Key == term.KeyPageDown:
		m.selected += sortMenuCapacity(size)
	case key.Key == term.KeyRune && key.Rune == 'd':
		m.toggleDirection()
	case key.Key == term.KeyEnter || key.Key == term.KeyRune && key.Rune == ' ':
		if m.selected == 0 {
			m.toggleDirection()
		} else {
			option := m.options[m.selected-1]
			if m.sort.Column == option.Column {
				m.toggleDirection()
			} else {
				m.sort = Sort{Column: option.Column, Descending: option.DescendingDefault}
			}
		}
	}
	m.clamp(size)
	return sortMenuContinue, before != m.sort
}

func (m *sortMenu) toggleDirection() {
	if !m.sort.IsDefault() {
		m.sort.Descending = !m.sort.Descending
	}
}

func sortMenuCapacity(size term.Size) int { return max(1, size.Height-6) }

func (m *sortMenu) clamp(size term.Size) {
	m.selected = min(max(0, m.selected), len(m.options))
	capacity := sortMenuCapacity(size)
	m.offset = min(max(0, m.offset), max(0, len(m.options)+1-capacity))
	if m.selected < m.offset {
		m.offset = m.selected
	}
	if m.selected >= m.offset+capacity {
		m.offset = m.selected - capacity + 1
	}
}

// render uses a local viewport so resizing does not mutate the menu.
func (m sortMenu) render(size term.Size, palette theme.Palette) []string {
	if size.Width <= 0 || size.Height <= 0 {
		return nil
	}
	if size.Width < 12 || size.Height < 7 {
		return []string{cells.Truncate("[Sort]", size.Width)}
	}
	m.clamp(size)
	width := min(size.Width, 86)
	row := func(text string) string { return "│ " + cells.Pad(cells.Truncate(text, width-4), width-4) + " │" }
	horizontal := strings.Repeat("─", width-2)
	lines := []string{"┌" + horizontal + "┐", row(palette.Paint(theme.RoleNeutral5, "Sort")), "├" + horizontal + "┤"}
	direction := "ascending"
	if m.sort.Descending {
		direction = "descending"
	}
	rows := []string{"Direction: " + direction}
	for _, option := range m.options {
		marker := "( ) "
		if m.sort.Column == option.Column {
			marker = "(x) "
		}
		rows = append(rows, marker+option.Label)
	}
	for index := m.offset; index < min(len(rows), m.offset+sortMenuCapacity(size)); index++ {
		cursor := "  "
		if index == m.selected {
			cursor = "> "
		}
		line := row(cursor + rows[index])
		if index == m.selected {
			line = palette.SelectRow(line)
		}
		lines = append(lines, line)
	}
	return append(lines, "├"+horizontal+"┤", row("↑/↓ j/k PgUp/PgDn · Space/Enter select · d direction · Esc/s close · q quit"), "└"+horizontal+"┘")
}
