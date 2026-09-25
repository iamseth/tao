package taodata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRepoResolver struct {
	current    Repo
	currentErr error
	stored     map[string]Repo
	repos      []Repo
}

func (f fakeRepoResolver) Current(context.Context) (Repo, error) { return f.current, f.currentErr }
func (f fakeRepoResolver) ReadRepo(id string) (Repo, error) {
	repo, ok := f.stored[id]
	if !ok {
		return Repo{}, os.ErrNotExist
	}
	return repo, nil
}
func (f fakeRepoResolver) ListRepos() ([]Repo, error) { return f.repos, nil }

func TestResolveCatalogRepoSelectorPolicy(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	for _, repo := range []Repo{
		{ID: "alpha-123", Name: "shared"},
		{ID: "alpha-456", Name: "exact-name"},
		{ID: "gamma-789", Name: "alpha-1"},
		{ID: "delta-123", Name: "shared"},
		{ID: "epsilon-123", Name: "alpha"},
		{ID: "zeta-123", Name: "alpha-123"},
	} {
		repo.Schema = RepoSchema
		repo.Root = filepath.Join(registry.DataHome, "missing-root")
		if err := registry.WriteRepo(repo); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"bad-json", "bad-other"} {
		dir := filepath.Join(registry.DataHome, "repos", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "repo.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		name     string
		selector string
		wantID   string
		wantErr  string
	}{
		{name: "exact ID beats name", selector: "alpha-123", wantID: "alpha-123"},
		{name: "unique prefix", selector: "gamma", wantID: "gamma-789"},
		{name: "exact name", selector: "exact-name", wantID: "alpha-456"},
		{name: "prefix beats name", selector: "alpha-1", wantID: "alpha-123"},
		{name: "ambiguous prefix beats exact name", selector: "alpha", wantErr: `repository "alpha" is ambiguous; use one of these IDs: alpha-123, alpha-456`},
		{name: "ambiguous name", selector: "shared", wantErr: `repository "shared" is ambiguous; use one of these IDs: alpha-123, delta-123`},
		{name: "unknown", selector: "unknown", wantErr: `repository "unknown" is not registered; run tao init in that checkout`},
		{name: "name prefix is not a match", selector: "exact", wantErr: `repository "exact" is not registered; run tao init in that checkout`},
		{name: "malformed exact ID", selector: "bad-json", wantID: "bad-json"},
		{name: "malformed prefix", selector: "bad-j", wantID: "bad-json"},
		{name: "malformed ambiguity", selector: "bad", wantErr: `repository "bad" is ambiguous; use one of these IDs: bad-json, bad-other`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := ResolveCatalogRepo(context.Background(), registry, tt.selector)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("ResolveCatalogRepo(%q) error = %v, want %q", tt.selector, err, tt.wantErr)
				}
				return
			}
			if err != nil || entry.Repo.ID != tt.wantID {
				t.Fatalf("ResolveCatalogRepo(%q) = %#v, %v", tt.selector, entry, err)
			}
			if !entry.Health.Error {
				t.Fatalf("inspection cleared unhealthy status: %#v", entry)
			}
		})
	}

	// Ordinary note/config selection must still exclude unreadable metadata.
	if _, err := ResolveRepo(context.Background(), registry, "bad-json"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("ordinary resolution of malformed metadata = %v", err)
	}
}

func TestResolveRepoEmptySelectorUsesCurrent(t *testing.T) {
	registered := Repo{ID: "alpha-123", Name: "alpha"}
	resolver := fakeRepoResolver{current: Repo{ID: "alpha-123"}, stored: map[string]Repo{"alpha-123": registered}}
	repo, err := ResolveRepo(context.Background(), resolver, "")
	if err != nil || repo.ID != "alpha-123" {
		t.Fatalf("ResolveRepo = %v, %v", repo, err)
	}

	unregistered := fakeRepoResolver{current: Repo{ID: "missing"}, stored: map[string]Repo{}}
	if _, err := ResolveRepo(context.Background(), unregistered, ""); err == nil || !strings.Contains(err.Error(), "not registered; run tao init first") {
		t.Fatalf("unregistered current error = %v", err)
	}

	failing := fakeRepoResolver{currentErr: errors.New("no checkout")}
	if _, err := ResolveRepo(context.Background(), failing, ""); err == nil || !strings.Contains(err.Error(), "resolve current repository") {
		t.Fatalf("current failure error = %v", err)
	}
}

func TestResolveRepoSelectorMatchesPrefixThenName(t *testing.T) {
	repos := []Repo{
		{ID: "alpha-123", Name: "alpha"},
		{ID: "alpha-456", Name: "beta"},
		{ID: "gamma-789", Name: "alpha-456"},
	}
	resolver := fakeRepoResolver{repos: repos}

	repo, err := ResolveRepo(context.Background(), resolver, "gamma")
	if err != nil || repo.ID != "gamma-789" {
		t.Fatalf("unique prefix = %v, %v", repo, err)
	}

	if _, err := ResolveRepo(context.Background(), resolver, "alpha-"); err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "alpha-123, alpha-456") {
		t.Fatalf("ambiguous prefix error = %v", err)
	}

	// An exact name matches only after no ID prefix matched; alpha-456 the ID
	// prefix wins over alpha-456 the name.
	repo, err = ResolveRepo(context.Background(), resolver, "alpha-456")
	if err != nil || repo.Name != "beta" {
		t.Fatalf("id-over-name = %v, %v", repo, err)
	}

	repo, err = ResolveRepo(context.Background(), resolver, "beta")
	if err != nil || repo.ID != "alpha-456" {
		t.Fatalf("exact name = %v, %v", repo, err)
	}

	if _, err := ResolveRepo(context.Background(), resolver, "unknown"); err == nil || !strings.Contains(err.Error(), "not registered; run tao init in that checkout") {
		t.Fatalf("unknown selector error = %v", err)
	}
}
