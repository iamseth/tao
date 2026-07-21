package verification

import "strings"

func cdCommand(lookup commandLookup, tokens []string) (string, []string, bool) {
	if len(tokens) < 4 || tokens[0] != "cd" {
		return "", nil, false
	}
	for i := 2; i < len(tokens); i++ {
		if tokens[i] == "&&" {
			return lookup.resolve(lookup.repoRoot(), tokens[1]), tokens[i+1:], true
		}
	}
	return "", nil, false
}

func pnpmDirCommand(lookup commandLookup, tokens []string) (string, bool) {
	if len(tokens) < 3 || tokens[0] != "pnpm" {
		return "", false
	}
	for i := 1; i < len(tokens)-1; i++ {
		if tokens[i] == "--dir" || tokens[i] == "-C" {
			return lookup.resolve(lookup.repoRoot(), tokens[i+1]), true
		}
	}
	return "", false
}

func pnpmFilterCommand(lookup commandLookup, tokens []string) (string, string, bool) {
	if len(tokens) < 3 || tokens[0] != "pnpm" {
		return "", "", false
	}
	for i := 1; i < len(tokens)-1; i++ {
		if tokens[i] != "--filter" && tokens[i] != "-F" {
			continue
		}
		filter := strings.TrimSuffix(tokens[i+1], "...")
		if strings.HasPrefix(filter, "./") || strings.HasPrefix(filter, "../") {
			return lookup.resolve(lookup.repoRoot(), filter), "", true
		}
		if dir, ok := lookup.findPackageDir(filter); ok {
			return dir, "", true
		}
		return "", tokens[i+1], false
	}
	return "", "", false
}
