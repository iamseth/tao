package run

import (
	"fmt"

	"github.com/iamseth/tao/internal/plan"
)

// checkSelectedApprovalEvidence supplements agent admission, never completion
// settlement. Callers retain ownership of safe-boundary and input preflight.
func checkSelectedApprovalEvidence(detail *plan.PlanDetail) error {
	slice := selectedRunSlice(detail)
	if slice == nil || slice.CommitIntent != nil || slice.Completion != nil || slice.Status == plan.StatusCompleted {
		return nil
	}
	if err := plan.CheckSliceApprovalEvidence(slice); err != nil {
		return fmt.Errorf("%w: %w", ErrCannotStart, err)
	}
	return nil
}
