package run

import (
	"context"
	"fmt"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/repoadmission"
)

// WithRepositoryAdmission retains shared execution ownership before any plan
// lock or mutation. Only acquisition failures are classified as cannot-start;
// callback failures keep their original classification.
func WithRepositoryAdmission(ctx context.Context, detail *plan.PlanDetail, guard repoadmission.Guard, operation func(context.Context) error) error {
	if guard == nil {
		guard = repoadmission.With
	}
	entered := false
	err := guard(ctx, repoadmission.Request{Root: detail.State.Repo.Root, PlanID: detail.State.Plan.ID, PlanDir: detail.Dir, Mode: repoadmission.Run}, func(ownedCtx context.Context) error {
		entered = true
		return operation(ownedCtx)
	})
	if err != nil && !entered {
		return fmt.Errorf("%w: %w", ErrCannotStart, err)
	}
	return err
}
