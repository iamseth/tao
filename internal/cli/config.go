package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
)

var configCommand = commandMetadata{
	name: "config", minPrefix: "config",
	usageLines:            []string{"config [--global | --repo ID]", "config get [key] [--global | --repo ID]", "config set <key> <value> [--global | --repo ID]", "config unset <key> [--global | --repo ID]"},
	completionDescription: "Inspect and edit scoped settings",
	long:                  "Inspect stored and effective settings with their sources and diagnostics. Defaults to the current registered repository; --global works outside a repository. Environment values mask saved values. Unset restores inheritance; null disables only budget STOP caps. Malformed files must be repaired manually at the reported path. Replaces the deprecated repo config adapter.",
	examples:              "  tao config\n  tao config set models.model provider/model --global\n  tao config unset pull_request --repo tao",
	registerFlags:         registerConfigFlags,
	subcommands:           configSubcommands(),
	execute:               func(c commandContext) error { return c.app.config(c.ctx, c.args) },
}

func registerConfigFlags(fs *flag.FlagSet) {
	fs.Bool("global", false, "use global settings instead of repository settings")
	fs.String("repo", "", "select a registered repository by ID prefix or exact name")
}

func configSubcommands() []commandSubcommand {
	var keys []string
	for _, def := range runtimeconfig.SettingDefinitions() {
		if len(def.Scopes) > 0 {
			keys = append(keys, def.Key)
		}
	}
	var result []commandSubcommand
	for _, verb := range []string{"get", "set", "unset"} {
		result = append(result, commandSubcommand{name: verb, description: verb + " scoped settings", registerFlags: registerConfigFlags, completion: completionContext{positional: completionPositional{index: 1, label: "key", candidates: keys}}})
	}
	return result
}

// Configuration selection deliberately does not establish execution eligibility.
func configRepo(ctx context.Context, registry taodata.Registry, selector string) (taodata.Repo, error) {
	if selector == "" {
		current, err := registry.Current(ctx)
		if err != nil {
			return taodata.Repo{}, fmt.Errorf("resolve current repository (run tao init first): %w", err)
		}
		selector = current.ID
	}
	entry, err := taodata.ResolveCatalogRepo(ctx, registry, selector)
	return entry.Repo, err
}

func (a App) config(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("config", args, registerConfigFlags)
	if err != nil {
		return err
	}
	if flagWasProvided(fs, "global") && flagWasProvided(fs, "repo") {
		return errors.New("--global and --repo are mutually exclusive")
	}
	target := settings.Target{Global: flagBoolValue(fs, "global")}
	if !target.Global {
		selector := flagStringValue(fs, "repo")
		if flagWasProvided(fs, "repo") && selector == "" {
			return errors.New("--repo requires a non-empty repository selector")
		}
		repo, err := configRepo(ctx, taodata.NewRegistry(""), selector)
		if err != nil {
			return err
		}
		target.RepositoryID = repo.ID
	}
	verb, key := "get", ""
	if len(positional) > 0 {
		verb = positional[0]
		positional = positional[1:]
	}
	switch verb {
	case "get":
		if len(positional) > 1 {
			return errors.New("usage: tao config get [key]")
		}
	case "set":
		if len(positional) != 2 {
			return errors.New("usage: tao config set <key> <value>")
		}
	case "unset":
		if len(positional) != 1 {
			return errors.New("usage: tao config unset <key>")
		}
	default:
		return fmt.Errorf("unknown config subcommand %q; use get, set, or unset", verb)
	}
	if len(positional) > 0 {
		key = positional[0]
	}
	service := a.settingsService()
	if verb != "get" {
		var value *string
		if verb == "set" {
			value = &positional[1]
		}
		if err := service.Update(ctx, target, map[string]*string{key: value}); err != nil {
			return err
		}
	}
	view, readErr := service.Read(ctx, target)
	scope := "repo"
	if target.Global {
		scope = "global"
	}
	if key != "" && verb == "get" {
		known := false
		for _, def := range runtimeconfig.SettingDefinitions() {
			if def.Key == key && slices.Contains(def.Scopes, scope) {
				known = true
			}
		}
		if _, saved := view.Stored[key]; !known && !saved {
			return fmt.Errorf("unknown or disallowed %s setting %q; use tao config get to list settings", scope, key)
		}
	}
	for _, status := range view.Effective.SettingsStatus() {
		if key != "" && status.Key != key {
			continue
		}
		stored := "unset"
		if raw, ok := view.Stored[status.Key]; ok {
			stored = string(raw)
		}
		masked := ""
		if stored != "unset" && status.Source == "env" {
			masked = " (saved value masked by environment)"
		}
		if err := writef(a.Out, "%s: %s [source=%s; stored=%s]%s\n", status.Key, status.Value, status.Source, stored, masked); err != nil {
			return err
		}
	}
	for _, diagnostic := range view.Diagnostics {
		if err := writef(a.Out, "Diagnostic: %s\n", diagnostic); err != nil {
			return err
		}
	}
	return readErr
}
