package planning

import (
	"io"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type ServiceOptions struct {
	Agent          runtimeconfig.AgentKind
	ProcessStarter agent.ProcessStarter
	Log            io.Writer
}

type Service struct {
	Repo           SliceRepository
	Runtime        agent.Runtime
	AgentKind      runtimeconfig.AgentKind
	ProcessStarter agent.ProcessStarter
	Log            io.Writer
}

func NewService(repo SliceRepository, runtime agent.Runtime, options ServiceOptions) *Service {
	return &Service{Repo: repo, Runtime: runtime, AgentKind: options.Agent, ProcessStarter: options.ProcessStarter, Log: options.Log}
}

// runtime returns the agent.Runtime used for synchronous plan generation. An
// injected Runtime takes precedence; otherwise the registry builds the runtime
// for the configured agent kind (empty resolves to Pi).
func (s *Service) runtime() agent.Runtime {
	if s.Runtime != nil {
		return s.Runtime
	}
	descriptor, _ := agent.Lookup(s.AgentKind)
	return descriptor.NewRuntime(agent.RuntimeDeps{ProcessStarter: s.ProcessStarter})
}
