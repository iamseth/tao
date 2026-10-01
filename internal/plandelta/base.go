package plandelta

import (
	"context"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

// BaseGit is the read-only Git surface used for persisted-review base parity.
type BaseGit interface {
	DefaultBranch(context.Context) (string, error)
	MergeBase(context.Context, string, string) (string, error)
}

// BaseSource identifies advisory base provenance, not merge authority.
type BaseSource string

const (
	BaseSourceLiveMergeBase BaseSource = "live_merge_base"
	BaseSourceWorkspace     BaseSource = "workspace.base_sha"
	BaseSourceRepo          BaseSource = "repo.base_commit"
	BaseSourceNone          BaseSource = "none"
)

type Base struct {
	SHA           string
	Source        BaseSource
	DefaultBranch string
	PlanBranch    string
}

// LiveMergeBase preserves persisted review's live-base lookup and fallback rules.
// An empty result leaves recorded-base fallback to the caller.
func LiveMergeBase(ctx context.Context, git BaseGit, state plan.State) string {
	return liveBase(ctx, git, state).SHA
}

func liveBase(ctx context.Context, git BaseGit, state plan.State) Base {
	result := Base{Source: BaseSourceNone}
	if state.Workspace == nil {
		return result
	}
	result.PlanBranch = strings.TrimSpace(state.Workspace.Branch)
	if result.PlanBranch == "" {
		return result
	}
	defaultBranch, err := git.DefaultBranch(ctx)
	result.DefaultBranch = strings.TrimSpace(defaultBranch)
	if err != nil || result.DefaultBranch == "" {
		result.DefaultBranch = strings.TrimSpace(state.Workspace.BaseBranch)
	}
	if result.DefaultBranch == "" || result.DefaultBranch == result.PlanBranch {
		return result
	}
	base, err := git.MergeBase(ctx, result.DefaultBranch, result.PlanBranch)
	if err != nil {
		return result
	}
	result.SHA = strings.TrimSpace(base)
	if result.SHA != "" {
		result.Source = BaseSourceLiveMergeBase
	}
	return result
}

func WorkspaceBase(state plan.State) string {
	if state.Workspace == nil {
		return ""
	}
	return strings.TrimSpace(state.Workspace.BaseSHA)
}

// ResolveBase uses the same persisted bases as review, without run-only overrides.
func ResolveBase(ctx context.Context, git BaseGit, state plan.State) Base {
	result := liveBase(ctx, git, state)
	if result.SHA != "" {
		return result
	}
	if result.SHA = WorkspaceBase(state); result.SHA != "" {
		result.Source = BaseSourceWorkspace
	} else if result.SHA = strings.TrimSpace(state.Repo.BaseCommit); result.SHA != "" {
		result.Source = BaseSourceRepo
	}
	return result
}
