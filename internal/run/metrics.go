package run

import (
	"context"

	"github.com/iamseth/tao/internal/plan"
)

func publishAgentMetrics(ctx context.Context, metrics plan.AgentMetrics) {
	active := headerFromContext(ctx)
	if active == nil {
		return
	}
	active.mu.Lock()
	if metrics.SessionID != "" && !active.seenSessions[metrics.SessionID] {
		active.seenSessions[metrics.SessionID] = true
		active.state.AgentSessionCount++
	}
	if metrics.TotalTokens > 0 {
		active.state.TotalTokens += metrics.TotalTokens
	}
	if metrics.Cost > 0 {
		active.state.Cost += metrics.Cost
	}
	reporter := active.reporter
	state := active.state.Clone()
	active.mu.Unlock()
	ReportHeader(reporter, state)
}
