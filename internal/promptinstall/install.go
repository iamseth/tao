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

func Dir(agent runtimeconfig.AgentKind) (string, error) {
	descriptor, ok := descriptorForAgent(agent)
	if !ok {
		return "", unsupportedAgentError(agent)
	}
	return descriptor.PromptDir()
}

func TargetDescription(agent runtimeconfig.AgentKind) string {
	descriptor, ok := agentpkg.Lookup(agent)
	if !ok {
		return unsupportedAgentError(agent).Error()
	}
	return descriptor.TargetDescription
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
