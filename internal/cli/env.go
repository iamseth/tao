package cli

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/gitops"

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
	if a.settingsGlobal != nil && a.settingsGlobal.LoadError != nil {
		return envDefaults{}, a.settingsGlobal.LoadError
	}
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
		"max_slices", runtimeconfig.EnvCommitPolicy, runtimeconfig.EnvExecutionMode,
		runtimeconfig.EnvAgent, runtimeconfig.EnvReviewAgent, runtimeconfig.EnvPullRequest, runtimeconfig.EnvReview,
		runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions,
		runtimeconfig.EnvModel, runtimeconfig.EnvRunModel, runtimeconfig.EnvReviewModel,
		runtimeconfig.EnvEffort, runtimeconfig.EnvRunEffort, runtimeconfig.EnvReviewEffort,
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

// resolveRunOptionsWithRepository is a legacy low-level stage adapter. Migrated
// execution callers pass an empty repository patch after snapshot composition;
// status/UI projections retain the adapter until their settings migration.
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

// currentRepositoryRunOptions and repositoryRunOptions preserve legacy projections,
// not execution precedence. Compose execution settings with settingsForRepository.
func (a App) currentRepositoryRunOptions(ctx context.Context) (runtimeconfig.RunOptionsPatch, error) {
	repo, err := a.registry().Current(ctx)
	if err != nil {
		return runtimeconfig.RunOptionsPatch{}, err
	}
	return repositoryRunOptions(repo), nil
}

// settingsForPlanRoot never consults the launch checkout. Unregistered and
// unhealthy recorded roots retain the baseline; execution owns health refusal.
func (a App) settingsForPlanRoot(ctx context.Context, root string) (App, error) {
	repos, err := a.registry().ListRepos()
	if err != nil {
		return a, err
	}
	canonical := func(root string) string {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			return resolved
		}
		return filepath.Clean(root)
	}
	match := func(root string) (taodata.Repo, bool) {
		for _, repo := range repos {
			if strings.TrimSpace(repo.Root) != "" && canonical(repo.Root) == canonical(root) {
				return repo, true
			}
		}
		return taodata.Repo{}, false
	}
	if strings.TrimSpace(root) == "" {
		return a, nil
	}
	candidates := []string{root}
	if repo, ok := match(root); ok {
		return a.settingsForRepository(ctx, repo)
	}
	// A linked worktree is registered through its control checkout. Resolve it
	// whenever any registration exists, readable or not: the catalog listing
	// omits unreadable documents, which must not hide a corrupt registration.
	service := a.settingsService()
	if len(repos) != 0 || service.HasRegistrations() {
		main, err := gitops.NewClient(root, a.CommandRunner).MainWorktreeRoot(ctx)
		if err == nil {
			if repo, ok := match(strings.TrimSpace(main)); ok {
				return a.settingsForRepository(ctx, repo)
			}
			candidates = append(candidates, strings.TrimSpace(main))
		}
	}
	// A root that is registered but unreadable must still fail closed instead
	// of looking unregistered and admitting execution on baseline defaults.
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if id := taodata.RepoID(canonical(candidate)); service.RepositoryRegistered(id) {
			return a.settingsForRepository(ctx, taodata.Repo{ID: id})
		}
	}
	return a, ctx.Err()
}

func repositoryRunOptions(repo taodata.Repo) runtimeconfig.RunOptionsPatch {
	var options runtimeconfig.RunOptionsPatch
	if value, ok := repo.ReviewAgentDefault(); ok {
		options.ReviewAgent = runtimeconfig.AgentKind(value)
	}
	if pullRequest, ok := repo.PullRequestDefault(); ok {
		options = options.WithPullRequest(pullRequest)
	}
	if models, ok := repo.ModelDefaults(); ok {
		options.ModelSelection = models
	}
	return options
}
