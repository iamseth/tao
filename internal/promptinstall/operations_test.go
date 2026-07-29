package promptinstall

import (
	"errors"
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

func TestDiscoveredOperationsPreserveAgentAndPromptOrder(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", claudeDir)
	t.Setenv("TAO_CODEX_COMMANDS_DIR", codexDir)
	claude, _ := agentpkg.Lookup(runtimeconfig.AgentClaude)
	codex, _ := agentpkg.Lookup(runtimeconfig.AgentCodex)
	descriptors := []agentpkg.Descriptor{claude, codex}

	results, err := InstallDiscovered(descriptors, false)
	if err != nil {
		t.Fatal(err)
	}
	promptCount := len(prompts.Definitions())
	if len(results) != 2*promptCount {
		t.Fatalf("InstallDiscovered results = %d, want %d", len(results), 2*promptCount)
	}
	for i, result := range results {
		wantAgent := runtimeconfig.AgentClaude
		if i >= promptCount {
			wantAgent = runtimeconfig.AgentCodex
		}
		if result.Agent != wantAgent {
			t.Fatalf("result %d agent = %q, want %q", i, result.Agent, wantAgent)
		}
	}

	checked, err := CheckDiscovered(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range checked {
		if result.Status != "current" {
			t.Fatalf("%s %s status = %q, want current", result.Agent, result.Name, result.Status)
		}
	}
}

func TestDiscoveredOperationsHandleNoAgents(t *testing.T) {
	installed, err := InstallDiscovered(nil, false)
	if err != nil || len(installed) != 0 {
		t.Fatalf("InstallDiscovered(nil) = %v, %v, want empty success", installed, err)
	}
	checked, err := CheckDiscovered(nil)
	if err != nil || len(checked) != 0 {
		t.Fatalf("CheckDiscovered(nil) = %v, %v, want empty success", checked, err)
	}
}

func TestDiscoveredOperationsIdentifyFailingAgent(t *testing.T) {
	descriptor := agentpkg.Descriptor{
		Kind:  runtimeconfig.AgentKind("broken"),
		Label: "broken",
		PromptDir: func() (string, error) {
			return "", errors.New("cannot resolve target")
		},
	}
	for name, operation := range map[string]func() error{
		"install": func() error {
			_, err := InstallDiscovered([]agentpkg.Descriptor{descriptor}, false)
			return err
		},
		"check": func() error {
			_, err := CheckDiscovered([]agentpkg.Descriptor{descriptor})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := operation()
			if err == nil || !strings.Contains(err.Error(), "broken prompts: cannot resolve target") {
				t.Fatalf("error = %v, want agent-qualified target error", err)
			}
		})
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

	path := filepath.Join(promptsDir, "tao-plan.md")
	text := readPromptInstallText(t, path)
	for _, want := range []string{"description: Guide a read-only planning session", "tao-managed: tao-plan v1", "You are in PLAN mode.", "$ARGUMENTS"} {
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
	insightsReview := readPromptInstallText(t, filepath.Join(promptsDir, "tao-insights-review.md"))
	for _, want := range []string{"agent: plan", "tao-managed: tao-insights-review v1", "tao insights --all-repos --digest", "not in a tao repo"} {
		if !strings.Contains(insightsReview, want) {
			t.Fatalf("expected %q in Pi Tao insights review prompt, got %q", want, insightsReview)
		}
	}
	if _, err := os.Stat(filepath.Join(promptsDir, "tao-commit.md")); !os.IsNotExist(err) {
		t.Fatalf("expected Pi tao-commit prompt template not to be installed, got %v", err)
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

	for _, name := range []string{"plan", "note-slice", "note", "run", "insights-review"} {
		commandName := "tao-" + name
		path := filepath.Join(commandsDir, commandName+".md")
		text := readPromptInstallText(t, path)
		for _, want := range []string{"description: Tao /" + commandName + " command wrapper", "tao-managed: " + commandName + " v1", "tao prompt " + name + " --arguments-stdin <<'TAO_PROMPT_ARGUMENTS'"} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected %q in Claude command wrapper, got %q", want, text)
			}
		}
		if strings.Contains(text, "You are in ") {
			t.Fatalf("expected thin Claude wrapper, got embedded prompt content %q", text)
		}
	}
	note := readPromptInstallText(t, filepath.Join(commandsDir, "tao-note.md"))
	for _, want := range []string{
		"allowed-tools: Bash(tao prompt note:*), Bash(tao note:*)",
		"tao prompt note --arguments-stdin",
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("expected %q in Claude note wrapper, got %q", want, note)
		}
	}
	if strings.Contains(note, "The first line must be a one-line title") {
		t.Fatalf("expected thin Claude note wrapper, got embedded prompt body %q", note)
	}
	assertManagedCommitDelegates(t, filepath.Join(commandsDir, "tao-commit.md"), "allowed-tools: Bash(tao commit:*)")
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
	agentModes := map[string]string{"plan": "plan", "grill-me": "plan", "insights-review": "plan", "note-slice": "build", "note": "build", "run": "build", "commit": "build", "slice": "build"}
	for _, name := range []string{"plan", "note-slice", "note", "run", "grill-me", "slice", "insights-review"} {
		commandName := "tao-" + name
		path := filepath.Join(commandsDir, commandName+".md")
		text := readPromptInstallText(t, path)
		styleB := "!`tao prompt " + name + " --arguments \"$ARGUMENTS\"`"
		for _, want := range []string{"<!-- tao-managed: " + commandName + " v1 -->", styleB, "agent: " + agentModes[name], "description: "} {
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
	assertManagedCommitDelegates(t, filepath.Join(commandsDir, "tao-commit.md"), "agent: build")
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

func TestInstalledCommandMetadataIsPrefixedAndDelegatesLogicalSelectors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "pi"))
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", filepath.Join(home, "claude"))
	t.Setenv("TAO_OPENCODE_COMMANDS_DIR", filepath.Join(home, "opencode"))
	t.Setenv("TAO_CODEX_COMMANDS_DIR", filepath.Join(home, "codex"))

	for _, kind := range []runtimeconfig.AgentKind{runtimeconfig.AgentPi, runtimeconfig.AgentClaude, runtimeconfig.AgentOpenCode, runtimeconfig.AgentCodex} {
		results, err := InstallAll(kind, false)
		if err != nil {
			t.Fatalf("InstallAll(%s): %v", kind, err)
		}
		for _, result := range results {
			if !strings.HasPrefix(result.Name, "tao-") {
				t.Fatalf("InstallAll(%s) result name = %q, want tao- prefix", kind, result.Name)
			}
			if filepath.Ext(result.Path) == ".md" && filepath.Base(result.Path) != result.Name+".md" {
				t.Fatalf("InstallAll(%s) result path = %q for name %q", kind, result.Path, result.Name)
			}
		}
	}

	for _, kind := range []runtimeconfig.AgentKind{runtimeconfig.AgentClaude, runtimeconfig.AgentOpenCode} {
		descriptor, ok := agentpkg.Lookup(kind)
		if !ok {
			t.Fatalf("missing %s descriptor", kind)
		}
		for _, definition := range prompts.Definitions() {
			if definition.Name == prompts.PromptCommit {
				continue
			}
			content, err := renderInstallContent(descriptor, definition)
			if err != nil {
				t.Fatalf("render %s %s: %v", kind, definition.Name, err)
			}
			if strings.Contains(content, "tao prompt tao-") || !strings.Contains(content, "tao prompt "+definition.Name) {
				t.Fatalf("%s command %s did not preserve logical selector: %q", kind, definition.CommandName, content)
			}
		}
	}
}

func TestManagedOpenCodeCommandFallsBackWithoutFrontmatter(t *testing.T) {
	content, err := promptfmt.ManagedOpenCodeCommand("tao-widget", "widget", "No frontmatter here.\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agent: build", "description: Tao /tao-widget command wrapper", "!`tao prompt widget --arguments \"$ARGUMENTS\"`"} {
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

	path := filepath.Join(promptsDir, "tao-note-slice.md")
	text := readPromptInstallText(t, path)
	for _, want := range []string{"description: Tao /tao-note-slice command wrapper", "<!-- tao-managed: tao-note-slice v1 -->", "# Tao Note Slice", "You are in SLICE mode for a durable Tao planning session."} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in Codex prompt, got %q", want, text)
		}
	}
	if strings.Contains(text, "agent: build") || strings.Contains(text, "{{ .Arguments }}") {
		t.Fatalf("expected Codex prompt to omit agent frontmatter and render the note-slice body, got %q", text)
	}
	assertPromptRenameInstalled(t, promptsDir)
	insightsReview := readPromptInstallText(t, filepath.Join(promptsDir, "tao-insights-review.md"))
	for _, want := range []string{"description: Review global Tao evidence", "tao-managed: tao-insights-review v1", "tao insights --all-repos --digest", "not in a tao repo"} {
		if !strings.Contains(insightsReview, want) {
			t.Fatalf("expected %q in Codex Tao insights review prompt, got %q", want, insightsReview)
		}
	}
	assertManagedCommitDelegates(t, filepath.Join(promptsDir, "tao-commit.md"), "description: Commit the current changes locally through Tao")

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
	content, err := promptfmt.ManagedCodexCommand("tao-widget", "widget", "Body {{ .Arguments }}\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"description: Tao /tao-widget command wrapper", "<!-- tao-managed: tao-widget v1 -->", "Body $ARGUMENTS"} {
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
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("existing unprefixed command\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertManagedCommitDelegates(t *testing.T, path, providerMarker string) {
	t.Helper()
	text := readPromptInstallText(t, path)
	for _, want := range []string{
		"<!-- tao-managed: tao-commit v1 -->",
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
	noteSlice := readPromptInstallText(t, filepath.Join(dir, "tao-note-slice.md"))
	if !strings.Contains(noteSlice, "<!-- tao-managed: tao-note-slice v1 -->") {
		t.Fatalf("expected managed note-slice prompt, got %q", noteSlice)
	}
	if _, err := os.Stat(filepath.Join(dir, "web-slice.md")); !os.IsNotExist(err) {
		t.Fatalf("expected retired managed web-slice prompt removed, stat error = %v", err)
	}
	if old := readPromptInstallText(t, filepath.Join(dir, "plan.md")); old != "existing unprefixed command\n" {
		t.Fatalf("existing unprefixed command changed: %q", old)
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
