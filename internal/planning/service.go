package planning

import (
	"io"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type ServiceOptions struct {
	EventAppender  plan.EventAppender
	Agent          runtimeconfig.AgentKind
	Model          string
	Effort         string
	ProcessStarter agent.ProcessStarter
	Log            io.Writer
}

type Service struct {
	EventAppender  plan.EventAppender
	Repo           SliceRepository
	Runtime        agent.Runtime
	AgentKind      runtimeconfig.AgentKind
	Model          string
	Effort         string
	ProcessStarter agent.ProcessStarter
	Log            io.Writer
}

func NewService(repo SliceRepository, runtime agent.Runtime, options ServiceOptions) *Service {
	return &Service{Repo: repo, Runtime: runtime, AgentKind: options.Agent, Model: options.Model, Effort: options.Effort, ProcessStarter: options.ProcessStarter, Log: options.Log, EventAppender: options.EventAppender}
}

// runtimeFor prefers an injected Runtime, then the requested kind, then the
// service kind (empty resolves to Pi).
func (s *Service) runtimeFor(kind runtimeconfig.AgentKind) agent.Runtime {
	if s.Runtime != nil {
		return s.Runtime
	}
	if kind == "" {
		kind = s.AgentKind
	}
	descriptor, _ := agent.Lookup(kind)
	return descriptor.NewRuntime(agent.RuntimeDeps{ProcessStarter: s.ProcessStarter})
}
