package runtimeconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/configtypes"
)

func commandSnapshot(values map[string]string) EnvSnapshot {
	return LoadEnv(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
}

// composedSnapshot layers raw JSON global and repository values beneath the
// captured environment exactly as the settings service does for commands.
func composedSnapshot(env, global, repository map[string]string) EnvSnapshot {
	layer := func(values map[string]string) configtypes.SettingsValues {
		if len(values) == 0 {
			return nil
		}
		out := configtypes.SettingsValues{}
		for key, value := range values {
			out[key] = json.RawMessage(value)
		}
		return out
	}
	return ResolveSettings(layer(global), layer(repository), commandSnapshot(env))
}

func TestCommandOptionsPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name               string
		env                string
		global, repository map[string]string
		flags              RunOptionsPatch
		want               bool
		source             string
	}{
		{"environment", "true", nil, nil, RunOptionsPatch{}, true, "env"},
		{"global", "", map[string]string{"pull_request": "true"}, nil, RunOptionsPatch{}, true, "global"},
		{"repository beats global", "", map[string]string{"pull_request": "true"}, map[string]string{"pull_request": "false"}, RunOptionsPatch{}, false, "repository"},
		{"environment masks repository", "true", nil, map[string]string{"pull_request": "false"}, RunOptionsPatch{}, true, "env"},
		{"explicit false", "true", nil, map[string]string{"pull_request": "true"}, RunOptionsPatch{}.WithPullRequest(false), false, "flag"},
		{"same as environment", "true", nil, map[string]string{"pull_request": "false"}, RunOptionsPatch{}.WithPullRequest(true), true, "flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			if tc.env != "" {
				env[EnvPullRequest] = tc.env
			}
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: composedSnapshot(env, tc.global, tc.repository), Flags: tc.flags})
			if err != nil {
				t.Fatal(err)
			}
			if got.RunOptions.PullRequest != tc.want || got.Sources[EnvPullRequest] != tc.source {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestCommandOptionsMaxSlicesSource(t *testing.T) {
	got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: composedSnapshot(nil, map[string]string{"max_slices": "3"}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunOptions.MaxSlices != 3 || got.Sources["--max-slices"] != "global" {
		t.Fatalf("got %+v", got)
	}
	got, err = ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: composedSnapshot(nil, map[string]string{"max_slices": "3"}, nil), Flags: RunOptionsPatch{}.WithMaxSlices(1)})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunOptions.MaxSlices != 1 || got.Sources["--max-slices"] != "flag" {
		t.Fatalf("got %+v", got)
	}
}

func TestCommandOptionsRejectsInvalidSavedMaxSlices(t *testing.T) {
	for _, scope := range []string{"global", "repository"} {
		t.Run(scope, func(t *testing.T) {
			layer := map[string]string{"max_slices": "-3"}
			var env EnvSnapshot
			if scope == "global" {
				env = composedSnapshot(nil, layer, nil)
			} else {
				env = composedSnapshot(nil, nil, layer)
			}
			_, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: env})
			if err == nil || !strings.Contains(err.Error(), "max_slices") {
				t.Fatalf("invalid saved limit admitted as all slices: %v", err)
			}
			// Inapplicable profiles ignore the saved limit entirely.
			if _, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandReview, Env: env}); err != nil {
				t.Fatalf("review consumed max_slices: %v", err)
			}
		})
	}
}

func TestCommandOptionsReviewAgent(t *testing.T) {
	for _, profile := range []CommandProfile{CommandRun, CommandReview} {
		for _, tc := range []struct {
			name       string
			env        string
			repository string
			flag, want AgentKind
			source     string
		}{
			{"inherited", "", "", "", "", "default"},
			{"environment", "claude", "", "", AgentClaude, "env"},
			{"repository", "", `"pi"`, "", AgentPi, "repository"},
			{"environment masks repository", "claude", `"pi"`, "", AgentClaude, "env"},
			{"flag", "claude", `"pi"`, AgentPi, AgentPi, "flag"},
		} {
			t.Run(string(profile)+"/"+tc.name, func(t *testing.T) {
				var repository map[string]string
				if tc.repository != "" {
					repository = map[string]string{"review_agent": tc.repository}
				}
				got, err := ResolveCommandOptions(CommandOptionsInput{Profile: profile,
					Env:   composedSnapshot(map[string]string{EnvReviewAgent: tc.env}, nil, repository),
					Flags: RunOptionsPatch{ReviewAgent: tc.flag}})
				if err != nil {
					t.Fatal(err)
				}
				if got.RunOptions.ReviewAgent != tc.want || got.Sources[EnvReviewAgent] != tc.source {
					t.Fatalf("got %+v", got)
				}
			})
		}
		for _, env := range []EnvSnapshot{
			commandSnapshot(map[string]string{EnvReviewAgent: "invalid"}),
			composedSnapshot(nil, nil, map[string]string{"review_agent": `"invalid"`}),
		} {
			if _, err := ResolveCommandOptions(CommandOptionsInput{Profile: profile, Env: env}); err == nil {
				t.Fatal("invalid reviewer admitted")
			}
		}
	}
	if _, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandMerge, Env: commandSnapshot(map[string]string{EnvReviewAgent: "invalid"})}); err != nil {
		t.Fatal(err)
	}
}

func TestCommandOptionsApplicability(t *testing.T) {
	for _, profile := range []CommandProfile{CommandReview, CommandMerge, CommandPromptRun, CommandPromptOther} {
		t.Run(string(profile), func(t *testing.T) {
			env := map[string]string{EnvPullRequest: "invalid", EnvAutoRework: "invalid", EnvPlannerRouting: "invalid", EnvBudgetSliceCostStop: "invalid"}
			if profile != CommandPromptRun {
				env[EnvCommitPolicy] = "invalid"
			}
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: profile, Env: composedSnapshot(env, nil, map[string]string{"pull_request": "true"})})
			if err != nil {
				t.Fatal(err)
			}
			if got.RunOptions.PullRequest {
				t.Fatal("irrelevant repository PR admitted")
			}
		})
	}
}

func TestCommandOptionsCrossFieldSources(t *testing.T) {
	_, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: composedSnapshot(map[string]string{EnvCommitPolicy: "none"}, nil, map[string]string{"pull_request": "true"})})
	if err == nil {
		t.Fatal("expected PR conflict")
	}
	for _, text := range []string{EnvCommitPolicy, "env", EnvPullRequest, "repository"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("missing %q: %v", text, err)
		}
	}
}

func TestCommandOptionsAuxiliary(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name                    string
		aux                     CommandAuxiliaryFlags
		flags                   RunOptionsPatch
		wantEnabled, wantHeader bool
		wantErr                 bool
	}{
		{"inherited", CommandAuxiliaryFlags{}, RunOptionsPatch{}, true, true, false},
		{"explicit false", CommandAuxiliaryFlags{AutoRework: &no, NoRunHeader: &no}, RunOptionsPatch{}, false, false, false},
		{"implicit normalization", CommandAuxiliaryFlags{}, RunOptionsPatch{}.WithReviewEnabled(false), false, true, false},
		{"explicit normalization", CommandAuxiliaryFlags{AutoRework: &yes}, RunOptionsPatch{}.WithReviewEnabled(false), false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: commandSnapshot(map[string]string{EnvRunHeader: "false", EnvMaxReworkAttempts: "3", EnvReworkEscalationFromAttempt: "2", EnvSkipPermissions: "true"}), Flags: tc.flags, Auxiliary: tc.aux})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "flag") {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.AutoRework.Enabled != tc.wantEnabled || got.NoRunHeader != tc.wantHeader || got.AutoRework.MaxAttempts != map[bool]int{true: 3, false: 0}[tc.wantEnabled] || got.ReworkEscalationFromAttempt != 2 || !got.SkipPermissions {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestCommandOptionsNumericReworkPrecedence(t *testing.T) {
	repository := map[string]string{"max_rework_attempts": "2", "rework_escalation_from_attempt": "6"}
	for _, tc := range []struct {
		name   string
		env    map[string]string
		aux    CommandAuxiliaryFlags
		want   int
		source string
	}{
		{"repository", nil, CommandAuxiliaryFlags{}, 2, "repository"},
		{"environment masks repository", map[string]string{EnvMaxReworkAttempts: "7"}, CommandAuxiliaryFlags{}, 7, "env"},
		{"explicit zero", map[string]string{EnvMaxReworkAttempts: "7"}, CommandAuxiliaryFlags{MaxReworkAttempts: new(0)}, 0, "flag"},
		{"count beats alias", nil, CommandAuxiliaryFlags{MaxReworkAttempts: new(3), AutoRework: new(false)}, 3, "flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: composedSnapshot(tc.env, nil, repository), Auxiliary: tc.aux})
			if err != nil {
				t.Fatal(err)
			}
			if got.AutoRework.MaxAttempts != tc.want || got.AutoRework.Enabled != (tc.want > 0) || got.ReworkEscalationFromAttempt != 6 || got.Sources[EnvMaxReworkAttempts] != tc.source || got.Sources[EnvReworkEscalationFromAttempt] != "repository" {
				t.Fatalf("options = %+v", got)
			}
		})
	}
}

func TestCommandOptionsProfilePolicy(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want []string
	}{
		{EnvCommitPolicy, []string{"run", "note run", "rework --run", "prompt run"}},
		{EnvModel, []string{"run", "note run", "rework --run", "review --run", "merge"}},
		{EnvReviewModel, []string{"run", "note run", "rework --run", "review --run"}},
		{EnvMergeReviewModel, []string{"merge"}},
		{EnvResolverModel, []string{"merge"}},
		{EnvRunHeader, []string{"run", "note run", "rework --run"}},
		{EnvBudgetSliceCostStop, nil}, {EnvPlannerRouting, nil}, {"unknown", nil},
	} {
		if got := ApplicableCommands(tc.key); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.key, got, tc.want)
		}
	}
	for _, profile := range []CommandProfile{CommandRun, CommandReview, CommandMerge, CommandPromptRun, CommandPromptOther} {
		for _, rule := range commandOptionRules {
			// Malformed values in both later stages must also be filtered, not only
			// unrelated snapshot failures. Applicable values retain strict admission.
			env := commandSnapshot(map[string]string{rule.key: "bad value"})
			if env.Require(rule.key) == nil {
				continue
			} // boolean/presentation fallbacks and non-env flags
			_, err := ResolveCommandOptions(CommandOptionsInput{Profile: profile, Env: env})
			applicable := false
			for _, candidate := range rule.profiles {
				applicable = applicable || profile == candidate
			}
			if (err != nil) != applicable {
				t.Errorf("%s %s: applicable=%v err=%v", profile, rule.key, applicable, err)
			}
		}
	}
	got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandPromptOther, Flags: RunOptionsPatch{Agent: "bad", CommitPolicy: "bad", ModelSelection: ModelSelection{Base: "bad model"}}, Auxiliary: CommandAuxiliaryFlags{MaxReworkAttempts: new(-1)}})
	if err != nil || len(got.Sources) != 0 {
		t.Fatalf("other prompt admitted execution settings: %+v %v", got, err)
	}
}

func TestCommandOptionsModelStages(t *testing.T) {
	for _, tc := range []struct {
		name         string
		flags        RunOptionsPatch
		want, source string
	}{
		{"role survives base", RunOptionsPatch{ModelSelection: ModelSelection{Base: "flag-base"}}, "env-review", "env"},
		{"role override", RunOptionsPatch{ModelSelection: ModelSelection{Review: "flag-review"}}, "flag-review", "flag"},
		{"all roles", RunOptionsPatch{}.WithModelForAllRoles("all"), "all", "flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// An unrelated malformed repository role must not block the review profile.
			env := composedSnapshot(map[string]string{EnvReviewModel: "env-review"}, nil, map[string]string{"models.model": `"repo-base"`, "models.run_model": `"bad model"`})
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandReview, Env: env, Flags: tc.flags})
			if err != nil {
				t.Fatal(err)
			}
			if got.RunOptions.Models.For(ModelRoleReview) != tc.want || got.Sources[EnvReviewModel] != tc.source || got.RunOptions.Models.Run != "" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestCommandOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      CommandOptionsInput
		diagnostic string
	}{
		{"profile", CommandOptionsInput{Profile: "unknown"}, "unknown command profile"},
		{"negative slices", CommandOptionsInput{Profile: CommandRun, Flags: RunOptionsPatch{}.WithMaxSlices(-1)}, "--max-slices source=flag"},
		{"negative timeout", CommandOptionsInput{Profile: CommandReview, Flags: RunOptionsPatch{}.WithSessionTimeout(-1)}, EnvSessionTimeout + " source=flag"},
		{"negative attempts", CommandOptionsInput{Profile: CommandRun, Auxiliary: CommandAuxiliaryFlags{MaxReworkAttempts: new(-1)}}, EnvMaxReworkAttempts + " source=flag"},
		{"invalid threshold", CommandOptionsInput{Profile: CommandRun, Auxiliary: CommandAuxiliaryFlags{ReworkEscalationFromAttempt: new(0)}}, EnvReworkEscalationFromAttempt + " source=flag"},
		{"invalid model", CommandOptionsInput{Profile: CommandMerge, Flags: RunOptionsPatch{ModelSelection: ModelSelection{Resolver: "bad model"}}}, EnvResolverModel + " source=flag"},
		{"invalid repository timeout", CommandOptionsInput{Profile: CommandReview, Env: composedSnapshot(nil, nil, map[string]string{"session_timeout": `"-1s"`})}, "session_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveCommandOptions(tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("got %v want %s", err, tc.diagnostic)
			}
		})
	}
	got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Flags: RunOptionsPatch{ExecutionMode: ExecutionModeCurrent, PullRequest: new(true)}, Auxiliary: CommandAuxiliaryFlags{AutoRework: new(true), MaxReworkAttempts: new(0), SkipPermissions: new(false)}})
	if err != nil {
		t.Fatalf("state-dependent PR/current validation must remain outside composition: %v", err)
	}
	if got.AutoRework.Enabled || got.SkipPermissions || got.Sources[EnvSkipPermissions] != "flag" {
		t.Fatalf("got %+v", got)
	}
}

func TestCommandOptionsModelsAndTypedFlags(t *testing.T) {
	// The environment base masks the repository base, so the unset review role
	// follows the environment; the explicit flag wins the timeout.
	env := composedSnapshot(map[string]string{EnvModel: "env-model", EnvSessionTimeout: "20m", EnvRunModel: "bad model"}, nil, map[string]string{"models.model": `"repo-model"`, "session_timeout": `"1m"`})
	got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandReview, Env: env, Flags: RunOptionsPatch{SessionTimeout: new(DefaultSessionTimeout)}})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunOptions.Models.For(ModelRoleReview) != "env-model" || got.Sources[EnvReviewModel] != "env" || got.RunOptions.SessionTimeout != DefaultSessionTimeout || got.Sources[EnvSessionTimeout] != "flag" {
		t.Fatalf("got %+v", got)
	}
	got, err = ResolveCommandOptions(CommandOptionsInput{Profile: CommandReview, Env: composedSnapshot(nil, nil, map[string]string{"models.review_model": `"repo-review"`, "session_timeout": `"1m"`})})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunOptions.Models.For(ModelRoleReview) != "repo-review" || got.Sources[EnvReviewModel] != "repository" || got.RunOptions.SessionTimeout != time.Minute || got.Sources[EnvSessionTimeout] != "repository" {
		t.Fatalf("got %+v", got)
	}
}
