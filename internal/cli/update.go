package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamseth/tao/internal/selfupdate"
	"github.com/iamseth/tao/internal/taodata"
)

// SelfUpdater performs explicit and cache-aware startup self-update operations.
type SelfUpdater interface {
	Update(context.Context) (selfupdate.UpdateResult, error)
	Startup(context.Context, selfupdate.Mode) selfupdate.StartupOutcome
}

var updateCommand = commandMetadata{
	name:                  "update",
	usageLines:            []string{"update"},
	completionDescription: "Update Tao to the latest stable release",
	long:                  "Discover and install the latest stable Tao release. This explicit command always checks for a release, even when TAO_UPDATE=off.",
	examples:              "  tao update",
	execute: func(c commandContext) error {
		return c.app.update(c.ctx, c.args)
	},
}

func (a App) update(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: tao update")
	}

	result, err := a.selfUpdater().Update(ctx)
	if err != nil {
		if selfupdate.IsHomebrewError(err) {
			return fmt.Errorf("cannot replace a Homebrew-managed Tao installation; run 'brew upgrade tao': %w", err)
		}
		return fmt.Errorf("update Tao: %w", err)
	}

	switch result.Comparison {
	case selfupdate.VersionCurrent:
		return writef(a.Out, "Tao %s is already up to date.\n", result.CurrentVersion)
	case selfupdate.VersionAhead:
		return writef(a.Out, "Tao %s is newer than the latest stable release %s; no update was installed.\n", result.CurrentVersion, result.LatestVersion)
	case selfupdate.VersionUpdateAvailable:
		switch {
		case result.Installed:
			return writef(a.Out, "Updated Tao from %s to %s at %s. The update will take effect on the next invocation.\n", result.CurrentVersion, result.LatestVersion, result.Path)
		case result.ConcurrentUpdate:
			return writef(a.Out, "Tao %s was installed by another process at %s and will take effect on the next invocation.\n", result.LatestVersion, result.Path)
		default:
			return errors.New("update Tao: update service returned without installing the available release")
		}
	default:
		return fmt.Errorf("update Tao: unsupported update result %q", result.Comparison)
	}
}

func (a App) selfUpdater() SelfUpdater {
	if a.SelfUpdater != nil {
		return a.SelfUpdater
	}
	return &selfupdate.Service{
		Discoverer:     selfupdate.NewDiscoverer(nil),
		Installer:      &selfupdate.Installer{},
		Cache:          selfupdate.NewCache(taodata.DataHome()),
		CurrentVersion: buildVersion,
	}
}

func (a App) runStartupUpdate(ctx context.Context) error {
	mode, err := selfupdate.ParseMode(os.Getenv("TAO_UPDATE"))
	if err != nil {
		return fmt.Errorf("TAO_UPDATE: %w", err)
	}
	if mode == selfupdate.ModeOff || (a.SelfUpdater == nil && buildVersion() == "dev") {
		return nil
	}

	outcome := a.selfUpdater().Startup(ctx, mode)
	if a.Err == nil {
		return nil
	}
	if outcome.AutomaticInstallFailure != nil {
		message := outcome.AutomaticInstallFailure.Error()
		if outcome.Notice != "" {
			message = outcome.Notice
		}
		_, _ = fmt.Fprintf(a.Err, "warning: automatic Tao update failed: %s\n", message)
	} else if outcome.Notice != "" {
		_, _ = fmt.Fprintf(a.Err, "notice: %s\n", outcome.Notice)
	}
	return nil
}
