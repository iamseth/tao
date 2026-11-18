package configtypes

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// NormalizeCommitSecretPathPrefixes returns independent, sorted directory prefixes.
// Validation precedes cleaning so traversal cannot disappear into an exemption.
func NormalizeCommitSecretPathPrefixes(prefixes []string) ([]string, error) {
	var normalized []string
	for _, prefix := range prefixes {
		value := strings.ReplaceAll(prefix, `\`, "/")
		if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || (len(value) >= 2 && value[1] == ':') {
			return nil, fmt.Errorf("invalid commit secret-path prefix %q: must be a repository-relative directory", prefix)
		}
		for _, part := range strings.Split(value, "/") {
			if part == ".." {
				return nil, fmt.Errorf("invalid commit secret-path prefix %q: parent traversal is forbidden", prefix)
			}
		}
		value = path.Clean(value)
		if len(value) >= 2 && value[1] == ':' {
			return nil, fmt.Errorf("invalid commit secret-path prefix %q: drive-qualified paths are forbidden", prefix)
		}
		if value == "." {
			return nil, fmt.Errorf("invalid commit secret-path prefix %q: repository-wide exemption is forbidden", prefix)
		}
		normalized = append(normalized, value+"/")
	}
	slices.Sort(normalized)
	return slices.Compact(normalized), nil
}
