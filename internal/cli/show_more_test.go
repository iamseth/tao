package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	planview "github.com/iamseth/tao/internal/view"
)

func TestRenderPlanDetailExplainsBlockedSlicesAndEvents(t *testing.T) {
	now := time.Date(2026, 8, 14, 3, 30, 0, 0, time.UTC)
	reason := "Waiting for the infrastructure team\n to restore " + strings.Repeat("service ", 30)
	detail := &plan.PlanDetail{
		State: plan.State{Status: plan.StatusBlocked, Plan: plan.PlanState{ID: "plan-a", Title: "Plan A"}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "001-blocked", Title: "Blocked", Status: plan.StatusBlocked, Goal: "Complete the integration", BlockerNote: reason},
			{ID: "002-legacy", Title: "Legacy", Status: plan.StatusBlocked, Goal: "Read an old plan"},
		}},
		Events: []plan.Event{
			{Type: "custom", Timestamp: now.Add(-time.Minute), Message: "unchanged message"},
			{Type: plan.EventTypeSliceBlocked, Timestamp: now, Message: "Slice blocked", Reason: reason},
		},
	}
	loaded := planview.Plan{Detail: detail, Derived: plan.Derive(detail, now), Now: now}

	var plain bytes.Buffer
	if err := renderPlanDetail(&plain, loaded); err != nil {
		t.Fatal(err)
	}
	text := plain.String()
	for _, want := range []string{
		"Status: blocked (waiting for outside action)",
		"Blocker Reason: Waiting for the infrastructure team to restore service",
		"Blocker Reason: No blocker reason was recorded.",
		"custom unchanged message",
		"slice_blocked Waiting for the infrastructure team to restore service",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in blocked detail:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[") || strings.Contains(text, "slice_blocked Slice blocked") {
		t.Fatalf("plain blocked detail contained ANSI or generic blocked event text:\n%q", text)
	}
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, "slice_blocked") && len([]rune(line)) > 150 {
			t.Fatalf("blocked event excerpt was not concise: %q", line)
		}
	}

	terminal := &testTerminalBuffer{}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if err := renderPlanDetail(terminal, loaded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.String(), "\x1b[33mblocked (waiting for outside action)\x1b[0m") || stripANSI(terminal.String()) != text {
		t.Fatalf("terminal blocked status was not amber-equivalent to plain output:\n%q", terminal.String())
	}
}

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
