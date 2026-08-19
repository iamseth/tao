package promptinstall

import (
	"path/filepath"
	"strings"
	"testing"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestAllAgentsHavePromptInstallHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "pi-agent"))
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", filepath.Join(home, "claude-commands"))

	for _, descriptor := range agentpkg.All() {
		t.Run(string(descriptor.Kind), func(t *testing.T) {
			if descriptor.PromptDir == nil {
				t.Fatal("PromptDir is nil")
			}
			dir, err := descriptor.PromptDir()
			if err != nil {
				t.Fatalf("PromptDir returned error: %v", err)
			}
			if dir == "" {
				t.Fatal("PromptDir returned empty path")
			}
			if descriptor.RenderPrompt == nil {
				t.Fatal("RenderPrompt is nil")
			}
			content, err := descriptor.RenderPrompt("tao-guard", "guard", "---\nagent: build\ndescription: Guard prompt\n---\n\nBody {{ .Arguments }}\n")
			if err != nil {
				t.Fatalf("RenderPrompt returned error: %v", err)
			}
			if strings.TrimSpace(content) == "" {
				t.Fatal("RenderPrompt returned empty content")
			}
		})
	}
}

func TestDirSelectsAgentTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", "")

	if _, err := Dir(runtimeconfig.AgentKind("legacy-agent")); err == nil {
		t.Fatal("expected unsupported agent target")
	}

	pi, err := Dir(runtimeconfig.AgentPi)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".pi", "agent", "prompts"); pi != want {
		t.Fatalf("Pi Dir() = %q, want %q", pi, want)
	}

	custom := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", custom)
	pi, err = Dir(runtimeconfig.AgentPi)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(custom, "prompts"); pi != want {
		t.Fatalf("Pi Dir() with env = %q, want %q", pi, want)
	}

	claude, err := Dir(runtimeconfig.AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".claude", "commands"); claude != want {
		t.Fatalf("Claude Dir() = %q, want %q", claude, want)
	}

	commands := t.TempDir()
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", commands)
	claude, err = Dir(runtimeconfig.AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	if claude != commands {
		t.Fatalf("Claude Dir() with env = %q, want %q", claude, commands)
	}
}
