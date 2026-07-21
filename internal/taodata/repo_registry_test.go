package taodata

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoIDStableFromCanonicalRoot(t *testing.T) {
	root := filepath.Clean("/tmp/example-repo")
	first := RepoID(root)
	second := RepoID(root)
	if first != second {
		t.Fatalf("RepoID not stable: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "example-repo-") || len(strings.TrimPrefix(first, "example-repo-")) != 12 {
		t.Fatalf("unexpected repo id shape %q", first)
	}
	if first == RepoID(filepath.Join(root, "other")) {
		t.Fatalf("RepoID should change with canonical root")
	}
}

func TestRegistryWritesRepoMetadata(t *testing.T) {
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome, Now: fixedNow}
	repo := Repo{Schema: RepoSchema, ID: "repo-123", Name: "repo", Root: "/repo", Branch: "main", RemoteURL: "git@example.com/repo.git", UpdatedAt: fixedNow().UTC().Format(time.RFC3339)}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatalf("WriteRepo() failed: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dataHome, "repos", repo.ID, "repo.json")) //nolint:gosec // G304: test reads from test-controlled data home
	if err != nil {
		t.Fatalf("read repo metadata: %v", err)
	}
	var got Repo
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatalf("unmarshal repo metadata: %v", err)
	}
	if got != repo {
		t.Fatalf("repo metadata = %+v, want %+v", got, repo)
	}
}

func TestRegistryWriteRepoReplacesMetadataAtomically(t *testing.T) {
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome, Now: fixedNow}
	repo := Repo{Schema: RepoSchema, ID: "repo-123", Name: "old-name", Root: "/repo", UpdatedAt: fixedNow().UTC().Format(time.RFC3339)}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatalf("initial WriteRepo() failed: %v", err)
	}

	repo.Name = "new-name"
	repo.Branch = "main"
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatalf("replacement WriteRepo() failed: %v", err)
	}

	repoDir := filepath.Join(dataHome, "repos", repo.ID)
	path := filepath.Join(repoDir, "repo.json")
	content, err := os.ReadFile(path) //nolint:gosec // G304: test reads from test-controlled data home
	if err != nil {
		t.Fatalf("read replaced repo metadata: %v", err)
	}
	var got Repo
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatalf("unmarshal replaced repo metadata: %v", err)
	}
	if got != repo {
		t.Fatalf("repo metadata = %+v, want %+v", got, repo)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat replaced repo metadata: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("repo metadata permissions = %o, want %o", got, want)
	}
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		t.Fatalf("read repo directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "repo.json" {
		t.Fatalf("repo directory entries = %v, want only repo.json", entries)
	}
}

func TestRegisterCurrentDiscoversGitRepo(t *testing.T) {
	root := newGitRepo(t)
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome, Now: fixedNow}
	withDir(t, root, func() {
		repo, err := registry.RegisterCurrent(context.Background())
		if err != nil {
			t.Fatalf("RegisterCurrent() failed: %v", err)
		}
		if repo.Root != root || repo.Name != filepath.Base(root) || repo.Branch == "" || repo.RemoteURL != "https://example.com/repo.git" {
			t.Fatalf("unexpected repo metadata: %+v", repo)
		}
		if _, err := os.Stat(filepath.Join(dataHome, "repos", repo.ID, "repo.json")); err != nil {
			t.Fatalf("repo metadata not written: %v", err)
		}
	})
}

func TestRegistryQueuePaths(t *testing.T) {
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome}
	repo := Repo{ID: "repo-123"}
	if got, want := registry.NotesDir(repo), filepath.Join(dataHome, "repos", repo.ID, "notes"); got != want {
		t.Fatalf("NotesDir() = %q, want %q", got, want)
	}
	if got, want := registry.QueuePath(repo), filepath.Join(dataHome, "repos", repo.ID, "queue.json"); got != want {
		t.Fatalf("QueuePath() = %q, want %q", got, want)
	}
	if got, want := registry.QueueLogPath(repo), filepath.Join(dataHome, "repos", repo.ID, "queue.jsonl"); got != want {
		t.Fatalf("QueueLogPath() = %q, want %q", got, want)
	}
}

func TestRegistryMergeBatchPathsStayOutsidePlans(t *testing.T) {
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome}
	repo := Repo{ID: "repo-123"}
	batchID := "batch-456"
	batchRoot := filepath.Join(dataHome, "repos", repo.ID, "merge-batches")

	paths := map[string]string{
		"batches": registry.MergeBatchesDir(repo),
		"batch":   registry.MergeBatchDir(repo, batchID),
		"state":   registry.MergeBatchStatePath(repo, batchID),
		"log":     registry.MergeBatchLogPath(repo, batchID),
		"active":  registry.ActiveMergeBatchPath(repo),
	}
	wants := map[string]string{
		"batches": batchRoot,
		"batch":   filepath.Join(batchRoot, batchID),
		"state":   filepath.Join(batchRoot, batchID, "state.json"),
		"log":     filepath.Join(batchRoot, batchID, "transitions.jsonl"),
		"active":  filepath.Join(batchRoot, "active.json"),
	}
	for name, got := range paths {
		if got != wants[name] {
			t.Errorf("%s merge batch path = %q, want %q", name, got, wants[name])
		}
		if strings.HasPrefix(got, registry.PlansDir(repo)+string(filepath.Separator)) {
			t.Errorf("%s merge batch path is inside plans directory: %q", name, got)
		}
	}
}

func TestAllocatePlanCreatesCentralPlanDir(t *testing.T) {
	dataHome := t.TempDir()
	now := time.Date(2026, 5, 31, 12, 0, 7, 0, time.FixedZone("UTC+2", 2*60*60))
	registry := Registry{DataHome: dataHome, Now: func() time.Time { return now }}
	repo := Repo{ID: "repo-123"}
	plan, err := registry.AllocatePlan(repo, "Example Plan")
	if err != nil {
		t.Fatalf("AllocatePlan() failed: %v", err)
	}
	if plan.ID != "20260531-100007-example-plan" {
		t.Fatalf("plan ID = %q", plan.ID)
	}
	if plan.Dir != filepath.Join(dataHome, "repos", repo.ID, "plans", plan.ID) {
		t.Fatalf("plan dir = %q", plan.Dir)
	}
	if info, err := os.Stat(plan.Dir); err != nil || !info.IsDir() {
		t.Fatalf("plan dir not created: info=%v err=%v", info, err)
	}
	second, err := registry.AllocatePlan(repo, "Example Plan")
	if err != nil {
		t.Fatalf("second AllocatePlan() failed: %v", err)
	}
	if second.ID != "20260531-100007-example-plan-2" {
		t.Fatalf("second plan ID = %q", second.ID)
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "remote", "add", "origin", "https://example.com/repo.git")
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: test invokes fixed git command with test-controlled args
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func withDir(t *testing.T, dir string, fn func()) {
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
