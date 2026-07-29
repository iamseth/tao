package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const defaultStartupInstallTimeout = 30 * time.Second

// ReleaseSource supplies validated release metadata.
type ReleaseSource interface {
	Latest(context.Context) (Release, error)
}

// ReleaseInstaller installs one validated release.
type ReleaseInstaller interface {
	Install(context.Context, Release) (InstallResult, error)
}

// Service shares discovery, comparison, caching, and installation policy
// between explicit updates and best-effort startup checks.
type Service struct {
	Discoverer            ReleaseSource
	Installer             ReleaseInstaller
	Cache                 *Cache
	CurrentVersion        func() string
	StartupInstallTimeout time.Duration
}

// UpdateResult describes the relationship to the latest release and any
// installation performed by this invocation.
type UpdateResult struct {
	CurrentVersion   string
	LatestVersion    string
	Comparison       VersionComparison
	Path             string
	Installed        bool
	ConcurrentUpdate bool
}

// StartupOutcome contains only non-fatal startup update information. Failure is
// diagnostic; callers must not turn it into failure of the requested command.
type StartupOutcome struct {
	UpdateResult
	Checked                 bool
	Cached                  bool
	Notice                  string
	Failure                 error
	AutomaticInstallFailure error
}

// Update performs an uncached release discovery and, when a newer stable
// release exists, installs it. Unlike Startup, every failure is returned.
func (service *Service) Update(ctx context.Context) (UpdateResult, error) {
	if err := service.validate(false); err != nil {
		return UpdateResult{}, err
	}
	current, err := service.validatedCurrentVersion()
	if err != nil {
		return UpdateResult{}, err
	}
	release, err := service.Discoverer.Latest(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	result, err := service.compare(current, release)
	if err != nil {
		return UpdateResult{}, err
	}
	if result.Comparison != VersionUpdateAvailable {
		return result, nil
	}
	installed, err := service.Installer.Install(ctx, release)
	result.Path = installed.Path
	result.Installed = installed.Installed
	result.ConcurrentUpdate = installed.ConcurrentUpdate
	if err != nil {
		return result, err
	}
	return result, nil
}

// Startup performs a cache-aware warn or auto check. It never returns an error;
// discovery, persistence, and installation failures are carried in Failure so
// startup integration cannot fail the user's requested command.
func (service *Service) Startup(ctx context.Context, mode Mode) StartupOutcome {
	var outcome StartupOutcome
	if mode == ModeOff {
		return outcome
	}
	if mode != ModeWarn && mode != ModeAuto {
		outcome.Failure = fmt.Errorf("invalid startup update mode %q", mode)
		return outcome
	}
	if err := service.validate(true); err != nil {
		outcome.Failure = err
		return outcome
	}

	lockContext, cancelLock := service.startupInstallContext(ctx)
	unlock, err := service.Cache.lock(lockContext)
	cancelLock()
	if err != nil {
		outcome.Failure = fmt.Errorf("acquire startup self-update policy lock: %w", err)
		return outcome
	}
	defer func() { _ = unlock() }()

	state, err := service.Cache.Load()
	if err != nil {
		outcome.Failure = err
		state = CacheState{}
	}
	var release Release
	if service.Cache.ShouldCheck(state) {
		outcome.Checked = true
		release, err = service.Discoverer.Latest(ctx)
		if err != nil {
			service.Cache.RecordFailedCheck(&state)
			outcome.Failure = errors.Join(outcome.Failure, err, service.bestEffortSave(state))
			return outcome
		}
		if err := service.Cache.RecordSuccessfulCheck(&state, release); err != nil {
			outcome.Failure = errors.Join(outcome.Failure, err)
			return outcome
		}
		outcome.Failure = errors.Join(outcome.Failure, service.bestEffortSave(state))
	} else {
		var fresh bool
		release, fresh = service.Cache.FreshRelease(state)
		if !fresh {
			return outcome
		}
		outcome.Cached = true
	}

	current, err := service.validatedCurrentVersion()
	if err != nil {
		outcome.Failure = errors.Join(outcome.Failure, err)
		return outcome
	}
	result, err := service.compare(current, release)
	if err != nil {
		outcome.Failure = errors.Join(outcome.Failure, err)
		return outcome
	}
	outcome.UpdateResult = result
	if result.Comparison != VersionUpdateAvailable {
		return outcome
	}

	if mode == ModeWarn {
		if service.Cache.RecordNotice(&state) {
			outcome.Notice = fmt.Sprintf("Tao %s is available (running %s); run 'tao update'", release.Tag, result.CurrentVersion)
			outcome.Failure = errors.Join(outcome.Failure, service.bestEffortSave(state))
		}
		return outcome
	}
	if !service.Cache.ShouldRetryAutomaticInstall(state) {
		return outcome
	}

	installContext, cancelInstall := service.startupInstallContext(ctx)
	defer cancelInstall()
	installed, installErr := service.Installer.Install(installContext, release)
	outcome.Path = installed.Path
	outcome.Installed = installed.Installed
	outcome.ConcurrentUpdate = installed.ConcurrentUpdate
	if installErr != nil {
		service.Cache.RecordAutomaticInstallFailure(&state)
		outcome.AutomaticInstallFailure = installErr
		outcome.Failure = errors.Join(outcome.Failure, installErr, service.bestEffortSave(state))
		if IsHomebrewError(installErr) {
			outcome.Notice = installErr.Error()
		}
		return outcome
	}

	service.Cache.RecordAutomaticInstallSuccess(&state)
	if installed.Installed {
		outcome.Notice = fmt.Sprintf("Tao %s was installed and will take effect on the next invocation", release.Tag)
	} else if installed.ConcurrentUpdate {
		outcome.Notice = fmt.Sprintf("Tao %s was installed by another process and will take effect on the next invocation", release.Tag)
	}
	service.Cache.RecordNotice(&state)
	outcome.Failure = errors.Join(outcome.Failure, service.bestEffortSave(state))
	return outcome
}

func (service *Service) startupInstallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := service.StartupInstallTimeout
	if timeout <= 0 {
		timeout = defaultStartupInstallTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func (service *Service) validatedCurrentVersion() (string, error) {
	current := service.CurrentVersion()
	if current == "dev" {
		return "", errors.New("development builds cannot self-update")
	}
	if _, err := parseStableVersion(current); err != nil {
		return "", fmt.Errorf("invalid running version: %w", err)
	}
	return current, nil
}

func (service *Service) compare(current string, release Release) (UpdateResult, error) {
	comparison, err := CompareVersions(current, release.Tag)
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{
		CurrentVersion: current,
		LatestVersion:  release.Tag,
		Comparison:     comparison,
	}, nil
}

func (service *Service) validate(startup bool) error {
	if service == nil {
		return errors.New("self-update service is nil")
	}
	if service.Discoverer == nil {
		return errors.New("self-update service requires a release discoverer")
	}
	if service.Installer == nil {
		return errors.New("self-update service requires an installer")
	}
	if service.CurrentVersion == nil {
		return errors.New("self-update service requires the current version")
	}
	if startup && service.Cache == nil {
		return errors.New("startup self-update service requires a cache")
	}
	return nil
}

func (service *Service) bestEffortSave(state CacheState) error {
	if err := service.Cache.Save(state); err != nil {
		return err
	}
	return nil
}
