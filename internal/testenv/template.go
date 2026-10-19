package testenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// RepoTemplate builds a fixture repository once per test process and hands out
// independent copies, replacing the init/add/commit Git processes each fixture
// would otherwise spend. Copies are only offered under the Git-relevant
// environment the template was built with; callers fall back to a fresh setup
// otherwise. Create it after SetupGit and call Cleanup after m.Run.
type RepoTemplate struct {
	build       func(dir string) error
	mu          sync.Mutex
	root        string
	environment string
}

// NewRepoTemplate captures the current Git-relevant environment. build runs at
// most once, lazily, in a private directory.
func NewRepoTemplate(build func(dir string) error) *RepoTemplate {
	return &RepoTemplate{build: build, environment: gitEnvironment()}
}

// CopyTo copies the template into dest, which must not exist yet. It returns
// false without error when the environment changed since NewRepoTemplate, so
// the caller can build the fixture itself.
func (r *RepoTemplate) CopyTo(dest string) (bool, error) {
	if gitEnvironment() != r.environment {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root == "" {
		root, err := os.MkdirTemp("", "tao-repo-template-")
		if err != nil {
			return false, err
		}
		if err := r.build(root); err != nil {
			return false, errors.Join(err, os.RemoveAll(root))
		}
		r.root = root
	}
	return true, CopyTree(r.root, dest)
}

// Cleanup removes the template. Call only after every copy user finished.
func (r *RepoTemplate) Cleanup() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root == "" {
		return nil
	}
	err := os.RemoveAll(r.root)
	if err == nil {
		r.root = ""
	}
	return err
}

func gitEnvironment() string {
	var values []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || key == "PATH" || key == "HOME" || key == "XDG_CONFIG_HOME" || key == "TZ" || key == "EMAIL" {
			values = append(values, entry)
		}
	}
	sort.Strings(values)
	return strings.Join(values, "\n")
}

// CopyTree copies regular files and directories from source into dest,
// preserving permissions. Symlinks and other special files are rejected.
func CopyTree(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported template entry %s", rel)
		}
		data, err := os.ReadFile(path) // #nosec G304 G122 -- private immutable template; symlinks are rejected above.
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil { // #nosec G703 -- relative WalkDir entry from a private template into a fresh test directory.
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}
