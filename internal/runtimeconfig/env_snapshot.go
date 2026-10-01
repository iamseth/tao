package runtimeconfig

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"

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
// mutate defaults; only theme/header failures are warning-only. Deprecated
// budget aliases follow their canonical rows in table order: a set alias is
// ignored when its canonical key is also set, otherwise it is applied and the
// canonical row reflects the value. Unset aliases produce no row.
func LoadEnv(lookup func(string) (string, bool)) EnvSnapshot {
	s := EnvSnapshot{
		defaults: builtinEnvDefaults(),
		rows:     make([]EnvVarStatus, 0, len(runtimeEnvVars)),
		failures: make(map[string]error),
	}
	builtins := s.defaults.RunOptionsPatch
	rowIndex := map[string]int{}
	overridden := map[string]bool{}
	for _, v := range runtimeEnvVars {
		row := EnvVarStatus{Name: v.name, Value: v.defaultValue(builtins), Source: "default"}
		var raw string
		var set bool
		if lookup != nil {
			raw, set = lookup(v.name)
		}
		if !v.hasOverride(raw, set) {
			if v.aliasOf != "" {
				continue
			}
			rowIndex[v.name] = len(s.rows)
			s.rows = append(s.rows, row)
			continue
		}
		overridden[v.name] = true
		if v.aliasOf != "" && overridden[v.aliasOf] {
			row.Value, row.Source = raw, "env"
			row.Warning = fmt.Sprintf("deprecated alias of %s; ignored because %s is set", v.aliasOf, v.aliasOf)
			rowIndex[v.name] = len(s.rows)
			s.rows = append(s.rows, row)
			continue
		}
		candidate := cloneEnvDefaults(s.defaults)
		value, err := v.apply(&candidate, raw)
		switch {
		case err == nil:
			s.defaults = candidate
			row.Value, row.Source = value, "env"
			if v.aliasOf != "" {
				row.Warning = fmt.Sprintf("deprecated; use %s", v.aliasOf)
				canonical := &s.rows[rowIndex[v.aliasOf]]
				canonical.Value, canonical.Source = value, "env"
				if v.name == EnvAutoRework {
					canonical.Value = fmt.Sprint(*candidate.MaxReworkAttempts)
				}
			}
		case v.fallbackOnInvalid:
			row.Warning = fmt.Sprintf("invalid env value %q: %v; using default", raw, err)
		default:
			failure := fmt.Errorf("%s: %w", v.name, err)
			s.failures[v.name] = failure
			row.Source = "invalid"
			row.Warning = fmt.Sprintf("invalid env value %q: %v; rejected", raw, failure)
			if v.aliasOf != "" {
				row.Warning += fmt.Sprintf("; deprecated; use %s", v.aliasOf)
			}
		}
		rowIndex[v.name] = len(s.rows)
		s.rows = append(s.rows, row)
	}
	s.rejectBudgetStopBelowWarn(rowIndex)
	return s
}

// rejectBudgetStopBelowWarn records the cross-field budget rule as a failure
// under the STOP key so it is rejected on consumption, never at load.
func (s *EnvSnapshot) rejectBudgetStopBelowWarn(rowIndex map[string]int) {
	err := s.defaults.Budget.Validate()
	if err == nil {
		return
	}
	errs := []error{err}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs = joined.Unwrap()
	}
	for _, err := range errs {
		var limit *plan.BudgetLimitError
		if !errors.As(err, &limit) {
			continue
		}
		stopKey, warnKey, ok := budgetLimitEnvKeys(limit.Scope, limit.Metric)
		if !ok {
			continue
		}
		failure := fmt.Errorf("%s: stop %s is below %s warn %s; stop must be at least warn", stopKey, formatBudgetNumber(limit.Stop), warnKey, formatBudgetNumber(limit.Warn))
		s.failures[stopKey] = failure
		if index, ok := rowIndex[stopKey]; ok {
			s.rows[index].Source = "invalid"
			s.rows[index].Warning = failure.Error()
		}
	}
}

// budgetLimitEnvKeys maps a validated scope and metric to its STOP and WARN
// keys. Only slice output tokens and cost register STOP rows.
func budgetLimitEnvKeys(scope, metric string) (stopKey, warnKey string, ok bool) {
	switch scope + "/" + metric {
	case "slice/output_tokens":
		return EnvBudgetSliceOutputTokensStop, EnvBudgetSliceOutputTokensWarn, true
	case "slice/cost":
		return EnvBudgetSliceCostStop, EnvBudgetSliceCostWarn, true
	}
	return "", "", false
}

func formatBudgetNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
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

// SessionWarnPercent validates only the warning percentage before projecting it.
// Zero disables warnings; a zero snapshot returns the built-in default.
func (s EnvSnapshot) SessionWarnPercent() (int, error) {
	if err := s.Require(EnvSessionWarnPercent); err != nil {
		return 0, err
	}
	return s.Defaults().SessionWarnPercent, nil
}

// Budget validates every budget key, canonical and alias, including the
// stop-not-below-warn rule, before projecting the typed budget. Provider,
// routing, and merge settings are unrelated.
func (s EnvSnapshot) Budget() (plan.AgentBudget, error) {
	if err := s.Require(BudgetEnvKeys()...); err != nil {
		return plan.AgentBudget{}, err
	}
	return s.Defaults().Budget, nil
}

// Status returns every captured row in table order. Source "invalid" denotes a
// rejected override, distinct from an accepted "default" or "env" value. A
// warning with source "default" is a presentation-only fallback. The single
// exception to complete-table output is deprecated budget aliases: an alias
// row appears only when the alias is set, and then always carries a warning.
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
		SessionWarnPercent:               DefaultSessionWarnPercent,
		AutoRework:                       &enabled,
		MaxReworkAttempts:                &attempts,
		MergeReviewMaxAttempts:           DefaultMergeReviewMaxAttempts,
		ReworkEscalationFromAttempt:      &escalation,
		UpdateMode:                       selfupdate.ModeWarn,
		Theme:                            theme.Default(),
		RunHeader:                        true,
		AggregateReviewConvergenceWindow: DefaultAggregateReviewConvergenceWindow,
		Budget:                           plan.DefaultAgentBudget(),
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
	d.Budget = d.Budget.Clone()
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
