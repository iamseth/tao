package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	planview "github.com/iamseth/tao/internal/view"
)

func TestShowPrintsElapsedAndSliceRows(t *testing.T) {
	started := time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC)
	completed := started.Add(90 * time.Second)
	planCreated := started.Add(2*time.Minute + 5*time.Second)
	var out bytes.Buffer
	repo := fakeRepository{details: map[string]*plan.PlanDetail{
		"20260427-1810": {
			State: plan.State{
				Status:    plan.StatusCompleted,
				CreatedAt: planCreated,
				Repo:      plan.Repo{Name: "repo", Branch: "main"},
				Plan: plan.PlanState{
					ID:     "20260427-1810-example",
					Title:  "Example Plan",
					Timing: plan.PlanTiming{StartedAt: &started},
				},
			},
			Slices: plan.SlicesFile{Slices: []plan.Slice{{
				ID:     "001-example",
				Title:  "Example slice",
				Status: plan.StatusCompleted,
				Goal:   "Render the planned slice work in show output while keeping long summary text aligned under a stable summary indentation for terminal readability.",
				Timing: plan.SliceTiming{StartedAt: &started, CompletedAt: &completed},
			}, {
				ID:     "002-empty-goal",
				Title:  "Empty goal slice",
				Status: plan.StatusPending,
			}}},
			PlanningSession: plan.PlanningSessionArtifacts{Stats: &plan.PlanningSessionStats{
				SessionID:         "planning-session-123",
				PlanningStartedAt: &started,
				ProviderID:        "test-provider",
				ModelID:           "gpt-5.5",
				TotalTokens:       12345,
				TotalMessages:     17,
				Cost:              0.0567,
			}},
		},
	}}

	err := App{Out: &out, Err: &out}.show(context.Background(), repo, []string{"20260427-1810"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Example Plan", "Elapsed: 1m30s", "Planning Session:", "Duration: 2m05s", "Tokens: 12345", "Messages: 17", "Model: gpt-5.5", "Provider: test-provider", "Cost: $0.0567", "Session ID: planning-session-123", "001-example", "Example slice", "Summary:   Render the planned slice work in show output while keeping long summary", "             text aligned under a stable summary indentation for terminal", "             readability.", "002-empty-goal  Empty goal slice"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
	foundEmptyGoalPlaceholder := false
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, "Summary:") && strings.HasSuffix(line, " -") {
			foundEmptyGoalPlaceholder = true
		}
	}
	if !foundEmptyGoalPlaceholder {
		t.Fatalf("expected empty slice goal placeholder in output:\n%s", text)
	}
}

func TestRenderPlanDetailUsesLifecycleStatusProjection(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusReviewed,
			Plan: plan.PlanState{
				ID:          "plan-a",
				Title:       "Plan A",
				PullRequest: &plan.PullRequest{Number: 42, HeadSHA: "head123"},
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"})

	var out bytes.Buffer
	if err := renderPlanDetail(&out, planview.Plan{Detail: detail, Derived: plan.Derive(detail, now), Now: now}); err != nil {
		t.Fatal(err)
	}
	text := stripANSI(out.String())
	if !strings.Contains(text, "Status: completed\n") || strings.Contains(text, "Status: reviewed\n") {
		t.Fatalf("detail did not use projected lifecycle status:\n%s", text)
	}
	if detail.State.Status != plan.StatusReviewed {
		t.Fatalf("render mutated persisted status to %q", detail.State.Status)
	}
}

func TestRenderPlanDetailUsesHumanTimestamps(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	started := now.Add(-2 * time.Hour)
	completed := now.Add(-30 * time.Minute)
	lastActivity := now.Add(-25 * time.Hour)
	var out bytes.Buffer
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusCompleted,
			Repo:   plan.Repo{Name: "repo", Branch: "main"},
			Plan: plan.PlanState{
				ID:    "20260525-1000-example",
				Title: "Example Plan",
				Timing: plan.PlanTiming{
					StartedAt:      &started,
					CompletedAt:    &completed,
					LastActivityAt: &lastActivity,
				},
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID:     "001-example",
			Title:  "Example slice",
			Status: plan.StatusCompleted,
			Goal:   "Example goal",
			Timing: plan.SliceTiming{StartedAt: &started, CompletedAt: &completed},
		}}},
	}

	if err := renderPlanDetail(&out, planview.Plan{Detail: detail, Derived: plan.DerivedPlan{Elapsed: 90 * time.Minute}, Now: now}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Started: 2h", "Completed: 30m", "Last Activity: 2026-05-24", "Started:   2h", "Completed: 30m"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "10:00:00") {
		t.Fatalf("expected compact timestamps, got:\n%s", text)
	}
}

func TestShowPlanningSessionUnavailableStates(t *testing.T) {
	planCreated := time.Date(2026, 4, 27, 18, 2, 5, 0, time.UTC)
	planningStarted := planCreated.Add(-2*time.Minute - 5*time.Second)
	repo := fakeRepository{details: map[string]*plan.PlanDetail{
		"missing": {State: plan.State{Status: plan.StatusPlanned, CreatedAt: planCreated, Plan: plan.PlanState{ID: "missing", Title: "Missing Stats"}}},
		"suspect": {
			State: plan.State{Status: plan.StatusPlanned, CreatedAt: planCreated, Plan: plan.PlanState{ID: "suspect", Title: "Suspect Stats"}},
			PlanningSession: plan.PlanningSessionArtifacts{Stats: &plan.PlanningSessionStats{
				PlanningStartedAt:    &planningStarted,
				CaptureSuspect:       true,
				CaptureSuspectReason: "stale planning session",
				TotalTokens:          99999,
				TotalMessages:        88,
			}},
		},
	}}

	var missingOut bytes.Buffer
	if err := (App{Out: &missingOut, Err: &missingOut}).show(context.Background(), repo, []string{"missing"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(missingOut.String(), "Planning Session:") {
		t.Fatalf("expected missing planning stats to stay quiet, got:\n%s", missingOut.String())
	}

	var suspectOut bytes.Buffer
	if err := (App{Out: &suspectOut, Err: &suspectOut}).show(context.Background(), repo, []string{"suspect"}); err != nil {
		t.Fatal(err)
	}
	text := suspectOut.String()
	if !strings.Contains(text, "Planning Session:") || !strings.Contains(text, "Unavailable: stale planning session") {
		t.Fatalf("expected suspect unavailable state, got:\n%s", text)
	}
	for _, hidden := range []string{"99999", "88"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("expected suspect metrics to be hidden, got:\n%s", text)
		}
	}
}

func TestShowPrintsAgentBudgetWarnings(t *testing.T) {
	started := time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	repo := fakeRepository{details: map[string]*plan.PlanDetail{
		"20260427-1810": {
			State: plan.State{Status: plan.StatusCompleted, Plan: plan.PlanState{ID: "20260427-1810-example", Title: "Example Plan"}},
			Events: []plan.Event{{
				Type:      plan.EventTypeAgentMetrics,
				Timestamp: started,
				PlanID:    "20260427-1810-example",
				SliceID:   "001-example",
				Metrics: &plan.AgentMetrics{
					SessionID:         "session-1",
					Status:            plan.StatusCompleted,
					AssistantMessages: 81,
				},
			}},
		},
	}}

	err := App{Out: &out, Err: &out}.show(context.Background(), repo, []string{"20260427-1810"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Agent Metrics Budget Warnings:", "assistant_messages", "observed 81 > threshold 80", "001-example"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestShowJSONUsesExplicitNextActionProjection(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Name: "repo", Branch: "main"}, Plan: plan.PlanState{
			ID: "plan-a", Title: "Plan A", PendingSlices: []string{"001-a"},
		}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPending}}},
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.show(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}, []string{"plan-a", "--json"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("structured output contains ANSI control sequences: %q", out.String())
	}
	var payload planview.ShowPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("show output is not clean JSON: %v\n%s", err, out.String())
	}
	if payload.Schema != "tao.show.v1" || payload.ID != "plan-a" {
		t.Fatalf("unexpected payload identity: %+v", payload)
	}
	if !reflect.DeepEqual(payload.NextAction, plan.Derive(detail, time.Time{}).NextAction) {
		t.Fatalf("JSON recommendation = %+v, want shared projection %+v", payload.NextAction, plan.Derive(detail, time.Time{}).NextAction)
	}
	if payload.NextAction.Primary.Command != "tao run plan-a" {
		t.Fatalf("unexpected next action: %+v", payload.NextAction)
	}
}

func TestRenderPlanDetailProminentlyShowsReasonAndSubordinateAlternatives(t *testing.T) {
	detail := &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusInReview, Plan: plan.PlanState{ID: "plan-a", Title: "Plan A", CompletedSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
	}
	loaded := planview.Plan{Detail: detail, Derived: plan.Derive(detail, time.Time{}), Now: time.Now()}
	var out bytes.Buffer
	if err := renderPlanDetail(&out, loaded); err != nil {
		t.Fatal(err)
	}
	text := stripANSI(out.String())
	if strings.Count(text, "Next:") != 1 || !strings.Contains(text, "Next: tao review --run plan-a\nReason: completed slice work needs a current approved review\n") {
		t.Fatalf("expected one prominent recommendation and reason:\n%s", text)
	}
	alternative := "  Alternative (administrative): tao merge --force plan-a"
	if !strings.Contains(text, alternative) || strings.Index(text, alternative) < strings.Index(text, "Reason:") {
		t.Fatalf("expected subordinate administrative alternative:\n%s", text)
	}
}

func TestRenderPlanDetailShowsTerminalNoActionGuidance(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Status: plan.StatusCompleted, Plan: plan.PlanState{ID: "done", Title: "Done"}}}
	loaded := planview.Plan{Detail: detail, Derived: plan.Derive(detail, time.Time{}), Now: time.Now()}
	var out bytes.Buffer
	if err := renderPlanDetail(&out, loaded); err != nil {
		t.Fatal(err)
	}
	text := stripANSI(out.String())
	if !strings.Contains(text, "Next: No action\nReason: legacy completed state is preserved without asserting merge evidence\n") {
		t.Fatalf("expected terminal no-action guidance:\n%s", text)
	}
	if strings.Contains(text, "Alternative (") {
		t.Fatalf("terminal guidance should not invent alternatives:\n%s", text)
	}
}

func TestShowUsageAndRepoErrors(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.show(context.Background(), fakeRepository{}, nil); err == nil {
		t.Fatal("expected show usage error")
	}
	err := app.show(context.Background(), fakeRepository{err: errors.New("nope")}, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected repo error, got %v", err)
	}
}
