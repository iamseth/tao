package pi

import (
	"context"
	"io"

	"github.com/iamseth/tao/internal/agent/jsonmap"
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
}

type SessionInfoMode int

const (
	SessionInfoNone SessionInfoMode = iota
	SessionInfoBestEffort
)

const DefaultNoProgressToolLimit = 75

func (c Client) RunAgentSession(ctx context.Context, request Request) (Result, error) {
	starter := c.ProcessStarter
	if starter == nil {
		starter = DefaultProcessStarter
	}
	proc, err := starter(ctx, request.RepoRoot, "pi", []string{"--mode", "rpc"})
	if err != nil {
		return Result{}, err
	}
	session := newSession(proc, c.Log, request.NoProgressToolLimit, request.VerificationCommands)
	defer session.close()

	if err := session.send(ctx, command{ID: "1", Type: "prompt", Message: request.Prompt}); err != nil {
		return Result{}, err
	}
	result, err := session.waitForAgentEnd(ctx)
	if err != nil {
		if session.log != nil {
			_, _ = io.WriteString(session.log, "tao pi: agent session ended with an error; stopping the RPC process...\n")
		}
		return result, session.abort(err)
	}
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
