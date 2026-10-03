// Package verifyoutput provides presentation-only line selection over untrusted
// verification output. Its results are never lifecycle or recovery authority.
package verifyoutput

import (
	"strings"
	"unicode"
)

// FailureLines returns distinct failure-bearing lines in their original order,
// with trailing whitespace removed. Leading whitespace is preserved for matching.
func FailureLines(output string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, line := range lines(output) {
		if strings.HasPrefix(line, "--- FAIL") || strings.HasPrefix(line, "FAIL") ||
			strings.HasPrefix(line, "panic:") || strings.Contains(line, "Error:") ||
			strings.Contains(line, "make: ***") {
			if !seen[line] {
				result = append(result, line)
				seen[line] = true
			}
		}
	}
	return result
}

// FirstFailingTest returns the first named failing test, or the first package
// FAIL line if no test name is present.
func FirstFailingTest(text string) string {
	var packageLine string
	for _, line := range lines(text) {
		if rest, ok := strings.CutPrefix(line, "--- FAIL:"); ok {
			name := strings.TrimSpace(rest)
			if end := strings.IndexAny(name, " (\t"); end >= 0 {
				name = name[:end]
			}
			if name != "" {
				return name
			}
		}
		if packageLine == "" {
			fields := strings.Fields(line)
			if strings.HasPrefix(line, "FAIL") && len(fields) >= 2 && fields[0] == "FAIL" {
				packageLine = strings.TrimSpace(line)
			}
		}
	}
	return packageLine
}

// Reason prioritizes failure lines, followed by distinct non-passing lines from
// the last twelve output lines. Unrecognized output relies entirely on fallback.
// The result is bounded by maxRunes, with non-positive limits returning empty.
func Reason(output, fallback string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	selected := FailureLines(output)
	if len(selected) == 0 {
		return head(fallback, maxRunes)
	}
	seen := make(map[string]bool, len(selected))
	for _, line := range selected {
		seen[line] = true
	}
	tail := lines(output)
	if len(tail) > 12 {
		tail = tail[len(tail)-12:]
	}
	for _, line := range tail {
		if line == "" || seen[line] || strings.HasPrefix(line, "ok ") || strings.HasPrefix(line, "?") {
			continue
		}
		selected = append(selected, line)
		seen[line] = true
	}
	return head(strings.Join(selected, "\n"), maxRunes)
}

func lines(text string) []string {
	// A terminal newline terminates the final line rather than adding a tail line.
	result := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range result {
		result[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	return result
}

func head(text string, maxRunes int) string {
	count := 0
	for index := range text {
		if count == maxRunes {
			return text[:index]
		}
		count++
	}
	return text
}
