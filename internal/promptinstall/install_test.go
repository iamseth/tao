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
	t.Setenv("TAO_OPENCODE_COMMANDS_DIR", filepath.Join(home, "opencode-commands"))
	t.Setenv("TAO_CODEX_COMMANDS_DIR", filepath.Join(home, "codex-prompts"))

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
			content, err := descriptor.RenderPrompt("guard", "---\nagent: build\ndescription: Guard prompt\n---\n\nBody {{ .Arguments }}\n")
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
	t.Setenv("TAO_OPENCODE_COMMANDS_DIR", "")
	t.Setenv("TAO_CODEX_COMMANDS_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")

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

	opencode, err := Dir(runtimeconfig.AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "opencode", "commands"); opencode != want {
		t.Fatalf("OpenCode Dir() = %q, want %q", opencode, want)
	}

	xdgConfig := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	opencode, err = Dir(runtimeconfig.AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdgConfig, "opencode", "commands"); opencode != want {
		t.Fatalf("OpenCode Dir() with XDG_CONFIG_HOME = %q, want %q", opencode, want)
	}

	opencodeCommands := t.TempDir()
	t.Setenv("TAO_OPENCODE_COMMANDS_DIR", opencodeCommands)
	opencode, err = Dir(runtimeconfig.AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if opencode != opencodeCommands {
		t.Fatalf("OpenCode Dir() with env = %q, want %q", opencode, opencodeCommands)
	}

	codex, err := Dir(runtimeconfig.AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".codex", "prompts"); codex != want {
		t.Fatalf("Codex Dir() = %q, want %q", codex, want)
	}

	prompts := t.TempDir()
	t.Setenv("TAO_CODEX_COMMANDS_DIR", prompts)
	codex, err = Dir(runtimeconfig.AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	if codex != prompts {
		t.Fatalf("Codex Dir() with env = %q, want %q", codex, prompts)
	}
}
