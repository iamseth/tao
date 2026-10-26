package cli

import (
	"context"
	"fmt"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/repoadmission"
)

// Batch admission precedes candidate discovery, so preserve the explicitly
// selected filesystem source rather than deriving it from selected candidates.
func (a App) repositoryObservationGuard(repository any) repoadmission.Guard {
	if a.RepositoryAdmission != nil {
		return a.RepositoryAdmission
	}
	plansDir := ""
	if source, ok := repository.(*plan.FileRepository); ok {
		plansDir = source.Dir
	}
	return func(ctx context.Context, request repoadmission.Request, operation func(context.Context) error) error {
		request.PlansDir = plansDir
		if a.Err != nil {
			request.Warn = func(err error) { _, _ = fmt.Fprintf(a.Err, "warning: repository admission observation: %v\n", err) }
		}
		return repoadmission.With(ctx, request, operation)
	}
}
