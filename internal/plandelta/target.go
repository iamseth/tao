package plandelta

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type Target struct {
	PlanID, RepoRoot, WorktreePath, Branch, Strategy string
	Separate, BranchIsNew                            bool
}

// ResolveTarget projects plan-recorded identity without filesystem or Git probes.
// BranchIsNew preserves RequireNew; branch existence must be checked separately.
// Callers supply workspace defaults explicitly, as for ResolveExecutionRoot.
func ResolveTarget(detail *plan.PlanDetail, config workspace.Config) (Target, error) {
	if detail == nil {
		return Target{}, fmt.Errorf("plan detail is nil")
	}
	root := strings.TrimSpace(detail.State.Repo.Root)
	if !filepath.IsAbs(root) {
		return Target{}, fmt.Errorf("plan repo root must be absolute and non-empty")
	}
	branch, err := workspace.ResolvePlanBranch(detail, config)
	if err != nil {
		return Target{}, err
	}
	execution, err := workspace.ResolveExecutionRoot(detail, config)
	if err != nil {
		return Target{}, err
	}
	if !filepath.IsAbs(execution.Root) {
		return Target{}, fmt.Errorf("plan execution root must be absolute and non-empty")
	}
	return Target{
		PlanID: strings.TrimSpace(detail.State.Plan.ID), RepoRoot: root,
		WorktreePath: execution.Root, Branch: branch.Name, Strategy: execution.Strategy,
		Separate: execution.Separate, BranchIsNew: branch.RequireNew,
	}, nil
}
