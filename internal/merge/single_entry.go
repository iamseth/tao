package merge

import (
	"context"
	"errors"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/repoadmission"
	"github.com/iamseth/tao/internal/run"
)

// SingleOwnedPlanOptions supplies single-merge ownership dependencies, not
// service configuration. BatchDependencies runs inside repository admission.
type SingleOwnedPlanOptions struct {
	StartedAt           time.Time
	RepositoryAdmission repoadmission.Guard
	RecordOnly          bool
	Restart             bool
	BatchDependencies   func(context.Context, string) (*BatchStore, LandingIntentProbe, error)
}

// WithSingleOwnedPlan admits a single merge and protects its refreshed plan
// through operation. Restart never inherits the record-only exemption.
func WithSingleOwnedPlan(ctx context.Context, resolver plan.Resolver, input string, options SingleOwnedPlanOptions, operation func(context.Context, *plan.PlanDetail) error) error {
	if options.BatchDependencies == nil {
		return errors.New("single merge requires batch dependencies")
	}
	recordOnly := options.RecordOnly && !options.Restart
	return run.WithOwnedPlan(ctx, resolver, input, run.OwnedPlanOptions{
		StartedAt: options.StartedAt,
		Scope: func(ctx context.Context, initial *plan.PlanDetail, next func(context.Context) error) error {
			return WithRepositoryAdmission(ctx, options.RepositoryAdmission, initial, recordOnly, func(ctx context.Context) error {
				store, probe, err := options.BatchDependencies(ctx, initial.State.Repo.Root)
				if err != nil {
					return err
				}
				if err := RequireNoPreLandingBatch(ctx, store, probe, initial.State.Plan.ID, recordOnly); err != nil {
					return err
				}
				return next(ctx)
			})
		},
	}, operation)
}
