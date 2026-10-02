package cli

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
)

// composedSnapshotWith layers raw JSON repository values beneath the captured
// environment, the shape every executing command sees after composition.
func composedSnapshotWith(env, repository map[string]string) *runtimeconfig.EnvSnapshot {
	values := configtypes.SettingsValues{}
	for key, value := range repository {
		values[key] = json.RawMessage(value)
	}
	snapshot := runtimeconfig.ResolveSettings(nil, values, *snapshotWith(env))
	return &snapshot
}

func TestResolveCommandOptionsCapturedAndExplicit(t *testing.T) {
	snapshot := composedSnapshotWith(
		map[string]string{runtimeconfig.EnvPullRequest: "true", runtimeconfig.EnvRunHeader: "false", runtimeconfig.EnvModel: "captured"},
		map[string]string{"pull_request": "true", "models.model": `"repository"`},
	)
	app := App{RuntimeEnv: snapshot}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	app.registerRunFlags(fs)
	if err := fs.Parse([]string{"--pull-request=false", "--no-review", "--no-run-header=false"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeconfig.EnvModel, "changed")
	got, err := app.resolveCommandOptions(fs, runtimeconfig.CommandRun)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunOptions.PullRequest || got.RunOptions.ReviewEnabled || got.AutoRework.Enabled || got.NoRunHeader || got.RunOptions.Models.Base != "captured" || got.Sources[runtimeconfig.EnvModel] != "env" || got.Sources[runtimeconfig.EnvPullRequest] != "flag" {
		t.Fatalf("options=%+v", got)
	}
}

func TestResolveCommandOptionsRepositoryLayer(t *testing.T) {
	snapshot := composedSnapshotWith(nil, map[string]string{"pull_request": "true", "models.model": `"repository"`, "review_agent": `"claude"`})
	app := App{RuntimeEnv: snapshot}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	app.registerRunFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	got, err := app.resolveCommandOptions(fs, runtimeconfig.CommandRun)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RunOptions.PullRequest || got.RunOptions.Models.Base != "repository" || got.RunOptions.ReviewAgent != runtimeconfig.AgentClaude ||
		got.Sources[runtimeconfig.EnvPullRequest] != "repository" || got.Sources[runtimeconfig.EnvModel] != "repository" || got.Sources[runtimeconfig.EnvReviewAgent] != "repository" {
		t.Fatalf("options=%+v", got)
	}
}

func TestReviewCommandOptionsMatchProfile(t *testing.T) {
	for _, permission := range []string{"true", "false"} {
		t.Run(permission, func(t *testing.T) {
			snapshot := composedSnapshotWith(map[string]string{
				runtimeconfig.EnvModel: "env-base", runtimeconfig.EnvReviewModel: "env-review",
				runtimeconfig.EnvSessionTimeout: "37s", runtimeconfig.EnvSkipPermissions: permission,
				runtimeconfig.EnvCommitPolicy: "none", runtimeconfig.EnvExecutionMode: "invalid",
			}, map[string]string{"pull_request": "true", "models.model": `"repo-base"`})
			app := App{RuntimeEnv: snapshot}
			fs := flag.NewFlagSet("review", flag.ContinueOnError)
			registerReviewFlags(fs)
			if err := fs.Parse([]string{"--run", "--model=explicit"}); err != nil {
				t.Fatal(err)
			}
			got, err := app.resolveCommandOptions(fs, runtimeconfig.CommandReview)
			if err != nil {
				t.Fatal(err)
			}
			want, err := runtimeconfig.ResolveCommandOptions(runtimeconfig.CommandOptionsInput{
				Env: *snapshot, Profile: runtimeconfig.CommandReview,
				Flags: (runtimeconfig.RunOptionsPatch{}).WithModelForAllRoles("explicit"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) || got.SkipPermissions != (permission == "true") || got.RunOptions.PullRequest || got.Sources[runtimeconfig.EnvReviewModel] != "flag" {
				t.Fatalf("got=%+v want=%+v", got, want)
			}
		})
	}
}

func TestCommandRunHandoffUsesResolvedOptions(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	app := App{Out: io.Discard, RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvModel: "captured", runtimeconfig.EnvRunHeader: "false"}), Registry: func() NoteRegistry { return &fakeNoteRegistry{} }}
	old := executeSinglePlan
	t.Cleanup(func() { executeSinglePlan = old })
	calls := 0
	executeSinglePlan = func(_ run.Service, _ context.Context, request run.Request) error {
		calls++
		if request.Models.Base != "captured" || request.ReviewEnabled || request.PullRequest || request.CommitPolicy != runtimeconfig.CommitPolicyNone {
			t.Fatalf("request=%+v", request)
		}
		return nil
	}
	repo := plan.NewFileRepository(fixture.root)
	if err := app.run(context.Background(), repo, []string{"--no-review", "--commit-policy=none", fixture.id}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("execution=%d", calls)
	}
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	app.registerRunFlags(fs)
	if err := fs.Parse([]string{"--no-review", "--commit-policy=none"}); err != nil {
		t.Fatal(err)
	}
	options, err := app.resolveCommandOptions(fs, runtimeconfig.CommandRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.executeCommandRun(context.Background(), repo, fixture.id, options); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("handoff recomposed options: execution=%d", calls)
	}
}

func TestResolveCommandOptionsStrictConflicts(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want []string
	}{
		{[]string{"--commit-policy=none"}, []string{"requires commit policy slice", "TAO_PULL_REQUEST source=repository", "TAO_COMMIT_POLICY source=flag"}},
		{[]string{"--max-rework-attempts=-1"}, []string{"TAO_MAX_REWORK_ATTEMPTS source=flag"}},
	} {
		app := App{RuntimeEnv: composedSnapshotWith(nil, map[string]string{"pull_request": "true"})}
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		app.registerRunFlags(fs)
		if err := fs.Parse(tt.args); err != nil {
			t.Fatal(err)
		}
		_, err := app.resolveCommandOptions(fs, runtimeconfig.CommandRun)
		for _, want := range tt.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %s: %v", want, err)
			}
		}
	}
}

func TestReworkRunRepositoryConflictBeforeMutation(t *testing.T) {
	for _, fromPR := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "from-pr"}[fromPR], func(t *testing.T) {
			root := t.TempDir()
			const id = "20260628-1200-conflict"
			dir := writeCLIReworkPlan(t, root, id, plan.StatusCompleted, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{{File: "file.go", Message: "fix"}}))
			before := readReworkArtifacts(t, dir)
			app := App{Out: io.Discard, RuntimeEnv: composedSnapshotWith(map[string]string{runtimeconfig.EnvCommitPolicy: "none"}, map[string]string{"pull_request": "true"}), Registry: func() NoteRegistry { return &fakeNoteRegistry{} }}
			args := []string{"--run", id}
			if fromPR {
				args = []string{"--from-pr", "--run", id}
			}
			err := app.rework(context.Background(), plan.NewFileRepository(root), args)
			if err == nil || !strings.Contains(err.Error(), "TAO_PULL_REQUEST source=repository") {
				t.Fatalf("expected repository conflict, got %v", err)
			}
			if readReworkArtifacts(t, dir) != before {
				t.Fatal("admission mutated artifacts")
			}
		})
	}
}

// This matrix tests the shared seam; command wiring is exercised separately by
// run handoff, note/rework admission, review/merge session, and prompt output tests.
// Prompt rendering intentionally has no repository/session/model admission;
// review and merge intentionally omit run commit/publication preferences.
func TestCommandOptionsSixPaths(t *testing.T) {
	snapshot := composedSnapshotWith(map[string]string{
		runtimeconfig.EnvModel: "env-base", runtimeconfig.EnvReviewModel: "env-review",
		runtimeconfig.EnvSessionTimeout: "37s", runtimeconfig.EnvSkipPermissions: "false",
		runtimeconfig.EnvCommitPolicy: "slice", runtimeconfig.EnvExecutionMode: "current",
	}, map[string]string{"pull_request": "true", "models.model": `"repo-base"`, "models.merge_review_model": `"repo-merge"`})
	app := App{RuntimeEnv: snapshot}
	var baseline runtimeconfig.CommandOptions
	for _, tt := range []struct {
		name     string
		profile  runtimeconfig.CommandProfile
		register func(*flag.FlagSet)
	}{
		{"run", runtimeconfig.CommandRun, app.registerRunFlags},
		{"note", runtimeconfig.CommandRun, app.registerNoteFlags},
		{"rework", runtimeconfig.CommandRun, registerReworkFlags},
		{"review", runtimeconfig.CommandReview, registerReviewFlags},
		{"merge", runtimeconfig.CommandMerge, registerMergeFlags},
		{"prompt", runtimeconfig.CommandPromptRun, app.registerPromptFlags},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tt.name, flag.ContinueOnError)
			tt.register(fs)
			if err := fs.Parse(nil); err != nil {
				t.Fatal(err)
			}
			got, err := app.resolveCommandOptions(fs, tt.profile)
			if err != nil {
				t.Fatal(err)
			}
			if tt.name == "run" {
				baseline = got
			}
			for key, source := range got.Sources {
				if other, ok := baseline.Sources[key]; ok && source != other {
					t.Errorf("%s source=%s, run=%s", key, source, other)
				}
			}
			if tt.profile == runtimeconfig.CommandRun || tt.profile == runtimeconfig.CommandPromptRun {
				if got.RunOptions.CommitPolicy != baseline.RunOptions.CommitPolicy || got.RunOptions.ExecutionMode != baseline.RunOptions.ExecutionMode {
					t.Fatal("run/render options diverged")
				}
			}
			if tt.profile == runtimeconfig.CommandRun && !reflect.DeepEqual(got, baseline) {
				t.Fatal("full execution options diverged")
			}
			if tt.profile == runtimeconfig.CommandMerge && (got.RunOptions.Models.MergeReview != "repo-merge" || got.Sources[runtimeconfig.EnvMergeReviewModel] != "repository") {
				t.Fatalf("merge lost repository role: %+v", got)
			}
			for key, values := range map[string][2]string{
				runtimeconfig.EnvReviewModel:   {got.RunOptions.Models.Review, baseline.RunOptions.Models.Review},
				runtimeconfig.EnvResolverModel: {got.RunOptions.Models.Resolver, baseline.RunOptions.Models.Resolver},
			} {
				if _, applicable := got.Sources[key]; applicable && values[0] != values[1] {
					t.Errorf("%s values diverged: %v", key, values)
				}
			}
			if tt.profile != runtimeconfig.CommandPromptRun {
				if got.RunOptions.Models.Base != baseline.RunOptions.Models.Base || got.RunOptions.SessionTimeout != baseline.RunOptions.SessionTimeout || got.SkipPermissions != baseline.SkipPermissions {
					t.Fatal("session options diverged")
				}
			}
		})
	}
}

func TestResolveCommandOptionsWithoutRepositoryProfile(t *testing.T) {
	app := App{RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvAgent: "invalid"}), Registry: func() NoteRegistry { t.Fatal("unexpected repository lookup"); return nil }}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	if _, err := app.resolveCommandOptions(fs, runtimeconfig.CommandPromptOther); err != nil {
		t.Fatal(err)
	}
}

// persistRepositorySettings stores the repository under a fresh data home so
// settings composition finds its run defaults. The repository keeps its own
// root (which must be absolute) and name.
func persistRepositorySettings(t *testing.T, repo taodata.Repo) taodata.Repo {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	repo.Schema = taodata.RepoSchema
	if repo.Name == "" {
		repo.Name = "test-repo"
	}
	dir := filepath.Join(home, "repos", repo.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return repo
}

// A corrupt global configuration file blocks every execution profile, even
// when the plan's recorded repository is unregistered and composition returns
// the invocation baseline untouched.
func TestResolveCommandOptionsRejectsStructuralGlobalLoadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(nil), Registry: func() NoteRegistry { return &fakeNoteRegistry{} }}
	app = app.initializeSettings(context.Background())
	if app.settingsGlobal == nil || app.settingsGlobal.LoadError == nil {
		t.Fatalf("corrupt global settings not surfaced: %+v", app.settingsGlobal)
	}
	composed, err := app.settingsForPlanRoot(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []runtimeconfig.CommandProfile{runtimeconfig.CommandRun, runtimeconfig.CommandReview, runtimeconfig.CommandMerge, runtimeconfig.CommandPromptRun} {
		fs := flag.NewFlagSet(string(profile), flag.ContinueOnError)
		composed.registerRunFlags(fs)
		if _, err := composed.resolveCommandOptions(fs, profile); err == nil || !strings.Contains(err.Error(), "config.json") {
			t.Fatalf("%s admitted a corrupt global configuration: %v", profile, err)
		}
	}
	fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	if err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{fixture.id}); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Fatalf("run admitted a corrupt global configuration: %v", err)
	}
	if _, err := settings.NewService(home, *snapshotWith(nil)).Read(context.Background(), settings.Target{Global: true}); err == nil {
		t.Fatal("service did not report the structural error")
	}
}

// A registered repository whose repo.json is unreadable is omitted from the
// catalog listing; composition must still find the registration by its
// derived identifier and refuse execution rather than fall back to baseline.
func TestSettingsForPlanRootFailsClosedOnUnreadableRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	fixture := newRunPlanFixture(t, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	detail, err := plan.NewFileRepository(fixture.root).ResolvePlan(context.Background(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	root := detail.State.Repo.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	id := taodata.RepoID(filepath.Clean(root))
	dir := filepath.Join(home, "repos", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := taodata.Registry{DataHome: home}
	if repos, err := registry.ListRepos(); err != nil || len(repos) != 0 {
		t.Fatalf("catalog listing should omit the unreadable registration: %v %v", repos, err)
	}
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(nil), Registry: func() NoteRegistry { return registry }}
	if _, err := app.settingsForPlanRoot(context.Background(), detail.State.Repo.Root); err == nil {
		t.Fatal("unreadable registration composed as unregistered")
	}
	if err := app.run(context.Background(), plan.NewFileRepository(fixture.root), []string{fixture.id}); err == nil || !strings.Contains(err.Error(), "repo.json") {
		t.Fatalf("run admitted an unreadable registration: %v", err)
	}
	// An unregistered root still uses the invocation baseline.
	if _, err := app.settingsForPlanRoot(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("unregistered root rejected: %v", err)
	}
}

// A linked worktree is registered through its control checkout. When that sole
// registration is unreadable, the catalog listing is empty, yet the plan must
// still be refused rather than treated as unregistered.
func TestSettingsForPlanRootFailsClosedOnUnreadableControlCheckout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	control := initTestGitRepo(t)
	if resolved, err := filepath.EvalSymlinks(control); err == nil {
		control = resolved
	}
	initRunGit(t, control, "-c", "user.name=tao", "-c", "user.email=tao@example.com", "commit", "--allow-empty", "-m", "init")
	linked := filepath.Join(t.TempDir(), "linked")
	initRunGit(t, control, "worktree", "add", "--detach", linked)
	t.Cleanup(func() {
		remove := exec.Command("git", "worktree", "remove", "--force", linked) //nolint:gosec // G204: fixed git command with test paths
		remove.Dir = control
		_ = remove.Run()
	})
	dir := filepath.Join(home, "repos", taodata.RepoID(filepath.Clean(control)))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := taodata.Registry{DataHome: home}
	if repos, err := registry.ListRepos(); err != nil || len(repos) != 0 {
		t.Fatalf("catalog listing should omit the unreadable registration: %v %v", repos, err)
	}
	app := App{Out: io.Discard, Err: io.Discard, RuntimeEnv: snapshotWith(nil), Registry: func() NoteRegistry { return registry }}
	if _, err := app.settingsForPlanRoot(context.Background(), linked); err == nil || !strings.Contains(err.Error(), "repo.json") {
		t.Fatalf("linked worktree of an unreadable registration composed as unregistered: %v", err)
	}
}
