package tui

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term/cells"
)

const (
	noteDetailHeaderLines = 8
	noteDetailFooter      = "←/→ prev/next ↑/↓ PgUp/PgDn p plan ^G edit Esc back"
	noteDetailFooterWide  = "←/→ prev/next ↑/↓ PgUp/PgDn p plan ^G edit c copy d/D del 0-3 tier Esc back"
)

// RenderNoteDetail builds a bounded frame for one open note.
func RenderNoteDetail(item note.CatalogNote, width, height int) string {
	return renderNoteDetail(item, width, height, 0)
}

func renderNoteDetail(item note.CatalogNote, width, height, offset int) string {
	return renderNoteDetailWithMessage(item, width, height, offset, "")
}

func renderNoteDetailWithMessage(item note.CatalogNote, width, height, offset int, message string) string {
	header, body := noteDetailSections(item, width)
	footerLines := 1
	if strings.TrimSpace(message) != "" {
		footerLines++
	}
	bodyHeight := noteDetailBodyHeightWithFooter(len(body), height, footerLines)
	offset = max(0, min(offset, len(body)-bodyHeight))
	lines := append([]string(nil), header...)
	lines = append(lines, body[offset:offset+bodyHeight]...)
	if strings.TrimSpace(message) != "" {
		lines = append(lines, singleLineDetail(message))
	}
	footer := noteDetailFooter
	if width <= 0 || width >= cells.Width(noteDetailFooterWide) {
		footer = noteDetailFooterWide
	}
	lines = append(lines, footer)
	return fitDetailFrame(lines, width, height)
}

func noteDetailSections(item note.CatalogNote, width int) (header, body []string) {
	created := "-"
	if !item.CreatedAt.IsZero() {
		created = item.CreatedAt.Format(time.RFC3339)
	}
	updated := "-"
	if !item.UpdatedAt.IsZero() {
		updated = item.UpdatedAt.Format(time.RFC3339)
	}
	header = []string{
		"Tao UI | NOTE DETAIL",
		"Repository: " + rowlabel.DisplayValue(singleLineNoteValue(item.RepositoryName)),
		"Note: " + rowlabel.DisplayValue(singleLineNoteValue(item.ID)),
		"Status: open",
		"Tags: " + rowlabel.DisplayValue(singleLineNoteValue(strings.Join(item.Tags, ", "))),
		"Created: " + created,
		"Updated: " + updated,
		"Text:",
	}
	return header, renderNoteText(item.Text, width)
}

func noteDetailBodyHeightWithFooter(bodyLines, height, footerLines int) int {
	if height <= 0 {
		return bodyLines
	}
	return max(0, min(bodyLines, height-noteDetailHeaderLines-footerLines))
}

func renderNoteText(text string, width int) []string {
	text = sanitizeNoteText(text)
	if text == "" {
		return []string{"  -"}
	}
	available := width - 2
	if width <= 0 {
		available = 0
	} else {
		available = max(available, 1)
	}
	var lines []string
	for _, source := range strings.Split(text, "\n") {
		if available <= 0 || cells.Width(source) <= available {
			lines = append(lines, "  "+source)
			continue
		}
		for cells.Width(source) > available {
			prefix := cells.Truncate(source, available)
			if prefix == "" {
				_, size := utf8.DecodeRuneInString(source)
				source = source[size:]
				continue
			}
			lines = append(lines, "  "+prefix)
			source = strings.TrimPrefix(source, prefix)
		}
		lines = append(lines, "  "+source)
	}
	return lines
}

func sanitizeNoteText(value string) string {
	var printable strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) {
			switch value[index+1] {
			case '[':
				index = skipDetailCSI(value, index+2)
				continue
			case ']':
				index = skipDetailOSC(value, index+2)
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		switch r {
		case '\u009b':
			index = skipDetailCSI(value, index+size)
			continue
		case '\u009d':
			index = skipDetailOSC(value, index+size)
			continue
		case '\n':
			printable.WriteRune(r)
		default:
			if unicode.IsPrint(r) {
				printable.WriteRune(r)
			} else {
				printable.WriteByte(' ')
			}
		}
		index += size
	}
	return strings.TrimSpace(printable.String())
}
