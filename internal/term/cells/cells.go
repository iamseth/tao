package cells

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func Width(value string) int {
	width := 0
	for index := 0; index < len(value); {
		if end, _, ok := ansiSequence(value, index); ok {
			index = end
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		width += runeCellWidth(r)
		index += size
	}
	return width
}

// ColumnWidths returns the maximum cell width of each column across headers
// and rows. Rows may contain fewer or more columns than the headers.
func ColumnWidths(headers []string, rows [][]string) []int {
	columnCount := len(headers)
	for _, row := range rows {
		columnCount = max(columnCount, len(row))
	}
	widths := make([]int, columnCount)
	for column, header := range headers {
		widths[column] = Width(header)
	}
	for _, row := range rows {
		for column, value := range row {
			widths[column] = max(widths[column], Width(value))
		}
	}
	return widths
}

func runeCellWidth(r rune) int {
	if r == 0 || unicode.IsControl(r) || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) ||
		(r >= 0x1160 && r <= 0x11FF) || (r >= 0x1F3FB && r <= 0x1F3FF) {
		return 0
	}

	low, high := 0, len(wideRuneIntervals)
	for low < high {
		middle := low + (high-low)/2
		candidate := wideRuneIntervals[middle]
		switch {
		case r < candidate.first:
			high = middle
		case r > candidate.last:
			low = middle + 1
		default:
			return 2
		}
	}
	return 1
}

func Pad(value string, width int) string {
	visible, styleActive := ansiState(value)
	var result strings.Builder
	result.WriteString(value)
	if visible < width {
		result.WriteString(strings.Repeat(" ", width-visible))
	}
	if styleActive {
		result.WriteString("\x1b[0m")
	}
	return result.String()
}

// TruncateEllipsis truncates value to leave room for a trailing ellipsis.
func TruncateEllipsis(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return Truncate(value, width-1) + "…"
}

func Truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}

	var result strings.Builder
	visible := 0
	styleActive := false
	for index := 0; index < len(value); {
		if end, sgr, ok := ansiSequence(value, index); ok {
			sequence := value[index:end]
			result.WriteString(sequence)
			if sgr {
				styleActive = sgrStyleActive(sequence)
			}
			index = end
			continue
		}

		r, size := utf8.DecodeRuneInString(value[index:])
		cellWidth := runeCellWidth(r)
		if cellWidth > 0 && visible+cellWidth > width {
			break
		}
		result.WriteRune(r)
		visible += cellWidth
		index += size
	}
	if styleActive {
		result.WriteString("\x1b[0m")
	}
	return result.String()
}

func ansiState(value string) (visible int, styleActive bool) {
	for index := 0; index < len(value); {
		if end, sgr, ok := ansiSequence(value, index); ok {
			if sgr {
				styleActive = sgrStyleActive(value[index:end])
			}
			index = end
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		visible += runeCellWidth(r)
		index += size
	}
	return visible, styleActive
}

// ansiSequence recognizes CSI and OSC sequences and returns the first byte
// after the sequence. Incomplete escapes remain visible input, matching the
// previous parser's fail-open behavior.
func ansiSequence(value string, index int) (end int, sgr bool, ok bool) {
	if index+1 >= len(value) || value[index] != '\x1b' {
		return 0, false, false
	}
	switch value[index+1] {
	case '[':
		for cursor := index + 2; cursor < len(value); cursor++ {
			if value[cursor] >= '@' && value[cursor] <= '~' {
				return cursor + 1, value[cursor] == 'm', true
			}
		}
	case ']':
		for cursor := index + 2; cursor < len(value); cursor++ {
			switch {
			case value[cursor] == '\a':
				return cursor + 1, false, true
			case value[cursor] == '\x1b' && cursor+1 < len(value) && value[cursor+1] == '\\':
				return cursor + 2, false, true
			}
		}
	}
	return 0, false, false
}

func sgrStyleActive(sequence string) bool {
	parameters := sequence[2 : len(sequence)-1]
	return parameters != "" && parameters != "0"
}
