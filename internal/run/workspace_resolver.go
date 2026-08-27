package run

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type ExecutionRootResolverFunc func(ctx context.Context, detail *plan.PlanDetail) (string, error)

func (f ExecutionRootResolverFunc) ResolveExecutionRoot(ctx context.Context, detail *plan.PlanDetail) (string, error) {
	return f(ctx, detail)
}

func preparedInterruptedExecutionRoot(detail *plan.PlanDetail, config ExecutionConfig) (string, error) {
	if detail == nil || detail.State.Workspace == nil {
		return "", fmt.Errorf("interrupted slice has no durable workspace metadata")
	}
	workspaceState := detail.State.Workspace
	if config.ExecutionMode != ExecutionModeIsolated || workspaceState.Strategy != plan.WorkspaceStrategyWorktree {
		return "", fmt.Errorf("interrupted automatic start does not record an isolated worktree")
	}
	if workspaceState.LifecycleStatus != plan.WorkspaceStatusReady {
		return "", fmt.Errorf("interrupted automatic start workspace is not ready")
	}
	root := workspace.ResolveRecordedWorktree(detail).Path
	if reason := interruptedWorktreeIdentityError(detail, root); reason != "" {
		return "", fmt.Errorf("interrupted automatic start workspace identity: %s", reason)
	}
	if workspaceState.Branch == "" || workspaceState.HeadSHA == "" {
		return "", fmt.Errorf("interrupted automatic start workspace metadata is missing branch or HEAD")
	}
	return root, nil
}

func executionRootResolver(execution runExecution) ExecutionRootResolver {
	if execution.ExecutionRoot == "" && execution.Dependencies.RootResolver != nil {
		return execution.Dependencies.RootResolver
	}
	input := workspaceResolverInput(execution)
	return ExecutionRootResolverFunc(func(ctx context.Context, detail *plan.PlanDetail) (string, error) {
		if input.ExecutionRoot != "" {
			return input.ExecutionRoot, nil
		}
		preparer := input.WorkspacePreparer
		if preparer == nil {
			preparer = prepareExecutionWorkspace
		}
		root, err := preparer(ctx, detail, input)
		if err != nil {
			return "", fmt.Errorf("prepare execution workspace: %w", err)
		}
		return root, nil
	})
}

func workspaceResolverInput(execution runExecution) WorkspaceResolverInput {
	return WorkspaceResolverInput{
		Config:            execution.Config,
		ExecutionRoot:     execution.ExecutionRoot,
		CommandRunner:     execution.Dependencies.CommandRunner,
		PlanRecordFactory: execution.Dependencies.PlanRecordFactory,
		WorkspacePreparer: execution.Dependencies.WorkspacePreparer,
		Now:               execution.Dependencies.Now,
	}
}

func prepareExecutionWorkspace(ctx context.Context, detail *plan.PlanDetail, input WorkspaceResolverInput) (string, error) {
	preparer := workspace.ExecutionPreparer{
		Runner:            input.CommandRunner,
		PlanRecordFactory: workspacePlanRecordFactory(input.PlanRecordFactory),
		Now:               input.Now,
		Config:            input.Config.WorkspaceConfig,
	}
	return preparer.Prepare(ctx, detail, workspace.ExecutionPrepareOptions{
		ExecutionMode: input.Config.ExecutionMode.String(),
	})
}

func workspacePlanRecordFactory(factory PlanRecordFactory) workspace.PlanRecordFactory {
	if factory == nil {
		return nil
	}
	return func(detail *plan.PlanDetail) (workspace.PlanRecord, error) {
		record, err := factory(detail)
		if err != nil {
			return nil, err
		}
		return record, nil
	}
}

func absolutePlanDir(planDir string) string {
	abs, err := filepath.Abs(planDir)
	if err != nil {
		return planDir
	}
	return abs
}
