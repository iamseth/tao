package runtimeconfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvCommitPolicy      = "TAO_COMMIT_POLICY"
	EnvExecutionMode     = "TAO_EXECUTION_MODE"
	EnvAgent             = "TAO_AGENT"
	EnvPullRequest       = "TAO_PULL_REQUEST"
	EnvReview            = "TAO_REVIEW"
	EnvAutoRework        = "TAO_AUTO_REWORK"
	EnvMaxReworkAttempts = "TAO_MAX_REWORK_ATTEMPTS"
	EnvSessionTimeout    = "TAO_SESSION_TIMEOUT"
	EnvNotifyCommand     = "TAO_NOTIFY_COMMAND"
	EnvSkipPermissions   = "TAO_DANGEROUSLY_SKIP_PERMISSIONS"
)

// EnvDefaults is the environment default layer shared by CLI and prompt
// commands. Optional defaults preserve explicit false semantics from
// environment variables.
type EnvDefaults struct {
	RunOptionsPatch
	AutoRework        *bool
	MaxReworkAttempts *int
	NotifyCommand     string
	SkipPermissions   bool
}

type EnvVarStatus struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"`
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
	apply func(defaults *EnvDefaults, value string) (string, error)
}

// runtimeEnvVars is the ordered table every runtime env-var site is derived
// from. Order is preserved for the RuntimeEnvStatus row list.
var runtimeEnvVars = []runtimeEnvVar{
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
	defaults := EnvDefaults{RunOptionsPatch: DefaultRunOptionsPatch()}
	for _, v := range runtimeEnvVars {
		value, ok := os.LookupEnv(v.name)
		if !ok || value == "" {
			continue
		}
		if _, err := v.apply(&defaults, value); err != nil {
			return defaults, fmt.Errorf("%s: %w", v.name, err)
		}
	}
	return defaults, nil
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
		var scratch EnvDefaults
		parsed, err := v.apply(&scratch, value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", v.name, err)
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
