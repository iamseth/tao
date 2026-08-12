package runtimeconfig

import (
	"testing"
	"time"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  Mode
	}{
		{name: "default", want: ModeRun},
		{name: "run", value: "run", want: ModeRun},
		{name: "step", value: "step", want: ModeStep},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMode(tt.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseMode(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseModeRejectsUnsupportedValue(t *testing.T) {
	_, err := ParseMode("other")
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
}

func TestParseCommitPolicy(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  CommitPolicy
	}{
		{name: "default", want: CommitPolicySlice},
		{name: "slice", value: "slice", want: CommitPolicySlice},
		{name: "none", value: "none", want: CommitPolicyNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCommitPolicy(tt.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseCommitPolicy(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseCommitPolicyRejectsUnsupportedValue(t *testing.T) {
	_, err := ParseCommitPolicy("always")
	if err == nil {
		t.Fatal("expected unsupported policy error")
	}
}

func TestParseCommitPolicyRejectsRemovedPlanPolicy(t *testing.T) {
	_, err := ParseCommitPolicy("plan")
	if err == nil || err.Error() != "commit policy plan was removed; use slice or none" {
		t.Fatalf("expected plan migration error, got %v", err)
	}
}

func TestParseExecutionMode(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  ExecutionMode
	}{
		{name: "default", want: ExecutionModeIsolated},
		{name: "isolated", value: "isolated", want: ExecutionModeIsolated},
		{name: "current", value: "current", want: ExecutionModeCurrent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExecutionMode(tt.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseExecutionMode(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseExecutionModeRejectsUnsupportedValue(t *testing.T) {
	_, err := ParseExecutionMode("sandbox")
	if err == nil || err.Error() != "unsupported execution mode \"sandbox\" (want isolated or current)" {
		t.Fatalf("expected unsupported execution mode error, got %v", err)
	}
}

// TestResolveRunOptionsResolvesExecutionMode verifies that an explicit
// ExecutionMode override is honored and that an unset mode falls back to the
// isolated default.
func TestResolveRunOptionsResolvesExecutionMode(t *testing.T) {
	isolated, err := ResolveRunOptions(RunOptionsPatch{}, RunOptionsPatch{ExecutionMode: ExecutionModeIsolated})
	if err != nil {
		t.Fatal(err)
	}
	if isolated.ExecutionMode != ExecutionModeIsolated {
		t.Fatalf("expected isolated execution mode, got %#v", isolated)
	}

	current, err := ResolveRunOptions(RunOptionsPatch{}, RunOptionsPatch{ExecutionMode: ExecutionModeCurrent})
	if err != nil {
		t.Fatal(err)
	}
	if current.ExecutionMode != ExecutionModeCurrent {
		t.Fatalf("expected current execution mode, got %#v", current)
	}

	fallback, err := ResolveRunOptions(RunOptionsPatch{}, RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.ExecutionMode != ExecutionModeIsolated {
		t.Fatalf("expected isolated default execution mode, got %#v", fallback)
	}
}

// TestResolveRunOptionsExecutionModeOverrideWinsOverDefault verifies the default
// layer can supply ExecutionMode and that an explicit override mode wins.
func TestResolveRunOptionsExecutionModeOverrideWinsOverDefault(t *testing.T) {
	options, err := ResolveRunOptions(RunOptionsPatch{ExecutionMode: ExecutionModeCurrent}, RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if options.ExecutionMode != ExecutionModeCurrent {
		t.Fatalf("expected default current mode to be honored, got %#v", options)
	}

	override, err := ResolveRunOptions(RunOptionsPatch{ExecutionMode: ExecutionModeCurrent}, RunOptionsPatch{ExecutionMode: ExecutionModeIsolated})
	if err != nil {
		t.Fatal(err)
	}
	if override.ExecutionMode != ExecutionModeIsolated {
		t.Fatalf("expected override mode to win over default mode, got %#v", override)
	}
}

func TestParseAgentKind(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  AgentKind
	}{
		{name: "default", want: AgentPi},
		{name: "pi", value: "pi", want: AgentPi},
		{name: "claude", value: "claude", want: AgentClaude},
		{name: "opencode", value: "opencode", want: AgentOpenCode},
		{name: "codex", value: "codex", want: AgentCodex},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAgentKind(tt.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseAgentKind(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseAgentKindRejectsUnsupportedValue(t *testing.T) {
	for _, value := range []string{"legacy-agent", "other"} {
		_, err := ParseAgentKind(value)
		if err == nil || err.Error() != "unsupported agent \""+value+"\" (want pi, claude, opencode, or codex)" {
			t.Fatalf("expected unsupported agent error for %q, got %v", value, err)
		}
	}
}

func TestResolveRunOptionsAppliesBuiltInDefaults(t *testing.T) {
	options, err := ResolveRunOptions(RunOptionsPatch{}, RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if options.Mode != ModeRun || options.MaxSlices != 0 || options.Continue || options.CommitPolicy != CommitPolicySlice || options.ExecutionMode != ExecutionModeIsolated || options.Agent != AgentPi || options.PullRequest || !options.ReviewEnabled || options.SessionTimeout != DefaultSessionTimeout {
		t.Fatalf("unexpected built-in defaults: %#v", options)
	}
}

func TestResolveRunOptionsWithRepositoryDefaultsPullRequestPrecedence(t *testing.T) {
	states := []struct {
		name  string
		value *bool
	}{
		{name: "unset"},
		{name: "true", value: new(true)},
		{name: "false", value: new(false)},
	}

	for _, defaults := range states {
		for _, repository := range states {
			for _, overrides := range states {
				name := "defaults=" + defaults.name + "/repository=" + repository.name + "/overrides=" + overrides.name
				t.Run(name, func(t *testing.T) {
					options, err := ResolveRunOptionsWithRepositoryDefaults(
						RunOptionsPatch{PullRequest: defaults.value},
						RunOptionsPatch{PullRequest: repository.value},
						RunOptionsPatch{PullRequest: overrides.value},
					)
					if err != nil {
						t.Fatal(err)
					}

					want := false
					switch {
					case overrides.value != nil:
						want = *overrides.value
					case repository.value != nil:
						want = *repository.value
					case defaults.value != nil:
						want = *defaults.value
					}
					if options.PullRequest != want {
						t.Fatalf("PullRequest = %t, want %t", options.PullRequest, want)
					}
				})
			}
		}
	}
}

func TestResolveRunOptionsWithRepositoryDefaultsExplicitFalseOverrideWins(t *testing.T) {
	options, err := ResolveRunOptionsWithRepositoryDefaults(
		RunOptionsPatch{},
		RunOptionsPatch{}.WithPullRequest(true),
		RunOptionsPatch{}.WithPullRequest(false),
	)
	if err != nil {
		t.Fatal(err)
	}
	if options.PullRequest {
		t.Fatalf("expected explicit false override to beat repository default, got %#v", options)
	}
}

func TestResolveRunOptionsReviewEnabledDefaultAndOverrides(t *testing.T) {
	enabled, err := ResolveRunOptions(RunOptionsPatch{}, RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.ReviewEnabled {
		t.Fatalf("expected review enabled by default, got %#v", enabled)
	}

	disabledDefault, err := ResolveRunOptions(DefaultRunOptionsPatch().WithReviewEnabled(false), RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if disabledDefault.ReviewEnabled {
		t.Fatalf("expected staged default to disable review, got %#v", disabledDefault)
	}

	override, err := ResolveRunOptions(DefaultRunOptionsPatch().WithReviewEnabled(true), RunOptionsPatch{}.WithReviewEnabled(false))
	if err != nil {
		t.Fatal(err)
	}
	if override.ReviewEnabled {
		t.Fatalf("expected explicit override to disable review, got %#v", override)
	}
}

func TestResolveRunOptionsSessionTimeoutDefaultAndOverrides(t *testing.T) {
	options, err := ResolveRunOptions(RunOptionsPatch{}, RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if options.SessionTimeout != 20*time.Minute {
		t.Fatalf("expected 20-minute default session timeout, got %s", options.SessionTimeout)
	}

	options, err = ResolveRunOptions(DefaultRunOptionsPatch().WithSessionTimeout(30*time.Minute), RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if options.SessionTimeout != 30*time.Minute {
		t.Fatalf("expected staged session timeout default, got %s", options.SessionTimeout)
	}

	options, err = ResolveRunOptions(DefaultRunOptionsPatch().WithSessionTimeout(30*time.Minute), RunOptionsPatch{}.WithSessionTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	if options.SessionTimeout != 0 {
		t.Fatalf("expected override to disable session timeout, got %s", options.SessionTimeout)
	}
}

func TestResolveRunOptionsAppliesStagedDefaultsAndOverrides(t *testing.T) {
	options, err := ResolveRunOptions(RunOptionsPatch{
		Mode:          ModeRun,
		MaxSlices:     new(6),
		Continue:      new(true),
		CommitPolicy:  CommitPolicySlice,
		ExecutionMode: ExecutionModeIsolated,
		Agent:         AgentClaude,
		PullRequest:   new(true),
		ReviewEnabled: new(true),
	}, RunOptionsPatch{
		Mode:          ModeStep,
		Continue:      new(false),
		CommitPolicy:  CommitPolicySlice,
		ExecutionMode: ExecutionModeCurrent,
		Agent:         AgentPi,
		PullRequest:   new(false),
		ReviewEnabled: new(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.Mode != ModeStep || options.MaxSlices != 1 || options.Continue || options.PullRequest || options.ReviewEnabled {
		t.Fatalf("expected mode-derived max slices and explicit false overrides, got %#v", options)
	}
	if options.CommitPolicy != CommitPolicySlice || options.ExecutionMode != ExecutionModeCurrent || options.Agent != AgentPi {
		t.Fatalf("expected request overrides to win, got %#v", options)
	}
}

func TestResolveRunOptionsModeAndMaxSlicesPrecedence(t *testing.T) {
	options, err := ResolveRunOptions(RunOptionsPatch{MaxSlices: new(4)}, RunOptionsPatch{Mode: ModeStep})
	if err != nil {
		t.Fatal(err)
	}
	if options.Mode != ModeStep || options.MaxSlices != 1 {
		t.Fatalf("expected step mode to derive one slice, got %#v", options)
	}

	options, err = ResolveRunOptions(DefaultRunOptionsPatch(), RunOptionsPatch{Mode: ModeStep}.WithMaxSlices(3))
	if err != nil {
		t.Fatal(err)
	}
	if options.Mode != ModeStep || options.MaxSlices != 3 {
		t.Fatalf("expected explicit max-slices override to win, got %#v", options)
	}
}

func TestResolveRunOptionsRejectsInvalidStagedValues(t *testing.T) {
	tests := []struct {
		name      string
		defaults  RunOptionsPatch
		overrides RunOptionsPatch
		want      string
	}{
		{name: "default agent", defaults: RunOptionsPatch{Agent: "other"}, want: "unsupported agent \"other\" (want pi, claude, opencode, or codex)"},
		{name: "override execution mode", overrides: RunOptionsPatch{ExecutionMode: ExecutionMode("sandbox")}, want: "unsupported execution mode \"sandbox\" (want isolated or current)"},
		{name: "negative max slices", overrides: RunOptionsPatch{}.WithMaxSlices(-1), want: "--max-slices must be 0 or greater"},
		{name: "pull request step", overrides: RunOptionsPatch{Mode: ModeStep, CommitPolicy: CommitPolicySlice, PullRequest: new(true)}, want: "--pull-request requires full run mode"},
		{name: "pull request commit none", overrides: RunOptionsPatch{Mode: ModeRun, CommitPolicy: CommitPolicyNone, PullRequest: new(true)}, want: "--pull-request requires commit policy slice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveRunOptions(tt.defaults, tt.overrides)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestNewConfigFromStagesBuildsResolvedOptions(t *testing.T) {
	config, err := NewConfigFromStages(RunOptionsPatch{ExecutionMode: ExecutionModeIsolated}, RunOptionsPatch{ExecutionMode: ExecutionModeCurrent}.WithPullRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	resolved := config.ResolvedOptions()
	if resolved.ExecutionMode != ExecutionModeCurrent || resolved.PullRequest {
		t.Fatalf("unexpected resolved options: %#v", resolved)
	}
}

func TestRunOptionsPatchHelpersPreserveOptionalValues(t *testing.T) {
	defaults := DefaultRunOptionsPatch()
	if defaults.ExecutionModeValue() != ExecutionModeIsolated || defaults.PullRequestValue() || !defaults.ReviewEnabledValue() || defaults.SessionTimeoutValue() != DefaultSessionTimeout {
		t.Fatalf("unexpected built-in default values: %#v", defaults)
	}

	defaults = defaults.WithPullRequest(false).WithReviewEnabled(false).WithContinue(false).WithMaxSlices(2).WithSessionTimeout(0)
	if defaults.PullRequest == nil || defaults.PullRequestValue() || defaults.ReviewEnabled == nil || defaults.ReviewEnabledValue() || defaults.Continue == nil || *defaults.Continue || defaults.MaxSlices == nil || *defaults.MaxSlices != 2 || defaults.SessionTimeout == nil || defaults.SessionTimeoutValue() != 0 {
		t.Fatalf("expected optional default pointers to preserve explicit values, got %#v", defaults)
	}
}

// TestResolvedRunOptionsRunOptionsPatchReappliesOnDefaults verifies that projecting
// resolved options back to the override layer and re-merging on top of a
// service's defaults reproduces the resolved options. This is the path the run
// service uses to re-apply a resolved request over its own defaults.
func TestResolvedRunOptionsRunOptionsPatchReappliesOnDefaults(t *testing.T) {
	resolved, err := ResolveRunOptions(DefaultRunOptionsPatch(), RunOptionsPatch{
		Mode:          ModeStep,
		CommitPolicy:  CommitPolicySlice,
		ExecutionMode: ExecutionModeCurrent,
		Agent:         AgentClaude,
	})
	if err != nil {
		t.Fatal(err)
	}

	reapplied, err := ResolveRunOptions(RunOptionsPatch{Agent: AgentPi}, resolved.RunOptionsPatch())
	if err != nil {
		t.Fatal(err)
	}
	if reapplied != resolved {
		t.Fatalf("expected re-applied overrides to reproduce resolved options\n got %#v\nwant %#v", reapplied, resolved)
	}
}

// TestResolvedRunOptionsRunOptionsPatchProjectsExecutionMode verifies the resolved
// execution mode survives projection back to the override layer so a re-applied
// request keeps its mode over a service default.
func TestResolvedRunOptionsRunOptionsPatchProjectsExecutionMode(t *testing.T) {
	resolved, err := ResolveRunOptions(DefaultRunOptionsPatch(), RunOptionsPatch{ExecutionMode: ExecutionModeCurrent})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RunOptionsPatch().ExecutionMode != ExecutionModeCurrent {
		t.Fatal("expected projected overrides to carry the resolved execution mode")
	}

	reapplied, err := ResolveRunOptions(RunOptionsPatch{ExecutionMode: ExecutionModeIsolated}, resolved.RunOptionsPatch())
	if err != nil {
		t.Fatal(err)
	}
	if reapplied.ExecutionMode != ExecutionModeCurrent {
		t.Fatalf("expected request execution mode to win over service default, got %#v", reapplied)
	}
}

func TestResolvedRunOptionsRunOptionsPatchRoundTrip(t *testing.T) {
	resolved, err := ResolveRunOptions(DefaultRunOptionsPatch().WithMaxSlices(3), RunOptionsPatch{ExecutionMode: ExecutionModeCurrent})
	if err != nil {
		t.Fatal(err)
	}
	defaults := resolved.RunOptionsPatch()
	if defaults.MaxSlices == nil || *defaults.MaxSlices != 3 || defaults.ExecutionMode != ExecutionModeCurrent || defaults.ReviewEnabled == nil || !*defaults.ReviewEnabled || defaults.SessionTimeout == nil || *defaults.SessionTimeout != DefaultSessionTimeout {
		t.Fatalf("expected resolved values to project back into defaults, got %#v", defaults)
	}
}
