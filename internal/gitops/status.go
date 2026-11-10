package gitops

// ProtectedBranch is the single protected-branch predicate for branches Tao must not modify directly.
func ProtectedBranch(branch string) bool {
	return branch == "main" || branch == "master"
}

// PorcelainPaths parses the complete raw NUL stream. Invalid streams return no
// candidate paths and retain the raw input for escaped refusal diagnostics.
func PorcelainPaths(status string) (paths []string, ambiguous []string) {
	entries, err := ParsePorcelainStatusZ(status)
	if err != nil {
		return nil, []string{status}
	}
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	return paths, nil
}

// PorcelainPath parses one raw XY-space-path record without its NUL terminator.
// It does not unquote paths or interpret arrows; rename/copy codes are refused.
func PorcelainPath(line string) (string, bool) {
	entries, err := ParsePorcelainStatusZ(line + "\x00")
	if err != nil || len(entries) != 1 {
		return "", true
	}
	return entries[0].Path, false
}

// PorcelainIndexStatus reports index status for one raw record without its NUL.
func PorcelainIndexStatus(line string) bool {
	_, ambiguous := PorcelainPath(line)
	return !ambiguous && line[0] != ' ' && line[0] != '?'
}
