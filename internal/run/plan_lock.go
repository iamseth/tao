package run

import (
	"context"
	"errors"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// WithPlanRunLock runs operation while holding the ordinary per-plan driver
// lock. The callback context carries ownership, so nested lifecycle drivers for
// the same plan are re-entrant and retain the original lock until operation
// returns. Contention also matches ErrCannotStart.
func WithPlanRunLock(ctx context.Context, detail *plan.PlanDetail, timestamp time.Time, operation func(context.Context) error) error {
	err := plan.WithRunLock(ctx, detail, timestamp, operation)
	if errors.Is(err, plan.ErrRunLocked) {
		return &planRunLockError{err: err}
	}
	return err
}

type planRunLockError struct {
	err error
}

func (e *planRunLockError) Error() string        { return e.err.Error() }
func (e *planRunLockError) Unwrap() error        { return e.err }
func (e *planRunLockError) Is(target error) bool { return target == ErrCannotStart }
