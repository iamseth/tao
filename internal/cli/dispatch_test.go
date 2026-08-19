package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/selfupdate"
)

func TestCommandRegistryOrder(t *testing.T) {
	want := []struct {
		name      string
		minPrefix string
	}{
		{name: "list", minPrefix: "l"},
		{name: "version"},
		{name: "update"},
		{name: "init"},
		{name: "log", minPrefix: "lo"},
		{name: "run", minPrefix: "r"},
		{name: "commit"},
		{name: "repo", minPrefix: "repo"},
		{name: "note", minPrefix: "n"},
		{name: "approve", minPrefix: "a"},
		{name: "slice-complete"},
		{name: "slice-blocked"},
		{name: "show", minPrefix: "s"},
		{name: "report"},
		{name: "review", minPrefix: "rev"},
		{name: "rework", minPrefix: "rew"},
		{name: "staleness", minPrefix: "stale"},
		{name: "validate", minPrefix: "v"},
		{name: "status", minPrefix: "st"},
		{name: "monitor", minPrefix: "mon"},
		{name: "ui"},
		{name: "insights", minPrefix: "insi"},
		{name: "cleanup", minPrefix: "c"},
		{name: "merge", minPrefix: "m"},
		{name: "delete", minPrefix: "de"},
		{name: "edit", minPrefix: "e"},
		{name: "capture-planning-session", minPrefix: "cap"},
		{name: "prompt", minPrefix: "p"},
		{name: "draft-prompt", minPrefix: "dr"},
		{name: "install-prompts", minPrefix: "i"},
		{name: "doctor", minPrefix: "d"},
		{name: "completion", minPrefix: "co"},
		{name: "workspace", minPrefix: "w"},
	}

	if len(commandRegistry) != len(want) {
		t.Fatalf("commandRegistry length = %d, want %d", len(commandRegistry), len(want))
	}
	for i, wantCommand := range want {
		got := commandRegistry[i]
		if got.name != wantCommand.name || got.minPrefix != wantCommand.minPrefix {
			t.Fatalf("commandRegistry[%d] = {name:%q minPrefix:%q}, want {name:%q minPrefix:%q}", i, got.name, got.minPrefix, wantCommand.name, wantCommand.minPrefix)
		}
		if got.execute == nil {
			t.Fatalf("commandRegistry[%d] %q missing executor", i, got.name)
		}
		if got.completionDescription == "" {
			t.Fatalf("commandRegistry[%d] %q missing completion description", i, got.name)
		}
		if len(got.usageLines) == 0 {
			t.Fatalf("commandRegistry[%d] %q missing usage", i, got.name)
		}
	}
}

func TestCommandPrefixNormalizationUsesRegistryOrder(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "i", want: "install-prompts"},
		{name: "in", want: "install-prompts"},
		{name: "inst", want: "install-prompts"},
		{name: "c", want: "cleanup"},
		{name: "ca", want: "ca"},
		{name: "cap", want: "capture-planning-session"},
		{name: "co", want: "completion"},
		{name: "n", want: "note"},
		{name: "not", want: "note"},
		{name: "complet", want: "completion"},
		{name: "complete", want: "complete"},
		{name: "version", want: "version"},
		{name: "update", want: "update"},
		{name: "up", want: "up"},
		{name: "ve", want: "ve"},
		{name: "--version", want: "--version"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeCommand(test.name); got != test.want {
				t.Fatalf("normalizeCommand(%q) = %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func TestCommandPrefixNormalization(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "l", want: "list"},
		{name: "li", want: "list"},
		{name: "lis", want: "list"},
		{name: "list", want: "list"},
		{name: "lo", want: "log"},
		{name: "log", want: "log"},
		{name: "r", want: "run"},
		{name: "ru", want: "run"},
		{name: "run", want: "run"},
		{name: "se", want: "se"},
		{name: "serv", want: "serv"},
		{name: "repo", want: "repo"},
		{name: "report", want: "report"},
		{name: "repor", want: "repor"},
		{name: "re", want: "re"},
		{name: "rev", want: "review"},
		{name: "revi", want: "review"},
		{name: "review", want: "review"},
		{name: "rew", want: "rework"},
		{name: "rewo", want: "rework"},
		{name: "rework", want: "rework"},
		{name: "removed", want: "removed"},
		{name: "stale", want: "staleness"},
		{name: "stalen", want: "staleness"},
		{name: "staleness", want: "staleness"},
		{name: "s", want: "show"},
		{name: "sh", want: "show"},
		{name: "sho", want: "show"},
		{name: "show", want: "show"},
		{name: "st", want: "status"},
		{name: "sta", want: "status"},
		{name: "status", want: "status"},
		{name: "mon", want: "monitor"},
		{name: "monito", want: "monitor"},
		{name: "monitor", want: "monitor"},
		{name: "v", want: "validate"},
		{name: "va", want: "validate"},
		{name: "val", want: "validate"},
		{name: "validate", want: "validate"},
		{name: "c", want: "cleanup"},
		{name: "cl", want: "cleanup"},
		{name: "cle", want: "cleanup"},
		{name: "clea", want: "cleanup"},
		{name: "clean", want: "cleanup"},
		{name: "cleanu", want: "cleanup"},
		{name: "cleanup", want: "cleanup"},
		{name: "m", want: "merge"},
		{name: "me", want: "merge"},
		{name: "mer", want: "merge"},
		{name: "merge", want: "merge"},
		{name: "de", want: "delete"},
		{name: "del", want: "delete"},
		{name: "dele", want: "delete"},
		{name: "delet", want: "delete"},
		{name: "delete", want: "delete"},
		{name: "co", want: "completion"},
		{name: "com", want: "completion"},
		{name: "comp", want: "completion"},
		{name: "compl", want: "completion"},
		{name: "comple", want: "completion"},
		{name: "completi", want: "completion"}, //nolint:misspell // intentional completion-prefix fixture
		{name: "completio", want: "completion"},
		{name: "completion", want: "completion"},
		{name: "p", want: "prompt"},
		{name: "pr", want: "prompt"},
		{name: "pro", want: "prompt"},
		{name: "prom", want: "prompt"},
		{name: "promp", want: "prompt"},
		{name: "prompt", want: "prompt"},
		{name: "dr", want: "draft-prompt"},
		{name: "dra", want: "draft-prompt"},
		{name: "draft", want: "draft-prompt"},
		{name: "draft-p", want: "draft-prompt"},
		{name: "draft-pr", want: "draft-prompt"},
		{name: "draft-pro", want: "draft-prompt"},
		{name: "draft-prom", want: "draft-prompt"},
		{name: "draft-promp", want: "draft-prompt"},
		{name: "draft-prompt", want: "draft-prompt"},
		{name: "i", want: "install-prompts"},
		{name: "in", want: "install-prompts"},
		{name: "ins", want: "install-prompts"},
		{name: "insi", want: "insights"},
		{name: "insig", want: "insights"},
		{name: "insights", want: "insights"},
		{name: "inst", want: "install-prompts"},
		{name: "insta", want: "install-prompts"},
		{name: "instal", want: "install-prompts"},
		{name: "install", want: "install-prompts"},
		{name: "install-", want: "install-prompts"},
		{name: "install-p", want: "install-prompts"},
		{name: "install-pr", want: "install-prompts"},
		{name: "install-pro", want: "install-prompts"},
		{name: "install-prom", want: "install-prompts"},
		{name: "install-promp", want: "install-prompts"},
		{name: "install-prompt", want: "install-prompts"},
		{name: "install-prompts", want: "install-prompts"},
		{name: "d", want: "doctor"},
		{name: "do", want: "doctor"},
		{name: "doc", want: "doctor"},
		{name: "doct", want: "doctor"},
		{name: "docto", want: "doctor"},
		{name: "doctor", want: "doctor"},
		{name: "complete", want: "complete"},
		{name: "up", want: "up"},
		{name: "update", want: "update"},
		{name: "wat", want: "wat"},
		{name: "", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeCommand(test.name); got != test.want {
				t.Fatalf("normalizeCommand(%q) = %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func TestCommandPrefixDispatch(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, Repository: func(plansDir string) Repository {
		return fakeRepository{summaries: []plan.PlanSummary{{ID: "20260427-1810-example", Title: "Example", Status: plan.StatusPlanned}}}
	}}

	if err := app.Run(context.Background(), []string{"l"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "example") {
		t.Fatalf("expected list prefix to render plans, got %q", out.String())
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"co", "zsh"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#compdef tao") {
		t.Fatalf("expected completion prefix to render zsh completion, got %q", out.String())
	}
}

func TestRunVersionOutputsBuildMetadata(t *testing.T) {
	t.Setenv("TAO_UPDATE", "off")
	originalBuildVersion := buildVersion
	originalBuildCommit := buildCommit
	originalBuildAge := buildAge
	buildVersion = func() string { return "v0.1.0" }
	buildCommit = func() string { return "abc1234" }
	buildAge = func() string { return "2 days old" }
	t.Cleanup(func() {
		buildVersion = originalBuildVersion
		buildCommit = originalBuildCommit
		buildAge = originalBuildAge
	})

	for _, args := range [][]string{{"version"}, {"--version"}} {
		var out bytes.Buffer
		app := App{Out: &out, Err: &out}
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatalf("Run(%v) failed: %v", args, err)
		}
		want := "tao v0.1.0\ncommit: abc1234\nbuild age: 2 days old\n"
		if got := out.String(); got != want {
			t.Fatalf("Run(%v) output = %q, want %q", args, got, want)
		}
		if firstLine, _, _ := strings.Cut(out.String(), "\n"); firstLine != "tao v0.1.0" {
			t.Fatalf("Run(%v) first line = %q, want %q", args, firstLine, "tao v0.1.0")
		}
	}
}

func TestRunVersionFallsBackToDevForLocalBuilds(t *testing.T) {
	t.Setenv("TAO_UPDATE", "off")
	originalBuildVersion := buildVersion
	originalBuildCommit := buildCommit
	originalBuildAge := buildAge
	buildVersion = func() string { return "dev" }
	buildCommit = func() string { return "abc1234" }
	buildAge = func() string { return "2 days old" }
	t.Cleanup(func() {
		buildVersion = originalBuildVersion
		buildCommit = originalBuildCommit
		buildAge = originalBuildAge
	})

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("Run(version) failed: %v", err)
	}
	want := "tao dev\ncommit: abc1234\nbuild age: 2 days old\n"
	if got := out.String(); got != want {
		t.Fatalf("Run(version) output = %q, want %q", got, want)
	}
}

func TestRunVersionHandlesUnknownBuildAge(t *testing.T) {
	t.Setenv("TAO_UPDATE", "off")
	originalBuildVersion := buildVersion
	originalBuildCommit := buildCommit
	originalBuildAge := buildAge
	buildVersion = func() string { return "v0.1.0" }
	buildCommit = func() string { return "abc1234" }
	buildAge = func() string { return "unknown" }
	t.Cleanup(func() {
		buildVersion = originalBuildVersion
		buildCommit = originalBuildCommit
		buildAge = originalBuildAge
	})

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"--version"}); err != nil {
		t.Fatalf("Run(--version) failed: %v", err)
	}
	want := "tao v0.1.0\ncommit: abc1234\nbuild age: unknown\n"
	if got := out.String(); got != want {
		t.Fatalf("Run(--version) output = %q, want %q", got, want)
	}
}

func TestRunDoesNotAddShortVersionAlias(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	err := app.Run(context.Background(), []string{"v"})
	if err == nil || !strings.Contains(err.Error(), "usage: tao validate") {
		t.Fatalf("expected v to remain validate prefix, got %v", err)
	}
}

func TestParseGlobalFlags(t *testing.T) {
	var dir string
	args, err := parseGlobalFlags([]string{"--plans-dir", "/tmp/plans", "list"}, &dir)
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/tmp/plans" || len(args) != 1 || args[0] != "list" {
		t.Fatalf("unexpected parse result dir=%q args=%v", dir, args)
	}
}

func TestParseGlobalFlagsEqualsForm(t *testing.T) {
	var dir string
	args, err := parseGlobalFlags([]string{"--plans-dir=/tmp/plans", "list"}, &dir)
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/tmp/plans" || len(args) != 1 || args[0] != "list" {
		t.Fatalf("unexpected parse result dir=%q args=%v", dir, args)
	}
}

func TestStartupUpdateRunsForRecognizedUserFacingCommandClasses(t *testing.T) {
	t.Setenv("TAO_UPDATE", "")
	tests := []struct {
		name string
		args []string
	}{
		{name: "top-level help command", args: []string{"help"}},
		{name: "top-level short help", args: []string{"-h"}},
		{name: "top-level long help", args: []string{"--help"}},
		{name: "per-command help", args: []string{"list", "--help"}},
		{name: "version command", args: []string{"version"}},
		{name: "version flag", args: []string{"--version"}},
		{name: "ordinary command", args: []string{"list"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updater := &fakeSelfUpdater{}
			var out, errOut bytes.Buffer
			repositoryCalls := 0
			app := App{
				Out:         &out,
				Err:         &errOut,
				SelfUpdater: updater,
				Repository: func(string) Repository {
					repositoryCalls++
					return fakeRepository{}
				},
			}
			if err := app.Run(context.Background(), test.args); err != nil {
				t.Fatalf("Run(%v) failed: %v", test.args, err)
			}
			if len(updater.startupModes) != 1 || updater.startupModes[0] != selfupdate.ModeWarn {
				t.Fatalf("startup modes = %v, want [warn]", updater.startupModes)
			}
			if test.name != "ordinary command" && repositoryCalls != 0 {
				t.Fatalf("help/version opened %d repositories", repositoryCalls)
			}
		})
	}
}

func TestStartupUpdateExcludesInternalAndExplicitCommandsAndUnrecognizedInput(t *testing.T) {
	t.Setenv("TAO_UPDATE", "invalid-for-excluded-commands")
	tests := []struct {
		name       string
		args       []string
		wantError  bool
		wantUpdate int
	}{
		{name: "explicit update", args: []string{"update"}, wantUpdate: 1},
		{name: "explicit update help", args: []string{"update", "--help"}},
		{name: "hidden completion", args: []string{"complete", "plan-ids"}},
		{name: "no arguments"},
		{name: "unknown command", args: []string{"unknown"}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updater := &fakeSelfUpdater{result: selfupdate.UpdateResult{
				CurrentVersion: "v1.0.0",
				LatestVersion:  "v1.0.0",
				Comparison:     selfupdate.VersionCurrent,
			}}
			var out bytes.Buffer
			app := App{Out: &out, Err: &bytes.Buffer{}, SelfUpdater: updater, Repository: func(string) Repository { return fakeRepository{} }}
			err := app.Run(context.Background(), test.args)
			if (err != nil) != test.wantError {
				t.Fatalf("Run(%v) error = %v, wantError %t", test.args, err, test.wantError)
			}
			if len(updater.startupModes) != 0 {
				t.Fatalf("startup modes = %v, want none", updater.startupModes)
			}
			if updater.calls != test.wantUpdate {
				t.Fatalf("explicit update calls = %d, want %d", updater.calls, test.wantUpdate)
			}
		})
	}
}

func TestStartupUpdateModesAndInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		value    string
		wantMode selfupdate.Mode
		wantCall bool
	}{
		{value: "", wantMode: selfupdate.ModeWarn, wantCall: true},
		{value: "warn", wantMode: selfupdate.ModeWarn, wantCall: true},
		{value: "auto", wantMode: selfupdate.ModeAuto, wantCall: true},
		{value: "off"},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("TAO_UPDATE", test.value)
			updater := &fakeSelfUpdater{}
			if err := (App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, SelfUpdater: updater}).Run(context.Background(), []string{"version"}); err != nil {
				t.Fatal(err)
			}
			if test.wantCall {
				if len(updater.startupModes) != 1 || updater.startupModes[0] != test.wantMode {
					t.Fatalf("startup modes = %v, want [%s]", updater.startupModes, test.wantMode)
				}
			} else if len(updater.startupModes) != 0 {
				t.Fatalf("off mode called startup with %v", updater.startupModes)
			}
		})
	}

	t.Setenv("TAO_UPDATE", "sometimes")
	updater := &fakeSelfUpdater{}
	var out bytes.Buffer
	err := (App{Out: &out, Err: &bytes.Buffer{}, SelfUpdater: updater}).Run(context.Background(), []string{"version"})
	if err == nil || !strings.Contains(err.Error(), "TAO_UPDATE: invalid update mode") || !strings.Contains(err.Error(), "warn, auto, or off") {
		t.Fatalf("invalid mode error = %v", err)
	}
	if out.Len() != 0 || len(updater.startupModes) != 0 {
		t.Fatalf("invalid mode ran command or updater: stdout=%q modes=%v", out.String(), updater.startupModes)
	}
}

func TestStartupUpdateNoticesAndFailuresStayOffStdout(t *testing.T) {
	t.Setenv("TAO_UPDATE", "warn")
	discoveryFailure := errors.New("release discovery unavailable")
	cacheFailure := &selfupdate.PersistenceError{Operation: "read", Path: "/cache/self-update.json", Err: errors.New("permission denied")}
	tests := []struct {
		name        string
		outcome     selfupdate.StartupOutcome
		wantStderr  string
		quietStderr bool
	}{
		{name: "warn notice", outcome: selfupdate.StartupOutcome{Notice: "Tao v1.1.0 is available"}, wantStderr: "notice: Tao v1.1.0 is available"},
		{name: "discovery failure", outcome: selfupdate.StartupOutcome{Failure: discoveryFailure}, quietStderr: true},
		{name: "cache failure", outcome: selfupdate.StartupOutcome{Failure: cacheFailure}, quietStderr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updater := &fakeSelfUpdater{startupOutcome: test.outcome}
			var out, errOut bytes.Buffer
			app := App{Out: &out, Err: &errOut, SelfUpdater: updater, Repository: func(string) Repository { return fakeRepository{} }}
			if err := app.Run(context.Background(), []string{"status", "--json"}); err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatalf("stdout is not clean JSON: %v\n%s", err, out.String())
			}
			if test.quietStderr && errOut.Len() != 0 {
				t.Fatalf("failure reached stderr: %q", errOut.String())
			}
			if test.wantStderr != "" && !strings.Contains(errOut.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want %q", errOut.String(), test.wantStderr)
			}
		})
	}
}

func TestStartupAutomaticInstallFailureWarningIsNonFatalAndThrottled(t *testing.T) {
	t.Setenv("TAO_UPDATE", "auto")
	installFailure := errors.New("replace Tao executable: read-only file system")
	updater := &fakeSelfUpdater{startupOutcomes: []selfupdate.StartupOutcome{
		{Failure: installFailure, AutomaticInstallFailure: installFailure},
		{},
	}}
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut, SelfUpdater: updater}
	for range 2 {
		if err := app.Run(context.Background(), []string{"version"}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(errOut.String(), "warning: automatic Tao update failed") != 1 || !strings.Contains(errOut.String(), "read-only file system") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if strings.Contains(out.String(), "automatic Tao update") || strings.Count(out.String(), "tao ") != 2 {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestStartupAutomaticInstallRunsRequestedCommandOnceWithoutReexec(t *testing.T) {
	t.Setenv("TAO_UPDATE", "auto")
	originalBuildVersion := buildVersion
	buildVersion = func() string { return "v1.0.0" }
	t.Cleanup(func() { buildVersion = originalBuildVersion })
	updater := &fakeSelfUpdater{startupOutcome: selfupdate.StartupOutcome{
		UpdateResult: selfupdate.UpdateResult{Installed: true, LatestVersion: "v1.1.0", Path: "/usr/local/bin/tao"},
		Notice:       "Tao v1.1.0 was installed and will take effect on the next invocation",
	}}
	var out, errOut bytes.Buffer
	if err := (App{Out: &out, Err: &errOut, SelfUpdater: updater}).Run(context.Background(), []string{"version"}); err != nil {
		t.Fatal(err)
	}
	if len(updater.startupModes) != 1 || strings.Count(out.String(), "tao v1.0.0") != 1 {
		t.Fatalf("startup modes=%v stdout=%q", updater.startupModes, out.String())
	}
	if !strings.Contains(errOut.String(), "next invocation") || strings.Contains(out.String(), "next invocation") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestStartupNoticeSuppressionHandoff(t *testing.T) {
	t.Setenv("TAO_UPDATE", "warn")
	updater := &fakeSelfUpdater{startupOutcomes: []selfupdate.StartupOutcome{
		{Notice: "Tao v1.1.0 is available"},
		{},
	}}
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut, SelfUpdater: updater}
	for range 2 {
		if err := app.Run(context.Background(), []string{"version"}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(errOut.String(), "Tao v1.1.0 is available") != 1 {
		t.Fatalf("notice was not suppressed after delivery: %q", errOut.String())
	}
	if strings.Count(out.String(), "tao ") != 2 {
		t.Fatalf("requested command executions = %d, want 2", strings.Count(out.String(), "tao "))
	}
}
