package cli

import (
	"context"

	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

type envDefaults struct {
	runtimeconfig.EnvDefaults
}

func cliEnvDefaults() (envDefaults, error) {
	defaults, err := runtimeconfig.RuntimeEnvDefaults()
	return envDefaults{EnvDefaults: defaults}, err
}

// runtimeFlagDefaults resolves environment-aware defaults for flag
// registration so command help reflects the same defaults execution applies
// under TAO_* overrides. Invalid env values fall back to the built-in defaults;
// the command path re-resolves them and surfaces the error before running.
func runtimeFlagDefaults() runtimeconfig.EnvDefaults {
	defaults, err := runtimeconfig.RuntimeEnvDefaults()
	if err != nil {
		return runtimeconfig.EnvDefaults{RunOptionsPatch: runtimeconfig.DefaultRunOptionsPatch()}
	}
	return defaults
}

func (d envDefaults) runConfig(overrides runtimeconfig.RunOptionsPatch) (runtimeconfig.Config, error) {
	return runtimeconfig.NewConfigFromStages(d.RunOptionsPatch, overrides)
}

func (d envDefaults) resolveRunOptionsWithRepository(repository, overrides runtimeconfig.RunOptionsPatch) (runtimeconfig.ResolvedRunOptions, error) {
	return runtimeconfig.ResolveRunOptionsWithRepositoryDefaults(d.RunOptionsPatch, repository, overrides)
}

func (d envDefaults) newRunRequest(input string, overrides runtimeconfig.RunOptionsPatch) (run.Request, error) {
	config, err := d.runConfig(overrides)
	if err != nil {
		return run.Request{}, err
	}
	return run.Request{Input: input, ResolvedRunOptions: config.ResolvedOptions()}, nil
}

func (d envDefaults) newRunRequestWithRepository(input string, repository, overrides runtimeconfig.RunOptionsPatch) (run.Request, error) {
	resolved, err := d.resolveRunOptionsWithRepository(repository, overrides)
	if err != nil {
		return run.Request{}, err
	}
	return run.Request{Input: input, ResolvedRunOptions: resolved}, nil
}

func (a App) currentRepositoryRunOptions(ctx context.Context) (runtimeconfig.RunOptionsPatch, error) {
	repo, err := a.registry().Current(ctx)
	if err != nil {
		return runtimeconfig.RunOptionsPatch{}, err
	}
	return repositoryRunOptions(repo), nil
}

func repositoryRunOptions(repo taodata.Repo) runtimeconfig.RunOptionsPatch {
	var options runtimeconfig.RunOptionsPatch
	if pullRequest, ok := repo.PullRequestDefault(); ok {
		options = options.WithPullRequest(pullRequest)
	}
	return options
}
