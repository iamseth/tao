package merge

import (
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
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

func TestNewBatchAgentMetricsCopiesProviderNeutralValues(t *testing.T) {
	got := newBatchAgentMetrics(&agent.Metrics{SessionID: "session-a", OutputTokens: 42, Cost: 0.5, ToolCalls: 3})
	if got.SessionID != "session-a" || got.OutputTokens != 42 || got.Cost != 0.5 || got.ToolCalls != 3 {
		t.Fatalf("metrics = %#v", got)
	}
}
