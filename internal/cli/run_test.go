package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runqueue"
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
	for _, want := range []string{"Running slice 001-a", "running 001-a", "Slice completed: 001-a", "Plan slices complete: " + fixture.id} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in tao output:\n%s", want, text)
		}
	}
	logText := readText(t, plan.LogPath(fixture.dir))
	for _, want := range []string{"running 001-a"} {
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
		"To unblock this plan, run:",
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

func TestRunReworkRestartReopensBeforeRealServiceExecution(t *testing.T) {
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
	detail.Events = []plan.Event{{Type: plan.EventTypeReworkStopped, Timestamp: now.Add(-time.Minute), PlanID: planID, Reason: "automatic rework stalled on equivalent consecutive findings"}}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail, detail.Dir: detail}}

	starts := 0
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
		ProcessStarter: fakeCLIProcessStarter(t, "slice complete", func(string) {
			starts++
			if starts > 1 {
				return
			}
			if len(detail.Slices.Slices) != 2 || !strings.HasPrefix(detail.Slices.Slices[1].ID, "r1") {
				t.Errorf("real service started without a reopened slice: slices=%v", detail.Slices.Slices)
				return
			}
			completedID := detail.Slices.Slices[1].ID
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

	err = app.run(context.Background(), repo, []string{"--commit-policy", "none", "--execution-mode", "current", "--rework-restart", planID})
	if err != nil {
		t.Fatalf("run with acknowledged restart failed: %v", err)
	}
	if starts == 0 || !strings.Contains(out.String(), "Running slice r1") {
		t.Fatalf("real service did not execute the reopened slice: starts=%d output=%q", starts, out.String())
	}
	if reworkpkg.RoundCount(detail) != 1 {
		t.Fatalf("rework rounds = %d, want 1", reworkpkg.RoundCount(detail))
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

func TestDirectAndQueueAutoReworkStopEquivalently(t *testing.T) {
	now := time.Date(2026, 7, 13, 20, 30, 0, 0, time.UTC)
	finding := func(line int, message, suggestion string) plan.ReviewFinding {
		return plan.ReviewFinding{Severity: "major", File: "store/file.go", Line: line, Message: message, Suggestion: suggestion}
	}
	warpSequence := []plan.ReviewFinding{
		finding(41, "Warp drops the recovered record", "retain the record after recovery"),
		finding(74, "Warp leaves the write transaction open", "close the transaction after writing"),
		finding(103, "Warp truncates the replacement before sync", "sync before replacing the file"),
		finding(128, "Warp accepts a stale checksum", "compare the checksum before loading"),
	}
	normalizedRepeat := warpSequence[2]
	normalizedRepeat.Severity = " MAJOR "
	normalizedRepeat.File = "./store/./file.go"
	normalizedRepeat.Message = "  WARP truncates the replacement\n before SYNC "
	normalizedRepeat.Suggestion = "SYNC before   replacing the FILE"

	tests := []struct {
		name        string
		maxAttempts int
		findings    []plan.ReviewFinding
		wantReason  string
		wantRounds  int
		wantLoud    bool
	}{
		{
			name:        "Warp-shaped distinct same-file findings reach cap",
			maxAttempts: 3,
			findings:    warpSequence,
			wantReason:  "automatic rework cap exhausted after 3 cycles",
			wantRounds:  3,
		},
		{
			name:        "normalized identical finding stalls",
			maxAttempts: 5,
			findings:    append(slices.Clone(warpSequence[:3]), normalizedRepeat),
			wantReason:  "automatic rework stalled on equivalent consecutive findings",
			wantRounds:  3,
			wantLoud:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			planID := "20260713-2030-equivalence"
			runSequence := func(detail *plan.PlanDetail) func(run.Service, context.Context, run.Request) error {
				calls := 0
				return func(run.Service, context.Context, run.Request) error {
					index := min(calls, len(tt.findings)-1)
					calls++
					detail.State.Status = plan.StatusChangesRequested
					detail.State.Plan.Review = reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{tt.findings[index]})
					return nil
				}
			}
			findEvent := func(events []plan.Event, eventType string) plan.Event {
				for _, event := range slices.Backward(events) {
					if event.Type == eventType {
						return event
					}
				}
				t.Fatalf("events %+v do not contain %q", events, eventType)
				return plan.Event{}
			}

			directDetail := singleRunReworkDetail(planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{tt.findings[0]}), now)
			directDetail.Dir = t.TempDir()
			directRepo := &recordingAutoReworkRepository{queueRepository: fakeRepository{details: map[string]*plan.PlanDetail{planID: directDetail}}}
			oldExecutor := executeSinglePlan
			executeSinglePlan = runSequence(directDetail)
			directErr := (App{Out: io.Discard, Now: func() time.Time { return now }}).run(context.Background(), directRepo, []string{"--max-rework-attempts=" + strconv.Itoa(tt.maxAttempts), planID})
			executeSinglePlan = oldExecutor
			if directErr == nil {
				t.Fatal("direct run unexpectedly succeeded")
			}

			queueDetail := singleRunReworkDetail(planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{tt.findings[0]}), now)
			queueDetail.Dir = t.TempDir()
			queueRepo := &recordingAutoReworkRepository{queueRepository: fakeRepository{details: map[string]*plan.PlanDetail{planID: queueDetail}}}
			queueExecute := runSequence(queueDetail)
			manager := runqueue.New(context.Background(), func(ctx context.Context, request run.Request) error {
				return queueExecute(run.Service{}, ctx, request)
			}, nil)
			if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: tt.maxAttempts}); err != nil {
				t.Fatal(err)
			}
			manager.SetAutoReworker(planAutoReworker(queueRepo, func() time.Time { return now }))
			request := run.Request{Input: planID, ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: run.CommitPolicySlice, ExecutionMode: run.ExecutionModeIsolated, Agent: run.AgentPi, ReviewEnabled: true}}
			if _, err := manager.Enqueue(request); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for manager.Queue().Entries[0].Status != runqueue.QueueStatusFailed && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			entry := manager.Queue().Entries[0]

			if directErr.Error() != entry.Error || !strings.Contains(directErr.Error(), tt.wantReason) {
				t.Fatalf("stop outputs = (direct %q, queue %q), want matching output containing %q", directErr, entry.Error, tt.wantReason)
			}
			wantFinding := tt.findings[len(tt.findings)-1].Message
			if tt.wantLoud {
				if !strings.Contains(entry.Error, "THE LOOP IS GOING IN CIRCLES") || !strings.Contains(entry.Error, strings.TrimSpace(wantFinding)) {
					t.Fatalf("stall output = %q, want loud output containing %q", entry.Error, wantFinding)
				}
			} else if strings.Contains(entry.Error, "GOING IN CIRCLES") || strings.Contains(entry.Error, "!!!!!!!!!!!!!!!!") {
				t.Fatalf("cap output is unexpectedly alarmed: %q", entry.Error)
			}
			directRounds, queueRounds := len(directDetail.State.Plan.PendingSlices), entry.ReworkAttempts
			if directRounds != tt.wantRounds || queueRounds != tt.wantRounds {
				t.Fatalf("final rounds = (direct %d, queue %d), want both %d", directRounds, queueRounds, tt.wantRounds)
			}
			if directDetail.State.Status != plan.StatusChangesRequested || queueDetail.State.Status != plan.StatusChangesRequested {
				t.Fatalf("final statuses = (direct %q, queue %q), want changes_requested", directDetail.State.Status, queueDetail.State.Status)
			}
			if !reflect.DeepEqual(directDetail.State.Plan.Review.Findings, []plan.ReviewFinding{tt.findings[len(tt.findings)-1]}) ||
				!reflect.DeepEqual(queueDetail.State.Plan.Review.Findings, directDetail.State.Plan.Review.Findings) {
				t.Fatalf("latest findings differ: direct=%+v queue=%+v", directDetail.State.Plan.Review.Findings, queueDetail.State.Plan.Review.Findings)
			}

			directStop := findEvent(directRepo.events, plan.EventTypeReworkStopped)
			queueStop := findEvent(queueRepo.events, plan.EventTypeReworkStopped)
			wantStopFingerprint := reworkpkg.ReworkFindingsFingerprint([]plan.ReviewFinding{tt.findings[len(tt.findings)-1]})
			if directStop.Reason != tt.wantReason || directStop.Attempts != tt.wantRounds || directStop.Fingerprint != wantStopFingerprint {
				t.Fatalf("direct stop event = %+v", directStop)
			}
			if queueStop.Reason != directStop.Reason || queueStop.Attempts != directStop.Attempts || queueStop.Fingerprint != directStop.Fingerprint {
				t.Fatalf("queue stop event = %+v, want parity with %+v", queueStop, directStop)
			}
			lastRound := findEvent(directRepo.events, plan.EventTypeReworkRound)
			wantDurableFingerprint := reworkpkg.ReworkFindingsFingerprint([]plan.ReviewFinding{tt.findings[tt.wantRounds-1]})
			if lastRound.Attempts != tt.wantRounds || lastRound.Fingerprint != wantDurableFingerprint || entry.PreviousFindingFingerprint != wantDurableFingerprint {
				t.Fatalf("durable progress = (event %+v, queue %q), want attempt %d fingerprint %q", lastRound, entry.PreviousFindingFingerprint, tt.wantRounds, wantDurableFingerprint)
			}
		})
	}
}

func TestDirectAndQueueRecoveryShareServiceExecuteOptionsAndPlanRunLock(t *testing.T) {
	clearTaoEnv(t)
	root := t.TempDir()
	planDir := t.TempDir()
	current := "001-work"
	started := time.Date(2026, 7, 16, 19, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		Dir: planDir,
		State: plan.State{
			Status: plan.StatusInProgress,
			Repo:   plan.Repo{Root: root, Branch: "master"},
			Workspace: &plan.Workspace{
				Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(root), Path: root,
				Branch: "tao/plan-a", HeadSHA: "base",
			},
			Plan: plan.PlanState{
				ID: "plan-a", CurrentSlice: &current, PendingSlices: []string{current},
				LastRunCommitPolicy: run.CommitPolicySlice.String(), LastRunStartingDirty: []string{},
				Timing: plan.PlanTiming{StartedAt: &started},
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID: current, Status: plan.StatusInProgress, ExecutionRoot: root,
			ExecutionStart: &plan.SliceExecutionStart{Branch: "tao/plan-a", Head: "base", CommitPolicy: run.CommitPolicySlice.String(), WorkspaceStrategy: plan.WorkspaceStrategyWorktree},
			CommitIntent:   &plan.SliceCommitIntent{Hash: "intent", Policy: run.CommitPolicySlice.String()},
			Timing:         plan.SliceTiming{StartedAt: &started}, Verification: plan.Verification{Commands: []string{"go test ./internal/cli"}},
		}}},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}
	gitCalls := 0
	options := run.Options{
		ExecutionConfig: run.ExecutionConfig{ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{CommitPolicy: run.CommitPolicyNone, ExecutionMode: run.ExecutionModeCurrent}},
		RunDependencies: run.RunDependencies{CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			gitCalls++
			return errors.New("post-intent recovery must not inspect or mutate Git")
		}},
	}
	request := run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{
		Mode: run.ModeRun, CommitPolicy: run.CommitPolicySlice, ExecutionMode: run.ExecutionModeIsolated,
		Agent: run.AgentPi, ReviewEnabled: false,
	}}
	direct := run.NewService(repo, io.Discard, options)
	queuedExecute := newQueueExecutor(repo, io.Discard, options)
	queuedOwner := newQueuePlanOwner(repo, io.Discard, options)

	var queuedErr error
	if err := queuedOwner(context.Background(), request, func(ownedCtx context.Context) error {
		competitorRan := false
		competingErr := direct.WithPlanRunLock(context.Background(), request, func(context.Context) error {
			competitorRan = true
			return nil
		})
		if !errors.Is(competingErr, run.ErrCannotStart) || competitorRan {
			t.Fatalf("competing direct lock = %v, ran=%t; want shared cross-process lock refusal", competingErr, competitorRan)
		}
		queuedErr = queuedExecute(ownedCtx, request)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	directErr := direct.Execute(context.Background(), request)
	for label, err := range map[string]error{"direct": directErr, "queue": queuedErr} {
		if err == nil || !strings.Contains(err.Error(), "interrupted post-intent completion transaction") || !strings.Contains(err.Error(), "policy=\"slice\"") || !strings.Contains(err.Error(), "workspace=\"worktree\"") || !strings.Contains(err.Error(), "changed_paths=none") {
			t.Fatalf("%s recovery diagnostic/effective options = %v", label, err)
		}
	}
	if gitCalls != 0 {
		t.Fatalf("post-intent direct/queue recovery made %d Git calls", gitCalls)
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

func TestRunAllEnqueuesRunnablePlansAndDrains(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planA := "20260628-0200-plan-a"
	planB := "20260628-0201-plan-b"
	completedPlan := "20260628-0202-completed"
	writeQueuePlan(t, plansRoot, planA)
	writeQueuePlan(t, plansRoot, planB)
	writeRunPlan(t, plansRoot, completedPlan, plan.StatusCompleted, nil, []string{"001-work"}, "001-work", plan.StatusCompleted)

	var mu sync.Mutex
	executed := make([]string, 0, 2)
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error {
		mu.Lock()
		executed = append(executed, request.Input)
		mu.Unlock()
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "run", "--all"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotExecuted := append([]string(nil), executed...)
	mu.Unlock()
	assertRunAllPlanSet(t, gotExecuted, []string{planA, planB})
	assertContains(t, out.String(), "Reconciled queue: 2 runnable, 2 enqueued, 0 already queued")
	assertContains(t, out.String(), "Queue drain completed.")

	snapshot := loadQueueSnapshotForTest(t)
	statuses := queueSnapshotStatuses(snapshot)
	assertRunAllQueueStatus(t, statuses, planA, runqueue.QueueStatusSucceeded)
	assertRunAllQueueStatus(t, statuses, planB, runqueue.QueueStatusSucceeded)
	if _, ok := statuses[completedPlan]; ok {
		t.Fatalf("completed plan should not be queued: %+v", statuses)
	}
	for _, entry := range snapshot.Entries {
		if entry.AutoReworkPolicy == nil || !entry.AutoReworkPolicy.Enabled || entry.AutoReworkPolicy.MaxAttempts != runtimeconfig.DefaultMaxReworkAttempts {
			t.Fatalf("run --all queue entry missing default auto-rework policy: %+v", entry)
		}
	}
}

func TestRunAllStoppedAutoReworkRequiresExplicitRestart(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantExecutions  int
		wantQueueStatus runqueue.QueueStatus
		wantReopened    bool
	}{
		{name: "without restart flag", wantQueueStatus: runqueue.QueueStatusFailed},
		{name: "with restart flag", args: []string{"--rework-restart"}, wantExecutions: 1, wantQueueStatus: runqueue.QueueStatusSucceeded, wantReopened: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearTaoEnv(t)
			configureQueueDataHome(t)
			plansRoot := t.TempDir()
			planID := "20260718-0200-stopped-rework"
			finding := plan.ReviewFinding{
				Severity:   "major",
				File:       "internal/cli/run.go",
				Line:       293,
				Message:    "select stopped plans during run --all reconciliation",
				Suggestion: "initialize a fresh queue rework budget",
			}
			planDir := writeCLIReworkPlan(t, plansRoot, planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))
			now := time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)
			repo := plan.NewFileRepository(plansRoot)
			if err := repo.AppendEvent(planDir, plan.Event{
				Type:      plan.EventTypeReworkStopped,
				Timestamp: now.Add(-time.Minute),
				PlanID:    planID,
				Reason:    "automatic rework stalled on equivalent consecutive findings",
				Message:   "automatic rework stalled on equivalent consecutive findings",
			}); err != nil {
				t.Fatal(err)
			}

			finishedAt := now.Add(-2 * time.Minute)
			if err := queueStoreForTest(t).SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{{
				PlanID:        planID,
				Status:        runqueue.QueueStatusFailed,
				QueuedAt:      now.Add(-3 * time.Minute),
				FinishedAt:    &finishedAt,
				Error:         "previous automatic rework stopped",
				Mode:          run.ModeRun,
				CommitPolicy:  run.CommitPolicySlice,
				ExecutionMode: run.ExecutionModeIsolated,
				Agent:         run.AgentPi,
			}}}); err != nil {
				t.Fatal(err)
			}

			executions := 0
			withQueueExecutor(t, func(context.Context, run.Request) error {
				executions++
				return nil
			})
			var out bytes.Buffer
			app := queueTestApp(plansRoot, &out)
			args := append([]string{"--plans-dir", plansRoot, "run", "--all"}, tt.args...)
			if err := app.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			if executions != tt.wantExecutions {
				t.Fatalf("executions = %d, want %d", executions, tt.wantExecutions)
			}
			assertContains(t, out.String(), "Reconciled queue: 1 runnable, 1 enqueued, 0 already queued")

			detail, err := repo.ResolvePlan(context.Background(), planID)
			if err != nil {
				t.Fatal(err)
			}
			reopened := detail.State.Status == plan.StatusInProgress && len(detail.State.Plan.PendingSlices) == 1
			if reopened != tt.wantReopened {
				t.Fatalf("reopened = %t, want %t; status=%q pending=%v", reopened, tt.wantReopened, detail.State.Status, detail.State.Plan.PendingSlices)
			}
			if tt.wantReopened {
				var roundEvent *plan.Event
				for i := range detail.Events {
					if detail.Events[i].Type == plan.EventTypeReworkRound {
						roundEvent = &detail.Events[i]
					}
				}
				if roundEvent == nil || roundEvent.Attempts != 1 {
					t.Fatalf("fresh rework budget event = %+v, want attempt 1", roundEvent)
				}
			} else {
				for _, want := range []string{"THE LOOP IS GOING IN CIRCLES", finding.Message, finding.Suggestion, "A new automatic-rework budget was not started", "--rework-restart"} {
					assertContains(t, out.String(), want)
				}
			}

			snapshot := loadQueueSnapshotForTest(t)
			if len(snapshot.Entries) != 2 {
				t.Fatalf("queue entries = %d, want prior failure plus fresh attempt: %+v", len(snapshot.Entries), snapshot)
			}
			latest := snapshot.Entries[1]
			if latest.Status != tt.wantQueueStatus {
				t.Fatalf("latest queue status = %q, want %q: %+v", latest.Status, tt.wantQueueStatus, latest)
			}
			if tt.wantReopened && (latest.ReworkAttempts != 1 || latest.ReworkBaselineRound == nil || *latest.ReworkBaselineRound != 0) {
				t.Fatalf("fresh queue budget progress = %+v, want baseline 0 and attempt 1", latest)
			}
		})
	}
}

func TestRunAllNotifyCommandReceivesSummaryEnv(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	t.Setenv(runtimeconfig.EnvNotifyCommand, "notify --message tao")
	plansRoot := t.TempDir()
	planA := "20260628-0203-notify"
	writeQueuePlan(t, plansRoot, planA)
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error { return nil })

	var out bytes.Buffer
	var errOut bytes.Buffer
	type notifyCall struct {
		cwd  string
		name string
		args []string
		env  map[string]string
	}
	var calls []notifyCall
	app := queueTestApp(plansRoot, &out)
	app.Err = &errOut
	app.CommandRunner = func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("notify command context has no deadline")
		}
		calls = append(calls, notifyCall{
			cwd:  cwd,
			name: name,
			args: append([]string(nil), args...),
			env: map[string]string{
				"TAO_BATCH_TOTAL":     os.Getenv("TAO_BATCH_TOTAL"),
				"TAO_BATCH_SUCCEEDED": os.Getenv("TAO_BATCH_SUCCEEDED"),
				"TAO_BATCH_REVIEWED":  os.Getenv("TAO_BATCH_REVIEWED"),
				"TAO_BATCH_FAILED":    os.Getenv("TAO_BATCH_FAILED"),
				"TAO_BATCH_PENDING":   os.Getenv("TAO_BATCH_PENDING"),
			},
		})
		return nil
	}

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "run", "--all"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("notify calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.cwd != "" || call.name != "sh" || len(call.args) != 2 || call.args[0] != "-c" || call.args[1] != "notify --message tao" {
		t.Fatalf("notify command = cwd %q name %q args %v", call.cwd, call.name, call.args)
	}
	wantEnv := map[string]string{
		"TAO_BATCH_TOTAL":     "1",
		"TAO_BATCH_SUCCEEDED": "1",
		"TAO_BATCH_REVIEWED":  "0",
		"TAO_BATCH_FAILED":    "0",
		"TAO_BATCH_PENDING":   "0",
	}
	for name, want := range wantEnv {
		if got := call.env[name]; got != want {
			t.Fatalf("notify env[%s] = %q, want %q in %+v", name, got, want, call.env)
		}
	}
	if errOut.String() != "" {
		t.Fatalf("unexpected notify stderr: %s", errOut.String())
	}
	assertContains(t, out.String(), "Queue drain completed.")
	assertContains(t, out.String(), "Final batch summary: 1 succeeded (0 reviewed), 0 failed, 0 running, 0 pending of 1")
}

func TestRunAllActiveFilteringEnqueuesOnlyActiveRunnablePlans(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	activePlan := "20260628-0210-active"
	plannedPlan := "20260628-0211-planned"
	writeRunPlan(t, plansRoot, activePlan, plan.StatusInProgress, []string{"001-work"}, nil, "001-work", plan.StatusPending)
	writeQueuePlan(t, plansRoot, plannedPlan)

	var mu sync.Mutex
	var executed []string
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error {
		mu.Lock()
		executed = append(executed, request.Input)
		mu.Unlock()
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "run", "--all", "--active"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotExecuted := append([]string(nil), executed...)
	mu.Unlock()
	assertRunAllPlanSet(t, gotExecuted, []string{activePlan})
	assertContains(t, out.String(), "Reconciled queue: 1 runnable, 1 enqueued, 0 already queued")

	statuses := queueSnapshotStatuses(loadQueueSnapshotForTest(t))
	assertRunAllQueueStatus(t, statuses, activePlan, runqueue.QueueStatusSucceeded)
	if _, ok := statuses[plannedPlan]; ok {
		t.Fatalf("planned inactive plan should not be queued with --active: %+v", statuses)
	}
}

func TestRunAllRejectsPositionalPlanID(t *testing.T) {
	clearTaoEnv(t)
	app := App{Out: io.Discard, Err: io.Discard}

	err := app.run(context.Background(), plan.NewFileRepository(t.TempDir()), []string{"--all", "plan-a"})
	if err == nil || !strings.Contains(err.Error(), "--all cannot be combined") {
		t.Fatalf("expected --all positional rejection, got %v", err)
	}
}

func assertRunAllPlanSet(t *testing.T, got []string, want []string) {
	t.Helper()
	gotSet := stringSet(got)
	if len(gotSet) != len(want) {
		t.Fatalf("executed plans = %v, want %v", got, want)
	}
	for _, planID := range want {
		if !gotSet[planID] {
			t.Fatalf("executed plans = %v, want %v", got, want)
		}
	}
}

func assertRunAllQueueStatus(t *testing.T, statuses map[string]runqueue.QueueStatus, planID string, want runqueue.QueueStatus) {
	t.Helper()
	if got := statuses[planID]; got != want {
		t.Fatalf("queue status for %s = %q, want %q in %+v", planID, got, want, statuses)
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
		if name != "pi" || strings.Join(args, " ") != "--mode rpc" {
			t.Fatalf("unexpected pi process start: %s %#v", name, args)
		}
		proc := newFakeCLIPiProcess(t)
		go func() {
			defer proc.finish()
			cmd, err := proc.readCommand()
			if err != nil {
				return
			}
			if cmd["type"] == "prompt" && onPrompt != nil {
				onPrompt(stringValue(cmd["message"]))
			}
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
