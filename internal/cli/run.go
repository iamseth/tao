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
	planview "github.com/iamseth/tao/internal/view"
)

var runCommand = commandMetadata{
	name:      "run",
	minPrefix: "r",
	usageLines: []string{
		"run (r) [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--continue|--restart|--repair-verification|--reverify] [--no-review] [--no-run-header] [--auto-rework] [--max-rework-attempts N] [--rework-restart] [--dangerously-skip-permissions] <plan-id-or-slug-or-path>",
	},
	completionDescription: "Run pending slices with the selected agent",
	long:                  "Run pending slices for a Tao plan with the selected agent. Tao prepares the requested workspace, executes pending work, automatically reworks review findings by default, records verification metadata, and follows the configured commit policy. In a sufficiently large terminal, Tao displays a pinned run header unless --no-run-header disables it.",
	examples: "  tao run 20260628-1618-kubectl-style-help\n" +
		"  tao run --max-slices 1 --commit-policy slice my-plan\n" +
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
		positional: completionPositional{index: 1, label: "plan", completer: completeRunnablePlanIDs},
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
	fs.Bool("continue", false, "continue a blocked slice at its preserved execution boundary")
	fs.Bool("restart", false, "restart a safe blocked automatic slice on a newer baseline")
	fs.Bool("repair-verification", false, "append and run one bounded repair for current failed final verification")
	fs.Bool("reverify", false, "rerun final verification at the exact recorded failed head")
	fs.Bool("no-run-header", !runHeaderEnvDefault(), "disable the pinned run header")
	fs.Bool("auto-rework", autoRework, "automatically rework plans with requested changes")
	fs.Int("max-rework-attempts", maxReworkAttempts, "maximum automatic rework cycles (0 disables)")
	fs.Bool("rework-restart", false, "start a new automatic-rework budget after a previous stop")
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

type planRunRepository interface {
	run.Repository
}

func (a App) run(ctx context.Context, repo planRunRepository, args []string) error {
	fs, positional, err := a.parseArgsFor(&commandMetadata{name: "run", registerFlags: registerRunFlags}, args)
	if err != nil {
		return err
	}
	inputs, err := resolveRunRequestFlags(fs)
	if err != nil {
		return err
	}
	reworkRestart := flagBoolValue(fs, "rework-restart")
	blockedRestart := flagBoolValue(fs, "restart")
	repairVerification := flagBoolValue(fs, "repair-verification")
	reverify := flagBoolValue(fs, "reverify")
	continueRun := flagBoolValue(fs, "continue")
	recoveryModeCount := 0
	for _, enabled := range []bool{continueRun, blockedRestart, repairVerification, reverify} {
		if enabled {
			recoveryModeCount++
		}
	}
	if recoveryModeCount > 1 {
		return fmt.Errorf("--continue, --restart, --repair-verification, and --reverify are mutually exclusive")
	}
	repositoryDefaults, err := a.currentRepositoryRunOptions(ctx)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao run [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--continue|--restart|--repair-verification|--reverify] [--no-review] [--no-run-header] [--auto-rework] [--max-rework-attempts N] [--rework-restart] [--dangerously-skip-permissions] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	input := positional[0]
	request, err := inputs.defaults.newRunRequestWithRepository(input, repositoryDefaults, inputs.overrides)
	if err != nil {
		return err
	}
	request.RestartBlocked = blockedRestart
	request.RepairVerification = repairVerification
	request.Reverify = reverify
	policy, err := resolveRunAutoReworkPolicy(fs, request.ReviewEnabled)
	if err != nil {
		return err
	}
	return a.executeResolvedRun(ctx, repo, input, request, inputs.skipPermissions, policy, reworkRestart, flagBoolValue(fs, "no-run-header"))
}

var executeSinglePlan = func(service run.Service, ctx context.Context, request run.Request) error {
	return service.Execute(ctx, request)
}

// executeResolvedRun is the single-plan execution boundary shared by run entry
// points after their inputs and runtime options have been fully resolved.
func (a App) executeResolvedRun(ctx context.Context, repo planRunRepository, input string, request run.Request, skipPermissions bool, policy runtimeconfig.AutoReworkPolicy, reworkRestart, noRunHeader bool) error {
	if request.Reverify {
		policy.Enabled = false
	}
	runCtx, stopSignals := newCommandSignalContext(ctx)
	defer stopSignals()

	runOut, headerReporter, closeHeader := installRunHeader(runCtx, a.Out, noRunHeader)
	defer closeHeader()
	service := run.NewService(repo, runOut, run.Options{
		ExecutionConfig: run.ExecutionConfig{
			ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Agent: request.Agent},
			SkipPermissions:    skipPermissions,
			MaxReworkAttempts:  policy.MaxAttempts,
		},
		RunDependencies: run.RunDependencies{CommandRunner: a.CommandRunner, ProcessStarter: a.ProcessStarter, StatusReporter: a.StatusReporter, HeaderReporter: headerReporter, SessionLogWriter: runOut, Now: a.now},
	})

	return service.WithPlanRunLock(runCtx, request, func(ownedCtx context.Context) error {
		firstExecution := true
		driver := newReworkDriver(repo, a.now)
		return driver.Run(ownedCtx, request.Input, reworkpkg.RunOptions{
			Enabled:        policy.Enabled,
			MaxAttempts:    policy.MaxAttempts,
			AllowRestart:   reworkRestart,
			BeforeDecision: automaticReworkPhaseHook(policy.MaxAttempts, policy.Enabled),
			Execute: func(executeCtx context.Context) error {
				err := executeSinglePlan(service, executeCtx, request)
				if firstExecution {
					firstExecution = false
					return decorateRunCannotStartError(executeCtx, repo, input, err)
				}
				return err
			},
			LogProgress: func(round int) error {
				return writef(runOut, "Plan reopened for rework round %d\n", round)
			},
		})
	})
}

func decorateRunCannotStartError(ctx context.Context, repo planRunRepository, input string, err error) error {
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
	derived := plan.Derive(detail, time.Time{})
	if !derived.Capabilities.NeedsApproval && derived.Capabilities.CanContinue && !derived.Capabilities.CanRun {
		if slice := runBlockedSlice(detail, derived); slice != nil {
			return fmt.Errorf("%w\n\n%s", err, planview.FormatBlockedRunGuidance(slice.ID, slice.BlockerNote, commands[0]))
		}
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
	return runSliceByID(detail, derived.Capabilities.ApprovalSliceID)
}

func runBlockedSlice(detail *plan.PlanDetail, derived plan.DerivedPlan) *plan.Slice {
	id := derived.NextSliceID
	if detail.State.Plan.CurrentSlice != nil {
		id = *detail.State.Plan.CurrentSlice
	}
	return runSliceByID(detail, id)
}

func runSliceByID(detail *plan.PlanDetail, id string) *plan.Slice {
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
	b.WriteString("Resolve the required action before continuing. Run:")
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
