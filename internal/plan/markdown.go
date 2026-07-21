package plan

import "strings"

func markdownHeading(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == len(trimmed) || trimmed[level] != ' ' {
		return "", false
	}
	return strings.TrimSpace(trimmed[level:]), true
}

func normalizeMarkdownHeading(heading string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(heading)), " "))
}

func containsMarkdownHeading(markdown string, title string) bool {
	want := normalizeMarkdownHeading(title)
	for line := range strings.SplitSeq(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		heading, ok := markdownHeading(line)
		if ok && normalizeMarkdownHeading(heading) == want {
			return true
		}
	}
	return false
}
