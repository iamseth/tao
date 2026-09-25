package run

import (
	"errors"
	"testing"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/agenttelemetry"
	"github.com/iamseth/tao/internal/plan"
)

func TestCollectAgentMetricsMapsNeutralMetricsAndMarksFailures(t *testing.T) {
	result := agent.Metrics{
		SessionID:        "stats-session",
		ProviderID:       "stats-provider",
		ModelID:          "stats-model",
		InputTokens:      11,
		OutputTokens:     7,
		ReasoningTokens:  3,
		CacheReadTokens:  2,
		CacheWriteTokens: 5,
		TotalTokens:      28,
		Cost:             1.25,
		ErroredMessages:  1,
	}
	got := agenttelemetry.Project(agentsession.Result{AgentLabel: "pi", Metrics: &result}, plan.AgentRoleExecution, errors.New("boom"))
	if got.SessionID != "stats-session" || got.ProviderID != "stats-provider" || got.ModelID != "stats-model" {
		t.Fatalf("unexpected identity mapping: %#v", got)
	}
	if got.InputTokens != 11 || got.OutputTokens != 7 || got.ReasoningTokens != 3 || got.CacheReadTokens != 2 || got.CacheWriteTokens != 5 || got.TotalTokens != 28 || got.Cost != 1.25 || got.ErroredMessages != 1 {
		t.Fatalf("unexpected numeric mapping: %#v", got)
	}
	if got.Status != "failed" || got.Result != "failed" {
		t.Fatalf("expected failed result, got %#v", got)
	}
}
