package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{"Repo: repo", "ID: repo-a", "Plans: 1", "Health: ok", "Finding: ok"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("repo show missing %q: %s", want, out.String())
		}
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

func TestRepoShowRejectsAmbiguousPrefix(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	registry := taodata.Registry{DataHome: dataHome}
	for _, id := range []string{"repo-a", "repo-b"} {
		if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: id, Name: id, Root: initTestGitRepo(t)}); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	err := (App{Out: &out, Err: &out}).Run(context.Background(), []string{"repo", "show", "repo"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous prefix error, got %v", err)
	}
}

func TestRepoUsageErrors(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	for _, args := range [][]string{{"repo"}, {"repo", "list", "extra"}, {"repo", "show"}, {"repo", "doctor", "extra"}, {"repo", "bad"}} {
		if err := app.Run(context.Background(), args); err == nil {
			t.Fatalf("Run(%v) succeeded unexpectedly", args)
		}
	}
}
