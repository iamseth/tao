package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

var runCommand = commandMetadata{
	name:      "run",
	minPrefix: "r",
	usageLines: []string{
		"run (r) [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--continue] [--no-review] [--auto-rework] [--max-rework-attempts N] [--rework-restart] [--dangerously-skip-permissions] <plan-id-or-slug-or-path>",
		"run (r) [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--continue] [--no-review] [--auto-rework] [--max-rework-attempts N] [--rework-restart] [--dangerously-skip-permissions] --all [--active]",
	},
	completionDescription: "Run pending slices with the selected agent",
	long:                  "Run pending slices for a Tao plan with the selected agent. Tao prepares the requested workspace, executes pending work, automatically reworks review findings by default, records verification metadata, and follows the configured commit policy.",
	examples: "  tao run 20260628-1618-kubectl-style-help\n" +
		"  tao run --max-slices 1 --commit-policy slice my-plan\n" +
		"  tao run --all --active\n" +
		"  tao run --auto-rework=false my-plan",
	registerFlags: registerRunFlags,
	completion: completionContext{
		flagValues: map[string]completionFlagValue{
			"auto-rework":         {kind: completionValueBoolean, label: "boolean", values: []string{"true", "false"}},
			"commit-policy":       {kind: completionValueEnum, label: "policy", values: []string{"slice", "none"}},
			"execution-mode":      {kind: completionValueEnum, label: "mode", values: []string{"isolated", "current"}},
			"max-rework-attempts": {kind: completionValueCount, label: "count"},
			"max-slices":          {kind: completionValueCount, label: "count"},
		},
		positional: completionPositional{position: 3, completer: completeRunPlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.run(c.ctx, c.repo, c.args)
	},
}

// registerRunRequestFlags registers the run-request flags shared by every
// handler that executes plans, currently tao run and tao note run.
func registerRunRequestFlags(fs *flag.FlagSet) {
	defaults := runtimeFlagDefaults()
	fs.Int("max-slices", 0, "maximum slices to run; use 0 for all")
	fs.String("commit-policy", defaults.CommitPolicy.String(), "automatic commit policy: slice or none")
	fs.String("execution-mode", defaults.ExecutionModeValue().String(), "execution mode: isolated or current")
	fs.Bool("pull-request", defaults.PullRequestValue(), "create a GitHub pull request after a completed full run")
	fs.Bool("dangerously-skip-permissions", defaults.SkipPermissions, "legacy no-op for the Pi agent")
	fs.Bool("no-review", !defaults.ReviewEnabledValue(), "disable automatic plan review for this run")
}

func registerRunFlags(fs *flag.FlagSet) {
	registerRunRequestFlags(fs)
	autoRework, maxReworkAttempts, _ := runReworkEnvDefaults()
	fs.Bool("continue", false, "continue a blocked plan or slice")
	fs.Bool("auto-rework", autoRework, "automatically rework plans with requested changes")
	fs.Int("max-rework-attempts", maxReworkAttempts, "maximum automatic rework cycles (0 disables)")
	fs.Bool("rework-restart", false, "start a new automatic-rework budget after a previous stop")
	fs.Bool("all", false, "enqueue and drain all runnable plans")
	fs.Bool("active", false, "with --all, enqueue only active runnable plans")
}

type runFlagValues struct {
	MaxSlices     int
	CommitPolicy  runtimeconfig.CommitPolicy
	ExecutionMode runtimeconfig.ExecutionMode
	PullRequest   bool
	Continue      bool
	NoReview      bool
}

func runRequestOverridesFromFlags(fs *flag.FlagSet, values runFlagValues) runtimeconfig.RunOptionsPatch {
	var overrides runtimeconfig.RunOptionsPatch
	if flagWasProvided(fs, "max-slices") {
		overrides = overrides.WithMaxSlices(values.MaxSlices)
	}
	if flagWasProvided(fs, "continue") {
		overrides = overrides.WithContinue(values.Continue)
	}
	if flagWasProvided(fs, "commit-policy") {
		overrides.CommitPolicy = values.CommitPolicy
	}
	if flagWasProvided(fs, "execution-mode") {
		overrides.ExecutionMode = values.ExecutionMode
	}
	if flagWasProvided(fs, "pull-request") {
		overrides = overrides.WithPullRequest(values.PullRequest)
	}
	if flagWasProvided(fs, "no-review") {
		overrides = overrides.WithReviewEnabled(!values.NoReview)
	}
	return overrides
}

// runRequestInputs carries the resolved environment defaults, flag-derived
// request overrides, and effective permission-skip value shared by handlers
// that execute plans.
type runRequestInputs struct {
	defaults        envDefaults
	overrides       runtimeconfig.RunOptionsPatch
	skipPermissions bool
}

// resolveRunRequestFlags resolves the shared run-request preamble from parsed
// flags. Flags the calling handler did not register resolve to zero values and
// never count as provided overrides.
func resolveRunRequestFlags(fs *flag.FlagSet) (runRequestInputs, error) {
	defaults, err := cliEnvDefaults()
	if err != nil {
		return runRequestInputs{}, err
	}
	overrides := runRequestOverridesFromFlags(fs, runFlagValues{
		MaxSlices:     flagIntValue(fs, "max-slices"),
		CommitPolicy:  runtimeconfig.CommitPolicy(flagStringValue(fs, "commit-policy")),
		ExecutionMode: runtimeconfig.ExecutionMode(flagStringValue(fs, "execution-mode")),
		PullRequest:   flagBoolValue(fs, "pull-request"),
		Continue:      flagBoolValue(fs, "continue"),
		NoReview:      flagBoolValue(fs, "no-review"),
	})
	return runRequestInputs{
		defaults:        defaults,
		overrides:       overrides,
		skipPermissions: effectiveBoolFlagValue(fs, "dangerously-skip-permissions", defaults.SkipPermissions),
	}, nil
}

func resolveRunAutoReworkPolicy(fs *flag.FlagSet, reviewEnabled bool) (runtimeconfig.AutoReworkPolicy, error) {
	if _, _, err := runReworkEnvDefaults(); err != nil {
		return runtimeconfig.AutoReworkPolicy{}, err
	}
	enabled := flagBoolValue(fs, "auto-rework")
	explicitConflict := enabled && flagWasProvided(fs, "auto-rework") &&
		flagWasProvided(fs, "no-review") && flagBoolValue(fs, "no-review")
	if !reviewEnabled && !explicitConflict {
		enabled = false
	}
	return runtimeconfig.ResolveAutoReworkPolicy(enabled, flagIntValue(fs, "max-rework-attempts"), reviewEnabled)
}

func (a App) run(ctx context.Context, repo queueRepository, args []string) error {
	fs, positional, err := a.parseArgsFor(&commandMetadata{name: "run", registerFlags: registerRunFlags}, args)
	if err != nil {
		return err
	}
	inputs, err := resolveRunRequestFlags(fs)
	if err != nil {
		return err
	}
	runAll := flagBoolValue(fs, "all")
	activeOnly := flagBoolValue(fs, "active")
	reworkRestart := flagBoolValue(fs, "rework-restart")
	if activeOnly && !runAll {
		return errors.New("--active requires --all")
	}
	if runAll {
		if len(positional) != 0 {
			return errors.New("--all cannot be combined with a positional plan id")
		}
		runtime, err := newQueueRuntime(inputs.defaults, inputs.overrides, inputs.skipPermissions, a.StatusReporter)
		if err != nil {
			return err
		}
		policy, err := resolveRunAutoReworkPolicy(fs, runtime.options.ReviewEnabled)
		if err != nil {
			return err
		}
		return a.runAll(ctx, repo, runtime, activeOnly, policy, reworkRestart)
	}
	if err := requirePositionals(positional, 1, "usage: tao run [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--continue] [--no-review] [--auto-rework] [--max-rework-attempts N] [--rework-restart] [--dangerously-skip-permissions] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	input := positional[0]
	request, err := inputs.defaults.newRunRequest(input, inputs.overrides)
	if err != nil {
		return err
	}
	policy, err := resolveRunAutoReworkPolicy(fs, request.ReviewEnabled)
	if err != nil {
		return err
	}
	return a.executeResolvedRun(ctx, repo, input, request, inputs.skipPermissions, policy, reworkRestart)
}

var executeSinglePlan = func(service run.Service, ctx context.Context, request run.Request) error {
	return service.Execute(ctx, request)
}

// executeResolvedRun is the single-plan execution boundary shared by run entry
// points after their inputs and runtime options have been fully resolved.
func (a App) executeResolvedRun(ctx context.Context, repo queueRepository, input string, request run.Request, skipPermissions bool, policy runtimeconfig.AutoReworkPolicy, reworkRestart bool) error {
	service := run.NewService(repo, a.Out, run.Options{
		ExecutionConfig: run.ExecutionConfig{ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Agent: request.Agent}, SkipPermissions: skipPermissions},
		RunDependencies: run.RunDependencies{CommandRunner: a.CommandRunner, ProcessStarter: a.ProcessStarter, StatusReporter: a.StatusReporter, SessionLogWriter: a.Out, Now: a.now},
	})
	runCtx, stopSignals := newCommandSignalContext(ctx)
	defer stopSignals()

	return service.WithPlanRunLock(runCtx, request, func(ownedCtx context.Context) error {
		baseline := 0
		acknowledgedStop := false
		if policy.Enabled && policy.MaxAttempts > 0 {
			detail, err := repo.ResolvePlan(ownedCtx, input)
			if err != nil {
				return err
			}
			_, acknowledgedStop, err = autoReworkRestartGuard(detail, reworkRestart)
			if err != nil {
				return err
			}
			baseline = reworkpkg.RoundCount(detail)
		}
		maxAttempts := 0
		if policy.Enabled {
			maxAttempts = policy.MaxAttempts
		}
		firstExecution := true
		driver := newReworkDriver(repo, a.now)
		return driver.Loop(ownedCtx, request.Input, reworkpkg.LoopOptions{
			Baseline:            baseline,
			MaxAttempts:         maxAttempts,
			DecideBeforeExecute: acknowledgedStop,
			BeforeDecision:      automaticReworkPhaseHook(ownedCtx, maxAttempts, policy.Enabled),
			Execute: func(executeCtx context.Context) error {
				err := executeSinglePlan(service, executeCtx, request)
				if firstExecution {
					firstExecution = false
					return decorateRunCannotStartError(executeCtx, repo, input, err)
				}
				return err
			},
			PersistProgress: func(context.Context, int, int, string) error { return nil },
			LogProgress: func(round int) error {
				return writef(a.Out, "Plan reopened for rework round %d\n", round)
			},
		})
	})
}

func decorateRunCannotStartError(ctx context.Context, repo queueRepository, input string, err error) error {
	if err == nil || !errors.Is(err, run.ErrCannotStart) || repo == nil {
		return err
	}
	detail, resolveErr := repo.ResolvePlan(ctx, input)
	if resolveErr != nil || detail == nil {
		return err
	}
	commands := runUnblockCommands(detail, input)
	if len(commands) == 0 {
		return err
	}
	return fmt.Errorf("%w\n\n%s", err, formatRunUnblockCommands(commands))
}

func runUnblockCommands(detail *plan.PlanDetail, input string) []string {
	if detail == nil {
		return nil
	}
	planRef := strings.TrimSpace(input)
	if planRef == "" {
		planRef = detail.State.Plan.ID
	}
	if strings.TrimSpace(planRef) == "" {
		return nil
	}
	planRef = shellCommandArg(planRef)
	derived := plan.Derive(detail, time.Time{})
	if derived.Capabilities.NeedsApproval {
		if slice := runApprovalSlice(detail, derived); slice != nil {
			runCommand := "tao run " + planRef
			if runNeedsContinueAfterUnblock(detail, slice) {
				runCommand = "tao run --continue " + planRef
			}
			return []string{
				"tao approve --slice " + shellCommandArg(slice.ID) + " " + planRef,
				runCommand,
			}
		}
	}
	if derived.Capabilities.CanContinue && !derived.Capabilities.CanRun {
		return []string{"tao run --continue " + planRef}
	}
	return nil
}

// runApprovalSlice returns the slice requiring approval, identified by the typed
// ApprovalSliceID field that plan.RunCapabilitiesFromLifecycle populates via errors.As.
func runApprovalSlice(detail *plan.PlanDetail, derived plan.DerivedPlan) *plan.Slice {
	id := derived.Capabilities.ApprovalSliceID
	if id == "" {
		return nil
	}
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == id {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}

func runNeedsContinueAfterUnblock(detail *plan.PlanDetail, slice *plan.Slice) bool {
	return detail.State.Status == plan.StatusBlocked || (slice != nil && slice.Status == plan.StatusBlocked)
}

func formatRunUnblockCommands(commands []string) string {
	var b strings.Builder
	b.WriteString("To unblock this plan, run:")
	for _, command := range commands {
		b.WriteString("\n  ")
		b.WriteString(command)
	}
	return b.String()
}

func shellCommandArg(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`!*?[]{}();&|<>") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (a App) runAll(ctx context.Context, repo queueRepository, runtime queueRuntime, activeOnly bool, policy runtimeconfig.AutoReworkPolicy, reworkRestart bool) error {
	if repo == nil {
		return errors.New("run --all requires a plan repository")
	}
	return a.startQueueDrain(ctx, repo, queueDrainOptions{maxParallel: 1, runtime: runtime, activeOnly: activeOnly, autoReworkPolicy: policy, reworkRestart: reworkRestart})
}
