package runtimeconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/configtypes"
)

func settingLayer(key, value string) configtypes.SettingsValues {
	return configtypes.SettingsValues{key: json.RawMessage(value)}
}
func capturedSettings(values map[string]string) EnvSnapshot {
	return LoadEnv(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
}
func settingStatus(t *testing.T, s EnvSnapshot, key string) SettingStatus {
	t.Helper()
	for _, row := range s.SettingsStatus() {
		if row.Key == key {
			return row
		}
	}
	t.Fatalf("missing %s", key)
	return SettingStatus{}
}

func TestEffortInvalidSavedLayerSurvivesMasking(t *testing.T) {
	for _, global := range []bool{false, true} {
		bad := settingLayer("models.run_effort", `"two levels"`)
		var g, r configtypes.SettingsValues
		if global {
			g = bad
		} else {
			r = bad
		}
		s := ResolveSettings(g, r, capturedSettings(map[string]string{EnvRunEffort: "high"}))
		if s.Defaults().RunEffort != "high" {
			t.Fatal(s.Defaults())
		}
		if err := s.Require(EnvRunEffort); err == nil {
			t.Fatal("masked invalid saved effort")
		}
		if row := settingStatus(t, s, "models.run_effort"); row.Warning == "" {
			t.Fatal(row)
		}
	}
}

func TestResolveSettingsSparseCapture(t *testing.T) {
	global := settingLayer("pull_request", `true`)
	global["max_slices"] = json.RawMessage(`0`)
	s := ResolveSettings(global, nil, LoadEnv(nil))
	if !s.Defaults().PullRequestValue() || s.Defaults().MaxSlices == nil || *s.Defaults().MaxSlices != 0 {
		t.Fatal(s.Defaults())
	}
	global["pull_request"][0] = 'x'
	d := s.Defaults()
	*d.PullRequest = false
	rows := s.SettingsStatus()
	rows[0].Value = "mutated"
	if !s.Defaults().PullRequestValue() {
		t.Fatal("mutable snapshot")
	}
	s = ResolveSettings(nil, settingLayer("pull_request", `false`), s)
	if s.Defaults().PullRequestValue() {
		t.Fatal("composed defaults promoted to environment")
	}
}

func TestResolveSettingsPrecedence(t *testing.T) {
	cases := []struct{ key, a, b, env string }{
		{"pull_request", `true`, `false`, "true"},
		{"review_enabled", `false`, `true`, "false"},
		{"agent", `"claude"`, `"pi"`, "claude"},
		{"execution_mode", `"current"`, `"isolated"`, "current"},
		{"commit_policy", `"none"`, `"slice"`, "none"},
		{"session_timeout", `"1m"`, `"0s"`, "2m"},
		{"session_warn_percent", `50`, `0`, "60"},
		{"review_agent", `"pi"`, `"claude"`, "pi"},
		{"max_rework_attempts", `2`, `3`, "4"},
		{"rework_escalation_from_attempt", `2`, `3`, "4"},
		{"dangerously_skip_permissions", `true`, `false`, "true"},
		{"run_header", `false`, `true`, "false"},
		{"models.model", `"base-a"`, `"base-b"`, "base-c"},
		{"models.run_effort", `"low"`, `"medium"`, "high"},
		{"models.run_model", `"run-a"`, `"run-b"`, "run-c"},
		{"models.review_model", `"review-a"`, `"review-b"`, "review-c"},
		{"models.merge_review_model", `"merge-a"`, `"merge-b"`, "merge-c"},
		{"models.resolver_model", `"resolve-a"`, `"resolve-b"`, "resolve-c"},
		{"models.rework_escalation_model", `"rework-a"`, `"rework-b"`, "rework-c"},
		{"budget.slice.output_tokens.warn", `100`, `0`, "200"},
		{"budget.slice.cost.stop", `10`, `null`, "20"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			def, err := settingDefinition(tc.key)
			if err != nil {
				t.Fatal(err)
			}
			for mask := 1; mask < 8; mask++ {
				var g, r configtypes.SettingsValues
				var env EnvSnapshot
				want, source := "", ""
				if mask&1 != 0 {
					g = settingLayer(tc.key, tc.a)
					want = tc.a
					source = "global"
				}
				if mask&2 != 0 {
					r = settingLayer(tc.key, tc.b)
					want = tc.b
					source = "repository"
				}
				if mask&4 != 0 {
					env = capturedSettings(map[string]string{def.EnvKey: tc.env})
					want = tc.env
					source = "env"
				}
				s := ResolveSettings(g, r, env)
				if err := s.Require(def.EnvKey); err != nil {
					t.Fatal(err)
				}
				row := settingStatus(t, s, tc.key)
				canonical, err := ParseSetting(tc.key, settingText(json.RawMessage(want)))
				if err != nil {
					t.Fatal(err)
				}
				display := settingText(canonical)
				if tc.key != "budget.slice.cost.stop" || display != "null" {
					if row.Value != display {
						t.Fatalf("mask %d: %s != %s", mask, row.Value, display)
					}
				}
				if row.Source != source {
					t.Fatalf("mask %d: %+v", mask, row)
				}
				if mask&1 != 0 && row.GlobalValue == "" || mask&2 != 0 && row.RepositoryValue == "" {
					t.Fatal(row)
				}
			}
		})
	}
}

func TestResolveSettingsAdmissionAndBudgets(t *testing.T) {
	const warn = "budget.slice.output_tokens.warn"
	const stop = "budget.slice.output_tokens.stop"
	g := settingLayer(warn, `100`)
	g[stop] = json.RawMessage(`50`)
	s := ResolveSettings(g, settingLayer(stop, `200`), EnvSnapshot{})
	if _, err := s.Budget(); err != nil {
		t.Fatal(err)
	}
	s = ResolveSettings(g, settingLayer(warn, `20`), EnvSnapshot{})
	if _, err := s.Budget(); err != nil {
		t.Fatal(err)
	}
	s = ResolveSettings(g, nil, EnvSnapshot{})
	if _, err := s.Budget(); err == nil {
		t.Fatal("invalid final relationship admitted")
	}
	if err := s.Require(EnvAgent); err != nil {
		t.Fatal(err)
	}
	s = ResolveSettings(g, settingLayer(stop, `null`), EnvSnapshot{})
	if _, err := s.Budget(); err != nil {
		t.Fatal(err)
	}
	s = ResolveSettings(settingLayer(stop, `"bad"`), settingLayer(stop, `200`), EnvSnapshot{})
	if _, err := s.Budget(); err == nil {
		t.Fatal("masked invalid layer admitted")
	}
	row := settingStatus(t, s, stop)
	if row.Source != "invalid" || !strings.Contains(row.Warning, "global") {
		t.Fatal(row)
	}
	s = ResolveSettings(settingLayer("max_slices", `-1`), settingLayer("max_slices", `0`), EnvSnapshot{})
	if s.Require("max_slices") == nil || s.Require(EnvAgent) != nil {
		t.Fatal("max_slices admission")
	}
	// Environment-only invalid relationships can be repaired by saved WARN values.
	env := capturedSettings(map[string]string{EnvBudgetSliceOutputTokensStop: "50"})
	s = ResolveSettings(settingLayer(warn, `20`), nil, env)
	if _, err := s.Budget(); err != nil {
		t.Fatal(err)
	}
	for _, values := range []map[string]string{
		{EnvBudgetSliceOutputTokensStop: "", "TAO_MAX_SLICE_OUTPUT_TOKENS": "bad"},
		{"TAO_MAX_SLICE_OUTPUT_TOKENS": "200"},
	} {
		s = ResolveSettings(g, nil, capturedSettings(values))
		if _, err := s.Budget(); err != nil {
			t.Fatal(err)
		}
	}
	s = ResolveSettings(nil, nil, capturedSettings(map[string]string{"TAO_MAX_SLICE_OUTPUT_TOKENS": "bad"}))
	if _, err := s.Budget(); err == nil {
		t.Fatal("invalid alias admitted")
	}
}

func TestResolveSettingsGlobalOnlyAndZero(t *testing.T) {
	for _, tc := range []struct{ key, saved, env string }{
		{"theme", `"default"`, "default"}, {"update", `"off"`, "warn"},
	} {
		def, _ := settingDefinition(tc.key)
		s := ResolveSettings(settingLayer(tc.key, tc.saved), nil, capturedSettings(map[string]string{def.EnvKey: tc.env}))
		if row := settingStatus(t, s, tc.key); row.Source != "env" || row.GlobalValue == "" {
			t.Fatal(row)
		}
		s = ResolveSettings(nil, settingLayer(tc.key, tc.saved), EnvSnapshot{})
		if row := settingStatus(t, s, tc.key); row.Warning == "" {
			t.Fatal("repository scope accepted", row)
		}
	}
	s := ResolveSettings(settingLayer("max_slices", `3`), settingLayer("max_slices", `0`), EnvSnapshot{})
	if *s.Defaults().MaxSlices != 0 || settingStatus(t, s, "max_slices").Source != "repository" {
		t.Fatal(s.Defaults())
	}
	g := settingLayer("budget.slice.output_tokens.warn", `0`)
	g["budget.slice.output_tokens.stop"] = json.RawMessage(`0`)
	s = ResolveSettings(g, nil, EnvSnapshot{})
	budget, err := s.Budget()
	if err != nil || budget.Slice.OutputTokens.Stop == nil || *budget.Slice.OutputTokens.Stop != 0 {
		t.Fatal(budget, err)
	}
	*budget.Slice.OutputTokens.Stop = 9
	if *s.Defaults().Budget.Slice.OutputTokens.Stop != 0 {
		t.Fatal("mutable budget")
	}
	// A masked syntactically invalid environment value is never discarded.
	s = ResolveSettings(settingLayer("agent", `"pi"`), nil, capturedSettings(map[string]string{EnvAgent: "bad"}))
	if s.Require(EnvAgent) == nil || settingStatus(t, s, "agent").Source != "invalid" {
		t.Fatal("invalid env admitted")
	}
}

func TestResolveSettingsPresentationAndInvocation(t *testing.T) {
	g := settingLayer("run_header", `false`)
	g["update"] = json.RawMessage(`"off"`)
	g["theme"] = json.RawMessage(`"bad-theme"`)
	s := ResolveSettings(g, nil, capturedSettings(map[string]string{EnvRunHeader: "bad"}))
	if s.Defaults().RunHeader || s.Require(EnvRunHeader, EnvTheme) != nil {
		t.Fatal("presentation fallback")
	}
	row := settingStatus(t, s, "run_header")
	if row.Source != "global" || row.Warning == "" {
		t.Fatal(row)
	}
	if settingStatus(t, s, "theme").Source != "default" || settingStatus(t, s, "update").Source != "global" {
		t.Fatal(s.SettingsStatus())
	}
	s = ResolveSettings(settingLayer("models.model", `"base"`), settingLayer("models.run_model", `"run"`), EnvSnapshot{})
	opts, err := ResolveRunOptions(s.Defaults().RunOptionsPatch, RunOptionsPatch{}.WithMaxSlices(0).WithPullRequest(false))
	if err != nil || opts.MaxSlices != 0 || opts.PullRequest {
		t.Fatal(opts, err)
	}
	if opts.Models.Base != "base" || opts.Models.Run != "run" || opts.Models.For(ModelRoleReview) != "base" {
		t.Fatal(opts.Models)
	}
	opts, err = ResolveRunOptions(s.Defaults().RunOptionsPatch, RunOptionsPatch{}.WithModelForAllRoles("invocation"))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []ModelRole{ModelRoleDefault, ModelRoleRun, ModelRoleReview, ModelRoleMergeReview, ModelRoleResolver} {
		if opts.Models.For(role) != "invocation" {
			t.Fatal(opts.Models)
		}
	}
}
