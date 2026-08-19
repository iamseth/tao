package run

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

// run_setup.go owns dependency wiring. It is the single place where omitted
// dependencies are defaulted, so the dependency graph and the order defaults
// resolve in are visible together rather than scattered behind lazy accessors.

func newRunExecution(config ExecutionConfig, dependencies RunDependencies) runExecution {
	return runExecution{Config: config, Dependencies: dependencies}
}

// runExecutionFromOptions builds an execution for callers that have no Service
// (tests and helpers). It performs no Service-backed defaulting; executor
// defaults resolve lazily once at the run/finalize entry points via
// resolveExecutorDefaults so they observe the final execution state.
func runExecutionFromOptions(options Options) runExecution {
	return newRunExecution(options.executionConfig(), newRunDependencies(options))
}

// prepareRunExecution resolves the full dependency graph for a Service-backed
// run. An in-progress automatic slice is inspected before resolving the normal
// execution root because that resolver may prepare, rebase, persist, or install
// dependencies in a worktree. Resumes instead bind to their immutable root and
// retain the clean-start metadata captured by the original run.
func (s Service) prepareRunExecution(ctx context.Context, detail *plan.PlanDetail, config ExecutionConfig) (runExecution, error) {
	execution := newRunExecution(config, s.dependencies)
	if execution.Config.PullRequest && execution.Config.ExecutionMode == ExecutionModeCurrent {
		return execution, fmt.Errorf("--pull-request requires --execution-mode isolated")
	}
	s.resolveServiceDependencies(&execution)

	boundary, err := (ExecutionBoundaryController{}).InspectSelected(ctx, ExecutionBoundaryDurableFacts{
		Detail: detail, ContinueBlocked: execution.Config.Continue,
	}, execution)
	if err != nil {
		return execution, err
	}
	execution.ExecutionBoundary = boundary
	if boundary != nil && !boundary.AllowWorkspacePreparation {
		if !boundary.AllowAgentHandoff {
			return execution, interruptedSliceRunError(boundary.Diagnostics.Facts.SliceID, *boundary)
		}
		execution.ExecutionRoot = boundary.FixedRoot
		execution.StartingBranch = boundary.StartingBranch
		execution.StartingDirtyPaths = append([]string(nil), boundary.StartingDirtyPaths...)
	} else {
		if boundary != nil && boundary.EffectiveDisposition != InterruptedSliceNewStart {
			return execution, fmt.Errorf("slice %s has unsupported execution-boundary action %q", boundary.Diagnostics.Facts.SliceID, boundary.EffectiveDisposition)
		}
		if err := resolveServiceExecutionRoot(ctx, detail, &execution); err != nil {
			return execution, err
		}
		if execution.Config.ExecutionMode == ExecutionModeCurrent {
			execution, err = captureStartingBranch(ctx, detail, execution)
			if err != nil {
				return execution, err
			}
		}
		execution, err = captureStartingDirtyPaths(ctx, execution)
		if err != nil {
			return execution, err
		}
	}
	resolveExecutorDefaults(&execution)
	if err := requireResolvedDependencies(execution.Dependencies); err != nil {
		return execution, err
	}
	return execution, nil
}

// resolveServiceDependencies is the single home for every pre-capture default:
// first the Service-independent collaborators, then the ones backed by the
// Service (its repository and output writer) and the execution root. A
// RunDependencies field that must exist before the branch and dirty-path
// capture is defaulted here; executor-backed fields default later in
// resolveExecutorDefaults. The two phases stay split across the capture boundary
// because executors must observe the final execution state.
func (s Service) resolveServiceDependencies(execution *runExecution) {
	dependencies := &execution.Dependencies
	// Service-independent collaborators.
	if dependencies.CommandRunner == nil {
		dependencies.CommandRunner = defaultCommandRunner
	}
	if dependencies.reviewGitFactory == nil {
		dependencies.reviewGitFactory = newReviewGitFactory(dependencies.CommandRunner)
	}
	if dependencies.ProcessStarter == nil {
		dependencies.ProcessStarter = defaultProcessStarter
	}
	if dependencies.WorkspacePreparer == nil {
		dependencies.WorkspacePreparer = prepareExecutionWorkspace
	}
	if dependencies.TransportRetryDelay == nil {
		dependencies.TransportRetryDelay = waitForTransportRetry
	}
	// Service-backed collaborators.
	if dependencies.EventAppender == nil {
		dependencies.EventAppender = s.repo
	}
	if dependencies.PlanRecordFactory == nil {
		dependencies.PlanRecordFactory = func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return s.repo.PlanRecord(detail)
		}
	}
	if dependencies.LogAppender == nil {
		dependencies.LogAppender = s.repo
	}
	if dependencies.OutputWriter == nil {
		dependencies.OutputWriter = s.out
	}
	if dependencies.RootResolver == nil {
		dependencies.RootResolver = executionRootResolver(*execution)
	}
}

func resolveServiceExecutionRoot(ctx context.Context, detail *plan.PlanDetail, execution *runExecution) error {
	if execution.ExecutionRoot != "" {
		return nil
	}
	root, err := execution.Dependencies.RootResolver.ResolveExecutionRoot(ctx, detail)
	if err != nil {
		return err
	}
	execution.ExecutionRoot = root
	return nil
}

func interruptedSliceRunError(sliceID string, action ExecutionBoundaryAction) error {
	facts := interruptedSliceDiagnosticFacts(action.Diagnostics.Facts)
	switch action.EffectiveDisposition {
	case InterruptedSliceCompletionRecovery:
		return fmt.Errorf("slice %s has an interrupted post-intent completion transaction: %s (%s); rerun tao slice-complete with the original notes and verification inputs; do not rerun the implementation agent or commit the automatic slice by hand", sliceID, action.Diagnostics.Reason, facts)
	case InterruptedSliceManualCompletion:
		return fmt.Errorf("slice %s requires manual completion: %s (%s); inspect and verify the current-checkout work, then run tao slice-complete with its notes and verification inputs or restore the recorded boundary; Tao will not attribute these changes to a resumed agent", sliceID, action.Diagnostics.Reason, facts)
	default:
		return fmt.Errorf("slice %s cannot be resumed safely: %s (%s); leave the automatic slice uncommitted, inspect the changed paths, and restore the recorded root, branch, and HEAD before rerunning Tao", sliceID, action.Diagnostics.Reason, facts)
	}
}

func interruptedSliceDiagnosticFacts(facts InterruptedSliceFacts) string {
	fields := []string{
		fmt.Sprintf("recorded_root=%q", facts.RecordedRoot),
		fmt.Sprintf("live_root=%q", facts.LiveRoot),
		fmt.Sprintf("recorded_branch=%q", facts.RecordedBranch),
		fmt.Sprintf("live_branch=%q", facts.Branch),
		fmt.Sprintf("recorded_HEAD=%q", facts.RecordedHead),
		fmt.Sprintf("live_HEAD=%q", facts.Head),
		fmt.Sprintf("policy=%q", facts.CommitPolicy),
		fmt.Sprintf("workspace=%q", facts.WorkspaceStrategy),
	}
	if len(facts.ChangedPaths) > 0 {
		fields = append(fields, "changed_paths="+strings.Join(facts.ChangedPaths, ","))
	} else {
		fields = append(fields, "changed_paths=none")
	}
	return strings.Join(fields, " ")
}

func resolveExecutorDefaults(execution *runExecution) {
	dependencies := &execution.Dependencies
	if dependencies.reviewGitFactory == nil {
		dependencies.reviewGitFactory = newReviewGitFactory(dependencies.CommandRunner)
	}
	if dependencies.TransportRetryDelay == nil {
		dependencies.TransportRetryDelay = waitForTransportRetry
	}
	if dependencies.AgentFactory == nil {
		dependencies.AgentFactory = defaultAgentCapabilitiesFactory
	}
	capabilities := dependencies.AgentFactory(*execution)
	if dependencies.SliceExecutor == nil {
		dependencies.SliceExecutor = capabilities.sliceExecutor
	}
	if dependencies.PullRequestCreator == nil {
		dependencies.PullRequestCreator = defaultPullRequestCreatorWithBody(*execution, capabilities.pullRequestBodyGenerator)
	}
	if dependencies.ReviewCreator == nil {
		dependencies.ReviewCreator = capabilities.reviewCreator
	}
}

func captureStartingBranch(ctx context.Context, detail *plan.PlanDetail, execution runExecution) (runExecution, error) {
	repoRoot := execution.ExecutionRoot
	if repoRoot == "" {
		repoRoot = detail.State.Repo.Root
	}
	branch, err := gitClient(execution, repoRoot).CurrentBranch(ctx)
	if err != nil {
		return execution, fmt.Errorf("detect starting branch for branch policy current: %w", err)
	}
	if branch == "" {
		return execution, fmt.Errorf("detect starting branch for branch policy current: git branch --show-current returned empty branch")
	}
	workspaceBranch := ""
	if detail.State.Workspace != nil {
		workspaceBranch = detail.State.Workspace.Branch
	}
	if detail.State.Repo.Branch != branch || workspaceBranch != branch {
		record, err := planMutationRecord(execution, detail)
		if err != nil {
			return execution, fmt.Errorf("record starting branch for branch policy current: %w", err)
		}
		if err := record.RecordStartingBranch(branch); err != nil {
			return execution, fmt.Errorf("record starting branch for branch policy current: %w", err)
		}
	}
	execution.StartingBranch = branch
	return execution, nil
}

func captureStartingDirtyPaths(ctx context.Context, execution runExecution) (runExecution, error) {
	status, err := gitClient(execution, execution.ExecutionRoot).StatusPorcelain(ctx)
	if err != nil {
		return execution, fmt.Errorf("capture run-start dirty paths: %w", err)
	}
	porcelainPaths, ambiguous := gitops.PorcelainPaths(status)
	if len(ambiguous) > 0 {
		return execution, fmt.Errorf("capture run-start dirty paths: ambiguous git status entry %q", ambiguous[0])
	}
	paths := map[string]bool{}
	for _, path := range porcelainPaths {
		paths[path] = true
	}
	execution.StartingDirtyPaths = sortedPathSet(paths)
	return execution, nil
}

func sortedPathSet(paths map[string]bool) []string {
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func waitForTransportRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func now(options clockConfig) time.Time {
	if options.clock() != nil {
		return options.clock()()
	}
	return time.Now()
}
