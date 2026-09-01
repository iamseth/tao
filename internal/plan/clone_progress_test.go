package plan

import (
	"testing"
	"time"
)

func TestClonePlanDetailDeepCopiesMutableFields(t *testing.T) {
	current := "001-a"
	approvedBy := "alice"
	approvedAt := "2026-05-31T12:00:00Z"
	duration := int64(42)
	verificationExitCode := 1
	eventExitCode := 127
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	detail := &PlanDetail{
		State: State{
			Status:           StatusInProgress,
			Workspace:        &Workspace{Path: "/repo/.tao/workspaces/plan", Timing: WorkspaceTiming{CreatedAt: &now, PreparedAt: &now, LastActivityAt: &now, CleanedAt: &now}, DependencyStartedAt: &now, DependencyCompletedAt: &now},
			GlobalInvariants: []string{"keep scope"},
			OpenQuestions:    []string{"question"},
			Plan: PlanState{
				ID: "plan-a", ChangeType: ChangeTypeFeat,
				Decision: &Decision{
					Problem: "A concrete planning problem.", WhyNow: "now", ExpectedBenefit: "benefit", Readiness: DecisionReadinessReady,
					SuccessCriteria: []string{"criterion"}, Disposition: DecisionDispositionReady,
					DispositionReason: "reason", Priority: Priority{Level: PriorityOverallLevelMust, Impact: PriorityLevelHigh, Urgency: PriorityLevelMedium, Effort: PriorityEffortSmall, Risk: PriorityLevelLow, Confidence: PriorityLevelHigh, Rationale: "rationale"},
				},
				Sequence:             &Sequence{Position: 1, Total: 2, Relationships: []PlanRelation{{PlanID: "plan-b", Type: PlanRelationBefore, Reason: "reason"}}},
				RuntimePrerequisites: []RuntimePrerequisite{{PlanID: "plan-c", Reason: "must merge first"}},
				CurrentSlice:         &current, CompletedSlices: []string{"000-z"}, PendingSlices: []string{"001-a"}, LastRunStartingDirty: []string{"README.md"}, Timing: PlanTiming{StartedAt: &now, CompletedAt: &now, LastActivityAt: &now}, PullRequest: &PullRequest{URL: "https://example.com/pr/1"}, Review: &PlanReview{Verdict: "pass", Summary: "ready", CommitMessage: &ReviewCommitMessage{Subject: "feat(review): persist proposal", Body: "What:\nPersist it.\n\nWhy:\nReuse it."}, ReviewedAt: now},
				FinalVerification: &FinalVerification{FailureKind: FinalVerificationFailureKindCode, ExitCode: &verificationExitCode},
			},
		},
		Slices: SlicesFile{PlanID: "plan-a", Slices: []Slice{{
			ID:                  "001-a",
			Status:              StatusPending,
			Tags:                []string{"tag"},
			DependsOn:           []string{"000-z"},
			Tasks:               []string{"task"},
			ExpectedFiles:       []string{"file.go"},
			RequiredInputs:      []RequiredInput{{Path: "go.mod", Kind: RequiredInputFile, Reason: "module metadata"}},
			Timing:              SliceTiming{CreatedAt: now, StartedAt: &now, CompletedAt: &now, UpdatedAt: now, LastActivityAt: &now, DurationSeconds: &duration},
			Verification:        Verification{Commands: []string{"go test ./..."}, Steps: []VerificationStep{{Command: "go test ./...", Reason: "step"}}, ManualChecks: []string{"manual"}},
			Approval:            &Approval{Required: true, ApprovedBy: &approvedBy, ApprovedAt: &approvedAt},
			VerificationResults: []VerificationRun{{Command: "go test ./...", Result: "passed"}},
			Extra:               map[string]any{"key": "value"},
		}}},
		Events:   []Event{{Type: EventTypeSliceCompleted, Metrics: &AgentMetrics{SessionID: "session"}, PullRequest: &PullRequest{URL: "https://example.com/pr/1"}, Review: &PlanReview{Verdict: "pass", Summary: "ready", ReviewedAt: now}, DurationSeconds: &duration, ExitCode: &eventExitCode}},
		Warnings: []string{"warning"},
	}
	clone := clonePlanDetail(detail)
	if clone.State.Plan.ChangeType != ChangeTypeFeat {
		t.Fatalf("cloned change type = %q, want %q", clone.State.Plan.ChangeType, ChangeTypeFeat)
	}
	if summary := Summarize(clone, now); summary.ChangeType != ChangeTypeFeat {
		t.Fatalf("summary change type = %q, want %q", summary.ChangeType, ChangeTypeFeat)
	}
	clone.State.Plan.ChangeType = ChangeTypeFix
	clone.State.Plan.Decision.SuccessCriteria[0] = "changed"
	clone.State.Plan.Sequence.Relationships[0].Reason = "changed"
	clone.State.Plan.RuntimePrerequisites[0].Reason = "changed"
	clone.State.Plan.CompletedSlices[0] = "changed"
	clone.State.Plan.LastRunStartingDirty[0] = "changed"
	*clone.State.Plan.CurrentSlice = "changed"
	clone.State.Workspace.Path = "changed"
	*clone.State.Workspace.Timing.CreatedAt = now.Add(time.Hour)
	clone.Slices.Slices[0].Tags[0] = "changed"
	clone.Slices.Slices[0].RequiredInputs[0].Path = "changed"
	clone.Slices.Slices[0].Verification.Commands[0] = "changed"
	clone.Slices.Slices[0].Verification.Steps[0].Reason = "changed"
	clone.Slices.Slices[0].VerificationResults[0].Result = "failed"
	clone.Slices.Slices[0].Extra["key"] = "changed"
	*clone.Slices.Slices[0].Approval.ApprovedBy = "bob"
	*clone.Events[0].Metrics = AgentMetrics{SessionID: "changed"}
	clone.State.Plan.Review.Summary = "changed"
	clone.State.Plan.Review.CommitMessage.Subject = "changed"
	*clone.State.Plan.FinalVerification.ExitCode = 2
	*clone.Events[0].PullRequest = PullRequest{URL: "changed"}
	clone.Events[0].Review.Summary = "changed"
	*clone.Events[0].DurationSeconds = 99
	*clone.Events[0].ExitCode = 126
	clone.Warnings[0] = "changed"

	if detail.State.Plan.ChangeType != ChangeTypeFeat || detail.State.Plan.Decision.SuccessCriteria[0] != "criterion" || detail.State.Plan.Sequence.Relationships[0].Reason != "reason" || detail.State.Plan.RuntimePrerequisites[0].Reason != "must merge first" || detail.State.Plan.CompletedSlices[0] != "000-z" || detail.State.Plan.LastRunStartingDirty[0] != "README.md" || *detail.State.Plan.CurrentSlice != "001-a" || detail.State.Workspace.Path != "/repo/.tao/workspaces/plan" || !detail.State.Workspace.Timing.CreatedAt.Equal(now) {
		t.Fatalf("state was not deeply cloned: %#v", detail.State)
	}
	slice := detail.Slices.Slices[0]
	if slice.Tags[0] != "tag" || slice.RequiredInputs[0].Path != "go.mod" || slice.Verification.Commands[0] != "go test ./..." || slice.Verification.Steps[0].Reason != "step" || slice.VerificationResults[0].Result != "passed" || slice.Extra["key"] != "value" || *slice.Approval.ApprovedBy != "alice" {
		t.Fatalf("slice was not deeply cloned: %#v", slice)
	}
	if detail.State.Plan.Review.Summary != "ready" || detail.State.Plan.Review.CommitMessage.Subject != "feat(review): persist proposal" {
		t.Fatalf("review metadata was not deeply cloned: %#v", detail.State.Plan.Review)
	}
	if detail.State.Plan.FinalVerification.ExitCode == clone.State.Plan.FinalVerification.ExitCode || *detail.State.Plan.FinalVerification.ExitCode != 1 {
		t.Fatalf("final verification exit code was not deeply cloned: %#v", detail.State.Plan.FinalVerification)
	}
	if detail.Events[0].Metrics.SessionID != "session" || detail.Events[0].PullRequest.URL != "https://example.com/pr/1" || detail.Events[0].Review.Summary != "ready" || *detail.Events[0].DurationSeconds != 42 || detail.Events[0].ExitCode == clone.Events[0].ExitCode || *detail.Events[0].ExitCode != 127 || detail.Warnings[0] != "warning" {
		t.Fatalf("events/warnings were not deeply cloned: events=%#v warnings=%#v", detail.Events, detail.Warnings)
	}
	if clonePlanDetail(nil) != nil || cloneDecision(nil) != nil || cloneSequence(nil) != nil || cloneRuntimePrerequisites(nil) != nil || cloneWorkspace(nil) != nil || clonePullRequest(nil) != nil || clonePlanReview(nil) != nil || cloneFinalVerification(nil) != nil || cloneRequiredInputs(nil) != nil || cloneVerificationSteps(nil) != nil || cloneVerificationRuns(nil) != nil || cloneMap(nil) != nil {
		t.Fatal("nil clone helpers should preserve nil")
	}
}

func TestProgressSnapshotDetectsProgress(t *testing.T) {
	detail := &PlanDetail{State: State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}}, Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}}}
	before := SnapshotProgress(detail)
	if ProgressedSince(detail, before) {
		t.Fatal("unchanged plan should not report progress")
	}
	detail.State.Status = StatusInProgress
	detail.State.Plan.PendingSlices = nil
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices[0].Status = StatusCompleted
	if !ProgressedSince(detail, before) {
		t.Fatal("completed slice should report progress")
	}
}
