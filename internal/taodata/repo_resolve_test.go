package taodata

import (
	"context"
	"errors"
	"os"
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
