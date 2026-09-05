//go:build darwin || linux

package merge

import (
	"fmt"
	"io/fs"
	"syscall"
)

func regularFileLinkCount(info fs.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected file metadata type %T", info.Sys())
	}
	// Nlink is uint16 on darwin and uint32 on linux/arm64, so the conversion is
	// only redundant on linux/amd64, where the linter runs.
	return uint64(stat.Nlink), nil //nolint:unconvert // Nlink is narrower than uint64 on darwin and linux/arm64.
}
