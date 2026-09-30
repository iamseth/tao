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

// envSnapshot never loads process state below the invocation boundary.
func (a App) envSnapshot() runtimeconfig.EnvSnapshot {
	if a.RuntimeEnv != nil {
		return *a.RuntimeEnv
	}
	return runtimeconfig.LoadEnv(nil)
}

func (a App) envDefaultsFor(keys ...string) (envDefaults, error) {
	snapshot := a.envSnapshot()
	if err := snapshot.Require(keys...); err != nil {
		return envDefaults{}, err
	}
	return envDefaults{EnvDefaults: snapshot.Defaults()}, nil
}

// flagDefaults is a display/registration projection, not execution admission.
// Valid fields remain visible even when unrelated fields have diagnostics.
func (a App) flagDefaults() runtimeconfig.EnvDefaults {
	return a.envSnapshot().Defaults()
}

// runEnvDefaults admits the shared run/note-run request, not independent
// routing, update, merge, or budget policy. Those have their own consumers.
func (a App) runEnvDefaults() (envDefaults, error) {
	return a.envDefaultsFor(
		runtimeconfig.EnvCommitPolicy, runtimeconfig.EnvExecutionMode,
		runtimeconfig.EnvAgent, runtimeconfig.EnvPullRequest, runtimeconfig.EnvReview,
		runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions,
		runtimeconfig.EnvModel, runtimeconfig.EnvRunModel, runtimeconfig.EnvReviewModel,
		runtimeconfig.EnvReworkEscalationModel, runtimeconfig.EnvReworkEscalationFromAttempt,
	)
}

// requireRunHandoffBudgets admits execution budgets before a handoff creates
// pending work. Ordinary run retains its operation-specific budget admission.
func (a App) requireRunHandoffBudgets() error {
	_, err := a.envSnapshot().Budget()
	return err
}

func (d envDefaults) runConfig(overrides runtimeconfig.RunOptionsPatch) (runtimeconfig.Config, error) {
	return runtimeconfig.NewConfigFromStages(d.RunOptionsPatch, overrides)
}

func (d envDefaults) resolveRunOptionsWithRepository(repository, overrides runtimeconfig.RunOptionsPatch) (runtimeconfig.ResolvedRunOptions, error) {
	return runtimeconfig.ResolveRunOptionsWithRepositoryDefaults(d.RunOptionsPatch, repository, overrides)
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
	if models, ok := repo.ModelDefaults(); ok {
		options.ModelSelection = models
	}
	return options
}
