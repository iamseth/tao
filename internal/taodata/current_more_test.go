package taodata

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRegistryCurrentReadsRegisteredRepoForGitRoot(t *testing.T) {
	root := newGitRepo(t)
	registry := Registry{DataHome: t.TempDir(), Now: fixedNow}
	repo := Repo{Schema: RepoSchema, ID: RepoID(root), Name: filepath.Base(root), Root: root, Branch: "main", UpdatedAt: fixedNow().UTC().Format("2006-01-02T15:04:05Z")}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	withDir(t, root, func() {
		got, err := registry.Current(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got != repo {
			t.Fatalf("Current() = %#v, want %#v", got, repo)
		}
	})
}

func TestRegistryNowFallsBackWhenUnset(t *testing.T) {
	if (Registry{}).now().IsZero() {
		t.Fatal("now fallback returned zero time")
	}
}
