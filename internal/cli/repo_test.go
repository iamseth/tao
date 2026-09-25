package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
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

func TestRepositoryPullRequestDefaultAppliesToRunAndExplicitFlagWins(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv(runtimeconfig.EnvPullRequest, "false")
	value := true
	registered := taodata.Repo{ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{PullRequest: &value}}
	registry := &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}}
	app := App{Out: io.Discard, Err: io.Discard, Registry: func() NoteRegistry { return registry }}

	first := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	active := &first
	oldExecutor := executeSinglePlan
	var requests []run.Request
	executeSinglePlan = func(_ run.Service, _ context.Context, got run.Request) error {
		requests = append(requests, got)
		active.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
		return nil
	}
	t.Cleanup(func() { executeSinglePlan = oldExecutor })

	if err := app.run(context.Background(), plan.NewFileRepository(first.root), []string{"--no-review", first.id}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || !requests[0].PullRequest {
		t.Fatalf("direct request = %#v, want repository pull_request true", requests)
	}

	second := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	active = &second
	if err := app.run(context.Background(), plan.NewFileRepository(second.root), []string{"--pull-request=false", "--no-review", second.id}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[1].PullRequest {
		t.Fatalf("explicit --pull-request=false did not override repository default: %#v", requests)
	}
}

func TestStatusShowsEffectiveRepositoryPullRequestSource(t *testing.T) {
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
	if !strings.Contains(line, "true") || !strings.Contains(line, "repository") {
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
			if err != nil || out.String() != tt.want {
				t.Fatalf("repo show = %q, %v; want %q", out.String(), err, tt.want)
			}
		})
	}

	var out bytes.Buffer
	err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"repo", "config", "--pull-request=true", "bad-json"})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
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
