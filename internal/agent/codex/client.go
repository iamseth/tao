package codex

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
	Output         string
	FinalText      string
	SessionID      string
	ProviderID     string
	ModelID        string
	Usage          TokenUsage
	UsageObserved  bool
	Metrics        agentmetrics.Metrics
	MetricsWarning string
}

func (c Client) RunAgentSession(ctx context.Context, request Request) (Result, error) {
	mode := request.PermissionMode
	if mode == "" {
		mode = perm.PermissionModeAuto
	}
	if !perm.Valid(mode) {
		return Result{}, fmt.Errorf("unsupported codex permission mode %q", mode)
	}
	args := []string{"exec", "--json"}
	switch mode {
	case perm.PermissionModeAuto:
		args = append(args, "--sandbox", "workspace-write", "--ask-for-approval", "never")
	case perm.PermissionModePlan:
		args = append(args, "--sandbox", "read-only", "--ask-for-approval", "never")
	case perm.PermissionModeBypassPermissions:
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, "-")
	result, err := streamjson.RunSession(ctx, streamjson.SessionConfig[Result]{
		Starter:    c.ProcessStarter,
		RepoRoot:   request.RepoRoot,
		Executable: "codex",
		Args:       args,
		Prompt:     request.Prompt,
		StreamKind: "json",
		Log:        c.Log,
		Handle:     c.handleEvent,
	})
	if !streamjson.IsPreReadError(err) {
		result.Metrics = parseSessionMetrics(result)
		result.MetricsWarning = metricsWarning(result)
	}
	return result, err
}
