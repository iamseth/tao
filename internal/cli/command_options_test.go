package cli

import (
	"context"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

type commandOptionsRegistry struct {
	fakeNoteRegistry
	calls int
}

func (r *commandOptionsRegistry) Current(ctx context.Context) (taodata.Repo, error) {
	r.calls++
	return r.fakeNoteRegistry.Current(ctx)
}

func TestResolveCommandOptionsCapturedAndExplicit(t *testing.T) {
	snapshot := snapshotWith(map[string]string{runtimeconfig.EnvPullRequest: "true", runtimeconfig.EnvRunHeader: "false", runtimeconfig.EnvModel: "captured"})
	registry := &commandOptionsRegistry{fakeNoteRegistry: fakeNoteRegistry{current: taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true)}}}}
	app := App{RuntimeEnv: snapshot, Registry: func() NoteRegistry { return registry }}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	app.registerRunFlags(fs)
	if err := fs.Parse([]string{"--pull-request=false", "--no-review", "--no-run-header=false"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeconfig.EnvModel, "changed")
	got, err := app.resolveCommandOptions(context.Background(), fs, runtimeconfig.CommandRun)
	if err != nil {
		t.Fatal(err)
	}
	if registry.calls != 1 || got.RunOptions.PullRequest || got.RunOptions.ReviewEnabled || got.AutoRework.Enabled || got.NoRunHeader || got.RunOptions.Models.Base != "captured" || got.Sources[runtimeconfig.EnvPullRequest] != "flag" {
		t.Fatalf("calls=%d options=%+v", registry.calls, got)
	}
}

func TestReviewCommandOptionsMatchProfile(t *testing.T) {
	for _, permission := range []string{"true", "false"} {
		t.Run(permission, func(t *testing.T) {
			snapshot := snapshotWith(map[string]string{
				runtimeconfig.EnvModel: "env-base", runtimeconfig.EnvReviewModel: "env-review",
				runtimeconfig.EnvSessionTimeout: "37s", runtimeconfig.EnvSkipPermissions: permission,
				runtimeconfig.EnvCommitPolicy: "none", runtimeconfig.EnvExecutionMode: "invalid",
			})
			registry := &commandOptionsRegistry{fakeNoteRegistry: fakeNoteRegistry{current: taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true), Models: &taodata.RepoModelDefaults{Base: "repo-base"}}}}}
			app := App{RuntimeEnv: snapshot, Registry: func() NoteRegistry { return registry }}
			fs := flag.NewFlagSet("review", flag.ContinueOnError)
			registerReviewFlags(fs)
			if err := fs.Parse([]string{"--run", "--model=explicit"}); err != nil {
				t.Fatal(err)
			}
			got, err := app.resolveCommandOptions(context.Background(), fs, runtimeconfig.CommandReview)
			if err != nil {
				t.Fatal(err)
			}
			want, err := runtimeconfig.ResolveCommandOptions(runtimeconfig.CommandOptionsInput{
				Env: *snapshot, Profile: runtimeconfig.CommandReview,
				Repository: runtimeconfig.RunOptionsPatch{PullRequest: new(true), ModelSelection: runtimeconfig.ModelSelection{Base: "repo-base"}},
				Flags:      (runtimeconfig.RunOptionsPatch{}).WithModelForAllRoles("explicit"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if registry.calls != 1 || !reflect.DeepEqual(got, want) || got.SkipPermissions != (permission == "true") || got.RunOptions.PullRequest || got.Sources[runtimeconfig.EnvReviewModel] != "flag" {
				t.Fatalf("calls=%d got=%+v want=%+v", registry.calls, got, want)
			}
		})
	}
}

func TestCommandRunHandoffUsesResolvedOptions(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	registry := &commandOptionsRegistry{}
	app := App{Out: io.Discard, RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvModel: "captured", runtimeconfig.EnvRunHeader: "false"}), Registry: func() NoteRegistry { return registry }}
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
	if registry.calls != 1 || calls != 1 {
		t.Fatalf("repository=%d execution=%d", registry.calls, calls)
	}
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	app.registerRunFlags(fs)
	if err := fs.Parse([]string{"--no-review", "--commit-policy=none"}); err != nil {
		t.Fatal(err)
	}
	options, err := app.resolveCommandOptions(context.Background(), fs, runtimeconfig.CommandRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.executeCommandRun(context.Background(), repo, fixture.id, options); err != nil {
		t.Fatal(err)
	}
	if registry.calls != 2 || calls != 2 {
		t.Fatalf("handoff recomposed options: repository=%d execution=%d", registry.calls, calls)
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
		registry := &fakeNoteRegistry{current: taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true)}}}
		app := App{RuntimeEnv: snapshotWith(nil), Registry: func() NoteRegistry { return registry }}
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		app.registerRunFlags(fs)
		if err := fs.Parse(tt.args); err != nil {
			t.Fatal(err)
		}
		_, err := app.resolveCommandOptions(context.Background(), fs, runtimeconfig.CommandRun)
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
			registry := &commandOptionsRegistry{fakeNoteRegistry: fakeNoteRegistry{current: taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true)}}}}
			app := App{Out: io.Discard, RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvCommitPolicy: "none"}), Registry: func() NoteRegistry { return registry }}
			args := []string{"--run", id}
			if fromPR {
				args = []string{"--from-pr", "--run", id}
			}
			err := app.rework(context.Background(), plan.NewFileRepository(root), args)
			if err == nil || !strings.Contains(err.Error(), "TAO_PULL_REQUEST source=repository") {
				t.Fatalf("expected repository conflict, got %v", err)
			}
			if registry.calls != 1 || readReworkArtifacts(t, dir) != before {
				t.Fatalf("admission mutated artifacts or lookup count=%d", registry.calls)
			}
		})
	}
}

// This matrix tests the shared seam; command wiring is exercised separately by
// run handoff, note/rework admission, review/merge session, and prompt output tests.
// Prompt rendering intentionally has no repository/session/model admission;
// review and merge intentionally omit run commit/publication preferences.
func TestCommandOptionsSixPaths(t *testing.T) {
	snapshot := snapshotWith(map[string]string{
		runtimeconfig.EnvModel: "env-base", runtimeconfig.EnvReviewModel: "env-review",
		runtimeconfig.EnvSessionTimeout: "37s", runtimeconfig.EnvSkipPermissions: "false",
		runtimeconfig.EnvCommitPolicy: "slice", runtimeconfig.EnvExecutionMode: "current",
	})
	registry := &commandOptionsRegistry{fakeNoteRegistry: fakeNoteRegistry{current: taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true), Models: &taodata.RepoModelDefaults{Base: "repo-base"}}}}}
	app := App{RuntimeEnv: snapshot, Registry: func() NoteRegistry { return registry }}
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
			before := registry.calls
			got, err := app.resolveCommandOptions(context.Background(), fs, tt.profile)
			if err != nil {
				t.Fatal(err)
			}
			if tt.name == "run" {
				baseline = got
			}
			wantCalls := 1
			if tt.name == "prompt" {
				wantCalls = 0
			}
			if registry.calls-before != wantCalls {
				t.Fatal("unexpected repository lookup count")
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
			for key, values := range map[string][2]string{
				runtimeconfig.EnvReviewModel:      {got.RunOptions.Models.Review, baseline.RunOptions.Models.Review},
				runtimeconfig.EnvMergeReviewModel: {got.RunOptions.Models.MergeReview, baseline.RunOptions.Models.MergeReview},
				runtimeconfig.EnvResolverModel:    {got.RunOptions.Models.Resolver, baseline.RunOptions.Models.Resolver},
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
	if _, err := app.resolveCommandOptions(context.Background(), fs, runtimeconfig.CommandPromptOther); err != nil {
		t.Fatal(err)
	}
}
