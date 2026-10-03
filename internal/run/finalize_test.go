package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestExplicitBaselineRepairAppendsSlice(t *testing.T) {
	root := initSliceCompletionRepo(t)
	head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	branch := strings.TrimSpace(runCommitTestGitOutput(t, root, "branch", "--show-current"))
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{Branch: branch, HeadSHA: head}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: head, Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindBaseline, Fingerprint: "failure", Baseline: &plan.FinalVerificationBaseline{SHA: head, Signatures: []string{"TestFlake"}}}
	execution := testRunExecution(ExecutionConfig{
		ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated},
	}, RunDependencies{
		CommandRunner: defaultCommandRunner,
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return plan.NewPlanRecord(detail.Dir, detail)
		},
	})
	execution.ExecutionRoot = root
	if err := appendVerificationRepair(context.Background(), detail, execution); err != nil {
		t.Fatalf("explicit baseline repair: %v", err)
	}
	if count := plan.VerificationRepairAttemptCount(detail); count != 1 {
		t.Fatalf("repair attempts = %d, want 1", count)
	}
	for _, event := range detail.Events {
		if event.Type == plan.EventTypeVerificationRepairCreated {
			return
		}
	}
	t.Fatal("explicit baseline repair did not record verification_repair_created")
}

func TestFinalizerBaselineFailureDoesNotScheduleRepair(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{HeadSHA: "head"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "head", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindBaseline, Fingerprint: "failure"}
	failure := &FinalVerificationError{Verification: *detail.State.Plan.FinalVerification}
	finalizer := Finalizer{}
	if err := finalizer.handleFinalVerificationFailure(context.Background(), 1, detail, detail.Dir, failure); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want original failure", err)
	}
	for _, event := range detail.Events {
		if event.Type == plan.EventTypeVerificationRepairCreated {
			t.Fatal("baseline failure scheduled automatic repair")
		}
	}
	if count := plan.VerificationRepairAttemptCount(detail); count != 0 {
		t.Fatalf("repair attempts = %d", count)
	}
}
