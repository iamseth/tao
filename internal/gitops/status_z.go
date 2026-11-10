package gitops

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PorcelainStatusEntry preserves a porcelain v1 status code and exact path bytes.
type PorcelainStatusEntry struct {
	Code string
	Path string
}

// ParsePorcelainStatusZ parses only NUL-delimited porcelain v1 --no-renames
// output. Errors discard the entire result; code validity does not establish
// whether an entry is admissible for staging, ownership, or recovery.
func ParsePorcelainStatusZ(status string) ([]PorcelainStatusEntry, error) {
	if status == "" {
		return nil, nil
	}
	if status[len(status)-1] != 0 {
		return nil, fmt.Errorf("porcelain status: missing final NUL terminator")
	}
	var entries []PorcelainStatusEntry
	for record := range strings.SplitSeq(status[:len(status)-1], "\x00") {
		n := len(entries) + 1
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("porcelain status: invalid header or empty path at record %d", n)
		}
		code := record[:2]
		if strings.ContainsAny(code, "RC") {
			return nil, fmt.Errorf("porcelain status: rename/copy code forbidden at record %d", n)
		}
		if !validPorcelainStatusCode(code) {
			return nil, fmt.Errorf("porcelain status: invalid XY code at record %d", n)
		}
		entries = append(entries, PorcelainStatusEntry{Code: code, Path: record[3:]})
	}
	return entries, nil
}

func validPorcelainStatusCode(code string) bool {
	switch code {
	case "??", "!!", "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	case " M", " T", " A", " D", "D ":
		return true
	}
	return strings.ContainsAny(code[:1], "MTA") && strings.ContainsAny(code[1:], " MTD")
}

// FormatPorcelainStatusZ is a presentation-only projection. It never changes
// machine paths and never treats malformed output as a clean worktree.
func FormatPorcelainStatusZ(status string) (string, error) {
	entries, err := ParsePorcelainStatusZ(status)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for i, entry := range entries {
		if i > 0 {
			out.WriteByte('\n')
		}
		path := entry.Path
		quote := !utf8.ValidString(path) || strings.TrimSpace(path) != path
		for _, r := range path {
			if unicode.IsControl(r) || r == '"' || r == '\\' {
				quote = true
				break
			}
		}
		if quote {
			path = strconv.Quote(path)
		}
		out.WriteString(entry.Code)
		out.WriteByte(' ')
		out.WriteString(path)
	}
	return out.String(), nil
}
