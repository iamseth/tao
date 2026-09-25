package merge

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agentsession"
)

func TestBatchAgentEventValidatesBoundedVersionedRecords(t *testing.T) {
	duration := int64(30)
	event := BatchAgentEvent{
		Schema: BatchAgentEventSchema, Type: BatchAgentEventTypeTimeout, BatchID: "batch-a",
		Timestamp: time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC), Operation: BatchAgentOperationAggregateReview,
		Attempt: 2, Agent: "pi", Outcome: BatchAgentOutcomeTimedOut, TimeoutDurationSeconds: &duration,
	}
	if err := event.validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	event.BatchID = "../plan"
	if err := event.validate(); err == nil {
		t.Fatal("path-shaped batch ID accepted")
	}
}

func TestBatchAgentMetricsRoundTripPresence(t *testing.T) {
	for _, availability := range []agentmetrics.Availability{"", agentmetrics.Reported, agentmetrics.Partial, agentmetrics.Unavailable} {
		t.Run(string(availability), func(t *testing.T) {
			result := agentsession.Result{MetricsAvailability: availability}
			if availability == agentmetrics.Reported || availability == agentmetrics.Partial {
				result.Metrics = &agent.Metrics{
					InputTokensPresent: true, OutputTokensPresent: true, ReasoningTokensPresent: true,
					CacheReadTokensPresent: true, CacheWriteTokensPresent: true, TotalTokensPresent: true,
					CostPresent: availability == agentmetrics.Reported,
				}
			}
			want := newBatchAgentMetrics(result)
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var got BatchAgentMetrics
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(&got, want) {
				t.Fatalf("round trip = %#v, want %#v", got, want)
			}
			if got.InputTokensPresent != (availability == agentmetrics.Reported || availability == agentmetrics.Partial) || got.InputTokens != 0 || got.CostPresent != (availability == agentmetrics.Reported) {
				t.Fatalf("zero presence lost: %#v", got)
			}
		})
	}
}

func TestNewBatchAgentMetricsCopiesProviderNeutralValues(t *testing.T) {
	got := newBatchAgentMetrics(agentsession.Result{Metrics: &agent.Metrics{SessionID: "session-a", OutputTokens: 42, Cost: 0.5, ToolCalls: 3}})
	if got.SessionID != "session-a" || got.OutputTokens != 42 || got.Cost != 0.5 || got.ToolCalls != 3 {
		t.Fatalf("metrics = %#v", got)
	}
}
