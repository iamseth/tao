package taodata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryCatalogIncludesPlanCountsAndMetadataErrors(t *testing.T) {
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome, Now: fixedNow}
	root := newGitRepo(t)
	repo := Repo{Schema: RepoSchema, ID: "repo-a", Name: "repo", Root: root, UpdatedAt: fixedNow().UTC().Format("2006-01-02T15:04:05Z")}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataHome, "repos", repo.ID, "plans", "plan-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataHome, "repos", "bad-json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataHome, "repos", "bad-json", "repo.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog, err := registry.Catalog(context.Background(), RepoHealthChecker{})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("Catalog() len = %d, want 2: %#v", len(catalog), catalog)
	}
	byID := map[string]RepoCatalogEntry{}
	for _, entry := range catalog {
		byID[entry.Repo.ID] = entry
	}
	if got := byID[repo.ID]; got.PlanCount != 1 || got.Health.Status != RepoHealthOK || got.MetadataError != nil {
		t.Fatalf("unexpected good repo entry: %#v", got)
	}
	if got := byID["bad-json"]; got.Health.Status != RepoHealthMetadataError || !got.Health.Error || got.MetadataError == nil {
		t.Fatalf("unexpected bad repo entry: %#v", got)
	}
}
