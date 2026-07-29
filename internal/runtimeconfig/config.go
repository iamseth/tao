// Package runtimeconfig owns typed run defaults and request normalization shared
// by the CLI and run service.
//
// The model is staged: RunOptionsPatch carries partial values in two roles,
// environment or service defaults and one request's overrides, while
// ResolvedRunOptions is the validated execution model after both are merged.
// Optional fields are pointers so an unset value is distinct from an explicit
// zero, false, or the default worktree strategy.
package runtimeconfig

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type Mode string

type CommitPolicy string

// ExecutionMode is the single user-facing run knob that drives both workspace
// placement and branch behavior. isolated reproduces the historical
// feature-branch + worktree default; current keeps the launch checkout on its
// launch branch. The run and workspace layers read it directly and derive the
// physical worktree/current placement from it.
type ExecutionMode string

type AgentKind string

// SliceBudgetCaps contains optional hard limits for cumulative slice telemetry.
// Nil fields are disabled so enforcement remains opt-in.
type SliceBudgetCaps struct {
	OutputTokens *int64
	Cost         *float64
}

// RunOptionsPatch models partial values supplied as environment or service
// defaults and as one run request's overrides. Empty enum fields mean unset.
// Pointer fields mean the caller supplied the value, including explicit false
// or zero.
type RunOptionsPatch struct {
	Mode           Mode           `json:"mode,omitempty"`
	MaxSlices      *int           `json:"max_slices,omitempty"`
	Continue       *bool          `json:"continue,omitempty"`
	CommitPolicy   CommitPolicy   `json:"commit_policy,omitempty"`
	ExecutionMode  ExecutionMode  `json:"execution_mode,omitempty"`
	Agent          AgentKind      `json:"agent,omitempty"`
	PullRequest    *bool          `json:"pull_request,omitempty"`
	ReviewEnabled  *bool          `json:"review_enabled,omitempty"`
	SessionTimeout *time.Duration `json:"session_timeout,omitempty"`
}

// ResolvedRunOptions is the validated execution model after defaults and
// overrides have been merged. ExecutionMode is the single knob the run and
// workspace layers read; they derive physical worktree/current placement from it.
type ResolvedRunOptions struct {
	Mode           Mode
	MaxSlices      int
	Continue       bool
	CommitPolicy   CommitPolicy
	ExecutionMode  ExecutionMode
	Agent          AgentKind
	PullRequest    bool
	ReviewEnabled  bool
	SessionTimeout time.Duration
}

const (
	// DefaultMaxReworkAttempts is the number of automatic rework cycles allowed
	// after the initial queue run.
	DefaultMaxReworkAttempts = 5
	// DefaultAggregateReviewConvergenceWindow is the number of consecutive
	// changes-requested rounds used to detect aggregate review non-convergence.
	DefaultAggregateReviewConvergenceWindow = 2

	ModeRun  Mode = "run"
	ModeStep Mode = "step"

	CommitPolicyPlan  CommitPolicy = "plan"
	CommitPolicySlice CommitPolicy = "slice"
	CommitPolicyNone  CommitPolicy = "none"

	ExecutionModeIsolated ExecutionMode = "isolated"
	ExecutionModeCurrent  ExecutionMode = "current"

	AgentPi       AgentKind = "pi"
	AgentClaude   AgentKind = "claude"
	AgentOpenCode AgentKind = "opencode"
	AgentCodex    AgentKind = "codex"

	DefaultSessionTimeout = 20 * time.Minute
)

// AgentKinds is the canonical ordered roster of supported agent runtimes.
var AgentKinds = []AgentKind{AgentPi, AgentClaude, AgentOpenCode, AgentCodex}

// SupportedAgentKindsText renders AgentKinds for user-facing "want ..." messages.
func SupportedAgentKindsText() string {
	names := make([]string, len(AgentKinds))
	for i, kind := range AgentKinds {
		names[i] = kind.String()
	}

	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " or " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	}
}

func ParseMode(value string) (Mode, error) {
	if value == "" {
		return ModeRun, nil
	}
	switch Mode(value) {
	case ModeRun, ModeStep:
		return Mode(value), nil
	default:
		return "", fmt.Errorf("unsupported run mode %q", value)
	}
}

func (m Mode) String() string {
	return string(m)
}

func (m Mode) MaxSlices() int {
	if m == ModeStep {
		return 1
	}
	return 0
}

func ParseCommitPolicy(value string) (CommitPolicy, error) {
	if value == "" {
		return CommitPolicySlice, nil
	}
	switch CommitPolicy(value) {
	case CommitPolicySlice, CommitPolicyNone:
		return CommitPolicy(value), nil
	case CommitPolicyPlan:
		return "", fmt.Errorf("commit policy plan was removed; use slice or none")
	default:
		return "", fmt.Errorf("unsupported commit policy %q (want slice or none)", value)
	}
}

func (p CommitPolicy) String() string {
	return string(p)
}

func ParseExecutionMode(value string) (ExecutionMode, error) {
	if value == "" {
		return ExecutionModeIsolated, nil
	}
	switch ExecutionMode(value) {
	case ExecutionModeIsolated, ExecutionModeCurrent:
		return ExecutionMode(value), nil
	default:
		return "", fmt.Errorf("unsupported execution mode %q (want isolated or current)", value)
	}
}

func (m ExecutionMode) String() string {
	return string(m)
}

func ParseAgentKind(value string) (AgentKind, error) {
	if value == "" {
		return AgentPi, nil
	}
	kind := AgentKind(value)
	if slices.Contains(AgentKinds, kind) {
		return kind, nil
	}
	return "", fmt.Errorf("unsupported agent %q (want %s)", value, SupportedAgentKindsText())
}

func (a AgentKind) String() string {
	return string(a)
}

// DefaultRunOptionsPatch returns the built-in defaults for the default layer. The
// execution mode defaults to isolated, reproducing the historical feature-branch
// worktree behavior.
func DefaultRunOptionsPatch() RunOptionsPatch {
	sessionTimeout := DefaultSessionTimeout
	return RunOptionsPatch{Mode: ModeRun, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, Agent: AgentPi, SessionTimeout: &sessionTimeout}
}

func (d RunOptionsPatch) ExecutionModeValue() ExecutionMode {
	if d.ExecutionMode != "" {
		return d.ExecutionMode
	}
	return ExecutionModeIsolated
}

func (d RunOptionsPatch) PullRequestValue() bool {
	return d.PullRequest != nil && *d.PullRequest
}

func (d RunOptionsPatch) ReviewEnabledValue() bool {
	if d.ReviewEnabled != nil {
		return *d.ReviewEnabled
	}
	return true
}

func (d RunOptionsPatch) SessionTimeoutValue() time.Duration {
	if d.SessionTimeout != nil {
		return *d.SessionTimeout
	}
	return DefaultSessionTimeout
}

func (p RunOptionsPatch) WithMaxSlices(maxSlices int) RunOptionsPatch {
	p.MaxSlices = &maxSlices
	return p
}

func (p RunOptionsPatch) WithContinue(continueRun bool) RunOptionsPatch {
	p.Continue = &continueRun
	return p
}

func (p RunOptionsPatch) WithPullRequest(pullRequest bool) RunOptionsPatch {
	p.PullRequest = &pullRequest
	return p
}

func (p RunOptionsPatch) WithReviewEnabled(reviewEnabled bool) RunOptionsPatch {
	p.ReviewEnabled = &reviewEnabled
	return p
}

func (p RunOptionsPatch) WithSessionTimeout(sessionTimeout time.Duration) RunOptionsPatch {
	p.SessionTimeout = &sessionTimeout
	return p
}

// ParseAutoReworkEnv applies raw environment values to caller-supplied
// defaults. Empty values leave the defaults unchanged; non-empty values are
// trimmed before parsing.
func ParseAutoReworkEnv(defaultEnabled bool, defaultMaxAttempts int, enabledValue, maxAttemptsValue string) (bool, int, error) {
	enabled := defaultEnabled
	maxAttempts := defaultMaxAttempts
	if enabledValue != "" {
		parsed, err := parseAutoReworkEnabled(enabledValue)
		if err != nil {
			return false, 0, fmt.Errorf("%s: %w", EnvAutoRework, err)
		}
		enabled = parsed
	}
	if maxAttemptsValue != "" {
		parsed, err := parseMaxReworkAttempts(maxAttemptsValue)
		if err != nil {
			return false, 0, fmt.Errorf("%s: %w", EnvMaxReworkAttempts, err)
		}
		maxAttempts = parsed
	}
	return enabled, maxAttempts, nil
}

func parseAutoReworkEnabled(value string) (bool, error) {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("must be a boolean (true/false, 1/0, or t/f)")
	}
	return parsed, nil
}

func parseMaxReworkAttempts(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return parsed, nil
}

func parseBudgetInteger(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return parsed, nil
}

func parseBudgetCost(value string) (float64, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("must be a non-negative decimal number")
	}
	return parsed, nil
}

func defaultAgentBudgetThresholds() plan.AgentBudgetThresholds {
	return plan.DefaultAgentBudgetThresholds()
}

// AutoReworkPolicy is the validated policy used by automatic rework loops.
// Enabled is normalized to false when MaxAttempts is zero.
type AutoReworkPolicy struct {
	Enabled     bool `json:"enabled"`
	MaxAttempts int  `json:"max_attempts"`
}

// ResolveAutoReworkPolicy validates and normalizes automatic rework settings.
func ResolveAutoReworkPolicy(enabled bool, maxAttempts int, reviewEnabled bool) (AutoReworkPolicy, error) {
	policy := AutoReworkPolicy{Enabled: enabled && maxAttempts > 0, MaxAttempts: maxAttempts}
	if err := ValidateAutoReworkPolicy(policy, reviewEnabled); err != nil {
		return AutoReworkPolicy{}, err
	}
	return policy, nil
}

// ValidateAutoReworkPolicy checks a resolved policy against the review option
// of the request that the policy will govern.
func ValidateAutoReworkPolicy(policy AutoReworkPolicy, reviewEnabled bool) error {
	if policy.MaxAttempts < 0 {
		return fmt.Errorf("--max-rework-attempts must be 0 or greater")
	}
	if policy.Enabled && policy.MaxAttempts > 0 && !reviewEnabled {
		return fmt.Errorf("--auto-rework requires automatic review")
	}
	return nil
}

type Config struct {
	resolved ResolvedRunOptions
}

// NewConfigFromStages merges the explicit default and override stages into a
// validated configuration.
func NewConfigFromStages(defaults RunOptionsPatch, overrides RunOptionsPatch) (Config, error) {
	resolved, err := ResolveRunOptions(defaults, overrides)
	if err != nil {
		return Config{}, err
	}
	return Config{resolved: resolved}, nil
}

// ResolvedOptions returns the validated execution model.
func (c Config) ResolvedOptions() ResolvedRunOptions {
	return c.resolved
}

// RunOptionsPatch projects resolved options to a patch so they can be re-applied
// in either role. Every concrete scalar field becomes explicit.
func (o ResolvedRunOptions) RunOptionsPatch() RunOptionsPatch {
	maxSlices := o.MaxSlices
	continueRun := o.Continue
	pullRequest := o.PullRequest
	reviewEnabled := o.ReviewEnabled
	sessionTimeout := o.SessionTimeout
	return RunOptionsPatch{
		Mode:           o.Mode,
		MaxSlices:      &maxSlices,
		Continue:       &continueRun,
		CommitPolicy:   o.CommitPolicy,
		ExecutionMode:  o.ExecutionMode,
		Agent:          o.Agent,
		PullRequest:    &pullRequest,
		ReviewEnabled:  &reviewEnabled,
		SessionTimeout: &sessionTimeout,
	}
}

// ResolveRunOptions is the staged model's normalization entry point. It applies
// the same patch merge first for defaults and then for request overrides before
// validating cross-field constraints.
func ResolveRunOptions(defaults RunOptionsPatch, overrides RunOptionsPatch) (ResolvedRunOptions, error) {
	options, err := mergeRunOptions(ResolvedRunOptions{}, defaults)
	if err != nil {
		return ResolvedRunOptions{}, err
	}
	options, err = mergeRunOptions(options, overrides)
	if err != nil {
		return ResolvedRunOptions{}, err
	}
	if err := validateResolvedRunOptions(options); err != nil {
		return ResolvedRunOptions{}, err
	}
	return options, nil
}

func mergeRunOptions(options ResolvedRunOptions, patch RunOptionsPatch) (ResolvedRunOptions, error) {
	if options.Mode == "" {
		options = ResolvedRunOptions{
			Mode:           ModeRun,
			CommitPolicy:   CommitPolicySlice,
			ExecutionMode:  ExecutionModeIsolated,
			Agent:          AgentPi,
			ReviewEnabled:  true,
			SessionTimeout: DefaultSessionTimeout,
		}
	}
	if patch.Mode != "" {
		mode, err := ParseMode(patch.Mode.String())
		if err != nil {
			return ResolvedRunOptions{}, err
		}
		options.Mode = mode
		options.MaxSlices = mode.MaxSlices()
	}
	if patch.MaxSlices != nil {
		options.MaxSlices = *patch.MaxSlices
	}
	if patch.Continue != nil {
		options.Continue = *patch.Continue
	}
	if patch.CommitPolicy != "" {
		commitPolicy, err := ParseCommitPolicy(patch.CommitPolicy.String())
		if err != nil {
			return ResolvedRunOptions{}, err
		}
		options.CommitPolicy = commitPolicy
	}
	if patch.ExecutionMode != "" {
		executionMode, err := ParseExecutionMode(patch.ExecutionMode.String())
		if err != nil {
			return ResolvedRunOptions{}, err
		}
		options.ExecutionMode = executionMode
	}
	if patch.Agent != "" {
		agent, err := ParseAgentKind(patch.Agent.String())
		if err != nil {
			return ResolvedRunOptions{}, err
		}
		options.Agent = agent
	}
	if patch.PullRequest != nil {
		options.PullRequest = *patch.PullRequest
	}
	if patch.ReviewEnabled != nil {
		options.ReviewEnabled = *patch.ReviewEnabled
	}
	if patch.SessionTimeout != nil {
		options.SessionTimeout = *patch.SessionTimeout
	}
	return options, nil
}

func validateResolvedRunOptions(options ResolvedRunOptions) error {
	if options.MaxSlices < 0 {
		return fmt.Errorf("--max-slices must be 0 or greater")
	}
	if options.PullRequest && options.Mode != ModeRun {
		return fmt.Errorf("--pull-request requires full run mode")
	}
	if options.PullRequest && options.CommitPolicy == CommitPolicyNone {
		return fmt.Errorf("--pull-request requires commit policy slice")
	}
	if options.SessionTimeout < 0 {
		return fmt.Errorf("session timeout must be 0 or greater")
	}
	return nil
}
