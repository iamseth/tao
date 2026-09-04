package pi

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/iamseth/tao/internal/agent/jsonmap"
	"github.com/iamseth/tao/internal/agent/lifecycle"
	"github.com/iamseth/tao/internal/agent/logrecord"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
)

type Client struct {
	ProcessStarter ProcessStarter
	Log            io.Writer
}

type Request struct {
	RepoRoot             string
	Prompt               string
	NoProgressToolLimit  int
	VerificationCommands []string
	SessionInfoMode      SessionInfoMode
}

type Result struct {
	Output           string
	FinalText        string
	SessionID        string
	State            map[string]any
	Stats            map[string]any
	Metrics          agentmetrics.Metrics
	SessionInfoError error
	PromptAcceptance lifecycle.PromptAcceptance
}

type SessionInfoMode int

const (
	SessionInfoNone SessionInfoMode = iota
	SessionInfoBestEffort
)

const (
	DefaultNoProgressToolLimit = 75
	readinessTimeout           = 10 * time.Second
	readinessStateID           = "tao-readiness-state"
	readinessModelsID          = "tao-readiness-models"
	promptID                   = "tao-prompt"
)

// CheckReadiness starts a disposable RPC process, verifies that Pi selected a
// locally available model, and stops it without sending a prompt. Callers use
// this to prove startup separately from an attributed model request.
func (c Client) CheckReadiness(ctx context.Context, repoRoot string) error {
	starter := c.ProcessStarter
	if starter == nil {
		starter = DefaultProcessStarter
	}
	proc, err := starter(ctx, repoRoot, "pi", []string{"--mode", "rpc", "--no-session"})
	if err != nil {
		return err
	}
	session := newSession(proc, c.Log, 0, nil)
	readyCtx, cancelReady := context.WithTimeout(ctx, readinessTimeout)
	err = session.verifyReadiness(readyCtx)
	cancelReady()
	if err != nil {
		return session.abort(fmt.Errorf("pi rpc readiness: %w", err))
	}
	// Readiness never owns a model request or a persistent session. Stopping the
	// disposable process also lets confinement owners remove its private view.
	return session.abort(nil)
}

func (c Client) RunAgentSession(ctx context.Context, request Request) (Result, error) {
	result := Result{PromptAcceptance: lifecycle.PromptAcceptanceUnknown}
	starter := c.ProcessStarter
	if starter == nil {
		starter = DefaultProcessStarter
	}
	proc, err := starter(ctx, request.RepoRoot, "pi", []string{"--mode", "rpc", "--no-session"})
	if err != nil {
		result.PromptAcceptance = lifecycle.PromptAcceptanceNotTransmitted
		return result, err
	}
	session := newSession(proc, c.Log, request.NoProgressToolLimit, request.VerificationCommands)
	defer session.close()

	readyCtx, cancelReady := context.WithTimeout(ctx, readinessTimeout)
	err = session.verifyReadiness(readyCtx)
	cancelReady()
	if err != nil {
		result.PromptAcceptance = lifecycle.PromptAcceptanceNotTransmitted
		return result, session.abort(fmt.Errorf("pi rpc readiness: %w", err))
	}

	// Once prompt delivery is attempted, every outcome is ambiguous until the
	// matching structured response explicitly accepts or rejects it.
	attempted, err := session.sendPrompt(ctx, command{ID: promptID, Type: "prompt", Message: request.Prompt})
	if err != nil {
		if !attempted {
			result.PromptAcceptance = lifecycle.PromptAcceptanceNotTransmitted
		}
		return result, session.abort(err)
	}
	acceptance, err := session.waitForPromptResponse(ctx, promptID)
	result.PromptAcceptance = acceptance
	if err != nil {
		partial := session.queuedResult()
		partial.PromptAcceptance = acceptance
		return partial, session.abort(err)
	}

	agentResult, err := session.waitForAgentEnd(ctx)
	agentResult.PromptAcceptance = result.PromptAcceptance
	if err != nil {
		if session.log != nil {
			_ = logrecord.Write(session.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: "tao pi: agent session ended with an error; stopping the RPC process..."})
		}
		return agentResult, session.abort(err)
	}
	result = agentResult
	if request.SessionInfoMode != SessionInfoBestEffort {
		return result, nil
	}
	result = session.collectSessionInfoBestEffort(ctx, result)
	result.Metrics = parseSessionMetrics(result.State, result.Stats)
	if result.Metrics.SessionID == "" {
		result.Metrics.SessionID = result.SessionID
	}
	return result, nil
}

func (s *session) collectSessionInfoBestEffort(ctx context.Context, result Result) Result {
	state, err := s.requestMap(ctx, command{ID: "2", Type: "get_state", SessionID: result.SessionID}, "state")
	if err != nil {
		result.SessionInfoError = err
		return result
	}
	result.State = state
	if result.SessionID == "" {
		result.SessionID = jsonmap.FirstString(state, "session_id", "sessionId")
	}

	stats, err := s.requestMap(ctx, command{ID: "3", Type: "get_session_stats", SessionID: result.SessionID}, "session_stats")
	if err != nil {
		result.SessionInfoError = err
		return result
	}
	result.Stats = stats
	return result
}
