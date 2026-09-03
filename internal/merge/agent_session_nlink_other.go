//go:build !darwin && !linux

package merge

import (
	"errors"
	"io/fs"
)

func regularFileLinkCount(fs.FileInfo) (uint64, error) {
	return 0, errors.New("hard-link inspection is unsupported on this platform")
}
