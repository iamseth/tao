package run

import (
	"context"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestAgentFactoryBuildsPiRunExecutors(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return nil, nil
	}
	sessionTimeout := 90 * time.Second
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent, SessionTimeout: sessionTimeout}}, RunDependencies{ProcessStarter: starter, LogAppender: repo, EventAppender: repo})
	execution.StartingBranch = "feature/pi"

	factory := newAgentFactory(execution)
	capabilities := factory.runCapabilities()
	sliceExecutor, ok := capabilities.sliceExecutor.(agentExecutor)
	if !ok || sliceExecutor.descriptor.Kind != AgentPi {
		t.Fatalf("expected Pi slice executor, got %T", capabilities.sliceExecutor)
	}
	if bodyGenerator, ok := capabilities.pullRequestBodyGenerator.(agentExecutor); !ok || bodyGenerator.descriptor.Kind != AgentPi {
		t.Fatalf("expected Pi pull request body generator, got %T", capabilities.pullRequestBodyGenerator)
	}
	if sliceExecutor.options.Deps.ProcessStarter == nil || sliceExecutor.options.CommitPolicy != CommitPolicySlice || sliceExecutor.options.ExecutionMode != ExecutionModeCurrent || sliceExecutor.options.SessionTimeout != sessionTimeout || sliceExecutor.options.StartingBranch != "feature/pi" {
		t.Fatalf("unexpected Pi run options: %+v", sliceExecutor.options)
	}
	if sliceExecutor.logAppender != repo || sliceExecutor.eventAppender != repo {
		t.Fatal("expected Pi executor to preserve log and event appenders")
	}
}

func TestAgentFactoryBuildsClaudeExecutorsAndAuxiliaryFallbacks(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return nil, nil
	}
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentClaude, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent}, SkipPermissions: true}, RunDependencies{ProcessStarter: starter, LogAppender: repo, EventAppender: repo})

	capabilities := newAgentFactory(execution).runCapabilities()
	sliceExecutor, ok := capabilities.sliceExecutor.(agentExecutor)
	if !ok || sliceExecutor.descriptor.Kind != AgentClaude {
		t.Fatalf("expected Claude slice executor, got %T", capabilities.sliceExecutor)
	}
	if bodyGenerator, ok := capabilities.pullRequestBodyGenerator.(agentExecutor); !ok || bodyGenerator.descriptor.Kind != AgentClaude {
		t.Fatalf("expected Claude pull request body generator, got %T", capabilities.pullRequestBodyGenerator)
	}
	if sliceExecutor.options.Deps.ProcessStarter == nil || sliceExecutor.options.CommitPolicy != CommitPolicySlice || sliceExecutor.options.ExecutionMode != ExecutionModeCurrent || !sliceExecutor.options.SkipPermissions {
		t.Fatalf("unexpected Claude run options: %+v", sliceExecutor.options)
	}
	if sliceExecutor.logAppender != repo || sliceExecutor.eventAppender != repo {
		t.Fatal("expected Claude executor to preserve log and event appenders")
	}
}
