package run

import "github.com/iamseth/tao/internal/agent"

// AgentCapabilitiesFactory builds the agent-backed run capabilities for an
// execution. It is a first-class, injectable dependency: run_setup.go defaults
// it to defaultAgentCapabilitiesFactory, and callers may supply their own to
// substitute the agent wiring without touching the defaulting cascade.
type AgentCapabilitiesFactory func(execution runExecution) agentRunCapabilities

// defaultAgentCapabilitiesFactory is the built-in wiring: it constructs the
// agent factory from the execution and returns its run capabilities.
func defaultAgentCapabilitiesFactory(execution runExecution) agentRunCapabilities {
	return newAgentFactory(execution).runCapabilities()
}

// agentFactory centralizes construction of agent-backed operations. Tao
// selects built-in agent runtimes while preserving Pi as the default.
type agentFactory struct {
	config             ExecutionConfig
	dependencies       RunDependencies
	startingBranch     string
	startingDirtyPaths []string
}

type agentRunCapabilities struct {
	sliceExecutor            SliceExecutor
	pullRequestBodyGenerator PullRequestBodyGenerator
	reviewCreator            ReviewCreator
}

func newAgentFactory(execution runExecution) agentFactory {
	return agentFactory{config: execution.Config, dependencies: execution.Dependencies, startingBranch: execution.StartingBranch, startingDirtyPaths: execution.StartingDirtyPaths}
}

func (f agentFactory) runCapabilities() agentRunCapabilities {
	descriptor, _ := agent.Lookup(f.config.Agent)
	executor := newAgentExecutor(descriptor, f.config, f.dependencies, f.startingBranch, f.startingDirtyPaths)
	return agentRunCapabilities{sliceExecutor: executor, pullRequestBodyGenerator: executor, reviewCreator: executor}
}
