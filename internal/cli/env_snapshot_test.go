package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/selfupdate"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/theme"
)

func TestComposedRunFlagDefaultsAreNotOverrides(t *testing.T) {
	base := runtimeconfig.ResolveSettings(configtypes.SettingsValues{"max_slices": []byte(`8`), "dangerously_skip_permissions": []byte(`true`)}, nil, runtimeconfig.LoadEnv(nil))
	for _, explicit := range []bool{false, true} {
		args := []string{"plan"}
		if explicit {
			args = append(args, "--max-slices=0", "--no-review=false", "--dangerously-skip-permissions=false", "--auto-rework=false", "--agent=pi", "--session-timeout=0")
		}
		app := App{Err: io.Discard, RuntimeEnv: &base}
		fs, _, err := app.parseArgs("run", args, app.registerRunFlags)
		if err != nil {
			t.Fatal(err)
		}
		composed := runtimeconfig.ResolveSettings(configtypes.SettingsValues{"max_slices": []byte(`8`)}, configtypes.SettingsValues{"max_slices": []byte(`2`), "review_enabled": []byte(`false`), "dangerously_skip_permissions": []byte(`true`), "auto_rework": []byte(`true`), "agent": []byte(`"claude"`), "session_timeout": []byte(`"3m"`)}, runtimeconfig.LoadEnv(nil))
		app.RuntimeEnv = &composed
		inputs, err := app.resolveRunRequestFlags(fs)
		if err != nil {
			t.Fatal(err)
		}
		request, err := inputs.defaults.newRunRequestWithRepository("plan", runtimeconfig.RunOptionsPatch{}, inputs.overrides)
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		wantAgent, wantTimeout := runtimeconfig.AgentClaude, 3*time.Minute
		if explicit {
			want = 0
			wantAgent, wantTimeout = runtimeconfig.AgentPi, 0
		}
		if request.Agent != wantAgent || request.SessionTimeout != wantTimeout {
			t.Fatalf("explicit=%v request=%+v", explicit, request)
		}
		if !explicit && (inputs.overrides.Agent != "" || inputs.overrides.SessionTimeout != nil) {
			t.Fatalf("absent flags became overrides: %+v", inputs.overrides)
		}
		if request.MaxSlices != want || request.ReviewEnabled != explicit || inputs.skipPermissions == explicit {
			t.Fatalf("explicit=%v request=%+v permissions=%v", explicit, request, inputs.skipPermissions)
		}
		policy, err := app.resolveRunAutoReworkPolicy(fs, request.ReviewEnabled)
		if err != nil || policy.Enabled {
			t.Fatalf("policy=%+v err=%v", policy, err)
		}
	}
	invalid := runtimeconfig.ResolveSettings(nil, configtypes.SettingsValues{"max_slices": []byte(`-1`)}, runtimeconfig.LoadEnv(nil))
	if _, err := (App{RuntimeEnv: &invalid}).runEnvDefaults(); err == nil {
		t.Fatal("invalid saved max_slices admitted")
	}
}

func TestRunScopedSettingsHandoff(t *testing.T) {
	clearTaoEnv(t)
	ctx := context.Background()
	home := t.TempDir()
	writeGlobalSettings(t, home, `{"max_slices":9,"review_enabled":true,"agent":"pi","session_timeout":"1m"}`)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	repo := plan.NewFileRepository(fixture.root)
	detail, err := repo.ResolvePlan(ctx, fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	registered := taodata.Repo{ID: "selected", Root: detail.State.Repo.Root}
	dir := filepath.Join(home, "repos", registered.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"schema": "tao.repo.v1", "id": registered.ID, "name": "selected", "root": registered.Root, "run_defaults": map[string]any{"max_slices": 2, "review_enabled": false, "auto_rework": false, "agent": "pi", "session_timeout": "2m", "models": map[string]any{"run_model": "repo"}}})
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	environment := snapshotWith(map[string]string{runtimeconfig.EnvRunModel: "env", runtimeconfig.EnvAgent: "claude", runtimeconfig.EnvSessionTimeout: "3m"})
	app := (App{Out: io.Discard, Err: io.Discard, SettingsService: settings.NewService(home, *environment), Registry: func() NoteRegistry {
		return &fakeNoteRegistry{current: taodata.Repo{ID: "wrong"}, repos: []taodata.Repo{registered}}
	}}).initializeSettings(ctx)
	old := executeSinglePlan
	defer func() { executeSinglePlan = old }()
	calls := 0
	executeSinglePlan = func(_ run.Service, _ context.Context, request run.Request) error {
		calls++
		want := 2
		wantAgent, wantTimeout := runtimeconfig.AgentClaude, 3*time.Minute
		if calls == 2 {
			want = 0
			wantAgent, wantTimeout = runtimeconfig.AgentPi, 0
		}
		if request.Agent != wantAgent || request.SessionTimeout != wantTimeout || request.MaxSlices != want || request.ReviewEnabled || request.Models.Run != "env" {
			t.Fatalf("request = %+v", request)
		}
		return nil
	}
	for _, args := range [][]string{{fixture.id}, {"--max-slices=0", "--agent=pi", "--session-timeout=0", fixture.id}} {
		if err := app.run(ctx, repo, args); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestDiagnosticCollectorsShareSnapshotAcrossRefreshes(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("PATH", "")
	values := map[string]string{
		runtimeconfig.EnvAgent: "invalid", runtimeconfig.EnvUpdate: "invalid",
		runtimeconfig.EnvBudgetSliceCostDeprecated: "invalid", runtimeconfig.EnvTheme: "invalid",
		runtimeconfig.EnvRunHeader: "invalid", runtimeconfig.EnvPullRequest: " YES ",
		runtimeconfig.EnvModel: "bad model",
	}
	lookups := map[string]int{}
	snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) {
		lookups[key]++
		value, ok := values[key]
		return value, ok
	})
	before := snapshot.Status()
	registered := taodata.Repo{ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(false), Models: &taodata.RepoModelDefaults{Base: "repo-model"}}}
	persistStatusRepository(t, registered)
	registry := &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}}
	app := App{RuntimeEnv: &snapshot, Registry: func() NoteRegistry { return registry }, RepoHealthCheck: func(context.Context, taodata.Repo) taodata.RepoHealth { return taodata.RepoHealth{Status: "ok"} }}
	settings := newUISettingsService(app)
	debug := newUIDebugCollector(app, "tao")
	for range 3 {
		s, err := settings.Collect(context.Background())
		if err != nil || s.CollectionError != "" || !s.InheritedPullRequest {
			t.Fatalf("settings lost typed baseline: %+v, %v", s, err)
		}
		d, err := debug.Collect(context.Background())
		if err != nil || len(s.RuntimeDefaults) != len(before) || len(d.RuntimeDefaults) != len(before)+1 {
			t.Fatalf("incomplete diagnostics: settings=%d debug=%d error=%v", len(s.RuntimeDefaults), len(d.RuntimeDefaults), err)
		}
		for i, row := range before {
			if s.RuntimeDefaults[i].Name != row.Name || s.RuntimeDefaults[i].Value != row.Value || s.RuntimeDefaults[i].Source != row.Source || s.RuntimeDefaults[i].Warning != row.Warning || d.RuntimeDefaults[i].Name != row.Name || d.RuntimeDefaults[i].Warning != row.Warning {
				t.Fatalf("lost captured diagnostic for %s: %+v, %+v", row.Name, s.RuntimeDefaults[i], d.RuntimeDefaults[i])
			}
			if row.Name == runtimeconfig.EnvModel && d.RuntimeDefaults[i].Source != "invalid" {
				t.Fatalf("lost invalid environment diagnostic: %+v", d.RuntimeDefaults[i])
			}
		}
		// Neither ambient changes nor mutation of a returned projection may leak.
		s.RuntimeDefaults[0].Value = "mutated"
		d.RuntimeDefaults[0].Warning = "mutated"
		t.Setenv(runtimeconfig.EnvPullRequest, "false")
		t.Setenv(runtimeconfig.EnvUpdate, "invalid later value")
	}
	if !reflect.DeepEqual(snapshot.Status(), before) || !reflect.DeepEqual(registry.current, registered) || !reflect.DeepEqual(registry.repos, []taodata.Repo{registered}) {
		t.Fatal("diagnostics mutated snapshot or repository defaults")
	}
	for key, count := range lookups {
		if count != 1 {
			t.Errorf("%s looked up %d times", key, count)
		}
	}
}

func TestModelConsumersComposeSelectedRepository(t *testing.T) {
	ctx := context.Background()
	keys := []string{"model", "run_model", "review_model", "merge_review_model", "resolver_model", "rework_escalation_model", "effort", "run_effort", "review_effort", "merge_review_effort", "resolver_effort"}
	envKeys := []string{runtimeconfig.EnvModel, runtimeconfig.EnvRunModel, runtimeconfig.EnvReviewModel, runtimeconfig.EnvMergeReviewModel, runtimeconfig.EnvResolverModel, runtimeconfig.EnvReworkEscalationModel, runtimeconfig.EnvEffort, runtimeconfig.EnvRunEffort, runtimeconfig.EnvReviewEffort, runtimeconfig.EnvMergeReviewEffort, runtimeconfig.EnvResolverEffort}
	for _, layer := range []string{"global", "repo", "env"} {
		t.Run(layer, func(t *testing.T) {
			home := t.TempDir()
			registered := taodata.Repo{ID: "selected", Root: t.TempDir()}
			globalModels, repoModels := map[string]string{}, map[string]string{}
			environment := map[string]string{runtimeconfig.EnvCommitPolicy: "invalid-unrelated"}
			for i, key := range keys {
				globalModels[key] = "global-" + key
				if layer != "global" {
					repoModels[key] = "repo-" + key
				}
				if layer == "env" {
					environment[envKeys[i]] = "env-" + key
				}
			}
			global, _ := json.Marshal(map[string]any{"models": globalModels})
			writeGlobalSettings(t, home, string(global))
			dir := filepath.Join(home, "repos", registered.ID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"schema": "tao.repo.v1", "id": registered.ID, "name": "selected", "root": registered.Root, "run_defaults": map[string]any{"models": repoModels}})
			if err := os.WriteFile(filepath.Join(dir, "repo.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			app := (App{Err: io.Discard, SettingsService: settings.NewService(home, *snapshotWith(environment)), Registry: func() NoteRegistry {
				return &fakeNoteRegistry{current: taodata.Repo{ID: "wrong-checkout"}, repos: []taodata.Repo{registered}}
			}}).initializeSettings(ctx)
			app, err := app.settingsForPlanRoot(ctx, registered.Root)
			if err != nil {
				t.Fatal(err)
			}
			models, err := app.effectiveMergeModels("")
			if err != nil {
				t.Fatal(err)
			}
			got := []string{models.Base, models.Run, models.Review, models.MergeReview, models.Resolver, models.ReworkEscalation, models.Effort, models.RunEffort, models.ReviewEffort, models.MergeReviewEffort, models.ResolverEffort}
			for i, key := range keys {
				if got[i] != layer+"-"+key {
					t.Fatalf("%s = %q", key, got[i])
				}
			}
			models, err = app.effectiveMergeModels("explicit")
			if err != nil || models.Base != "explicit" || models.Run != "explicit" || models.Review != "explicit" || models.MergeReview != "explicit" || models.Resolver != "explicit" || models.ReworkEscalation != layer+"-rework_escalation_model" {
				t.Fatalf("all-role override changed semantics: %+v %v", models, err)
			}
			if _, err := newReworkTriageTextGenerator(app, nil); err != nil {
				t.Fatalf("unrelated invalid commit policy blocked generator: %v", err)
			}
		})
	}
}

func TestMergeModelsAndConstructorsUseInvocationSnapshot(t *testing.T) {
	snapshot := snapshotWith(map[string]string{
		runtimeconfig.EnvModel: "captured-base", runtimeconfig.EnvMergeReviewModel: "captured-review",
		runtimeconfig.EnvResolverModel: "captured-resolver", runtimeconfig.EnvAgent: "invalid-unused",
		runtimeconfig.EnvMergeVerifyCommand: "captured-gate",
	})
	registry := &fakeNoteRegistry{current: taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{Models: &taodata.RepoModelDefaults{Resolver: "repo-resolver"}}}}
	app := App{RuntimeEnv: snapshot, Registry: func() NoteRegistry { return registry }}
	t.Setenv(runtimeconfig.EnvModel, "invalid later model")
	t.Setenv(runtimeconfig.EnvMergeVerifyCommand, "later-gate")
	for _, override := range []string{"", "explicit"} {
		models, err := app.mergeModels(context.Background(), override)
		if err != nil {
			t.Fatal(err)
		}
		wantBase, wantReview, wantResolver := "captured-base", "captured-review", "captured-resolver"
		if override != "" {
			wantBase, wantReview, wantResolver = override, override, override
		}
		if models.Base != wantBase || models.For(runtimeconfig.ModelRoleMergeReview) != wantReview || models.For(runtimeconfig.ModelRoleResolver) != wantResolver {
			t.Fatalf("models = %#v", models)
		}
		batch := newMergeBatchAgentConfig(app, "", nil, nil, models, nil)
		single := newSingleMergeAgentConfig(app, nil, "", nil, nil, models)
		for _, captured := range []*runtimeconfig.EnvSnapshot{batch.RuntimeEnv, single.RuntimeEnv} {
			if captured == nil || captured.Defaults().MergeVerifyCommand != "captured-gate" || captured.Require(runtimeconfig.EnvAgent) == nil {
				t.Fatalf("constructor lost captured values or deferred failures: %#v", captured)
			}
		}
	}
	app.RuntimeEnv = nil
	if models, err := app.mergeModels(context.Background(), ""); err != nil || models.Base != "" {
		t.Fatalf("direct app read ambient models: %#v, %v", models, err)
	}
	app.RuntimeEnv = snapshotWith(map[string]string{runtimeconfig.EnvModel: "invalid model"})
	if _, err := app.mergeModels(context.Background(), "explicit"); err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvModel) {
		t.Fatalf("model validation boundary changed: %v", err)
	}
}

// Changing the process environment during startup must not change flags later
// in dispatch, but a second independent Run on the same App must see it.
func TestInvocationSnapshotFreezesDefaultsUntilNextRun(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv(runtimeconfig.EnvExecutionMode, "current")
	var out bytes.Buffer
	updater := &snapshotMutatingUpdater{mutate: func() {
		t.Setenv(runtimeconfig.EnvExecutionMode, "isolated")
	}}
	app := App{Out: &out, Err: io.Discard, SelfUpdater: updater}
	for _, want := range []string{"current", "isolated"} {
		out.Reset()
		if err := app.Run(context.Background(), []string{"run", "--help"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "execution mode: isolated or current (default "+want+")") {
			t.Fatalf("want captured %s default: %s", want, out.String())
		}
	}
}

type snapshotMutatingUpdater struct {
	fakeSelfUpdater
	mutate func()
}

func (u *snapshotMutatingUpdater) Startup(ctx context.Context, mode selfupdate.Mode) selfupdate.StartupOutcome {
	u.mutate()
	return u.fakeSelfUpdater.Startup(ctx, mode)
}

func TestSnapshotHelpRetainsValidDefaultsBesideInvalidFields(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv(runtimeconfig.EnvExecutionMode, "current")
	t.Setenv(runtimeconfig.EnvPullRequest, "yes")
	t.Setenv(runtimeconfig.EnvCommitPolicy, "invalid")
	t.Setenv(runtimeconfig.EnvAutoRework, "off")
	t.Setenv(runtimeconfig.EnvMaxReworkAttempts, "invalid")
	t.Setenv(runtimeconfig.EnvRunHeader, "false")
	for _, command := range []string{"run", "prompt", "note"} {
		t.Run(command, func(t *testing.T) {
			var out bytes.Buffer
			if err := (App{Out: &out, Err: io.Discard}).Run(context.Background(), []string{command, "--help"}); err != nil {
				t.Fatal(err)
			}
			wants := []string{"execution mode: isolated or current (default current)", "commit policy: slice or none (default slice)"}
			if command != "prompt" {
				wants = append(wants, "pull request after a completed full run (default true)")
			}
			if command == "run" {
				wants = append(wants, "disable the pinned run header (default true)", "deprecated: use --max-rework-attempts (false=0, true=5) (default true)")
			}
			for _, want := range wants {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q: %s", want, out.String())
				}
			}
		})
	}
}

func TestInjectedSnapshotIsSharedWithoutReloading(t *testing.T) {
	clearTaoEnv(t)
	values := map[string]string{
		runtimeconfig.EnvExecutionMode: "current",
		runtimeconfig.EnvTheme:         "gruvbox",
		runtimeconfig.EnvUpdate:        "auto",
	}
	lookups := map[string]int{}
	snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) {
		lookups[key]++
		value, ok := values[key]
		return value, ok
	})
	t.Setenv(runtimeconfig.EnvExecutionMode, "isolated")
	t.Setenv(runtimeconfig.EnvTheme, "invalid")
	t.Setenv(runtimeconfig.EnvUpdate, "invalid")
	var out bytes.Buffer
	updater := &fakeSelfUpdater{}
	app := App{Out: &out, Err: io.Discard, RuntimeEnv: &snapshot, SelfUpdater: updater}
	for _, command := range []string{"run", "prompt", "note"} {
		out.Reset()
		if err := app.Run(context.Background(), []string{command, "--help"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "execution mode: isolated or current (default current)") {
			t.Fatalf("injected defaults lost: %s", out.String())
		}
	}
	if !reflect.DeepEqual(updater.startupModes, []selfupdate.Mode{selfupdate.ModeAuto, selfupdate.ModeAuto, selfupdate.ModeAuto}) {
		t.Fatalf("injected update modes lost: %v", updater.startupModes)
	}
	selected, _ := theme.Lookup("gruvbox")
	if got := app.withRuntimeTheme().outputTheme(); !reflect.DeepEqual(got, selected) {
		t.Fatal("theme did not use injected snapshot")
	}
	explicit := theme.Default()
	app.Theme = &explicit
	if app.withRuntimeTheme().Theme != &explicit {
		t.Fatal("explicit theme injection replaced")
	}
	for _, row := range snapshot.Status() {
		if lookups[row.Name] != 1 {
			t.Errorf("lookups[%s] = %d, want one", row.Name, lookups[row.Name])
		}
	}
	// Binding help must not mutate metadata shared with completion/other Runs.
	out.Reset()
	if err := renderCommandHelp(&out, commandByName("run")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "execution mode: isolated or current (default isolated)") {
		t.Fatal("invocation mutated static command metadata")
	}
}

func TestInvocationLookupCountsAcrossHelpExecutionAndDiagnostics(t *testing.T) {
	clearTaoEnv(t)
	for _, args := range [][]string{{"run", "--help"}, {"prompt", "plan"}, {"status", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var prior *runtimeconfig.EnvSnapshot
			for _, model := range []string{"first-model", "second-model"} {
				counts := map[string]int{}
				snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) {
					counts[key]++
					return model, key == runtimeconfig.EnvModel
				})
				app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: &snapshot,
					SelfUpdater: &fakeSelfUpdater{},
					Repository:  func(string) Repository { return fakeRepository{} },
					Registry:    func() NoteRegistry { return &fakeNoteRegistry{} },
				}
				// Ambient mutation cannot trigger hidden reloads in any path.
				t.Setenv(runtimeconfig.EnvModel, "invalid later model")
				if err := app.Run(context.Background(), args); err != nil {
					t.Fatal(err)
				}
				if snapshot.Defaults().Base != model || (prior != nil && prior.Defaults().Base != "first-model") {
					t.Fatal("independent invocation snapshots shared state")
				}
				if len(counts) != len(runtimeconfig.RuntimeEnvKeys()) {
					t.Fatalf("incomplete lookup table: %v", counts)
				}
				for _, key := range runtimeconfig.RuntimeEnvKeys() {
					if counts[key] != 1 {
						t.Errorf("%s looked up %d times", key, counts[key])
					}
				}
				prior = &snapshot
			}
		})
	}
}

func TestLowerLevelSnapshotDefaultsAndConsumption(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv(runtimeconfig.EnvExecutionMode, "current")
	t.Setenv(runtimeconfig.EnvTheme, "gruvbox")
	t.Setenv(runtimeconfig.EnvUpdate, "invalid")
	app := App{}
	if !reflect.DeepEqual(app.flagDefaults(), runtimeconfig.LoadEnv(nil).Defaults()) {
		t.Fatal("lower-level defaults loaded process environment")
	}
	if err := app.envSnapshot().Require(runtimeconfig.EnvUpdate); err != nil {
		t.Fatalf("lower-level snapshot loaded process environment: %v", err)
	}
	snapshot := runtimeconfig.RuntimeEnv()
	app.RuntimeEnv = &snapshot
	if _, err := app.envDefaultsFor(runtimeconfig.EnvExecutionMode); err != nil {
		t.Fatalf("unused invalid setting rejected: %v", err)
	}
	if _, err := app.envDefaultsFor(runtimeconfig.EnvUpdate); err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvUpdate) {
		t.Fatalf("consumed invalid setting admitted: %v", err)
	}
	updater := &fakeSelfUpdater{}
	app.SelfUpdater = updater
	if err := app.runStartupUpdate(context.Background()); err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvUpdate) {
		t.Fatalf("invalid mode admitted to startup: %v", err)
	}
	if len(updater.startupModes) != 0 {
		t.Fatal("updater received rejected mode")
	}
}

func TestPresentationDiagnosticsDoNotRejectExecutionDefaults(t *testing.T) {
	snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) {
		return "invalid", key == runtimeconfig.EnvTheme || key == runtimeconfig.EnvRunHeader
	})
	app := App{RuntimeEnv: &snapshot}
	defaults, err := app.envDefaultsFor(runtimeconfig.EnvTheme, runtimeconfig.EnvRunHeader)
	if err != nil || !defaults.RunHeader || !reflect.DeepEqual(defaults.Theme, theme.Default()) {
		t.Fatalf("presentation failures did not retain built-ins: %+v, %v", defaults, err)
	}
	for _, row := range snapshot.Status() {
		if (row.Name == runtimeconfig.EnvTheme || row.Name == runtimeconfig.EnvRunHeader) && (row.Warning == "" || row.Source != "default") {
			t.Errorf("lost presentation warning: %+v", row)
		}
	}
}

func snapshotWith(values map[string]string) *runtimeconfig.EnvSnapshot {
	snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	return &snapshot
}

func TestReviewAgentUnregisteredAndMissingRepository(t *testing.T) {
	root := initTestGitRepo(t)
	t.Chdir(root)
	registry := taodata.Registry{DataHome: t.TempDir()}
	app := App{RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvReviewAgent: "claude"}), Registry: func() NoteRegistry { return registry }}
	repository, err := app.currentRepositoryRunOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := app.runEnvDefaults()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := defaults.resolveRunOptionsWithRepository(repository, runtimeconfig.RunOptionsPatch{})
	if err != nil || resolved.ReviewAgentKind() != runtimeconfig.AgentClaude {
		t.Fatalf("unregistered inheritance: %+v, %v", resolved, err)
	}
	t.Chdir(t.TempDir())
	if _, err := app.currentRepositoryRunOptions(context.Background()); err == nil {
		t.Fatal("missing git repository accepted")
	}
}

func TestReviewAgentSnapshotAdmission(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	repo := plan.NewFileRepository(fixture.root)
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvReviewAgent: "invalid"}), Registry: func() NoteRegistry { return &fakeNoteRegistry{} }}
	for _, command := range []string{"run", "review", "rework"} {
		var err error
		switch command {
		case "run":
			err = app.run(context.Background(), repo, []string{fixture.id})
		case "review":
			err = app.runPlanReview(context.Background(), repo, fixture.id, runtimeconfig.RunOptionsPatch{})
		case "rework":
			err = app.rework(context.Background(), repo, []string{"--run", fixture.id})
		}
		if err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvReviewAgent) {
			t.Errorf("%s consumed review selector: %v", command, err)
		}
	}
	if _, err := newReworkTriageTextGenerator(app, nil); err != nil {
		t.Fatalf("triage consumed unrelated review selector: %v", err)
	}
}

func TestReviewAgentFlagPrecedence(t *testing.T) {
	app := App{Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvAgent: "pi", runtimeconfig.EnvReviewAgent: "pi"})}
	repository := repositoryRunOptions((taodata.Repo{}).WithReviewAgentDefault("claude"))
	for _, test := range []struct{ arg, want string }{{"", "claude"}, {"--review-agent=pi", "pi"}, {"--review-agent=claude", "claude"}} {
		args := []string{}
		if test.arg != "" {
			args = append(args, test.arg)
		}
		fs, _, err := app.parseArgs("run", args, app.registerRunFlags)
		if err != nil {
			t.Fatal(err)
		}
		inputs, err := app.resolveRunRequestFlags(fs)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := inputs.defaults.resolveRunOptionsWithRepository(repository, inputs.overrides)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.ReviewAgentKind().String() != test.want {
			t.Fatalf("%s: got %s want %s", test.arg, resolved.ReviewAgentKind(), test.want)
		}
	}
}

func TestOperationSnapshotAdmission(t *testing.T) {
	clearTaoEnv(t)
	for _, command := range []string{"run", "review", "triage", "prompt"} {
		t.Run(command, func(t *testing.T) {
			key := runtimeconfig.EnvSessionTimeout
			if command == "prompt" {
				key = runtimeconfig.EnvExecutionMode
			}
			app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{key: "invalid"})}
			var err error
			switch command {
			case "run":
				err = app.run(context.Background(), fakeRepository{}, []string{"--reverify", "plan-a"})
			case "review":
				err = app.runPlanReview(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": {}}}, "plan-a", runtimeconfig.RunOptionsPatch{})
			case "triage":
				_, err = newReworkTriageTextGenerator(app, nil)
			case "prompt":
				err = app.prompt(context.Background(), nil, []string{"run", "--execution-mode=current"})
			}
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("consumed snapshot %s not rejected: %v", key, err)
			}
		})
	}
}

func TestReviewSnapshotApplicability(t *testing.T) {
	clearTaoEnv(t)
	keys := []string{runtimeconfig.EnvAgent, runtimeconfig.EnvModel, runtimeconfig.EnvReviewModel, runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
			before := readText(t, filepath.Join(fixture.dir, "state.json"))
			app := App{Out: io.Discard, Err: io.Discard,
				RuntimeEnv:     snapshotWith(map[string]string{key: "invalid value"}),
				Registry:       func() NoteRegistry { return &fakeNoteRegistry{} },
				ProcessStarter: fakeCLIProcessStarter(t, "", func(string) { t.Fatal("provider called after rejected admission") }),
			}
			repo := plan.NewFileRepository(fixture.root)
			if err := app.review(context.Background(), repo, []string{fixture.id}); err != nil {
				t.Fatalf("persisted review admitted execution settings: %v", err)
			}
			if err := app.review(context.Background(), repo, []string{"--run", "--model=override", fixture.id}); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("fresh review did not reject %s: %v", key, err)
			}
			if readText(t, filepath.Join(fixture.dir, "state.json")) != before {
				t.Fatal("admission mutated plan")
			}
		})
	}
}

func TestPromptSnapshotApplicability(t *testing.T) {
	for _, key := range []string{runtimeconfig.EnvCommitPolicy, runtimeconfig.EnvExecutionMode} {
		t.Run(key, func(t *testing.T) {
			app := App{Out: io.Discard, RuntimeEnv: snapshotWith(map[string]string{key: "invalid"}), Registry: func() NoteRegistry { t.Fatal("rendering looked up repository"); return nil }}
			if err := app.prompt(context.Background(), nil, []string{"plan", "--commit-policy=invalid", "--execution-mode=invalid"}); err != nil {
				t.Fatalf("non-run prompt consumed run settings: %v", err)
			}
			if err := app.prompt(context.Background(), nil, []string{"run", "--commit=false"}); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("run prompt failed to admit %s: %v", key, err)
			}
		})
	}
}

func TestPromptAndTriageIgnoreUnrelatedEnvironment(t *testing.T) {
	clearTaoEnv(t)
	for _, key := range []string{runtimeconfig.EnvSessionWarnPercent, runtimeconfig.EnvUpdate, runtimeconfig.EnvAutoRework, runtimeconfig.EnvMergeReviewModel, runtimeconfig.EnvAggregateReviewConvergenceWindow, runtimeconfig.EnvPlannerRoutingArms, runtimeconfig.EnvMaxSliceCostDeprecated} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "invalid value")
			app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{key: "invalid value"})}
			if err := app.prompt(context.Background(), nil, []string{"plan"}); err != nil {
				t.Fatalf("prompt rejected unused setting: %v", err)
			}
			if _, err := newReworkTriageTextGenerator(app, nil); err != nil {
				t.Fatalf("triage rejected unused setting: %v", err)
			}
		})
	}
}

func TestAppRunRejectsConsumedSettingsBeforeExecution(t *testing.T) {
	clearTaoEnv(t)
	for _, args := range [][]string{{"run"}, {"run", "--continue"}, {"run", "--restart"}, {"run", "--repair-verification"}, {"run", "--reverify"}, {"review", "--run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			before := readText(t, filepath.Join(fixture.dir, "state.json"))
			slicesBefore := readText(t, filepath.Join(fixture.dir, "slices.json"))
			app := App{
				Out: io.Discard, Err: io.Discard,
				RuntimeEnv:     snapshotWith(map[string]string{runtimeconfig.EnvSessionTimeout: "invalid"}),
				Repository:     func(string) Repository { return plan.NewFileRepository(fixture.root) },
				ProcessStarter: fakeCLIProcessStarter(t, "", func(string) { t.Fatal("provider called after rejected admission") }),
			}
			err := app.Run(context.Background(), append(args, fixture.id))
			if err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvSessionTimeout) {
				t.Fatalf("consumed setting not rejected: %v", err)
			}
			if readText(t, filepath.Join(fixture.dir, "state.json")) != before || readText(t, filepath.Join(fixture.dir, "slices.json")) != slicesBefore {
				t.Fatal("failed admission mutated the plan")
			}
		})
	}
}

func TestRunIgnoresSettingsOwnedByOtherConsumers(t *testing.T) {
	clearTaoEnv(t)
	for _, key := range []string{runtimeconfig.EnvUpdate, runtimeconfig.EnvPlannerRoutingArms, runtimeconfig.EnvAggregateReviewConvergenceWindow, runtimeconfig.EnvMergeReviewModel, runtimeconfig.EnvMaxSliceCostDeprecated} {
		t.Run(key, func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			calls := 0
			old := executeSinglePlan
			executeSinglePlan = func(_ run.Service, _ context.Context, request run.Request) error {
				calls++
				if request.ReviewEnabled || request.PullRequest || request.MaxSlices != 0 || request.SessionTimeout != 0 {
					t.Fatalf("explicit false/zero lost: %+v", request)
				}
				return nil
			}
			t.Cleanup(func() { executeSinglePlan = old })
			app := App{Out: io.Discard, Err: io.Discard, Registry: func() NoteRegistry { return &fakeNoteRegistry{} }, RuntimeEnv: snapshotWith(map[string]string{
				key: "invalid value", runtimeconfig.EnvPullRequest: "yes", runtimeconfig.EnvSessionTimeout: "0",
			})}
			// Direct admission excludes startup update and lower-level budget policy.
			err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{"--no-review", "--pull-request=false", "--max-slices=0", fixture.id})
			if err != nil || calls != 1 {
				t.Fatalf("unused %s rejected: calls=%d, err=%v", key, calls, err)
			}
		})
	}
}

func TestBudgetRenderConsumersUseCapturedValuesAndIgnoreUnusedSettings(t *testing.T) {
	clearTaoEnv(t)
	// Stop caps are part of the single budget admission now, so only settings
	// outside the budget block are left invalid here.
	snapshot := snapshotWith(map[string]string{
		runtimeconfig.EnvBudgetSliceOutputTokensDeprecated: "7", runtimeconfig.EnvBudgetPlanCostDeprecated: "0",
		runtimeconfig.EnvAgent: "invalid", runtimeconfig.EnvPlannerRoutingArms: "invalid",
		runtimeconfig.EnvAggregateReviewConvergenceWindow: "invalid",
	})
	t.Setenv(runtimeconfig.EnvBudgetSliceOutputTokensDeprecated, "99999")
	t.Setenv(runtimeconfig.EnvBudgetPlanCostDeprecated, "99999")
	detail := validatePlanDetail(t.TempDir(), []string{"go version"}, nil)
	detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, SliceID: "001-a", Metrics: &plan.AgentMetrics{OutputTokens: 8, Cost: 1}}}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}
	for _, command := range []string{"show", "validate"} {
		t.Run(command, func(t *testing.T) {
			var out bytes.Buffer
			app := App{Out: &out, Err: io.Discard, RuntimeEnv: snapshot}
			var err error
			if command == "show" {
				err = app.show(context.Background(), repo, []string{"example"})
			} else {
				err = app.validate(context.Background(), repo, []string{"example"})
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"observed 8 > threshold 7", "observed 1 > threshold 0"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, out.String())
				}
			}
		})
	}
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvBudgetSliceCostDeprecated: "invalid"})}
	if err := app.show(context.Background(), repo, []string{"--json", "example"}); err != nil {
		t.Fatalf("JSON show consumed unused budgets: %v", err)
	}
}

func TestReadOnlyBudgetConsumersUseSelectedRepository(t *testing.T) {
	ctx := context.Background()
	for _, invalid := range []bool{false, true} {
		home := t.TempDir()
		writeGlobalSettings(t, home, `{"budget":{"slice":{"output_tokens":{"warn":99}}}}`)
		detail := validatePlanDetail(t.TempDir(), []string{"go version"}, nil)
		detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, SliceID: "001-a", Metrics: &plan.AgentMetrics{OutputTokens: 8}}}
		registered := taodata.Repo{ID: "selected", Root: detail.State.Repo.Root}
		dir := filepath.Join(home, "repos", registered.ID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		thresholds := map[string]any{"warn": 7}
		if invalid {
			thresholds["stop"] = 6
		}
		data, _ := json.Marshal(map[string]any{"schema": "tao.repo.v1", "id": registered.ID, "name": "selected", "root": registered.Root, "run_defaults": map[string]any{"budget": map[string]any{"slice": map[string]any{"output_tokens": thresholds}}}})
		if err := os.WriteFile(filepath.Join(dir, "repo.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"show", "validate"} {
			var out bytes.Buffer
			app := (App{Out: &out, Err: io.Discard, SettingsService: settings.NewService(home, *snapshotWith(map[string]string{runtimeconfig.EnvAgent: "invalid-unrelated"})), Registry: func() NoteRegistry {
				return &fakeNoteRegistry{current: taodata.Repo{ID: "wrong-checkout"}, repos: []taodata.Repo{registered}}
			}}).initializeSettings(ctx)
			repo := fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}
			var err error
			if command == "show" {
				err = app.show(ctx, repo, []string{"example"})
			} else {
				err = app.validate(ctx, repo, []string{"example"})
			}
			if invalid {
				if err == nil || !strings.Contains(err.Error(), "STOP") {
					t.Fatalf("%s admitted invalid saved budget: %v", command, err)
				}
			} else if err != nil || !strings.Contains(out.String(), "observed 8 > threshold 7") {
				t.Fatalf("%s ignored selected budget: %s %v", command, out.String(), err)
			}
		}
	}
}

func TestBudgetConsumersRejectInvalidThresholds(t *testing.T) {
	clearTaoEnv(t)
	for _, command := range []string{"show", "validate", "run", "review"} {
		t.Run(command, func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			app := App{Out: io.Discard, Err: io.Discard,
				RuntimeEnv:     snapshotWith(map[string]string{runtimeconfig.EnvBudgetPlanCostDeprecated: "invalid"}),
				Repository:     func(string) Repository { return plan.NewFileRepository(fixture.root) },
				Registry:       func() NoteRegistry { return &fakeNoteRegistry{} },
				ProcessStarter: fakeCLIProcessStarter(t, "", func(string) { t.Error("provider called after rejected budget") }),
				CommandRunner:  func(context.Context, string, string, []string, io.Writer, io.Writer) error { return context.Canceled },
			}
			args := []string{command, fixture.id}
			if command == "review" {
				args = []string{command, "--run", fixture.id}
			}
			err := app.Run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvBudgetPlanCostDeprecated) {
				t.Fatalf("consumed threshold not rejected: %v", err)
			}
		})
	}
}

func TestRunReworkRestartRejectsInvalidHardCapsBeforeReopening(t *testing.T) {
	for _, key := range []string{runtimeconfig.EnvMaxSliceCostDeprecated, runtimeconfig.EnvMaxSliceOutputTokensDeprecated} {
		t.Run(key, func(t *testing.T) {
			clearTaoEnv(t)
			now := time.Date(2026, 9, 28, 23, 0, 0, 0, time.UTC)
			const planID = "20260928-2300-invalid-restart-cap"
			detail, _ := autoReworkTestDetail(planID, now)
			detail.Dir = t.TempDir()
			detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: "r101-fix", Status: plan.StatusCompleted})
			detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, "r101-fix")
			detail.Events = []plan.Event{
				{Type: plan.EventTypeReworkRound, PlanID: planID, Round: 1, Attempts: 1},
				{Type: plan.EventTypeReworkStopped, PlanID: planID, Round: 1, Attempts: 1, Reason: "automatic rework cap exhausted after 1 cycles"},
			}
			before, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			repo := newRecordingAutoReworkRepository(planID, detail)
			providerCalls := 0
			app := App{
				Out: io.Discard, Err: io.Discard, Now: func() time.Time { return now },
				RuntimeEnv: snapshotWith(map[string]string{key: "invalid"}),
				Registry:   func() NoteRegistry { return &fakeNoteRegistry{} },
				ProcessStarter: fakeCLIProcessStarter(t, "", func(string) {
					providerCalls++
				}),
			}
			// The captured error must win even if the live environment is repaired.
			t.Setenv(key, "0")
			err = app.run(context.Background(), repo, []string{"--rework-restart", planID})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("invalid hard cap not rejected: %v", err)
			}
			after, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("rejected restart changed state, slices, or historical events (including attempts):\nbefore: %s\nafter: %s", before, after)
			}
			for _, event := range repo.events {
				if event.Type == plan.EventTypePlanReopened || event.Type == plan.EventTypeReworkRound || event.Type == plan.EventTypeReworkStopped {
					t.Errorf("rejected restart appended rework evidence: %+v", event)
				}
			}
			if providerCalls != 0 {
				t.Errorf("provider calls = %d, want 0", providerCalls)
			}
		})
	}
}

func TestRunNonSlicePathsIgnoreInvalidHardCaps(t *testing.T) {
	for _, key := range []string{runtimeconfig.EnvMaxSliceCostDeprecated, runtimeconfig.EnvMaxSliceOutputTokensDeprecated} {
		// Stop caps share the single budget admission with the warn thresholds,
		// so the plain run path that consumes thresholds rejects them; paths that
		// consume no budget policy still ignore them.
		for _, tc := range []struct {
			args     []string
			rejected bool
		}{{nil, true}, {[]string{"--reverify", "--rework-restart"}, false}, {[]string{"--no-review", "--rework-restart"}, false}, {[]string{"--max-rework-attempts=0", "--rework-restart"}, false}} {
			args := tc.args
			t.Run(key+"/"+strings.Join(args, " "), func(t *testing.T) {
				clearTaoEnv(t)
				const planID = "20260928-2300-unused-cap"
				detail, _ := autoReworkTestDetail(planID, time.Now())
				detail.Dir = t.TempDir()
				detail.State.Status = plan.StatusReviewed
				detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
				repo := newRecordingAutoReworkRepository(planID, detail)
				calls := 0
				old := executeSinglePlan
				executeSinglePlan = func(run.Service, context.Context, run.Request) error {
					calls++
					return nil
				}
				t.Cleanup(func() { executeSinglePlan = old })
				app := App{Out: io.Discard, Err: io.Discard,
					RuntimeEnv: snapshotWith(map[string]string{key: "invalid"}),
					Registry:   func() NoteRegistry { return &fakeNoteRegistry{} },
				}
				err := app.run(context.Background(), repo, append(args, planID))
				if tc.rejected {
					if err == nil || !strings.Contains(err.Error(), key) || calls != 0 {
						t.Fatalf("consumed hard cap admitted: calls=%d, error=%v", calls, err)
					}
					return
				}
				if err != nil || calls != 1 {
					t.Fatalf("unused hard cap rejected: calls=%d, error=%v", calls, err)
				}
			})
		}
	}
}

type unavailableDoctorRegistry struct{ fakeNoteRegistry }

func (unavailableDoctorRegistry) Current(context.Context) (taodata.Repo, error) {
	return taodata.Repo{}, errors.New("not a registered checkout")
}

func TestDoctorUsesCapturedReviewRolesAndRepositoryDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setPathExecutables(t)
	snapshot := snapshotWith(map[string]string{
		runtimeconfig.EnvAgent: "claude", runtimeconfig.EnvReviewAgent: "pi",
		runtimeconfig.EnvModel: "invalid model", runtimeconfig.EnvSessionTimeout: "invalid",
	})
	t.Setenv(runtimeconfig.EnvAgent, "invalid")
	t.Setenv(runtimeconfig.EnvReviewAgent, "invalid")
	registry := &fakeNoteRegistry{}
	app := App{RuntimeEnv: snapshot, Registry: func() NoteRegistry { return registry }}
	for _, tc := range []struct {
		name, override, env string
		want                runtimeconfig.AgentKind
	}{
		{"environment", "", "pi", runtimeconfig.AgentPi},
		{"repository", "claude", "", runtimeconfig.AgentClaude},
		{"environment masks repository", "claude", "pi", runtimeconfig.AgentPi},
	} {
		env := map[string]string{runtimeconfig.EnvAgent: "claude", runtimeconfig.EnvModel: "invalid model", runtimeconfig.EnvSessionTimeout: "invalid"}
		if tc.env != "" {
			env[runtimeconfig.EnvReviewAgent] = tc.env
		}
		app.RuntimeEnv = snapshotWith(env)
		// An unrelated malformed saved model must not block role diagnostics.
		repo := (taodata.Repo{ID: "doctor-repo", RunDefaults: &taodata.RepoRunDefaults{Models: &taodata.RepoModelDefaults{Base: "invalid model"}}}).WithReviewAgentDefault(tc.override)
		persistStatusRepository(t, repo)
		registry.current = repo
		report, err := app.collectDoctorReport()
		if err != nil || report.selectedAgent != runtimeconfig.AgentClaude || report.reviewAgent != tc.want || report.repositoryUnavailable {
			t.Fatalf("%s: %+v, %v", tc.name, report, err)
		}
	}
	app.Registry = func() NoteRegistry { return &unavailableDoctorRegistry{} }
	app.RuntimeEnv = snapshotWith(map[string]string{runtimeconfig.EnvAgent: "claude"})
	report, err := app.collectDoctorReport()
	if err != nil || report.reviewAgent != runtimeconfig.AgentClaude || !report.repositoryUnavailable {
		t.Fatalf("outside checkout inheritance: %+v, %v", report, err)
	}
	app.RuntimeEnv = snapshotWith(map[string]string{runtimeconfig.EnvReviewAgent: "invalid"})
	if _, err := app.collectDoctorReport(); err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvReviewAgent) {
		t.Fatalf("invalid reviewer not diagnosed: %v", err)
	}
}

func TestPromptManagementConsumesOnlySelectedAgent(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(runtimeconfig.EnvAgent, "invalid")
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{
		runtimeconfig.EnvAgent: "claude", runtimeconfig.EnvSessionTimeout: "invalid",
	})}
	report, err := app.collectDoctorReport()
	if err != nil || report.selectedAgent != runtimeconfig.AgentClaude {
		t.Fatalf("doctor did not use captured agent independently of session settings: %+v, %v", report, err)
	}
	app.RuntimeEnv = snapshotWith(map[string]string{runtimeconfig.EnvAgent: "invalid"})
	if _, err := app.collectDoctorReport(); err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvAgent) {
		t.Fatalf("doctor admitted invalid selected agent: %v", err)
	}
	if err := app.installPrompts([]string{"--check"}); err != nil {
		t.Fatalf("all-agent prompt discovery consumed selected runtime: %v", err)
	}
}

type noteReworkTestRepository struct {
	fakeRepository
	recording *recordingAutoReworkRepository
}

func (r noteReworkTestRepository) PlanRecord(detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	return r.recording.PlanRecord(detail)
}

func TestNoteRunInheritsCapturedReworkSettings(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, attempts string
		args                    []string
		wantCalls               int
	}{
		{"enabled", "true", "1", nil, 2},
		{"disabled", "false", "0", nil, 1},
		{"zero attempts", "true", "0", nil, 1},
		{"explicit no review", "true", "1", []string{"--no-review"}, 1},
		{"explicit existing flags", "true", "1", []string{"--no-review=false", "--pull-request=false", "--max-slices=2", "--commit-policy=none", "--execution-mode=current", "--dangerously-skip-permissions=false"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTaoEnv(t)
			now := time.Now()
			const planID = "generated-note-plan"
			detail, _ := autoReworkTestDetail(planID, now)
			detail.Dir = t.TempDir()
			repo := newRecordingAutoReworkRepository(planID, detail)
			meta := taodata.Repo{ID: "note-repo", Name: "note", Root: "/repo", RunDefaults: &taodata.RepoRunDefaults{Models: &taodata.RepoModelDefaults{Run: "repository-implementation"}}}
			other := taodata.Repo{ID: "other-repo", Name: "other", Root: "/other", RunDefaults: &taodata.RepoRunDefaults{Models: &taodata.RepoModelDefaults{Run: "wrong-repository"}}}
			app, _, _ := noteTestApp(t, strings.NewReader(""), other, meta)
			t.Setenv("TERM", "xterm-256color")
			terminal := newFakeRunHeaderTerminalWriter(term.Size{Width: 100, Height: 24})
			app.Out = terminal
			app.RuntimeEnv = snapshotWith(map[string]string{
				runtimeconfig.EnvRunHeader:  tc.enabled,
				runtimeconfig.EnvAutoRework: tc.enabled, runtimeconfig.EnvMaxReworkAttempts: tc.attempts,
				runtimeconfig.EnvModel:                 "planning-base",
				runtimeconfig.EnvReworkEscalationModel: "escalated", runtimeconfig.EnvReworkEscalationFromAttempt: "1",
			})
			app.Repository = func(string) Repository {
				return noteReworkTestRepository{fakeRepository: repo.planRunRepository.(fakeRepository), recording: repo}
			}
			app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
				t.Setenv(runtimeconfig.EnvAutoRework, "false")
				t.Setenv(runtimeconfig.EnvMaxReworkAttempts, "0")
				t.Setenv(runtimeconfig.EnvRunModel, "changed")
				t.Setenv(runtimeconfig.EnvRunHeader, "false")
				return &planning.GeneratePlanResult{Allocation: planning.PlanAllocation{ID: planID, Dir: detail.Dir}}, nil
			})
			calls := 0
			old := executeSinglePlan
			t.Cleanup(func() { executeSinglePlan = old })
			executeSinglePlan = func(_ run.Service, _ context.Context, request run.Request) error {
				calls++
				if request.Models.Base != "planning-base" || request.Models.Run != "repository-implementation" {
					t.Fatalf("execution models = %+v", request.Models)
				}
				if tc.name == "explicit existing flags" && (!request.ReviewEnabled || request.PullRequest || request.MaxSlices != 2 || request.CommitPolicy != "none" || request.ExecutionMode != "current") {
					t.Fatalf("explicit note flags lost: %+v", request)
				}
				if calls == 2 {
					detail.State.Status = plan.StatusReviewed
					detail.State.Plan.Review = reworkReview(plan.ReviewVerdictApprove, nil)
				}
				return nil
			}
			item, err := app.noteRepository(meta).Create(context.Background(), "implement fix", nil)
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string{"note", "run", "--repo", meta.ID, item.ID}, tc.args...)
			if err := app.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			if calls != tc.wantCalls {
				t.Fatalf("execution calls = %d, want %d", calls, tc.wantCalls)
			}
			if pinned := strings.Contains(terminal.String(), "\x1b[8;24r"); pinned != (tc.enabled == "true") {
				t.Fatalf("pinned header = %v, captured enabled = %s", pinned, tc.enabled)
			}
			if calls == 2 {
				found := false
				for _, event := range repo.events {
					if event.Type == plan.EventTypeReworkRound {
						found = true
						if event.Model != "escalated" || event.Attempts != 1 {
							t.Fatalf("inherited escalation = %+v", event)
						}
					}
				}
				if !found {
					t.Fatal("missing rework round evidence")
				}
			}
		})
	}
}

func TestAutoReworkUsesTypedSnapshotNotRegisteredDisplayDefaults(t *testing.T) {
	app := App{Err: io.Discard, RuntimeEnv: snapshotWith(map[string]string{
		runtimeconfig.EnvAutoRework: "off", runtimeconfig.EnvMaxReworkAttempts: "0",
	})}
	fs, _, err := app.parseArgs("run", nil, registerRunFlags)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := app.resolveRunAutoReworkPolicy(fs, true)
	if err != nil || policy.Enabled || policy.MaxAttempts != 0 {
		t.Fatalf("typed snapshot lost: %+v, %v", policy, err)
	}
}

func TestInvalidUpdateAllowsDiagnosticEntryWithoutUpdater(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"run", "--help"}, {"status", "--json"}, {"ui"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			clearTaoEnv(t)
			t.Setenv(runtimeconfig.EnvUpdate, "invalid")
			t.Setenv("TAO_DATA_HOME", t.TempDir())
			var out testTerminalBuffer
			updater := &fakeSelfUpdater{}
			registry := taodata.NewRegistry(t.TempDir())
			repositoryCalls := 0
			app := App{
				In: strings.NewReader("q"), Out: &out, Err: io.Discard,
				SelfUpdater: updater,
				Repository: func(string) Repository {
					repositoryCalls++
					return fakeRepository{}
				},
				Registry: func() NoteRegistry { return registry },
				MonitorTicker: func(time.Duration) MonitorTicker {
					return &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
				},
				UITerminal: &uiTerminalStub{size: term.Size{Width: 160, Height: 60}},
			}
			err := app.Run(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if args[0] == "status" && (repositoryCalls != 1 || !strings.Contains(out.String(), `"source": "invalid"`)) {
				t.Fatalf("status did not collect invalid update diagnostic: repositories=%d, output=%s", repositoryCalls, out.String())
			}
			if len(updater.startupModes) != 0 || updater.calls != 0 {
				t.Fatalf("rejected update mode reached updater: %+v", updater)
			}
		})
	}
}
