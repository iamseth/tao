package cli

import (
	"context"
	"os"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func newReworkDriver(repo planRunRepository, now func() time.Time, escalation reworkpkg.EscalationPolicy) reworkpkg.Driver {
	return reworkpkg.Driver{
		Resolve: repo.ResolvePlan,
		Record: func(detail *plan.PlanDetail) (reworkpkg.AutomaticRecord, error) {
			return repo.PlanRecord(detail)
		},
		Now:        now,
		Escalation: escalation,
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

func runReworkEnvDefaults() (bool, int, error) {
	return runtimeconfig.ParseAutoReworkEnv(
		true,
		runtimeconfig.DefaultMaxReworkAttempts,
		os.Getenv(runtimeconfig.EnvAutoRework),
		os.Getenv(runtimeconfig.EnvMaxReworkAttempts),
	)
}
