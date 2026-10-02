package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
)

func TestRepoListAndShow(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	root := initTestGitRepo(t)
	repo := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Name: "repo", Root: root, Branch: "main", RemoteURL: "https://example.com/repo.git", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	registry := taodata.Registry{DataHome: dataHome}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataHome, "repos", repo.ID, "plans", "plan-a"), 0o700); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"repo", "list"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"REPO ID", "repo-a", "ok", "1", root} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("repo list missing %q: %s", want, out.String())
		}
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"repo", "show", "repo"}); err != nil {
		t.Fatal(err)
	}
	want := "Repo: repo\nID: repo-a\nRoot: " + root + "\nBranch: main\nRemote: https://example.com/repo.git\nPlans: 1\nHealth: ok\nFinding: ok\n"
	if out.String() != want {
		t.Fatalf("repo show = %q, want %q", out.String(), want)
	}
}

func TestRepoConfigShowsUnsetAndSetsPullRequest(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	root := initTestGitRepo(t)
	repo := taodata.Repo{
		Schema:    taodata.RepoSchema,
		ID:        taodata.RepoID(root),
		Name:      "repo",
		Root:      root,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	registry := taodata.Registry{DataHome: dataHome}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"repo", "config"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pull_request: unset") {
		t.Fatalf("unset config output = %q", out.String())
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"repo", "config", "--pull-request", "true"}); err != nil {
		t.Fatal(err)
	}
	stored, err := registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := stored.PullRequestDefault(); !ok || !value {
		t.Fatalf("stored pull_request = (%t, %t), want (true, true)", value, ok)
	}
	if !strings.Contains(out.String(), "pull_request: true") {
		t.Fatalf("set config output = %q", out.String())
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"repo", "config", "--pull-request=false"}); err != nil {
		t.Fatal(err)
	}
	stored, err = registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := stored.PullRequestDefault(); !ok || value {
		t.Fatalf("stored pull_request = (%t, %t), want (false, true)", value, ok)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"repo", "config", "--pull-request=unset"}); err != nil {
		t.Fatal(err)
	}
	stored, err = registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := stored.PullRequestDefault(); ok || value {
		t.Fatalf("unset pull_request = (%t, %t), want (false, false)", value, ok)
	}
	if !strings.Contains(out.String(), "pull_request: unset") {
		t.Fatalf("unset config output = %q", out.String())
	}
}

func TestRepoConfigReviewAgent(t *testing.T) {
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	t.Chdir(initTestGitRepo(t))
	registry := taodata.NewRegistry("")
	repo, err := registry.RegisterCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	for _, value := range []string{"claude", "pi", "unset"} {
		out.Reset()
		if err := app.Run(context.Background(), []string{"repo", "config", "--review-agent=" + value, "--pull-request=false", "--review-model=provider/review"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "review_agent: "+value+"\n") {
			t.Fatal(out.String())
		}
		stored, err := registry.ReadRepo(repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := stored.ReviewAgentDefault()
		if (value == "unset" && ok) || (value != "unset" && got != value) {
			t.Fatalf("stored selector = %q, %t", got, ok)
		}
		models, _ := stored.ModelDefaults()
		pr, hasPR := stored.PullRequestDefault()
		if models.Review != "provider/review" || !hasPR || pr {
			t.Fatalf("lost sibling defaults: %#v", stored)
		}
	}
	before, err := registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "invalid", "PI", " claude"} {
		err := app.Run(context.Background(), []string{"repo", "config", "--pull-request=true", "--review-agent=" + value})
		if err == nil || !strings.Contains(err.Error(), "--review-agent") {
			t.Fatalf("%q: %v", value, err)
		}
	}
	after, err := registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatal("invalid selector mutated registry")
	}
}

func TestRepoConfigModelDefaults(t *testing.T) {
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	root := initTestGitRepo(t)
	t.Chdir(root)
	registry := taodata.NewRegistry("")
	repo, err := registry.RegisterCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	flags := []string{"model", "run-model", "review-model", "merge-review-model", "resolver-model", "rework-escalation-model"}
	keys := []string{"model", "run_model", "review_model", "merge_review_model", "resolver_model", "rework_escalation_model"}
	if err := app.Run(context.Background(), []string{"repo", "config"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if !strings.Contains(out.String(), key+": unset\n") {
			t.Errorf("missing unset %s in %q", key, out.String())
		}
	}
	args := []string{"repo", "config", "--pull-request=true"}
	for _, flag := range flags {
		args = append(args, "--"+flag+"=provider/"+flag)
	}
	out.Reset()
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		if !strings.Contains(out.String(), key+": provider/"+flags[i]+"\n") {
			t.Errorf("missing set %s in %q", key, out.String())
		}
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"repo", "config", repo.ID}); err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		if !strings.Contains(out.String(), key+": provider/"+flags[i]+"\n") {
			t.Errorf("missing persisted %s in %q", key, out.String())
		}
	}
	// Unsetting one key must preserve all siblings and pull_request.
	for i, flag := range flags {
		if err := app.Run(context.Background(), []string{"repo", "config", "--" + flag + "=unset"}); err != nil {
			t.Fatal(err)
		}
		stored, err := registry.ReadRepo(repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		content, err := json.Marshal(stored.RunDefaults)
		if err != nil {
			t.Fatal(err)
		}
		var defaults struct {
			PullRequest bool              `json:"pull_request"`
			Models      map[string]string `json:"models"`
		}
		if err := json.Unmarshal(content, &defaults); err != nil {
			t.Fatal(err)
		}
		if !defaults.PullRequest {
			t.Fatal("unsetting model removed pull_request")
		}
		for j, key := range keys {
			want := ""
			if j > i {
				want = "provider/" + flags[j]
			}
			if got := defaults.Models[key]; got != want {
				t.Errorf("after unsetting %s, %s = %q, want %q", flag, key, got, want)
			}
		}
	}
	for _, flag := range flags {
		for _, invalid := range []string{"", "bad model", "   ", "tab\tmodel", "line\nmodel"} {
			err := app.Run(context.Background(), []string{"repo", "config", "--pull-request=false", "--" + flag + "=" + invalid})
			if err == nil || !strings.Contains(err.Error(), "--"+flag) {
				t.Errorf("%s=%q error = %v, want flag-specific rejection", flag, invalid, err)
			}
		}
	}
	stored, err := registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := stored.PullRequestDefault(); !ok || !value {
		t.Fatal("invalid model partially persisted pull_request change")
	}
}

func TestRepositoryPullRequestDefaultAppliesToRunAndExplicitFlagWins(t *testing.T) {
	clearTaoEnv(t)
	ctx := context.Background()
	home := t.TempDir()
	first := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	detail, err := plan.NewFileRepository(first.root).ResolvePlan(ctx, first.id)
	if err != nil {
		t.Fatal(err)
	}
	// The repository is selected by the plan's recorded root, and its persisted
	// pull_request default is read from the data-home repo.json.
	registered := taodata.Repo{ID: "repo-a", Root: detail.State.Repo.Root}
	dir := filepath.Join(home, "repos", registered.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"schema": taodata.RepoSchema, "id": registered.ID, "name": "repo", "root": registered.Root, "run_defaults": map[string]any{"pull_request": true}})
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	newApp := func(environment map[string]string) App {
		return (App{Out: io.Discard, Err: io.Discard, SettingsService: settings.NewService(home, *snapshotWith(environment)), Registry: func() NoteRegistry {
			return &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}}
		}}).initializeSettings(ctx)
	}

	active := &first
	oldExecutor := executeSinglePlan
	var requests []run.Request
	executeSinglePlan = func(_ run.Service, _ context.Context, got run.Request) error {
		requests = append(requests, got)
		active.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
		return nil
	}
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	app := newApp(nil)
	if err := app.run(ctx, plan.NewFileRepository(first.root), []string{"--no-review", first.id}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || !requests[0].PullRequest {
		t.Fatalf("direct request = %#v, want repository pull_request true", requests)
	}

	second := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	active = &second
	if err := app.run(ctx, plan.NewFileRepository(second.root), []string{"--pull-request=false", "--no-review", second.id}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[1].PullRequest {
		t.Fatalf("explicit --pull-request=false did not override repository default: %#v", requests)
	}

	// Environment sits above the repository layer, so an explicit environment
	// false masks the persisted repository true.
	third := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	active = &third
	masked := newApp(map[string]string{runtimeconfig.EnvPullRequest: "false"})
	if err := masked.run(ctx, plan.NewFileRepository(third.root), []string{"--no-review", third.id}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[2].PullRequest {
		t.Fatalf("environment false did not mask repository pull_request true: %#v", requests)
	}

	fourth := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	active = &fourth
	if err := masked.run(ctx, plan.NewFileRepository(fourth.root), []string{"--pull-request=true", "--no-review", fourth.id}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 4 || !requests[3].PullRequest {
		t.Fatalf("explicit --pull-request=true did not override environment false: %#v", requests)
	}
}

func TestStatusShowsEnvironmentMasksRepositoryPullRequestSource(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv(runtimeconfig.EnvPullRequest, "false")
	value := true
	registered := taodata.Repo{ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{PullRequest: &value}}
	registry := &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}}
	var out bytes.Buffer
	app := App{
		Out:        &out,
		Repository: func(string) Repository { return fakeRepository{} },
		Registry:   func() NoteRegistry { return registry },
	}
	if err := app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	line := ""
	for _, candidate := range strings.Split(out.String(), "\n") {
		if strings.Contains(candidate, runtimeconfig.EnvPullRequest) {
			line = candidate
			break
		}
	}
	if !strings.Contains(line, "false") || !strings.Contains(line, "env") {
		t.Fatalf("effective pull_request status line = %q; output=%q", line, out.String())
	}
}

func TestRepoDoctorReportsErrorsAndReturnsNonZero(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	registry := taodata.Registry{DataHome: dataHome}
	if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: "missing", Name: "missing", Root: filepath.Join(t.TempDir(), "missing")}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataHome, "repos", "bad-json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataHome, "repos", "bad-json", "repo.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"repo", "doctor"})
	if err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("expected unhealthy repo error, got %v", err)
	}
	for _, want := range []string{"missing [missing_root]", "bad-json [metadata_error]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("repo doctor missing %q: %s", want, out.String())
		}
	}
}

func TestRepoShowUsesSharedSelectorPolicy(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	registry := taodata.Registry{DataHome: dataHome}
	for _, repo := range []taodata.Repo{
		{ID: "repo-a", Name: "shared"},
		{ID: "repo-b", Name: "unique-name"},
		{ID: "other-alpha", Name: "repo-a"},
		{ID: "fourth", Name: "other-a"},
		{ID: "other-b", Name: "shared"},
		{ID: "third-a", Name: "repo"},
		{ID: "third-b", Name: "repo-"},
	} {
		repo.Schema = taodata.RepoSchema
		repo.Root = filepath.Join(dataHome, "missing-root")
		if err := registry.WriteRepo(repo); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		selector string
		wantID   string
		wantErr  string
	}{
		{selector: "repo-b", wantID: "repo-b"},
		{selector: "other-a", wantID: "other-alpha"},
		{selector: "third-a", wantID: "third-a"},
		{selector: "unique-name", wantID: "repo-b"},
		{selector: "repo-a", wantID: "repo-a"},
		{selector: "repo", wantErr: `repository "repo" is ambiguous; use one of these IDs: repo-a, repo-b`},
		{selector: "repo-", wantErr: `repository "repo-" is ambiguous; use one of these IDs: repo-a, repo-b`},
		{selector: "shared", wantErr: `repository "shared" is ambiguous; use one of these IDs: other-b, repo-a`},
		{selector: "unknown", wantErr: `repository "unknown" is not registered; run tao init in that checkout`},
		{selector: "unique", wantErr: `repository "unique" is not registered; run tao init in that checkout`},
	} {
		t.Run(tt.selector, func(t *testing.T) {
			var out bytes.Buffer
			err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"repo", "show", tt.selector})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("repo show error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !strings.Contains(out.String(), "ID: "+tt.wantID+"\n") {
				t.Fatalf("repo show = %q, %v", out.String(), err)
			}
		})
	}
}

func TestRepoShowPreservesUnhealthyCatalogDetails(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	registry := taodata.Registry{DataHome: dataHome}
	root := filepath.Join(dataHome, "absent")
	repo := taodata.Repo{Schema: taodata.RepoSchema, ID: "missing-root", Name: "missing", Root: root, Branch: "main", RemoteURL: "https://example.com/repo.git"}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(registry.PlansDir(repo), "plan-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Malformed entries historically have zero catalog plan count, even if
	// their data-home plan directories survive.
	badDir := filepath.Join(dataHome, "repos", "bad-json")
	if err := os.MkdirAll(filepath.Join(badDir, "plans", "plan-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "repo.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		selector string
		want     string
	}{
		{selector: "missing", want: "Repo: missing\nID: missing-root\nRoot: " + root + "\nBranch: main\nRemote: https://example.com/repo.git\nPlans: 1\nHealth: missing_root\nFinding: repo root does not exist\n"},
		{selector: "bad-json", want: "Repo: -\nID: bad-json\nRoot: -\nBranch: -\nRemote: -\nPlans: 0\nHealth: metadata_error\nFinding: repo metadata cannot be read: read repo metadata: unexpected end of JSON input\n"},
		{selector: "bad-j", want: "Repo: -\nID: bad-json\nRoot: -\nBranch: -\nRemote: -\nPlans: 0\nHealth: metadata_error\nFinding: repo metadata cannot be read: read repo metadata: unexpected end of JSON input\n"},
	} {
		t.Run(tt.selector, func(t *testing.T) {
			var out bytes.Buffer
			err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"repo", "show", tt.selector})
			if (err != nil) != strings.HasPrefix(tt.selector, "bad-") || !strings.HasPrefix(out.String(), tt.want) {
				t.Fatalf("repo show = %q, %v; want %q", out.String(), err, tt.want)
			}
		})
	}

	var out bytes.Buffer
	err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"repo", "config", "--pull-request=true", "bad-json"})
	if err == nil || !strings.Contains(err.Error(), "repair the file manually") {
		t.Fatalf("config accepted malformed metadata: %v", err)
	}
}

func TestRepoUsageErrors(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	for _, args := range [][]string{{"repo"}, {"repo", "list", "extra"}, {"repo", "show"}, {"repo", "config", "one", "two"}, {"repo", "doctor", "extra"}, {"repo", "bad"}} {
		if err := app.Run(context.Background(), args); err == nil {
			t.Fatalf("Run(%v) succeeded unexpectedly", args)
		}
	}
}

func TestRepoConfigReworkDefaults(t *testing.T) {
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	registry := taodata.NewRegistry("")
	repo := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Name: "repo", Root: initTestGitRepo(t)}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	call := func(args ...string) error {
		out.Reset()
		return app.Run(context.Background(), append(append([]string{"repo", "config"}, args...), repo.ID))
	}
	if err := call(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"max_rework_attempts", "rework_escalation_from_attempt"} {
		if !strings.Contains(out.String(), key+": unset\n") {
			t.Fatalf("legacy output: %s", &out)
		}
	}
	if err := call("--max-rework-attempts=0", "--rework-escalation-from-attempt=1", "--model=provider/base", "--pull-request=false"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "max_rework_attempts: 0\n") {
		t.Fatal(out.String())
	}
	stored, err := registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(stored)
	for _, flag := range []string{"max-rework-attempts", "rework-escalation-from-attempt"} {
		invalids := []string{"", "-1", "x", "1.5", "9999999999999999999999999"}
		if flag == "rework-escalation-from-attempt" {
			invalids = append(invalids, "0")
		}
		for _, value := range invalids {
			if err := call("--pull-request=true", "--model=changed", "--"+flag+"="+value); err == nil {
				t.Fatalf("accepted %s=%q", flag, value)
			}
			got, err := registry.ReadRepo(repo.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(got)
			if !bytes.Equal(before, after) {
				t.Fatalf("invalid value wrote repository: %s", after)
			}
		}
	}
	if err := call("--max-rework-attempts=unset"); err != nil {
		t.Fatal(err)
	}
	stored, err = registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RunDefaults.MaxReworkAttempts != nil || *stored.RunDefaults.ReworkEscalationFromAttempt != 1 {
		t.Fatalf("unset lost sibling: %+v", stored.RunDefaults)
	}
	if err := call("--max-rework-attempts=7", "--rework-escalation-from-attempt=unset"); err != nil {
		t.Fatal(err)
	}
	stored, err = registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *stored.RunDefaults.MaxReworkAttempts != 7 || stored.RunDefaults.ReworkEscalationFromAttempt != nil {
		t.Fatalf("set/unset: %+v", stored.RunDefaults)
	}
	if value, ok := stored.PullRequestDefault(); !ok || value {
		t.Fatal("lost explicit false")
	}
	if models, _ := stored.ModelDefaults(); models.Base != "provider/base" {
		t.Fatal("lost model")
	}
	if err := call("--model=other", "--pull-request=true"); err != nil {
		t.Fatal(err)
	}
	stored, err = registry.ReadRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *stored.RunDefaults.MaxReworkAttempts != 7 {
		t.Fatal("unmentioned count lost")
	}
}
