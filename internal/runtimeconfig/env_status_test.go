package runtimeconfig

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/configtypes"
)

func TestRuntimeEnvDisplayGroups(t *testing.T) {
	members := map[string][]string{
		"execution":     {EnvCommitPolicy, EnvExecutionMode, EnvAgent, EnvReviewAgent, EnvSessionTimeout, EnvSessionWarnPercent, EnvModel, EnvRunModel, EnvReviewModel, EnvMergeReviewModel, EnvResolverModel, EnvReworkEscalationModel, EnvEffort, EnvRunEffort, EnvReviewEffort, EnvMergeReviewEffort, EnvResolverEffort},
		"workflow":      {EnvPullRequest, EnvReview, EnvAutoRework, EnvMaxReworkAttempts, EnvReworkEscalationFromAttempt, EnvPlannerRouting, EnvPlannerRoutingArms, EnvPlannerRoutingFloor},
		"safety-update": {EnvUpdate, EnvSkipPermissions},
		"other":         {EnvMergeVerifyCommand, EnvMergeReviewMaxAttempts, EnvAggregateReviewConvergenceWindow, EnvApprovedBy, EnvRunHeader, EnvTheme},
		"budgets":       BudgetEnvKeys(),
	}
	want := make(map[string]string)
	for group, names := range members {
		for _, name := range names {
			want[name] = group
		}
	}
	if len(want) != len(runtimeEnvVars) {
		t.Fatalf("membership count = %d, registry = %d", len(want), len(runtimeEnvVars))
	}
	for _, row := range runtimeEnvVars {
		t.Run(row.name, func(t *testing.T) {
			group, ok := RuntimeEnvDisplayGroup(row.name)
			if !ok || group != want[row.name] || row.displayGroup != group || group == "" {
				t.Fatalf("group = %q, %v; want %q", group, ok, want[row.name])
			}
			if row.aliasOf != "" {
				canonical, _ := RuntimeEnvDisplayGroup(row.aliasOf)
				if group != canonical {
					t.Fatal("alias metadata differs from canonical")
				}
				for _, status := range snapshotFrom(nil).Status() {
					if status.Name == row.name {
						t.Fatal("unset alias reported")
					}
				}
				value := "1"
				if row.name == EnvAutoRework {
					value = "true"
				}
				found := false
				for _, status := range snapshotFrom(map[string]string{row.name: value, row.aliasOf: "0"}).Status() {
					if status.Name == row.name {
						found = true
						if !strings.Contains(status.Warning, "deprecated") {
							t.Fatalf("missing deprecation: %+v", status)
						}
					}
					if status.Name == row.aliasOf && status.Value != "0" {
						t.Fatalf("canonical precedence lost: %+v", status)
					}
				}
				if !found {
					t.Fatal("set alias omitted")
				}
			}
		})
	}
	for _, row := range modelEnvVars() {
		if row.displayGroup != "execution" {
			t.Errorf("model row: %+v", row)
		}
	}
	for _, row := range agentBudgetRuntimeEnvVars() {
		if row.displayGroup != "budgets" {
			t.Errorf("budget row: %+v", row)
		}
	}
	for _, name := range []string{"", "TAO_UNKNOWN"} {
		if group, ok := RuntimeEnvDisplayGroup(name); group != "other" || ok {
			t.Fatalf("unknown = %q, %v", group, ok)
		}
	}
}

// Legacy reader tests are replaced by the snapshot's complete-table writes,
// built-ins, empty/zero/presence, boolean grammar, consumption and status tests.
// Exercise invalid syntax for every model role rather than a legacy filter.
func TestSnapshotModelStatusAndConsumption(t *testing.T) {
	for _, key := range []string{EnvModel, EnvRunModel, EnvReviewModel, EnvMergeReviewModel, EnvResolverModel, EnvReworkEscalationModel, EnvEffort, EnvRunEffort, EnvReviewEffort, EnvMergeReviewEffort, EnvResolverEffort} {
		for _, value := range []string{"", " \t\u2003", "two models", "two\tmodels", "two\nmodels", "two\u00a0models"} {
			s := snapshotFrom(map[string]string{key: value})
			if err := s.Require(key); err == nil || !strings.HasPrefix(err.Error(), key+":") {
				t.Fatalf("%s=%q: %v", key, value, err)
			}
			for _, row := range s.Status() {
				if row.Name == key && (row.Source != "invalid" || row.Warning == "") {
					t.Fatalf("missing diagnostic: %+v", row)
				}
			}
		}
		s := snapshotFrom(map[string]string{key: "\u2003provider/model\t "})
		if err := s.Require(key); err != nil {
			t.Fatal(err)
		}
		for _, row := range s.Status() {
			if row.Name == key && (row.Value != "provider/model" || row.Source != "env" || row.Warning != "") {
				t.Fatalf("noncanonical model row: %+v", row)
			}
		}
	}
}

func TestModelEnvironmentDescriptorProjection(t *testing.T) {
	values := map[string]string{}
	var wantKeys []string
	for _, tc := range modelProjectionCases() {
		values[tc.env] = tc.env
		wantKeys = append(wantKeys, tc.env)
	}
	snapshot := snapshotFrom(values)
	defaults := snapshot.Defaults()
	for _, tc := range modelProjectionCases() {
		if got := *tc.field(&defaults.ModelSelection); got != tc.env {
			t.Fatalf("%s: %q", tc.key, got)
		}
	}
	var keys []string
	for _, row := range snapshot.Status() {
		if _, ok := values[row.Name]; ok {
			keys = append(keys, row.Name)
		}
	}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("order %v", keys)
	}
	if len(configtypes.ModelSettings()) != len(wantKeys) {
		t.Fatal("descriptor membership drift")
	}
	// Session aliases must not cause duplicated configurable fields.
	for _, role := range configtypes.SessionRoles() {
		for _, kind := range []string{"model", "effort"} {
			count := 0
			for _, setting := range configtypes.ModelSettings() {
				if setting.Role == role.ModelRole && setting.Kind == kind {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("session %s has %d %s fields", role.Role, count, kind)
			}
		}
	}
}

func TestReviewAgentStatus(t *testing.T) {
	for _, value := range []string{"", "pi", "claude", "invalid"} {
		s := snapshotFrom(map[string]string{EnvReviewAgent: value})
		found := false
		for _, row := range s.Status() {
			if row.Name != EnvReviewAgent {
				continue
			}
			found = true
			if value == "invalid" {
				if row.Source != "invalid" || row.Warning == "" {
					t.Fatalf("%+v", row)
				}
			} else if row.Value != value || row.Warning != "" {
				t.Fatalf("%+v", row)
			}
		}
		if !found {
			t.Fatal("missing review agent row")
		}
	}
}

func TestSnapshotDefaultsRemainLowestPrecedence(t *testing.T) {
	s := snapshotFrom(map[string]string{EnvAgent: "claude", EnvPullRequest: "true", EnvExecutionMode: "current"})
	overrides := RunOptionsPatch{Agent: AgentPi, ExecutionMode: ExecutionModeIsolated}.WithPullRequest(false)
	resolved, err := ResolveRunOptions(s.Defaults().RunOptionsPatch, overrides)
	if err != nil || resolved.Agent != AgentPi || resolved.PullRequest || resolved.ExecutionMode != ExecutionModeIsolated {
		t.Fatalf("explicit overrides lost: %+v, %v", resolved, err)
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	original, ok := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			if err := os.Setenv(name, original); err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
			return
		}
		if err := os.Unsetenv(name); err != nil {
			t.Errorf("unset %s: %v", name, err)
		}
	})
}
