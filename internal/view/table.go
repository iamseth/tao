package view

import (
	"strings"
	"unicode/utf8"
)

// RuneWidth returns the number of runes in value.
func RuneWidth(value string) int {
	return utf8.RuneCountInString(value)
}

// ColumnWidths returns the maximum rune width of each column across headers
// and rows. Rows may contain fewer or more columns than the headers.
func ColumnWidths(headers []string, rows [][]string) []int {
	columnCount := len(headers)
	for _, row := range rows {
		columnCount = max(columnCount, len(row))
	}
	widths := make([]int, columnCount)
	for column, header := range headers {
		widths[column] = RuneWidth(header)
	}
	for _, row := range rows {
		for column, value := range row {
			widths[column] = max(widths[column], RuneWidth(value))
		}
	}
	return widths
}

// Pad appends spaces until value reaches width runes.
func Pad(value string, width int) string {
	padding := width - RuneWidth(value)
	if padding <= 0 {
		return value
	}
	return value + strings.Repeat(" ", padding)
}
