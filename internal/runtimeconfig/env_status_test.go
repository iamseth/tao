package runtimeconfig

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/selfupdate"
)

func TestRuntimeEnvDefaultsAppliesAllSupportedValues(t *testing.T) {
	t.Setenv(EnvCommitPolicy, "slice")
	t.Setenv(EnvExecutionMode, "current")
	t.Setenv(EnvAgent, "codex")
	t.Setenv(EnvPullRequest, "true")
	t.Setenv(EnvReview, "off")
	t.Setenv(EnvAutoRework, "false")
	t.Setenv(EnvMaxReworkAttempts, "7")
	t.Setenv(EnvSessionTimeout, "30m")
	t.Setenv(EnvUpdate, "auto")
	t.Setenv(EnvSkipPermissions, "true")

	got, err := RuntimeEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitPolicy != CommitPolicySlice || got.ExecutionMode != ExecutionModeCurrent || got.Agent != AgentCodex || got.PullRequest == nil || !*got.PullRequest || got.ReviewEnabled == nil || *got.ReviewEnabled || got.AutoRework == nil || *got.AutoRework || got.MaxReworkAttempts == nil || *got.MaxReworkAttempts != 7 || got.SessionTimeout == nil || *got.SessionTimeout != 30*time.Minute || got.UpdateMode != selfupdate.ModeAuto || !got.SkipPermissions {
		t.Fatalf("unexpected env defaults: %#v", got)
	}
}

func TestRuntimeEnvDefaultsApplyInDefaultsRole(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		t.Setenv(name, "")
	}
	t.Setenv(EnvAgent, "codex")

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
	for _, name := range runtimeEnvKeys() {
		t.Setenv(name, "")
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
		t.Setenv(name, "")
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
		t.Setenv(name, "")
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
		{Name: EnvUpdate, Value: "warn", Source: "default"},
		{Name: EnvPullRequest, Value: "false", Source: "default"},
		{Name: EnvReview, Value: "true", Source: "default"},
		{Name: EnvAutoRework, Value: "true", Source: "default"},
		{Name: EnvMaxReworkAttempts, Value: "5", Source: "default"},
		{Name: EnvSkipPermissions, Value: "false", Source: "default"},
		{Name: EnvMaxSliceOutputTokens, Value: "disabled", Source: "default"},
		{Name: EnvMaxSliceCost, Value: "disabled", Source: "default"},
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

func TestRuntimeAgentBudgetThresholdsAppliesOverrides(t *testing.T) {
	for _, name := range runtimeEnvKeys() {
		t.Setenv(name, "")
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
		t.Setenv(name, "")
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
		t.Setenv(name, "")
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
				t.Setenv(name, "")
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
		t.Setenv(name, "")
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
	for _, agent := range []AgentKind{AgentPi, AgentClaude, AgentOpenCode, AgentCodex} {
		t.Run(agent.String(), func(t *testing.T) {
			for _, name := range runtimeEnvKeys() {
				t.Setenv(name, "")
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
