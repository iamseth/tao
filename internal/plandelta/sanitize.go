package plandelta

import (
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agentinput"
)

// SanitizeLine preserves indentation while neutralizing terminal controls.
func SanitizeLine(line string) string {
	line = strings.TrimSuffix(line, "\r")
	var b strings.Builder
	count := 0
	for _, r := range line {
		if count >= MaxLineRunes {
			break
		}
		if r == '\t' {
			b.WriteString("    ")
			count += 4
			continue
		}
		if r < 0x20 || r >= 0x7f && r <= 0x9f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 || r == 0x200e || r == 0x200f {
			r = utf8.RuneError
		}
		b.WriteRune(r)
		count++
	}
	return agentinput.CapRunes(b.String(), MaxLineRunes)
}

func sanitizeReason(reason string) string {
	first, _, _ := strings.Cut(reason, "\n")
	return agentinput.CapRunes(SanitizeLine(first), MaxReasonChars)
}
