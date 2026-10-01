package runtimeconfig

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func commandSnapshot(values map[string]string) EnvSnapshot {
	return LoadEnv(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
}

func TestCommandOptionsPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name              string
		repository, flags RunOptionsPatch
		want              bool
		source            string
	}{
		{"environment", RunOptionsPatch{}, RunOptionsPatch{}, true, "env"},
		{"repository false", RunOptionsPatch{}.WithPullRequest(false), RunOptionsPatch{}, false, "repository"},
		{"explicit false", RunOptionsPatch{}.WithPullRequest(true), RunOptionsPatch{}.WithPullRequest(false), false, "flag"},
		{"same as environment", RunOptionsPatch{}.WithPullRequest(false), RunOptionsPatch{}.WithPullRequest(true), true, "flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: commandSnapshot(map[string]string{EnvPullRequest: "true"}), Repository: tc.repository, Flags: tc.flags})
			if err != nil {
				t.Fatal(err)
			}
			if got.RunOptions.PullRequest != tc.want || got.Sources[EnvPullRequest] != tc.source {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestCommandOptionsApplicability(t *testing.T) {
	for _, profile := range []CommandProfile{CommandReview, CommandMerge, CommandPromptRun, CommandPromptOther} {
		t.Run(string(profile), func(t *testing.T) {
			env := map[string]string{EnvPullRequest: "invalid", EnvAutoRework: "invalid", EnvPlannerRouting: "invalid", EnvBudgetSliceCostStop: "invalid"}
			if profile != CommandPromptRun {
				env[EnvCommitPolicy] = "invalid"
			}
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: profile, Env: commandSnapshot(env), Repository: RunOptionsPatch{}.WithPullRequest(true)})
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
	_, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandRun, Env: commandSnapshot(map[string]string{EnvCommitPolicy: "none"}), Repository: RunOptionsPatch{}.WithPullRequest(true)})
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
	for _, tc := range []struct {
		name   string
		aux    CommandAuxiliaryFlags
		want   int
		source string
	}{
		{"repository", CommandAuxiliaryFlags{}, 2, "repository"},
		{"explicit zero", CommandAuxiliaryFlags{MaxReworkAttempts: new(0)}, 0, "flag"},
		{"count beats alias", CommandAuxiliaryFlags{MaxReworkAttempts: new(3), AutoRework: new(false)}, 3, "flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCommandOptions(CommandOptionsInput{
				Profile: CommandRun, Env: commandSnapshot(map[string]string{EnvMaxReworkAttempts: "7"}),
				RepositoryRework: ReworkOptionsPatch{MaxAttempts: new(2), EscalationFromAttempt: new(6)}, Auxiliary: tc.aux,
			})
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
			got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandReview, Env: commandSnapshot(map[string]string{EnvReviewModel: "env-review"}), Repository: RunOptionsPatch{ModelSelection: ModelSelection{Base: "repo-base", Run: "bad model"}}, Flags: tc.flags})
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
		{"negative timeout", CommandOptionsInput{Profile: CommandReview, Repository: RunOptionsPatch{}.WithSessionTimeout(-1)}, EnvSessionTimeout + " source=repository"},
		{"negative attempts", CommandOptionsInput{Profile: CommandRun, Auxiliary: CommandAuxiliaryFlags{MaxReworkAttempts: new(-1)}}, EnvMaxReworkAttempts + " source=flag"},
		{"invalid threshold", CommandOptionsInput{Profile: CommandRun, Auxiliary: CommandAuxiliaryFlags{ReworkEscalationFromAttempt: new(0)}}, EnvReworkEscalationFromAttempt + " source=flag"},
		{"invalid model", CommandOptionsInput{Profile: CommandMerge, Flags: RunOptionsPatch{ModelSelection: ModelSelection{Resolver: "bad model"}}}, EnvResolverModel + " source=flag"},
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
	got, err := ResolveCommandOptions(CommandOptionsInput{Profile: CommandReview, Env: commandSnapshot(map[string]string{EnvModel: "env-model", EnvSessionTimeout: "20m", EnvRunModel: "bad model"}), Repository: RunOptionsPatch{ModelSelection: ModelSelection{Base: "repo-model"}, SessionTimeout: new(time.Minute)}, Flags: RunOptionsPatch{SessionTimeout: new(DefaultSessionTimeout)}})
	if err != nil {
		t.Fatal(err)
	}
	if got.RunOptions.Models.For(ModelRoleReview) != "repo-model" || got.Sources[EnvReviewModel] != "repository" || got.RunOptions.SessionTimeout != DefaultSessionTimeout || got.Sources[EnvSessionTimeout] != "flag" {
		t.Fatalf("got %+v", got)
	}
}
