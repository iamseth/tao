package testenv

import "strings"

// StatusFixture frames explicit XY-space-path records for status test doubles.
// It does not interpret or normalize records and is not for production use.
func StatusFixture(records ...string) string {
	if len(records) == 0 {
		return ""
	}
	return strings.Join(records, "\x00") + "\x00"
}
