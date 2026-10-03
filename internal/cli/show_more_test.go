package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/theme"
	planview "github.com/iamseth/tao/internal/view"
)

func TestShowPlanOwnedBlocker(t *testing.T) {
	for _, latestOwned := range []bool{true, false} {
		detail := &plan.PlanDetail{
			State:  plan.State{Status: plan.StatusBlocked, Plan: plan.PlanState{ID: "plan-a"}},
			Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusBlocked, BlockerNote: "lint failed"}}},
			Events: []plan.Event{{Type: plan.EventTypeSliceBlocked, SliceID: "001-a", BlockerClassification: plan.BlockerClassificationPlanOwned, Command: "go vet ./internal/run", Paths: []string{"internal/run/a.go", "internal/run/b.go"}}},
		}
		if !latestOwned {
			detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeSliceBlocked, SliceID: "001-a", Reason: "external blocker"})
		}
		// Another slice's block must not hide this slice's evidence.
		detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeSliceBlocked, SliceID: "002-b"})
		var out bytes.Buffer
		if err := (App{}).renderPlanDetail(&out, planview.Plan{Detail: detail, Derived: plan.Derive(detail, time.Time{})}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Classification: plan_owned (go vet ./internal/run)", "Paths: internal/run/a.go, internal/run/b.go"} {
			if strings.Contains(out.String(), want) != latestOwned {
				t.Fatalf("latestOwned=%v: unexpected rendering for %q:\n%s", latestOwned, want, out.String())
			}
		}
	}
}

func TestRenderPlanDetailFinalVerificationEvents(t *testing.T) {
	now := time.Date(2026, 8, 14, 3, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, result, reason, suffix string
	}{
		{"failed", "failed", "--- FAIL: TestChangesRunExitCancelsLoader (0.01s)", ": first failing TestChangesRunExitCancelsLoader"},
		{"historical", "failed", "ok example/pkg 0.01s coverage: 90%", ""},
		{"passed", "passed", "--- FAIL: TestIgnored (0.01s)", ""},
		{"skipped", "skipped", "--- FAIL: TestIgnored (0.01s)", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := "Final verification " + tc.result + " in /workspace"
			detail := &plan.PlanDetail{
				State:  plan.State{Plan: plan.PlanState{ID: "plan-a"}},
				Events: []plan.Event{{Type: plan.EventTypeFinalVerification, Timestamp: now, Result: tc.result, Reason: tc.reason, Message: message}},
			}
			var out bytes.Buffer
			if err := (App{}).renderPlanDetail(&out, planview.Plan{Detail: detail, Derived: plan.Derive(detail, now)}); err != nil {
				t.Fatal(err)
			}
			_, recent, ok := strings.Cut(out.String(), "Recent Events:\n")
			want := "- " + now.Format(time.RFC3339) + " final_verification " + message + tc.suffix + "\n"
			if !ok || recent != want {
				t.Fatalf("Recent Events = %q; want %q", recent, want)
			}
		})
	}
}

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
	if err := (App{}).renderPlanDetail(&plain, loaded); err != nil {
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
	if err := (App{}).renderPlanDetail(terminal, loaded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.String(), (App{}).outputPalette(terminal).Paint(theme.RoleWarn, "blocked (waiting for outside action)")) || stripANSI(terminal.String()) != text {
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
	if err := (App{}).renderPlanDetail(&out, loaded); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Plan A", "ID: plan-a", "Warnings:", "old artifact", "Planning Session:", "Tokens: 100", "Model: model", "Slices:", "001-a", "Recent Events:", "custom done"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in rendered detail:\n%s", want, text)
		}
	}
}
