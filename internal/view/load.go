package view

import (
	"context"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type Repository interface {
	GetPlan(ctx context.Context, id string) (*plan.PlanDetail, error)
}

type Options struct {
	Now func() time.Time
}

type Plan struct {
	Detail  *plan.PlanDetail
	Derived plan.DerivedPlan
	Now     time.Time
}

func LoadPlan(ctx context.Context, repo Repository, id string, options Options) (Plan, error) {
	detail, err := repo.GetPlan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}

	return Plan{Detail: detail, Derived: plan.Derive(detail, now), Now: now}, nil
}
