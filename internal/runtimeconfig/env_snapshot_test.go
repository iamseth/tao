package runtimeconfig

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/selfupdate"
	"github.com/iamseth/tao/internal/theme"
)

func snapshotFrom(values map[string]string) EnvSnapshot {
	return LoadEnv(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
}

func TestSnapshotBudgetProjectionsRequireOnlyConsumedKeys(t *testing.T) {
	budgetKeys := BudgetEnvKeys()
	if len(budgetKeys) != 24 || !slices.Equal(budgetKeys[:12], []string{
		EnvBudgetSliceOutputTokensWarn, EnvBudgetSliceCostWarn, EnvBudgetSliceToolCallsWarn, EnvBudgetSliceAssistantMessagesWarn, EnvBudgetSliceErroredMessagesWarn,
		EnvBudgetPlanOutputTokensWarn, EnvBudgetPlanCostWarn, EnvBudgetPlanToolCallsWarn, EnvBudgetPlanAssistantMessagesWarn, EnvBudgetPlanErroredMessagesWarn,
		EnvBudgetSliceOutputTokensStop, EnvBudgetSliceCostStop,
	}) || !slices.Equal(budgetKeys[12:], []string{
		EnvBudgetSliceOutputTokensDeprecated, EnvBudgetSliceCostDeprecated, EnvBudgetSliceToolCallsDeprecated, EnvBudgetSliceAssistantMessagesDeprecated, EnvBudgetSliceErroredMessagesDeprecated,
		EnvBudgetPlanOutputTokensDeprecated, EnvBudgetPlanCostDeprecated, EnvBudgetPlanToolCallsDeprecated, EnvBudgetPlanAssistantMessagesDeprecated, EnvBudgetPlanErroredMessagesDeprecated,
		EnvMaxSliceOutputTokensDeprecated, EnvMaxSliceCostDeprecated,
	}) {
		t.Fatalf("budget keys: %v", budgetKeys)
	}
	for _, key := range RuntimeEnvKeys() {
		t.Run(key, func(t *testing.T) {
			// Use each row's retained failure to distinguish presentation fallback
			// and opaque string settings from genuinely rejected configuration.
			snapshot := snapshotFrom(map[string]string{key: "invalid value"})
			_, budgetErr := snapshot.Budget()
			invalid := snapshot.Require(key) != nil
			budget := slices.Contains(budgetKeys, key)
			if (budgetErr != nil) != (invalid && budget) {
				t.Fatalf("budget admission for %s: %v", key, budgetErr)
			}
		})
	}
	snapshot := snapshotFrom(map[string]string{EnvBudgetSliceCostStop: "0", EnvBudgetSliceOutputTokensStop: "0", EnvBudgetPlanCostWarn: "0", EnvBudgetSliceCostWarn: "0", EnvBudgetSliceOutputTokensWarn: "0"})
	budget, err := snapshot.Budget()
	if err != nil || budget.Slice.Cost.Stop == nil || *budget.Slice.Cost.Stop != 0 || budget.Slice.OutputTokens.Stop == nil || *budget.Slice.OutputTokens.Stop != 0 || budget.Plan.Cost.Warn != 0 {
		t.Fatalf("zero budget lost: %+v, %v", budget, err)
	}
	*budget.Slice.Cost.Stop = 100
	captured, err := snapshot.Budget()
	if err != nil || *captured.Slice.Cost.Stop != 0 || *snapshot.Defaults().Budget.Slice.Cost.Stop != 0 {
		t.Fatalf("projection mutated snapshot: %+v, %v", captured, err)
	}
	if thresholds := budget.Warn(); thresholds.Plan.Cost != 0 || thresholds.Slice.Cost != 0 || thresholds.Slice.OutputTokens != 0 {
		t.Fatalf("warn projection disagrees: %+v", thresholds)
	}
	defaults, err := (EnvSnapshot{}).Budget()
	if err != nil || !reflect.DeepEqual(defaults, plan.DefaultAgentBudget()) {
		t.Fatalf("built-in budget: %+v, %v", defaults, err)
	}
}

func TestEnvSnapshotBudgetCanonicalValues(t *testing.T) {
	s := snapshotFrom(map[string]string{
		EnvBudgetSliceOutputTokensWarn: "101", EnvBudgetSliceCostWarn: "2.5", EnvBudgetSliceToolCallsWarn: "102", EnvBudgetSliceAssistantMessagesWarn: "103", EnvBudgetSliceErroredMessagesWarn: "104",
		EnvBudgetPlanOutputTokensWarn: "201", EnvBudgetPlanCostWarn: "3.5", EnvBudgetPlanToolCallsWarn: "202", EnvBudgetPlanAssistantMessagesWarn: "203", EnvBudgetPlanErroredMessagesWarn: "204",
		EnvBudgetSliceOutputTokensStop: "1000", EnvBudgetSliceCostStop: "9.5",
	})
	budget, err := s.Budget()
	if err != nil {
		t.Fatal(err)
	}
	tokens, cost := int64(1000), 9.5
	want := plan.AgentBudget{
		Slice: plan.AgentScopeBudget{
			OutputTokens: plan.BudgetLimit[int64]{Warn: 101, Stop: &tokens}, Cost: plan.BudgetLimit[float64]{Warn: 2.5, Stop: &cost},
			ToolCalls: plan.BudgetLimit[int64]{Warn: 102}, AssistantMessages: plan.BudgetLimit[int64]{Warn: 103}, ErroredMessages: plan.BudgetLimit[int64]{Warn: 104},
		},
		Plan: plan.AgentScopeBudget{
			OutputTokens: plan.BudgetLimit[int64]{Warn: 201}, Cost: plan.BudgetLimit[float64]{Warn: 3.5},
			ToolCalls: plan.BudgetLimit[int64]{Warn: 202}, AssistantMessages: plan.BudgetLimit[int64]{Warn: 203}, ErroredMessages: plan.BudgetLimit[int64]{Warn: 204},
		},
	}
	if !reflect.DeepEqual(budget, want) {
		t.Fatalf("budget = %+v, want %+v", budget, want)
	}
	for _, row := range s.Status() {
		if slices.Contains(BudgetEnvKeys()[12:], row.Name) {
			t.Fatalf("unset alias surfaced: %+v", row)
		}
		if slices.Contains(BudgetEnvKeys()[:12], row.Name) && (row.Source != "env" || row.Warning != "") {
			t.Fatalf("canonical row: %+v", row)
		}
	}
	for _, key := range []string{EnvBudgetSliceOutputTokensStop, EnvBudgetSliceCostStop} {
		row := snapshotFrom(nil).Status()[slices.Index(RuntimeEnvKeys(), key)]
		if row.Name != key || row.Value != "disabled" || row.Source != "default" {
			t.Fatalf("stop default row: %+v", row)
		}
	}
	first, second := s.Defaults(), s.Defaults()
	if first.Budget.Slice.Cost.Stop == second.Budget.Slice.Cost.Stop || first.Budget.Slice.OutputTokens.Stop == second.Budget.Slice.OutputTokens.Stop {
		t.Fatal("Defaults clones share stop pointers")
	}
	*first.Budget.Slice.Cost.Stop = 1
	if *s.Defaults().Budget.Slice.Cost.Stop != 9.5 {
		t.Fatal("clone mutation reached snapshot")
	}
}

func TestEnvSnapshotBudgetAliases(t *testing.T) {
	aliases := map[string]string{
		EnvBudgetSliceOutputTokensDeprecated: EnvBudgetSliceOutputTokensWarn, EnvBudgetSliceCostDeprecated: EnvBudgetSliceCostWarn,
		EnvBudgetSliceToolCallsDeprecated: EnvBudgetSliceToolCallsWarn, EnvBudgetSliceAssistantMessagesDeprecated: EnvBudgetSliceAssistantMessagesWarn,
		EnvBudgetSliceErroredMessagesDeprecated: EnvBudgetSliceErroredMessagesWarn, EnvBudgetPlanOutputTokensDeprecated: EnvBudgetPlanOutputTokensWarn,
		EnvBudgetPlanCostDeprecated: EnvBudgetPlanCostWarn, EnvBudgetPlanToolCallsDeprecated: EnvBudgetPlanToolCallsWarn,
		EnvBudgetPlanAssistantMessagesDeprecated: EnvBudgetPlanAssistantMessagesWarn, EnvBudgetPlanErroredMessagesDeprecated: EnvBudgetPlanErroredMessagesWarn,
		EnvMaxSliceOutputTokensDeprecated: EnvBudgetSliceOutputTokensStop, EnvMaxSliceCostDeprecated: EnvBudgetSliceCostStop,
	}
	rowByName := func(rows []EnvVarStatus, name string) (EnvVarStatus, bool) {
		for _, row := range rows {
			if row.Name == name {
				return row, true
			}
		}
		return EnvVarStatus{}, false
	}
	for alias, canonical := range aliases {
		t.Run(alias, func(t *testing.T) {
			// Alias only: the value lands under the canonical key with a warning.
			// The value exceeds every built-in warn so STOP aliases stay valid.
			s := snapshotFrom(map[string]string{alias: "70000"})
			if err := s.Require(alias, canonical); err != nil {
				t.Fatal(err)
			}
			rows := s.Status()
			if len(rows) != len(RuntimeEnvKeys())-11 {
				t.Fatalf("rows=%d; unset aliases must be absent", len(rows))
			}
			aliasRow, ok := rowByName(rows, alias)
			if !ok || aliasRow.Value != "70000" || aliasRow.Source != "env" || aliasRow.Warning != "deprecated; use "+canonical {
				t.Fatalf("alias row: %+v", aliasRow)
			}
			canonicalRow, _ := rowByName(rows, canonical)
			if canonicalRow.Value != "70000" || canonicalRow.Source != "env" || canonicalRow.Warning != "" {
				t.Fatalf("canonical row: %+v", canonicalRow)
			}
			if !reflect.DeepEqual(s.Defaults().Budget, snapshotFrom(map[string]string{canonical: "70000"}).Defaults().Budget) {
				t.Fatalf("alias value not applied: %+v", s.Defaults().Budget)
			}
			// Alias plus canonical: the canonical value wins and the alias row warns.
			s = snapshotFrom(map[string]string{alias: "bad", canonical: "90000"})
			if err := s.Require(alias, canonical); err != nil {
				t.Fatal(err)
			}
			rows = s.Status()
			aliasRow, _ = rowByName(rows, alias)
			if aliasRow.Value != "bad" || aliasRow.Source != "env" || aliasRow.Warning != "deprecated alias of "+canonical+"; ignored because "+canonical+" is set" {
				t.Fatalf("ignored alias row: %+v", aliasRow)
			}
			canonicalRow, _ = rowByName(rows, canonical)
			if canonicalRow.Value != "90000" || canonicalRow.Source != "env" || canonicalRow.Warning != "" {
				t.Fatalf("canonical row with alias: %+v", canonicalRow)
			}
			if !reflect.DeepEqual(s.Defaults().Budget, snapshotFrom(map[string]string{canonical: "90000"}).Defaults().Budget) {
				t.Fatalf("alias overrode canonical: %+v", s.Defaults().Budget)
			}
			// Invalid alias is rejected on consumption under its own name.
			s = snapshotFrom(map[string]string{alias: "bad"})
			if _, err := s.Budget(); err == nil || !strings.Contains(err.Error(), alias) {
				t.Fatalf("invalid alias admitted: %v", err)
			}
			aliasRow, _ = rowByName(s.Status(), alias)
			if aliasRow.Source != "invalid" || aliasRow.Warning == "" {
				t.Fatalf("invalid alias row: %+v", aliasRow)
			}
			if !reflect.DeepEqual(s.Defaults().Budget, plan.DefaultAgentBudget()) {
				t.Fatal("invalid alias mutated defaults")
			}
		})
	}
	// Unset aliases are looked up but never appear in Status.
	rows := snapshotFrom(nil).Status()
	for alias := range aliases {
		if _, ok := rowByName(rows, alias); ok {
			t.Fatalf("unset alias %s present in status", alias)
		}
	}
	if len(rows) != len(RuntimeEnvKeys())-len(aliases) {
		t.Fatalf("rows=%d keys=%d aliases=%d", len(rows), len(RuntimeEnvKeys()), len(aliases))
	}
}

func TestEnvSnapshotBudgetStopBelowWarn(t *testing.T) {
	for _, tc := range []struct{ stop, warn, stopValue, warnValue string }{
		{EnvBudgetSliceCostStop, EnvBudgetSliceCostWarn, "1", "2.5"},
		{EnvBudgetSliceOutputTokensStop, EnvBudgetSliceOutputTokensWarn, "10", "20"},
		{EnvMaxSliceCostDeprecated, EnvBudgetSliceCostWarn, "1", "2.5"},
		{EnvBudgetSliceCostStop, EnvBudgetSliceCostDeprecated, "1", "2.5"},
	} {
		t.Run(tc.stop+"<"+tc.warn, func(t *testing.T) {
			s := snapshotFrom(map[string]string{tc.stop: tc.stopValue, tc.warn: tc.warnValue, EnvTheme: "gruvbox"})
			canonicalStop, canonicalWarn := tc.stop, tc.warn
			for _, v := range runtimeEnvVars {
				if v.name == tc.stop && v.aliasOf != "" {
					canonicalStop = v.aliasOf
				}
				if v.name == tc.warn && v.aliasOf != "" {
					canonicalWarn = v.aliasOf
				}
			}
			_, err := s.Budget()
			if err == nil || !strings.Contains(err.Error(), canonicalStop) || !strings.Contains(err.Error(), canonicalWarn) || !strings.Contains(err.Error(), tc.stopValue) || !strings.Contains(err.Error(), tc.warnValue) {
				t.Fatalf("stop below warn admitted: %v", err)
			}
			if s.Require(canonicalStop) == nil || s.Require(canonicalWarn) != nil || s.Require(EnvTheme) != nil {
				t.Fatalf("failure scope: stop=%v warn=%v theme=%v", s.Require(canonicalStop), s.Require(canonicalWarn), s.Require(EnvTheme))
			}
			if s.Defaults().Theme.Name() != "gruvbox" {
				t.Fatal("unrelated setting lost")
			}
			row := s.Status()[slices.Index(RuntimeEnvKeys(), canonicalStop)]
			if row.Name != canonicalStop || row.Source != "invalid" || row.Warning != s.Require(canonicalStop).Error() {
				t.Fatalf("stop row: %+v", row)
			}
		})
	}
	s := snapshotFrom(map[string]string{EnvBudgetSliceCostStop: "2.5", EnvBudgetSliceCostWarn: "2.5", EnvBudgetSliceOutputTokensStop: "20", EnvBudgetSliceOutputTokensWarn: "20"})
	if _, err := s.Budget(); err != nil {
		t.Fatalf("stop equal to warn rejected: %v", err)
	}
	if s.Require(RuntimeEnvKeys()...) != nil {
		t.Fatal("stop equal to warn recorded a failure")
	}
}

func TestEnvSnapshotCompleteTableWrites(t *testing.T) {
	values := map[string]string{
		EnvCommitPolicy: "none", EnvExecutionMode: "current", EnvAgent: "claude", EnvSessionTimeout: "3m0s",
		EnvModel: "base", EnvRunModel: "run", EnvReviewModel: "review", EnvMergeReviewModel: "merge", EnvResolverModel: "resolve", EnvReworkEscalationModel: "strong",
		EnvUpdate: "off", EnvPullRequest: "true", EnvReview: "false", EnvAutoRework: "false", EnvMaxReworkAttempts: "7", EnvReworkEscalationFromAttempt: "6", EnvSkipPermissions: "true",
		EnvMergeVerifyCommand: "go test ./...", EnvAggregateReviewConvergenceWindow: "5", EnvApprovedBy: "bot", EnvRunHeader: "false", EnvTheme: "gruvbox",
		EnvPlannerRouting: "shadow", EnvPlannerRoutingArms: "pi=0.5,claude=0.5", EnvPlannerRoutingFloor: "0.2",
		EnvBudgetSliceOutputTokensWarn: "101", EnvBudgetSliceCostWarn: "2.5", EnvBudgetSliceToolCallsWarn: "102", EnvBudgetSliceAssistantMessagesWarn: "103", EnvBudgetSliceErroredMessagesWarn: "104",
		EnvBudgetPlanOutputTokensWarn: "201", EnvBudgetPlanCostWarn: "3.5", EnvBudgetPlanToolCallsWarn: "202", EnvBudgetPlanAssistantMessagesWarn: "203", EnvBudgetPlanErroredMessagesWarn: "204",
		EnvBudgetSliceOutputTokensStop: "1000", EnvBudgetSliceCostStop: "9.5",
	}
	// Every alias is set too; each is ignored in favor of its canonical key.
	aliases := BudgetEnvKeys()[12:]
	for _, alias := range aliases {
		values[alias] = "1"
	}
	s := snapshotFrom(values)
	if err := s.Require(RuntimeEnvKeys()...); err != nil {
		t.Fatal(err)
	}
	rows := s.Status()
	if len(rows) != len(values) || len(rows) != len(RuntimeEnvKeys()) {
		t.Fatalf("rows=%d values=%d; update complete-table coverage", len(rows), len(values))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		value, ok := values[row.Name]
		alias := slices.Contains(aliases, row.Name)
		if !ok || seen[row.Name] || row.Value != value || row.Source != "env" || (row.Warning != "") != alias || (alias && !strings.Contains(row.Warning, "ignored because")) {
			t.Errorf("unexpected row: %+v", row)
		}
		seen[row.Name] = true
	}
	yes, no, attempts, escalation, timeout := true, false, 7, 6, 3*time.Minute
	tokens, cost := int64(1000), 9.5
	selected, _ := theme.Lookup("gruvbox")
	want := EnvDefaults{
		RunOptionsPatch: RunOptionsPatch{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentClaude, PullRequest: &yes, ReviewEnabled: &no, SessionTimeout: &timeout, Model: "base", RunModel: "run", ReviewModel: "review", MergeReviewModel: "merge", ResolverModel: "resolve", ReworkEscalationModel: "strong"},
		AutoRework:      &no, MaxReworkAttempts: &attempts, ReworkEscalationFromAttempt: &escalation, UpdateMode: selfupdate.ModeOff, Theme: selected, SkipPermissions: true,
		MergeVerifyCommand: "go test ./...", MergeVerifyCommandSet: true, AggregateReviewConvergenceWindow: 5, ApprovedBy: "bot", RunHeader: false,
		PlannerRouting: PlannerRoutingConfig{Mode: "shadow", Arms: []PlannerRoutingArm{{AgentPi, 0.5}, {AgentClaude, 0.5}}, Floor: 0.2, ModeSet: true, ArmsSet: true, FloorSet: true},
		Budget: plan.AgentBudget{
			Slice: plan.AgentScopeBudget{
				OutputTokens: plan.BudgetLimit[int64]{Warn: 101, Stop: &tokens}, Cost: plan.BudgetLimit[float64]{Warn: 2.5, Stop: &cost},
				ToolCalls: plan.BudgetLimit[int64]{Warn: 102}, AssistantMessages: plan.BudgetLimit[int64]{Warn: 103}, ErroredMessages: plan.BudgetLimit[int64]{Warn: 104},
			},
			Plan: plan.AgentScopeBudget{
				OutputTokens: plan.BudgetLimit[int64]{Warn: 201}, Cost: plan.BudgetLimit[float64]{Warn: 3.5},
				ToolCalls: plan.BudgetLimit[int64]{Warn: 202}, AssistantMessages: plan.BudgetLimit[int64]{Warn: 203}, ErroredMessages: plan.BudgetLimit[int64]{Warn: 204},
			},
		},
	}
	if got := s.Defaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestEnvSnapshotBuiltinsAndEmptyRules(t *testing.T) {
	for _, key := range RuntimeEnvKeys() {
		unsetEnv(t, key)
	}
	s := LoadEnv(nil)
	var zero EnvSnapshot
	if !reflect.DeepEqual(zero.Defaults(), s.Defaults()) || !reflect.DeepEqual(zero.Status(), s.Status()) || zero.Require(RuntimeEnvKeys()...) != nil {
		t.Fatal("zero snapshot does not project built-ins")
	}
	d := s.Defaults()
	if d.CommitPolicy != CommitPolicySlice || d.ExecutionMode != ExecutionModeIsolated || d.Agent != AgentPi || d.SessionTimeoutValue() != 20*time.Minute || d.PullRequestValue() || !d.ReviewEnabledValue() || d.AutoRework == nil || !*d.AutoRework || d.MaxReworkAttempts == nil || *d.MaxReworkAttempts != 5 || d.ReworkEscalationFromAttemptValue() != 4 || d.UpdateMode != selfupdate.ModeWarn || d.Theme != theme.Default() || !d.RunHeader || d.AggregateReviewConvergenceWindow != 2 || d.SkipPermissions || d.MergeVerifyCommandSet || d.MergeVerifyCommand != "" || d.ApprovedBy != "" || !reflect.DeepEqual(d.Budget, plan.DefaultAgentBudget()) {
		t.Fatalf("built-ins = %+v", d)
	}
	for _, v := range runtimeEnvVars {
		t.Run(v.name, func(t *testing.T) {
			unset := snapshotFrom(nil)
			empty := snapshotFrom(map[string]string{v.name: ""})
			switch {
			case v.applyWhenEmpty && v.name != EnvMergeVerifyCommand:
				if empty.Require(v.name) == nil || unset.Require(v.name) != nil {
					t.Fatal("models must reject set-empty only")
				}
			case v.name == EnvMergeVerifyCommand:
				if !empty.Defaults().MergeVerifyCommandSet || unset.Defaults().MergeVerifyCommandSet {
					t.Fatal("lost set-empty command")
				}
			default:
				if !reflect.DeepEqual(empty.Defaults(), unset.Defaults()) || !reflect.DeepEqual(empty.Status(), unset.Status()) {
					t.Fatal("empty must retain default")
				}
			}
		})
	}
}

func TestEnvSnapshotAggregateWindowBlankRetainsDefault(t *testing.T) {
	s := snapshotFrom(map[string]string{EnvAggregateReviewConvergenceWindow: " \t "})
	if err := s.Require(EnvAggregateReviewConvergenceWindow); err != nil {
		t.Fatal(err)
	}
	if s.Defaults().AggregateReviewConvergenceWindow != DefaultAggregateReviewConvergenceWindow {
		t.Fatal("blank convergence window lost its built-in default")
	}
	row := s.Status()[slices.Index(RuntimeEnvKeys(), EnvAggregateReviewConvergenceWindow)]
	if row.Source != "default" || row.Warning != "" {
		t.Fatalf("blank convergence window: %+v", row)
	}
}

func TestEnvSnapshotBooleanGrammar(t *testing.T) {
	for _, key := range []string{EnvPullRequest, EnvReview, EnvAutoRework, EnvSkipPermissions, EnvRunHeader} {
		for _, group := range []struct {
			values []string
			want   string
		}{
			{[]string{"true", "1", "yes", "on", "t", "y"}, "true"}, {[]string{"false", "0", "no", "off", "f", "n"}, "false"},
		} {
			for _, value := range group.values {
				raw := " \t" + strings.ToUpper(value) + " \n"
				s := snapshotFrom(map[string]string{key: raw})
				if err := s.Require(key); err != nil {
					t.Fatal(err)
				}
				row := s.Status()[slices.Index(RuntimeEnvKeys(), key)]
				if row.Value != group.want || row.Source != "env" || row.Warning != "" {
					t.Fatalf("%s=%q: %+v", key, raw, row)
				}
				parsed, err := ParseEnvBool(raw)
				if err != nil || parsed != (group.want == "true") {
					t.Fatalf("ParseEnvBool(%q)=%t,%v", raw, parsed, err)
				}
			}
		}
	}
	for _, raw := range []string{"", " ", "00", "enabled", "maybe"} {
		if _, err := ParseEnvBool(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestEnvSnapshotFailuresAreConsumptionScoped(t *testing.T) {
	invalid := map[string]string{
		EnvCommitPolicy: "plan", EnvExecutionMode: "sandbox", EnvAgent: "robot", EnvSessionTimeout: "soon",
		EnvModel: "", EnvRunModel: "two models", EnvReviewModel: "\t", EnvMergeReviewModel: "a\nb", EnvResolverModel: "a\u00a0b", EnvReworkEscalationModel: "a\tb",
		EnvUpdate: "sometimes", EnvPullRequest: "maybe", EnvReview: "maybe", EnvAutoRework: "maybe", EnvSkipPermissions: "maybe",
		EnvMaxReworkAttempts: "-1", EnvReworkEscalationFromAttempt: "0", EnvAggregateReviewConvergenceWindow: "1", EnvMaxSliceOutputTokensDeprecated: "-1", EnvMaxSliceCostDeprecated: "NaN",
		EnvTheme: "unknown", EnvRunHeader: "unknown",
	}
	for _, v := range runtimeEnvVars {
		if strings.HasPrefix(v.name, "TAO_BUDGET_") {
			invalid[v.name] = "-1"
		}
	}
	// Every alias is set (invalidly), so every table row is present.
	s := snapshotFrom(invalid)
	if !reflect.DeepEqual(s.Defaults(), LoadEnv(nil).Defaults()) {
		t.Fatalf("invalid fields mutated defaults: %+v", s.Defaults())
	}
	if err := s.Require(EnvApprovedBy, EnvMergeVerifyCommand, EnvTheme, EnvRunHeader); err != nil {
		t.Fatalf("unused failures blocked valid settings: %v", err)
	}
	if s.Require() != nil {
		t.Fatal("empty requirement rejected")
	}
	rows := s.Status()
	if len(rows) != len(RuntimeEnvKeys()) {
		t.Fatal("lost diagnostic rows")
	}
	for _, row := range rows {
		if _, bad := invalid[row.Name]; !bad {
			continue
		}
		err := s.Require(row.Name)
		presentation := row.Name == EnvTheme || row.Name == EnvRunHeader
		if row.Warning == "" {
			t.Errorf("missing warning: %+v", row)
		}
		if slices.Contains(BudgetEnvKeys()[12:], row.Name) {
			// A set alias yields to its set canonical key: warned, never rejected.
			if err != nil || row.Source != "env" || !strings.Contains(row.Warning, "ignored because") {
				t.Errorf("ignored alias: %+v, %v", row, err)
			}
			continue
		}
		if presentation {
			if err != nil || row.Source != "default" || !strings.Contains(row.Warning, "using default") {
				t.Errorf("presentation: %+v, %v", row, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), row.Name) || row.Source != "invalid" {
			t.Errorf("rejection: %+v, %v", row, err)
		}
	}
	err := s.Require(EnvAgent, EnvBudgetSliceCostStop)
	if err == nil || !strings.Contains(err.Error(), EnvAgent) || !strings.Contains(err.Error(), EnvBudgetSliceCostStop) || strings.Contains(err.Error(), EnvUpdate) {
		t.Fatalf("mixed requirements: %v", err)
	}
	for _, v := range runtimeEnvVars {
		if v.fallbackOnInvalid != (v.name == EnvTheme || v.name == EnvRunHeader) {
			t.Errorf("unexpected warning-only metadata: %s", v.name)
		}
	}
}

func TestEnvSnapshotDiagnosticsDescribeAcceptedValues(t *testing.T) {
	for _, tc := range []struct{ key, raw, want string }{
		{EnvCommitPolicy, "bad", "slice or none"}, {EnvExecutionMode, "bad", "isolated or current"}, {EnvAgent, "bad", "pi or claude"},
		{EnvSessionTimeout, "soon", "positive duration"}, {EnvSessionTimeout, "-1s", "positive duration"}, {EnvModel, "", "not be empty"}, {EnvModel, "two models", "whitespace"},
		{EnvPullRequest, "bad", "true/false, 1/0, yes/no, on/off, t/f, y/n"}, {EnvMaxReworkAttempts, "-1", "non-negative integer"}, {EnvReworkEscalationFromAttempt, "0", "at least 1"}, {EnvAggregateReviewConvergenceWindow, "1", "at least 2"},
		{EnvBudgetSliceCostStop, "Inf", "non-negative decimal"}, {EnvMaxSliceCostDeprecated, "Inf", "non-negative decimal"}, {EnvBudgetPlanCostWarn, "NaN", "non-negative decimal"}, {EnvBudgetPlanCostDeprecated, "NaN", "non-negative decimal"}, {EnvBudgetSliceToolCallsWarn, "1.5", "non-negative integer"}, {EnvUpdate, "bad", "warn, auto, or off"},
	} {
		err := snapshotFrom(map[string]string{tc.key: tc.raw}).Require(tc.key)
		if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s=%q: %v; want %q", tc.key, tc.raw, err, tc.want)
		}
	}
}

func TestEnvSnapshotTypedRouting(t *testing.T) {
	builtins := PlannerRoutingConfig{Mode: "off", Arms: []PlannerRoutingArm{{AgentPi, 0.5}, {AgentClaude, 0.5}}, Floor: 0.1}
	for _, raw := range []string{"", " \t\n"} {
		s := snapshotFrom(map[string]string{EnvPlannerRouting: raw, EnvPlannerRoutingArms: raw, EnvPlannerRoutingFloor: raw})
		if !reflect.DeepEqual(s.Defaults().PlannerRouting, builtins) || !reflect.DeepEqual(s.Status(), LoadEnv(nil).Status()) {
			t.Fatalf("blank routing lost built-ins: %+v", s.Defaults().PlannerRouting)
		}
	}
	for _, mode := range []string{"off", "shadow", "randomized"} {
		s := snapshotFrom(map[string]string{EnvPlannerRouting: " " + mode + " ", EnvPlannerRoutingArms: " pi=1 ", EnvPlannerRoutingFloor: " 0 "})
		want := PlannerRoutingConfig{Mode: PlannerRoutingMode(mode), Arms: []PlannerRoutingArm{{AgentPi, 1}}, Floor: 0, ModeSet: true, ArmsSet: true, FloorSet: true}
		if err := s.Require(EnvPlannerRouting, EnvPlannerRoutingArms, EnvPlannerRoutingFloor); err != nil || !reflect.DeepEqual(s.Defaults().PlannerRouting, want) {
			t.Fatalf("routing: %+v, %v", s.Defaults().PlannerRouting, err)
		}
		for key, value := range map[string]string{EnvPlannerRouting: mode, EnvPlannerRoutingArms: "pi=1", EnvPlannerRoutingFloor: "0"} {
			row := s.Status()[slices.Index(RuntimeEnvKeys(), key)]
			if row.Value != value || row.Source != "env" || row.Warning != "" {
				t.Fatalf("routing row disagrees: %+v", row)
			}
		}
	}
}

func TestEnvSnapshotRoutingDoesNotEnforcePolicy(t *testing.T) {
	// Syntax permits a zero-weight arm even when randomized policy's floor
	// would reject it; that cross-field decision belongs to plannerroute.
	s := snapshotFrom(map[string]string{EnvPlannerRouting: "randomized", EnvPlannerRoutingArms: "pi=0,claude=1", EnvPlannerRoutingFloor: "0.1"})
	if err := s.Require(EnvPlannerRouting, EnvPlannerRoutingArms, EnvPlannerRoutingFloor); err != nil {
		t.Fatalf("snapshot enforced routing policy: %v", err)
	}
	if got := s.Defaults().PlannerRouting; got.Arms[0].Probability != 0 || got.Floor != 0.1 {
		t.Fatalf("snapshot rewrote routing policy: %+v", got)
	}
}

func TestEnvSnapshotRoutingFailuresAreConsumptionScoped(t *testing.T) {
	for _, mode := range []string{"off", "invalid"} {
		s := snapshotFrom(map[string]string{EnvPlannerRouting: mode, EnvPlannerRoutingArms: "pi=NaN", EnvPlannerRoutingFloor: "Inf"})
		if err := s.Require(EnvPlannerRouting); (err == nil) != (mode == "off") {
			t.Fatalf("mode requirement: %v", err)
		}
		for _, key := range []string{EnvPlannerRouting, EnvPlannerRoutingArms, EnvPlannerRoutingFloor} {
			if key == EnvPlannerRouting && mode == "off" {
				continue
			}
			err := s.Require(key)
			row := s.Status()[slices.Index(RuntimeEnvKeys(), key)]
			if err == nil || !strings.Contains(err.Error(), key) || row.Source != "invalid" || row.Warning == "" {
				t.Fatalf("missing failure: %+v, %v", row, err)
			}
		}
		got, want := s.Defaults().PlannerRouting, LoadEnv(nil).Defaults().PlannerRouting
		want.ModeSet = mode == "off"
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("invalid settings mutated defaults: %+v", got)
		}
	}
}

func TestEnvSnapshotZeroAndRoutingPresence(t *testing.T) {
	values := map[string]string{EnvSessionTimeout: "0", EnvMaxReworkAttempts: "0", EnvPlannerRouting: " \t", EnvPlannerRoutingArms: " pi=1 ", EnvPlannerRoutingFloor: " 0.2 "}
	for _, key := range BudgetEnvKeys()[:12] {
		values[key] = "0"
	}
	s := snapshotFrom(values)
	if err := s.Require(RuntimeEnvKeys()...); err != nil {
		t.Fatal(err)
	}
	d := s.Defaults()
	zero := int64(0)
	zeroCost := 0.0
	wantBudget := plan.AgentBudget{Slice: plan.AgentScopeBudget{OutputTokens: plan.BudgetLimit[int64]{Stop: &zero}, Cost: plan.BudgetLimit[float64]{Stop: &zeroCost}}}
	if d.SessionTimeout == nil || *d.SessionTimeout != 0 || d.MaxReworkAttempts == nil || *d.MaxReworkAttempts != 0 || !reflect.DeepEqual(d.Budget, wantBudget) {
		t.Fatalf("lost explicit zero: %+v", d)
	}
	wantRouting := PlannerRoutingConfig{Mode: "off", Arms: []PlannerRoutingArm{{AgentPi, 1}}, Floor: 0.2, ArmsSet: true, FloorSet: true}
	if !reflect.DeepEqual(d.PlannerRouting, wantRouting) {
		t.Fatalf("routing: %+v", d.PlannerRouting)
	}
}

func TestEnvSnapshotLookupOnceAndDefensiveCopies(t *testing.T) {
	values := map[string]string{EnvPullRequest: "true", EnvReview: "false", EnvAutoRework: "false", EnvMaxReworkAttempts: "7", EnvReworkEscalationFromAttempt: "6", EnvBudgetSliceOutputTokensStop: "100000", EnvBudgetSliceCostStop: "10.5", EnvAgent: "bad"}
	counts := map[string]int{}
	s := LoadEnv(func(key string) (string, bool) { counts[key]++; value, ok := values[key]; return value, ok })
	before, rows, failure := s.Defaults(), s.Status(), s.Require(EnvAgent).Error()
	values[EnvAgent] = "claude"
	values[EnvMaxReworkAttempts] = "99"
	for range 2 {
		d := s.Defaults()
		*d.PullRequest = false
		*d.ReviewEnabled = true
		*d.AutoRework = true
		*d.MaxReworkAttempts = 999
		*d.ReworkEscalationFromAttempt = 999
		*d.SessionTimeout = 0
		*d.Budget.Slice.OutputTokens.Stop = 999
		*d.Budget.Slice.Cost.Stop = 999
		d.PlannerRouting.Arms[0].Probability = 999
		changed := s.Status()
		changed[0].Value = "changed"
		if !reflect.DeepEqual(s.Defaults(), before) || !reflect.DeepEqual(s.Status(), rows) || s.Require(EnvAgent).Error() != failure {
			t.Fatal("snapshot changed")
		}
	}
	if len(counts) != len(RuntimeEnvKeys()) {
		t.Fatalf("lookups: %v", counts)
	}
	for _, key := range RuntimeEnvKeys() {
		if counts[key] != 1 {
			t.Errorf("%s read %d times", key, counts[key])
		}
	}
	if fresh := snapshotFrom(values); fresh.Require(EnvAgent) != nil || *fresh.Defaults().MaxReworkAttempts != 99 {
		t.Fatal("new invocation did not observe changes")
	}
}

func TestEnvSnapshotMixedSuccessAndFailure(t *testing.T) {
	s := snapshotFrom(map[string]string{
		EnvAgent: "invalid", EnvUpdate: "invalid", EnvTheme: "invalid",
		EnvRunModel: " provider/run ", EnvApprovedBy: "bot", EnvBudgetPlanCostWarn: "0",
	})
	d := s.Defaults()
	if d.Agent != AgentPi || d.UpdateMode != selfupdate.ModeWarn || d.Theme != theme.Default() || d.RunModel != "provider/run" || d.ApprovedBy != "bot" || d.Budget.Plan.Cost.Warn != 0 {
		t.Fatalf("mixed defaults: %+v", d)
	}
	if s.Require(EnvRunModel, EnvApprovedBy, EnvBudgetPlanCostWarn, EnvTheme) != nil || s.Require(EnvAgent, EnvUpdate) == nil {
		t.Fatal("failures escaped their consumption scope")
	}
}

func TestRuntimeEnvDoesNotCache(t *testing.T) {
	t.Setenv(EnvApprovedBy, "first")
	first := RuntimeEnv()
	t.Setenv(EnvApprovedBy, "second")
	if first.Defaults().ApprovedBy != "first" || RuntimeEnv().Defaults().ApprovedBy != "second" {
		t.Fatal("production binding cached across invocations")
	}
}
