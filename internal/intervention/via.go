package intervention

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxViaBytes bounds a trimmed via label. The label is free-form provenance
// describing who or what relayed an operator decision; it is provenance only
// and never authority.
const MaxViaBytes = 128

// ErrInvalidVia reports a via label that violates the shared bound.
var ErrInvalidVia = errors.New("invalid via label")

// ParseVia trims surrounding whitespace and returns the bounded label, or an
// empty string when nothing remains. It never truncates: a label longer than
// MaxViaBytes, not valid UTF-8, or containing any control character (including
// newline and tab) is rejected with an error.
func ParseVia(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" {
		return "", nil
	}
	if len(label) > MaxViaBytes {
		return "", fmt.Errorf("%w: exceeds %d bytes", ErrInvalidVia, MaxViaBytes)
	}
	if !utf8.ValidString(label) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrInvalidVia)
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: contains a control character", ErrInvalidVia)
		}
	}
	return label, nil
}
