package runtimeconfig

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/selfupdate"
	"github.com/iamseth/tao/internal/theme"
)

func TestRuntimeTheme(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		warning         bool
	}{
		{name: "unset", want: theme.Default().Name()},
		{name: "empty", want: theme.Default().Name()},
		{name: "gruvbox", raw: "gruvbox", want: "gruvbox"},
		{name: "normalized", raw: "Gruvbox ", want: "gruvbox"},
		{name: "alias", raw: "default", want: theme.Default().Name()},
		{name: "invalid", raw: "nope", want: theme.Default().Name(), warning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range RuntimeEnvKeys() {
				unsetEnv(t, key)
			}
			if tc.name != "unset" {
				t.Setenv(EnvTheme, tc.raw)
			}
			got, warning := RuntimeTheme(os.Getenv)
			if got.Name() != tc.want || (warning != "") != tc.warning {
				t.Fatalf("RuntimeTheme() = %s, %q; want %s, warning=%v", got.Name(), warning, tc.want, tc.warning)
			}
			defaults, err := RuntimeEnvDefaults()
			if err != nil || defaults.Theme.Name() != tc.want {
				t.Fatalf("defaults = %+v, %v", defaults, err)
			}
			rows, err := RuntimeEnvStatus()
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(rows, func(row EnvVarStatus) bool { return row.Name == EnvTheme })
			if i < 0 {
				t.Fatal("missing TAO_THEME status row")
			}
			row := rows[i]
			source := "env"
			if tc.raw == "" || tc.warning {
				source = "default"
			}
			if row.Value != tc.want || row.Source != source || (row.Warning != "") != tc.warning {
				t.Fatalf("theme status = %+v", row)
			}
			if tc.warning && !strings.HasSuffix(row.Warning, "using default") {
				t.Fatalf("warning = %q", row.Warning)
			}
		})
	}
}

func TestRuntimeModelAndSessionDefaultsAreIndependent(t *testing.T) {
	t.Setenv(EnvAgent, "invalid-unused-provider")
	t.Setenv(EnvModel, "base")
	t.Setenv(EnvResolverModel, "resolver")
	models, err := RuntimeModelEnvDefaults()
	if err != nil || models.Model != "base" || models.ResolverModel != "resolver" {
		t.Fatalf("models=%#v error=%v", models, err)
	}
	t.Setenv(EnvModel, "")
	if _, err := RuntimeModelEnvDefaults(); err == nil || !strings.Contains(err.Error(), EnvModel) {
		t.Fatalf("invalid model: %v", err)
	}
	t.Setenv(EnvAgent, "claude")
	t.Setenv(EnvSessionTimeout, "1m")
	t.Setenv(EnvSkipPermissions, "true")
	session, err := RuntimeAgentSessionEnvDefaults()
	if err != nil || session.Agent != AgentClaude || session.SessionTimeoutValue() != time.Minute || !session.SkipPermissions || session.Model != "" || session.ResolverModel != "" {
		t.Fatalf("session=%#v error=%v", session, err)
	}
	t.Setenv(EnvSessionTimeout, "invalid")
	if _, err := RuntimeAgentSessionEnvDefaults(); err == nil || !strings.Contains(err.Error(), EnvSessionTimeout) {
		t.Fatalf("invalid timeout: %v", err)
	}
}

func TestRuntimeEnvReworkEscalationDefaultsAndOverrides(t *testing.T) {
	for _, name := range RuntimeEnvKeys() {
		unsetEnv(t, name)
	}
	defaults, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.ReworkEscalationModel != "" || defaults.ReworkEscalationFromAttempt != nil || defaults.ReworkEscalationFromAttemptValue() != 4 || DefaultReworkEscalationFromAttempt != 4 {
		t.Fatalf("unexpected escalation defaults: %#v", defaults)
	}
	for _, tt := range []struct {
		threshold string
		want      int
	}{
		{threshold: " 06 ", want: 6},
		{threshold: "1", want: 1},
		{threshold: "", want: 4},
	} {
		t.Run(tt.threshold, func(t *testing.T) {
			t.Setenv(EnvReworkEscalationModel, " provider/strong ")
			t.Setenv(EnvReworkEscalationFromAttempt, tt.threshold)
			got, err := RuntimeEnvDefaults()
			if err != nil {
				t.Fatal(err)
			}
			if got.ReworkEscalationModel != "provider/strong" || got.ReworkEscalationFromAttemptValue() != tt.want {
				t.Fatalf("unexpected escalation settings: %#v", got)
			}
			if tt.threshold != "" && (got.ReworkEscalationFromAttempt == nil || *got.ReworkEscalationFromAttempt != tt.want) {
				t.Fatalf("explicit threshold not stored: %#v", got)
			}
			models, err := RuntimeModelEnvDefaults()
			if err != nil || models.ReworkEscalationModel != "provider/strong" {
				t.Fatalf("model defaults = %#v, %v", models, err)
			}
			rows, err := RuntimeEnvStatus()
			if err != nil {
				t.Fatal(err)
			}
			source := "env"
			if tt.threshold == "" {
				source = "default"
			}
			for _, want := range []EnvVarStatus{
				{Name: EnvReworkEscalationModel, Value: "provider/strong", Source: "env"},
				{Name: EnvReworkEscalationFromAttempt, Value: strconv.Itoa(tt.want), Source: source},
			} {
				if !slices.Contains(rows, want) {
					t.Errorf("missing status row %#v", want)
				}
			}
		})
	}
}

func TestRuntimeEnvReworkEscalationRejectsInvalidValues(t *testing.T) {
	for _, name := range RuntimeEnvKeys() {
		unsetEnv(t, name)
	}
	for _, tt := range []struct{ name, value string }{
		{EnvReworkEscalationFromAttempt, "0"},
		{EnvReworkEscalationFromAttempt, "-1"},
		{EnvReworkEscalationFromAttempt, "four"},
		{EnvReworkEscalationFromAttempt, "1.5"},
		{EnvReworkEscalationModel, "two models"},
		{EnvReworkEscalationModel, " \t"},
		{EnvReworkEscalationModel, ""},
	} {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			t.Setenv(tt.name, tt.value)
			_, defaultsErr := RuntimeEnvDefaults()
			_, statusErr := RuntimeEnvStatus()
			for _, err := range []error{defaultsErr, statusErr} {
				if err == nil || !strings.HasPrefix(err.Error(), tt.name+":") {
					t.Errorf("error = %v, want variable name %s", err, tt.name)
				}
			}
		})
	}
}

func TestRuntimeEnvDefaultsAppliesAllSupportedValues(t *testing.T) {
	t.Setenv(EnvCommitPolicy, "slice")
	t.Setenv(EnvExecutionMode, "current")
	t.Setenv(EnvAgent, "claude")
	t.Setenv(EnvPullRequest, "true")
	t.Setenv(EnvReview, "off")
	t.Setenv(EnvAutoRework, "false")
	t.Setenv(EnvMaxReworkAttempts, "7")
	t.Setenv(EnvSessionTimeout, "30m")
	t.Setenv(EnvModel, " base ")
	t.Setenv(EnvRunModel, " run ")
	t.Setenv(EnvReviewModel, " review ")
	t.Setenv(EnvMergeReviewModel, " merge ")
	t.Setenv(EnvResolverModel, " resolver ")
	t.Setenv(EnvUpdate, "auto")
	t.Setenv(EnvSkipPermissions, "true")

	got, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitPolicy != CommitPolicySlice || got.ExecutionMode != ExecutionModeCurrent || got.Agent != AgentClaude || got.PullRequest == nil || !*got.PullRequest || got.ReviewEnabled == nil || *got.ReviewEnabled || got.AutoRework == nil || *got.AutoRework || got.MaxReworkAttempts == nil || *got.MaxReworkAttempts != 7 || got.SessionTimeout == nil || *got.SessionTimeout != 30*time.Minute || got.UpdateMode != selfupdate.ModeAuto || !got.SkipPermissions {
		t.Fatalf("unexpected env defaults: %#v", got)
	}
	if got.Model != "base" || got.RunModel != "run" || got.ReviewModel != "review" || got.MergeReviewModel != "merge" || got.ResolverModel != "resolver" {
		t.Fatalf("unexpected model env defaults: %#v", got.RunOptionsPatch)
	}
}

func TestRuntimeEnvDefaultsApplyInDefaultsRole(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvAgent, "pi")

	defaults, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	options, err := ResolveRunOptions(defaults.RunOptionsPatch, RunOptionsPatch{Agent: AgentClaude})
	if err != nil {
		t.Fatal(err)
	}
	if options.Agent != AgentClaude {
		t.Fatalf("expected request override to win over env-derived default, got %q", options.Agent)
	}
}

func TestRuntimeEnvDefaultsSessionTimeoutZeroDisables(t *testing.T) {
	t.Setenv(EnvSessionTimeout, "0")

	got, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionTimeout == nil || *got.SessionTimeout != 0 {
		t.Fatalf("expected session timeout disabled by env, got %#v", got)
	}
}

func TestRuntimeEnvDefaultsReportsInvalidValueWithName(t *testing.T) {
	t.Setenv(EnvPullRequest, "maybe")
	_, err := RuntimeEnvDefaults()
	if err == nil || !strings.HasPrefix(err.Error(), EnvPullRequest) {
		t.Fatalf("expected env var name in error, got %v", err)
	}
}

func TestRuntimeEnvDefaultsReportsInvalidReviewWithName(t *testing.T) {
	t.Setenv(EnvReview, "maybe")
	_, err := RuntimeEnvDefaults()
	if err == nil || !strings.HasPrefix(err.Error(), EnvReview) {
		t.Fatalf("expected review env error, got %v", err)
	}
}

func TestRuntimeEnvDefaultsReportsInvalidSessionTimeoutWithName(t *testing.T) {
	t.Setenv(EnvSessionTimeout, "soon")
	_, err := RuntimeEnvDefaults()
	if err == nil || !strings.HasPrefix(err.Error(), EnvSessionTimeout) {
		t.Fatalf("expected session timeout env error, got %v", err)
	}
}

func TestRuntimeEnvDefaultsIgnoresEmptyEnvOverrides(t *testing.T) {
	for _, v := range runtimeEnvVars {
		unsetEnv(t, v.name)
		if !v.applyWhenEmpty {
			t.Setenv(v.name, "")
		}
	}

	got, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitPolicy != CommitPolicySlice || got.ExecutionMode != ExecutionModeIsolated || got.Agent != AgentPi || got.PullRequest != nil || got.ReviewEnabled != nil || got.SessionTimeout == nil || *got.SessionTimeout != DefaultSessionTimeout || got.UpdateMode != selfupdate.ModeWarn || got.SkipPermissions {
		t.Fatalf("expected built-in defaults with unset optional values, got %#v", got)
	}
}

func TestRuntimeEnvDefaultsRecordsExplicitFalsePullRequestAndPiAgent(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvAgent, "pi")
	t.Setenv(EnvPullRequest, "false")
	t.Setenv(EnvSkipPermissions, "false")

	got, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != AgentPi || got.PullRequest == nil || *got.PullRequest || got.SkipPermissions {
		t.Fatalf("expected pi agent with explicit false env defaults, got %#v", got)
	}
}

func TestRuntimeEnvStatusPlannerRouting(t *testing.T) {
	for _, raw := range []string{"", " shadow ", "not-valid"} {
		t.Run(raw, func(t *testing.T) {
			for _, name := range runtimeEnvKeys() {
				unsetEnv(t, name)
			}
			names := []string{EnvPlannerRouting, EnvPlannerRoutingArms, EnvPlannerRoutingFloor}
			for _, name := range names {
				if raw == "" {
					unsetEnv(t, name)
				} else {
					t.Setenv(name, raw)
				}
			}
			rows, err := RuntimeEnvStatus()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				i := slices.IndexFunc(rows, func(row EnvVarStatus) bool { return row.Name == name })
				if i < 0 {
					t.Fatalf("missing %s row", name)
				}
				row := rows[i]
				if raw == "" {
					if row.Source != "default" || !strings.Contains(row.Value, "not set") {
						t.Errorf("unset %s row = %#v", name, row)
					}
				} else if row.Source != "env" || row.Value != raw || row.Warning != "" {
					t.Errorf("raw %s row = %#v, want %q without validation", name, row, raw)
				}
			}
		})
	}
}

func TestRuntimeEnvStatusReportsDefaultsAndOverrides(t *testing.T) {
	t.Setenv(EnvExecutionMode, "")
	t.Setenv(EnvAgent, "")
	t.Setenv(EnvSkipPermissions, "")
	t.Setenv(EnvCommitPolicy, "none")
	t.Setenv(EnvPullRequest, "1")
	t.Setenv(EnvReview, "off")
	t.Setenv(EnvSessionTimeout, "45m")

	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]EnvVarStatus{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if byName[EnvCommitPolicy].Value != "none" || byName[EnvCommitPolicy].Source != "env" {
		t.Fatalf("unexpected commit policy row: %#v", byName[EnvCommitPolicy])
	}
	if byName[EnvPullRequest].Value != "true" || byName[EnvPullRequest].Source != "env" {
		t.Fatalf("unexpected pull request row: %#v", byName[EnvPullRequest])
	}
	if byName[EnvSessionTimeout].Value != (45*time.Minute).String() || byName[EnvSessionTimeout].Source != "env" {
		t.Fatalf("unexpected session timeout row: %#v", byName[EnvSessionTimeout])
	}
	if byName[EnvReview].Value != "false" || byName[EnvReview].Source != "env" {
		t.Fatalf("unexpected review row: %#v", byName[EnvReview])
	}
	if byName[EnvAgent].Value != AgentPi.String() || byName[EnvAgent].Source != "default" {
		t.Fatalf("unexpected agent row: %#v", byName[EnvAgent])
	}
}

func TestRuntimeEnvStatusDefaultRowsDeriveFromRunOptionsPatch(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}

	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}

	defaults := DefaultRunOptionsPatch()
	want := []EnvVarStatus{
		{Name: EnvCommitPolicy, Value: defaults.CommitPolicy.String(), Source: "default"},
		{Name: EnvExecutionMode, Value: defaults.ExecutionModeValue().String(), Source: "default"},
		{Name: EnvAgent, Value: defaults.Agent.String(), Source: "default"},
		{Name: EnvSessionTimeout, Value: defaults.SessionTimeoutValue().String(), Source: "default"},
		{Name: EnvModel, Value: "", Source: "default"},
		{Name: EnvRunModel, Value: "", Source: "default"},
		{Name: EnvReviewModel, Value: "", Source: "default"},
		{Name: EnvMergeReviewModel, Value: "", Source: "default"},
		{Name: EnvResolverModel, Value: "", Source: "default"},
		{Name: EnvReworkEscalationModel, Value: "", Source: "default"},
		{Name: EnvUpdate, Value: "warn", Source: "default"},
		{Name: EnvPullRequest, Value: "false", Source: "default"},
		{Name: EnvReview, Value: "true", Source: "default"},
		{Name: EnvAutoRework, Value: "true", Source: "default"},
		{Name: EnvMaxReworkAttempts, Value: "5", Source: "default"},
		{Name: EnvReworkEscalationFromAttempt, Value: "4", Source: "default"},
		{Name: EnvSkipPermissions, Value: "false", Source: "default"},
		{Name: EnvMergeVerifyCommand, Value: "auto-detect", Source: "default"},
		{Name: EnvAggregateReviewConvergenceWindow, Value: "2", Source: "default"},
		{Name: EnvApprovedBy, Value: "", Source: "default"},
		{Name: EnvRunHeader, Value: "true", Source: "default"},
		{Name: EnvPlannerRouting, Value: "not set (default: off)", Source: "default"},
		{Name: EnvPlannerRoutingArms, Value: "not set (comma list, e.g. pi=0.5,claude=0.5)", Source: "default"},
		{Name: EnvPlannerRoutingFloor, Value: "not set (default: 0.1)", Source: "default"},
		{Name: EnvMaxSliceOutputTokens, Value: "disabled", Source: "default"},
		{Name: EnvMaxSliceCost, Value: "disabled", Source: "default"},
		{Name: EnvTheme, Value: theme.Default().Name(), Source: "default"},
		{Name: EnvBudgetSliceOutputTokens, Value: "40000", Source: "default"},
		{Name: EnvBudgetSliceCost, Value: "5", Source: "default"},
		{Name: EnvBudgetSliceToolCalls, Value: "120", Source: "default"},
		{Name: EnvBudgetSliceAssistantMessages, Value: "80", Source: "default"},
		{Name: EnvBudgetSliceErroredMessages, Value: "0", Source: "default"},
		{Name: EnvBudgetPlanOutputTokens, Value: "150000", Source: "default"},
		{Name: EnvBudgetPlanCost, Value: "20", Source: "default"},
		{Name: EnvBudgetPlanToolCalls, Value: "400", Source: "default"},
		{Name: EnvBudgetPlanAssistantMessages, Value: "300", Source: "default"},
		{Name: EnvBudgetPlanErroredMessages, Value: "0", Source: "default"},
	}
	if len(rows) != len(want) {
		t.Fatalf("expected %d status rows, got %d: %#v", len(want), len(rows), rows)
	}
	for i, expected := range want {
		if rows[i] != expected {
			t.Fatalf("row %d = %#v, want %#v", i, rows[i], expected)
		}
	}
}

func TestRuntimeEnvStatusReportsNewOverrides(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvMergeVerifyCommand, "go test ./...")
	t.Setenv(EnvAggregateReviewConvergenceWindow, "4")
	t.Setenv(EnvApprovedBy, "release-bot")
	t.Setenv(EnvRunHeader, "0")

	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]EnvVarStatus, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}
	want := map[string]string{
		EnvMergeVerifyCommand:               "go test ./...",
		EnvAggregateReviewConvergenceWindow: "4",
		EnvApprovedBy:                       "release-bot",
		EnvRunHeader:                        "false",
	}
	for name, value := range want {
		row := byName[name]
		if row.Value != value || row.Source != "env" || row.Warning != "" {
			t.Fatalf("unexpected %s row: %#v", name, row)
		}
	}
}

func TestRuntimeMergeVerifyCommandRetainsSetEmpty(t *testing.T) {
	unsetEnv(t, EnvMergeVerifyCommand)
	if command, set := RuntimeMergeVerifyCommand(); command != "" || set {
		t.Fatalf("unset RuntimeMergeVerifyCommand() = %q, %t", command, set)
	}
	t.Setenv(EnvMergeVerifyCommand, "")

	command, set := RuntimeMergeVerifyCommand()
	if command != "" || !set {
		t.Fatalf("RuntimeMergeVerifyCommand() = %q, %t, want empty and set", command, set)
	}
	if _, err := RuntimeEnvDefaults(); err != nil {
		t.Fatal(err)
	}
	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == EnvMergeVerifyCommand {
			if row.Value != "" || row.Source != "env" {
				t.Fatalf("set-empty merge verify row = %#v", row)
			}
			return
		}
	}
	t.Fatalf("missing %s status row", EnvMergeVerifyCommand)
}

func TestRuntimeAggregateReviewConvergenceWindow(t *testing.T) {
	t.Setenv(EnvAggregateReviewConvergenceWindow, "")
	if got, err := RuntimeAggregateReviewConvergenceWindow(); err != nil || got != DefaultAggregateReviewConvergenceWindow {
		t.Fatalf("empty window = %d, %v", got, err)
	}

	t.Setenv(EnvAggregateReviewConvergenceWindow, "5")
	if got, err := RuntimeAggregateReviewConvergenceWindow(); err != nil || got != 5 {
		t.Fatalf("override window = %d, %v", got, err)
	}

	for _, value := range []string{"1", "many"} {
		t.Setenv(EnvAggregateReviewConvergenceWindow, value)
		if _, err := RuntimeEnvDefaults(); err != nil {
			t.Fatalf("RuntimeEnvDefaults() with window %q: %v", value, err)
		}
		rows, err := RuntimeEnvStatus()
		if err != nil {
			t.Fatalf("RuntimeEnvStatus() with window %q: %v", value, err)
		}
		var windowRow EnvVarStatus
		for _, row := range rows {
			if row.Name == EnvAggregateReviewConvergenceWindow {
				windowRow = row
				break
			}
		}
		if windowRow.Value != "2" || windowRow.Source != "default" || windowRow.Warning == "" {
			t.Fatalf("invalid window status = %#v", windowRow)
		}

		_, err = RuntimeAggregateReviewConvergenceWindow()
		want := EnvAggregateReviewConvergenceWindow + " must be an integer of at least 2"
		if err == nil || err.Error() != want {
			t.Fatalf("window %q error = %v, want %q", value, err, want)
		}
	}
}

func TestRuntimeRunHeaderUsesEnabledUnlessExactlyZeroSemantics(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  string
	}{
		{value: "0", want: "false"},
		{value: "false", want: "true"},
		{value: "00", want: "true"},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(EnvRunHeader, tt.value)
			rows, err := RuntimeEnvStatus()
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Name == EnvRunHeader {
					if row.Value != tt.want || row.Source != "env" {
						t.Fatalf("run header row = %#v, want value %q from env", row, tt.want)
					}
					return
				}
			}
			t.Fatalf("missing %s status row", EnvRunHeader)
		})
	}
}

func TestRuntimeAgentBudgetThresholdsAppliesOverrides(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvBudgetSliceOutputTokens, "42000")
	t.Setenv(EnvBudgetSliceCost, "6.25")
	t.Setenv(EnvBudgetPlanErroredMessages, "3")

	got := RuntimeAgentBudgetThresholds()
	if got.Slice.OutputTokens != 42000 || got.Slice.Cost != 6.25 || got.Plan.ErroredMessages != 3 {
		t.Fatalf("unexpected budget overrides: %+v", got)
	}
	if got.Plan.OutputTokens != 150000 || got.Slice.ToolCalls != 120 {
		t.Fatalf("unset thresholds should retain defaults: %+v", got)
	}
}

func TestInvalidBudgetOverridesFallBackAndWarnInStatus(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvBudgetSliceCost, "expensive")
	t.Setenv(EnvBudgetPlanToolCalls, "-1")

	defaults, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.AgentBudgetThresholds.Slice.Cost != 5 || defaults.AgentBudgetThresholds.Plan.ToolCalls != 400 {
		t.Fatalf("invalid overrides should retain defaults: %+v", defaults.AgentBudgetThresholds)
	}
	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]EnvVarStatus)
	for _, row := range rows {
		byName[row.Name] = row
	}
	for _, name := range []string{EnvBudgetSliceCost, EnvBudgetPlanToolCalls} {
		if byName[name].Source != "default" || byName[name].Warning == "" {
			t.Fatalf("expected fallback warning for %s: %#v", name, byName[name])
		}
	}
}

func TestRuntimeEnvDefaultsAppliesExecutionMode(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvExecutionMode, "current")

	got, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionMode != ExecutionModeCurrent {
		t.Fatalf("expected execution mode current, got %#v", got)
	}
}

func TestRuntimeSliceBudgetCapsAreOptInAndInvalidValuesDisable(t *testing.T) {
	t.Setenv(EnvMaxSliceOutputTokens, "")
	t.Setenv(EnvMaxSliceCost, "")
	caps, warnings := RuntimeSliceBudgetCaps()
	if caps.OutputTokens != nil || caps.Cost != nil || len(warnings) != 0 {
		t.Fatalf("unset caps = %#v, warnings=%v", caps, warnings)
	}

	t.Setenv(EnvMaxSliceOutputTokens, "1200")
	t.Setenv(EnvMaxSliceCost, "2.5")
	caps, warnings = RuntimeSliceBudgetCaps()
	if caps.OutputTokens == nil || *caps.OutputTokens != 1200 || caps.Cost == nil || *caps.Cost != 2.5 || len(warnings) != 0 {
		t.Fatalf("valid caps = %#v, warnings=%v", caps, warnings)
	}

	t.Setenv(EnvMaxSliceOutputTokens, "many")
	t.Setenv(EnvMaxSliceCost, "NaN")
	caps, warnings = RuntimeSliceBudgetCaps()
	if caps.OutputTokens != nil || caps.Cost != nil || len(warnings) != 2 {
		t.Fatalf("invalid caps = %#v, warnings=%v", caps, warnings)
	}
	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == EnvMaxSliceOutputTokens || row.Name == EnvMaxSliceCost {
			if row.Value != "disabled" || row.Source != "default" || !strings.Contains(row.Warning, "cap disabled") {
				t.Fatalf("invalid cap status = %#v", row)
			}
		}
	}
}

func TestRuntimeUpdateModeParsingStatusAndKeyCoverage(t *testing.T) {
	for _, mode := range []selfupdate.Mode{selfupdate.ModeWarn, selfupdate.ModeAuto, selfupdate.ModeOff} {
		t.Run(string(mode), func(t *testing.T) {
			for _, name := range runtimeEnvKeys() {
				unsetEnv(t, name)
			}
			t.Setenv(EnvUpdate, string(mode))

			defaults, err := RuntimeEnvDefaults()
			if err != nil {
				t.Fatal(err)
			}
			if defaults.UpdateMode != mode {
				t.Fatalf("UpdateMode = %q, want %q", defaults.UpdateMode, mode)
			}
			rows, err := RuntimeEnvStatus()
			if err != nil {
				t.Fatal(err)
			}
			var updateRow EnvVarStatus
			for _, row := range rows {
				if row.Name == EnvUpdate {
					updateRow = row
					break
				}
			}
			if updateRow.Value != string(mode) || updateRow.Source != "env" {
				t.Fatalf("update status = %#v", updateRow)
			}
		})
	}

	if !slices.Contains(RuntimeEnvKeys(), EnvUpdate) {
		t.Fatalf("RuntimeEnvKeys() does not contain %s", EnvUpdate)
	}
}

func TestRuntimeUpdateModeRejectsInvalidValue(t *testing.T) {
	t.Setenv(EnvUpdate, "sometimes")
	_, err := RuntimeEnvDefaults()
	if err == nil || !strings.Contains(err.Error(), "TAO_UPDATE: invalid update mode") || !strings.Contains(err.Error(), "warn, auto, or off") {
		t.Fatalf("RuntimeEnvDefaults() error = %v", err)
	}
	_, err = RuntimeEnvStatus()
	if err == nil || !strings.Contains(err.Error(), "TAO_UPDATE: invalid update mode") {
		t.Fatalf("RuntimeEnvStatus() error = %v", err)
	}
}

func TestRuntimeEnvStatusReportsExecutionModeOverride(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvExecutionMode, "current")

	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]EnvVarStatus{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if byName[EnvExecutionMode].Value != "current" || byName[EnvExecutionMode].Source != "env" {
		t.Fatalf("unexpected execution mode row: %#v", byName[EnvExecutionMode])
	}
}

func TestRuntimeEnvStatusReportsExecutionModeInvalidOverride(t *testing.T) {
	t.Setenv(EnvExecutionMode, "sandbox")
	_, err := RuntimeEnvStatus()
	if err == nil || !strings.HasPrefix(err.Error(), EnvExecutionMode) {
		t.Fatalf("expected execution mode env error, got %v", err)
	}
}

func TestRuntimeEnvStatusReportsSessionTimeoutInvalidOverride(t *testing.T) {
	t.Setenv(EnvSessionTimeout, "soon")
	_, err := RuntimeEnvStatus()
	if err == nil || !strings.HasPrefix(err.Error(), EnvSessionTimeout) {
		t.Fatalf("expected session timeout env error, got %v", err)
	}
}

func TestRuntimeEnvStatusReportsReviewInvalidOverride(t *testing.T) {
	t.Setenv(EnvReview, "maybe")
	_, err := RuntimeEnvStatus()
	if err == nil || !strings.HasPrefix(err.Error(), EnvReview) {
		t.Fatalf("expected review env error, got %v", err)
	}
}

func TestRuntimeEnvStatusReportsInvalidOverride(t *testing.T) {
	t.Setenv(EnvAgent, "robot")
	_, err := RuntimeEnvStatus()
	if err == nil || !strings.HasPrefix(err.Error(), EnvAgent) {
		t.Fatalf("expected agent env error, got %v", err)
	}
}

func TestRuntimeEnvStatusReportsAgentAndExplicitFalsePullRequest(t *testing.T) {
	for _, agent := range []AgentKind{AgentPi, AgentClaude} {
		t.Run(agent.String(), func(t *testing.T) {
			for _, name := range runtimeEnvKeys() {
				unsetEnv(t, name)
			}
			t.Setenv(EnvAgent, agent.String())
			t.Setenv(EnvPullRequest, "false")

			rows, err := RuntimeEnvStatus()
			if err != nil {
				t.Fatal(err)
			}
			byName := map[string]EnvVarStatus{}
			for _, row := range rows {
				byName[row.Name] = row
			}
			if byName[EnvAgent].Value != agent.String() || byName[EnvAgent].Source != "env" {
				t.Fatalf("unexpected agent row: %#v", byName[EnvAgent])
			}
			if byName[EnvPullRequest].Value != "false" || byName[EnvPullRequest].Source != "env" {
				t.Fatalf("unexpected pull request row: %#v", byName[EnvPullRequest])
			}
		})
	}
}

func TestRuntimeModelEnvStatusAndKeys(t *testing.T) {
	for _, name := range RuntimeEnvKeys() {
		unsetEnv(t, name)
	}
	models := map[string]string{
		EnvModel: "provider/base", EnvRunModel: "provider/run", EnvReviewModel: "provider/review",
		EnvMergeReviewModel: "provider/merge", EnvResolverModel: "provider/resolver",
	}
	for name, value := range models {
		if !slices.Contains(RuntimeEnvKeys(), name) {
			t.Fatalf("missing model key %s", name)
		}
		t.Setenv(name, "\u2003"+value+"\t ")
	}
	rows, err := RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]EnvVarStatus)
	for _, row := range rows {
		byName[row.Name] = row
	}
	for name, value := range models {
		if got := byName[name]; got != (EnvVarStatus{Name: name, Value: value, Source: "env"}) {
			t.Fatalf("model status = %#v", got)
		}
	}
}

func TestRuntimeModelEnvRejectsInvalidValuesWithName(t *testing.T) {
	for _, name := range RuntimeEnvKeys() {
		unsetEnv(t, name)
	}
	for _, name := range []string{EnvModel, EnvRunModel, EnvReviewModel, EnvMergeReviewModel, EnvResolverModel} {
		for _, value := range []string{"", " \t\u2003", "two models", "two\tmodels", "two\nmodels", "two\u00a0models"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				t.Setenv(name, value)
				_, defaultsErr := RuntimeEnvDefaults()
				_, statusErr := RuntimeEnvStatus()
				for _, err := range []error{defaultsErr, statusErr} {
					if err == nil || !strings.HasPrefix(err.Error(), name+":") {
						t.Fatalf("expected %s error for %q, got %v", name, value, err)
					}
				}
			})
		}
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
