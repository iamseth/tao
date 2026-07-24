package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirtyFingerprint is a content-aware digest of a checkout's uncommitted state.
type DirtyFingerprint struct {
	Hash  string
	Paths []string
}

// DirtyFingerprint captures status, index identity, tracked content changes, and changed paths.
func (c Client) DirtyFingerprint(ctx context.Context) (DirtyFingerprint, error) {
	status, err := c.StatusPorcelain(ctx)
	if err != nil {
		return DirtyFingerprint{}, err
	}
	diff, err := c.rawOutput(ctx, "diff", "HEAD")
	if err != nil {
		return DirtyFingerprint{}, err
	}
	index, err := c.rawOutput(ctx, "ls-files", "--stage", "-z")
	if err != nil {
		return DirtyFingerprint{}, err
	}
	diffNames, err := c.ChangedFiles(ctx, "HEAD")
	if err != nil {
		return DirtyFingerprint{}, err
	}
	untracked, err := c.rawOutput(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return DirtyFingerprint{}, err
	}
	untrackedPaths := nulPaths(untracked)
	paths := uniqueSortedPaths(append(append(porcelainPaths(status), diffNames...), untrackedPaths...))

	h := sha256.New()
	writeFingerprintField(h, "status", []byte(status))
	writeFingerprintField(h, "diff-head", []byte(diff))
	writeFingerprintField(h, "index", []byte(index))
	for _, path := range untrackedPaths {
		if err := c.writeUntrackedFingerprint(h, path); err != nil {
			return DirtyFingerprint{}, err
		}
	}
	return DirtyFingerprint{Hash: hex.EncodeToString(h.Sum(nil)), Paths: paths}, nil
}

func nulPaths(raw string) []string {
	paths := make([]string, 0)
	for path := range strings.SplitSeq(raw, "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func (c Client) writeUntrackedFingerprint(h hash.Hash, path string) error {
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("fingerprint untracked path %q: path escapes repository", path)
	}
	fullPath := filepath.Join(c.repoRoot, clean)
	info, err := os.Lstat(fullPath)
	if err != nil {
		return fmt.Errorf("fingerprint untracked path %q: %w", path, err)
	}

	var contents []byte
	switch {
	case info.Mode().IsRegular():
		contents, err = os.ReadFile(fullPath) // #nosec G304 -- Git supplied a validated repository-relative untracked path.
	case info.Mode()&os.ModeSymlink != 0:
		var target string
		target, err = os.Readlink(fullPath)
		contents = []byte(target)
	}
	if err != nil {
		return fmt.Errorf("fingerprint untracked path %q: %w", path, err)
	}
	writeFingerprintField(h, "untracked-path", []byte(path))
	writeFingerprintField(h, "untracked-mode", []byte(info.Mode().String()))
	writeFingerprintField(h, "untracked-contents", contents)
	return nil
}

func writeFingerprintField(h hash.Hash, label string, value []byte) {
	_, _ = h.Write([]byte(label))
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

func porcelainPaths(status string) []string {
	var paths []string
	for line := range strings.SplitSeq(status, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		if strings.Contains(path, " -> ") {
			parts := strings.Split(path, " -> ")
			path = parts[len(parts)-1]
		}
		path = strings.Trim(path, `"`)
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func uniqueSortedPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
