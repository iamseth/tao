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
	return uint64(stat.Nlink), nil
}
