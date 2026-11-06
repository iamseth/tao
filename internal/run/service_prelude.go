package run

import (
	"context"

	"github.com/iamseth/tao/internal/plan"
)

type resolvedPlanOptions struct {
	admission bool
	header    bool
	status    bool
}

func (s Service) withResolvedPlan(ctx context.Context, request Request, options resolvedPlanOptions, operation func(context.Context, *plan.PlanDetail, ExecutionConfig) error) error {
	config, err := prepareRequestConfig(s.config, request)
	if err != nil {
		return err
	}
	startedAt := now(s.dependencies).UTC()
	return WithOwnedPlan(ctx, s.repo, request.Input, OwnedPlanOptions{
		StartedAt: startedAt,
		Scope: func(ctx context.Context, initial *plan.PlanDetail, runLocked func(context.Context) error) error {
			runWithStatus := func(ctx context.Context) error {
				if options.status {
					return trackRunStatus(ctx, s.dependencies.StatusReporter, initial, startedAt, runLocked)
				}
				return runLocked(ctx)
			}
			run := func(ctx context.Context) error {
				if options.header {
					return trackRunHeader(ctx, s.dependencies.HeaderReporter, initial, config, startedAt, runWithStatus)
				}
				return runWithStatus(ctx)
			}
			if options.admission {
				return WithRepositoryAdmission(ctx, initial, s.dependencies.RepositoryAdmission, run)
			}
			return run(ctx)
		},
	}, func(ownedCtx context.Context, detail *plan.PlanDetail) error {
		return operation(ownedCtx, detail, config)
	})
}
