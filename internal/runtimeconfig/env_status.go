package runtimeconfig

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/selfupdate"
	"github.com/iamseth/tao/internal/theme"
)

const DefaultSessionWarnPercent = 80

// Environment keys name runtime settings and compatibility aliases read by the settings table.
const (
	EnvSessionWarnPercent               = "TAO_SESSION_WARN_PERCENT"
	EnvCommitPolicy                     = "TAO_COMMIT_POLICY"
	EnvExecutionMode                    = "TAO_EXECUTION_MODE"
	EnvAgent                            = "TAO_AGENT"
	EnvPullRequest                      = "TAO_PULL_REQUEST"
	EnvReview                           = "TAO_REVIEW"
	EnvAutoRework                       = "TAO_AUTO_REWORK"
	EnvMaxReworkAttempts                = "TAO_MAX_REWORK_ATTEMPTS"
	EnvReworkEscalationFromAttempt      = "TAO_REWORK_ESCALATION_FROM_ATTEMPT"
	EnvSessionTimeout                   = "TAO_SESSION_TIMEOUT"
	EnvModel                            = "TAO_MODEL"
	EnvRunModel                         = "TAO_RUN_MODEL"
	EnvReviewModel                      = "TAO_REVIEW_MODEL"
	EnvMergeReviewModel                 = "TAO_MERGE_REVIEW_MODEL"
	EnvResolverModel                    = "TAO_RESOLVER_MODEL"
	EnvReworkEscalationModel            = "TAO_REWORK_ESCALATION_MODEL"
	EnvUpdate                           = "TAO_UPDATE"
	EnvSkipPermissions                  = "TAO_DANGEROUSLY_SKIP_PERMISSIONS"
	EnvMergeVerifyCommand               = "TAO_MERGE_VERIFY_COMMAND"
	EnvAggregateReviewConvergenceWindow = "TAO_AGGREGATE_REVIEW_CONVERGENCE_WINDOW"
	EnvApprovedBy                       = "TAO_APPROVED_BY"
	EnvRunHeader                        = "TAO_RUN_HEADER"
	EnvTheme                            = "TAO_THEME"

	EnvPlannerRouting      = "TAO_PLANNER_ROUTING"
	EnvPlannerRoutingArms  = "TAO_PLANNER_ROUTING_ARMS"
	EnvPlannerRoutingFloor = "TAO_PLANNER_ROUTING_FLOOR"

	// Agent budget keys follow TAO_BUDGET_<SCOPE>_<METRIC>_WARN for advisory
	// thresholds and TAO_BUDGET_SLICE_<METRIC>_STOP for the enforced slice caps.
	EnvBudgetSliceOutputTokensWarn      = "TAO_BUDGET_SLICE_OUTPUT_TOKENS_WARN" // #nosec G101 -- environment key, not a credential.
	EnvBudgetSliceCostWarn              = "TAO_BUDGET_SLICE_COST_WARN"
	EnvBudgetSliceToolCallsWarn         = "TAO_BUDGET_SLICE_TOOL_CALLS_WARN"
	EnvBudgetSliceAssistantMessagesWarn = "TAO_BUDGET_SLICE_ASSISTANT_MESSAGES_WARN"
	EnvBudgetSliceErroredMessagesWarn   = "TAO_BUDGET_SLICE_ERRORED_MESSAGES_WARN"
	EnvBudgetPlanOutputTokensWarn       = "TAO_BUDGET_PLAN_OUTPUT_TOKENS_WARN" // #nosec G101 -- environment key, not a credential.
	EnvBudgetPlanCostWarn               = "TAO_BUDGET_PLAN_COST_WARN"
	EnvBudgetPlanToolCallsWarn          = "TAO_BUDGET_PLAN_TOOL_CALLS_WARN"
	EnvBudgetPlanAssistantMessagesWarn  = "TAO_BUDGET_PLAN_ASSISTANT_MESSAGES_WARN"
	EnvBudgetPlanErroredMessagesWarn    = "TAO_BUDGET_PLAN_ERRORED_MESSAGES_WARN"
	EnvBudgetSliceOutputTokensStop      = "TAO_BUDGET_SLICE_OUTPUT_TOKENS_STOP" // #nosec G101 -- environment key, not a credential.
	EnvBudgetSliceCostStop              = "TAO_BUDGET_SLICE_COST_STOP"

	// Deprecated budget aliases remain accepted for one release. Each maps to
	// the canonical key of the same metric; the canonical key wins when both are
	// set, and an accepted alias carries a deprecation warning in status.
	EnvBudgetSliceOutputTokensDeprecated      = "TAO_BUDGET_SLICE_OUTPUT_TOKENS" // #nosec G101 -- environment key, not a credential.
	EnvBudgetSliceCostDeprecated              = "TAO_BUDGET_SLICE_COST"
	EnvBudgetSliceToolCallsDeprecated         = "TAO_BUDGET_SLICE_TOOL_CALLS"
	EnvBudgetSliceAssistantMessagesDeprecated = "TAO_BUDGET_SLICE_ASSISTANT_MESSAGES"
	EnvBudgetSliceErroredMessagesDeprecated   = "TAO_BUDGET_SLICE_ERRORED_MESSAGES"
	EnvBudgetPlanOutputTokensDeprecated       = "TAO_BUDGET_PLAN_OUTPUT_TOKENS" // #nosec G101 -- environment key, not a credential.
	EnvBudgetPlanCostDeprecated               = "TAO_BUDGET_PLAN_COST"
	EnvBudgetPlanToolCallsDeprecated          = "TAO_BUDGET_PLAN_TOOL_CALLS"
	EnvBudgetPlanAssistantMessagesDeprecated  = "TAO_BUDGET_PLAN_ASSISTANT_MESSAGES"
	EnvBudgetPlanErroredMessagesDeprecated    = "TAO_BUDGET_PLAN_ERRORED_MESSAGES"
	EnvMaxSliceOutputTokensDeprecated         = "TAO_MAX_SLICE_OUTPUT_TOKENS" // #nosec G101 -- environment key, not a credential.
	EnvMaxSliceCostDeprecated                 = "TAO_MAX_SLICE_COST"
)

// EnvDefaults is the environment default layer shared by CLI and prompt
// commands. Optional defaults preserve explicit false semantics from
// environment variables.
type EnvDefaults struct {
	RunOptionsPatch
	SessionWarnPercent          int
	AutoRework                  *bool
	MaxReworkAttempts           *int
	ReworkEscalationFromAttempt *int
	UpdateMode                  selfupdate.Mode
	Theme                       theme.Theme
	SkipPermissions             bool
	// Budget holds every agent budget threshold and cap loaded from the
	// TAO_BUDGET_* table rows; read it through EnvSnapshot.Budget.
	Budget                           plan.AgentBudget
	MergeVerifyCommand               string
	MergeVerifyCommandSet            bool
	AggregateReviewConvergenceWindow int
	ApprovedBy                       string
	RunHeader                        bool
	PlannerRouting                   PlannerRoutingConfig
}

func (d EnvDefaults) ReworkEscalationFromAttemptValue() int {
	if d.ReworkEscalationFromAttempt != nil {
		return *d.ReworkEscalationFromAttempt
	}
	return DefaultReworkEscalationFromAttempt
}

type EnvVarStatus struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Source  string `json:"source"`
	Warning string `json:"warning,omitempty"`
}

// runtimeEnvVar describes one runtime environment variable. It is the single
// source of truth that the env-default loader, the status reporter, and the
// key list are all derived from, so adding a new runtime env var only requires
// one new table entry.
type runtimeEnvVar struct {
	// name is the environment variable key.
	name string
	// aliasOf names the canonical key this deprecated row feeds. Alias rows
	// share the canonical apply function, yield to a set canonical key, and
	// are omitted from status while unset.
	aliasOf string
	// defaultValue renders the built-in default for a status row. Run defaults
	// come from DefaultRunOptionsPatch; command-specific settings use their owning
	// policy defaults.
	defaultValue func(RunOptionsPatch) string
	// apply validates value, writes it into defaults, and returns its canonical
	// string form. It is the only per-var logic: the loader uses the mutation,
	// the status reporter uses the canonical string, neither duplicates parsing.
	apply          func(defaults *EnvDefaults, value string) (string, error)
	applyWhenEmpty bool
	blankIsEmpty   bool
	// Only presentation settings may warn and default in a snapshot.
	fallbackOnInvalid bool
}

func (v runtimeEnvVar) hasOverride(value string, present bool) bool {
	if v.blankIsEmpty {
		value = strings.TrimSpace(value)
	}
	return present && (value != "" || v.applyWhenEmpty)
}

// runtimeEnvVars is the ordered table every runtime env-var site is derived
// from. Order is preserved for the snapshot's status row list.
var runtimeEnvVars = append([]runtimeEnvVar{
	{
		name:         EnvCommitPolicy,
		defaultValue: func(d RunOptionsPatch) string { return d.CommitPolicy.String() },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseCommitPolicy(value)
			if err != nil {
				return "", err
			}
			defaults.CommitPolicy = parsed
			return parsed.String(), nil
		},
	},
	{
		name:         EnvExecutionMode,
		defaultValue: func(d RunOptionsPatch) string { return d.ExecutionModeValue().String() },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseExecutionMode(value)
			if err != nil {
				return "", err
			}
			defaults.ExecutionMode = parsed
			return parsed.String(), nil
		},
	},
	{
		name:         EnvAgent,
		defaultValue: func(d RunOptionsPatch) string { return d.Agent.String() },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseAgentKind(value)
			if err != nil {
				return "", err
			}
			defaults.Agent = parsed
			return parsed.String(), nil
		},
	},
	{
		name:         EnvSessionTimeout,
		defaultValue: func(d RunOptionsPatch) string { return d.SessionTimeoutValue().String() },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := parseSessionTimeout(value)
			if err != nil {
				return "", err
			}
			defaults.SessionTimeout = &parsed
			return parsed.String(), nil
		},
	},
	{
		name:         EnvSessionWarnPercent,
		defaultValue: func(RunOptionsPatch) string { return strconv.Itoa(DefaultSessionWarnPercent) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 0 || parsed > 99 {
				return "", fmt.Errorf("must be an integer from 0 to 99 (0 disables warnings)")
			}
			defaults.SessionWarnPercent = parsed
			return strconv.Itoa(parsed), nil
		},
	},
	{
		name: EnvModel, applyWhenEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseModelName(value)
			if err != nil {
				return "", err
			}
			defaults.Base = parsed
			return parsed, nil
		},
	},
	{
		name: EnvRunModel, applyWhenEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseModelName(value)
			if err != nil {
				return "", err
			}
			defaults.Run = parsed
			return parsed, nil
		},
	},
	{
		name: EnvReviewModel, applyWhenEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseModelName(value)
			if err != nil {
				return "", err
			}
			defaults.Review = parsed
			return parsed, nil
		},
	},
	{
		name: EnvMergeReviewModel, applyWhenEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseModelName(value)
			if err != nil {
				return "", err
			}
			defaults.MergeReview = parsed
			return parsed, nil
		},
	},
	{
		name: EnvResolverModel, applyWhenEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseModelName(value)
			if err != nil {
				return "", err
			}
			defaults.Resolver = parsed
			return parsed, nil
		},
	},
	{
		name: EnvReworkEscalationModel, applyWhenEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseModelName(value)
			if err != nil {
				return "", err
			}
			defaults.ReworkEscalation = parsed
			return parsed, nil
		},
	},
	{
		name:         EnvUpdate,
		defaultValue: func(RunOptionsPatch) string { return string(selfupdate.ModeWarn) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := selfupdate.ParseMode(value)
			if err != nil {
				return "", err
			}
			defaults.UpdateMode = parsed
			return string(parsed), nil
		},
	},
	{
		name:         EnvPullRequest,
		defaultValue: func(d RunOptionsPatch) string { return strconv.FormatBool(d.PullRequestValue()) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseEnvBool(value)
			if err != nil {
				return "", err
			}
			defaults.PullRequest = &parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		name:         EnvReview,
		defaultValue: func(d RunOptionsPatch) string { return strconv.FormatBool(d.ReviewEnabledValue()) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseEnvBool(value)
			if err != nil {
				return "", err
			}
			defaults.ReviewEnabled = &parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		// Direct runs default on; false disables automatic rework.
		name:         EnvAutoRework,
		defaultValue: func(RunOptionsPatch) string { return strconv.FormatBool(true) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseEnvBool(value)
			if err != nil {
				return "", err
			}
			defaults.AutoRework = &parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		// Direct-run rework defaults to five bounded cycles; zero disables it.
		name:         EnvMaxReworkAttempts,
		defaultValue: func(RunOptionsPatch) string { return strconv.Itoa(DefaultMaxReworkAttempts) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := parseMaxReworkAttempts(value)
			if err != nil {
				return "", err
			}
			defaults.MaxReworkAttempts = &parsed
			return strconv.Itoa(parsed), nil
		},
	},
	{
		name:         EnvReworkEscalationFromAttempt,
		defaultValue: func(RunOptionsPatch) string { return strconv.Itoa(DefaultReworkEscalationFromAttempt) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 1 {
				return "", fmt.Errorf("must be an integer at least 1")
			}
			defaults.ReworkEscalationFromAttempt = &parsed
			return strconv.Itoa(parsed), nil
		},
	},
	{
		name:         EnvSkipPermissions,
		defaultValue: func(RunOptionsPatch) string { return strconv.FormatBool(false) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseEnvBool(value)
			if err != nil {
				return "", err
			}
			defaults.SkipPermissions = parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		// An explicitly empty merge command disables verification, while an unset
		// variable allows command auto-detection.
		name: EnvMergeVerifyCommand, applyWhenEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "auto-detect" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			defaults.MergeVerifyCommand = value
			defaults.MergeVerifyCommandSet = true
			return value, nil
		},
	},
	{
		name:         EnvAggregateReviewConvergenceWindow,
		blankIsEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return strconv.Itoa(DefaultAggregateReviewConvergenceWindow) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := parseAggregateReviewConvergenceWindow(value)
			if err != nil {
				return "", err
			}
			defaults.AggregateReviewConvergenceWindow = parsed
			return strconv.Itoa(parsed), nil
		},
	},
	{
		name:         EnvApprovedBy,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			defaults.ApprovedBy = value
			return value, nil
		},
	},
	{
		name:              EnvRunHeader,
		fallbackOnInvalid: true,
		defaultValue:      func(RunOptionsPatch) string { return strconv.FormatBool(true) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParseEnvBool(value)
			if err != nil {
				return "", err
			}
			defaults.RunHeader = parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		name:         EnvPlannerRouting,
		blankIsEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "not set (default: off)" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParsePlannerRoutingMode(strings.TrimSpace(value))
			if err != nil {
				return "", err
			}
			defaults.PlannerRouting.Mode = parsed
			defaults.PlannerRouting.ModeSet = true
			return string(parsed), nil
		},
	},
	{
		name:         EnvPlannerRoutingArms,
		blankIsEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "not set (comma list, e.g. pi=0.5,claude=0.5)" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParsePlannerRoutingArms(strings.TrimSpace(value))
			if err != nil {
				return "", err
			}
			defaults.PlannerRouting.Arms = parsed
			defaults.PlannerRouting.ArmsSet = true
			return strings.TrimSpace(value), nil
		},
	},
	{
		name:         EnvPlannerRoutingFloor,
		blankIsEmpty: true,
		defaultValue: func(RunOptionsPatch) string { return "not set (default: 0.1)" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := ParsePlannerRoutingFloor(strings.TrimSpace(value))
			if err != nil {
				return "", err
			}
			defaults.PlannerRouting.Floor = parsed
			defaults.PlannerRouting.FloorSet = true
			return strconv.FormatFloat(parsed, 'f', -1, 64), nil
		},
	},
	{
		name: EnvTheme, fallbackOnInvalid: true,
		defaultValue: func(RunOptionsPatch) string { return theme.Default().Name() },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			selected, ok := theme.Lookup(value)
			if !ok {
				return "", fmt.Errorf("unknown theme; choose %s", strings.Join(theme.Names(), " or "))
			}
			defaults.Theme = selected
			return selected.Name(), nil
		},
	},
}, budgetEnvVars...)

// budgetEnvVars is the budget block of the table: twelve canonical rows
// followed by their twelve deprecated aliases.
var budgetEnvVars = agentBudgetRuntimeEnvVars()

func agentBudgetRuntimeEnvVars() []runtimeEnvVar {
	defaults := plan.DefaultAgentBudget()
	warnInteger := func(name string, defaultValue int64, set func(*plan.AgentBudget, int64)) runtimeEnvVar {
		return budgetIntegerEnvVar(name, strconv.FormatInt(defaultValue, 10), set)
	}
	warnCost := func(name string, defaultValue float64, set func(*plan.AgentBudget, float64)) runtimeEnvVar {
		return budgetCostEnvVar(name, strconv.FormatFloat(defaultValue, 'f', -1, 64), set)
	}
	canonical := []runtimeEnvVar{
		warnInteger(EnvBudgetSliceOutputTokensWarn, defaults.Slice.OutputTokens.Warn, func(b *plan.AgentBudget, n int64) { b.Slice.OutputTokens.Warn = n }),
		warnCost(EnvBudgetSliceCostWarn, defaults.Slice.Cost.Warn, func(b *plan.AgentBudget, n float64) { b.Slice.Cost.Warn = n }),
		warnInteger(EnvBudgetSliceToolCallsWarn, defaults.Slice.ToolCalls.Warn, func(b *plan.AgentBudget, n int64) { b.Slice.ToolCalls.Warn = n }),
		warnInteger(EnvBudgetSliceAssistantMessagesWarn, defaults.Slice.AssistantMessages.Warn, func(b *plan.AgentBudget, n int64) { b.Slice.AssistantMessages.Warn = n }),
		warnInteger(EnvBudgetSliceErroredMessagesWarn, defaults.Slice.ErroredMessages.Warn, func(b *plan.AgentBudget, n int64) { b.Slice.ErroredMessages.Warn = n }),
		warnInteger(EnvBudgetPlanOutputTokensWarn, defaults.Plan.OutputTokens.Warn, func(b *plan.AgentBudget, n int64) { b.Plan.OutputTokens.Warn = n }),
		warnCost(EnvBudgetPlanCostWarn, defaults.Plan.Cost.Warn, func(b *plan.AgentBudget, n float64) { b.Plan.Cost.Warn = n }),
		warnInteger(EnvBudgetPlanToolCallsWarn, defaults.Plan.ToolCalls.Warn, func(b *plan.AgentBudget, n int64) { b.Plan.ToolCalls.Warn = n }),
		warnInteger(EnvBudgetPlanAssistantMessagesWarn, defaults.Plan.AssistantMessages.Warn, func(b *plan.AgentBudget, n int64) { b.Plan.AssistantMessages.Warn = n }),
		warnInteger(EnvBudgetPlanErroredMessagesWarn, defaults.Plan.ErroredMessages.Warn, func(b *plan.AgentBudget, n int64) { b.Plan.ErroredMessages.Warn = n }),
		// Stop caps are opt-in: unset stays disabled and an explicit zero is a hard limit.
		budgetIntegerEnvVar(EnvBudgetSliceOutputTokensStop, "disabled", func(b *plan.AgentBudget, n int64) { b.Slice.OutputTokens.Stop = &n }),
		budgetCostEnvVar(EnvBudgetSliceCostStop, "disabled", func(b *plan.AgentBudget, n float64) { b.Slice.Cost.Stop = &n }),
	}
	aliases := []struct{ name, canonical string }{
		{EnvBudgetSliceOutputTokensDeprecated, EnvBudgetSliceOutputTokensWarn},
		{EnvBudgetSliceCostDeprecated, EnvBudgetSliceCostWarn},
		{EnvBudgetSliceToolCallsDeprecated, EnvBudgetSliceToolCallsWarn},
		{EnvBudgetSliceAssistantMessagesDeprecated, EnvBudgetSliceAssistantMessagesWarn},
		{EnvBudgetSliceErroredMessagesDeprecated, EnvBudgetSliceErroredMessagesWarn},
		{EnvBudgetPlanOutputTokensDeprecated, EnvBudgetPlanOutputTokensWarn},
		{EnvBudgetPlanCostDeprecated, EnvBudgetPlanCostWarn},
		{EnvBudgetPlanToolCallsDeprecated, EnvBudgetPlanToolCallsWarn},
		{EnvBudgetPlanAssistantMessagesDeprecated, EnvBudgetPlanAssistantMessagesWarn},
		{EnvBudgetPlanErroredMessagesDeprecated, EnvBudgetPlanErroredMessagesWarn},
		{EnvMaxSliceOutputTokensDeprecated, EnvBudgetSliceOutputTokensStop},
		{EnvMaxSliceCostDeprecated, EnvBudgetSliceCostStop},
	}
	rows := make([]runtimeEnvVar, 0, len(canonical)+len(aliases))
	rows = append(rows, canonical...)
	for _, alias := range aliases {
		for _, row := range canonical {
			if row.name == alias.canonical {
				row.name, row.aliasOf = alias.name, alias.canonical
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func budgetIntegerEnvVar(name, defaultValue string, set func(*plan.AgentBudget, int64)) runtimeEnvVar {
	return runtimeEnvVar{
		name:         name,
		defaultValue: func(RunOptionsPatch) string { return defaultValue },
		apply: func(env *EnvDefaults, value string) (string, error) {
			parsed, err := parseBudgetInteger(value)
			if err != nil {
				return "", err
			}
			set(&env.Budget, parsed)
			return strconv.FormatInt(parsed, 10), nil
		},
	}
}

func budgetCostEnvVar(name, defaultValue string, set func(*plan.AgentBudget, float64)) runtimeEnvVar {
	return runtimeEnvVar{
		name:         name,
		defaultValue: func(RunOptionsPatch) string { return defaultValue },
		apply: func(env *EnvDefaults, value string) (string, error) {
			parsed, err := parseBudgetCost(value)
			if err != nil {
				return "", err
			}
			set(&env.Budget, parsed)
			return strconv.FormatFloat(parsed, 'f', -1, 64), nil
		},
	}
}

// BudgetEnvKeys returns the budget block of the runtime table in order: the
// twelve canonical WARN/STOP keys followed by their twelve deprecated aliases.
func BudgetEnvKeys() []string {
	keys := make([]string, len(budgetEnvVars))
	for i, v := range budgetEnvVars {
		keys[i] = v.name
	}
	return keys
}

func parseSessionTimeout(value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("must be 0 or a positive duration (e.g. 20m): %w", err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("must be 0 or a positive duration")
	}
	return parsed, nil
}

// ParseEnvBool accepts the shared, case-insensitive runtime boolean grammar.
// Empty handling belongs to each setting, not this parser.
func ParseEnvBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("must be a boolean (true/false, 1/0, yes/no, on/off, t/f, y/n)")
	}
}

func parseAggregateReviewConvergenceWindow(value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 2 {
		return 0, fmt.Errorf("must be an integer of at least 2")
	}
	return parsed, nil
}

// RuntimeEnvKeys returns the canonical ordered runtime environment key list.
func RuntimeEnvKeys() []string {
	keys := make([]string, len(runtimeEnvVars))
	for i, v := range runtimeEnvVars {
		keys[i] = v.name
	}
	return keys
}
