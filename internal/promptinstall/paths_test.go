package promptinstall

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestResolvePathsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "missing-agent"))
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", filepath.Join(home, "missing-commands"))
	paths, err := ResolvePaths(runtimeconfig.AgentPi)
	if err != nil {
		t.Fatal(err)
	}
	if paths.PromptDir != filepath.Join(home, "missing-agent", "prompts") || paths.ExtensionTarget != filepath.Join(home, "missing-agent", "extensions", "tao") || paths.ExtensionSource == "" {
		t.Fatalf("paths: %+v", paths)
	}
	if _, err := os.Stat(filepath.Join(home, "missing-agent")); !os.IsNotExist(err) {
		t.Fatalf("discovery wrote destination: %v", err)
	}
	paths, err = ResolvePaths(runtimeconfig.AgentClaude)
	if err != nil || paths.PromptDir != filepath.Join(home, "missing-commands") || paths.ExtensionTarget != "" {
		t.Fatalf("paths: %+v, %v", paths, err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", "")
	pi, err := ResolvePaths(runtimeconfig.AgentPi)
	if err != nil || pi.PromptDir != filepath.Join(home, ".pi", "agent", "prompts") {
		t.Fatalf("fallback: %+v %v", pi, err)
	}
	claude, err := ResolvePaths(runtimeconfig.AgentClaude)
	if err != nil || claude.PromptDir != filepath.Join(home, ".claude", "commands") {
		t.Fatalf("fallback: %+v %v", claude, err)
	}
	if _, err := ResolvePaths(runtimeconfig.AgentKind("invalid")); err == nil {
		t.Fatal("accepted invalid agent")
	}
}
