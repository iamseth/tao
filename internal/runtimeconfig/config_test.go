package runtimeconfig

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/taodata"
)

func TestReviewAgentStages(t *testing.T) {
	for _, env := range []AgentKind{"", AgentPi, AgentClaude} {
		for _, repo := range []AgentKind{"", AgentPi, AgentClaude} {
			for _, invocation := range []AgentKind{"", AgentPi, AgentClaude} {
				want := env
				if repo != "" {
					want = repo
				}
				if invocation != "" {
					want = invocation
				}
				o, err := ResolveRunOptionsWithRepositoryDefaults(RunOptionsPatch{ReviewAgent: env, ModelSelection: ModelSelection{Review: "review-model"}}, RunOptionsPatch{ReviewAgent: repo}, RunOptionsPatch{Agent: AgentClaude, ReviewAgent: invocation})
				if err != nil || o.ReviewAgent != want || o.Models.Review != "review-model" {
					t.Fatalf("%q/%q/%q: %+v %v", env, repo, invocation, o, err)
				}
				effective := want
				if effective == "" {
					effective = AgentClaude
				}
				if o.ReviewAgentKind() != effective {
					t.Fatal("wrong effective reviewer")
				}
				reprojected, err := ResolveRunOptions(o.RunOptionsPatch(), RunOptionsPatch{Agent: AgentPi})
				if err != nil || reprojected.ReviewAgent != want {
					t.Fatalf("reprojection: %+v %v", reprojected, err)
				}
				if want == "" && reprojected.ReviewAgentKind() != AgentPi {
					t.Fatal("inheritance frozen")
				}
			}
		}
	}
	if (ResolvedRunOptions{}).ReviewAgentKind() != AgentPi {
		t.Fatal("zero default")
	}
	for i := range 3 {
		stages := make([]RunOptionsPatch, 3)
		stages[i].ReviewAgent = "invalid"
		if _, err := ResolveRunOptionsWithRepositoryDefaults(stages[0], stages[1], stages[2]); err == nil {
			t.Fatal("accepted invalid reviewer")
		}
	}
	data, err := json.Marshal(RunOptionsPatch{})
	if err != nil || strings.Contains(string(data), "review_agent") {
		t.Fatalf("empty JSON: %s %v", data, err)
	}
	for _, agent := range []AgentKind{AgentPi, AgentClaude} {
		data, err := json.Marshal(RunOptionsPatch{ReviewAgent: agent})
		var patch RunOptionsPatch
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &patch); err != nil || patch.ReviewAgent != agent {
			t.Fatalf("round trip: %s %v", data, err)
		}
	}
}

func TestParseModelName(t *testing.T) {
	for _, value := range []string{"provider/model-v1:latest", "model*", "模型", " \tprovider/model-v1:latest\u2003"} {
		got, err := ParseModelName(value)
		if err != nil || got != strings.TrimSpace(value) {
			t.Fatalf("ParseModelName(%q) = %q, %v", value, got, err)
		}
	}
	for _, tt := range []struct{ value, problem string }{
		{"", "empty"}, {" \t\u2003", "empty"},
		{"two models", "whitespace"}, {"two\tmodels", "whitespace"},
		{"two\nmodels", "whitespace"}, {"two\u00a0models", "whitespace"},
		{"two\u2003models", "whitespace"},
	} {
		got, err := ParseModelName(tt.value)
		if err == nil || !strings.Contains(err.Error(), tt.problem) || got != "" {
			t.Fatalf("ParseModelName(%q) = %q, %v; want %s error", tt.value, got, err, tt.problem)
		}
	}
}

func TestModelSelectionFor(t *testing.T) {
	roles := []ModelRole{ModelRoleDefault, ModelRoleRun, ModelRoleReview, ModelRoleMergeReview, ModelRoleResolver, "unknown"}
	for _, role := range roles {
		if got := (ModelSelection{}).For(role); got != "" {
			t.Fatalf("unset For(%q) = %q", role, got)
		}
		if got := (ModelSelection{ReworkEscalation: "strong"}).For(role); got != "" {
			t.Fatalf("escalation leaked into For(%q) = %q", role, got)
		}
		if got := (ModelSelection{Base: "base", ReworkEscalation: "strong"}).For(role); got != "base" {
			t.Fatalf("base For(%q) = %q", role, got)
		}
	}
	models := ModelSelection{Base: "base", Run: "run", Review: "review", MergeReview: "merge-review", Resolver: "resolver"}
	for role, want := range map[ModelRole]string{
		ModelRoleDefault: "base", ModelRoleRun: "run", ModelRoleReview: "review",
		ModelRoleMergeReview: "merge-review", ModelRoleResolver: "resolver", "unknown": "base",
	} {
		if got := models.For(role); got != want {
			t.Fatalf("For(%q) = %q, want %q", role, got, want)
		}
	}
	models.Base = ""
	if got := models.For(ModelRoleReview); got != "review" {
		t.Fatalf("role override without base = %q", got)
	}
}

func TestResolveRunOptionsModelPrecedence(t *testing.T) {
	for _, name := range RuntimeEnvKeys() {
		unsetEnv(t, name)
	}
	t.Setenv(EnvModel, " env-base ")
	t.Setenv(EnvReviewModel, "env-review")
	snapshot := RuntimeEnv()
	if err := snapshot.Require(EnvModel, EnvReviewModel); err != nil {
		t.Fatal(err)
	}
	defaults := snapshot.Defaults()
	repository := RunOptionsPatch{ModelSelection: ModelSelection{Run: " repo-run ", Review: "repo-review", MergeReview: "repo-merge", Resolver: "repo-resolver"}}
	resolved, err := ResolveRunOptionsWithRepositoryDefaults(defaults.RunOptionsPatch, repository, RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	want := ModelSelection{Base: "env-base", Run: "repo-run", Review: "repo-review", MergeReview: "repo-merge", Resolver: "repo-resolver"}
	if resolved.Models != want {
		t.Fatalf("Models = %#v, want %#v", resolved.Models, want)
	}
	baseOnly, err := ResolveRunOptionsWithRepositoryDefaults(defaults.RunOptionsPatch, repository, RunOptionsPatch{ModelSelection: ModelSelection{Base: "request-base"}})
	if err != nil {
		t.Fatal(err)
	}
	want.Base = "request-base"
	if baseOnly.Models != want {
		t.Fatalf("base override changed role overrides: %#v", baseOnly.Models)
	}
	overrides := RunOptionsPatch{Agent: AgentClaude}.WithModelForAllRoles(" request-model ")
	if overrides.Agent != AgentClaude {
		t.Fatal("model helper lost unrelated fields")
	}
	resolved, err = ResolveRunOptionsWithRepositoryDefaults(defaults.RunOptionsPatch, repository, overrides)
	if err != nil {
		t.Fatal(err)
	}
	want = ModelSelection{Base: "request-model", Run: "request-model", Review: "request-model", MergeReview: "request-model", Resolver: "request-model"}
	if resolved.Models != want {
		t.Fatalf("request Models = %#v, want %#v", resolved.Models, want)
	}
}

func TestSharedModelSelectionStages(t *testing.T) {
	for _, tt := range []struct {
		name            string
		env, repository ModelSelection
		explicit        string
		unset           bool
		want            ModelSelection
	}{
		{name: "no selection"},
		{name: "environment fallback", env: ModelSelection{Base: "env"}, want: ModelSelection{Base: "env"}},
		{name: "repository base preserves environment role", env: ModelSelection{Base: "env", Review: "env-review"}, repository: ModelSelection{Base: "repo"}, want: ModelSelection{Base: "repo", Review: "env-review"}},
		{name: "repository role", env: ModelSelection{Base: "env", Run: "env-run"}, repository: ModelSelection{Run: "repo-run"}, want: ModelSelection{Base: "env", Run: "repo-run"}},
		{name: "unset restores inheritance", env: ModelSelection{Base: "env", Run: "env-run"}, repository: ModelSelection{Base: "repo", Run: "repo-run"}, unset: true, want: ModelSelection{Base: "env", Run: "env-run"}},
		{name: "explicit leaves escalation independent", env: ModelSelection{Base: "env", ReworkEscalation: "env-strong"}, repository: ModelSelection{Run: "repo-run", ReworkEscalation: "repo-strong"}, explicit: "explicit", want: ModelSelection{Base: "explicit", Run: "explicit", Review: "explicit", MergeReview: "explicit", Resolver: "explicit", ReworkEscalation: "repo-strong"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range RuntimeEnvKeys() {
				unsetEnv(t, key)
			}
			for key, value := range map[string]string{EnvModel: tt.env.Base, EnvRunModel: tt.env.Run, EnvReviewModel: tt.env.Review, EnvReworkEscalationModel: tt.env.ReworkEscalation} {
				if value != "" {
					t.Setenv(key, value)
				}
			}
			repo := (taodata.Repo{}).WithModelDefaults(tt.repository)
			if tt.unset {
				repo = repo.WithModelDefaults(taodata.RepoModelDefaults{})
			}
			models, _ := repo.ModelDefaults()
			var overrides RunOptionsPatch
			if tt.explicit != "" {
				overrides = overrides.WithModelForAllRoles(tt.explicit)
			}
			resolved, err := ResolveRunOptionsWithRepositoryDefaults(RuntimeEnv().Defaults().RunOptionsPatch, RunOptionsPatch{ModelSelection: models}, overrides)
			if err != nil || resolved.Models != tt.want {
				t.Fatalf("models = %+v, %v; want %+v", resolved.Models, err, tt.want)
			}
			for _, role := range []ModelRole{ModelRoleDefault, ModelRoleRun, ModelRoleReview, ModelRoleMergeReview, ModelRoleResolver} {
				if got := resolved.Models.For(role); got != tt.want.For(role) {
					t.Fatalf("For(%s) = %q, want %q", role, got, tt.want.For(role))
				}
			}
		})
	}
}

func TestResolveRunOptionsReworkEscalationPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, baseline, repository, override, want string
	}{
		{name: "unset"},
		{name: "environment", baseline: "env-strong", want: "env-strong"},
		{name: "repository", baseline: "env-strong", repository: " repo-strong ", want: "repo-strong"},
		{name: "override", baseline: "env-strong", repository: "repo-strong", override: " request-strong ", want: "request-strong"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := ResolveRunOptionsWithRepositoryDefaults(
				RunOptionsPatch{ModelSelection: ModelSelection{ReworkEscalation: tt.baseline}},
				RunOptionsPatch{ModelSelection: ModelSelection{ReworkEscalation: tt.repository}},
				RunOptionsPatch{ModelSelection: ModelSelection{ReworkEscalation: tt.override}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Models.ReworkEscalation != tt.want {
				t.Fatalf("escalation = %q, want %q", resolved.Models.ReworkEscalation, tt.want)
			}
		})
	}
}

func TestResolveRunOptionsRejectsInvalidModelsInEveryStage(t *testing.T) {
	for _, patch := range []RunOptionsPatch{
		{ModelSelection: ModelSelection{Base: " "}}, {ModelSelection: ModelSelection{Run: "two models"}}, {ModelSelection: ModelSelection{Review: "two\tmodels"}},
		{ModelSelection: ModelSelection{MergeReview: "two\u2003models"}}, {ModelSelection: ModelSelection{Resolver: "two\nmodels"}},
		{ModelSelection: ModelSelection{ReworkEscalation: "two models"}}, {ModelSelection: ModelSelection{ReworkEscalation: " \t"}},
	} {
		for stage := range 3 {
			stages := [3]RunOptionsPatch{}
			stages[stage] = patch
			if _, err := ResolveRunOptionsWithRepositoryDefaults(stages[0], stages[1], stages[2]); err == nil {
				t.Fatalf("stage %d accepted invalid patch %#v", stage, patch)
			}
		}
	}
}

func TestRunOptionsPatchModelJSON(t *testing.T) {
	for _, tt := range []struct {
		name  string
		patch RunOptionsPatch
		want  string
	}{
		{name: "empty", want: `{}`},
		{name: "base", patch: RunOptionsPatch{ModelSelection: ModelSelection{Base: "base"}}, want: `{"model":"base"}`},
		{name: "role", patch: RunOptionsPatch{ModelSelection: ModelSelection{Review: "review"}}, want: `{"review_model":"review"}`},
		{name: "escalation", patch: RunOptionsPatch{ModelSelection: ModelSelection{ReworkEscalation: "strong"}}, want: `{"rework_escalation_model":"strong"}`},
		{name: "explicit false", patch: RunOptionsPatch{PullRequest: new(false), ModelSelection: ModelSelection{Run: "run"}}, want: `{"pull_request":false,"run_model":"run"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.patch)
			if err != nil || string(data) != tt.want {
				t.Fatalf("JSON = %s, %v; want %s", data, err, tt.want)
			}
			var decoded RunOptionsPatch
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.ModelSelection != tt.patch.ModelSelection || (decoded.PullRequest == nil) != (tt.patch.PullRequest == nil) || decoded.PullRequestValue() != tt.patch.PullRequestValue() {
				t.Fatalf("round trip = %+v, want %+v", decoded, tt.patch)
			}
		})
	}
	data, err := json.Marshal(RunOptionsPatch{})
	if err != nil || string(data) != "{}" {
		t.Fatalf("empty patch JSON = %s, %v", data, err)
	}
	patch := RunOptionsPatch{ModelSelection: ModelSelection{Base: "base", Run: "run", Review: "review", MergeReview: "merge", Resolver: "resolver", ReworkEscalation: "strong"}}
	data, err = json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"model":"base","run_model":"run","review_model":"review","merge_review_model":"merge","resolver_model":"resolver","rework_escalation_model":"strong"}`; string(data) != want {
		t.Fatalf("patch JSON = %s, want %s", data, want)
	}
	var decoded RunOptionsPatch
	if err := json.Unmarshal(data, &decoded); err != nil || decoded != patch {
		t.Fatalf("decoded patch = %#v, %v", decoded, err)
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
	for _, value := range []string{"sandbox", "worktree", " isolated ", "Current"} {
		_, err := ParseExecutionMode(value)
		if err == nil || err.Error() != "unsupported execution mode \""+value+"\" (want isolated or current)" {
			t.Fatalf("expected unsupported execution mode error for %q, got %v", value, err)
		}
	}
}

func TestSharedConfigAliases(t *testing.T) {
	var mode ExecutionMode
	var sharedMode configtypes.ExecutionMode
	mode = configtypes.ExecutionModeCurrent
	sharedMode = mode
	if sharedMode != ExecutionModeCurrent || mode.String() != "current" {
		t.Fatalf("execution mode alias lost identity: %q", mode)
	}
	var models ModelSelection
	var sharedModels configtypes.ModelSelection
	var role ModelRole
	var sharedRole configtypes.ModelRole
	models = configtypes.ModelSelection{Base: "base", Run: "run"}
	sharedModels = models
	role = configtypes.ModelRoleRun
	sharedRole = role
	if sharedRole != ModelRoleRun || sharedModels.For(role) != "run" || models.For(ModelRoleDefault) != "base" {
		t.Fatalf("model aliases lost identity or methods: %+v, %q", models, role)
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
		if err == nil || err.Error() != "unsupported agent \""+value+"\" (want pi or claude)" {
			t.Fatalf("expected unsupported agent error for %q, got %v", value, err)
		}
	}
}

func TestResolveRunOptionsAppliesBuiltInDefaults(t *testing.T) {
	options, err := ResolveRunOptions(RunOptionsPatch{}, RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if options.MaxSlices != 0 || options.Continue || options.CommitPolicy != CommitPolicySlice || options.ExecutionMode != ExecutionModeIsolated || options.Agent != AgentPi || options.PullRequest || !options.ReviewEnabled || options.SessionTimeout != DefaultSessionTimeout || options.Models != (ModelSelection{}) {
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
		MaxSlices:     new(6),
		Continue:      new(true),
		CommitPolicy:  CommitPolicySlice,
		ExecutionMode: ExecutionModeIsolated,
		Agent:         AgentClaude,
		PullRequest:   new(true),
		ReviewEnabled: new(true),
	}, RunOptionsPatch{
		MaxSlices:     new(1),
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
	if options.MaxSlices != 1 || options.Continue || options.PullRequest || options.ReviewEnabled {
		t.Fatalf("expected explicit max slices and false overrides, got %#v", options)
	}
	if options.CommitPolicy != CommitPolicySlice || options.ExecutionMode != ExecutionModeCurrent || options.Agent != AgentPi {
		t.Fatalf("expected request overrides to win, got %#v", options)
	}
}

func TestResolveRunOptionsMaxSlicesPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		repository *int
		override   *int
		want       int
	}{
		{name: "inherit default", want: 4},
		{name: "repository override", repository: new(2), want: 2},
		{name: "explicit single slice", repository: new(2), override: new(1), want: 1},
		{name: "explicit multiple slices", override: new(3), want: 3},
		{name: "explicit unlimited", repository: new(2), override: new(0), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options, err := ResolveRunOptionsWithRepositoryDefaults(
				RunOptionsPatch{MaxSlices: new(4)},
				RunOptionsPatch{MaxSlices: tt.repository},
				RunOptionsPatch{MaxSlices: tt.override},
			)
			if err != nil {
				t.Fatal(err)
			}
			if options.MaxSlices != tt.want {
				t.Fatalf("MaxSlices = %d, want %d", options.MaxSlices, tt.want)
			}
		})
	}
}

func TestResolveRunOptionsRejectsInvalidStagedValues(t *testing.T) {
	tests := []struct {
		name      string
		defaults  RunOptionsPatch
		overrides RunOptionsPatch
		want      string
	}{
		{name: "default agent", defaults: RunOptionsPatch{Agent: "other"}, want: "unsupported agent \"other\" (want pi or claude)"},
		{name: "override execution mode", overrides: RunOptionsPatch{ExecutionMode: ExecutionMode("sandbox")}, want: "unsupported execution mode \"sandbox\" (want isolated or current)"},
		{name: "negative max slices", overrides: RunOptionsPatch{}.WithMaxSlices(-1), want: "--max-slices must be 0 or greater"},
		{name: "pull request commit none", overrides: RunOptionsPatch{CommitPolicy: CommitPolicyNone, PullRequest: new(true)}, want: "--pull-request requires commit policy slice"},
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
		MaxSlices:      new(1),
		CommitPolicy:   CommitPolicySlice,
		ExecutionMode:  ExecutionModeCurrent,
		Agent:          AgentClaude,
		ModelSelection: ModelSelection{Base: "base", Run: "run", Review: "review", MergeReview: "merge-review", Resolver: "resolver", ReworkEscalation: "strong"},
	})
	if err != nil {
		t.Fatal(err)
	}

	reapplied, err := ResolveRunOptions(RunOptionsPatch{Agent: AgentPi}.WithModelForAllRoles("service-model"), resolved.RunOptionsPatch())
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
	resolved, err := ResolveRunOptions(DefaultRunOptionsPatch().WithMaxSlices(3), RunOptionsPatch{
		ExecutionMode:  ExecutionModeCurrent,
		ModelSelection: ModelSelection{Base: "base", Run: "run", Review: "review", MergeReview: "merge", Resolver: "resolver", ReworkEscalation: "strong"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defaults := resolved.RunOptionsPatch()
	if defaults.ModelSelection != (ModelSelection{Base: "base", Run: "run", Review: "review", MergeReview: "merge", Resolver: "resolver", ReworkEscalation: "strong"}) {
		t.Fatalf("model projection = %#v", defaults)
	}
	if defaults.MaxSlices == nil || *defaults.MaxSlices != 3 || defaults.ExecutionMode != ExecutionModeCurrent || defaults.ReviewEnabled == nil || !*defaults.ReviewEnabled || defaults.SessionTimeout == nil || *defaults.SessionTimeout != DefaultSessionTimeout {
		t.Fatalf("expected resolved values to project back into defaults, got %#v", defaults)
	}
}
