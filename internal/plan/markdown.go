package plan

import (
	"strings"
	"unicode"
)

func markdownHeadingWithLevel(line string) (string, int, bool) {
	trimmed, ok := markdownLineWithAllowedIndent(line)
	if !ok || !strings.HasPrefix(trimmed, "#") {
		return "", 0, false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level > 6 || level == len(trimmed) || (trimmed[level] != ' ' && trimmed[level] != '\t') {
		return "", 0, false
	}
	heading := strings.TrimSpace(trimmed[level:])
	closingStart := len(heading)
	for closingStart > 0 && heading[closingStart-1] == '#' {
		closingStart--
	}
	if closingStart < len(heading) {
		withoutClosingSpace := strings.TrimRightFunc(heading[:closingStart], unicode.IsSpace)
		if len(withoutClosingSpace) < closingStart {
			heading = withoutClosingSpace
		}
	}
	if heading == "" {
		return "", 0, false
	}
	return heading, level, true
}

func normalizeMarkdownHeading(heading string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(heading)), " "))
}

func containsMarkdownHeading(markdown string, title string) bool {
	_, ok := extractMarkdownSection(markdown, title)
	return ok
}

// extractMarkdownSection returns the body below the first exact ATX heading
// until the next heading at the same or a higher level. Fenced content and
// malformed headings are ignored so legacy prose cannot manufacture sections
// with examples embedded in code blocks.
func extractMarkdownSection(markdown string, title string) (string, bool) {
	want := normalizeMarkdownHeading(title)
	if want == "" {
		return "", false
	}
	var body []string
	matchedLevel := 0
	var fenceDelimiter byte
	fenceLength := 0
	for line := range strings.SplitSeq(strings.ReplaceAll(strings.ReplaceAll(markdown, "\r\n", "\n"), "\r", "\n"), "\n") {
		delimiter, length, isFence := markdownFenceMarker(line)
		if fenceLength != 0 {
			if isFence && delimiter == fenceDelimiter && length >= fenceLength && markdownFenceHasOnlyWhitespaceSuffix(line, length) {
				fenceDelimiter = 0
				fenceLength = 0
			}
			continue
		}
		if isFence {
			fenceDelimiter = delimiter
			fenceLength = length
			continue
		}
		heading, level, ok := markdownHeadingWithLevel(line)
		if ok {
			if matchedLevel != 0 && level <= matchedLevel {
				return strings.TrimSpace(strings.Join(body, "\n")), true
			}
			if matchedLevel == 0 && normalizeMarkdownHeading(heading) == want {
				matchedLevel = level
			}
			continue
		}
		if matchedLevel != 0 {
			body = append(body, line)
		}
	}
	if matchedLevel == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(body, "\n")), true
}

func markdownFenceHasOnlyWhitespaceSuffix(line string, markerLength int) bool {
	trimmed, ok := markdownLineWithAllowedIndent(line)
	return ok && markerLength <= len(trimmed) && strings.TrimSpace(trimmed[markerLength:]) == ""
}

func markdownFenceMarker(line string) (byte, int, bool) {
	trimmed, ok := markdownLineWithAllowedIndent(line)
	if !ok || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false
	}
	delimiter := trimmed[0]
	length := 1
	for length < len(trimmed) && trimmed[length] == delimiter {
		length++
	}
	if length < 3 {
		return 0, 0, false
	}
	return delimiter, length, true
}

func markdownLineWithAllowedIndent(line string) (string, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 {
		return "", false
	}
	return strings.TrimRightFunc(line[indent:], unicode.IsSpace), true
}
