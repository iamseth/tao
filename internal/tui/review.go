package tui

import (
	"strings"

	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

// renderReview is presentation only: it does not project plans or repository changes.
func renderReview(model Model) string {
	lines := renderFrame(model, PageReview)
	lines = append(lines, "", model.Palette().Paint(theme.RoleAccent, "Review — coming soon"), "")
	if model.Width >= 60 {
		left := model.Width / 3
		lines = append(lines, cells.Pad("Changed files", left)+"│ Diff", strings.Repeat("─", left)+"┼"+strings.Repeat("─", model.Width-left-1))
		for range max(1, min(model.Height-9, 8)) {
			lines = append(lines, strings.Repeat(" ", left)+"│")
		}
	} else {
		lines = append(lines, "Changed files", "─────────────", "", "Diff", "────", "")
	}
	footer := "Tab / Shift+Tab / ← / →  switch tabs · ? help · q quit"
	if model.Height > 0 {
		lines = lines[:min(len(lines), max(model.Height-1, 0))]
	}
	lines = append(lines, footer)
	if model.ShowShortcuts {
		lines = overlayShortcutLegend(lines, PageReview, model.Width, model.Height, model.Palette())
	}
	if model.Width > 0 {
		for i := range lines {
			lines[i] = cells.Pad(cells.Truncate(lines[i], model.Width), model.Width)
		}
	}
	return clearScreenSequence + strings.Join(lines, "\n")
}
