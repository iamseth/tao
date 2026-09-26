package run

import (
	"context"
	"fmt"

	"github.com/iamseth/tao/internal/plan"
)

type resolvedPlanOptions struct {
	header bool
	status bool
}

func (s Service) withResolvedPlan(ctx context.Context, request Request, options resolvedPlanOptions, operation func(context.Context, *plan.PlanDetail, ExecutionConfig) error) error {
	config, err := prepareRequestConfig(s.config, request)
	if err != nil {
		return err
	}
	lockDetail, err := s.repo.ResolvePlan(ctx, request.Input)
	if err != nil {
		return err
	}
	if lockDetail == nil {
		return fmt.Errorf("plan %q not found", request.Input)
	}
	planDir := lockDetail.Dir
	startedAt := now(s.dependencies).UTC()
	runLocked := func(ctx context.Context) error {
		return WithPlanRunLock(ctx, lockDetail, startedAt, func(ownedCtx context.Context) error {
			// Resolve by the exact directory after acquisition. The pre-lock detail
			// identifies ownership only: another lifecycle driver may have changed
			// the plan before this lock was acquired.
			detail, err := s.repo.ResolvePlan(ownedCtx, planDir)
			if err != nil {
				return err
			}
			if detail == nil {
				return fmt.Errorf("plan %q not found", planDir)
			}
			return operation(ownedCtx, detail, config)
		})
	}
	runWithStatus := func(ctx context.Context) error {
		if options.status {
			return trackRunStatus(ctx, s.dependencies.StatusReporter, lockDetail, startedAt, runLocked)
		}
		return runLocked(ctx)
	}
	if options.header {
		return trackRunHeader(ctx, s.dependencies.HeaderReporter, lockDetail, config, startedAt, runWithStatus)
	}
	return runWithStatus(ctx)
}
