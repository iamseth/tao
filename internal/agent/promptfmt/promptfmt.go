// Package promptfmt owns per-agent prompt-install directory resolution and
// command rendering.
package promptfmt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func PiDir() (string, error) {
	agentDir, err := PiAgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(agentDir, "prompts"), nil
}

func PiAgentDir() (string, error) {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

func ClaudeDir() (string, error) {
	if dir := os.Getenv("TAO_CLAUDE_COMMANDS_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "commands"), nil
}

func OpenCodeDir() (string, error) {
	if dir := os.Getenv("TAO_OPENCODE_COMMANDS_DIR"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "opencode", "commands"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", "commands"), nil
}

func CodexDir() (string, error) {
	if dir := os.Getenv("TAO_CODEX_COMMANDS_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "prompts"), nil
}

func ManagedPiTemplate(commandName, _ string, content string) (string, error) {
	marker := "<!-- tao-managed: " + commandName + " v1 -->\n\n"
	content = piTemplateContent(content)
	if !strings.HasPrefix(content, "---\n") {
		return marker + content, nil
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return marker + content, nil
	}
	insert := 4 + end + len("\n---")
	if insert < len(content) && content[insert] == '\r' {
		insert++
	}
	if insert < len(content) && content[insert] == '\n' {
		insert++
	}
	return content[:insert] + "\n" + marker + content[insert:], nil
}

func piTemplateContent(content string) string {
	replacer := strings.NewReplacer(
		"{{ .Arguments }}", "$ARGUMENTS",
		"{{.Arguments}}", "$ARGUMENTS",
	)
	return replacer.Replace(content)
}

func ManagedClaudeCommand(commandName, promptName, _ string) (string, error) {
	// Claude Code substitutes $ARGUMENTS as literal text into the command source
	// before the shell runs, so any quote, backtick, $, or backslash the user
	// types would corrupt an inline `tao ... "$ARGUMENTS"` command. Pass the raw
	// text through a quoted heredoc instead: bash reads the body verbatim (no
	// expansion, no quote parsing) and tao consumes it from stdin via
	// --arguments-stdin. A quoted-heredoc body requires the fenced ```! form;
	// the inline !`...` form is single-line only.
	return fmt.Sprintf("---\ndescription: Tao /%[1]s command wrapper\nallowed-tools: Bash(tao prompt %[2]s:*)\nargument-hint: [arguments]\n---\n\n<!-- tao-managed: %[1]s v1 -->\n\n```!\ntao prompt %[2]s --arguments-stdin <<'TAO_PROMPT_ARGUMENTS'\n$ARGUMENTS\nTAO_PROMPT_ARGUMENTS\n```\n", commandName, promptName), nil
}

// ManagedOpenCodeCommand renders a Tao-managed OpenCode command file. OpenCode
// substitutes $ARGUMENTS textually and runs `!`...“ bodies through the shell, so
// the approved Style B body defers rendering to `tao prompt` at invocation time
// instead of embedding the prompt. The `agent:` mode (plan vs build) and
// `description:` are derived from each prompt template's own frontmatter, reusing
// Pi's frontmatter-preservation approach; templates without frontmatter fall back
// to the build mode and a generic wrapper description.
func ManagedOpenCodeCommand(commandName, promptName, template string) (string, error) {
	agentMode, description := openCodeFrontmatter(template)
	if agentMode == "" {
		agentMode = "build"
	}
	if description == "" {
		description = fmt.Sprintf("Tao /%s command wrapper", commandName)
	}
	return fmt.Sprintf("---\nagent: %[3]s\ndescription: %[4]s\n---\n\n<!-- tao-managed: %[1]s v1 -->\n\n!`tao prompt %[2]s --arguments \"$ARGUMENTS\"`\n", commandName, promptName, agentMode, description), nil
}

// ManagedCodexCommand renders a Tao-managed Codex prompt file. Codex reads
// markdown prompts directly from ~/.codex/prompts and substitutes $ARGUMENTS
// textually, so keep the prompt body inline while preserving the template's
// description frontmatter.
func ManagedCodexCommand(commandName, _ string, template string) (string, error) {
	_, description := openCodeFrontmatter(template)
	if description == "" {
		description = fmt.Sprintf("Tao /%s command wrapper", commandName)
	}
	body := piTemplateContent(templateBody(template))
	return fmt.Sprintf("---\ndescription: %[2]s\n---\n\n<!-- tao-managed: %[1]s v1 -->\n\n%[3]s", commandName, description, body), nil
}

func templateBody(template string) string {
	if !strings.HasPrefix(template, "---\n") {
		return template
	}
	end := strings.Index(template[4:], "\n---")
	if end < 0 {
		return template
	}
	insert := 4 + end + len("\n---")
	if insert < len(template) && template[insert] == '\r' {
		insert++
	}
	if insert < len(template) && template[insert] == '\n' {
		insert++
	}
	return template[insert:]
}

// openCodeFrontmatter extracts the agent mode and description from a prompt
// template's leading YAML frontmatter block. Missing frontmatter or keys yield
// empty strings so the caller can apply defaults.
func openCodeFrontmatter(template string) (agentMode string, description string) {
	if !strings.HasPrefix(template, "---\n") {
		return "", ""
	}
	end := strings.Index(template[4:], "\n---")
	if end < 0 {
		return "", ""
	}
	for line := range strings.SplitSeq(template[4:4+end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "agent":
			agentMode = strings.TrimSpace(value)
		case "description":
			description = strings.TrimSpace(value)
		}
	}
	return agentMode, description
}
