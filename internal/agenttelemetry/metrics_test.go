package agenttelemetry

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/plan"
)

func TestProjectPreservesMeasurements(t *testing.T) {
	for _, label := range []string{"pi", "claude"} {
		t.Run(label, func(t *testing.T) {
			m := &agent.Metrics{
				SessionID: "session", ProviderID: "provider", ModelID: "model",
				InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2, CacheReadTokens: 3,
				CacheWriteTokens: 4, TotalTokens: 24, Cost: 0.5,
				InputTokensPresent: true, OutputTokensPresent: true, ReasoningTokensPresent: true,
				CacheReadTokensPresent: true, CacheWriteTokensPresent: true, TotalTokensPresent: true, CostPresent: true,
				TotalMessages: 6, UserMessages: 1, AssistantMessages: 5, ErroredMessages: 2, ToolCalls: 7,
			}
			got := Project(agentsession.Result{AgentLabel: label, Metrics: m, MetricsAvailability: agentmetrics.Reported}, plan.AgentRoleRework, nil)
			want := plan.AgentMetrics{
				Role: plan.AgentRoleRework, Availability: plan.AgentMetricsReported, Agent: label,
				Status: plan.StatusCompleted, Result: plan.StatusCompleted,
				SessionID: "session", ProviderID: "provider", ModelID: "model",
				InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2, CacheReadTokens: 3,
				CacheWriteTokens: 4, TotalTokens: 24, Cost: 0.5,
				InputTokensPresent: true, OutputTokensPresent: true, ReasoningTokensPresent: true,
				CacheReadTokensPresent: true, CacheWriteTokensPresent: true, TotalTokensPresent: true, CostPresent: true,
				TotalMessages: 6, UserMessages: 1, AssistantMessages: 5, ErroredMessages: 2, ToolCalls: 7,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("projection = %+v, want %+v", got, want)
			}
		})
	}
}

func TestProjectAvailabilityPresenceAndSafeFailure(t *testing.T) {
	for _, tt := range []struct {
		name         string
		metrics      *agent.Metrics
		availability agentmetrics.Availability
		want         plan.AgentMetricsAvailability
	}{
		{"missing", nil, agentmetrics.Unavailable, plan.AgentMetricsUnavailable},
		{"unavailable", &agent.Metrics{SessionID: "session"}, agentmetrics.Unavailable, plan.AgentMetricsUnavailable},
		{"partial zero", &agent.Metrics{OutputTokensPresent: true}, agentmetrics.Partial, plan.AgentMetricsPartial},
		{"reported zero", &agent.Metrics{InputTokensPresent: true, OutputTokensPresent: true, TotalTokensPresent: true, CostPresent: true}, agentmetrics.Reported, plan.AgentMetricsReported},
		{"legacy", &agent.Metrics{OutputTokens: 5}, "", plan.AgentMetricsUnknown},
		{"future", nil, "future", plan.AgentMetricsUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, runErr := range []error{nil, errors.New("private-provider-error"), &agent.SessionTimeoutError{Timeout: time.Minute}} {
				got := Project(agentsession.Result{
					AgentLabel: "pi", Metrics: tt.metrics, MetricsAvailability: tt.availability,
					Output: "private-output", MetricsWarning: "private-warning", MetricsWarningMessage: "private-warning", MetricsMessage: "private-message",
				}, "future-role", runErr)
				if got.Role != plan.AgentRoleUnknown || got.Availability != tt.want || (got.Status == "failed") != (runErr != nil) || got.Result != got.Status {
					t.Fatalf("projection = %+v", got)
				}
				now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.FixedZone("test", 3600))
				event := Event("plan-a", "", now, got)
				if event.Type != plan.EventTypeAgentMetrics || event.PlanID != "plan-a" || event.SliceID != "" || event.Agent != "pi" || event.Timestamp.Location() != time.UTC || !event.Timestamp.Equal(now) {
					t.Fatalf("event = %+v", event)
				}
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "private-") {
					t.Fatalf("unsafe event: %s", encoded)
				}
				var decoded plan.Event
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				wantOutputPresence := tt.metrics != nil && (tt.metrics.OutputTokensPresent || tt.metrics.OutputTokens != 0)
				if decoded.Metrics.OutputTokensPresent != wantOutputPresence || decoded.Metrics.CostPresent != got.CostPresent {
					t.Fatalf("measurement presence lost: %s", encoded)
				}
			}
		})
	}
}
