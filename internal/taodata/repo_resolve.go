package taodata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// RepoResolver is the registry surface repository-selector resolution needs.
// It is satisfied by Registry and by injectable registry fakes.
type RepoResolver interface {
	Current(context.Context) (Repo, error)
	ReadRepo(string) (Repo, error)
	ListRepos() ([]Repo, error)
}

// ResolveRepo resolves a registered repository from a user-supplied selector.
// An empty selector resolves the current checkout's registered repository.
// Otherwise a unique repository ID prefix wins, then an exact repository name;
// ambiguous selectors are errors naming the candidate IDs.
func ResolveRepo(ctx context.Context, registry RepoResolver, selector string) (Repo, error) {
	if selector == "" {
		current, err := registry.Current(ctx)
		if err != nil {
			return Repo{}, fmt.Errorf("resolve current repository (run tao init first): %w", err)
		}
		registered, err := registry.ReadRepo(current.ID)
		if err != nil {
			if os.IsNotExist(err) {
				return Repo{}, errors.New("current repository is not registered; run tao init first")
			}
			return Repo{}, fmt.Errorf("read registered repository: %w", err)
		}
		return registered, nil
	}
	repos, err := registry.ListRepos()
	if err != nil {
		return Repo{}, err
	}
	var idMatches, nameMatches []Repo
	for _, repo := range repos {
		if strings.HasPrefix(repo.ID, selector) {
			idMatches = append(idMatches, repo)
		}
		if repo.Name == selector {
			nameMatches = append(nameMatches, repo)
		}
	}
	if len(idMatches) == 1 {
		return idMatches[0], nil
	}
	if len(idMatches) > 1 {
		return Repo{}, ambiguousRepoError(selector, idMatches)
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], nil
	}
	if len(nameMatches) > 1 {
		return Repo{}, ambiguousRepoError(selector, nameMatches)
	}
	return Repo{}, fmt.Errorf("repository %q is not registered; run tao init in that checkout", selector)
}

func ambiguousRepoError(selector string, repos []Repo) error {
	ids := make([]string, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.ID)
	}
	return fmt.Errorf("repository %q is ambiguous; use one of these IDs: %s", selector, strings.Join(ids, ", "))
}
