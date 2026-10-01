package plandelta

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// Unlike agentinput.ReadBoundedFile, this truncating presentation keeps a useful
// prefix instead of rejecting oversized input, and confines all path traversal
// with os.Root. Nonblocking/no-follow opens plus descriptor checks close the
// lstat/open replacement window for symlinks and special files.
func untrackedDiff(ctx context.Context, rootPath, path string) (FileDiff, error) {
	d := FileDiff{}
	if err := validatePath(path); err != nil {
		return d, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return d, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(path)
	if err != nil {
		return d, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(path)
		if err != nil {
			return d, err
		}
		d.Lines = []Line{{Meta, SanitizeLine("Symlink -> " + target)}}
		return d, nil
	}
	if !info.Mode().IsRegular() {
		return d, errors.New("untracked path is not a regular file")
	}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return d, err
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil {
		return d, err
	}
	if !info.Mode().IsRegular() {
		return d, errors.New("untracked path changed to a special file")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxUntrackedBytes+1))
	if err != nil {
		return d, err
	}
	if err := ctx.Err(); err != nil {
		return d, err
	}
	capped := len(data) > MaxUntrackedBytes
	if capped {
		data = data[:MaxUntrackedBytes]
		d.Truncated = true
	}
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		d.Binary = true
		d.Lines = []Line{{Meta, fmt.Sprintf("Binary file, %d bytes", info.Size())}}
	} else {
		d.Lines = []Line{{Meta, "New untracked file"}}
		text := string(data)
		for text != "" {
			if len(d.Lines) == MaxFileDiffLines {
				d.Truncated = true
				d.Lines = append(d.Lines, Line{Marker, "[showing first 4000 lines]"})
				break
			}
			line, rest, _ := strings.Cut(text, "\n")
			text = rest
			d.Lines = append(d.Lines, Line{Add, SanitizeLine(line)})
		}
	}
	if capped {
		d.Lines = append(d.Lines, Line{Marker, "[untracked file truncated at 256 KiB]"})
	}
	return d, nil
}
