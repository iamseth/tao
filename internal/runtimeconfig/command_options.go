package runtimeconfig

import (
	"fmt"
	"slices"
)

// CommandProfile admits only settings consumed by a command's execution path.
// It is not lifecycle or recovery authority.
type CommandProfile string

const (
	CommandRun         CommandProfile = "run"
	CommandReview      CommandProfile = "review"
	CommandMerge       CommandProfile = "merge"
	CommandPromptRun   CommandProfile = "prompt-run"
	CommandPromptOther CommandProfile = "prompt-other"
)

// CommandAuxiliaryFlags preserves providedness independently of flag defaults.
// NoRunHeader is the inverse of TAO_RUN_HEADER.
type CommandAuxiliaryFlags struct {
	AutoRework                  *bool
	MaxReworkAttempts           *int
	NoRunHeader                 *bool
	SkipPermissions             *bool
	ReworkEscalationFromAttempt *int
}

// CommandOptionsInput uses one composed snapshot: global and repository
// settings already sit beneath the captured environment (see ResolveSettings).
// Callers must supply only explicitly provided, registered flags. Empty
// model/enum fields remain unset.
type CommandOptionsInput struct {
	Env       EnvSnapshot
	Flags     RunOptionsPatch
	Profile   CommandProfile
	Auxiliary CommandAuxiliaryFlags
}

// CommandOptions contains applicable configuration, never execution authority.
// Sources is keyed by environment name (or --max-slices/--continue) and records
// the winning layer: "default", "global", "repository", "env", or "flag".
// Unset model roles report their base model's winning source. Inapplicable
// options retain inert built-ins; their keys are absent from Sources. Models
// retain their normal lazy fallback.
type CommandOptions struct {
	RunOptions                  ResolvedRunOptions
	AutoRework                  AutoReworkPolicy
	NoRunHeader                 bool
	SkipPermissions             bool
	ReworkEscalationFromAttempt int
	Sources                     map[string]string
	Warnings                    []string
}

type commandOptionRule struct {
	key      string
	profiles []CommandProfile
	project  func(RunOptionsPatch) RunOptionsPatch
}

// The same rules drive admission, patch filtering, and presentation. Auxiliary
// settings have no patch projection; they resolve from the snapshot and flags.
var commandOptionRules = []commandOptionRule{
	{"--max-slices", []CommandProfile{CommandRun}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{MaxSlices: p.MaxSlices} }},
	{"--continue", []CommandProfile{CommandRun}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{Continue: p.Continue} }},
	{EnvCommitPolicy, []CommandProfile{CommandRun, CommandPromptRun}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{CommitPolicy: p.CommitPolicy} }},
	{EnvExecutionMode, []CommandProfile{CommandRun, CommandPromptRun}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{ExecutionMode: p.ExecutionMode} }},
	{EnvAgent, []CommandProfile{CommandRun, CommandReview, CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{Agent: p.Agent} }},
	{EnvReviewAgent, []CommandProfile{CommandRun, CommandReview}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{ReviewAgent: p.ReviewAgent} }},
	{EnvPullRequest, []CommandProfile{CommandRun}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{PullRequest: p.PullRequest} }},
	{EnvReview, []CommandProfile{CommandRun}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{ReviewEnabled: p.ReviewEnabled} }},
	{EnvSessionTimeout, []CommandProfile{CommandRun, CommandReview, CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch { return RunOptionsPatch{SessionTimeout: p.SessionTimeout} }},
	{EnvModel, []CommandProfile{CommandRun, CommandReview, CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{Base: p.Base}}
	}},
	{EnvRunModel, []CommandProfile{CommandRun}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{Run: p.Run}}
	}},
	{EnvReviewModel, []CommandProfile{CommandRun, CommandReview}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{Review: p.Review}}
	}},
	{EnvMergeReviewModel, []CommandProfile{CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{MergeReview: p.MergeReview}}
	}},
	{EnvResolverModel, []CommandProfile{CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{Resolver: p.Resolver}}
	}},
	{EnvReworkEscalationModel, []CommandProfile{CommandRun}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{ReworkEscalation: p.ReworkEscalation}}
	}},
	{EnvEffort, []CommandProfile{CommandRun, CommandReview, CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{Effort: p.Effort}}
	}},
	{EnvRunEffort, []CommandProfile{CommandRun}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{RunEffort: p.RunEffort}}
	}},
	{EnvReviewEffort, []CommandProfile{CommandRun, CommandReview}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{ReviewEffort: p.ReviewEffort}}
	}},
	{EnvMergeReviewEffort, []CommandProfile{CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{MergeReviewEffort: p.MergeReviewEffort}}
	}},
	{EnvResolverEffort, []CommandProfile{CommandMerge}, func(p RunOptionsPatch) RunOptionsPatch {
		return RunOptionsPatch{ModelSelection: ModelSelection{ResolverEffort: p.ResolverEffort}}
	}},
	{EnvAutoRework, []CommandProfile{CommandRun}, nil},
	{EnvMaxReworkAttempts, []CommandProfile{CommandRun}, nil},
	{EnvRunHeader, []CommandProfile{CommandRun}, nil},
	{EnvReworkEscalationFromAttempt, []CommandProfile{CommandRun}, nil},
	{EnvSkipPermissions, []CommandProfile{CommandRun, CommandReview, CommandMerge}, nil},
}

// ApplicableCommands presents only the composition policy owned here. Other
// settings (notably budgets and planning routing) retain independent consumers.
// Unknown keys return no commands; that does not disable those consumers.
func ApplicableCommands(key string) []string {
	for _, rule := range commandOptionRules {
		if rule.key != key {
			continue
		}
		var commands []string
		for _, profile := range rule.profiles {
			switch profile {
			case CommandRun:
				commands = append(commands, "run", "note run", "rework --run")
			case CommandReview:
				commands = append(commands, "review --run")
			case CommandMerge:
				commands = append(commands, "merge")
			case CommandPromptRun:
				commands = append(commands, "prompt run")
			}
		}
		return commands
	}
	return nil
}

// ResolveCommandOptions composes the snapshot's effective values (built-ins,
// global and repository settings, then the captured environment) with explicit
// invocation options, without consulting process state. Applicable malformed
// settings are rejected even when a flag overrides them, preserving
// EnvSnapshot's admission contract. PR/current-workspace checks are
// deliberately left to state-dependent execution validation.
func ResolveCommandOptions(input CommandOptionsInput) (CommandOptions, error) {
	switch input.Profile {
	case CommandRun, CommandReview, CommandMerge, CommandPromptRun, CommandPromptOther:
	default:
		return CommandOptions{}, fmt.Errorf("unknown command profile %q", input.Profile)
	}
	defaults := input.Env.Defaults()
	out := CommandOptions{Sources: make(map[string]string)}
	var err error
	out.RunOptions, err = mergeRunOptions(ResolvedRunOptions{}, RunOptionsPatch{})
	if err != nil {
		return CommandOptions{}, err
	}
	origins := snapshotOrigins(input.Env)
	for _, rule := range commandOptionRules {
		if !slices.Contains(rule.profiles, input.Profile) {
			continue
		}
		if err := input.Env.Require(admissionKey(rule.key)); err != nil {
			return CommandOptions{}, fmt.Errorf("environment: %w", err)
		}
		out.Sources[rule.key] = "default"
		if origin := origins[rule.key]; origin != "" {
			out.Sources[rule.key] = origin
		}
		if rule.project == nil {
			continue
		}
		for _, stage := range []struct {
			patch  RunOptionsPatch
			source string
		}{
			{defaults.RunOptionsPatch, out.Sources[rule.key]},
			{input.Flags, "flag"},
		} {
			patch := rule.project(stage.patch)
			if patch == (RunOptionsPatch{}) {
				continue
			}
			out.Sources[rule.key] = stage.source
			out.RunOptions, err = mergeRunOptions(out.RunOptions, patch)
			if err != nil {
				return CommandOptions{}, out.optionError(err, rule.key)
			}
		}
	}
	if out.RunOptions.MaxSlices < 0 {
		return CommandOptions{}, out.optionError(fmt.Errorf("--max-slices must be 0 or greater"), "--max-slices")
	}
	if out.RunOptions.SessionTimeout < 0 {
		return CommandOptions{}, out.optionError(fmt.Errorf("session timeout must be 0 or greater"), EnvSessionTimeout)
	}
	if out.RunOptions.PullRequest && out.RunOptions.CommitPolicy == CommitPolicyNone {
		return CommandOptions{}, out.optionError(fmt.Errorf("--pull-request requires commit policy slice"), EnvPullRequest, EnvCommitPolicy)
	}
	for _, role := range []struct{ key, value, base string }{
		{EnvRunModel, out.RunOptions.Models.Run, EnvModel}, {EnvReviewModel, out.RunOptions.Models.Review, EnvModel},
		{EnvMergeReviewModel, out.RunOptions.Models.MergeReview, EnvModel}, {EnvResolverModel, out.RunOptions.Models.Resolver, EnvModel},
		{EnvRunEffort, out.RunOptions.Models.RunEffort, EnvEffort}, {EnvReviewEffort, out.RunOptions.Models.ReviewEffort, EnvEffort},
		{EnvMergeReviewEffort, out.RunOptions.Models.MergeReviewEffort, EnvEffort}, {EnvResolverEffort, out.RunOptions.Models.ResolverEffort, EnvEffort},
	} {
		if _, applicable := out.Sources[role.key]; applicable && role.value == "" {
			out.Sources[role.key] = out.Sources[role.base]
		}
	}
	if _, applicable := out.Sources[EnvSkipPermissions]; applicable {
		out.SkipPermissions = commandAuxiliaryValue(&out, EnvSkipPermissions, defaults.SkipPermissions, input.Auxiliary.SkipPermissions)
	}
	if input.Profile == CommandRun {
		if err := out.resolveRunAuxiliary(input, defaults); err != nil {
			return CommandOptions{}, err
		}
	}
	return out, nil
}

// admissionKey maps a rule to the snapshot key that records its failures.
// max_slices has no environment alias, so its saved-value diagnostics live
// under the canonical setting key rather than the flag name.
func admissionKey(ruleKey string) string {
	if ruleKey == "--max-slices" {
		return "max_slices"
	}
	return ruleKey
}

// snapshotOrigins reports each option's winning layer within the composed
// snapshot. Environment rows carry their own provenance; max_slices has no
// environment alias and is read from the persisted-settings projection.
func snapshotOrigins(env EnvSnapshot) map[string]string {
	origins := make(map[string]string)
	for _, row := range env.Status() {
		origins[row.Name] = row.Source
	}
	for _, setting := range env.SettingsStatus() {
		if setting.Key == "max_slices" && setting.Source != "default" {
			origins["--max-slices"] = setting.Source
		}
	}
	return origins
}

func commandAuxiliaryValue[T any](out *CommandOptions, key string, value T, flag *T) T {
	if flag != nil {
		out.Sources[key] = "flag"
		return *flag
	}
	return value
}

func (out *CommandOptions) resolveRunAuxiliary(input CommandOptionsInput, defaults EnvDefaults) error {
	aux := input.Auxiliary
	out.NoRunHeader = commandAuxiliaryValue(out, EnvRunHeader, !defaults.RunHeader, aux.NoRunHeader)
	flags := ReworkOptionsPatch{MaxAttempts: aux.MaxReworkAttempts, AutoRework: aux.AutoRework, EscalationFromAttempt: aux.ReworkEscalationFromAttempt}
	if flags.MaxAttempts != nil || flags.AutoRework != nil {
		out.Sources[EnvMaxReworkAttempts] = "flag"
	}
	if flags.EscalationFromAttempt != nil {
		out.Sources[EnvReworkEscalationFromAttempt] = "flag"
	}
	for _, row := range input.Env.Status() {
		if row.Name == EnvAutoRework {
			out.Warnings = append(out.Warnings, "TAO_AUTO_REWORK is deprecated; use TAO_MAX_REWORK_ATTEMPTS")
		}
	}
	if aux.AutoRework != nil {
		out.Warnings = append(out.Warnings, "--auto-rework is deprecated; use --max-rework-attempts")
	}
	resolved, err := ResolveReworkOptions(true, false,
		ReworkOptionsPatch{MaxAttempts: defaults.MaxReworkAttempts, EscalationFromAttempt: defaults.ReworkEscalationFromAttempt},
		flags)
	if err != nil {
		return out.optionError(err, EnvMaxReworkAttempts, EnvReworkEscalationFromAttempt)
	}
	if !out.RunOptions.ReviewEnabled && resolved.MaxAttempts > 0 {
		out.Warnings = append(out.Warnings, "automatic rework disabled because automatic review is disabled")
		resolved.MaxAttempts = 0
	}
	out.ReworkEscalationFromAttempt = resolved.EscalationFromAttempt
	out.AutoRework = AutoReworkPolicy{Enabled: resolved.MaxAttempts > 0, MaxAttempts: resolved.MaxAttempts}
	return nil
}

func (out CommandOptions) optionError(err error, keys ...string) error {
	for _, key := range keys {
		err = fmt.Errorf("%w; %s source=%s", err, key, out.Sources[key])
	}
	return err
}
