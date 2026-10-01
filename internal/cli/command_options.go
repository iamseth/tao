package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// resolveCommandOptions composes only applicable settings from the invocation's
// captured environment and explicit registered flags. Rendering-only profiles
// do not require a registered checkout.
func (a App) resolveCommandOptions(ctx context.Context, fs *flag.FlagSet, profile runtimeconfig.CommandProfile) (runtimeconfig.CommandOptions, error) {
	var repository runtimeconfig.RunOptionsPatch
	var rework runtimeconfig.ReworkOptionsPatch
	switch profile {
	case runtimeconfig.CommandRun, runtimeconfig.CommandReview, runtimeconfig.CommandMerge:
		repo, err := a.registry().Current(ctx)
		if err != nil {
			return runtimeconfig.CommandOptions{}, err
		}
		repository = repositoryRunOptions(repo)
		rework = repositoryReworkOptions(repo)
	}
	return a.resolveCommandOptionsWithRepository(fs, profile, repository, rework)
}

// resolveCommandOptionsWithRepository reuses an already selected repository,
// including note --repo selections that differ from the current checkout.
func (a App) resolveCommandOptionsWithRepository(fs *flag.FlagSet, profile runtimeconfig.CommandProfile, repository runtimeconfig.RunOptionsPatch, rework ...runtimeconfig.ReworkOptionsPatch) (runtimeconfig.CommandOptions, error) {
	input := runtimeconfig.CommandOptionsInput{Env: a.envSnapshot(), Profile: profile, Repository: repository}
	if len(rework) > 0 {
		input.RepositoryRework = rework[0]
	}
	var err error
	input.Flags, err = runRequestFlagOverrides(fs)
	if err != nil {
		return runtimeconfig.CommandOptions{}, err
	}
	input.Auxiliary = runtimeconfig.CommandAuxiliaryFlags{
		AutoRework:                  commandBoolFlag(fs, "auto-rework"),
		MaxReworkAttempts:           commandIntFlag(fs, "max-rework-attempts"),
		ReworkEscalationFromAttempt: commandIntFlag(fs, "rework-escalation-from-attempt"),
		NoRunHeader:                 commandBoolFlag(fs, "no-run-header"),
		SkipPermissions:             commandBoolFlag(fs, "dangerously-skip-permissions"),
	}
	options, err := runtimeconfig.ResolveCommandOptions(input)
	if err == nil && a.Err != nil {
		for _, warning := range options.Warnings {
			_, _ = fmt.Fprintln(a.Err, "warning: "+warning)
		}
	}
	return options, err
}

func commandBoolFlag(fs *flag.FlagSet, name string) *bool {
	if fs.Lookup(name) == nil || !flagWasProvided(fs, name) {
		return nil
	}
	return new(flagBoolValue(fs, name))
}

func commandIntFlag(fs *flag.FlagSet, name string) *int {
	if fs.Lookup(name) == nil || !flagWasProvided(fs, name) {
		return nil
	}
	return new(flagIntValue(fs, name))
}

// executeCommandRun is the full-run handoff for callers without recovery flags.
// Recovery authority remains explicit on the ordinary run path.
func (a App) executeCommandRun(ctx context.Context, repo planRunRepository, input string, options runtimeconfig.CommandOptions) error {
	request := run.Request{Input: input, ResolvedRunOptions: options.RunOptions}
	return a.executeCommandRunRequest(ctx, repo, input, request, options, false)
}

func (a App) executeCommandRunRequest(ctx context.Context, repo planRunRepository, input string, request run.Request, options runtimeconfig.CommandOptions, reworkRestart bool) error {
	return a.executeResolvedRunWithSources(ctx, repo, input, request, options.SkipPermissions, options.AutoRework, options.ReworkEscalationFromAttempt, reworkRestart, options.NoRunHeader, options.Sources)
}
