package gitops

import (
	"path/filepath"
	"strings"
)

// ProtectedBranch is the single protected-branch predicate for branches Tao must not modify directly.
func ProtectedBranch(branch string) bool {
	return branch == "main" || branch == "master"
}

// PorcelainPaths returns parsed paths and ambiguous raw lines from git status --porcelain output.
func PorcelainPaths(status string) (paths []string, ambiguous []string) {
	for line := range strings.SplitSeq(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path, isAmbiguous := PorcelainPath(line)
		if isAmbiguous {
			ambiguous = append(ambiguous, line)
			continue
		}
		paths = append(paths, path)
	}
	return paths, ambiguous
}

// PorcelainPath returns the path from a git status --porcelain line.
// Rename/copy entries and malformed entries are reported as ambiguous.
func PorcelainPath(line string) (string, bool) {
	if len(line) < 4 {
		return "", true
	}
	status := line[:2]
	path := strings.TrimSpace(line[3:])
	if strings.ContainsAny(status, "RC") || strings.Contains(path, " -> ") || path == "" {
		return "", true
	}
	return filepath.ToSlash(path), false
}

// PorcelainIndexStatus reports whether a porcelain line has an index status.
func PorcelainIndexStatus(line string) bool {
	return len(line) >= 1 && line[0] != ' ' && line[0] != '?'
}
