// Package runtimeconfig owns typed run defaults and request normalization shared
// by the CLI and run service.
//
// The model is staged: RunOptionsPatch carries partial values as environment or
// service defaults, repository defaults, and one request's overrides, while
// ResolvedRunOptions is the validated execution model after the applicable
// stages are merged. Optional scalar fields are pointers so an unset value is
// distinct from an explicit zero or false; empty enum and model fields are unset.
package runtimeconfig

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/configtypes"
)

type CommitPolicy string

// ExecutionMode is the single user-facing run knob that drives both workspace
// placement and branch behavior. isolated reproduces the historical
// feature-branch + worktree default; current keeps the launch checkout on its
// launch branch. The run and workspace layers read it directly and derive the
// physical worktree/current placement from it.
type ExecutionMode = configtypes.ExecutionMode

type AgentKind string

type ModelRole = configtypes.ModelRole

// Model roles select operation-specific overrides during runtime model resolution.
const (
	ModelRoleDefault     = configtypes.ModelRoleDefault
	ModelRoleRun         = configtypes.ModelRoleRun
	ModelRoleReview      = configtypes.ModelRoleReview
	ModelRoleMergeReview = configtypes.ModelRoleMergeReview
	ModelRoleResolver    = configtypes.ModelRoleResolver
)

// ModelSelection keeps role overrides separate from their shared fallback.
// Empty names leave model selection to the agent runtime.
type ModelSelection = configtypes.ModelSelection

// ParseModelName treats model names as opaque runtime-specific identifiers.
func ParseModelName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", fmt.Errorf("model name must not be empty")
	}
	if strings.ContainsFunc(name, unicode.IsSpace) {
		return "", fmt.Errorf("model name must not contain whitespace")
	}
	return name, nil
}

// RunOptionsPatch models partial values supplied as environment or service
// defaults, repository defaults, or one run request's overrides. Empty enum and
// model fields mean unset. Pointer fields mean the caller supplied the value,
// including explicit false or zero.
type RunOptionsPatch struct {
	MaxSlices      *int           `json:"max_slices,omitempty"`
	Continue       *bool          `json:"continue,omitempty"`
	CommitPolicy   CommitPolicy   `json:"commit_policy,omitempty"`
	ExecutionMode  ExecutionMode  `json:"execution_mode,omitempty"`
	Agent          AgentKind      `json:"agent,omitempty"`
	PullRequest    *bool          `json:"pull_request,omitempty"`
	ReviewEnabled  *bool          `json:"review_enabled,omitempty"`
	SessionTimeout *time.Duration `json:"session_timeout,omitempty"`
	ModelSelection
}

// ResolvedRunOptions is the validated execution model after defaults and
// overrides have been merged. ExecutionMode is the single knob the run and
// workspace layers read; they derive physical worktree/current placement from it.
type ResolvedRunOptions struct {
	MaxSlices      int
	Continue       bool
	CommitPolicy   CommitPolicy
	ExecutionMode  ExecutionMode
	Agent          AgentKind
	PullRequest    bool
	ReviewEnabled  bool
	SessionTimeout time.Duration
	Models         ModelSelection
}

// Run option values and built-in limits supply normalization defaults and accepted
// selectors; the legacy plan commit policy is retained for rejection diagnostics.
const (
	// DefaultMaxReworkAttempts is the number of automatic rework cycles allowed
	// after the initial direct run.
	DefaultMaxReworkAttempts = 5
	// DefaultReworkEscalationFromAttempt is the first attempt eligible for a
	// configured escalation model within an automatic-rework window.
	DefaultReworkEscalationFromAttempt = 4
	// DefaultAggregateReviewConvergenceWindow is the number of consecutive
	// changes-requested rounds used to detect aggregate review non-convergence.
	DefaultAggregateReviewConvergenceWindow = 2

	CommitPolicyPlan  CommitPolicy = "plan"
	CommitPolicySlice CommitPolicy = "slice"
	CommitPolicyNone  CommitPolicy = "none"

	ExecutionModeIsolated = configtypes.ExecutionModeIsolated
	ExecutionModeCurrent  = configtypes.ExecutionModeCurrent

	AgentPi     AgentKind = "pi"
	AgentClaude AgentKind = "claude"

	DefaultSessionTimeout = 20 * time.Minute
)

// AgentKinds is the canonical ordered roster of supported agent runtimes.
var AgentKinds = []AgentKind{AgentPi, AgentClaude}

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
	return RunOptionsPatch{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, Agent: AgentPi, SessionTimeout: &sessionTimeout}
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

// WithModelForAllRoles overrides both the base and any inherited role choices.
func (p RunOptionsPatch) WithModelForAllRoles(name string) RunOptionsPatch {
	p.Base = name
	p.Run = name
	p.Review = name
	p.MergeReview = name
	p.Resolver = name
	return p
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
// in either role. Scalar fields become explicit; empty model fields stay unset.
func (o ResolvedRunOptions) RunOptionsPatch() RunOptionsPatch {
	maxSlices := o.MaxSlices
	continueRun := o.Continue
	pullRequest := o.PullRequest
	reviewEnabled := o.ReviewEnabled
	sessionTimeout := o.SessionTimeout
	return RunOptionsPatch{
		MaxSlices:      &maxSlices,
		Continue:       &continueRun,
		CommitPolicy:   o.CommitPolicy,
		ExecutionMode:  o.ExecutionMode,
		Agent:          o.Agent,
		PullRequest:    &pullRequest,
		ReviewEnabled:  &reviewEnabled,
		SessionTimeout: &sessionTimeout,
		ModelSelection: o.Models,
	}
}

// ResolveRunOptions is the staged model's two-stage normalization entry point.
// It applies defaults and then request overrides before validating cross-field
// constraints.
func ResolveRunOptions(defaults RunOptionsPatch, overrides RunOptionsPatch) (ResolvedRunOptions, error) {
	return resolveRunOptionsStages(defaults, overrides)
}

// ResolveRunOptionsWithRepositoryDefaults applies environment or service
// defaults, repository defaults, and request overrides in increasing precedence
// order before validating cross-field constraints.
func ResolveRunOptionsWithRepositoryDefaults(defaults, repository, overrides RunOptionsPatch) (ResolvedRunOptions, error) {
	return resolveRunOptionsStages(defaults, repository, overrides)
}

func resolveRunOptionsStages(stages ...RunOptionsPatch) (ResolvedRunOptions, error) {
	var options ResolvedRunOptions
	for _, stage := range stages {
		var err error
		options, err = mergeRunOptions(options, stage)
		if err != nil {
			return ResolvedRunOptions{}, err
		}
	}
	if err := validateResolvedRunOptions(options); err != nil {
		return ResolvedRunOptions{}, err
	}
	return options, nil
}

func mergeRunOptions(options ResolvedRunOptions, patch RunOptionsPatch) (ResolvedRunOptions, error) {
	if options.CommitPolicy == "" {
		options = ResolvedRunOptions{
			CommitPolicy:   CommitPolicySlice,
			ExecutionMode:  ExecutionModeIsolated,
			Agent:          AgentPi,
			ReviewEnabled:  true,
			SessionTimeout: DefaultSessionTimeout,
		}
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
	for _, model := range []struct {
		field  string
		value  string
		target *string
	}{
		{"model", patch.Base, &options.Models.Base},
		{"run_model", patch.Run, &options.Models.Run},
		{"review_model", patch.Review, &options.Models.Review},
		{"merge_review_model", patch.MergeReview, &options.Models.MergeReview},
		{"resolver_model", patch.Resolver, &options.Models.Resolver},
		{"rework_escalation_model", patch.ReworkEscalation, &options.Models.ReworkEscalation},
	} {
		if model.value == "" {
			continue
		}
		parsed, err := ParseModelName(model.value)
		if err != nil {
			return ResolvedRunOptions{}, fmt.Errorf("%s: %w", model.field, err)
		}
		*model.target = parsed
	}
	return options, nil
}

func validateResolvedRunOptions(options ResolvedRunOptions) error {
	if options.MaxSlices < 0 {
		return fmt.Errorf("--max-slices must be 0 or greater")
	}
	if options.PullRequest && options.CommitPolicy == CommitPolicyNone {
		return fmt.Errorf("--pull-request requires commit policy slice")
	}
	if options.SessionTimeout < 0 {
		return fmt.Errorf("session timeout must be 0 or greater")
	}
	return nil
}
