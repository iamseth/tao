package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

type changesGeometry struct {
	Width, Height, ListWidth, DiffWidth, Rows int
	Wide                                      bool
}

func changesLayout(width, height int, zoom bool) changesGeometry {
	if width <= 0 {
		width = 80
	}
	g := changesGeometry{Width: max(width-4, 0), Height: max(height, 0), Rows: max(height-3, 0)}
	g.Wide = g.Width >= 96 && !zoom
	g.ListWidth = g.Width
	g.DiffWidth = g.Width
	if g.Wide {
		g.ListWidth = max(24, min(g.Width/3, 48))
		g.DiffWidth = max(g.Width-g.ListWidth-1, 0)
	}
	return g
}

// Render only cached, safe visible windows. No I/O, parsing or whole-diff work
// belongs here: activity log repaints must stay independent of patch size.
func renderChangesPane(m DetailChangesModel, width, height int, p theme.Palette, now time.Time) []string {
	g := changesLayout(width, height, m.Zoom)
	if g.Height == 0 {
		return nil
	}
	status := changesStatus(m)
	truncation := ""
	if m.Snapshot.FilesTruncated {
		truncation = "Files truncated | "
	}
	if g.Height < 4 || g.Width < 20 {
		return []string{cells.Truncate(truncation+status, g.Width)}
	}
	s := m.Snapshot
	scope := s.Scope
	if scope == "" {
		scope = m.Scope
	}
	if scope == "" {
		scope = "branch"
	}
	strip := truncation + scope + fmt.Sprintf(" | %d files +%d -%d", s.Total.Files, s.Total.Added, s.Total.Deleted)
	updated := m.UpdatedAt
	if updated.IsZero() {
		updated = s.CollectedAt
	}
	if m.Updating {
		strip += " | updating"
	} else if !now.IsZero() && !updated.IsZero() {
		strip += " | " + rowlabel.DurationLabel(max(now.Sub(updated), 0)) + " ago"
	}
	if s.Base.DefaultBranch != "" {
		strip += " | default " + s.Base.DefaultBranch
	}
	if g.Width >= 60 {
		strip += " | base " + shortChangesSHA(s.Base.SHA) + " (" + s.Base.Source + ") head " + shortChangesSHA(s.Head)
	}
	if s.Uncommitted.Files > 0 {
		strip += fmt.Sprintf(" | * %d +%d -%d", s.Uncommitted.Files, s.Uncommitted.Added, s.Uncommitted.Deleted)
	}
	badges := changesBadges(s)
	if m.RefreshError != "" {
		badges = "refresh failed: " + m.RefreshError + " | " + badges
	}
	lines := []string{cells.Truncate(strip, g.Width), cells.Truncate(badges, g.Width)}
	if m.Status != "ready" && s.Availability != "ready" {
		return append(lines, cells.Truncate(status, g.Width))
	}
	if len(s.Files) == 0 {
		return append(lines, cells.Truncate("No changes in this scope.", g.Width))
	}
	fileHeader := "FILES"
	diffHeader := "DIFF"
	if m.Focus == DetailChangesFocusDiff {
		diffHeader = "> DIFF"
	} else {
		fileHeader = "> FILES"
	}
	if len(m.Diff.Hunks) > 0 {
		k := sort.SearchInts(m.Diff.Hunks, max(m.DiffOffset, 0)+1)
		diffHeader += fmt.Sprintf(" hunk %d/%d", max(k, 1), len(m.Diff.Hunks))
	}
	if m.Updating || m.DiffStatus == "loading" {
		diffHeader += " updating"
	}
	if m.DiffStatus == "error" {
		diffHeader += " failed"
	}
	if m.Diff.Path != "" && m.Diff.Signature != s.Signature {
		diffHeader += " stale"
	}
	showDiff := m.Focus == DetailChangesFocusDiff || m.Zoom
	switch {
	case g.Wide:
		lines = append(lines, changesPad(fileHeader, g.ListWidth)+"│"+cells.Truncate(diffHeader, g.DiffWidth))
	case showDiff:
		lines = append(lines, diffHeader)
	default:
		lines = append(lines, fileHeader)
	}
	for row := 0; row < g.Rows; row++ {
		switch {
		case g.Wide:
			lines = append(lines, changesPad(changesFileRow(m, max(m.ListOffset, 0)+row, g.ListWidth), g.ListWidth)+"│"+changesDiffRow(m, max(m.DiffOffset, 0)+row, g.DiffWidth, p))
		case showDiff:
			lines = append(lines, changesDiffRow(m, max(m.DiffOffset, 0)+row, g.Width, p))
		default:
			lines = append(lines, changesFileRow(m, max(m.ListOffset, 0)+row, g.Width))
		}
	}
	for i := range lines {
		lines[i] = cells.Truncate(lines[i], g.Width)
	}
	return lines
}
func shortChangesSHA(s string) string {
	if s == "" {
		return "-"
	}
	return cells.Truncate(s, 8)
}
func changesStatus(m DetailChangesModel) string {
	if m.RefreshError != "" {
		return "Changes error: " + m.RefreshError
	}
	switch m.Status {
	case "loading":
		return "Loading changes…"
	case "not-ready":
		return "Changes not ready"
	case "error":
		return "Changes error"
	}
	if m.Snapshot.Reason != "" {
		return m.Snapshot.Reason
	}
	if m.Status == "ready" || m.Snapshot.Availability == "ready" {
		return "Changes ready"
	}
	return "Changes unavailable"
}
func changesBadges(s DetailChangesSnapshot) string {
	var b []string
	if s.Dirty || s.UntrackedCount > 0 {
		b = append(b, "uncommitted * (not reviewed)")
	}
	if s.Strategy == "current" || !s.Separate {
		b = append(b, "control checkout")
	}
	if s.WorktreeMissing {
		b = append(b, "worktree missing")
	}
	if s.ActiveOperation != "" {
		b = append(b, s.ActiveOperation)
	}
	if s.RebaseIntent {
		b = append(b, "rebase")
	}
	if s.Merged != nil && *s.Merged {
		b = append(b, "ancestor of default (advisory)")
	}
	if s.Review.Recorded {
		parity := "review differs"
		if s.Review.BaseMatches && s.Review.HeadMatches {
			parity = "review matches committed head"
		}
		if s.Review.Superseded {
			parity = "review superseded"
		}
		b = append(b, parity+" (advisory)")
	}
	b = append(b, s.Warnings...)
	return strings.Join(b, " | ")
}
func changesFileRow(m DetailChangesModel, index, width int) string {
	if index >= len(m.Snapshot.Files) {
		return ""
	}
	f := m.Snapshot.Files[index]
	cursor := " "
	if index == m.FileIndex {
		cursor = ">"
	}
	star := " "
	if f.Uncommitted || f.Untracked {
		star = "*"
	}
	prefix := cursor + string(f.Status) + star + " "
	counts := ""
	if width >= 34 {
		counts = fmt.Sprintf("+%d -%d", f.Added, f.Deleted)
		if f.Binary {
			counts = "bin"
		} else if f.Untracked {
			counts = "new"
		}
		counts = changesPad(counts, 10)
	}
	pathWidth := max(width-cells.Width(prefix)-cells.Width(counts), 0)
	return cells.Truncate(prefix+changesPad(changesLeft(f.Display, pathWidth), pathWidth)+counts, width)
}
func changesLeft(s string, width int) string {
	if cells.Width(s) <= width {
		return s
	}
	if width < 2 {
		return cells.Truncate(s, width)
	}
	r := []rune(s)
	for len(r) > 0 && cells.Width(string(r)) > width-1 {
		r = r[1:]
	}
	return "…" + string(r)
}
func changesPad(s string, width int) string {
	s = cells.Truncate(s, width)
	return s + strings.Repeat(" ", max(width-cells.Width(s), 0))
}
func changesDiffRow(m DetailChangesModel, index, width int, p theme.Palette) string {
	if m.FileIndex < 0 || m.FileIndex >= len(m.Snapshot.Files) {
		return ""
	}
	if m.Diff.Path != m.Snapshot.Files[m.FileIndex].Path {
		if index == 0 {
			text := "Diff unavailable"
			switch m.DiffStatus {
			case "loading":
				text = "Loading diff…"
			case "error":
				text = "Diff failed"
			}
			return cells.Truncate(text, width)
		}
		return ""
	}
	if index >= len(m.Diff.Lines) {
		if index == len(m.Diff.Lines) {
			text := ""
			switch {
			case m.Diff.Truncated:
				text = "… byte/line limit: diff truncated"
			case m.Diff.Binary:
				text = "Binary file"
			case m.DiffStatus == "loading" || m.Updating:
				text = "Updating diff…"
			case m.DiffStatus == "error":
				text = "Diff failed (last good content above)"
			case len(m.Diff.Lines) == 0:
				text = "No textual changes"
			}
			return cells.Truncate(text, width)
		}
		return ""
	}
	line := m.Diff.Lines[index]
	role := theme.RoleDetailBody
	switch line.Kind {
	case DetailDiffAdd:
		role = theme.RoleDetailSuccess
	case DetailDiffDel:
		role = theme.RoleDetailError
	case DetailDiffHunk:
		role = theme.RoleDetailInfo
	case DetailDiffMarker, DetailDiffMeta:
		role = theme.RoleDetailWarning
	}
	return p.Paint(role, cells.Truncate(line.Text, width))
}
