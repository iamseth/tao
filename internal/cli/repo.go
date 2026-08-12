package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/taodata"
)

var repoCommand = commandMetadata{
	name:                  "repo",
	minPrefix:             "repo",
	usageLines:            []string{"repo list", "repo show <repo-id>", "repo config [--pull-request true|false] [<repo-id>]", "repo doctor"},
	completionDescription: "Inspect registered repositories",
	long:                  "Inspect and configure repositories registered in Tao's centralized catalog. Use repo commands to list known checkouts, show catalog details, set repository run defaults, and diagnose repository health before running plans.",
	examples: "  tao repo list\n" +
		"  tao repo show tao-146d10c48b68\n" +
		"  tao repo config --pull-request true\n" +
		"  tao repo doctor",
	subcommands: []commandSubcommand{
		{name: "list", description: "List registered repositories and health summaries"},
		{name: "show", description: "Show details for one registered repository"},
		{name: "config", description: "Show or set repository run defaults", registerFlags: registerRepoConfigFlags},
		{name: "doctor", description: "Check registered repositories for health problems"},
	},
	registerFlags: registerRepoConfigFlags,
	execute: func(c commandContext) error {
		return c.app.repo(c.ctx, c.args)
	},
}

func (a App) repo(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tao repo list|show <repo-id>|config [--pull-request true|false] [<repo-id>]|doctor")
	}
	registry := taodata.NewRegistry("")
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: tao repo list")
		}
		return a.repoList(ctx, registry)
	case "show":
		if len(args) != 2 {
			return errors.New("usage: tao repo show <repo-id>")
		}
		return a.repoShow(ctx, registry, args[1])
	case "config":
		return a.repoConfig(ctx, registry, args[1:])
	case "doctor":
		if len(args) != 1 {
			return errors.New("usage: tao repo doctor")
		}
		return a.repoDoctor(ctx, registry)
	default:
		return fmt.Errorf("unknown repo subcommand %q", args[0])
	}
}

func (a App) repoList(ctx context.Context, registry taodata.Registry) error {
	catalog, err := registry.Catalog(ctx, taodata.RepoHealthChecker{})
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return writeln(a.Out, "No repositories registered.")
	}
	if err := writeln(a.Out, "REPO ID  NAME  HEALTH  PLANS  ROOT"); err != nil {
		return err
	}
	for _, entry := range catalog {
		name := entry.Repo.Name
		if name == "" {
			name = "-"
		}
		root := entry.Repo.Root
		if root == "" {
			root = "-"
		}
		if err := writef(a.Out, "%s  %s  %s  %d  %s\n", entry.Repo.ID, name, entry.Health.Status, entry.PlanCount, root); err != nil {
			return err
		}
	}
	return nil
}

func (a App) repoShow(ctx context.Context, registry taodata.Registry, input string) error {
	entry, err := resolveRepo(ctx, registry, input)
	if err != nil {
		return err
	}
	lines := []string{
		"Repo: " + emptyDash(entry.Repo.Name),
		"ID: " + emptyDash(entry.Repo.ID),
		"Root: " + emptyDash(entry.Repo.Root),
		"Branch: " + emptyDash(entry.Repo.Branch),
		"Remote: " + emptyDash(entry.Repo.RemoteURL),
		fmt.Sprintf("Plans: %d", entry.PlanCount),
		"Health: " + entry.Health.Status,
		"Finding: " + entry.Health.Message,
	}
	return writeLines(a.Out, lines...)
}

func registerRepoConfigFlags(fs *flag.FlagSet) {
	fs.String("pull-request", "", "set the repository pull_request run default to true or false")
}

func (a App) repoConfig(ctx context.Context, registry taodata.Registry, args []string) error {
	fs, positional, err := a.parseArgs("repo config", args, registerRepoConfigFlags)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New("usage: tao repo config [--pull-request true|false] [<repo-id>]")
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	repo, err := taodata.ResolveRepo(ctx, registry, selector)
	if err != nil {
		return err
	}
	if flagWasProvided(fs, "pull-request") {
		value, err := strconv.ParseBool(flagStringValue(fs, "pull-request"))
		if err != nil {
			return errors.New("--pull-request must be true or false")
		}
		if repo.RunDefaults == nil {
			repo.RunDefaults = &taodata.RepoRunDefaults{}
		}
		repo.RunDefaults.PullRequest = &value
		repo.UpdatedAt = a.now().UTC().Format("2006-01-02T15:04:05Z07:00")
		if err := registry.WriteRepo(repo); err != nil {
			return err
		}
	}
	pullRequest := "unset"
	if value, ok := repo.PullRequestDefault(); ok {
		pullRequest = strconv.FormatBool(value)
	}
	return writeLines(a.Out,
		"Repo: "+emptyDash(repo.Name),
		"ID: "+emptyDash(repo.ID),
		"pull_request: "+pullRequest,
	)
}

func (a App) repoDoctor(ctx context.Context, registry taodata.Registry) error {
	catalog, err := registry.Catalog(ctx, taodata.RepoHealthChecker{})
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return writeln(a.Out, "No repositories registered.")
	}
	hasErrors := false
	for _, entry := range catalog {
		if entry.Health.Error {
			hasErrors = true
		}
		if err := writef(a.Out, "%s [%s]: %s\n", emptyDash(entry.Repo.ID), entry.Health.Status, entry.Health.Message); err != nil {
			return err
		}
	}
	if hasErrors {
		return errors.New("repo doctor found unhealthy repositories")
	}
	return nil
}

func resolveRepo(ctx context.Context, registry taodata.Registry, input string) (taodata.RepoCatalogEntry, error) {
	catalog, err := registry.Catalog(ctx, taodata.RepoHealthChecker{})
	if err != nil {
		return taodata.RepoCatalogEntry{}, err
	}
	matches := make([]taodata.RepoCatalogEntry, 0, 1)
	nameMatches := make([]taodata.RepoCatalogEntry, 0, 1)
	for _, entry := range catalog {
		if entry.Repo.ID == input || strings.HasPrefix(entry.Repo.ID, input) {
			matches = append(matches, entry)
		}
		if entry.Repo.Name == input {
			nameMatches = append(nameMatches, entry)
		}
	}
	if len(matches) == 0 {
		matches = nameMatches
	}
	switch len(matches) {
	case 0:
		return taodata.RepoCatalogEntry{}, fmt.Errorf("repo %q not found", input)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.Repo.ID)
		}
		return taodata.RepoCatalogEntry{}, fmt.Errorf("repo %q is ambiguous: %s", input, strings.Join(ids, ", "))
	}
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
