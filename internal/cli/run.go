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
		"run (r) [--agent pi|claude] [--session-timeout DURATION] [--review-agent pi|claude] [--model NAME] [--rework-escalation-model NAME] [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--continue|--restart|--repair-verification|--reverify] [--no-review] [--no-run-header] [--auto-rework] [--max-rework-attempts N] [--rework-restart] [--dangerously-skip-permissions] <plan-id-or-slug-or-path>",
	},
	completionDescription: "Run pending slices with the selected agent",
	long:                  "Run pending slices for a Tao plan with the selected agent. Tao prepares the requested workspace, executes pending work, automatically reworks review findings by default, records verification metadata, and follows the configured commit policy. In a sufficiently large terminal, Tao displays a pinned run header unless --no-run-header disables it.",
	examples: "  tao run 20260628-1618-kubectl-style-help\n" +
		"  tao run --max-slices 1 --commit-policy slice my-plan\n" +
		"  tao run --auto-rework=false my-plan",
	registerFlags:        registerRunFlags,
	registerRuntimeFlags: App.registerRunFlags,
	completion: completionContext{
		flagValues: map[string]completionFlagValue{
			"agent":                          {kind: completionValueEnum, label: "agent", values: []string{"pi", "claude"}},
			"session-timeout":                {kind: completionValueText, label: "duration"},
			"auto-rework":                    {kind: completionValueBoolean, label: "boolean", values: []string{"true", "false"}},
			"commit-policy":                  {kind: completionValueEnum, label: "policy", values: []string{"slice", "none"}},
			"execution-mode":                 {kind: completionValueEnum, label: "mode", values: []string{"isolated", "current"}},
			"max-rework-attempts":            {kind: completionValueCount, label: "count"},
			"max-slices":                     {kind: completionValueCount, label: "count"},
			"review-agent":                   {kind: completionValueEnum, label: "agent", values: []string{"pi", "claude"}},
			"rework-escalation-from-attempt": {kind: completionValueCount, label: "count"},
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
func (a App) registerRunRequestFlags(fs *flag.FlagSet) {
	defaults := a.flagDefaults()
	fs.String("agent", defaults.Agent.String(), "agent runtime: pi or claude")
	fs.String("session-timeout", defaults.SessionTimeoutValue().String(), "session timeout as a Go duration; 0 disables")
	fs.Int("max-slices", 0, "maximum slices to run; use 0 for all")
	fs.String("commit-policy", defaults.CommitPolicy.String(), "automatic commit policy: slice or none")
	fs.String("execution-mode", defaults.ExecutionModeValue().String(), "execution mode: isolated or current")
	fs.Bool("pull-request", defaults.PullRequestValue(), "create a GitHub pull request after a completed full run")
	fs.Bool("dangerously-skip-permissions", defaults.SkipPermissions, "bypass Claude permission checks; compatibility no-op for Pi")
	fs.Bool("no-review", !defaults.ReviewEnabledValue(), "disable automatic plan review for this run")
}

func registerRunFlags(fs *flag.FlagSet) { (App{}).registerRunFlags(fs) }

func (a App) registerRunFlags(fs *flag.FlagSet) {
	a.registerRunRequestFlags(fs)
	registerReviewAgentFlag(fs)
	defaults := a.flagDefaults()
	fs.String("model", "", "override the agent model for every role in this run")
	fs.String("rework-escalation-model", "", "override the model for late automatic rework attempts")
	fs.Bool("continue", false, "continue a blocked slice at its preserved execution boundary")
	fs.Bool("restart", false, "restart a safe blocked automatic slice on a newer baseline")
	fs.Bool("repair-verification", false, "append and run one bounded repair for current failed final verification")
	fs.Bool("reverify", false, "rerun final verification at the exact recorded failed head")
	fs.Bool("no-run-header", !defaults.RunHeader, "disable the pinned run header")
	fs.Bool("auto-rework", *defaults.AutoRework, "deprecated: use --max-rework-attempts (false=0, true=5)")
	fs.Int("rework-escalation-from-attempt", defaults.ReworkEscalationFromAttemptValue(), "first automatic rework attempt eligible for escalation")
	fs.Int("max-rework-attempts", *defaults.MaxReworkAttempts, "maximum automatic rework cycles (0 disables)")
	fs.Bool("rework-restart", false, "start a new automatic-rework budget after a previous stop")
}

type runFlagValues struct {
	Agent                 runtimeconfig.AgentKind
	SessionTimeout        time.Duration
	Model                 string
	ReworkEscalationModel string
	MaxSlices             int
	CommitPolicy          runtimeconfig.CommitPolicy
	ExecutionMode         runtimeconfig.ExecutionMode
	PullRequest           bool
	Continue              bool
	NoReview              bool
}

func runRequestOverridesFromFlags(fs *flag.FlagSet, values runFlagValues) runtimeconfig.RunOptionsPatch {
	var overrides runtimeconfig.RunOptionsPatch
	if flagWasProvided(fs, "agent") {
		overrides.Agent = values.Agent
	}
	if flagWasProvided(fs, "session-timeout") {
		overrides = overrides.WithSessionTimeout(values.SessionTimeout)
	}
	if flagWasProvided(fs, "model") && values.Model != "" {
		overrides = overrides.WithModelForAllRoles(values.Model)
	}
	if flagWasProvided(fs, "rework-escalation-model") {
		overrides.ReworkEscalation = values.ReworkEscalationModel
	}
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
func (a App) resolveRunRequestFlags(fs *flag.FlagSet) (runRequestInputs, error) {
	defaults, err := a.runEnvDefaults()
	if err != nil {
		return runRequestInputs{}, err
	}
	overrides, err := runRequestFlagOverrides(fs)
	if err != nil {
		return runRequestInputs{}, err
	}
	return runRequestInputs{
		defaults:        defaults,
		overrides:       overrides,
		skipPermissions: effectiveBoolFlagValue(fs, "dangerously-skip-permissions", defaults.SkipPermissions),
	}, nil
}

func runRequestFlagOverrides(fs *flag.FlagSet) (runtimeconfig.RunOptionsPatch, error) {
	model, err := modelFlagValue(fs)
	if err != nil {
		return runtimeconfig.RunOptionsPatch{}, err
	}
	var escalationModel string
	if flagWasProvided(fs, "rework-escalation-model") {
		escalationModel, err = runtimeconfig.ParseModelName(flagStringValue(fs, "rework-escalation-model"))
		if err != nil {
			return runtimeconfig.RunOptionsPatch{}, fmt.Errorf("--rework-escalation-model: %w", err)
		}
	}
	var agentKind runtimeconfig.AgentKind
	if flagWasProvided(fs, "agent") {
		if strings.TrimSpace(flagStringValue(fs, "agent")) == "" {
			return runtimeconfig.RunOptionsPatch{}, fmt.Errorf("--agent: must be pi or claude")
		}
		agentKind, err = runtimeconfig.ParseAgentKind(flagStringValue(fs, "agent"))
		if err != nil {
			return runtimeconfig.RunOptionsPatch{}, fmt.Errorf("--agent: %w", err)
		}
	}
	var timeout time.Duration
	if flagWasProvided(fs, "session-timeout") {
		value := flagStringValue(fs, "session-timeout")
		if _, err := runtimeconfig.ParseSetting("session_timeout", value); err != nil {
			return runtimeconfig.RunOptionsPatch{}, fmt.Errorf("--session-timeout: %w", err)
		}
		timeout, err = time.ParseDuration(value)
		if err != nil {
			return runtimeconfig.RunOptionsPatch{}, fmt.Errorf("--session-timeout: %w", err)
		}
	}
	overrides := runRequestOverridesFromFlags(fs, runFlagValues{
		Agent:                 agentKind,
		SessionTimeout:        timeout,
		Model:                 model,
		ReworkEscalationModel: escalationModel,
		MaxSlices:             flagIntValue(fs, "max-slices"),
		CommitPolicy:          runtimeconfig.CommitPolicy(flagStringValue(fs, "commit-policy")),
		ExecutionMode:         runtimeconfig.ExecutionMode(flagStringValue(fs, "execution-mode")),
		PullRequest:           flagBoolValue(fs, "pull-request"),
		Continue:              flagBoolValue(fs, "continue"),
		NoReview:              flagBoolValue(fs, "no-review"),
	})
	reviewAgent, err := reviewAgentFlagValue(fs, false)
	if err != nil {
		return runtimeconfig.RunOptionsPatch{}, err
	}
	overrides.ReviewAgent = reviewAgent
	return overrides, nil
}

func (a App) resolveRunAutoReworkPolicy(fs *flag.FlagSet, reviewEnabled bool) (runtimeconfig.AutoReworkPolicy, error) {
	options, err := a.resolveRunReworkOptions(fs, reviewEnabled, runtimeconfig.ReworkOptionsPatch{}, true)
	return runtimeconfig.AutoReworkPolicy{Enabled: options.MaxAttempts > 0, MaxAttempts: options.MaxAttempts}, err
}

func (a App) resolveRunReworkOptions(fs *flag.FlagSet, reviewEnabled bool, repository runtimeconfig.ReworkOptionsPatch, warn bool) (runtimeconfig.ResolvedReworkOptions, error) {
	warning := func(message string) {
		if warn && a.Err != nil {
			_, _ = fmt.Fprintln(a.Err, "warning: "+message)
		}
	}
	for _, row := range a.envSnapshot().Status() {
		if row.Name == runtimeconfig.EnvAutoRework {
			warning("TAO_AUTO_REWORK is deprecated; use TAO_MAX_REWORK_ATTEMPTS")
		}
	}
	var invocation runtimeconfig.ReworkOptionsPatch
	if flagWasProvided(fs, "auto-rework") {
		value := flagBoolValue(fs, "auto-rework")
		invocation.AutoRework = &value
		warning("--auto-rework is deprecated; use --max-rework-attempts")
	}
	if flagWasProvided(fs, "max-rework-attempts") {
		value := flagIntValue(fs, "max-rework-attempts")
		invocation.MaxAttempts = &value
	}
	if flagWasProvided(fs, "rework-escalation-from-attempt") {
		value := flagIntValue(fs, "rework-escalation-from-attempt")
		invocation.EscalationFromAttempt = &value
	}
	defaults, err := a.envDefaultsFor(runtimeconfig.EnvAutoRework, runtimeconfig.EnvMaxReworkAttempts, runtimeconfig.EnvReworkEscalationFromAttempt)
	if err != nil {
		return runtimeconfig.ResolvedReworkOptions{}, err
	}
	environment := runtimeconfig.ReworkOptionsPatch{MaxAttempts: defaults.MaxReworkAttempts, EscalationFromAttempt: defaults.ReworkEscalationFromAttempt}
	raw, err := runtimeconfig.ResolveReworkOptions(true, false, environment, repository, invocation)
	if err != nil {
		return raw, err
	}
	if !reviewEnabled && raw.MaxAttempts > 0 {
		warning("automatic rework disabled because automatic review is disabled")
	}
	return runtimeconfig.ResolveReworkOptions(reviewEnabled, flagBoolValue(fs, "reverify"), environment, repository, invocation)
}

type planRunRepository interface {
	run.Repository
}

func (a App) run(ctx context.Context, repo planRunRepository, args []string) error {
	return a.runWithRepositorySettings(ctx, repo, args, true)
}

func (a App) runWithRepositorySettings(ctx context.Context, repo planRunRepository, args []string, compose bool) error {
	fs, positional, err := a.parseArgs("run", args, a.registerRunFlags)
	if err != nil {
		return err
	}
	// Validate explicit model syntax even before resolving the selected plan.
	if _, err := modelFlagValue(fs); err != nil {
		return err
	}
	if flagWasProvided(fs, "rework-escalation-model") {
		if _, err := runtimeconfig.ParseModelName(flagStringValue(fs, "rework-escalation-model")); err != nil {
			return fmt.Errorf("--rework-escalation-model: %w", err)
		}
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
	if err := requirePositionals(positional, 1, "usage: tao run [--agent pi|claude] [--session-timeout DURATION] [--review-agent pi|claude] [--model NAME] [--rework-escalation-model NAME] [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--continue|--restart|--repair-verification|--reverify] [--no-review] [--no-run-header] [--auto-rework] [--max-rework-attempts N] [--rework-restart] [--dangerously-skip-permissions] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	input := positional[0]
	detail, err := repo.ResolvePlan(ctx, input)
	if err != nil {
		return err
	}
	if compose && detail != nil {
		a, err = a.settingsForPlanRoot(ctx, detail.State.Repo.Root)
		if err != nil {
			return err
		}
	}
	options, err := a.resolveCommandOptions(fs, runtimeconfig.CommandRun)
	if err != nil {
		return err
	}
	request := run.Request{Input: input, ResolvedRunOptions: options.RunOptions}
	request.RecoveryMode = run.RecoveryMode{
		RestartBlocked:     blockedRestart,
		RepairVerification: repairVerification,
		Reverify:           reverify,
	}
	return a.executeCommandRunRequest(ctx, repo, input, request, options, reworkRestart)
}

var executeSinglePlan = func(service run.Service, ctx context.Context, request run.Request) error {
	return service.Execute(ctx, request)
}

// executeResolvedRun is the single-plan execution boundary shared by run entry
// points after their inputs and runtime options have been fully resolved.
func (a App) executeResolvedRun(ctx context.Context, repo planRunRepository, input string, request run.Request, skipPermissions bool, policy runtimeconfig.AutoReworkPolicy, escalationFromAttempt int, reworkRestart, noRunHeader bool) error {
	return a.executeResolvedRunWithSources(ctx, repo, input, request, skipPermissions, policy, escalationFromAttempt, reworkRestart, noRunHeader, nil)
}

func (a App) executeResolvedRunWithSources(ctx context.Context, repo planRunRepository, input string, request run.Request, skipPermissions bool, policy runtimeconfig.AutoReworkPolicy, escalationFromAttempt int, reworkRestart, noRunHeader bool, sources map[string]string) error {
	if request.Reverify {
		policy = runtimeconfig.AutoReworkPolicy{}
	}
	snapshot := a.envSnapshot()
	thresholds := snapshot.Defaults().Budget.Warn()
	if policy.Enabled {
		// Budget() validates warn thresholds and stop caps together. An enabled
		// restart can reopen slices before Service.Execute reaches slice-budget
		// admission, so this single error path also rejects captured hard-cap
		// errors before that mutation; other paths retain their lazy
		// service-level validation.
		budget, err := snapshot.Budget()
		if err != nil {
			return err
		}
		thresholds = budget.Warn()
	}
	runCtx, stopSignals := newCommandSignalContext(ctx)
	defer stopSignals()

	runOut, headerReporter, closeHeader := a.installRunHeader(runCtx, a.Out, noRunHeader)
	defer closeHeader()
	service := run.NewService(repo, runOut, run.Options{
		ExecutionConfig: run.ExecutionConfig{
			RuntimeEnv:         &snapshot,
			ResolvedRunOptions: request.ResolvedRunOptions,
			OptionSources:      run.RunOptionSources{PullRequest: sources[runtimeconfig.EnvPullRequest], ExecutionMode: sources[runtimeconfig.EnvExecutionMode]},
			SkipPermissions:    skipPermissions,
			MaxReworkAttempts:  policy.MaxAttempts,
		},
		RunDependencies: run.RunDependencies{CommandRunner: a.CommandRunner, ProcessStarter: a.ProcessStarter, StatusReporter: a.StatusReporter, HeaderReporter: headerReporter, SessionLogWriter: runOut, Now: a.now},
	})

	return service.WithPlanRunLock(runCtx, request, func(ownedCtx context.Context) error {
		firstExecution := true
		driver := newReworkDriver(repo, a.now, reworkpkg.EscalationPolicy{
			Model: request.Models.ReworkEscalation, FromAttempt: escalationFromAttempt,
		})
		return driver.Run(ownedCtx, request.Input, reworkpkg.RunOptions{
			Enabled:          policy.Enabled,
			MaxAttempts:      policy.MaxAttempts,
			BudgetThresholds: thresholds,
			AllowRestart:     reworkRestart,
			BeforeDecision:   automaticReworkPhaseHook(policy.MaxAttempts, policy.Enabled),
			Execute: func(executeCtx context.Context) error {
				err := executeSinglePlan(service, executeCtx, request)
				if firstExecution {
					firstExecution = false
					request = request.ForNextRound()
					return decorateRunCannotStartError(executeCtx, repo, input, err)
				}
				return err
			},
			LogProgress: func(round int) error {
				return writef(runOut, "Plan reopened for rework round %d\n", round)
			},
			LogAdvisories: func(round int, advisories []reworkpkg.Advisory) error {
				return writef(runOut, "%s", reworkpkg.FormatAdvisories(round, advisories))
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
	var evidence *plan.ApprovalEvidenceError
	if errors.As(err, &evidence) {
		if slice := runSliceByID(detail, evidence.SliceID); slice != nil {
			return fmt.Errorf("%w\n\n%s", err, formatApprovalEvidenceGuidance(detail, slice, input, evidence.Requirement))
		}
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

func formatApprovalEvidenceGuidance(detail *plan.PlanDetail, slice *plan.Slice, input, requirement string) string {
	planRef := strings.TrimSpace(input)
	if planRef == "" {
		planRef = detail.State.Plan.ID
	}
	planRef = shellCommandArg(planRef)
	sliceRef := shellCommandArg(slice.ID)
	amend := "tao edit amend " + planRef + " " + sliceRef + " --reason-file " + shellCommandArg("<reason-file>")
	var b strings.Builder
	fmt.Fprintf(&b, "Unresolved requirement: %q\n", planview.FormatBlockerText(requirement).Detailed)
	b.WriteString("Approval is authorization-only; repeated approval cannot supply missing facts.\n")
	if slice.Status == plan.StatusInProgress {
		b.WriteString("This interrupted slice is in_progress and cannot be amended. After confirming no run is active, write a blocker reason describing the missing facts and explicitly block it first:\n  tao slice-blocked --plan-dir " + shellCommandArg(detail.Dir) + " --slice-id " + sliceRef + " --reason-file " + shellCommandArg("<blocker-reason-file>"))
		b.WriteString("\nBlocking preserves the recorded execution boundary and worktree changes; do not reset or commit interrupted automatic work. Amendment locking and ordinary recovery checks still apply.\n")
	}
	b.WriteString("Replace the placeholders below with your own files or text. The goal file must contain the complete replacement goal plus actual facts; the reason file only explains the change. Record the contract amendment:\n  ")
	b.WriteString(amend + " --goal-file " + shellCommandArg("<goal-file>"))
	b.WriteString("\nAlternatively, append a task containing the actual facts (do not invent observations):\n  ")
	b.WriteString(amend + " --add-task " + shellCommandArg("<task containing actual facts>"))
	b.WriteString("\nA recorded goal/tasks amendment lifts only this heuristic, not approval or other run gates. Declared inputs must still pass execution-worktree checks; neither amendments nor file existence prove factual completeness.")
	if slice.Approval != nil && slice.Approval.Required && !slice.Approval.Approved {
		b.WriteString("\nAuthorization is also outstanding; approve separately:\n  tao approve --slice " + sliceRef + " " + planRef)
	}
	b.WriteString("\nOnly after remediation, run:\n  tao run ")
	if slice.Status == plan.StatusInProgress || runNeedsContinueAfterUnblock(detail, slice) {
		b.WriteString("--continue ")
	}
	b.WriteString(planRef)
	return b.String()
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
