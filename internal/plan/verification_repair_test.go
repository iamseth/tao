package plan

import (
	"strings"
	"testing"
	"time"
)

func TestAppendVerificationRepairIsBoundAndSingleUse(t *testing.T) {
	detail := completedReopenDetail()
	detail.Dir = t.TempDir()
	detail.State.Status = StatusInReview
	detail.State.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree, Path: "/worktree", Branch: "feature/repair", HeadSHA: "head-a"}
	detail.State.Plan.FinalVerification = &FinalVerification{Command: "make verify", CWD: "/worktree", HeadSHA: "head-a", Result: "failed", FailureKind: FinalVerificationFailureKindCode, Details: "failing package", Fingerprint: "abcdef1234567890", OutputTruncated: true, VerifiedAt: time.Now()}
	record := testRecord(detail.Dir, detail)
	request := VerificationRepairRequest{Binding: VerificationRepairBinding{Command: "make verify", HeadSHA: "head-a", Fingerprint: "abcdef1234567890"}, CreatedAt: time.Now().UTC()}

	if err := record.AppendVerificationRepair(request); err != nil {
		t.Fatal(err)
	}
	if len(detail.State.Plan.PendingSlices) != 1 {
		t.Fatalf("pending slices = %v", detail.State.Plan.PendingSlices)
	}
	repair := findSlice(detail, detail.State.Plan.PendingSlices[0])
	if repair == nil || repair.VerificationRepair == nil || repair.Verification.Commands[0] != "make verify" {
		t.Fatalf("generated repair = %+v", repair)
	}
	for _, want := range []string{"Command: make verify", "Head: head-a", "Output truncated: yes", "failing package"} {
		if !strings.Contains(repair.Context, want) {
			t.Fatalf("repair context missing %q: %s", want, repair.Context)
		}
	}
	if action := DeriveNextAction(detail).Primary; action.Kind != PlanActionRun {
		t.Fatalf("generated repair next action = %+v, want ordinary run", action)
	}
	if err := record.AppendVerificationRepair(request); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate repair error = %v", err)
	}
}

func TestAppendVerificationRepairAllowsNewFailureAfterCompletedRepair(t *testing.T) {
	detail := completedReopenDetail()
	detail.Dir = t.TempDir()
	detail.State.Status = StatusInReview
	detail.State.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree, Path: "/worktree", Branch: "feature/repair", HeadSHA: "head-b"}
	detail.State.Plan.FinalVerification = &FinalVerification{Command: "make verify", HeadSHA: "head-b", Result: "failed", FailureKind: FinalVerificationFailureKindCode, Fingerprint: "failure-b"}
	firstRepair := Slice{
		ID: "vr01-final-verification-failure-a", Title: "Repair final verification", Status: StatusCompleted,
		DependsOn: []string{}, Timing: SliceTiming{CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Minute)},
		VerificationRepair: &VerificationRepairBinding{Command: "make verify", HeadSHA: "head-a", Fingerprint: "failure-a"},
		Completion:         &SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "head-b"},
	}
	detail.Slices.Slices = append(detail.Slices.Slices, firstRepair)
	detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, firstRepair.ID)
	record := testRecord(detail.Dir, detail)

	if action := DeriveNextAction(detail).Primary; action.Kind != PlanActionRepairVerification {
		t.Fatalf("post-repair failure next action = %+v", action)
	}
	second := VerificationRepairRequest{Binding: VerificationRepairBinding{Command: "make verify", HeadSHA: "head-b", Fingerprint: "failure-b"}, CreatedAt: time.Now().UTC()}
	if err := record.AppendVerificationRepair(second); err != nil {
		t.Fatal(err)
	}
	if got := detail.State.Plan.PendingSlices; len(got) != 1 || !strings.HasPrefix(got[0], "vr02-") {
		t.Fatalf("follow-up pending slices = %v", got)
	}
	followUp := findSlice(detail, detail.State.Plan.PendingSlices[0])
	if followUp == nil || followUp.VerificationRepair == nil || followUp.VerificationRepair.HeadSHA != "head-b" {
		t.Fatalf("follow-up repair = %+v", followUp)
	}
}

func TestDeriveNextActionPrefersCurrentVerificationRepair(t *testing.T) {
	detail := completedReopenDetail()
	detail.State.Status = StatusInReview
	detail.State.Workspace = &Workspace{HeadSHA: "head-a"}
	detail.State.Plan.FinalVerification = &FinalVerification{Command: "make verify", HeadSHA: "head-a", Result: "failed", FailureKind: FinalVerificationFailureKindCode, Fingerprint: "fingerprint"}

	action := DeriveNextAction(detail).Primary
	if action.Kind != PlanActionRepairVerification || action.Command != "tao run --repair-verification "+detail.State.Plan.ID {
		t.Fatalf("next action = %+v", action)
	}
	detail.State.Plan.FinalVerification.Result = "passed"
	if got := DeriveNextAction(detail).Primary.Kind; got != PlanActionReview {
		t.Fatalf("passing next action = %q, want review", got)
	}
}
