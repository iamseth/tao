package claude

import (
	"context"
	"fmt"
	"io"

	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agent/perm"
	"github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/agent/streamjson"
)

type Client struct {
	ProcessStarter process.ProcessStarter
	Log            io.Writer
}

type Request struct {
	RepoRoot       string
	Prompt         string
	PermissionMode perm.PermissionMode
}

type Result struct {
	Output    string
	FinalText string
	SessionID string
	Model     string
	Usage     map[string]any
	CostUSD   float64
	Events    map[string]any
	// Metrics is the neutral view of session statistics parsed from the stream
	// output, so the run layer never reaches into Usage with Claude's
	// stream-json vocabulary.
	Metrics agentmetrics.Metrics
	// MetricsWarning explains why typed metrics could not be captured from the
	// stream output, or is empty when Metrics is usable.
	MetricsWarning string
}

func (c Client) RunAgentSession(ctx context.Context, request Request) (Result, error) {
	mode := request.PermissionMode
	if mode == "" {
		mode = perm.PermissionModeAuto
	}
	if !perm.Valid(mode) {
		return Result{}, fmt.Errorf("unsupported claude permission mode %q", mode)
	}
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", string(mode)}
	result, err := streamjson.RunSession(ctx, streamjson.SessionConfig[Result]{
		Starter:    c.ProcessStarter,
		RepoRoot:   request.RepoRoot,
		Executable: "claude",
		Args:       args,
		Prompt:     request.Prompt,
		StreamKind: "stream-json",
		Log:        c.Log,
		Handle:     c.handleEvent,
	})
	if !streamjson.IsPreReadError(err) {
		result.Metrics = parseSessionMetrics(result)
		result.MetricsWarning = metricsWarning(result)
	}
	return result, err
}
