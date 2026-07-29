package taodata

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryMetadataInventoryRetainsWarningsWithoutGitChecks(t *testing.T) {
	dataHome := t.TempDir()
	gitCalled := false
	registry := Registry{
		DataHome: dataHome,
		Now:      fixedNow,
		Runner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			gitCalled = true
			return errors.New("unexpected Git health check")
		},
	}
	repo := Repo{Schema: RepoSchema, ID: "repo-a", Name: "repo", Root: "/does/not/need/to/exist", UpdatedAt: fixedNow().UTC().Format("2006-01-02T15:04:05Z")}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(registry.PlansDir(repo), "plan-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataHome, "repos", "bad-json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataHome, "repos", "bad-json", "repo.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	inventory, err := registry.MetadataInventory()
	if err != nil {
		t.Fatal(err)
	}
	if gitCalled {
		t.Fatal("MetadataInventory() ran a Git health check")
	}
	if len(inventory) != 2 {
		t.Fatalf("MetadataInventory() len = %d, want 2: %#v", len(inventory), inventory)
	}
	byID := map[string]RepoInventoryEntry{}
	for _, entry := range inventory {
		byID[entry.Repo.ID] = entry
	}
	if got := byID[repo.ID]; got.PlanCount != 1 || got.MetadataError != nil || got.Repo != repo {
		t.Fatalf("unexpected good inventory entry: %#v", got)
	}
	if got := byID["bad-json"]; got.MetadataError == nil || got.Repo.ID != "bad-json" {
		t.Fatalf("unexpected warning inventory entry: %#v", got)
	}
}

func TestRegistryMetadataInventoryClassifiesInvalidMetadataWithoutFollowingItsID(t *testing.T) {
	dataHome := t.TempDir()
	registry := Registry{DataHome: dataHome, Now: fixedNow}
	catalogID := "catalog-entry"
	dir := filepath.Join(dataHome, "repos", catalogID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"schema":"tao.repo.v1","id":"other-entry","name":"repo","root":"/repo","updated_at":"2026-07-29T04:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	inventory, err := registry.MetadataInventory()
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 1 {
		t.Fatalf("MetadataInventory() len = %d, want 1", len(inventory))
	}
	entry := inventory[0]
	if entry.MetadataError == nil || entry.Repo.ID != catalogID {
		t.Fatalf("invalid metadata entry = %#v, want warning under catalog id", entry)
	}
	if entry.PlansDir != filepath.Join(dir, "plans") || entry.RuntimeStatusDir != filepath.Join(dir, "run-status") {
		t.Fatalf("invalid metadata paths escaped catalog entry: %#v", entry)
	}
}

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
