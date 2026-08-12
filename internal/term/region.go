package term

import (
	"io"
	"strconv"
)

const (
	resetScrollRegionSequence = "\x1b[r"
	saveCursorSequence        = "\x1b[s"
	restoreCursorSequence     = "\x1b[u"
)

// SetScrollRegion restricts terminal scrolling to the inclusive row range.
func SetScrollRegion(w io.Writer, top, bottom int) error {
	return writeSequence(w, "\x1b["+strconv.Itoa(top)+";"+strconv.Itoa(bottom)+"r")
}

// ResetScrollRegion restores scrolling to the terminal's full height. It is
// safe to call even when no scroll region has been set.
func ResetScrollRegion(w io.Writer) error {
	return writeSequence(w, resetScrollRegionSequence)
}

// SaveCursor saves the cursor's current position.
func SaveCursor(w io.Writer) error {
	return writeSequence(w, saveCursorSequence)
}

// RestoreCursor restores the cursor's previously saved position.
func RestoreCursor(w io.Writer) error {
	return writeSequence(w, restoreCursorSequence)
}

// PositionCursor moves the cursor to an absolute row and column.
func PositionCursor(w io.Writer, row, column int) error {
	return writeSequence(w, "\x1b["+strconv.Itoa(row)+";"+strconv.Itoa(column)+"H")
}
