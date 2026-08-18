package run

import (
	"context"
	"io"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
)

// Process aliases and defaults are re-exported here so the run package's
// dependency wiring can reference them without importing internal/agent
// directly.
type ProcessStarter = agent.ProcessStarter
type Process = agent.Process

var defaultProcessStarter ProcessStarter = agent.DefaultProcessStarter

// agentExecutor is the single descriptor-driven executor shared by every
// runtime. It resolves all per-kind behavior (permission policy, no-progress
// watchdog limit, commit-prompt renderer, runtime construction) from the
// agent.Descriptor and run-local data rather than from an agent-kind switch.
type agentExecutor struct {
	descriptor    agent.Descriptor
	options       agentExecutorOptions
	planRecords   PlanRecordFactory
	logAppender   plan.LogAppender
	eventAppender plan.EventAppender
}

type agentExecutorOptions struct {
	Deps               agent.RuntimeDeps
	CommitPolicy       CommitPolicy
	ExecutionMode      ExecutionMode
	SkipPermissions    bool
	SessionTimeout     time.Duration
	SessionLogWriter   io.Writer
	CommandRunner      CommandRunner
	reviewGitFactory   reviewGitFactory
	Now                func() time.Time
	StartingBranch     string
	StartingDirtyPaths []string
}

func (o agentExecutorOptions) clock() func() time.Time { return o.Now }

// newAgentExecutor builds the unified executor for the resolved descriptor.
func newAgentExecutor(descriptor agent.Descriptor, config ExecutionConfig, dependencies RunDependencies, startingBranch string, startingDirtyPaths []string) agentExecutor {
	return agentExecutor{
		descriptor: descriptor,
		options: agentExecutorOptions{
			Deps:               agent.RuntimeDeps{ProcessStarter: dependencies.ProcessStarter},
			CommitPolicy:       config.CommitPolicy,
			ExecutionMode:      config.ExecutionMode,
			SkipPermissions:    config.SkipPermissions,
			SessionTimeout:     config.SessionTimeout,
			SessionLogWriter:   dependencies.SessionLogWriter,
			CommandRunner:      dependencies.CommandRunner,
			reviewGitFactory:   dependencies.reviewGitFactory,
			Now:                dependencies.Now,
			StartingBranch:     startingBranch,
			StartingDirtyPaths: startingDirtyPaths,
		},
		planRecords:   dependencies.PlanRecordFactory,
		logAppender:   dependencies.LogAppender,
		eventAppender: dependencies.EventAppender,
	}
}

func (e agentExecutor) operationOptions() agentOperationOptions {
	return agentOperationOptions{
		CommitPolicy:        e.options.CommitPolicy,
		ExecutionMode:       e.options.ExecutionMode,
		StartingBranch:      e.options.StartingBranch,
		StartingDirtyPaths:  e.options.StartingDirtyPaths,
		Agent:               e.descriptor.Label,
		CommandRunner:       e.options.CommandRunner,
		reviewGitFactory:    e.options.reviewGitFactory,
		Now:                 e.options.Now,
		NoProgressToolLimit: e.descriptor.DefaultNoProgressToolLimit,
	}
}

// permissionMode resolves the agent permission mode from descriptor data. Only
// runtimes that support bypass honor a SkipPermissions request; every other
// runtime stays on Tao-managed auto permissions, preserving Pi's always-Auto
// behavior and Claude's --dangerously-skip-permissions -> bypass mapping.
func (e agentExecutor) permissionMode() agent.PermissionMode {
	if e.descriptor.SupportsBypassPermissions && e.options.SkipPermissions {
		return agent.PermissionModeBypassPermissions
	}
	return agent.PermissionModeAuto
}

func (e agentExecutor) RunSlice(ctx context.Context, run SliceRun) error {
	return runSliceWithAgentSession(ctx, e, e.operationOptions(), run)
}

func (e agentExecutor) CreatePullRequest(ctx context.Context, run PullRequestRun) (plan.PullRequest, error) {
	return createPullRequestWithAgentSession(ctx, e, e.operationOptions(), run)
}

func (e agentExecutor) GeneratePullRequestBody(ctx context.Context, run PullRequestBodyRun) (string, error) {
	return generatePullRequestBodyWithAgentSession(ctx, e, e.operationOptions(), run)
}

func (e agentExecutor) CreateReview(ctx context.Context, run ReviewRun) (plan.PlanReview, error) {
	return createReviewWithAgentSession(ctx, e, e.operationOptions(), run, e.planRecords)
}

func (e agentExecutor) RunAgentSession(ctx context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
	return e.sessionRunner().RunAgentSession(ctx, request)
}

func (e agentExecutor) sessionRunner() agentSessionRunner {
	return newAgentSessionRunner(agentSessionRunnerConfig{
		descriptor:       e.descriptor,
		deps:             e.options.Deps,
		permissionMode:   e.permissionMode(),
		sessionTimeout:   e.options.SessionTimeout,
		logAppender:      e.logAppender,
		eventAppender:    e.eventAppender,
		sessionLogWriter: e.options.SessionLogWriter,
		commandRunner:    e.options.CommandRunner,
		now:              e.options.Now,
	})
}
