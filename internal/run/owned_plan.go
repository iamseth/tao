package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// OwnedPlanOptions supplies the caller's admission/presentation scope. Scope
// must invoke its callback with the context carrying any acquired ownership.
type OwnedPlanOptions struct {
	StartedAt time.Time
	Scope     func(context.Context, *plan.PlanDetail, func(context.Context) error) error
}

var errOwnedPlanAbandoned error = errors.New("owned plan abandoned")

// WithOwnedPlan resolves once before scope entry, then reloads that exact
// directory under the plan lock. Only the refreshed snapshot reaches operation.
func WithOwnedPlan(ctx context.Context, resolver plan.Resolver, input string, options OwnedPlanOptions, operation func(context.Context, *plan.PlanDetail) error) error {
	return withOwnedPlan(ctx, resolver, input, options, operation, WithPlanRunLock)
}

func withOwnedPlan(ctx context.Context, resolver plan.Resolver, input string, options OwnedPlanOptions, operation func(context.Context, *plan.PlanDetail) error, lock func(context.Context, *plan.PlanDetail, time.Time, func(context.Context) error) error) error {
	if ctx == nil || resolver == nil || options.Scope == nil || operation == nil {
		return errors.New("owned plan requires context, resolver, scope, and operation")
	}
	if strings.TrimSpace(input) == "" {
		return errors.New("owned plan requires a selector")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	initial, err := resolver.ResolvePlan(ctx, input)
	if err != nil {
		return err
	}
	if initial == nil {
		return fmt.Errorf("plan %q not found", input)
	}
	dir := initial.Dir
	return options.Scope(ctx, initial, func(ownedCtx context.Context) error {
		if err := ownedCtx.Err(); err != nil {
			return err
		}
		return lock(ownedCtx, initial, options.StartedAt, func(lockedCtx context.Context) error {
			if err := lockedCtx.Err(); err != nil {
				return err
			}
			refreshed, err := resolver.ResolvePlan(lockedCtx, dir)
			if err != nil {
				return err
			}
			if refreshed == nil {
				return fmt.Errorf("plan %q not found", dir)
			}
			if err := plan.RequireNotAbandoned(refreshed); err != nil {
				return &ownedPlanAbandonedError{err: err}
			}
			return operation(lockedCtx, refreshed)
		})
	})
}

type ownedPlanAbandonedError struct{ err error }

func (e *ownedPlanAbandonedError) Error() string        { return e.err.Error() }
func (e *ownedPlanAbandonedError) Unwrap() error        { return e.err }
func (e *ownedPlanAbandonedError) Is(target error) bool { return target == errOwnedPlanAbandoned }
