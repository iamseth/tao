package promptinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type Result struct {
	Agent  runtimeconfig.AgentKind
	Name   string
	Path   string
	Status string
}

func InstallAll(agent runtimeconfig.AgentKind, force bool) ([]Result, error) {
	descriptor, ok := descriptorForAgent(agent)
	if !ok {
		return nil, unsupportedAgentError(agent)
	}
	return installDescriptor(descriptor, force)
}

func CheckAll(agent runtimeconfig.AgentKind) ([]Result, error) {
	descriptor, ok := descriptorForAgent(agent)
	if !ok {
		return nil, unsupportedAgentError(agent)
	}
	return checkDescriptor(descriptor)
}

func Dir(agent runtimeconfig.AgentKind) (string, error) {
	descriptor, ok := descriptorForAgent(agent)
	if !ok {
		return "", unsupportedAgentError(agent)
	}
	return descriptor.PromptDir()
}

func descriptorForAgent(agent runtimeconfig.AgentKind) (agentpkg.Descriptor, bool) {
	for _, descriptor := range agentpkg.All() {
		if descriptor.Kind == agent {
			return descriptor, true
		}
	}
	return agentpkg.Descriptor{}, false
}

func unsupportedAgentError(agent runtimeconfig.AgentKind) error {
	return fmt.Errorf("unsupported agent %q (want %s)", agent, runtimeconfig.SupportedAgentKindsText())
}

func Path(dir string, name string) string {
	return filepath.Join(dir, name+".md")
}

func Install(path string, content string, force bool) error {
	existing, err := os.ReadFile(path) //nolint:gosec // G304: path is a caller-provided install target
	if err == nil {
		if string(existing) == content {
			return nil
		}
		if !force && !strings.Contains(string(existing), "tao-managed:") {
			return fmt.Errorf("%s exists and is not tao-managed; use --force to overwrite", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// reconcileMarkdown publishes only complete files. Missing targets use a hard
// link for no-clobber publication; managed updates use a same-directory rename.
// Explicit installation retains its existing behavior separately.
func reconcileMarkdown(path string, content string) (string, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// Preserve the link, as Install does, rather than replacing it with a file.
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		path = resolved
	}
	status, err := Status(path, content)
	if err != nil || status == "current" || status == "unmanaged" {
		return status, err
	}
	mode := os.FileMode(0o600)
	if status == "stale" {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".tao-prompt-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	// Recheck ownership and freshness after preparing the replacement.
	status, err = Status(path, content)
	if err != nil || status == "current" || status == "unmanaged" {
		return status, err
	}
	if status == "missing" {
		if err := os.Link(file.Name(), path); err != nil {
			if errors.Is(err, os.ErrExist) {
				return Status(path, content)
			}
			return "", err
		}
	} else if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return "installed", nil
}

func removeManaged(path string, name string) (bool, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // G304: path is a caller-provided install target
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	marker := "<!-- tao-managed: " + name + " v1 -->"
	managed := false
	for line := range strings.SplitSeq(string(existing), "\n") {
		if strings.TrimSuffix(line, "\r") == marker {
			managed = true
			break
		}
	}
	if !managed {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func Status(path string, want string) (string, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // G304: path is a caller-provided install target
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if string(existing) == want {
		return "current", nil
	}
	if strings.Contains(string(existing), "tao-managed:") {
		return "stale", nil
	}
	return "unmanaged", nil
}
