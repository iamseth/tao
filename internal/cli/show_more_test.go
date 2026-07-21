package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	planview "github.com/iamseth/tao/internal/view"
)

func TestRenderPlanDetailIncludesWarningsPlanningMetricsSlicesAndEvents(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	planningStarted := started.Add(-5 * time.Minute)
	completed := now.Add(-time.Minute)
	duration := int64(60)
	detail := &plan.PlanDetail{
		State:  plan.State{CreatedAt: started, Status: plan.StatusCompleted, Repo: plan.Repo{Name: "repo", Branch: "feature"}, Plan: plan.PlanState{ID: "plan-a", Title: "Plan A", CompletedSlices: []string{"001-a"}, Timing: plan.PlanTiming{StartedAt: &started, CompletedAt: &completed, LastActivityAt: &completed}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Title: "Slice A", Status: plan.StatusCompleted, Goal: strings.Repeat("goal ", 30), Timing: plan.SliceTiming{StartedAt: &started, CompletedAt: &completed, DurationSeconds: &duration}}}},
		Events: []plan.Event{
			{Type: plan.EventTypeAgentMetrics, Timestamp: completed, Message: "metrics", Metrics: &plan.AgentMetrics{Agent: "pi", SessionID: "s", TotalTokens: 10}},
			{Type: "custom", Timestamp: completed, Message: "done"},
		},
		PlanningSession: plan.PlanningSessionArtifacts{Stats: &plan.PlanningSessionStats{SessionID: "planning", ProviderID: "provider", ModelID: "model", PlanningStartedAt: &planningStarted, TotalTokens: 100, TotalMessages: 4, Cost: 0.25}},
		Warnings:        []string{"old artifact"},
	}
	var out bytes.Buffer
	loaded := planview.Plan{Detail: detail, Derived: plan.Derive(detail, now), Now: now}
	if err := renderPlanDetail(&out, loaded); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Plan A", "ID: plan-a", "Warnings:", "old artifact", "Planning Session:", "Tokens: 100", "Model: model", "Slices:", "001-a", "Recent Events:", "custom done"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in rendered detail:\n%s", want, text)
		}
	}
}
