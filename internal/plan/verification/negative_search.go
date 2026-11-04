package verification

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// NegativeSearchMatch describes matching lines, with at most five locations.
type NegativeSearchMatch struct {
	Total     int
	Locations []string
	FirstPath string
}

// NegativeSearchMatches passively scans supported literal negative search gates.
func (a *Analyzer) NegativeSearchMatches(command string) (NegativeSearchMatch, bool) {
	empty := NegativeSearchMatch{}
	if a == nil || a.lookup == nil || !negativeSearchLiteralShell(command) {
		return empty, false
	}
	pipelines := CommandPipelines(command)
	if len(pipelines) != 1 {
		return empty, false
	}
	for _, token := range splitCommandTokens(command) {
		if token.separator {
			return empty, false
		}
	}
	fields := pipelines[0]
	if len(fields) < 5 || fields[0] != "!" || (fields[1] != "grep" && fields[1] != "rg") {
		return empty, false
	}
	i := 2
	quiet, fixed := false, false
	for i < len(fields) && strings.HasPrefix(fields[i], "-") {
		flag := strings.TrimPrefix(fields[i], "-")
		if flag == "" || strings.Trim(flag, "qFs") != "" {
			return empty, false
		}
		quiet = quiet || strings.Contains(flag, "q")
		fixed = fixed || strings.Contains(flag, "F")
		i++
	}
	if !quiet || i+1 >= len(fields) {
		return empty, false
	}
	pattern := fields[i]
	meta := ".[]*^$\\"
	if fields[1] == "rg" {
		meta += "+?(){}|"
	}
	if !fixed && strings.ContainsAny(pattern, meta) {
		return empty, false
	}
	root, err := filepath.EvalSymlinks(a.lookup.repoRoot())
	if err != nil {
		return empty, false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return empty, false
	}
	result := NegativeSearchMatch{}
	for _, arg := range fields[i+1:] {
		if strings.HasPrefix(arg, "-") || strings.ContainsAny(arg, "*?[]") {
			return empty, false
		}
		path := arg
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return empty, false
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return empty, false
		}
		data, ok := negativeSearchRead(path)
		if !ok {
			return empty, false
		}
		// Do not count a phantom empty line after a trailing newline.
		for line, remaining := 1, data; len(remaining) > 0; line++ {
			text, rest, _ := bytes.Cut(remaining, []byte{'\n'})
			remaining = rest
			if !bytes.Contains(text, []byte(pattern)) {
				continue
			}
			result.Total++
			if result.FirstPath == "" {
				result.FirstPath = filepath.ToSlash(rel)
			}
			if len(result.Locations) < 5 {
				result.Locations = append(result.Locations, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), line))
			}
		}
	}
	return result, true
}

func negativeSearchRead(path string) ([]byte, bool) {
	const maxSize = 4 << 20
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, false
	}
	f, err := os.Open(path) // #nosec G304 -- caller resolves symlinks and confines path to the repository root.
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	return data, err == nil && len(data) <= maxSize
}

// Refuse expansions, redirections, and malformed quoting that the shared
// shell-like tokenizer intentionally does not interpret.
func negativeSearchLiteralShell(command string) bool {
	var quote rune
	escaped := false
	for _, r := range command {
		if quote == '\'' {
			if r == quote {
				quote = 0
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '$' || r == '`' {
			return false
		}
		if quote == '"' {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if strings.ContainsRune("<>#(){}~*?[]", r) {
			return false
		}
	}
	return quote == 0 && !escaped
}
