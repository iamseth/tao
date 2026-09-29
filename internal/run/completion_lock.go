package run

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamseth/tao/internal/filelock"
)

// Completion writers never acquire the driver's .run.lock. The persistent
// inode is deliberate: unlinking an advisory lock lets concurrent openers lock
// different inodes. The kernel releases ownership on death, with no PID reuse
// or age-based takeover of a live writer.
func acquireSliceCompletionLock(planDir string) (func() error, error) {
	return acquireCompletionFileLock(planDir, ".slice-completion.lock")
}

func completionDirectory(planDir string) (string, error) {
	if !filepath.IsAbs(planDir) {
		return "", fmt.Errorf("completion requires an absolute plan directory")
	}
	dir, err := filepath.EvalSymlinks(planDir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir) // #nosec G703 -- canonical caller-supplied plan metadata directory, not an execution target.
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("completion plan path is not a directory")
	}
	return dir, nil
}

func acquireCompletionFileLock(planDir, name string) (func() error, error) {
	dir, err := completionDirectory(planDir)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304,G703 -- private plan-local coordination file.
	if err != nil {
		return nil, err
	}
	if err := filelock.TryLock(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("slice completion ownership unavailable: %w", err)
	}
	// Close releases this open file description only; repeated release must not
	// affect a subsequent owner of the same inode.
	return sync.OnceValue(file.Close), nil
}
