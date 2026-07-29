package runtimeconfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

const (
	EnvCommitPolicy         = "TAO_COMMIT_POLICY"
	EnvExecutionMode        = "TAO_EXECUTION_MODE"
	EnvAgent                = "TAO_AGENT"
	EnvPullRequest          = "TAO_PULL_REQUEST"
	EnvReview               = "TAO_REVIEW"
	EnvAutoRework           = "TAO_AUTO_REWORK"
	EnvMaxReworkAttempts    = "TAO_MAX_REWORK_ATTEMPTS"
	EnvSessionTimeout       = "TAO_SESSION_TIMEOUT"
	EnvNotifyCommand        = "TAO_NOTIFY_COMMAND"
	EnvSkipPermissions      = "TAO_DANGEROUSLY_SKIP_PERMISSIONS"
	EnvMaxSliceOutputTokens = "TAO_MAX_SLICE_OUTPUT_TOKENS" // #nosec G101 -- environment key, not a credential.
	EnvMaxSliceCost         = "TAO_MAX_SLICE_COST"

	EnvBudgetSliceOutputTokens      = "TAO_BUDGET_SLICE_OUTPUT_TOKENS" // #nosec G101 -- environment key, not a credential.
	EnvBudgetSliceCost              = "TAO_BUDGET_SLICE_COST"
	EnvBudgetSliceToolCalls         = "TAO_BUDGET_SLICE_TOOL_CALLS"
	EnvBudgetSliceAssistantMessages = "TAO_BUDGET_SLICE_ASSISTANT_MESSAGES"
	EnvBudgetSliceErroredMessages   = "TAO_BUDGET_SLICE_ERRORED_MESSAGES"
	EnvBudgetPlanOutputTokens       = "TAO_BUDGET_PLAN_OUTPUT_TOKENS" // #nosec G101 -- environment key, not a credential.
	EnvBudgetPlanCost               = "TAO_BUDGET_PLAN_COST"
	EnvBudgetPlanToolCalls          = "TAO_BUDGET_PLAN_TOOL_CALLS"
	EnvBudgetPlanAssistantMessages  = "TAO_BUDGET_PLAN_ASSISTANT_MESSAGES"
	EnvBudgetPlanErroredMessages    = "TAO_BUDGET_PLAN_ERRORED_MESSAGES"
)

// EnvDefaults is the environment default layer shared by CLI and prompt
// commands. Optional defaults preserve explicit false semantics from
// environment variables.
type EnvDefaults struct {
	RunOptionsPatch
	AutoRework            *bool
	MaxReworkAttempts     *int
	NotifyCommand         string
	SkipPermissions       bool
	SliceBudgetCaps       SliceBudgetCaps
	AgentBudgetThresholds plan.AgentBudgetThresholds
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
	// defaultValue renders the built-in default for a status row. Run defaults
	// come from DefaultRunOptionsPatch; command-specific settings use their owning
	// policy defaults.
	defaultValue func(RunOptionsPatch) string
	// apply validates value, writes it into defaults, and returns its canonical
	// string form. It is the only per-var logic: the loader uses the mutation,
	// the status reporter uses the canonical string, neither duplicates parsing.
	apply             func(defaults *EnvDefaults, value string) (string, error)
	budget            bool
	fallbackOnInvalid bool
}

// runtimeEnvVars is the ordered table every runtime env-var site is derived
// from. Order is preserved for the RuntimeEnvStatus row list.
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
		name:         EnvPullRequest,
		defaultValue: func(d RunOptionsPatch) string { return strconv.FormatBool(d.PullRequestValue()) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return "", err
			}
			defaults.PullRequest = &parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		name:         EnvNotifyCommand,
		defaultValue: func(RunOptionsPatch) string { return "" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			command := strings.TrimSpace(value)
			defaults.NotifyCommand = command
			return command, nil
		},
	},
	{
		name:         EnvReview,
		defaultValue: func(d RunOptionsPatch) string { return strconv.FormatBool(d.ReviewEnabledValue()) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := parseReviewEnabled(value)
			if err != nil {
				return "", err
			}
			defaults.ReviewEnabled = &parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		// Direct runs default on and false disables them; queue start remains
		// opt-in and true enables it.
		name:         EnvAutoRework,
		defaultValue: func(RunOptionsPatch) string { return strconv.FormatBool(true) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := parseAutoReworkEnabled(value)
			if err != nil {
				return "", err
			}
			defaults.AutoRework = &parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		// Direct and queued rework default to five bounded cycles; zero disables
		// automatic rework.
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
		name:         EnvSkipPermissions,
		defaultValue: func(RunOptionsPatch) string { return strconv.FormatBool(false) },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return "", err
			}
			defaults.SkipPermissions = parsed
			return strconv.FormatBool(parsed), nil
		},
	},
	{
		name: EnvMaxSliceOutputTokens, fallbackOnInvalid: true,
		defaultValue: func(RunOptionsPatch) string { return "disabled" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := parseBudgetInteger(value)
			if err != nil {
				return "", err
			}
			defaults.SliceBudgetCaps.OutputTokens = &parsed
			return strconv.FormatInt(parsed, 10), nil
		},
	},
	{
		name: EnvMaxSliceCost, fallbackOnInvalid: true,
		defaultValue: func(RunOptionsPatch) string { return "disabled" },
		apply: func(defaults *EnvDefaults, value string) (string, error) {
			parsed, err := parseBudgetCost(value)
			if err != nil {
				return "", err
			}
			defaults.SliceBudgetCaps.Cost = &parsed
			return strconv.FormatFloat(parsed, 'f', -1, 64), nil
		},
	},
}, agentBudgetRuntimeEnvVars()...)

func agentBudgetRuntimeEnvVars() []runtimeEnvVar {
	defaults := defaultAgentBudgetThresholds()
	integer := func(name string, defaultValue int64, set func(*plan.AgentBudgetThresholds, int64)) runtimeEnvVar {
		return runtimeEnvVar{
			name: name, budget: true, fallbackOnInvalid: true,
			defaultValue: func(RunOptionsPatch) string { return strconv.FormatInt(defaultValue, 10) },
			apply: func(env *EnvDefaults, value string) (string, error) {
				parsed, err := parseBudgetInteger(value)
				if err != nil {
					return "", err
				}
				set(&env.AgentBudgetThresholds, parsed)
				return strconv.FormatInt(parsed, 10), nil
			},
		}
	}
	cost := func(name string, defaultValue float64, set func(*plan.AgentBudgetThresholds, float64)) runtimeEnvVar {
		return runtimeEnvVar{
			name: name, budget: true, fallbackOnInvalid: true,
			defaultValue: func(RunOptionsPatch) string { return strconv.FormatFloat(defaultValue, 'f', -1, 64) },
			apply: func(env *EnvDefaults, value string) (string, error) {
				parsed, err := parseBudgetCost(value)
				if err != nil {
					return "", err
				}
				set(&env.AgentBudgetThresholds, parsed)
				return strconv.FormatFloat(parsed, 'f', -1, 64), nil
			},
		}
	}
	return []runtimeEnvVar{
		integer(EnvBudgetSliceOutputTokens, defaults.Slice.OutputTokens, func(v *plan.AgentBudgetThresholds, n int64) { v.Slice.OutputTokens = n }),
		cost(EnvBudgetSliceCost, defaults.Slice.Cost, func(v *plan.AgentBudgetThresholds, n float64) { v.Slice.Cost = n }),
		integer(EnvBudgetSliceToolCalls, defaults.Slice.ToolCalls, func(v *plan.AgentBudgetThresholds, n int64) { v.Slice.ToolCalls = n }),
		integer(EnvBudgetSliceAssistantMessages, defaults.Slice.AssistantMessages, func(v *plan.AgentBudgetThresholds, n int64) { v.Slice.AssistantMessages = n }),
		integer(EnvBudgetSliceErroredMessages, defaults.Slice.ErroredMessages, func(v *plan.AgentBudgetThresholds, n int64) { v.Slice.ErroredMessages = n }),
		integer(EnvBudgetPlanOutputTokens, defaults.Plan.OutputTokens, func(v *plan.AgentBudgetThresholds, n int64) { v.Plan.OutputTokens = n }),
		cost(EnvBudgetPlanCost, defaults.Plan.Cost, func(v *plan.AgentBudgetThresholds, n float64) { v.Plan.Cost = n }),
		integer(EnvBudgetPlanToolCalls, defaults.Plan.ToolCalls, func(v *plan.AgentBudgetThresholds, n int64) { v.Plan.ToolCalls = n }),
		integer(EnvBudgetPlanAssistantMessages, defaults.Plan.AssistantMessages, func(v *plan.AgentBudgetThresholds, n int64) { v.Plan.AssistantMessages = n }),
		integer(EnvBudgetPlanErroredMessages, defaults.Plan.ErroredMessages, func(v *plan.AgentBudgetThresholds, n int64) { v.Plan.ErroredMessages = n }),
	}
}

func parseSessionTimeout(value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if parsed < 0 {
		return 0, fmt.Errorf("must be 0 or a positive duration")
	}
	return parsed, nil
}

func parseReviewEnabled(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("must be truthy or falsey (true/false, 1/0, yes/no, on/off)")
	}
}

func RuntimeEnvDefaults() (EnvDefaults, error) {
	defaults := EnvDefaults{RunOptionsPatch: DefaultRunOptionsPatch(), AgentBudgetThresholds: defaultAgentBudgetThresholds()}
	for _, v := range runtimeEnvVars {
		value, ok := os.LookupEnv(v.name)
		if !ok || value == "" {
			continue
		}
		if _, err := v.apply(&defaults, value); err != nil {
			if v.fallbackOnInvalid {
				continue
			}
			return defaults, fmt.Errorf("%s: %w", v.name, err)
		}
	}
	return defaults, nil
}

// RuntimeAgentBudgetThresholds resolves budget overrides without coupling
// advisory warnings to unrelated runtime configuration errors.
func RuntimeAgentBudgetThresholds() plan.AgentBudgetThresholds {
	defaults := EnvDefaults{AgentBudgetThresholds: defaultAgentBudgetThresholds()}
	for _, v := range runtimeEnvVars {
		if !v.budget {
			continue
		}
		value, ok := os.LookupEnv(v.name)
		if !ok || value == "" {
			continue
		}
		_, _ = v.apply(&defaults, value)
	}
	return defaults.AgentBudgetThresholds
}

// RuntimeSliceBudgetCaps resolves opt-in hard caps independently from other
// runtime settings. Invalid values are reported and leave that cap disabled.
func RuntimeSliceBudgetCaps() (SliceBudgetCaps, []string) {
	var defaults EnvDefaults
	var warnings []string
	for _, v := range runtimeEnvVars {
		if v.name != EnvMaxSliceOutputTokens && v.name != EnvMaxSliceCost {
			continue
		}
		value, ok := os.LookupEnv(v.name)
		if !ok || value == "" {
			continue
		}
		if _, err := v.apply(&defaults, value); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s=%q is invalid (%v); cap disabled", v.name, value, err))
		}
	}
	return defaults.SliceBudgetCaps, warnings
}

func RuntimeEnvStatus() ([]EnvVarStatus, error) {
	defaults := DefaultRunOptionsPatch()
	rows := make([]EnvVarStatus, len(runtimeEnvVars))
	for i, v := range runtimeEnvVars {
		rows[i] = EnvVarStatus{Name: v.name, Value: v.defaultValue(defaults), Source: "default"}
		value, ok := os.LookupEnv(v.name)
		if !ok || value == "" {
			continue
		}
		scratch := EnvDefaults{AgentBudgetThresholds: defaultAgentBudgetThresholds()}
		parsed, err := v.apply(&scratch, value)
		if err != nil {
			if !v.fallbackOnInvalid {
				return nil, fmt.Errorf("%s: %w", v.name, err)
			}
			fallback := "using default"
			if v.name == EnvMaxSliceOutputTokens || v.name == EnvMaxSliceCost {
				fallback = "cap disabled"
			}
			rows[i].Warning = fmt.Sprintf("invalid env value %q: %v; %s", value, err, fallback)
			continue
		}
		rows[i].Value = parsed
		rows[i].Source = "env"
	}
	return rows, nil
}

// RuntimeEnvKeys returns the canonical ordered runtime environment key list.
func RuntimeEnvKeys() []string {
	keys := make([]string, len(runtimeEnvVars))
	for i, v := range runtimeEnvVars {
		keys[i] = v.name
	}
	return keys
}

func runtimeEnvKeys() []string {
	return RuntimeEnvKeys()
}
