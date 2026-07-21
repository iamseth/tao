package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitJSONRegistersRepoAndAllocatesPlan(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	withWorkingDir(t, repoRoot, func() {
		if err := app.Run(context.Background(), []string{"init", "--slug", "Example Plan", "--json"}); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out.String())
		}
	})

	var payload struct {
		Schema   string `json:"schema"`
		DataHome string `json:"data_home"`
		Repo     struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Root      string `json:"root"`
			Branch    string `json:"branch"`
			RemoteURL string `json:"remote_url"`
		} `json:"repo"`
		Plan struct {
			ID  string `json:"id"`
			Dir string `json:"dir"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out.String())
	}
	if payload.Schema != "tao.init.v1" || payload.DataHome != dataHome {
		t.Fatalf("unexpected init payload: %+v", payload)
	}
	if payload.Repo.ID == "" || payload.Repo.Root != repoRoot || payload.Repo.Branch != "main" || payload.Repo.RemoteURL != "https://example.com/repo.git" {
		t.Fatalf("unexpected repo payload: %+v", payload.Repo)
	}
	if !strings.HasSuffix(payload.Plan.ID, "-example-plan") {
		t.Fatalf("unexpected plan id %q", payload.Plan.ID)
	}
	if payload.Plan.Dir != filepath.Join(dataHome, "repos", payload.Repo.ID, "plans", payload.Plan.ID) {
		t.Fatalf("unexpected plan dir %q", payload.Plan.Dir)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "repos", payload.Repo.ID, "repo.json")); err != nil {
		t.Fatalf("repo.json not written: %v", err)
	}
	if info, err := os.Stat(payload.Plan.Dir); err != nil || !info.IsDir() {
		t.Fatalf("plan dir not created: info=%v err=%v", info, err)
	}
}

func TestInitDefaultOutputRegistersWithoutPlan(t *testing.T) {
	repoRoot := initTestGitRepo(t)
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	withWorkingDir(t, repoRoot, func() {
		if err := app.Run(context.Background(), []string{"init"}); err != nil {
			t.Fatalf("init failed: %v\n%s", err, out.String())
		}
	})
	text := out.String()
	for _, want := range []string{"registered Tao repository", "data home: " + dataHome, "repo: "} {
		if !strings.Contains(text, want) {
			t.Fatalf("init output missing %q: %s", want, text)
		}
	}
	plansRoot := filepath.Join(dataHome, "repos")
	if _, err := os.Stat(plansRoot); err != nil {
		t.Fatalf("repo registration not written: %v", err)
	}
	if strings.Contains(text, "plan dir:") {
		t.Fatalf("default init should not allocate a plan: %s", text)
	}
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initRunGit(t, root, "init", "-b", "main")
	initRunGit(t, root, "remote", "add", "origin", "https://example.com/repo.git")
	return root
}

func initRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: fixed git command with test args
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}
