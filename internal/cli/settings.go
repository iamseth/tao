package cli

import (
	"context"
	"fmt"

	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
)

// settingsService never captures environment below the invocation boundary.
func (a App) settingsService() *settings.Service {
	if a.SettingsService != nil {
		return a.SettingsService
	}
	environment := a.envSnapshot()
	if a.settingsEnvironment != nil {
		environment = *a.settingsEnvironment
	}
	return settings.NewService(taodata.DataHome(), environment)
}

func (a App) initializeSettings(ctx context.Context) App {
	if a.settingsEnvironment == nil {
		environment := a.envSnapshot()
		a.settingsEnvironment = &environment
	}
	a.SettingsService = a.settingsService()
	return a.refreshSettings(ctx)
}

// refreshSettings is the explicit file-refresh boundary for Settings editors.
// The service and original environment stay frozen; repository copies are
// always composed from this global layer, never a prior effective snapshot.
func (a App) refreshSettings(ctx context.Context) App {
	view, err := a.settingsService().Read(ctx, settings.Target{Global: true})
	a.settingsGlobal = &view
	a.RuntimeEnv = &view.Effective
	if a.runtimeTheme {
		a.Theme = nil
		a = a.withRuntimeTheme()
	}
	if err != nil && a.Err != nil {
		_ = writeln(a.Err, fmt.Sprintf("warning: settings: %v", err))
	}
	return a
}

func (a App) settingsForRepository(ctx context.Context, repository taodata.Repo) (App, error) {
	if a.settingsGlobal == nil {
		// Direct same-package callers retain built-ins (or injected snapshots).
		a.SettingsService = a.settingsService()
		a = a.refreshSettings(ctx)
	}
	view, err := a.settingsService().ReadWithGlobal(ctx, settings.Target{RepositoryID: repository.ID}, *a.settingsGlobal)
	a.RuntimeEnv = &view.Effective
	if view.LoadError != nil {
		return a, view.LoadError
	}
	if ctx.Err() != nil {
		return a, ctx.Err()
	}
	// Field errors remain in the snapshot for operation-specific Require calls.
	if err != nil && a.Err != nil {
		_ = writeln(a.Err, fmt.Sprintf("warning: settings: %v", err))
	}
	return a, nil
}
