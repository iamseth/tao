package cli

import (
	"context"
	"os"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runqueue"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func newReworkDriver(repo queueRepository, now func() time.Time) reworkpkg.Driver {
	return reworkpkg.Driver{
		Resolve: repo.ResolvePlan,
		Record: func(detail *plan.PlanDetail) (reworkpkg.Record, error) {
			return repo.PlanRecord(detail)
		},
		Now:         now,
		AppendEvent: repo.AppendEvent,
	}
}

func planAutoReworker(repo queueRepository, now func() time.Time) runqueue.AutoReworker {
	return planAutoReworkerWithRestart(repo, now, false)
}

func planAutoReworkerWithRestart(repo queueRepository, now func() time.Time, allowRestart bool) runqueue.AutoReworker {
	driver := newReworkDriver(repo, now)
	return func(ctx context.Context, planID string, baseline int, attempts int, previous string, maxAttempts int) (reworkpkg.Decision, error) {
		run.ReportPhase(ctx, run.PhaseAutomaticRework, nil)
		detail, err := repo.ResolvePlan(ctx, planID)
		if err != nil {
			return reworkpkg.Decision{}, err
		}
		if _, _, err := reworkpkg.GuardAutoReworkRestart(detail, allowRestart); err != nil {
			return reworkpkg.Decision{}, err
		}
		return driver.Decide(ctx, planID, baseline, attempts, previous, maxAttempts)
	}
}

func automaticReworkPhaseHook(maxAttempts int, enabled bool) reworkpkg.DecisionCheck {
	if !enabled || maxAttempts <= 0 {
		return nil
	}
	return func(ctx context.Context) (int, bool, error) {
		run.ReportPhase(ctx, run.PhaseAutomaticRework, nil)
		return maxAttempts, true, nil
	}
}

const (
	envAutoRework        = runtimeconfig.EnvAutoRework
	envMaxReworkAttempts = runtimeconfig.EnvMaxReworkAttempts
)

func runReworkEnvDefaults() (bool, int, error) {
	return runtimeconfig.ParseAutoReworkEnv(
		true,
		runtimeconfig.DefaultMaxReworkAttempts,
		os.Getenv(runtimeconfig.EnvAutoRework),
		os.Getenv(runtimeconfig.EnvMaxReworkAttempts),
	)
}

func reworkEnvDefaults() (bool, int, error) {
	return runtimeconfig.ParseAutoReworkEnv(
		false,
		runtimeconfig.DefaultMaxReworkAttempts,
		os.Getenv(runtimeconfig.EnvAutoRework),
		os.Getenv(runtimeconfig.EnvMaxReworkAttempts),
	)
}
