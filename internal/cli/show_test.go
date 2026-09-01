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

func TestShowProjectsSafeAbandonmentEvidenceInTextAndJSON(t *testing.T) {
	abandonedAt := time.Date(2026, 9, 1, 17, 0, 0, 0, time.FixedZone("offset", 3600))
	reason := "superseded\nby\ta safer path\x1b[31m " + strings.Repeat("界", 120)
	detail := &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusAbandoned, Plan: plan.PlanState{ID: "plan-a", Title: "Plan A"}},
		Events: []plan.Event{{Type: plan.EventTypePlanAbandoned, Timestamp: abandonedAt, Reason: reason, Message: "Plan abandoned"}},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}

	var textOut bytes.Buffer
	if err := (App{Out: &textOut, Err: &textOut}).show(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSI(textOut.String())
	for _, want := range []string{"Status: abandoned", "Abandoned: 2026-09-01T16:00:00Z", "Abandonment reason: superseded by a safer path [31m", "Next: No action", "Reason: the plan was abandoned"} {
		if !strings.Contains(text, want) {
			t.Fatalf("show output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\nby") || strings.Contains(text, "\t") || strings.Contains(text, "\x1b") || strings.Contains(text, strings.Repeat("界", 100)) {
		t.Fatalf("show rendered unsafe or unbounded abandonment reason: %q", text)
	}

	var jsonOut bytes.Buffer
	if err := (App{Out: &jsonOut, Err: &jsonOut}).show(context.Background(), repo, []string{"plan-a", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload planview.ShowPayload
	if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Abandonment == nil || payload.Abandonment.AbandonedAt == nil || payload.Abandonment.AbandonedAt.Format(time.RFC3339) != "2026-09-01T16:00:00Z" || strings.Contains(payload.Abandonment.Reason, "\n") {
		t.Fatalf("show JSON abandonment = %+v", payload.Abandonment)
	}
	if strings.Contains(payload.NextAction.Primary.Reason, reason) {
		t.Fatalf("show JSON duplicated raw abandonment reason: %+v", payload.NextAction)
	}
}

func TestShowHandlesAbandonedStatusWithoutEvidence(t *testing.T) {
	detail := &plan.PlanDetail{State: plan.State{Status: plan.StatusAbandoned, Plan: plan.PlanState{ID: "plan-a", Title: "Plan A"}}}
	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out}).show(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}, []string{"plan-a", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload planview.ShowPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != plan.StatusAbandoned || payload.Abandonment != nil {
		t.Fatalf("missing-evidence abandonment payload = %+v", payload)
	}
}

func TestShowProjectsDurableFinalizationRecoveryInTextJSONAndEvents(t *testing.T) {
	failedAt := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
	failure := &plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "fix/plan-a", HeadSHA: "head-secret",
		FailedAt: failedAt, RecoveryAction: "resume_pull_request",
	}
	detail := &plan.PlanDetail{
		State: plan.State{Status: plan.StatusReviewed, Repo: plan.Repo{Name: "repo", Branch: "main"}, Workspace: &plan.Workspace{Branch: "fix/plan-a", HeadSHA: "head-secret"}, Plan: plan.PlanState{
			ID: "plan-a", Title: "Plan A", CompletedSlices: []string{"001-a"}, FinalizationFailure: failure,
		}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
		Events: []plan.Event{{Type: plan.EventTypeFinalizationFailed, Timestamp: failedAt, FinalizationFailure: failure, Message: "Plan finalization failed"}},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head-secret"})
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}

	var textOut bytes.Buffer
	if err := (App{Out: &textOut, Err: &textOut}).show(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSI(textOut.String())
	for _, want := range []string{
		"Next: tao run --pull-request plan-a", "Finalization failure: pull_request_finalization (publication_failed)",
		"finalization_failed pull_request_finalization publication_failed; recovery: resume_pull_request",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("show output missing %q:\n%s", want, text)
		}
	}

	var jsonOut bytes.Buffer
	if err := (App{Out: &jsonOut, Err: &jsonOut}).show(context.Background(), repo, []string{"plan-a", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload planview.ShowPayload
	if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Finalization == nil || payload.Finalization.Phase != plan.FinalizationFailurePhasePullRequest || payload.NextAction.Primary.Command != "tao run --pull-request plan-a" {
		t.Fatalf("show JSON recovery = finalization %+v next %+v", payload.Finalization, payload.NextAction.Primary)
	}
	if strings.Contains(jsonOut.String(), "head-secret") || strings.Contains(jsonOut.String(), "fix/plan-a") {
		t.Fatalf("show JSON exposed exact recovery boundaries: %s", jsonOut.String())
	}
}

func TestShowProjectsNonRetryablePullRequestRecoveryActions(t *testing.T) {
	tests := []struct {
		name      string
		category  string
		action    string
		wantText  string
		wantCmd   string
		wantInstr string
	}{
		{name: "missing or mismatched linked worktree", category: "workspace_mismatch", action: plan.FinalizationRecoveryResumePullRequest, wantText: "Repair or restore the plan's recorded linked worktree", wantInstr: "recorded path, branch, and HEAD"},
		{name: "head drift", category: "head_drift", action: plan.FinalizationRecoveryResumePullRequest, wantText: "Restore the plan worktree to its recorded branch and HEAD", wantInstr: "recorded branch and HEAD"},
		{name: "dirty worktree", category: "workspace_dirty", action: plan.FinalizationRecoveryRestoreBoundary, wantText: "Restore a clean plan worktree", wantInstr: "clean plan worktree"},
		{name: "review mismatch", category: "review_head_mismatch", action: plan.FinalizationRecoveryRerunReview, wantText: "Next: tao review --run plan-a", wantCmd: "tao review --run plan-a"},
		{name: "intent mismatch", category: "intent_mismatch", action: plan.FinalizationRecoveryRepairIntent, wantText: "Repair the conflicting durable pull-request intent", wantInstr: "durable pull-request intent"},
		{name: "identity mismatch", category: "identity_mismatch", action: plan.FinalizationRecoveryRepairIntent, wantText: "Repair the stale recorded pull-request number and URL", wantInstr: "do not adopt a remotely discovered identity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failedAt := time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC)
			detail := &plan.PlanDetail{
				State: plan.State{Status: plan.StatusReviewed, Workspace: &plan.Workspace{Branch: "fix/plan-a", HeadSHA: "head-secret"}, Plan: plan.PlanState{
					ID: "plan-a", Title: "Plan A", CompletedSlices: []string{"001-a"}, FinalizationFailure: &plan.FinalizationFailure{
						Phase: plan.FinalizationFailurePhasePullRequest, Category: test.category, Branch: "fix/plan-a", HeadSHA: "head-secret", FailedAt: failedAt, RecoveryAction: test.action,
					},
				}},
				Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
			}
			plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head-secret"})
			repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}

			var textOut bytes.Buffer
			if err := (App{Out: &textOut, Err: &textOut}).show(context.Background(), repo, []string{"plan-a"}); err != nil {
				t.Fatal(err)
			}
			if text := stripANSI(textOut.String()); !strings.Contains(text, test.wantText) || strings.Contains(text, "Next: tao run --pull-request") {
				t.Fatalf("show text recovery:\n%s", text)
			}

			var jsonOut bytes.Buffer
			if err := (App{Out: &jsonOut, Err: &jsonOut}).show(context.Background(), repo, []string{"plan-a", "--json"}); err != nil {
				t.Fatal(err)
			}
			var payload planview.ShowPayload
			if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.NextAction.Primary.Command != test.wantCmd || !strings.Contains(payload.NextAction.Primary.Instruction, test.wantInstr) {
				t.Fatalf("show JSON next action = %#v", payload.NextAction.Primary)
			}
			if test.category == "identity_mismatch" && payload.Finalization.RecoveryAction != plan.FinalizationRecoveryRepairIdentity {
				t.Fatalf("show JSON identity recovery = finalization %+v", payload.Finalization)
			}
		})
	}
}

func TestShowProjectsPostCorrectionRecoveryActions(t *testing.T) {
	tests := []struct {
		name      string
		category  string
		action    string
		wantText  string
		wantCmd   string
		wantInstr string
	}{
		{name: "head drift", category: "head_drift", action: plan.FinalizationRecoveryRerunReview, wantText: "Restore the plan worktree to its recorded branch and HEAD", wantInstr: "recorded branch and HEAD"},
		{name: "workspace mismatch", category: "workspace_mismatch", action: plan.FinalizationRecoveryRestoreBoundary, wantText: "Repair or restore the plan's recorded linked worktree", wantInstr: "recorded path, branch, and HEAD"},
		{name: "dirty worktree", category: "workspace_dirty", action: plan.FinalizationRecoveryRerunReview, wantText: "Restore a clean plan worktree", wantInstr: "clean plan worktree"},
		{name: "intent mismatch", category: "intent_mismatch", action: plan.FinalizationRecoveryRepairIntent, wantText: "Repair the conflicting durable pull-request intent", wantInstr: "durable pull-request intent"},
		{name: "invalid proposal", category: "proposal_invalid", action: plan.FinalizationRecoveryRerunReview, wantText: "Next: tao review --run plan-a", wantCmd: "tao review --run plan-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failedAt := time.Date(2026, 8, 31, 17, 30, 0, 0, time.UTC)
			detail := &plan.PlanDetail{
				State: plan.State{Status: plan.StatusReviewed, Plan: plan.PlanState{
					ID: "plan-a", Title: "Plan A", CompletedSlices: []string{"001-a"}, FinalizationFailure: &plan.FinalizationFailure{
						Phase: plan.FinalizationFailurePhaseProposalRepair, Category: test.category, ReviewBase: "base123", ReviewHead: "head123", FailedAt: failedAt, RecoveryAction: test.action,
					},
				}},
				Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
			}
			plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: "base123", Head: "head123"})
			repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}

			var textOut bytes.Buffer
			if err := (App{Out: &textOut, Err: &textOut}).show(context.Background(), repo, []string{"plan-a"}); err != nil {
				t.Fatal(err)
			}
			if text := stripANSI(textOut.String()); !strings.Contains(text, test.wantText) {
				t.Fatalf("show text recovery missing %q:\n%s", test.wantText, text)
			}

			var jsonOut bytes.Buffer
			if err := (App{Out: &jsonOut, Err: &jsonOut}).show(context.Background(), repo, []string{"plan-a", "--json"}); err != nil {
				t.Fatal(err)
			}
			var payload planview.ShowPayload
			if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Finalization == nil || payload.Finalization.RecoveryAction != plan.ProposalRepairRecoveryAction(test.category) || payload.NextAction.Primary.Command != test.wantCmd || !strings.Contains(payload.NextAction.Primary.Instruction, test.wantInstr) {
				t.Fatalf("show JSON recovery = finalization %+v next %+v", payload.Finalization, payload.NextAction.Primary)
			}
		})
	}
}

func TestShowProjectsFailedProposalCorrectionForLegacyEmptyReviewBase(t *testing.T) {
	failedAt := time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC)
	failure := &plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhaseProposalRepair, Category: "proposal_invalid", ReviewBase: "workspace-base", ReviewHead: "head123",
		FailedAt: failedAt, RecoveryAction: "rerun_review",
	}
	detail := &plan.PlanDetail{
		State: plan.State{
			Status:    plan.StatusReviewed,
			Repo:      plan.Repo{Name: "repo", Branch: "main", BaseCommit: "plan-base"},
			Workspace: &plan.Workspace{BaseSHA: "workspace-base"},
			Plan: plan.PlanState{
				ID: "plan-a", Title: "Plan A", CompletedSlices: []string{"001-a"}, FinalizationFailure: failure,
			},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
	}
	plan.SetPersistedReview(detail, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"})
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"plan-a": detail}}

	var textOut bytes.Buffer
	if err := (App{Out: &textOut, Err: &textOut}).show(context.Background(), repo, []string{"plan-a"}); err != nil {
		t.Fatal(err)
	}
	text := stripANSI(textOut.String())
	for _, want := range []string{"Next: tao review --run plan-a", "Finalization failure: proposal_repair (proposal_invalid)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("legacy proposal recovery output missing %q:\n%s", want, text)
		}
	}

	var jsonOut bytes.Buffer
	if err := (App{Out: &jsonOut, Err: &jsonOut}).show(context.Background(), repo, []string{"plan-a", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload planview.ShowPayload
	if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Finalization == nil || payload.Finalization.RecoveryAction != "rerun_review" || payload.NextAction.Primary.Command != "tao review --run plan-a" {
		t.Fatalf("legacy proposal recovery JSON = finalization %+v next %+v", payload.Finalization, payload.NextAction.Primary)
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

func TestRenderPrimaryNextActionShowsRecoveryInstruction(t *testing.T) {
	var out bytes.Buffer
	next := plan.PlanNextAction{Primary: plan.PlanAction{
		Kind:        plan.PlanActionRecoverSliceCompletion,
		Class:       plan.PlanActionClassRecovery,
		Instruction: "Rerun the original complete tao slice-complete invocation with all previously supplied file arguments",
		Reason:      "an automatic slice commit intent is not settled",
	}}
	if err := renderPrimaryNextAction(&out, next); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Next: Rerun the original complete tao slice-complete invocation") || strings.Contains(text, "Next: tao slice-complete\n") {
		t.Fatalf("expected non-executable recovery guidance, got:\n%s", text)
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
