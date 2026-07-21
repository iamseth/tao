package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// DirtyFingerprint is a content-aware digest of a checkout's uncommitted state.
type DirtyFingerprint struct {
	Hash  string
	Paths []string
}

// DirtyFingerprint captures status, tracked content changes, and changed paths.
func (c Client) DirtyFingerprint(ctx context.Context) (DirtyFingerprint, error) {
	status, err := c.StatusPorcelain(ctx)
	if err != nil {
		return DirtyFingerprint{}, err
	}
	diff, err := c.rawOutput(ctx, "diff", "HEAD")
	if err != nil {
		return DirtyFingerprint{}, err
	}
	diffNames, err := c.ChangedFiles(ctx, "HEAD")
	if err != nil {
		return DirtyFingerprint{}, err
	}
	paths := uniqueSortedPaths(append(porcelainPaths(status), diffNames...))

	h := sha256.New()
	_, _ = h.Write([]byte("status\x00"))
	_, _ = h.Write([]byte(status))
	_, _ = h.Write([]byte("\ndiff-head\x00"))
	_, _ = h.Write([]byte(diff))
	return DirtyFingerprint{Hash: hex.EncodeToString(h.Sum(nil)), Paths: paths}, nil
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
