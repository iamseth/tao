package tui

import (
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

// Overwrite content before erasing its unused suffix. Clearing the entire screen
// first exposes a blank frame during routine refreshes on slower terminals.
func terminalFrame(frame string, size term.Size) string {
	lines := strings.Split(strings.TrimPrefix(frame, clearScreenSequence), "\n")
	if size.Height > 0 && len(lines) > size.Height {
		lines = lines[:size.Height]
	}
	var out strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&out, "\x1b[%d;1H%s", i+1, line)
		// At the right margin the cursor may be pending autowrap; EL there would
		// erase the last cell. Explicit row positioning also avoids scrolling.
		if size.Width <= 0 || cells.Width(line) < size.Width {
			out.WriteString("\x1b[K")
		}
	}
	if size.Height <= 0 || len(lines) < size.Height {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[J", len(lines)+1)
	}
	return out.String()
}
