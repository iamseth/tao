package merge

import (
	"context"
	"fmt"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/repoadmission"
)

// WithRepositoryAdmission encloses mutation before any narrower ownership.
// Record-only callers retain their existing locks without exclusive admission.
func WithRepositoryAdmission(ctx context.Context, guard repoadmission.Guard, detail *plan.PlanDetail, recordOnly bool, operation func(context.Context) error) error {
	if recordOnly {
		return operation(ctx)
	}
	if detail == nil {
		return fmt.Errorf("merge plan detail is nil")
	}
	if guard == nil {
		guard = repoadmission.With
	}
	return guard(ctx, repoadmission.Request{Root: detail.State.Repo.Root, PlanID: detail.State.Plan.ID, PlanDir: detail.Dir, Mode: repoadmission.Merge}, operation)
}
