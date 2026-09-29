package runtimeconfig

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/selfupdate"
	"github.com/iamseth/tao/internal/theme"
)

// EnvSnapshot captures the runtime table for one invocation. Construction never
// fails: consumers must Require the settings they use before acting on Defaults.
// Fields are immutable after construction; projections do not share mutable data.
// The zero snapshot projects built-ins, without consulting the environment.
type EnvSnapshot struct {
	defaults EnvDefaults
	rows     []EnvVarStatus
	failures map[string]error
}

// LoadEnv looks up and parses each runtime key once, retaining every status row
// and failure. A nil lookup selects built-ins. Invalid fields cannot partially
// mutate defaults; only theme/header failures are warning-only.
func LoadEnv(lookup func(string) (string, bool)) EnvSnapshot {
	s := EnvSnapshot{
		defaults: builtinEnvDefaults(),
		rows:     make([]EnvVarStatus, 0, len(runtimeEnvVars)),
		failures: make(map[string]error),
	}
	builtins := s.defaults.RunOptionsPatch
	for _, v := range runtimeEnvVars {
		row := EnvVarStatus{Name: v.name, Value: v.defaultValue(builtins), Source: "default"}
		var raw string
		var set bool
		if lookup != nil {
			raw, set = lookup(v.name)
		}
		if v.hasOverride(raw, set) {
			candidate := cloneEnvDefaults(s.defaults)
			value, err := v.apply(&candidate, raw)
			switch {
			case err == nil:
				s.defaults = candidate
				row.Value, row.Source = value, "env"
			case v.fallbackOnInvalid:
				row.Warning = fmt.Sprintf("invalid env value %q: %v; using default", raw, err)
			default:
				failure := fmt.Errorf("%s: %w", v.name, err)
				s.failures[v.name] = failure
				row.Source = "invalid"
				row.Warning = fmt.Sprintf("invalid env value %q: %v; rejected", raw, failure)
			}
		}
		s.rows = append(s.rows, row)
	}
	return s
}

// RuntimeEnv is the production binding. It deliberately does not cache across
// invocations; callers capture once and pass the snapshot to their consumers.
func RuntimeEnv() EnvSnapshot { return LoadEnv(os.LookupEnv) }

// Defaults returns a defensive copy of the captured values. Rejected settings
// retain built-ins, but those values are not an authorization to bypass Require.
func (s EnvSnapshot) Defaults() EnvDefaults {
	if s.rows == nil {
		return builtinEnvDefaults()
	}
	return cloneEnvDefaults(s.defaults)
}

// Require rejects only requested, invalid non-presentation settings. An empty
// key list requires nothing; unrelated failures remain available through Status.
func (s EnvSnapshot) Require(keys ...string) error {
	var failures []error
	for _, key := range keys {
		if err := s.failures[key]; err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// BudgetThresholds validates the advisory settings before projecting their
// typed values. Provider, routing, merge, and hard-cap settings are unrelated.
func (s EnvSnapshot) BudgetThresholds() (plan.AgentBudgetThresholds, error) {
	if err := s.Require(
		EnvBudgetSliceOutputTokens, EnvBudgetSliceCost, EnvBudgetSliceToolCalls,
		EnvBudgetSliceAssistantMessages, EnvBudgetSliceErroredMessages,
		EnvBudgetPlanOutputTokens, EnvBudgetPlanCost, EnvBudgetPlanToolCalls,
		EnvBudgetPlanAssistantMessages, EnvBudgetPlanErroredMessages,
	); err != nil {
		return plan.AgentBudgetThresholds{}, err
	}
	return s.Defaults().AgentBudgetThresholds, nil
}

// BudgetCaps validates opt-in implementation limits independently of advisory
// thresholds. Missing caps stay disabled; explicit zero remains a hard limit.
func (s EnvSnapshot) BudgetCaps() (SliceBudgetCaps, error) {
	if err := s.Require(EnvMaxSliceOutputTokens, EnvMaxSliceCost); err != nil {
		return SliceBudgetCaps{}, err
	}
	return s.Defaults().SliceBudgetCaps, nil
}

// Status returns every captured row in table order. Source "invalid" denotes a
// rejected override, distinct from an accepted "default" or "env" value. A
// warning with source "default" is a presentation-only fallback.
func (s EnvSnapshot) Status() []EnvVarStatus {
	if s.rows == nil {
		return LoadEnv(nil).Status()
	}
	return slices.Clone(s.rows)
}

func builtinEnvDefaults() EnvDefaults {
	enabled, attempts, escalation := true, DefaultMaxReworkAttempts, DefaultReworkEscalationFromAttempt
	return EnvDefaults{
		RunOptionsPatch:                  DefaultRunOptionsPatch(),
		AutoRework:                       &enabled,
		MaxReworkAttempts:                &attempts,
		ReworkEscalationFromAttempt:      &escalation,
		UpdateMode:                       selfupdate.ModeWarn,
		Theme:                            theme.Default(),
		RunHeader:                        true,
		AggregateReviewConvergenceWindow: DefaultAggregateReviewConvergenceWindow,
		AgentBudgetThresholds:            defaultAgentBudgetThresholds(),
		PlannerRouting: PlannerRoutingConfig{
			Mode: "off", Arms: []PlannerRoutingArm{{Runtime: AgentPi, Probability: 0.5}, {Runtime: AgentClaude, Probability: 0.5}}, Floor: 0.1,
		},
	}
}

func cloneEnvDefaults(d EnvDefaults) EnvDefaults {
	d.MaxSlices = cloneEnvPointer(d.MaxSlices)
	d.Continue = cloneEnvPointer(d.Continue)
	d.PullRequest = cloneEnvPointer(d.PullRequest)
	d.ReviewEnabled = cloneEnvPointer(d.ReviewEnabled)
	d.SessionTimeout = cloneEnvPointer(d.SessionTimeout)
	d.AutoRework = cloneEnvPointer(d.AutoRework)
	d.MaxReworkAttempts = cloneEnvPointer(d.MaxReworkAttempts)
	d.ReworkEscalationFromAttempt = cloneEnvPointer(d.ReworkEscalationFromAttempt)
	d.SliceBudgetCaps.OutputTokens = cloneEnvPointer(d.SliceBudgetCaps.OutputTokens)
	d.SliceBudgetCaps.Cost = cloneEnvPointer(d.SliceBudgetCaps.Cost)
	d.PlannerRouting.Arms = slices.Clone(d.PlannerRouting.Arms)
	return d
}

func cloneEnvPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
