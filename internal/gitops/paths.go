package gitops

import (
	"path/filepath"
	"strings"
)

// IsTaoMetadataPath reports whether path belongs to workspace-local Tao metadata.
func IsTaoMetadataPath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	return path == ".tao" || strings.HasPrefix(path, ".tao/")
}
