package agent

import (
	"context"
	"io"

	claudeagent "github.com/iamseth/tao/internal/agent/claude"
	"github.com/iamseth/tao/internal/agent/logrecord"
	piagent "github.com/iamseth/tao/internal/agent/pi"
	"github.com/iamseth/tao/internal/agent/process"
)

// piRuntime adapts the leaf pi.Client onto the neutral Runtime contract. It maps
// CollectMetrics onto Pi's best-effort session-info mode, passes the
// no-progress tool limit and declared verification commands through, and
// ignores PermissionMode (Pi has no permission policy).
func providerLog(session Session) io.Writer {
	if session.Progress == nil {
		return session.Log
	}
	presentation := logrecord.PresentationWriter(session.Progress)
	if session.Log == nil {
		return presentation
	}
	return io.MultiWriter(session.Log, presentation)
}

type piRuntime struct {
	starter piagent.ProcessStarter
}

func (r piRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	mode := piagent.SessionInfoNone
	if session.CollectMetrics {
		mode = piagent.SessionInfoBestEffort
	}
	client := piagent.Client{ProcessStarter: r.starter, Log: providerLog(session)}
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
	client := claudeagent.Client{ProcessStarter: r.starter, Log: providerLog(session)}
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
