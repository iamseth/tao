package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func TestMain(m *testing.M) {
	// Set process-wide once so parallel tests never need t.Setenv: Git must
	// not prompt, and an owner inherited from a managed Tao session must not
	// leak into completion lifetime checks. Completion helper children are the
	// exception: they inherit their owner from the parent test on purpose.
	if err := os.Setenv("GIT_TERMINAL_PROMPT", "0"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Getenv("TAO_TEST_COMPLETION_HELPER") == "" {
		if err := os.Unsetenv(sliceCompletionOwnerEnv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	cleanup, err := testenv.SetupGit()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sliceTemplateEnvironment = sliceRepoEnvironment()
	code := m.Run()
	if err := sharedSliceTemplate.cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// Only the ordinary SetupGit environment is reusable. Overrides (including
// process fakes) keep the original fresh setup and its subprocess sequence.
var sliceTemplateEnvironment string
var sharedSliceTemplate sliceRepoTemplate

func sliceRepoEnvironment() string {
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

func canCopySliceRepo() bool {
	if sliceRepoEnvironment() != sliceTemplateEnvironment {
		return false
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		// Editor and prompt settings cannot change a noninteractive fixture, and
		// agent sessions commonly set them.
		switch key {
		case "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR":
			continue
		}
		if strings.HasPrefix(key, "GIT_") {
			return false
		}
	}
	// Re-read configuration so in-place edits and includes cannot silently reuse
	// a template created under different settings. No test CommandRunner is used.
	cmd := exec.Command("git", "config", "--list")
	cmd.Dir = os.Getenv("HOME")
	out, err := cmd.Output()
	return err == nil && string(out) == "gc.auto=0\nmaintenance.auto=false\ncommit.gpgsign=false\ninit.defaultbranch=main\n"
}

type sliceRepoTemplate struct {
	mu   sync.Mutex
	root string
}

func (c *sliceRepoTemplate) source(build func(string) error) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root != "" {
		return c.root, nil
	}
	root, err := os.MkdirTemp("", "tao-run-template-")
	if err != nil {
		return "", err
	}
	if err := build(root); err != nil {
		return "", errors.Join(err, os.RemoveAll(root))
	}
	c.root = root
	return root, nil
}

// Called only after all copy users have finished, before SetupGit cleanup.
func (c *sliceRepoTemplate) cleanup() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == "" {
		return nil
	}
	err := os.RemoveAll(c.root)
	if err == nil {
		c.root = ""
	}
	return err
}

func buildSliceRepoTemplate(root string) error {
	commands := [][]string{{"init"}, {"config", "user.email", "tao@example.com"}, {"config", "user.name", "Tao Test"}}
	for _, args := range commands {
		if err := sliceTemplateGit(root, args...); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0600); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "base"}, {"checkout", "-b", "tao/test"}} {
		if err := sliceTemplateGit(root, args...); err != nil {
			return err
		}
	}
	return nil
}

func sliceTemplateGit(root string, args ...string) error {
	cmd := exec.Command("git", args...) // #nosec G204 -- fixed fixture setup arguments, no shell or injected runner.
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("template git %v: %w: %s", args, err, out)
	}
	return nil
}

// Copy ordinary files, not clones, hardlinks, alternates, or worktree records.
// The source is private and never handed to a fixture consumer.
func copySliceRepo(source, dest string) error {
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
		data, err := os.ReadFile(path) // #nosec G304 G122 -- private immutable template; no concurrent mutation, and symlinks are rejected above.
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil { // #nosec G703 -- relative WalkDir entry from private template into a fresh test directory.

			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}
