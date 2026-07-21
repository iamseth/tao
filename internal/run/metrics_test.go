package run

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
)

func TestCollectAgentMetricsConvertsNeutralMetrics(t *testing.T) {
	state := plan.State{}
	state.Plan.ID = "plan-a"
	result := agent.Metrics{
		SessionID:         "session-pi",
		ProviderID:        "pi-provider",
		ModelID:           "pi-model",
		InputTokens:       10,
		OutputTokens:      5,
		TotalTokens:       15,
		TotalMessages:     3,
		AssistantMessages: 2,
		ToolCalls:         4,
	}

	metrics := collectAgentMetrics(state, "001-a", "pi", "Captured Pi agent metrics", &result, nil)
	if metrics.planID != "plan-a" || metrics.sliceID != "001-a" || metrics.eventType != plan.EventTypeAgentMetrics {
		t.Fatalf("unexpected ids/type: %#v", metrics)
	}
	got := metrics.metrics
	if got.Agent != "pi" || got.SessionID != "session-pi" || got.ProviderID != "pi-provider" || got.ModelID != "pi-model" || got.Status != plan.StatusCompleted {
		t.Fatalf("unexpected identity/status: %#v", got)
	}
	if got.InputTokens != 10 || got.OutputTokens != 5 || got.TotalTokens != 15 || got.TotalMessages != 3 || got.AssistantMessages != 2 || got.ToolCalls != 4 {
		t.Fatalf("unexpected totals: %#v", got)
	}
}

func TestCollectAgentMetricsMarksFailure(t *testing.T) {
	metrics := collectAgentMetrics(plan.State{}, "001-a", "pi", "Captured Pi agent metrics", nil, errors.New("boom"))
	if metrics.metrics.Status != "failed" || metrics.metrics.Result != "failed" {
		t.Fatalf("expected failed status/result, got %#v", metrics.metrics)
	}
}

func TestCollectedAgentMetricsEventDefaultsToGenericAgentMetrics(t *testing.T) {
	createdAt := unixMillis(2000)
	event := (collectedAgentMetrics{planID: "plan-a", sliceID: "001-a", metrics: plan.AgentMetrics{Agent: "pi"}}).event(createdAt)
	if event.Type != plan.EventTypeAgentMetrics || event.Message != "Captured agent metrics" || event.Agent != "pi" || event.Metrics == nil {
		t.Fatalf("unexpected event: %#v", event)
	}
	if !event.Timestamp.Equal(createdAt) {
		t.Fatalf("expected timestamp %s, got %s", createdAt, event.Timestamp)
	}
}

func writeMetricsPlan(t *testing.T, repoRoot string, planID string) string { //nolint:unparam // repoRoot kept for test readability and future cases
	t.Helper()
	planDir := filepath.Join(t.TempDir(), planID)
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	state := `{"schema":"tao.plan.state.v1","status":"in_progress","created_at":"2026-05-01T00:00:00Z","updated_at":"2026-05-01T00:00:00Z","repo":{"name":"tao","root":"` + repoRoot + `","branch":"feature"},"plan":{"id":"` + planID + `","title":"Plan","current_slice":null,"completed_slices":[],"pending_slices":["001-a"],"timing":{"started_at":null,"completed_at":null,"last_activity_at":null}},"global_invariants":[],"open_questions":[]}`
	if err := os.WriteFile(filepath.Join(planDir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return planDir
}

func readMetricsText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // test reads a path from a t.TempDir-derived location
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func unixMillis(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}
