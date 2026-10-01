package tui

import "github.com/iamseth/tao/internal/term"

func (a App) handleChangesKey(s *loopState, key term.KeyEvent) bool {
	d := s.detail
	m := &d.changes
	if m.Focus == "" {
		m.Focus = DetailChangesFocusFiles
	}
	diff := m.Focus == DetailChangesFocusDiff
	g := changesLayout(s.size.Width, max(s.size.Height-planDetailFixedLines, 0), m.Zoom)
	delta := 0
	jump := 0
	switch {
	case key.Key == term.KeyEsc || key.Key == term.KeyBackspace:
		if !diff {
			return false
		}
		m.Focus = DetailChangesFocusFiles
		m.Zoom = false
		return true
	case key.Key == term.KeyEnter || key.Key == term.KeyRune && key.Rune == 'l':
		if !diff {
			m.Focus = DetailChangesFocusDiff
			a.startDetailFileDiff(s, true)
		}
		return true
	case key.Key == term.KeyRune && key.Rune == 'h':
		m.Focus = DetailChangesFocusFiles
		m.Zoom = false
		return true
	case key.Key == term.KeyRune && key.Rune == 'u':
		d.changesForce = true
		a.syncDetailChanges(s)
		return true
	case key.Key == term.KeyRune && key.Rune == 'w':
		if m.Snapshot.WorktreeMissing {
			m.RefreshError = "worktree missing; branch delta only"
			return true
		}
		m.Pinned = false
		d.requestedScope = DetailChangesScopeWorktree
		if m.Snapshot.Scope == DetailChangesScopeWorktree {
			d.requestedScope = DetailChangesScopeBranch
		}
		d.changesForce = true
		a.syncDetailChanges(s)
		return true
	case key.Key == term.KeyRune && key.Rune == 'z':
		if changesLayout(s.size.Width, max(s.size.Height-planDetailFixedLines, 0), false).Wide {
			m.Zoom = !m.Zoom
			m.Focus = DetailChangesFocusDiff
			d.clampOffsets(s.size)
		}
		return true
	case key.Key == term.KeyRune && (key.Rune == 'n' || key.Rune == 'p'):
		delta = 1
		if key.Rune == 'p' {
			delta = -1
		}
		a.moveChangesFile(s, delta)
		return true
	case key.Key == term.KeyRune && (key.Rune == '[' || key.Rune == ']'):
		if diff {
			m.Pinned = false
			if key.Rune == ']' {
				for _, h := range m.Diff.Hunks {
					if h > m.DiffOffset {
						m.DiffOffset = h
						break
					}
				}
			} else {
				for i := len(m.Diff.Hunks) - 1; i >= 0; i-- {
					if m.Diff.Hunks[i] < m.DiffOffset {
						m.DiffOffset = m.Diff.Hunks[i]
						break
					}
				}
			}
			d.clampOffsets(s.size)
		}
		return true
	case key.Key == term.KeyArrowUp || key.Key == term.KeyRune && key.Rune == 'k':
		delta = -1
	case key.Key == term.KeyArrowDown || key.Key == term.KeyRune && key.Rune == 'j':
		delta = 1
	case key.Key == term.KeyPageUp:
		delta = -max(g.Rows-1, 1)
	case key.Key == term.KeyPageDown:
		delta = max(g.Rows-1, 1)
	case key.Key == term.KeyRune && key.Rune == 'g':
		jump = -1
	case key.Key == term.KeyRune && key.Rune == 'G':
		jump = 1
	default:
		return false
	}
	if !diff {
		if jump < 0 {
			delta = -len(m.Snapshot.Files)
		} else if jump > 0 {
			delta = len(m.Snapshot.Files)
		}
		a.moveChangesFile(s, delta)
	} else {
		limit := d.maxOffset(detailTabChanges, s.size)
		switch {
		case jump < 0:
			m.DiffOffset = 0
		case jump > 0:
			m.DiffOffset = limit
		default:
			m.DiffOffset = max(0, min(limit, m.DiffOffset+delta))
		}
		m.Pinned = jump > 0 || limit > 0 && m.DiffOffset == limit && delta > 0
	}
	return true
}
func (a App) moveChangesFile(s *loopState, delta int) {
	d := s.detail
	m := &d.changes
	m.Pinned = false
	old := m.FileIndex
	m.FileIndex = max(0, min(len(m.Snapshot.Files)-1, old+delta))
	g := changesLayout(s.size.Width, max(s.size.Height-planDetailFixedLines, 0), m.Zoom)
	if m.FileIndex < m.ListOffset {
		m.ListOffset = m.FileIndex
	}
	if m.FileIndex >= m.ListOffset+g.Rows {
		m.ListOffset = max(0, m.FileIndex-g.Rows+1)
	}
	if old != m.FileIndex {
		a.startDetailFileDiff(s, false)
	}
}
