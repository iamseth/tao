package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestRunInvokesPiUntilPlanCompletedAndLogsOutput(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	var out bytes.Buffer
	var calls int
	var gotName string
	var gotCwd string
	var prompts []string
	app := App{Out: &out, Err: &out, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "sqlite3" {
			return errors.New("sqlite unavailable")
		}
		if name == "git" {
			writeRunGitOutput(stdout, args)
			return nil
		}
		return nil
	}, ProcessStarter: fakeCLIProcessStarter(t, "pi stdout", func(prompt string) {
		calls++
		gotName = "pi"
		gotCwd, _ = os.Getwd()
		prompts = append(prompts, prompt)
		if calls == 1 {
			fixture.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
		}
	})}

	err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--commit-policy", "none", "--no-review", fixture.id})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one slice Pi call, got %d", calls)
	}
	if gotName != "pi" {
		t.Fatalf("unexpected command %q", gotName)
	}
	wantCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if gotCwd != wantCwd {
		t.Fatalf("expected Pi cwd %q, got %q", wantCwd, gotCwd)
	}
	if !strings.Contains(prompts[0], "Plan directory: `"+fixture.dir+"`") {
		t.Fatalf("expected rendered transactional work prompt with plan dir, got %q", prompts[0])
	}
	text := out.String()
	for _, want := range []string{"Running slice 001-a", "running 001-a ---\n", "Slice completed: 001-a", "Plan slices complete: " + fixture.id} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in tao output:\n%s", want, text)
		}
	}
	if strings.Contains(text, logrecord.Prefix) {
		t.Fatalf("tao output exposed persisted agent-log framing:\n%s", text)
	}
	logText := readText(t, plan.LogPath(fixture.dir))
	for _, want := range []string{logrecord.Prefix, "running 001-a"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in agent log:\n%s", want, logText)
		}
	}
}

func TestCLIEnvDefaultsPreserveBuiltInsWhenUnset(t *testing.T) {
	clearTaoEnv(t)

	defaults, err := cliEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.CommitPolicy != runtimeconfig.CommitPolicySlice || defaults.ExecutionMode != runtimeconfig.ExecutionModeIsolated || defaults.Agent != runtimeconfig.AgentPi || defaults.PullRequest != nil || defaults.SkipPermissions {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}
}

func TestCLIEnvDefaultsAcceptValidValues(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_COMMIT_POLICY", "slice")
	t.Setenv("TAO_EXECUTION_MODE", "current")
	t.Setenv("TAO_AGENT", "pi")
	t.Setenv("TAO_PULL_REQUEST", "true")
	t.Setenv("TAO_DANGEROUSLY_SKIP_PERMISSIONS", "true")

	defaults, err := cliEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.CommitPolicy != runtimeconfig.CommitPolicySlice || defaults.ExecutionMode != runtimeconfig.ExecutionModeCurrent || defaults.Agent != runtimeconfig.AgentPi || defaults.PullRequest == nil || !*defaults.PullRequest || !defaults.SkipPermissions {
		t.Fatalf("unexpected env defaults: %#v", defaults)
	}
}

func TestCLIEnvDefaultsRejectInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "commit policy", key: "TAO_COMMIT_POLICY", value: "always"},
		{name: "execution mode", key: "TAO_EXECUTION_MODE", value: "sandbox"},
		{name: "agent", key: "TAO_AGENT", value: "other"},
		{name: "pull request", key: "TAO_PULL_REQUEST", value: "maybe"},
		{name: "skip permissions", key: "TAO_DANGEROUSLY_SKIP_PERMISSIONS", value: "maybe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearTaoEnv(t)
			t.Setenv(test.key, test.value)

			_, err := cliEnvDefaults()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("expected error naming %s, got %v", test.key, err)
			}
		})
	}
}

func TestCLIEnvExecutionModeDefaultsNormalizeRunRequest(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_EXECUTION_MODE", "current")

	defaults, err := cliEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	request, err := defaults.newRunRequest("plan-a", runtimeconfig.RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if request.ExecutionMode != runtimeconfig.ExecutionModeCurrent {
		t.Fatalf("expected current execution mode request, got %#v", request)
	}
}

func TestRunExecutionModeFlagReachesRunRequest(t *testing.T) {
	buildRequest := func(t *testing.T, args []string) run.Request {
		t.Helper()
		defaults, err := cliEnvDefaults()
		if err != nil {
			t.Fatal(err)
		}
		fs := App{Err: io.Discard}.flagSet("run")
		executionMode := fs.String("execution-mode", defaults.ExecutionModeValue().String(), "execution mode")
		flagArgs, positional := splitSubcommandFlags(args, map[string]bool{"--execution-mode": true})
		if err := fs.Parse(flagArgs); err != nil {
			t.Fatal(err)
		}
		if len(positional) != 1 {
			t.Fatalf("expected one plan positional, got %#v", positional)
		}
		requestOptions := runRequestOverridesFromFlags(fs, runFlagValues{ExecutionMode: runtimeconfig.ExecutionMode(*executionMode)})
		request, err := defaults.newRunRequest(positional[0], requestOptions)
		if err != nil {
			t.Fatal(err)
		}
		return request
	}

	tests := []struct {
		name     string
		env      string
		args     []string
		wantMode runtimeconfig.ExecutionMode
	}{
		{name: "implicit default is isolated", args: []string{"plan-a"}, wantMode: runtimeconfig.ExecutionModeIsolated},
		{name: "isolated flag", args: []string{"--execution-mode", "isolated", "plan-a"}, wantMode: runtimeconfig.ExecutionModeIsolated},
		{name: "current flag", args: []string{"--execution-mode", "current", "plan-a"}, wantMode: runtimeconfig.ExecutionModeCurrent},
		{name: "flag overrides env current", env: "current", args: []string{"--execution-mode", "isolated", "plan-a"}, wantMode: runtimeconfig.ExecutionModeIsolated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearTaoEnv(t)
			if tt.env != "" {
				t.Setenv("TAO_EXECUTION_MODE", tt.env)
			}
			request := buildRequest(t, tt.args)
			if request.ExecutionMode != tt.wantMode {
				t.Fatalf("unexpected execution mode request: %#v", request)
			}
		})
	}
}

func TestCLIRunRequestBuilderPreservesEnvDefaultsAndFlagOverrides(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_COMMIT_POLICY", "slice")
	t.Setenv("TAO_EXECUTION_MODE", "current")
	t.Setenv("TAO_AGENT", "pi")
	t.Setenv("TAO_PULL_REQUEST", "true")

	defaults, err := cliEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	fs := App{Err: io.Discard}.flagSet("run")
	maxSlices := fs.Int("max-slices", 0, "maximum slices")
	commitPolicy := fs.String("commit-policy", defaults.CommitPolicy.String(), "commit policy")
	executionMode := fs.String("execution-mode", defaults.ExecutionModeValue().String(), "execution mode")
	pullRequest := fs.Bool("pull-request", defaults.PullRequestValue(), "pull request")
	continueBlocked := fs.Bool("continue", false, "continue")
	flagArgs, _ := splitSubcommandFlags([]string{"--max-slices", "2", "--commit-policy", "none", "--execution-mode", "isolated", "--pull-request=false", "--continue=false"}, map[string]bool{"--max-slices": true, "--commit-policy": true, "--execution-mode": true})
	if err := fs.Parse(flagArgs); err != nil {
		t.Fatal(err)
	}
	requestOptions := runRequestOverridesFromFlags(fs, runFlagValues{
		MaxSlices:     *maxSlices,
		CommitPolicy:  runtimeconfig.CommitPolicy(*commitPolicy),
		ExecutionMode: runtimeconfig.ExecutionMode(*executionMode),
		PullRequest:   *pullRequest,
		Continue:      *continueBlocked,
	})
	request, err := defaults.newRunRequest("plan-a", requestOptions)
	if err != nil {
		t.Fatal(err)
	}
	if request.Input != "plan-a" || request.MaxSlices != 2 || request.Continue {
		t.Fatalf("unexpected run request shape: %#v", request)
	}
	if request.ExecutionMode != runtimeconfig.ExecutionModeIsolated {
		t.Fatalf("expected isolated execution-mode flag to win over env current, got %#v", request)
	}
	if request.CommitPolicy != runtimeconfig.CommitPolicyNone {
		t.Fatalf("expected commit-policy flag override, got %#v", request)
	}
	if request.Agent != runtimeconfig.AgentPi || request.PullRequest {
		t.Fatalf("expected env-selected agent and explicit false pull request override, got %#v", request)
	}
}

func TestRunRejectsMutuallyExclusiveRecoveryModes(t *testing.T) {
	clearTaoEnv(t)
	app := App{Out: io.Discard, Err: io.Discard}
	for _, args := range [][]string{
		{"--continue", "--restart", "plan-a"},
		{"--repair-verification", "--reverify", "plan-a"},
	} {
		err := app.run(context.Background(), nil, args)
		if err == nil || !strings.Contains(err.Error(), "--continue, --restart, --repair-verification, and --reverify are mutually exclusive") {
			t.Fatalf("run %v error = %v, want mutually exclusive recovery flags", args, err)
		}
	}
}

func TestRunNoReviewFlagOverridesRunRequest(t *testing.T) {
	clearTaoEnv(t)
	defaults, err := cliEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	fs := App{Err: io.Discard}.flagSet("run")
	noReview := fs.Bool("no-review", false, "disable review")
	flagArgs, _ := splitSubcommandFlags([]string{"--no-review"}, nil)
	if err := fs.Parse(flagArgs); err != nil {
		t.Fatal(err)
	}
	requestOptions := runRequestOverridesFromFlags(fs, runFlagValues{NoReview: *noReview})
	request, err := defaults.newRunRequest("plan-a", requestOptions)
	if err != nil {
		t.Fatal(err)
	}
	if request.ReviewEnabled {
		t.Fatalf("expected --no-review to disable review, got %#v", request)
	}
}

func TestRunAgentFlagIsRejected(t *testing.T) {
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}

	err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--agent", "pi", "--commit-policy", "none", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -agent") {
		t.Fatalf("expected --agent rejection, got %v", err)
	}
}

func TestRunRejectsRemovedMultiPlanFlags(t *testing.T) {
	app := App{Out: io.Discard, Err: io.Discard}
	for _, flag := range []string{"--all", "--active"} {
		err := app.run(context.Background(), nil, []string{flag})
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("run %s error = %v, want unknown flag", flag, err)
		}
	}
}

func TestRunWiresStatusReporter(t *testing.T) {
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	reporter := newRecordingCLIStatusReporter()
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, StatusReporter: reporter, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		writeRunGitOutput(stdout, args)
		return nil
	}, ProcessStarter: fakeCLIProcessStarter(t, "slice done", func(string) {
		fixture.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	})}

	if err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--execution-mode", "current", "--commit-policy", "none", "--no-review", fixture.id}); err != nil {
		t.Fatal(err)
	}
	reporter.requireCall(t, "run run-plan", "idle")
}

func TestRunSignalContextCancelsActiveRun(t *testing.T) {
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	reporter := newRecordingCLIStatusReporter()
	var signalCancel context.CancelFunc
	withCLICommandSignalContext(t, func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		signalCancel = cancel
		return ctx, cancel
	})
	app := App{Out: io.Discard, Err: io.Discard, StatusReporter: reporter, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" {
			writeRunGitOutput(stdout, args)
		}
		return nil
	}, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (run.Process, error) {
		if signalCancel == nil {
			t.Fatal("signal context was not installed before process start")
		}
		signalCancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}}

	err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--execution-mode", "current", "--commit-policy", "none", "--no-review", fixture.id})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	reporter.requireCall(t, "run run-plan", "blocked")
}

func TestRunApprovalGateShowsUnblockCommands(t *testing.T) {
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	addApprovalGate(t, fixture.dir, "human approval")
	var out bytes.Buffer
	app := App{Out: &out, Err: &out, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		writeRunGitOutput(stdout, args)
		return nil
	}}

	err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--execution-mode", "current", "--commit-policy", "none", "--no-review", fixture.id})
	if err == nil {
		t.Fatal("expected approval gate error")
	}
	text := err.Error()
	for _, want := range []string{
		"slice 001-a requires approval: human approval",
		"Resolve the required action before continuing. Run:",
		"tao approve --slice 001-a " + fixture.id,
		"tao run " + fixture.id,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in error:\n%s", want, text)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("expected no stdout before run starts, got %q", out.String())
	}
}

func TestRunPrerequisiteGateRefusesBeforeWorkspaceOrAgentSideEffects(t *testing.T) {
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	repo := plan.NewFileRepository(fixture.root)
	detail, err := repo.ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.RuntimePrerequisites = []plan.RuntimePrerequisite{{PlanID: "20260430-1100-required", Reason: "required first"}}
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	commandCalls := 0
	baselineCalls := 0
	agentCalls := 0
	app := App{
		Out: io.Discard, Err: io.Discard,
		CommandRunner: func(_ context.Context, _ string, name string, args []string, stdout, _ io.Writer) error {
			if name == "git" && len(args) >= 3 {
				switch {
				case args[2] == "symbolic-ref":
					baselineCalls++
					_, _ = io.WriteString(stdout, "origin/main\n")
					return nil
				case args[2] == "branch" && args[len(args)-1] == "main":
					baselineCalls++
					_, _ = io.WriteString(stdout, "main\n")
					return nil
				case args[2] == "rev-parse" && args[3] == "main":
					baselineCalls++
					_, _ = io.WriteString(stdout, "main-sha\n")
					return nil
				}
			}
			commandCalls++
			return errors.New("workspace command must not run")
		},
		ProcessStarter: func(context.Context, string, string, []string) (run.Process, error) {
			agentCalls++
			return nil, errors.New("agent must not start")
		},
	}

	err = app.run(context.Background(), repo, []string{"--execution-mode", "isolated", "--commit-policy", "none", "--no-review", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "runtime prerequisite 20260430-1100-required is missing") || !strings.Contains(err.Error(), "next: tao show 20260430-1100-required") {
		t.Fatalf("unexpected prerequisite refusal: %v", err)
	}
	if baselineCalls != 3 || commandCalls != 0 || agentCalls != 0 {
		t.Fatalf("effects before prerequisite refusal: baseline_reads=%d workspace_commands=%d agents=%d", baselineCalls, commandCalls, agentCalls)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.CurrentSlice != nil || reloaded.Slices.Slices[0].Status != plan.StatusPending || reloaded.State.Workspace.Strategy != plan.WorkspaceStrategyCurrent {
		t.Fatalf("prerequisite refusal mutated run lifecycle: state=%+v slice=%+v", reloaded.State, reloaded.Slices.Slices[0])
	}
}

func TestRunBlockedPreflightShowsReasonWithoutStartingAgent(t *testing.T) {
	for _, tt := range []struct {
		name   string
		reason string
		want   string
	}{
		{name: "persisted reason", reason: "waiting on dependency\nwith terminal \x1b[31mtext", want: "waiting on dependency with terminal [31mtext"},
		{name: "legacy missing reason", want: "No blocker reason was recorded."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearTaoEnv(t)
			fixture := newRunPlanFixture(t, plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked)
			if tt.reason != "" {
				detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
				if err != nil {
					t.Fatal(err)
				}
				record, err := plan.NewPlanRecord("", detail)
				if err != nil {
					t.Fatal(err)
				}
				if err := record.BlockSlice("001-a", tt.reason, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			agentCalls := 0
			app := App{Out: io.Discard, Err: io.Discard, ProcessStarter: func(context.Context, string, string, []string) (run.Process, error) {
				agentCalls++
				return nil, errors.New("agent must not start")
			}}

			err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--execution-mode", "current", "--commit-policy", "none", "--no-review", fixture.id})
			if err == nil {
				t.Fatal("expected blocked preflight error")
			}
			for _, want := range []string{"Blocked slice 001-a: " + tt.want, "Resolve this blocker before continuing, then run:", "tao run --continue " + fixture.id} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("expected %q in error:\n%s", want, err)
				}
			}
			if agentCalls != 0 {
				t.Fatalf("expected no agent handoff, got %d calls", agentCalls)
			}
		})
	}
}

func TestRunApprovalGateShowsContinueWhenBlocked(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked)
	addApprovalGate(t, fixture.dir, "human approval")
	detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	commands := runUnblockCommands(detail, fixture.id)
	want := []string{
		"tao approve --slice 001-a " + fixture.id,
		"tao run --continue " + fixture.id,
	}
	if strings.Join(commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestRunAgentPiEnvRoutesSliceRunToPi(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_AGENT", "pi")
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	var out bytes.Buffer
	piCalls := 0
	app := App{Out: &out, Err: &out, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
			return nil
		}
		writeRunGitOutput(stdout, args)
		return nil
	}, ProcessStarter: fakeCLIProcessStarter(t, `slice done`, func(prompt string) {
		piCalls++
		fixture.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	})}

	if err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--commit-policy", "none", "--no-review", fixture.id}); err != nil {
		t.Fatal(err)
	}
	if piCalls != 1 {
		t.Fatalf("expected one pi call, got %d", piCalls)
	}
}

func TestRunAgentClaudeEnvRoutesSliceRunToClaude(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_AGENT", "claude")
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	var out bytes.Buffer
	claudeCalls := 0
	var gotArgs []string
	app := App{Out: &out, Err: &out, CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
			return nil
		}
		writeRunGitOutput(stdout, args)
		return nil
	}, ProcessStarter: func(ctx context.Context, cwd string, name string, args []string) (run.Process, error) {
		claudeCalls++
		if name != "claude" {
			t.Fatalf("unexpected claude command %s", name)
		}
		gotArgs = append([]string{}, args...)
		proc := newFakeCLIClaudeProcess(t)
		go func() {
			defer proc.finish()
			_, _ = io.ReadAll(proc.stdinReader)
			fixture.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
			proc.writeEvent(`{"type":"result","result":"slice done"}`)
		}()
		return proc, nil
	}}

	if err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--commit-policy", "none", "--no-review", fixture.id}); err != nil {
		t.Fatal(err)
	}
	if claudeCalls != 1 {
		t.Fatalf("expected one claude call, got %d", claudeCalls)
	}
	if strings.Join(gotArgs, " ") != "--print --output-format stream-json --verbose --no-session-persistence --permission-mode auto" {
		t.Fatalf("unexpected claude args: %#v", gotArgs)
	}
}

func TestRunPullRequestRejectsInvalidCombinations(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "commit policy none", args: []string{"--pull-request", "--commit-policy", "none"}, want: "--pull-request requires commit policy slice"},
		{name: "current execution mode", args: []string{"--pull-request", "--execution-mode", "current"}, want: "--pull-request requires --execution-mode isolated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			app := App{CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
				if name != "git" {
					t.Fatalf("unexpected command %s %v", name, args)
					return nil
				}
				switch cleanupCommandKey(args) {
				case "branch --show-current":
					_, _ = io.WriteString(stdout, "main\n")
				case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
					_, _ = io.WriteString(stdout, "origin/main\n")
				}
				return nil
			}}

			args := append(append([]string{}, test.args...), fixture.id)
			err := app.run(context.Background(), plan.NewFileRepository(fixture.root), args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestRunRejectsInvalidExecutionMode(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	app := App{CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("unexpected command %s %v", name, args)
		return nil
	}}

	err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--execution-mode", "shared", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("expected unsupported execution mode error, got %v", err)
	}
}

func TestRunRejectsInvalidCommitPolicy(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	app := App{CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("unexpected command %s %v", name, args)
		return nil
	}}

	err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--commit-policy", "always", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "unsupported commit policy") {
		t.Fatalf("expected unsupported commit policy error, got %v", err)
	}
	err = app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--commit-policy", "plan", fixture.id})
	if err == nil || !strings.Contains(err.Error(), "plan was removed; use slice or none") {
		t.Fatalf("expected removed plan policy migration error, got %v", err)
	}
}

func TestRunAutoReworkPolicyResolution(t *testing.T) {
	tests := []struct {
		name          string
		envEnabled    string
		envAttempts   string
		envReview     string
		args          []string
		reviewEnabled bool
		want          runtimeconfig.AutoReworkPolicy
		wantError     string
	}{
		{name: "default on", reviewEnabled: true, want: runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}},
		{name: "environment disables", envEnabled: "false", reviewEnabled: true, want: runtimeconfig.AutoReworkPolicy{MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}},
		{name: "explicit flag beats environment", envEnabled: "false", envAttempts: "3", args: []string{"--auto-rework", "--max-rework-attempts=2"}, reviewEnabled: true, want: runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 2}},
		{name: "review disabled silently disables default", reviewEnabled: false, want: runtimeconfig.AutoReworkPolicy{MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}},
		{name: "review environment disabled silently overrides explicit auto rework", envReview: "false", args: []string{"--auto-rework"}, reviewEnabled: false, want: runtimeconfig.AutoReworkPolicy{MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}},
		{name: "explicit conflict", args: []string{"--auto-rework", "--no-review"}, reviewEnabled: false, wantError: "--auto-rework requires automatic review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearTaoEnv(t)
			if tt.envEnabled != "" {
				t.Setenv(envAutoRework, tt.envEnabled)
			}
			if tt.envAttempts != "" {
				t.Setenv(envMaxReworkAttempts, tt.envAttempts)
			}
			if tt.envReview != "" {
				t.Setenv(runtimeconfig.EnvReview, tt.envReview)
			}
			fs, _, err := (App{Err: io.Discard}).parseArgsFor(&runCommand, tt.args)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := resolveRunAutoReworkPolicy(fs, tt.reviewEnabled)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if policy != tt.want {
				t.Fatalf("policy = %+v, want %+v", policy, tt.want)
			}
		})
	}
}

func TestRunReverifyBypassesAutomaticReworkForChangesRequestedPlan(t *testing.T) {
	clearTaoEnv(t)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	planID := "20260901-1600-reverify"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/run.go", Line: 190, Message: "fix the run loop"}
	detail := singleRunReworkDetail(planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}), now)
	detail.Dir = t.TempDir()
	detail.State.Plan.FinalVerification = &plan.FinalVerification{HeadSHA: "failed-head", Result: "failed", VerifiedAt: now.Add(-time.Minute)}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}
	initialSliceCount := len(detail.Slices.Slices)

	calls := 0
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(_ run.Service, _ context.Context, request run.Request) error {
		calls++
		if !request.Reverify {
			t.Fatal("run request did not retain reverify mode")
		}
		detail.State.Plan.FinalVerification = &plan.FinalVerification{HeadSHA: "failed-head", Result: "passed", VerifiedAt: now}
		return nil
	}
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	if err := (App{Out: io.Discard, Now: func() time.Time { return now }}).run(context.Background(), repo, []string{"--reverify", planID}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("execute calls = %d, want one reverification without automatic rework", calls)
	}
	if detail.State.Status != plan.StatusChangesRequested {
		t.Fatalf("plan status = %q, want unchanged changes_requested", detail.State.Status)
	}
	if len(detail.Slices.Slices) != initialSliceCount || len(detail.State.Plan.PendingSlices) != 0 {
		t.Fatalf("reverification appended slices: slices=%d pending=%v", len(detail.Slices.Slices), detail.State.Plan.PendingSlices)
	}
	for _, event := range detail.Events {
		if event.Type == plan.EventTypePlanReopened || event.Type == plan.EventTypeReworkRound {
			t.Fatalf("reverification appended automatic-rework event: %+v", event)
		}
	}
}

func TestRunSinglePlanAutoReworkLoop(t *testing.T) {
	now := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/run.go", Line: 150, Message: "fix the run loop", Suggestion: "rerun after reopening"}

	tests := []struct {
		name           string
		args           []string
		initialStatus  string
		initialReview  *plan.PlanReview
		initialEvents  []plan.Event
		afterRework    func(*plan.PlanDetail)
		completeOnCall int
		wantCalls      int
		wantError      string
		wantStopOutput []string
		doNotWant      []string
		wantProgress   bool
	}{
		{
			name:          "approved on first review",
			initialStatus: plan.StatusReviewed,
			initialReview: reworkReview(plan.ReviewVerdictApprove, nil),
			wantCalls:     1,
		},
		{
			name:          "changes requested reopens and reruns",
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			afterRework: func(detail *plan.PlanDetail) {
				detail.State.Status = plan.StatusReviewed
				detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
			},
			wantCalls:    2,
			wantProgress: true,
		},
		{
			name:          "cap exhaustion",
			args:          []string{"--max-rework-attempts=1"},
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			afterRework: func(detail *plan.PlanDetail) {
				detail.State.Status = plan.StatusChangesRequested
				detail.State.Plan.Review = reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding})
			},
			wantCalls:      2,
			wantError:      "automatic rework cap exhausted after 1 cycles",
			wantStopOutput: []string{"Automatic rework stopped: attempt cap reached", "Read the review"},
			doNotWant:      []string{"!!!!!!!!!!!!!!!!", "GOING IN CIRCLES", finding.Message},
			wantProgress:   true,
		},
		{
			name:          "equivalent findings stall",
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			afterRework: func(detail *plan.PlanDetail) {
				detail.State.Status = plan.StatusChangesRequested
				detail.State.Plan.Review = reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding})
			},
			wantCalls:      2,
			wantError:      "AUTOMATIC REWORK STOPPED: THE LOOP IS GOING IN CIRCLES",
			wantStopOutput: []string{"!!!!!!!!!!!!!!!!", "Read the review", "internal/cli/run.go:150", finding.Message, finding.Suggestion, "before re-running"},
			wantProgress:   true,
		},
		{
			name:          "stalled restart is guarded",
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			initialEvents: []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: "automatic rework stalled on equivalent consecutive findings"}},
			wantCalls:     0,
			wantError:     "AUTOMATIC REWORK STOPPED: THE LOOP IS GOING IN CIRCLES",
			wantStopOutput: []string{
				"!!!!!!!!!!!!!!!!", "internal/cli/run.go:150", finding.Message, finding.Suggestion,
				"A new automatic-rework budget was not started", "--rework-restart",
			},
		},
		{
			name:          "recurring files restart is guarded",
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			initialEvents: []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: "automatic rework stalled on files recurring across three consecutive reviews: [\"internal/cli/run.go\"]"}},
			wantCalls:     0,
			wantError:     "AUTOMATIC REWORK STOPPED: THE SAME FILES KEEP RECURRING",
			wantStopOutput: []string{
				"- internal/cli/run.go", "internal/cli/run.go:150", finding.Message, finding.Suggestion,
				"A new automatic-rework budget was not started", "--rework-restart",
			},
		},
		{
			name:          "restart flag grants a new budget",
			args:          []string{"--rework-restart"},
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			initialEvents: []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: "automatic rework stalled on equivalent consecutive findings"}},
			afterRework: func(detail *plan.PlanDetail) {
				detail.State.Status = plan.StatusReviewed
				detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
			},
			completeOnCall: 1,
			wantCalls:      1,
			wantProgress:   true,
		},
		{
			name:          "cap exhaustion restart is guarded",
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			initialEvents: []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: "automatic rework cap exhausted after 5 cycles"}},
			wantCalls:     0,
			wantError:     "Automatic rework stopped: attempt cap reached",
			wantStopOutput: []string{
				"automatic rework cap exhausted after 5 cycles", "A new automatic-rework budget was not started", "--rework-restart",
			},
			doNotWant: []string{"!!!!!!!!!!!!!!!!", "GOING IN CIRCLES", finding.Message},
		},
		{
			name:          "explicitly disabled",
			args:          []string{"--auto-rework=false"},
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			initialEvents: []plan.Event{{Type: plan.EventTypeReworkStopped, Reason: "automatic rework stalled on equivalent consecutive findings"}},
			wantCalls:     1,
		},
		{
			name:          "review disabled",
			args:          []string{"--no-review"},
			initialStatus: plan.StatusChangesRequested,
			initialReview: reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			wantCalls:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearTaoEnv(t)
			dataHome := t.TempDir()
			t.Setenv("TAO_DATA_HOME", dataHome)
			planID := "20260713-2000-single-run"
			detail := singleRunReworkDetail(planID, tt.initialStatus, tt.initialReview, now)
			detail.Dir = t.TempDir()
			detail.Events = append([]plan.Event(nil), tt.initialEvents...)
			repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}
			var out bytes.Buffer
			calls := 0
			oldExecutor := executeSinglePlan
			executeSinglePlan = func(run.Service, context.Context, run.Request) error {
				calls++
				completeOnCall := tt.completeOnCall
				if completeOnCall == 0 {
					completeOnCall = 2
				}
				if calls == completeOnCall && tt.afterRework != nil {
					tt.afterRework(detail)
				}
				return nil
			}
			defer func() { executeSinglePlan = oldExecutor }()

			args := append(append([]string{}, tt.args...), planID)
			err := (App{Out: &out, Now: func() time.Time { return now }}).run(context.Background(), repo, args)
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
			if err != nil {
				for _, want := range tt.wantStopOutput {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("stop output %q does not contain %q", err, want)
					}
				}
				for _, unwanted := range tt.doNotWant {
					if strings.Contains(err.Error(), unwanted) {
						t.Errorf("stop output %q unexpectedly contains %q", err, unwanted)
					}
				}
			}
			if calls != tt.wantCalls {
				t.Fatalf("execute calls = %d, want %d", calls, tt.wantCalls)
			}
			if got := strings.Contains(out.String(), "Plan reopened for rework round 1"); got != tt.wantProgress {
				t.Fatalf("progress output present = %t, want %t; output=%q", got, tt.wantProgress, out.String())
			}
			if tt.wantError != "" && detail.State.Status != plan.StatusChangesRequested {
				t.Fatalf("stopped plan status = %q, want changes_requested", detail.State.Status)
			}
			assertNoSingleRunQueueFiles(t, dataHome)
		})
	}
}

func TestRunAutoReworkUsesOneStatusInvocationAndPublishesPhase(t *testing.T) {
	clearTaoEnv(t)
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	planID := "20260729-0600-status-rework"
	finding := plan.ReviewFinding{File: "internal/cli/run.go", Message: "fix it"}
	detail := singleRunReworkDetail(planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}), now)
	detail.Dir = t.TempDir()
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}
	reporter := &phaseRecordingCLIStatusReporter{}

	calls := 0
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(run.Service, context.Context, run.Request) error {
		calls++
		if calls == 2 {
			detail.State.Status = plan.StatusReviewed
			detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
		}
		return nil
	}
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	app := App{Out: io.Discard, StatusReporter: reporter, Now: func() time.Time { return now }}
	if err := app.run(context.Background(), repo, []string{planID}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || reporter.invocations != 1 {
		t.Fatalf("execute calls=%d status invocations=%d, want 2 and 1", calls, reporter.invocations)
	}
	if !slices.Contains(reporter.phases, run.PhaseAutomaticRework) {
		t.Fatalf("status phases = %q, want automatic rework", reporter.phases)
	}
}

func TestRunReworkRestartStartsFreshRecurringFileWindowBeforeRealServiceExecution(t *testing.T) {
	clearTaoEnv(t)
	now := time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)
	planID := "20260718-1500-real-restart"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/run.go", Line: 173, Message: "reopen before execution"}
	detail := singleRunReworkDetail(planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}), now)
	detail.Dir = t.TempDir()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Repo = plan.Repo{Name: "tao", Root: root, Branch: "feature"}
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	historicalRounds := []plan.Slice{
		{ID: "r101-internal-cli-run-go", Status: plan.StatusCompleted, ExpectedFiles: []string{"internal/cli/run.go"}},
		{ID: "r201-internal-cli-run-go", Status: plan.StatusCompleted, ExpectedFiles: []string{"internal/cli/run.go"}},
	}
	detail.Slices.Slices = append(detail.Slices.Slices, historicalRounds...)
	for _, historical := range historicalRounds {
		detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, historical.ID)
	}
	detail.Events = []plan.Event{{
		Type: plan.EventTypeReworkStopped, Timestamp: now.Add(-time.Minute), PlanID: planID,
		Reason: "automatic rework stalled on files recurring across three consecutive reviews: [\"internal/cli/run.go\"]",
	}}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail, detail.Dir: detail}}

	workStarts := 0
	var out bytes.Buffer
	app := App{
		Out: &out,
		Now: func() time.Time { return now },
		CommandRunner: func(_ context.Context, _ string, name string, args []string, stdout io.Writer, _ io.Writer) error {
			if name == "git" {
				writeRunGitOutput(stdout, args)
			}
			return nil
		},
		ProcessStarter: fakeCLIProcessStarter(t, "slice complete", func(prompt string) {
			if !strings.Contains(prompt, "You are in WORK mode.") {
				return
			}
			workStarts++
			if len(detail.Slices.Slices) != 4 || !strings.HasPrefix(detail.Slices.Slices[3].ID, "r3") {
				t.Errorf("real service started without a fresh-window rework slice: slices=%v", detail.Slices.Slices)
				return
			}
			completedID := detail.Slices.Slices[3].ID
			for i := range detail.Slices.Slices {
				if detail.Slices.Slices[i].ID == completedID {
					detail.Slices.Slices[i].Status = plan.StatusCompleted
				}
			}
			detail.State.Status = plan.StatusReviewed
			detail.State.Plan.CurrentSlice = nil
			detail.State.Plan.PendingSlices = nil
			detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, completedID)
			detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
		}),
	}

	err = app.run(context.Background(), repo, []string{"--commit-policy", "none", "--execution-mode", "current", "--max-rework-attempts", "1", "--rework-restart", planID})
	if err != nil {
		t.Fatalf("run with acknowledged restart failed: %v", err)
	}
	if workStarts != 1 || !strings.Contains(out.String(), "Running slice r3") {
		t.Fatalf("real service did not execute exactly one fresh-window slice: starts=%d output=%q", workStarts, out.String())
	}
	if reworkpkg.RoundCount(detail) != 3 {
		t.Fatalf("rework rounds = %d, want historical rounds plus round 3", reworkpkg.RoundCount(detail))
	}
	for _, historical := range historicalRounds {
		if !slices.ContainsFunc(detail.Slices.Slices, func(slice plan.Slice) bool { return slice.ID == historical.ID }) {
			t.Errorf("restart cleared historical slice %s: %+v", historical.ID, detail.Slices.Slices)
		}
	}
}

func TestRunAutoReworkRestartGuardLeavesPendingPlanUnchanged(t *testing.T) {
	clearTaoEnv(t)
	now := time.Date(2026, 7, 13, 20, 15, 0, 0, time.UTC)
	planID := "20260713-2015-pending"
	detail := singleRunReworkDetail(planID, plan.StatusInProgress, nil, now)
	detail.Dir = t.TempDir()
	detail.State.Plan.CompletedSlices = nil
	detail.State.Plan.PendingSlices = []string{"001-work"}
	detail.State.Plan.CurrentSlice = new("001-work")
	detail.Slices.Slices[0].Status = plan.StatusPending
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}

	calls := 0
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(run.Service, context.Context, run.Request) error {
		calls++
		return nil
	}
	defer func() { executeSinglePlan = oldExecutor }()

	if err := (App{Out: io.Discard, Now: func() time.Time { return now }}).run(context.Background(), repo, []string{planID}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("execute calls = %d, want 1", calls)
	}
	if detail.State.Status != plan.StatusInProgress || detail.Slices.Slices[0].Status != plan.StatusPending {
		t.Fatalf("pending plan changed: status=%q slice=%q", detail.State.Status, detail.Slices.Slices[0].Status)
	}
}

func TestRunSinglePlanHoldsLockAcrossReopenBoundary(t *testing.T) {
	clearTaoEnv(t)
	now := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	planID := "20260713-2000-lock-rework"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/run.go", Line: 183, Message: "hold the plan lock", Suggestion: "keep ownership across reopen"}
	detail := singleRunReworkDetail(planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}), now)
	detail.Dir = t.TempDir()
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}

	ctx := context.Background()
	oldExecutor := executeSinglePlan
	calls := 0
	competitorRan := false
	executeSinglePlan = func(_ run.Service, _ context.Context, request run.Request) error {
		calls++
		if calls == 1 {
			competitor := run.NewService(repo, io.Discard, run.Options{})
			result := make(chan error, 1)
			go func() {
				result <- competitor.WithPlanRunLock(ctx, request, func(context.Context) error {
					competitorRan = true
					return nil
				})
			}()
			select {
			case err := <-result:
				if !errors.Is(err, run.ErrCannotStart) {
					t.Fatalf("competing lock error = %v, want ErrCannotStart", err)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for competing plan lock")
			}
		} else {
			detail.State.Status = plan.StatusReviewed
			detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
		}
		return nil
	}
	defer func() { executeSinglePlan = oldExecutor }()

	if err := (App{Out: io.Discard, Now: func() time.Time { return now }}).run(ctx, repo, []string{planID}); err != nil {
		t.Fatal(err)
	}
	if competitorRan {
		t.Fatal("competing operation ran between initial execution and rework")
	}
	if calls != 2 {
		t.Fatalf("execute calls = %d, want 2", calls)
	}
}

func singleRunReworkDetail(planID, status string, review *plan.PlanReview, now time.Time) *plan.PlanDetail {
	completed := now.Add(-time.Minute)
	return &plan.PlanDetail{
		State: plan.State{
			Status:    status,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
			Plan: plan.PlanState{
				ID:              planID,
				CompletedSlices: []string{"001-work"},
				Timing:          plan.PlanTiming{StartedAt: &completed, CompletedAt: &completed, LastActivityAt: &completed},
				Review:          review,
			},
		},
		Slices: plan.SlicesFile{PlanID: planID, Slices: []plan.Slice{{
			ID:            "001-work",
			Title:         "Original work",
			Status:        plan.StatusCompleted,
			Goal:          "Complete the original work",
			ExpectedFiles: []string{"internal/cli/run.go"},
			Verification:  plan.Verification{Commands: []string{"go test ./internal/cli"}},
		}}},
	}
}

func assertNoSingleRunQueueFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Name() == "queue.json" || info.Name() == "queue.jsonl" {
			t.Errorf("single-plan run wrote durable queue file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func addApprovalGate(t *testing.T, planDir string, reason string) {
	t.Helper()
	slicesPath := filepath.Join(planDir, "slices.json")
	slices := readText(t, slicesPath)
	updated := strings.Replace(slices, `"verification":{"commands":["go test ./internal/cli"],"manual_checks":[]}`, `"verification":{"commands":["go test ./internal/cli"],"manual_checks":[]},"approval":{"required":true,"reason":"`+reason+`","approved":false}`, 1)
	if updated == slices {
		t.Fatal("test fixture verification block not found")
	}
	if err := os.WriteFile(slicesPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRunGitOutput(stdout io.Writer, args []string) {
	key := cleanupCommandKey(args)
	switch key {
	case "branch --show-current":
		_, _ = io.WriteString(stdout, "feature\n")
	case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
		_, _ = io.WriteString(stdout, "origin/main\n")
	}
}

func fakeCLIProcessStarter(t *testing.T, finalText string, onPrompt func(string)) run.ProcessStarter {
	t.Helper()
	return func(ctx context.Context, cwd string, name string, args []string) (run.Process, error) {
		if name != "pi" || strings.Join(args, " ") != "--mode rpc --no-session" {
			t.Fatalf("unexpected pi process start: %s %#v", name, args)
		}
		proc := newFakeCLIPiProcess(t)
		go func() {
			defer proc.finish()
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"id":"tao-readiness-state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"test-provider","id":"test-model"}}}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"id":"tao-readiness-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"test-provider","id":"test-model"}]}}`)
			cmd, err := proc.readCommand()
			if err != nil {
				return
			}
			if cmd["type"] == "prompt" && onPrompt != nil {
				onPrompt(stringValue(cmd["message"]))
			}
			proc.writeEvent(`{"id":"tao-prompt","type":"response","command":"prompt","success":true}`)
			proc.writeEvent(`{"type":"message","role":"assistant","text":` + strconv.Quote(finalText) + `}`)
			proc.writeEvent(`{"type":"agent_end","session_id":"session-1"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"type":"state","session_id":"session-1"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"type":"session_stats","session_id":"session-1"}`)
		}()
		return proc, nil
	}
}

type fakeCLIClaudeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	done         chan struct{}
	once         sync.Once
}

func newFakeCLIClaudeProcess(t *testing.T) *fakeCLIClaudeProcess {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	return &fakeCLIClaudeProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
}

func (p *fakeCLIClaudeProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *fakeCLIClaudeProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *fakeCLIClaudeProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (p *fakeCLIClaudeProcess) Wait() error {
	<-p.done
	return nil
}
func (p *fakeCLIClaudeProcess) Kill() error { return nil }

func (p *fakeCLIClaudeProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stdinReader.Close()
		close(p.done)
	})
}

func (p *fakeCLIClaudeProcess) writeEvent(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

type fakeCLIPiProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdinDecoder *json.Decoder
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	done         chan struct{}
	once         sync.Once
}

func newFakeCLIPiProcess(t *testing.T) *fakeCLIPiProcess {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	return &fakeCLIPiProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdinDecoder: json.NewDecoder(stdinReader), stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
}

func (p *fakeCLIPiProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *fakeCLIPiProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *fakeCLIPiProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (p *fakeCLIPiProcess) Wait() error {
	<-p.done
	return nil
}
func (p *fakeCLIPiProcess) Kill() error { return nil }

func (p *fakeCLIPiProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stdinReader.Close()
		close(p.done)
	})
}

func (p *fakeCLIPiProcess) readCommand() (map[string]any, error) {
	var cmd map[string]any
	if err := p.stdinDecoder.Decode(&cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (p *fakeCLIPiProcess) writeEvent(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

type phaseRecordingCLIStatusReporter struct {
	invocations int
	phases      []runstatus.Phase
}

func (r *phaseRecordingCLIStatusReporter) Track(_ string, fn func() error) error {
	return fn()
}

func (r *phaseRecordingCLIStatusReporter) TrackInvocation(_ string, _ run.StatusInvocation, fn func(run.PhaseReporter) error) error {
	r.invocations++
	r.ReportPhase(run.PhaseWaitingForOwnership, nil)
	return fn(r)
}

func (r *phaseRecordingCLIStatusReporter) ReportPhase(phase runstatus.Phase, _ *runstatus.SliceDetail) {
	r.phases = append(r.phases, phase)
}

type cliStatusCall struct {
	status     string
	settlement string
}

type recordingCLIStatusReporter struct {
	mu    sync.Mutex
	calls []cliStatusCall
}

func newRecordingCLIStatusReporter() *recordingCLIStatusReporter {
	return &recordingCLIStatusReporter{}
}

func (r *recordingCLIStatusReporter) Track(status string, fn func() error) (err error) {
	call := cliStatusCall{status: status}
	defer func() {
		if recovered := recover(); recovered != nil {
			call.settlement = "blocked"
			r.record(call)
			panic(recovered)
		}
		if err != nil {
			call.settlement = "blocked"
		} else {
			call.settlement = "idle"
		}
		r.record(call)
	}()
	return fn()
}

func (r *recordingCLIStatusReporter) record(call cliStatusCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recordingCLIStatusReporter) requireCall(t *testing.T, wantStatus string, wantSettlement string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) != 1 {
		t.Fatalf("status reporter calls = %+v, want one", r.calls)
	}
	if got := r.calls[0]; got.status != wantStatus || got.settlement != wantSettlement {
		t.Fatalf("status reporter call = %+v, want status %q settlement %q", got, wantStatus, wantSettlement)
	}
}

func withCLICommandSignalContext(t *testing.T, fn commandSignalContextFunc) {
	t.Helper()
	old := newCommandSignalContext
	newCommandSignalContext = fn
	t.Cleanup(func() { newCommandSignalContext = old })
}

func TestRunApprovalSliceUsesTypedCapabilityField(t *testing.T) {
	// Ensure runApprovalSlice reads ApprovalSliceID from RunCapabilities, not prose.
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	addApprovalGate(t, fixture.dir, "security sign-off")

	detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	derived := plan.Derive(detail, time.Time{})

	if !derived.Capabilities.NeedsApproval {
		t.Fatalf("expected NeedsApproval=true for approval-gated slice, got %+v", derived.Capabilities)
	}
	if derived.Capabilities.ApprovalSliceID != "001-a" {
		t.Fatalf("expected ApprovalSliceID=001-a, got %+v", derived.Capabilities)
	}
	slice := runApprovalSlice(detail, derived)
	if slice == nil {
		t.Fatal("expected runApprovalSlice to return the gated slice")
	}
	if slice.ID != "001-a" {
		t.Fatalf("expected approval slice ID=001-a, got %q", slice.ID)
	}
}

func TestRunApprovalSliceReturnsNilWhenNoApprovalSliceID(t *testing.T) {
	// When NeedsApproval is false (ApprovalSliceID empty), runApprovalSlice returns nil.
	clearTaoEnv(t)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	derived := plan.Derive(detail, time.Time{})
	if derived.Capabilities.NeedsApproval {
		t.Fatalf("expected no approval gate for runnable plan, got %+v", derived.Capabilities)
	}
	if slice := runApprovalSlice(detail, derived); slice != nil {
		t.Fatalf("expected nil from runApprovalSlice with no approval gate, got %+v", slice)
	}
}

func TestRunUsesInjectedRepositoryFactory(t *testing.T) {
	var out bytes.Buffer
	var gotDir string
	app := App{Out: &out, Err: &out, Repository: func(plansDir string) Repository {
		gotDir = plansDir
		return fakeRepository{summaries: []plan.PlanSummary{{ID: "20260427-1810-example", Title: "Example", Status: plan.StatusPlanned}}}
	}}

	if err := app.Run(context.Background(), []string{"--plans-dir", "/tmp/custom-plans", "list"}); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/tmp/custom-plans" || !strings.Contains(out.String(), "example") {
		t.Fatalf("expected injected repository to render list, dir=%q output=%q", gotDir, out.String())
	}
}
