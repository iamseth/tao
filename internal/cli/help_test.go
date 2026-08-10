package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestUsageRendersGroupedLayout(t *testing.T) {
	var out bytes.Buffer
	if err := (App{Out: &out}).usage(); err != nil {
		t.Fatal(err)
	}

	output := strings.TrimRight(out.String(), "\n")
	wantPrefix := topLevelHelpTagline + "\n\nFind more information at: " + topLevelHelpURL + "\n\n"
	if !strings.HasPrefix(output, wantPrefix) {
		t.Fatalf("usage prefix = %q, want prefix %q", output, wantPrefix)
	}

	lastHeadingIndex := -1
	for _, group := range topLevelCommandGroups {
		heading := group.heading + ":"
		headingIndex := strings.Index(output, heading)
		if headingIndex == -1 {
			t.Fatalf("expected usage to contain group heading %q, got %q", heading, output)
		}
		if headingIndex < lastHeadingIndex {
			t.Fatalf("group heading %q appeared out of order in %q", heading, output)
		}
		lastHeadingIndex = headingIndex

		for _, commandName := range group.commands {
			want := topLevelHelpRow(t, commandName)
			if !strings.Contains(output, want) {
				t.Fatalf("expected usage to contain %q, got %q", want, output)
			}
		}
	}

	wantFooter := "Usage:\n  tao [--plans-dir DIR] <command> [options]\n\n" +
		`Use "tao <command> --help" for more information about a given command.`
	if !strings.Contains(output, wantFooter) {
		t.Fatalf("expected usage footer %q, got %q", wantFooter, output)
	}
}

func TestCommandHelpRendersOptionsExamplesAndFlaglessCommands(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"run", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Run pending slices for a Tao plan", "Examples:", "Options:", "--max-slices", "--auto-rework", "--max-rework-attempts", "Usage:\n  tao run (r)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected run help to contain %q, got %q", want, out.String())
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"show", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Show detailed status for a Tao plan", "Examples:", "  tao show my-plan", "Usage:\n  tao show (s) <plan-id-or-slug>"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected show help to contain %q, got %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Options:") {
		t.Fatalf("expected flagless show help to omit Options block, got %q", out.String())
	}
}

func TestPlanAndExecutionCommandHelpIncludesSubcommandsOptionsAndExamples(t *testing.T) {
	assertCommandOutputContains(t, "report help", []string{"report", "--help"},
		"Export one readable Tao plan", "Options:", "--output", "--planning-only", "--force", "tao report --output -")
	assertCommandOutputContains(t, "queue help", []string{"queue", "--help"},
		"Available Commands:", "add", "start", "status", "--auto-rework",
		"--max-rework-attempts", "maximum automatic rework cycles (0 disables) (default 5)")
	assertCommandOutputContains(t, "review help", []string{"review", "--help"}, "Options:", "--run", "Examples:")
}

func TestRepositoryWorkspaceAndMonitoringHelpIncludesSubcommandsAndOptions(t *testing.T) {
	assertCommandOutputContains(t, "repo help", []string{"repo", "--help"}, "Available Commands:", "list", "show", "doctor")
	assertCommandOutputContains(t, "cleanup help", []string{"cleanup", "--help"}, "Options:", "--dry-run", "--force")
	assertCommandOutputContains(t, "monitor help", []string{"monitor", "--help"}, "non-completed plans across registered repositories", "Heartbeats report process liveness", "--once", "--interval", "tao monitor --interval 5s")
}

func TestPromptSettingsAndOtherHelpIncludesOptions(t *testing.T) {
	assertCommandOutputContains(t, "update help", []string{"update", "--help"},
		"latest stable Tao release", "TAO_UPDATE=off", "Usage:\n  tao update")
	assertCommandOutputContains(t, "prompt help", []string{"prompt", "--help"}, "Render one of Tao's built-in prompt templates", "Options:", "--execution-mode", "Usage:\n  tao prompt (p)")
	assertCommandOutputContains(t, "doctor help", []string{"doctor", "--help"}, "actionable prompt and tool problems", "Options:", "--verbose", "-v", "tao doctor --verbose")
	assertCommandOutputContains(t, "capture help", []string{"capture-planning-session", "--help"}, "no longer supported", "Options:", "--plan-dir")
}

func TestEveryRegisteredCommandRendersPerCommandHelp(t *testing.T) {
	for i := range commandRegistry {
		metadata := &commandRegistry[i]
		t.Run(metadata.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := renderCommandHelp(&out, metadata); err != nil {
				t.Fatal(err)
			}
			output := out.String()
			if strings.TrimSpace(output) == "" {
				t.Fatal("expected non-empty command help")
			}
			if !strings.Contains(output, "Usage:") {
				t.Fatalf("expected command help to contain Usage, got %q", output)
			}
		})
	}
}

func TestRunHandlesHelpCompletionAndUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		topLevelHelpTagline,
		"Plan Commands:",
		"Execution Commands:",
		"Usage:\n  tao [--plans-dir DIR] <command> [options]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected usage to contain %q, got %q", want, out.String())
		}
	}
	for _, commandName := range []string{"cleanup", "run", "repo", "validate", "delete", "monitor", "prompt", "workspace", "completion", "update"} {
		want := topLevelHelpRow(t, commandName)
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected usage to contain %q, got %q", want, out.String())
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"completion", "zsh"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#compdef tao") {
		t.Fatalf("expected completion output, got %q", out.String())
	}
	if !strings.Contains(out.String(), "'l:List plans'") {
		t.Fatalf("expected shortest aliases in completion output, got %q", out.String())
	}
	if !strings.Contains(out.String(), "l|li|lis|list)") {
		t.Fatalf("expected list aliases in completion case handling, got %q", out.String())
	}
	if !strings.Contains(out.String(), "co|com|comp|compl|comple|completi|completio|completion)") { //nolint:misspell // intentional completion-prefix fixture
		t.Fatalf("expected completion aliases in completion case handling, got %q", out.String())
	}
	if !strings.Contains(out.String(), "de|del|dele|delet|delete)") {
		t.Fatalf("expected delete aliases in completion case handling, got %q", out.String())
	}
	if !strings.Contains(out.String(), "repo)") || !strings.Contains(out.String(), "'show:Show details for one registered repository'") {
		t.Fatalf("expected repo subcommands in completion output, got %q", out.String())
	}
	if !strings.Contains(out.String(), "rev|revi|revie|review)") || strings.Contains(out.String(), "[review mode]") {
		t.Fatalf("expected registry-generated review completion without the removed review mode flag, got %q", out.String())
	}
	if strings.Contains(out.String(), "--agent") {
		t.Fatalf("expected completion output to omit --agent, got %q", out.String())
	}
	if !strings.Contains(out.String(), "w|wo|wor|work|works|worksp|workspa|workspac|workspace)") || !strings.Contains(out.String(), "--force-dirty[allow cleaning a dirty or unmerged workspace]") {
		t.Fatalf("expected workspace aliases and flags in completion output, got %q", out.String())
	}
	for _, want := range []string{"--commit-policy[automatic commit policy: slice or none]:policy:(slice none)", "--commit-policy[run prompt commit policy: slice or none]:policy:(slice none)", "--execution-mode[execution mode: isolated or current]:mode:(isolated current)", "--execution-mode[run prompt execution mode: isolated or current]:mode:(isolated current)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected runtime completion %q, got %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), ":policy:(plan ") {
		t.Fatalf("expected completions to omit plan as an executable commit policy, got %q", out.String())
	}

	if err := app.Run(context.Background(), []string{"wat"}); err == nil {
		t.Fatal("expected unknown command error")
	}
}

func TestRunHandlesNoArgsAndGlobalFlagErrors(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Usage:\n  tao") {
		t.Fatalf("expected usage for no args, got %q", out.String())
	}
	if err := app.Run(context.Background(), []string{"--plans-dir"}); err == nil {
		t.Fatal("expected missing plans-dir value error")
	}
	if err := app.Run(context.Background(), []string{"--plans-dir="}); err == nil {
		t.Fatal("expected empty plans-dir value error")
	}
}

func TestCommandHelpReflectsRuntimeEnvDefaults(t *testing.T) {
	t.Setenv("TAO_COMMIT_POLICY", "slice")
	t.Setenv("TAO_EXECUTION_MODE", "current")
	t.Setenv("TAO_PULL_REQUEST", "true")
	t.Setenv("TAO_REVIEW", "false")
	t.Setenv("TAO_DANGEROUSLY_SKIP_PERMISSIONS", "true")
	t.Setenv("TAO_AUTO_REWORK", "true")
	t.Setenv("TAO_MAX_REWORK_ATTEMPTS", "3")

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"run", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--commit-policy",
		"automatic commit policy: slice or none (default slice)",
		"execution mode: isolated or current (default current)",
		"create a GitHub pull request after a completed full run (default true)",
		"disable automatic plan review for this run (default true)",
		"automatically rework plans with requested changes (default true)",
		"maximum automatic rework cycles (0 disables) (default 3)",
		"legacy no-op for the Pi agent (default true)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected run help to reflect env default %q, got %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "plan, slice") || strings.Contains(out.String(), "plan|slice") {
		t.Fatalf("expected run help to omit plan as an executable commit policy, got %q", out.String())
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"queue", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"automatically rework plans with requested changes (default true)",
		"maximum automatic rework cycles (0 disables) (default 3)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected queue help to reflect env default %q, got %q", want, out.String())
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"prompt", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"run prompt commit policy: slice or none (default slice)",
		"run prompt execution mode: isolated or current (default current)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected prompt help to reflect env default %q, got %q", want, out.String())
		}
	}
}

func assertCommandOutputContains(t *testing.T, label string, args []string, wants ...string) {
	t.Helper()
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %s to contain %q, got %q", label, want, output)
		}
	}
}

func topLevelHelpRow(t *testing.T, commandName string) string {
	t.Helper()
	metadata := commandByName(commandName)
	if metadata == nil {
		t.Fatalf("unknown command %q", commandName)
	}
	return "  " + pad(commandName, topLevelCommandNameWidth()+2) + metadata.completionDescription
}
