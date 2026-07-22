package agent

import (
	"context"
	"fmt"

	"github.com/iamseth/tao/internal/runtimeconfig"

	claudeagent "github.com/iamseth/tao/internal/agent/claude"
	codexagent "github.com/iamseth/tao/internal/agent/codex"
	opencodeagent "github.com/iamseth/tao/internal/agent/opencode"
	piagent "github.com/iamseth/tao/internal/agent/pi"
	"github.com/iamseth/tao/internal/agent/process"
)

// piRuntime adapts the leaf pi.Client onto the neutral Runtime contract. It maps
// CollectMetrics onto Pi's best-effort session-info mode, passes the
// no-progress tool limit and declared verification commands through, and
// ignores PermissionMode (Pi has no permission policy).
// SessionAdapter is a narrow provider-neutral entry point for non-run
// operations such as merge-batch repair. It uses the same registry adapters and
// timeout wrapper as ordinary run sessions without importing run orchestration.
type SessionAdapter struct {
	runtime    Runtime
	descriptor Descriptor
}

// NewSessionAdapter resolves a configured provider and transport dependency.
func NewSessionAdapter(kind runtimeconfig.AgentKind, deps RuntimeDeps) (SessionAdapter, error) {
	descriptor, ok := Lookup(kind)
	if !ok {
		return SessionAdapter{}, fmt.Errorf("unsupported agent %q", kind)
	}
	return SessionAdapter{runtime: WithSessionTimeout(descriptor.NewRuntime(deps)), descriptor: descriptor}, nil
}

// Run executes one neutral session. Provider-specific metrics warnings remain
// best-effort data on the returned result.
func (a SessionAdapter) Run(ctx context.Context, session Session) (SessionResult, error) {
	return a.runtime.RunSession(ctx, session)
}

func (a SessionAdapter) Descriptor() Descriptor { return a.descriptor }

type piRuntime struct {
	starter piagent.ProcessStarter
}

func (r piRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	mode := piagent.SessionInfoNone
	if session.CollectMetrics {
		mode = piagent.SessionInfoBestEffort
	}
	client := piagent.Client{ProcessStarter: r.starter, Log: session.Log}
	result, err := client.RunAgentSession(ctx, piagent.Request{
		RepoRoot:             session.RepoRoot,
		Prompt:               session.Prompt,
		NoProgressToolLimit:  session.NoProgressToolLimit,
		VerificationCommands: session.VerificationCommands,
		SessionInfoMode:      mode,
	})
	out := SessionResult{Output: result.Output, FinalText: result.FinalText}
	if session.CollectMetrics {
		out.Metrics = &result.Metrics
		if result.SessionInfoError != nil {
			out.MetricsWarning = result.SessionInfoError.Error()
		}
	}
	return out, err
}

// claudeRuntime adapts the leaf claude.Client onto the neutral Runtime contract.
// It passes PermissionMode through to Claude and ignores the no-progress tool
// limit (Claude has no watchdog), matching current behavior.
type claudeRuntime struct {
	starter process.ProcessStarter
}

func (r claudeRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	client := claudeagent.Client{ProcessStarter: r.starter, Log: session.Log}
	result, err := client.RunAgentSession(ctx, claudeagent.Request{
		RepoRoot:       session.RepoRoot,
		Prompt:         session.Prompt,
		PermissionMode: session.PermissionMode,
	})
	out := SessionResult{Output: result.Output, FinalText: result.FinalText}
	if session.CollectMetrics {
		out.Metrics = &result.Metrics
		out.MetricsWarning = result.MetricsWarning
	}
	return out, err
}

// openCodeRuntime adapts the leaf opencode.Client onto the neutral Runtime
// contract. Like claudeRuntime it passes PermissionMode through to OpenCode
// (only bypassPermissions adds an argv flag) and ignores the no-progress tool
// limit, since OpenCode has no watchdog. Metrics are best-effort: parse warnings
// surface through MetricsWarning and never fail the run.
type openCodeRuntime struct {
	starter process.ProcessStarter
}

func (r openCodeRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	client := opencodeagent.Client{ProcessStarter: r.starter, Log: session.Log}
	result, err := client.RunAgentSession(ctx, opencodeagent.Request{
		RepoRoot:       session.RepoRoot,
		Prompt:         session.Prompt,
		PermissionMode: session.PermissionMode,
	})
	out := SessionResult{Output: result.Output, FinalText: result.FinalText}
	if session.CollectMetrics {
		out.Metrics = &result.Metrics
		out.MetricsWarning = result.MetricsWarning
	}
	return out, err
}

// codexRuntime adapts the leaf codex.Client onto the neutral Runtime contract.
// It passes PermissionMode through to Codex and ignores the no-progress tool
// limit, since Codex has no watchdog. Metrics are best-effort: parse warnings
// surface through MetricsWarning and never fail the run.
type codexRuntime struct {
	starter process.ProcessStarter
}

func (r codexRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	client := codexagent.Client{ProcessStarter: r.starter, Log: session.Log}
	result, err := client.RunAgentSession(ctx, codexagent.Request{
		RepoRoot:       session.RepoRoot,
		Prompt:         session.Prompt,
		PermissionMode: session.PermissionMode,
	})
	out := SessionResult{Output: result.Output, FinalText: result.FinalText}
	if session.CollectMetrics {
		out.Metrics = &result.Metrics
		out.MetricsWarning = result.MetricsWarning
	}
	return out, err
}
