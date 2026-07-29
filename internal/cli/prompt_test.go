package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptinstall"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestRunWarnsOnceForStaleManagedPromptsAndExecutesCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	app := App{
		Out: &out,
		Err: &errOut,
		PromptFreshnessCheck: func() ([]promptinstall.Result, error) {
			return []promptinstall.Result{
				{Agent: runtimeconfig.AgentPi, Status: "missing"},
				{Agent: runtimeconfig.AgentPi, Status: "stale"},
				{Agent: runtimeconfig.AgentPi, Status: "stale"},
				{Agent: runtimeconfig.AgentClaude, Status: "unmanaged"},
				{Agent: runtimeconfig.AgentCodex, Status: "stale"},
			}, nil
		},
	}

	if err := app.Run(context.Background(), []string{"prompt", "plan"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "You are in PLAN mode") {
		t.Fatalf("expected requested command to execute, got %q", out.String())
	}
	want := "warning: stale Tao-managed prompts for pi, codex; run tao install-prompts\n"
	if got := errOut.String(); got != want {
		t.Fatalf("stale warning = %q, want %q", got, want)
	}
}

func TestPromptFreshnessFailuresDoNotBlockCommand(t *testing.T) {
	for _, failure := range []string{"executable lookup", "prompt directory", "Pi extension inspection"} {
		t.Run(failure, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := App{
				Out: &out,
				Err: &errOut,
				PromptFreshnessCheck: func() ([]promptinstall.Result, error) {
					return nil, errors.New(failure)
				},
			}
			if err := app.Run(context.Background(), []string{"prompt", "plan"}); err != nil {
				t.Fatalf("requested command failed: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("expected requested command output")
			}
			if errOut.Len() != 0 {
				t.Fatalf("freshness failure should be silent, got %q", errOut.String())
			}
		})
	}
}

func TestPromptFreshnessWarningSkipsDirectStatusAndNonCommandPaths(t *testing.T) {
	clearTaoEnv(t)
	setPathExecutables(t)
	calls := 0
	check := func() ([]promptinstall.Result, error) {
		calls++
		return []promptinstall.Result{{Agent: runtimeconfig.AgentPi, Status: "stale"}}, nil
	}
	for _, args := range [][]string{
		{"doctor"},
		{"install-prompts", "--check"},
		{"prompt", "--help"},
		{"--version"},
		{"unknown"},
	} {
		var out, errOut bytes.Buffer
		_ = (App{Out: &out, Err: &errOut, PromptFreshnessCheck: check}).Run(context.Background(), args)
		if errOut.Len() != 0 {
			t.Fatalf("%v emitted duplicate stale warning: %q", args, errOut.String())
		}
	}
	if calls != 0 {
		t.Fatalf("freshness check called %d times on excluded paths", calls)
	}
}

func TestPromptArgumentsStdinHandlesShellMetacharacters(t *testing.T) {
	clearTaoEnv(t)
	// The text below contains the characters that corrupt an inline
	// `--arguments "$ARGUMENTS"` wrapper once Claude Code substitutes it:
	// double quotes, a backtick, a $, and a backslash. Via stdin they must
	// pass through verbatim into the rendered prompt.
	raw := "build a new \"export\" flow with `backticks` and $VARS and a \\ slash\n"
	want := "build a new \"export\" flow with `backticks` and $VARS and a \\ slash"
	var out bytes.Buffer
	app := App{In: strings.NewReader(raw), Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"prompt", "plan", "--arguments-stdin"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), want) {
		t.Fatalf("expected stdin arguments %q in rendered prompt, got %q", want, out.String())
	}
}

func TestPromptRendersCanonicalPrompts(t *testing.T) {
	clearTaoEnv(t)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"prompt", "plan", "--arguments", "add export flow"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"You are in PLAN mode.", "add export flow", "# Planning Packet", "Do not implement code.", "Do not edit files.", "Do not create Tao plan artifacts.", "Ask user-facing clarification questions only in the final assistant response"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected rendered plan prompt content %q, got %q", want, text)
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "slice"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "You are in SLICE mode.") {
		t.Fatalf("expected slice prompt, got %q", out.String())
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "slice", "--arguments", "capture planning context"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if !strings.Contains(text, "## Planning Topic") || !strings.Contains(text, "capture planning context") {
		t.Fatalf("expected rendered slice prompt with arguments, got %q", text)
	}
	if strings.Contains(text, "tao capture-planning-session") {
		t.Fatalf("unexpected planning capture instructions, got %q", text)
	}
	for _, want := range []string{"planning-brief.md", "## User Goal", "## Validation Strategy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected planning brief instruction %q, got %q", want, text)
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "run", "--plan-dir", "/tmp/plan", "--commit=false"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if !strings.Contains(text, "Plan directory: `/tmp/plan`") {
		t.Fatalf("expected rendered plan dir, got %q", text)
	}
	if strings.Contains(text, "Commit with a message") || !strings.Contains(text, "user to review or commit manually") {
		t.Fatalf("expected no-commit instructions, got %q", text)
	}
	if strings.Contains(text, "# Tao Run Packet") {
		t.Fatalf("expected prompt rendering to work without a real plan packet, got %q", text)
	}

	out.Reset()
	planDir := writeRunPlan(t, t.TempDir(), "20260430-1200-prompt", plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	if err := app.Run(context.Background(), []string{"prompt", "run", "--plan-dir", planDir, "--commit=false"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	for _, want := range []string{"# Tao Run Packet", "- ID: 001-a", "## Fallback Files"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected rendered run packet content %q, got %q", want, text)
		}
	}
	if !strings.Contains(text, "tao slice-complete --plan-dir") {
		t.Fatalf("expected run prompt to call slice-complete, got %q", text)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "run", "--plan-dir", "/tmp/plan", "--commit-policy", "slice"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if !strings.Contains(text, "`tao slice-complete` owns the recoverable commit transaction") || strings.Contains(text, "Commit with a message") {
		t.Fatalf("expected Tao-owned slice commit instructions, got %q", text)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "run", "--plan-dir", "/tmp/plan", "--execution-mode", "current"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if !strings.Contains(text, "Use the current branch where `tao run` started") || strings.Contains(text, "Create or reuse a single feature branch") {
		t.Fatalf("expected current branch run prompt instructions, got %q", text)
	}

	err := app.Run(context.Background(), []string{"prompt", "run", "--execution-mode", "main"})
	if err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("expected unsupported prompt execution mode error, got %v", err)
	}
	err = app.Run(context.Background(), []string{"prompt", "run", "--commit-policy", "plan"})
	if err == nil || !strings.Contains(err.Error(), "plan was removed; use slice or none") {
		t.Fatalf("expected removed plan policy migration error, got %v", err)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "repo-health", "--arguments", "focus on bloat"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	for _, want := range []string{"Audit the current git repository", "read-only audit", "focus on bloat"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected rendered repo-health prompt content %q, got %q", want, text)
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "commit", "--arguments", "include staged docs"}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if !strings.Contains(text, "Create one local Git commit") || !strings.Contains(text, "include staged docs") {
		t.Fatalf("expected rendered commit prompt with arguments, got %q", text)
	}
}

func TestPromptRunExecutionMode(t *testing.T) {
	clearTaoEnv(t)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	planDir := writeRunPlan(t, t.TempDir(), "20260430-1200-prompt", plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	if err := app.Run(context.Background(), []string{"prompt", "run", "--plan-dir", planDir, "--execution-mode", "current"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Use the current branch where `tao run` started") || strings.Contains(text, "Create or reuse a single feature branch") {
		t.Fatalf("expected current branch run prompt instructions, got %q", text)
	}
	if !strings.Contains(text, "- Execution Mode: current") {
		t.Fatalf("expected current execution mode in run packet, got %q", text)
	}

	err := app.Run(context.Background(), []string{"prompt", "run", "--execution-mode", "main"})
	if err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("expected unsupported prompt execution mode error, got %v", err)
	}
}

func TestInstallPromptsWritesAndChecksPiPrompts(t *testing.T) {
	clearTaoEnv(t)
	setPathExecutables(t, "pi")
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".pi", "agent", "prompts")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"install-prompts"}); err != nil {
		t.Fatal(err)
	}
	promptNames := []string{"plan", "slice", "note-slice", "run", "grill-me", "improve-codebase-architecture", "improve-documentation", "repo-health", "pr"}
	for _, name := range promptNames {
		path := filepath.Join(root, name+".md")
		text := readText(t, path)
		if !strings.Contains(text, "tao-managed: "+name+" v1") {
			t.Fatalf("expected tao-managed Pi prompt in %s, got %q", path, text)
		}
		if strings.Contains(text, "tao prompt "+name) {
			t.Fatalf("expected direct Pi template in %s, got wrapper content %q", path, text)
		}
	}
	if _, err := os.Readlink(filepath.Join(home, ".pi", "agent", "extensions", "tao")); err != nil {
		t.Fatalf("expected Pi Tao extension to be enabled: %v", err)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"install-prompts", "--check"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range promptNames {
		path := filepath.Join(root, name+".md")
		if !strings.Contains(out.String(), "current "+path) {
			t.Fatalf("expected current prompt status for %s, got %q", path, out.String())
		}
	}
}

func TestInstallPromptsRefusesUnmanagedFiles(t *testing.T) {
	clearTaoEnv(t)
	setPathExecutables(t, "pi")
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".pi", "agent", "prompts")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "slice.md"), []byte("custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"install-prompts"})
	if err == nil || !strings.Contains(err.Error(), "not tao-managed") {
		t.Fatalf("expected unmanaged file error, got %v", err)
	}
}

func TestDoctorReportsWrapperStatus(t *testing.T) {
	clearTaoEnv(t)
	setPathExecutables(t, "pi")
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"install-prompts"}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSIGreen(out.String())
	nameWidth := len("improve-codebase-architecture")
	for _, want := range []string{
		"selected runtime agent: pi",
		"prompts (pi):",
		"Pi prompt templates plus Tao commit extension command",
		fmt.Sprintf("%-*s ✓ current", nameWidth, "slice"),
		fmt.Sprintf("%-*s ✓ current", nameWidth, "run"),
		fmt.Sprintf("%-*s ✓ current", nameWidth, "improve-codebase-architecture"),
		fmt.Sprintf("%-*s ✓ current", nameWidth, "improve-documentation"),
		fmt.Sprintf("%-*s ✓ current", nameWidth, "repo-health"),
		fmt.Sprintf("%-*s ✓ current", nameWidth, "pr"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, out.String())
		}
	}
	if !strings.Contains(out.String(), colorGreen("✓ current  ")) {
		t.Fatalf("expected current wrappers, got %q", text)
	}
}

func TestDoctorReportsMissingAndValidatesUsage(t *testing.T) {
	clearTaoEnv(t)
	setPathExecutables(t, "pi")
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSIGreen(out.String())
	nameWidth := len("improve-codebase-architecture")
	for _, want := range []string{
		fmt.Sprintf("%-*s ⚠ missing", nameWidth, "slice"),
		fmt.Sprintf("%-*s ⚠ missing", nameWidth, "run"),
		fmt.Sprintf("%-*s ⚠ missing", nameWidth, "repo-health"),
		fmt.Sprintf("%-*s ⚠ missing", nameWidth, "pr"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected missing wrapper %q, got %q", want, text)
		}
	}

	err := app.Run(context.Background(), []string{"doctor", "extra"})
	if err == nil || !strings.Contains(err.Error(), "usage: tao doctor") {
		t.Fatalf("expected doctor agent argument error, got %v", err)
	}
}

func TestInstallPromptsAndDoctorUseSelectedClaudeAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TAO_AGENT", "claude")
	setPathExecutables(t, "claude")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"install-prompts"}); err != nil {
		t.Fatal(err)
	}

	commandsRoot := filepath.Join(home, ".claude", "commands")
	for _, name := range []string{"plan", "slice", "note-slice", "run", "commit", "repo-health", "pr"} {
		path := filepath.Join(commandsRoot, name+".md")
		text := readText(t, path)
		if !strings.Contains(text, "tao-managed: "+name+" v1") {
			t.Fatalf("expected Claude wrapper %s to contain its managed marker, got %q", path, text)
		}
		if name == "commit" {
			for _, want := range []string{"Use this active agent session", "tao commit --proposal-file"} {
				if !strings.Contains(text, want) {
					t.Fatalf("expected inline Claude commit wrapper %s to contain %q, got %q", path, want, text)
				}
			}
			if strings.Contains(text, "tao prompt commit") {
				t.Fatalf("Claude commit wrapper started a nested prompt handoff: %q", text)
			}
		} else {
			want := "tao prompt " + name + " --arguments-stdin <<'TAO_PROMPT_ARGUMENTS'"
			if !strings.Contains(text, want) || strings.Contains(text, "You are in ") {
				t.Fatalf("expected thin Claude wrapper %s to contain %q, got %q", path, want, text)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "extensions", "tao")); !os.IsNotExist(err) {
		t.Fatalf("expected Claude install not to enable Pi extension, got %v", err)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"install-prompts", "--check"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "current "+filepath.Join(commandsRoot, "commit.md")) {
		t.Fatalf("expected current Claude commit command status, got %q", out.String())
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSIGreen(out.String())
	for _, want := range []string{
		"selected runtime agent: claude",
		"prompts (claude):",
		"Claude Markdown slash commands that render tao prompts dynamically",
		"commit                        ✓ current",
		"✓ ok      claude (claude)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, text)
		}
	}
}

func TestInstallPromptsAndDoctorUseSelectedOpenCodeAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("TAO_OPENCODE_COMMANDS_DIR", "")
	t.Setenv("TAO_AGENT", "opencode")
	setPathExecutables(t, "opencode")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"install-prompts"}); err != nil {
		t.Fatal(err)
	}

	commandsRoot := filepath.Join(home, ".config", "opencode", "commands")
	agentModes := map[string]string{"plan": "plan", "note-slice": "build", "run": "build", "commit": "build", "repo-health": "plan", "pr": "build"}
	for _, name := range []string{"plan", "note-slice", "run", "commit", "repo-health", "pr"} {
		path := filepath.Join(commandsRoot, name+".md")
		text := readText(t, path)
		for _, want := range []string{"tao-managed: " + name + " v1", "agent: " + agentModes[name]} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected OpenCode command %s to contain %q, got %q", path, want, text)
			}
		}
		if name == "commit" {
			for _, want := range []string{"Use this active agent session", "tao commit --proposal-file"} {
				if !strings.Contains(text, want) {
					t.Fatalf("expected inline OpenCode commit command %s to contain %q, got %q", path, want, text)
				}
			}
			if strings.Contains(text, "tao prompt commit") {
				t.Fatalf("OpenCode commit command started a nested prompt handoff: %q", text)
			}
		} else {
			want := "!`tao prompt " + name + " --arguments \"$ARGUMENTS\"`"
			if !strings.Contains(text, want) || strings.Contains(text, "Run tao prompt") || strings.Contains(text, "--arguments-stdin") || strings.Contains(text, "You are in ") {
				t.Fatalf("expected thin Style B OpenCode wrapper in %s with %q, got %q", path, want, text)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "extensions", "tao")); !os.IsNotExist(err) {
		t.Fatalf("expected OpenCode install not to enable Pi extension, got %v", err)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"install-prompts", "--check"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "current "+filepath.Join(commandsRoot, "commit.md")) {
		t.Fatalf("expected current OpenCode commit command status, got %q", out.String())
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSIGreen(out.String())
	for _, want := range []string{
		"selected runtime agent: opencode",
		"prompts (opencode):",
		"OpenCode Markdown commands that render tao prompts dynamically",
		"commit                        ✓ current",
		"✓ ok      opencode (opencode)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, text)
		}
	}
}

func TestInstallPromptsAndDoctorUseSelectedPiAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TAO_AGENT", "pi")
	setPathExecutables(t, "pi")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"install-prompts"}); err != nil {
		t.Fatal(err)
	}

	piRoot := filepath.Join(home, ".pi", "agent", "prompts")
	for _, name := range []string{"plan", "slice", "note-slice", "run", "grill-me", "improve-codebase-architecture", "improve-documentation", "repo-health", "pr"} {
		path := filepath.Join(piRoot, name+".md")
		text := readText(t, path)
		if !strings.Contains(text, "tao-managed: "+name+" v1") {
			t.Fatalf("expected tao-managed Pi template in %s, got %q", path, text)
		}
		if strings.Contains(text, "tao prompt "+name) {
			t.Fatalf("expected direct Pi template in %s, got wrapper content %q", path, text)
		}
	}
	if _, err := os.Stat(filepath.Join(piRoot, "commit.md")); !os.IsNotExist(err) {
		t.Fatalf("expected Pi commit prompt not to be installed, got %v", err)
	}
	if _, err := os.Readlink(filepath.Join(home, ".pi", "agent", "extensions", "tao")); err != nil {
		t.Fatalf("expected Pi Tao extension to be enabled: %v", err)
	}
	planTemplate := readText(t, filepath.Join(piRoot, "plan.md"))
	for _, want := range []string{"You are in PLAN mode.", "# Planning Packet", "## Planning Topic", "$ARGUMENTS", "Ask user-facing clarification questions only in the final assistant response"} {
		if !strings.Contains(planTemplate, want) {
			t.Fatalf("expected installed Pi /plan template to contain %q, got %q", want, planTemplate)
		}
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "plan", "--arguments", "add Pi workflow"}); err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.Contains(text, "You are in PLAN mode.") || !strings.Contains(text, "add Pi workflow") {
		t.Fatalf("expected prompt command to render Pi-installed /plan content, got %q", text)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSIGreen(out.String())
	for _, want := range []string{
		"selected runtime agent: pi",
		"prompts (pi):",
		"Pi prompt templates plus Tao commit extension command",
		"plan                          ✓ current",
		"commit                        ✓ current",
		"extensions/tao",
		"✓ ok      pi (pi)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, text)
		}
	}
}

func TestInstallPromptsAndDoctorManageEveryInstalledAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TAO_AGENT", "claude")
	setPathExecutables(t, "codex", "pi")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}

	if err := app.Run(context.Background(), []string{"install-prompts"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	piOutput := "[pi] installed " + filepath.Join(home, ".pi", "agent", "prompts", "plan.md")
	codexOutput := "[codex] installed " + filepath.Join(home, ".codex", "prompts", "plan.md")
	if piIndex, codexIndex := strings.Index(text, piOutput), strings.Index(text, codexOutput); piIndex < 0 || codexIndex < 0 || piIndex >= codexIndex {
		t.Fatalf("expected Pi then Codex install output independent of TAO_AGENT, got %q", text)
	}
	if strings.Contains(text, "[claude]") || strings.Contains(text, "[opencode]") {
		t.Fatalf("expected only installed agents in output, got %q", text)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"install-prompts", "--check"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[pi] current ", "[codex] current "} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in check output, got %q", want, out.String())
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text = stripANSIGreen(out.String())
	for _, want := range []string{"selected runtime agent: claude", "prompts (pi):", "prompts (codex):", "✓ ok      pi (pi)", "✓ ok      codex (codex)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, text)
		}
	}
	if strings.Index(text, "prompts (pi):") >= strings.Index(text, "prompts (codex):") {
		t.Fatalf("expected registry-ordered prompt groups, got %q", text)
	}
	for _, heading := range []string{"tools dev:", "tools recommended:"} {
		if strings.Count(text, heading) != 1 {
			t.Fatalf("expected shared heading %q once, got %q", heading, text)
		}
	}
}

func TestInstallPromptsReportsNoSupportedAgents(t *testing.T) {
	clearTaoEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	setPathExecutables(t)
	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"install-prompts"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no supported agents found in PATH") {
		t.Fatalf("expected explicit no-agent output, got %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".pi")); !os.IsNotExist(err) {
		t.Fatalf("expected no Pi fallback installation, got %v", err)
	}
}

func TestDoctorReportsToolCategoriesAndMissingWarnings(t *testing.T) {
	clearTaoEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSIGreen(out.String())
	for _, want := range []string{
		"supported agents: none found in PATH",
		"tools required:",
		"no supported agent executables found",
		"tools dev:",
		"⚠ warning git (missing)",
		"tools recommended:",
		"⚠ warning rg (missing)",
		"⚠ warning fd (missing)",
		"⚠ warning AWS CLI (aws) (missing)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, text)
		}
	}
}

func TestDoctorReportsPresentToolsAndFdfindAlias(t *testing.T) {
	clearTaoEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	for _, name := range []string{"pi", "git", "go", "make", "rg", "fdfind", "aws"} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // G306: executable test stub needs exec bit
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSIGreen(out.String())
	for _, want := range []string{
		"✓ ok      pi (pi)",
		"✓ ok      git (git)",
		"✓ ok      rg (rg)",
		"✓ ok      fd (fdfind)",
		"✓ ok      AWS CLI (aws) (aws)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in doctor output, got %q", want, text)
		}
	}
	if !strings.Contains(out.String(), colorGreen("✓ ok     ")+" pi (pi)") {
		t.Fatalf("expected green ok tool status, got %q", out.String())
	}
}

func setPathExecutables(t *testing.T, names ...string) {
	t.Helper()
	bin := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // executable test stub needs exec bit
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
}

func stripANSIGreen(value string) string {
	return strings.NewReplacer("\x1b[32m", "", "\x1b[0m", "").Replace(value)
}

func TestPromptUsesEnvPolicyDefaults(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_COMMIT_POLICY", "slice")
	t.Setenv("TAO_EXECUTION_MODE", "current")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}

	if err := app.Run(context.Background(), []string{"prompt", "run", "--plan-dir", "/tmp/plan"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "`tao slice-complete` owns the recoverable commit transaction") || strings.Contains(text, "Commit with a message") {
		t.Fatalf("expected Tao-owned slice commit default from env, got %q", text)
	}
	if !strings.Contains(text, "Use the current branch where `tao run` started") || strings.Contains(text, "Create or reuse a single feature branch") {
		t.Fatalf("expected current branch default from env, got %q", text)
	}
}
