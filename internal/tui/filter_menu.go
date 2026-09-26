package tui

import (
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

// filterMenu owns a working copy; closing and persistence are the caller's job.
// selected indexes selectable rows, while offset indexes rendered content rows
// (including section headings).
type filterMenu struct {
	filter       Filter
	repositories []FilterOption
	statuses     []FilterOption
	tags         []FilterOption
	selected     int
	offset       int
}

func newFilterMenu(filter Filter, plans monitor.Snapshot, notes note.Snapshot) *filterMenu {
	filter.Repositories = slices.Clone(filter.Repositories)
	filter.Statuses = slices.Clone(filter.Statuses)
	filter.Tags = slices.Clone(filter.Tags)
	return &filterMenu{
		filter:       filter,
		repositories: DiscoverRepositories(plans, notes, filter.Repositories),
		statuses:     DiscoverStatuses(plans, filter.Statuses),
		tags:         DiscoverTags(notes, filter.Tags),
	}
}

type filterMenuAction uint8

const (
	filterMenuContinue filterMenuAction = iota
	filterMenuClosed
	filterMenuQuit
)

// filterMenuChange describes only the last key. These actions can be persisted
// immediately, independently of when ordinary criterion edits are applied.
type filterMenuChange struct {
	EnabledChanged bool
	Cleared        bool
}

func (m *filterMenu) handleKey(key term.KeyEvent, size term.Size) (filterMenuAction, filterMenuChange) {
	m.clamp(size)
	var change filterMenuChange
	switch {
	case key.Key == term.KeyCtrlC || quitKey(key):
		return filterMenuQuit, change
	case key.Key == term.KeyEsc || key.Key == term.KeyRune && key.Rune == 'f':
		return filterMenuClosed, change
	case key.Key == term.KeyArrowUp || key.Key == term.KeyRune && key.Rune == 'k':
		m.selected--
	case key.Key == term.KeyArrowDown || key.Key == term.KeyRune && key.Rune == 'j':
		m.selected++
	case key.Key == term.KeyPageUp:
		m.selected -= filterMenuCapacity(size)
	case key.Key == term.KeyPageDown:
		m.selected += filterMenuCapacity(size)
	case key.Key == term.KeyRune && key.Rune == 't':
		m.filter.Enabled = !m.filter.Enabled
		change.EnabledChanged = true
	case key.Key == term.KeyRune && key.Rune == 'c':
		m.filter.Clear()
		change.Cleared = true
	case key.Key == term.KeyEnter || key.Key == term.KeyRune && key.Rune == ' ':
		switch m.selected {
		case 0:
			m.filter.Enabled = !m.filter.Enabled
			change.EnabledChanged = true
		case m.optionCount() + 1:
			m.filter.Clear()
			change.Cleared = true
		default:
			index := m.selected - 1
			switch {
			case index < len(m.repositories):
				toggleFilterValue(&m.filter.Repositories, m.repositories[index].ID)
			case index < len(m.repositories)+len(m.statuses):
				toggleFilterValue(&m.filter.Statuses, m.statuses[index-len(m.repositories)].ID)
			default:
				toggleFilterValue(&m.filter.Tags, m.tags[index-len(m.repositories)-len(m.statuses)].ID)
			}
		}
	}
	m.clamp(size)
	return filterMenuContinue, change
}

func toggleFilterValue(values *[]string, value string) {
	if slices.Contains(*values, value) {
		*values = slices.DeleteFunc(*values, func(item string) bool { return item == value })
	} else {
		*values = append(*values, value)
	}
}

func (m filterMenu) optionCount() int {
	return len(m.repositories) + len(m.statuses) + len(m.tags)
}

func filterMenuCapacity(size term.Size) int {
	return max(1, size.Height-6)
}

func (m *filterMenu) clamp(size term.Size) {
	m.selected = min(max(0, m.selected), m.optionCount()+1)
	rows := m.rows()
	capacity := filterMenuCapacity(size)
	m.offset = min(max(0, m.offset), max(0, len(rows)-capacity))
	for index, row := range rows {
		if row.selection != m.selected {
			continue
		}
		if index < m.offset {
			m.offset = index
		}
		if index >= m.offset+capacity {
			m.offset = index - capacity + 1
		}
		break
	}
}

type filterMenuRow struct {
	text      string
	selection int // -1 for nonselectable section headings
}

func (m filterMenu) rows() []filterMenuRow {
	status := "off"
	if m.filter.Enabled {
		status = "on"
	}
	rows := []filterMenuRow{{text: "Filter on/off: " + status, selection: 0}}
	selection := 1
	for _, section := range []struct {
		name    string
		options []FilterOption
		values  []string
	}{
		{"Repositories", m.repositories, m.filter.Repositories},
		{"Statuses", m.statuses, m.filter.Statuses},
		{"Tags", m.tags, m.filter.Tags},
	} {
		heading := section.name
		if len(section.options) == 0 {
			heading += " (none)"
		}
		rows = append(rows, filterMenuRow{text: heading, selection: -1})
		for _, option := range section.options {
			check := "[ ] "
			if slices.Contains(section.values, option.ID) {
				check = "[x] "
			}
			name := option.Name
			if name == "" {
				name = option.ID
			}
			label := singleLineNoteValue(name)
			if section.name == "Repositories" && name != option.ID {
				label += " [" + singleLineNoteValue(option.ID) + "]"
			}
			if !option.Available {
				label += " (unavailable)"
			}
			rows = append(rows, filterMenuRow{text: check + label, selection: selection})
			selection++
		}
	}
	return append(rows, filterMenuRow{text: "Clear all", selection: selection})
}

// render is pure: resizing adjusts a local viewport, not the working filter or
// cursor. The caller can place these lines over the current list with overlayBox.
func (m filterMenu) render(size term.Size, profile Profile) []string {
	if size.Width <= 0 || size.Height <= 0 {
		return nil
	}
	if size.Width < 12 || size.Height < 7 {
		return []string{cells.Truncate("[Filters]", size.Width)}
	}
	m.clamp(size)
	width := min(size.Width, 86)
	row := func(text string) string {
		return "│ " + cells.Pad(cells.Truncate(text, width-4), width-4) + " │"
	}
	horizontal := strings.Repeat("─", width-2)
	lines := []string{
		"┌" + horizontal + "┐",
		row(Paint(profile, RoleNeutral5, "Filters")),
		"├" + horizontal + "┤",
	}
	rows := m.rows()
	for _, item := range rows[m.offset:min(len(rows), m.offset+filterMenuCapacity(size))] {
		cursor := "  "
		if item.selection == m.selected {
			cursor = "> "
		}
		line := row(cursor + item.text)
		if item.selection == m.selected {
			line = SelectRow(profile, line)
		}
		lines = append(lines, line)
	}
	return append(lines,
		"├"+horizontal+"┤",
		row("↑/↓ j/k PgUp/PgDn · Space/Enter select · t on/off · c clear · Esc/f close · q quit"),
		"└"+horizontal+"┘",
	)
}
