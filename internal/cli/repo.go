package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
)

const repoConfigUsage = "repo config [--review-agent pi|claude|unset] [--pull-request true|false|unset] [--max-rework-attempts N|unset] [--rework-escalation-from-attempt N|unset] [--model NAME|unset] [--run-model NAME|unset] [--review-model NAME|unset] [--merge-review-model NAME|unset] [--resolver-model NAME|unset] [--rework-escalation-model NAME|unset] [<repo-id>]"

var repoCommand = commandMetadata{
	name:                  "repo",
	minPrefix:             "repo",
	usageLines:            []string{"repo list", "repo show <repo-id>", repoConfigUsage, "repo doctor"},
	completionDescription: "Inspect registered repositories",
	long:                  "Inspect and configure repositories registered in Tao's centralized catalog. Use repo commands to list known checkouts, show catalog details, set repository run defaults, and diagnose repository health before running plans.",
	examples: "  tao repo list\n" +
		"  tao repo show tao-146d10c48b68\n" +
		"  tao repo config --pull-request true\n" +
		"  tao repo config --model provider/model --review-model provider/reviewer\n" +
		"  tao repo config --run-model unset\n" +
		"  tao repo doctor",
	subcommands: []commandSubcommand{
		{name: "list", description: "List registered repositories and health summaries"},
		{name: "show", description: "Show details for one registered repository"},
		{name: "config", description: "Deprecated compatibility adapter; use tao config", registerFlags: registerRepoConfigFlags,
			completion: completionContext{flagValues: map[string]completionFlagValue{"review-agent": {kind: completionValueEnum, label: "agent", values: []string{"pi", "claude", "unset"}}}}},
		{name: "doctor", description: "Check registered repositories for health problems"},
	},
	registerFlags: registerRepoConfigFlags,
	execute: func(c commandContext) error {
		return c.app.repo(c.ctx, c.args)
	},
}

func (a App) repo(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tao repo list|show <repo-id>|doctor; tao " + repoConfigUsage)
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
	entry, err := taodata.ResolveCatalogRepo(ctx, registry, input)
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
	if err := writeLines(a.Out, lines...); err != nil {
		return err
	}
	return a.repoSettingsDiagnostics(ctx, registry, entry.Repo.ID)
}

func (a App) repoSettingsDiagnostics(ctx context.Context, registry taodata.Registry, id string) error {
	service := a.SettingsService
	if service == nil {
		service = settings.NewService(registry.DataHome, a.envSnapshot())
	}
	view, readErr := service.Read(ctx, settings.Target{RepositoryID: id})
	for _, diagnostic := range view.Diagnostics {
		if err := writef(a.Out, "Settings diagnostic: %s\n", diagnostic); err != nil {
			return err
		}
	}
	return readErr
}

func registerRepoConfigFlags(fs *flag.FlagSet) {
	fs.String("max-rework-attempts", "", "set automatic rework attempts (non-negative integer, zero disables) or unset")
	fs.String("rework-escalation-from-attempt", "", "set the first escalation-eligible attempt (integer at least one) or unset")
	fs.String("review-agent", "", "set the repository review_agent default to pi, claude, or unset")
	fs.String("pull-request", "", "set the repository pull_request run default to true, false, or unset")
	for _, name := range []string{"model", "run-model", "review-model", "merge-review-model", "resolver-model", "rework-escalation-model"} {
		fs.String(name, "", "set the repository "+strings.ReplaceAll(name, "-", "_")+" default to a model name or unset")
	}
}

func (a App) repoConfig(ctx context.Context, registry taodata.Registry, args []string) error {
	fs, positional, err := a.parseArgs("repo config", args, registerRepoConfigFlags)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New("usage: tao " + repoConfigUsage)
	}
	selector := ""
	if len(positional) == 1 {
		selector = positional[0]
	}
	repo, err := configRepo(ctx, registry, selector)
	if err != nil {
		return err
	}
	changes := map[string]*string{}
	if flagWasProvided(fs, "review-agent") {
		raw := flagStringValue(fs, "review-agent")
		var value *string
		if raw != "unset" {
			if raw == "" {
				return errors.New("--review-agent must be pi, claude, or unset")
			}
			parsed, err := runtimeconfig.ParseAgentKind(raw)
			if err != nil {
				return fmt.Errorf("--review-agent: %w (use unset to inherit)", err)
			}
			text := parsed.String()
			value = &text
		}
		changes["review_agent"] = value
	}
	if flagWasProvided(fs, "pull-request") {
		var value *bool
		if raw := strings.TrimSpace(flagStringValue(fs, "pull-request")); !strings.EqualFold(raw, "unset") {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return errors.New("--pull-request must be true, false, or unset")
			}
			value = &parsed
		}
		var text *string
		if value != nil {
			raw := strconv.FormatBool(*value)
			text = &raw
		}
		changes["pull_request"] = text
	}
	numericFlags := []struct {
		name    string
		minimum int
	}{
		{"max-rework-attempts", 0},
		{"rework-escalation-from-attempt", 1},
	}
	for _, setting := range numericFlags {
		if !flagWasProvided(fs, setting.name) {
			continue
		}
		raw := strings.TrimSpace(flagStringValue(fs, setting.name))
		var value *string
		if !strings.EqualFold(raw, "unset") {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < setting.minimum {
				return fmt.Errorf("--%s must be an integer at least %d, or unset", setting.name, setting.minimum)
			}
			text := strconv.Itoa(parsed)
			value = &text
		}
		changes[strings.ReplaceAll(setting.name, "-", "_")] = value
	}
	models := struct{ Base, Run, Review, MergeReview, Resolver, ReworkEscalation string }{}
	modelFlags := []struct {
		name  string
		value *string
	}{
		{"model", &models.Base},
		{"run-model", &models.Run},
		{"review-model", &models.Review},
		{"merge-review-model", &models.MergeReview},
		{"resolver-model", &models.Resolver},
		{"rework-escalation-model", &models.ReworkEscalation},
	}
	for _, model := range modelFlags {
		if !flagWasProvided(fs, model.name) {
			continue
		}
		raw := flagStringValue(fs, model.name)
		var value *string
		if raw != "unset" {
			parsed, err := runtimeconfig.ParseModelName(raw)
			if err != nil {
				return fmt.Errorf("--%s: %w (use unset to inherit)", model.name, err)
			}
			value = &parsed
		}
		changes["models."+strings.ReplaceAll(model.name, "-", "_")] = value
	}
	service := a.SettingsService
	if service == nil {
		service = settings.NewService(registry.DataHome, a.envSnapshot())
	}
	target := settings.Target{RepositoryID: repo.ID}
	if len(changes) > 0 {
		if err := service.Update(ctx, target, changes); err != nil {
			return err
		}
	}
	view, readErr := service.Read(ctx, target)
	pullRequest := "unset"
	if raw, ok := view.Stored["pull_request"]; ok {
		pullRequest = string(raw)
	}
	for _, model := range modelFlags {
		if raw, ok := view.Stored["models."+strings.ReplaceAll(model.name, "-", "_")]; ok {
			if err := json.Unmarshal(raw, model.value); err != nil {
				*model.value = string(raw)
			}
		}
	}
	lines := []string{
		"Repo: " + emptyDash(repo.Name),
		"ID: " + emptyDash(repo.ID),
		"pull_request: " + pullRequest,
	}
	for _, key := range []string{"review_agent", "max_rework_attempts", "rework_escalation_from_attempt"} {
		value := "unset"
		if raw, ok := view.Stored[key]; ok {
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				text = string(raw)
			}
			value = text
		}
		lines = append(lines, key+": "+value)
	}
	for _, model := range modelFlags {
		value := *model.value
		if value == "" {
			value = "unset"
		}
		lines = append(lines, strings.ReplaceAll(model.name, "-", "_")+": "+value)
	}
	if err := writeLines(a.Out, lines...); err != nil {
		return err
	}
	for _, diagnostic := range view.Diagnostics {
		if err := writef(a.Out, "Diagnostic: %s\n", diagnostic); err != nil {
			return err
		}
	}
	return readErr
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
		if err := a.repoSettingsDiagnostics(ctx, registry, entry.Repo.ID); err != nil {
			hasErrors = true
		}
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

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
