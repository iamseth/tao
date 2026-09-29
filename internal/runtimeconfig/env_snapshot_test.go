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
	for _, key := range RuntimeEnvKeys() {
		t.Run(key, func(t *testing.T) {
			// Use each row's retained failure to distinguish presentation fallback
			// and opaque string settings from genuinely rejected configuration.
			snapshot := snapshotFrom(map[string]string{key: "invalid value"})
			_, thresholdErr := snapshot.BudgetThresholds()
			_, capErr := snapshot.BudgetCaps()
			invalid := snapshot.Require(key) != nil
			if (thresholdErr != nil) != (invalid && strings.HasPrefix(key, "TAO_BUDGET_")) {
				t.Fatalf("threshold admission for %s: %v", key, thresholdErr)
			}
			if (capErr != nil) != (invalid && (key == EnvMaxSliceCost || key == EnvMaxSliceOutputTokens)) {
				t.Fatalf("cap admission for %s: %v", key, capErr)
			}
		})
	}
	snapshot := snapshotFrom(map[string]string{EnvMaxSliceCost: "0", EnvMaxSliceOutputTokens: "0", EnvBudgetPlanCost: "0"})
	caps, err := snapshot.BudgetCaps()
	if err != nil || caps.Cost == nil || *caps.Cost != 0 || caps.OutputTokens == nil || *caps.OutputTokens != 0 {
		t.Fatalf("zero caps lost: %+v, %v", caps, err)
	}
	*caps.Cost = 100
	captured, err := snapshot.BudgetCaps()
	if err != nil || *captured.Cost != 0 {
		t.Fatalf("projection mutated snapshot: %+v, %v", captured, err)
	}
	thresholds, err := snapshot.BudgetThresholds()
	if err != nil || thresholds.Plan.Cost != 0 {
		t.Fatalf("zero threshold lost: %+v, %v", thresholds, err)
	}
	defaults, err := (EnvSnapshot{}).BudgetCaps()
	if err != nil || defaults.Cost != nil || defaults.OutputTokens != nil {
		t.Fatalf("built-in caps enabled: %+v, %v", defaults, err)
	}
}

func TestEnvSnapshotCompleteTableWrites(t *testing.T) {
	values := map[string]string{
		EnvCommitPolicy: "none", EnvExecutionMode: "current", EnvAgent: "claude", EnvSessionTimeout: "3m0s",
		EnvModel: "base", EnvRunModel: "run", EnvReviewModel: "review", EnvMergeReviewModel: "merge", EnvResolverModel: "resolve", EnvReworkEscalationModel: "strong",
		EnvUpdate: "off", EnvPullRequest: "true", EnvReview: "false", EnvAutoRework: "false", EnvMaxReworkAttempts: "7", EnvReworkEscalationFromAttempt: "6", EnvSkipPermissions: "true",
		EnvMergeVerifyCommand: "go test ./...", EnvAggregateReviewConvergenceWindow: "5", EnvApprovedBy: "bot", EnvRunHeader: "false", EnvTheme: "gruvbox",
		EnvPlannerRouting: "shadow", EnvPlannerRoutingArms: "pi=0.5,claude=0.5", EnvPlannerRoutingFloor: "0.2",
		EnvMaxSliceOutputTokens: "100", EnvMaxSliceCost: "1.5",
		EnvBudgetSliceOutputTokens: "101", EnvBudgetSliceCost: "2.5", EnvBudgetSliceToolCalls: "102", EnvBudgetSliceAssistantMessages: "103", EnvBudgetSliceErroredMessages: "104",
		EnvBudgetPlanOutputTokens: "201", EnvBudgetPlanCost: "3.5", EnvBudgetPlanToolCalls: "202", EnvBudgetPlanAssistantMessages: "203", EnvBudgetPlanErroredMessages: "204",
	}
	s := snapshotFrom(values)
	if err := s.Require(RuntimeEnvKeys()...); err != nil {
		t.Fatal(err)
	}
	rows := s.Status()
	if len(rows) != len(values) {
		t.Fatalf("rows=%d values=%d; update complete-table coverage", len(rows), len(values))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		value, ok := values[row.Name]
		if !ok || seen[row.Name] || row.Value != value || row.Source != "env" || row.Warning != "" {
			t.Errorf("unexpected row: %+v", row)
		}
		seen[row.Name] = true
	}
	yes, no, attempts, escalation, timeout := true, false, 7, 6, 3*time.Minute
	tokens, cost := int64(100), 1.5
	selected, _ := theme.Lookup("gruvbox")
	want := EnvDefaults{
		RunOptionsPatch: RunOptionsPatch{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentClaude, PullRequest: &yes, ReviewEnabled: &no, SessionTimeout: &timeout, Model: "base", RunModel: "run", ReviewModel: "review", MergeReviewModel: "merge", ResolverModel: "resolve", ReworkEscalationModel: "strong"},
		AutoRework:      &no, MaxReworkAttempts: &attempts, ReworkEscalationFromAttempt: &escalation, UpdateMode: selfupdate.ModeOff, Theme: selected, SkipPermissions: true,
		MergeVerifyCommand: "go test ./...", MergeVerifyCommandSet: true, AggregateReviewConvergenceWindow: 5, ApprovedBy: "bot", RunHeader: false,
		PlannerRouting:  PlannerRoutingConfig{Mode: "shadow", Arms: []PlannerRoutingArm{{AgentPi, 0.5}, {AgentClaude, 0.5}}, Floor: 0.2, ModeSet: true, ArmsSet: true, FloorSet: true},
		SliceBudgetCaps: SliceBudgetCaps{OutputTokens: &tokens, Cost: &cost},
	}
	want.AgentBudgetThresholds.Slice.OutputTokens = 101
	want.AgentBudgetThresholds.Slice.Cost = 2.5
	want.AgentBudgetThresholds.Slice.ToolCalls = 102
	want.AgentBudgetThresholds.Slice.AssistantMessages = 103
	want.AgentBudgetThresholds.Slice.ErroredMessages = 104
	want.AgentBudgetThresholds.Plan.OutputTokens = 201
	want.AgentBudgetThresholds.Plan.Cost = 3.5
	want.AgentBudgetThresholds.Plan.ToolCalls = 202
	want.AgentBudgetThresholds.Plan.AssistantMessages = 203
	want.AgentBudgetThresholds.Plan.ErroredMessages = 204
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
	if d.CommitPolicy != CommitPolicySlice || d.ExecutionMode != ExecutionModeIsolated || d.Agent != AgentPi || d.SessionTimeoutValue() != 20*time.Minute || d.PullRequestValue() || !d.ReviewEnabledValue() || d.AutoRework == nil || !*d.AutoRework || d.MaxReworkAttempts == nil || *d.MaxReworkAttempts != 5 || d.ReworkEscalationFromAttemptValue() != 4 || d.UpdateMode != selfupdate.ModeWarn || d.Theme != theme.Default() || !d.RunHeader || d.AggregateReviewConvergenceWindow != 2 || d.SkipPermissions || d.MergeVerifyCommandSet || d.MergeVerifyCommand != "" || d.ApprovedBy != "" || d.SliceBudgetCaps != (SliceBudgetCaps{}) || d.AgentBudgetThresholds != plan.DefaultAgentBudgetThresholds() {
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
		EnvMaxReworkAttempts: "-1", EnvReworkEscalationFromAttempt: "0", EnvAggregateReviewConvergenceWindow: "1", EnvMaxSliceOutputTokens: "-1", EnvMaxSliceCost: "NaN",
		EnvTheme: "unknown", EnvRunHeader: "unknown",
	}
	for _, v := range runtimeEnvVars {
		if strings.HasPrefix(v.name, "TAO_BUDGET_") {
			invalid[v.name] = "-1"
		}
	}
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
		if presentation {
			if err != nil || row.Source != "default" || !strings.Contains(row.Warning, "using default") {
				t.Errorf("presentation: %+v, %v", row, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), row.Name) || row.Source != "invalid" {
			t.Errorf("rejection: %+v, %v", row, err)
		}
	}
	err := s.Require(EnvAgent, EnvMaxSliceCost)
	if err == nil || !strings.Contains(err.Error(), EnvAgent) || !strings.Contains(err.Error(), EnvMaxSliceCost) || strings.Contains(err.Error(), EnvUpdate) {
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
		{EnvMaxSliceCost, "Inf", "non-negative decimal"}, {EnvBudgetPlanCost, "NaN", "non-negative decimal"}, {EnvBudgetSliceToolCalls, "1.5", "non-negative integer"}, {EnvUpdate, "bad", "warn, auto, or off"},
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
	values := map[string]string{EnvSessionTimeout: "0", EnvMaxReworkAttempts: "0", EnvMaxSliceOutputTokens: "0", EnvMaxSliceCost: "0", EnvPlannerRouting: " \t", EnvPlannerRoutingArms: " pi=1 ", EnvPlannerRoutingFloor: " 0.2 "}
	for _, v := range runtimeEnvVars {
		if strings.HasPrefix(v.name, "TAO_BUDGET_") {
			values[v.name] = "0"
		}
	}
	s := snapshotFrom(values)
	if err := s.Require(RuntimeEnvKeys()...); err != nil {
		t.Fatal(err)
	}
	d := s.Defaults()
	if d.SessionTimeout == nil || *d.SessionTimeout != 0 || d.MaxReworkAttempts == nil || *d.MaxReworkAttempts != 0 || d.SliceBudgetCaps.OutputTokens == nil || *d.SliceBudgetCaps.OutputTokens != 0 || d.SliceBudgetCaps.Cost == nil || *d.SliceBudgetCaps.Cost != 0 || d.AgentBudgetThresholds != (plan.AgentBudgetThresholds{}) {
		t.Fatalf("lost explicit zero: %+v", d)
	}
	wantRouting := PlannerRoutingConfig{Mode: "off", Arms: []PlannerRoutingArm{{AgentPi, 1}}, Floor: 0.2, ArmsSet: true, FloorSet: true}
	if !reflect.DeepEqual(d.PlannerRouting, wantRouting) {
		t.Fatalf("routing: %+v", d.PlannerRouting)
	}
}

func TestEnvSnapshotLookupOnceAndDefensiveCopies(t *testing.T) {
	values := map[string]string{EnvPullRequest: "true", EnvReview: "false", EnvAutoRework: "false", EnvMaxReworkAttempts: "7", EnvReworkEscalationFromAttempt: "6", EnvMaxSliceOutputTokens: "100", EnvMaxSliceCost: "1.5", EnvAgent: "bad"}
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
		*d.SliceBudgetCaps.OutputTokens = 999
		*d.SliceBudgetCaps.Cost = 999
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
		EnvRunModel: " provider/run ", EnvApprovedBy: "bot", EnvBudgetPlanCost: "0",
	})
	d := s.Defaults()
	if d.Agent != AgentPi || d.UpdateMode != selfupdate.ModeWarn || d.Theme != theme.Default() || d.RunModel != "provider/run" || d.ApprovedBy != "bot" || d.AgentBudgetThresholds.Plan.Cost != 0 {
		t.Fatalf("mixed defaults: %+v", d)
	}
	if s.Require(EnvRunModel, EnvApprovedBy, EnvBudgetPlanCost, EnvTheme) != nil || s.Require(EnvAgent, EnvUpdate) == nil {
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
