package promptinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/promptfmt"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/prompts"
)

func TestInstallAllRejectsUnsupportedAgent(t *testing.T) {
	unsupported := runtimeconfig.AgentKind("legacy-agent")
	if _, err := InstallAll(unsupported, false); err == nil || !strings.Contains(err.Error(), "want pi, claude, opencode, or codex") {
		t.Fatalf("expected unsupported agent install error, got %v", err)
	}
	if _, err := CheckAll(unsupported); err == nil || !strings.Contains(err.Error(), "want pi, claude, opencode, or codex") {
		t.Fatalf("expected unsupported agent check error, got %v", err)
	}
}

func TestInstallAllPiWritesPromptTemplatesAndTaoExtension(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	promptsDir := filepath.Join(agentDir, "prompts")
	writeRetiredManagedPrompt(t, promptsDir)

	results, err := InstallAll(runtimeconfig.AgentPi, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(prompts.Definitions()) {
		t.Fatalf("InstallAll results = %d, want %d", len(results), len(prompts.Definitions()))
	}

	path := filepath.Join(promptsDir, "plan.md")
	text := readPromptInstallText(t, path)
	for _, want := range []string{"description: Guide a read-only planning session", "tao-managed: plan v1", "You are in PLAN mode.", "$ARGUMENTS"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in Pi prompt template, got %q", want, text)
		}
	}
	if strings.Contains(text, "{{ .Arguments }}") {
		t.Fatalf("expected Pi argument placeholder, got %q", text)
	}
	if strings.Contains(text, "tao prompt plan") {
		t.Fatalf("expected direct Pi prompt template, got wrapper content %q", text)
	}
	noteSlice := assertPromptRenameInstalled(t, promptsDir)
	if !strings.Contains(noteSlice, "# Tao Note Slice") || strings.Contains(noteSlice, "tao prompt note-slice") {
		t.Fatalf("expected direct Pi note-slice template, got %q", noteSlice)
	}
	if _, err := os.Stat(filepath.Join(promptsDir, "commit.md")); !os.IsNotExist(err) {
		t.Fatalf("expected Pi commit prompt template not to be installed, got %v", err)
	}
	link := filepath.Join(agentDir, "extensions", "tao")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected Pi Tao extension symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "package.json")); err != nil {
		t.Fatalf("expected Pi Tao extension package target, got %q: %v", target, err)
	}
	commitSource := readPromptInstallText(t, filepath.Join(target, "src", "commit.ts"))
	for _, want := range []string{`run("tao"`, `os.tmpdir()`, `await rm(tempDir, { recursive: true, force: true })`} {
		if !strings.Contains(commitSource, want) {
			t.Fatalf("expected Pi commit extension to delegate with %q, got %q", want, commitSource)
		}
	}
	if strings.Contains(commitSource, `run("git"`) {
		t.Fatalf("Pi commit extension retained an independent Git command: %q", commitSource)
	}

	checked, err := CheckAll(runtimeconfig.AgentPi)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range checked {
		if result.Status != "current" {
			t.Fatalf("expected current status for %s, got %s", result.Name, result.Status)
		}
	}
}

func TestInstallAllClaudeWritesManagedCommandWrappers(t *testing.T) {
	commandsDir := t.TempDir()
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", commandsDir)
	writeRetiredManagedPrompt(t, commandsDir)

	results, err := InstallAll(runtimeconfig.AgentClaude, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(prompts.Definitions()) {
		t.Fatalf("InstallAll results = %d, want %d", len(results), len(prompts.Definitions()))
	}

	for _, name := range []string{"plan", "note-slice", "run"} {
		path := filepath.Join(commandsDir, name+".md")
		text := readPromptInstallText(t, path)
		for _, want := range []string{"description: Tao /" + name + " command wrapper", "tao-managed: " + name + " v1", "tao prompt " + name + " --arguments-stdin <<'TAO_PROMPT_ARGUMENTS'"} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected %q in Claude command wrapper, got %q", want, text)
			}
		}
		if strings.Contains(text, "You are in ") {
			t.Fatalf("expected thin Claude wrapper, got embedded prompt content %q", text)
		}
	}
	assertManagedCommitDelegates(t, filepath.Join(commandsDir, "commit.md"), "allowed-tools: Bash(tao commit:*)")
	assertPromptRenameInstalled(t, commandsDir)

	checked, err := CheckAll(runtimeconfig.AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range checked {
		if result.Status != "current" {
			t.Fatalf("expected current status for %s, got %s", result.Name, result.Status)
		}
	}
}

func TestInstallAllOpenCodeWritesStyleBCommands(t *testing.T) {
	commandsDir := t.TempDir()
	t.Setenv("TAO_OPENCODE_COMMANDS_DIR", commandsDir)
	writeRetiredManagedPrompt(t, commandsDir)

	results, err := InstallAll(runtimeconfig.AgentOpenCode, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(prompts.Definitions()) {
		t.Fatalf("InstallAll results = %d, want %d", len(results), len(prompts.Definitions()))
	}

	// Read-only planning prompts carry agent: plan; mutating prompts carry agent: build.
	agentModes := map[string]string{"plan": "plan", "grill-me": "plan", "note-slice": "build", "run": "build", "commit": "build", "slice": "build"}
	for _, name := range []string{"plan", "note-slice", "run", "grill-me", "slice"} {
		path := filepath.Join(commandsDir, name+".md")
		text := readPromptInstallText(t, path)
		styleB := "!`tao prompt " + name + " --arguments \"$ARGUMENTS\"`"
		for _, want := range []string{"<!-- tao-managed: " + name + " v1 -->", styleB, "agent: " + agentModes[name], "description: "} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected %q in OpenCode command %s, got %q", want, name, text)
			}
		}
		if strings.Contains(text, "Run tao prompt") {
			t.Fatalf("expected no natural-language body in OpenCode command %s, got %q", name, text)
		}
		if strings.Contains(text, "--arguments-stdin") || strings.Contains(text, "You are in ") {
			t.Fatalf("expected thin Style B OpenCode wrapper for %s, got %q", name, text)
		}
	}
	assertManagedCommitDelegates(t, filepath.Join(commandsDir, "commit.md"), "agent: build")
	assertPromptRenameInstalled(t, commandsDir)

	checked, err := CheckAll(runtimeconfig.AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range checked {
		if result.Status != "current" {
			t.Fatalf("expected current status for %s, got %s", result.Name, result.Status)
		}
	}
}

func TestManagedOpenCodeCommandFallsBackWithoutFrontmatter(t *testing.T) {
	content, err := promptfmt.ManagedOpenCodeCommand("widget", "No frontmatter here.\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agent: build", "description: Tao /widget command wrapper", "!`tao prompt widget --arguments \"$ARGUMENTS\"`"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in fallback OpenCode command, got %q", want, content)
		}
	}
}

func TestInstallAllCodexWritesManagedPrompts(t *testing.T) {
	promptsDir := t.TempDir()
	t.Setenv("TAO_CODEX_COMMANDS_DIR", promptsDir)
	writeRetiredManagedPrompt(t, promptsDir)

	results, err := InstallAll(runtimeconfig.AgentCodex, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(prompts.Definitions()) {
		t.Fatalf("InstallAll results = %d, want %d", len(results), len(prompts.Definitions()))
	}

	path := filepath.Join(promptsDir, "note-slice.md")
	text := readPromptInstallText(t, path)
	for _, want := range []string{"description: Tao /note-slice command wrapper", "<!-- tao-managed: note-slice v1 -->", "# Tao Note Slice", "You are in SLICE mode for a durable Tao planning session."} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in Codex prompt, got %q", want, text)
		}
	}
	if strings.Contains(text, "agent: build") || strings.Contains(text, "{{ .Arguments }}") {
		t.Fatalf("expected Codex prompt to omit agent frontmatter and render the note-slice body, got %q", text)
	}
	assertPromptRenameInstalled(t, promptsDir)
	assertManagedCommitDelegates(t, filepath.Join(promptsDir, "commit.md"), "description: Commit the current changes locally through Tao")

	checked, err := CheckAll(runtimeconfig.AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range checked {
		if result.Status != "current" {
			t.Fatalf("expected current status for %s, got %s", result.Name, result.Status)
		}
	}
}

func TestManagedCodexCommandFallsBackWithoutFrontmatter(t *testing.T) {
	content, err := promptfmt.ManagedCodexCommand("widget", "Body {{ .Arguments }}\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"description: Tao /widget command wrapper", "<!-- tao-managed: widget v1 -->", "Body $ARGUMENTS"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in fallback Codex command, got %q", want, content)
		}
	}
}

func TestInstallContentRejectsEmptySelectedContent(t *testing.T) {
	descriptor, ok := agentpkg.Lookup(runtimeconfig.AgentPi)
	if !ok {
		t.Fatal("expected pi descriptor")
	}
	_, err := installContent(descriptor, prompts.Definition{Name: "empty", Template: ""})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty template error, got %v", err)
	}
}

func writeRetiredManagedPrompt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web-slice.md"), []byte("<!-- tao-managed: web-slice v1 -->\nretired\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertManagedCommitDelegates(t *testing.T, path, providerMarker string) {
	t.Helper()
	text := readPromptInstallText(t, path)
	for _, want := range []string{
		"<!-- tao-managed: commit v1 -->",
		providerMarker,
		"tao commit --context",
		"tao commit --proposal-file",
		"${TMPDIR:-/tmp}/tao-commit.XXXXXX",
		"Best-effort remove both temporary files",
		"do not start another agent or model session",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("managed commit command missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "tao prompt commit") || strings.Contains(text, "git commit -m") {
		t.Fatalf("managed commit command bypasses Tao's active-session boundary: %q", text)
	}
}

func assertPromptRenameInstalled(t *testing.T, dir string) string {
	t.Helper()
	noteSlice := readPromptInstallText(t, filepath.Join(dir, "note-slice.md"))
	if !strings.Contains(noteSlice, "<!-- tao-managed: note-slice v1 -->") {
		t.Fatalf("expected managed note-slice prompt, got %q", noteSlice)
	}
	if _, err := os.Stat(filepath.Join(dir, "web-slice.md")); !os.IsNotExist(err) {
		t.Fatalf("expected retired managed web-slice prompt removed, stat error = %v", err)
	}
	return noteSlice
}

func readPromptInstallText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads a test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
