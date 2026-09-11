package run

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestAppendVerificationRepairRefusesNonCodeRecoveryBeforeGit(t *testing.T) {
	tests := []struct {
		name string
		kind plan.FinalVerificationFailureKind
	}{
		{name: "tool missing", kind: plan.FinalVerificationFailureKindToolMissing},
		{name: "timeout", kind: plan.FinalVerificationFailureKindTimeout},
		{name: "cancelled", kind: plan.FinalVerificationFailureKindCancelled},
		{name: "invalid command", kind: plan.FinalVerificationFailureKindInvalidCommand},
		{name: "legacy unclassified"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: test.kind, Fingerprint: "failure"}
			gitCalled := false
			execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated}}, RunDependencies{
				CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
					gitCalled = true
					return nil
				},
			})
			execution.ExecutionRoot = t.TempDir()

			err := appendVerificationRepair(context.Background(), detail, execution)
			if err == nil || !strings.Contains(err.Error(), "does not authorize code repair") {
				t.Fatalf("append error = %v", err)
			}
			if gitCalled {
				t.Fatal("git inspected before recovery decision refused repair")
			}
		})
	}
}

func TestAppendVerificationRepairRefusesExactHeadDriftBeforeMutation(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "failure"}
	factoryCalled := false
	runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
		if command != "git" {
			return nil
		}
		switch runGitKey(args) {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "advanced-head\n")
		}
		return nil
	}
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated}}, RunDependencies{
		CommandRunner: runner,
		PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) {
			factoryCalled = true
			return nil, nil
		},
	})
	execution.ExecutionRoot = t.TempDir()

	err := appendVerificationRepair(context.Background(), detail, execution)
	if err == nil || !strings.Contains(err.Error(), "worktree boundary is stale") {
		t.Fatalf("head drift error = %v", err)
	}
	if factoryCalled || len(detail.State.Plan.PendingSlices) != 0 {
		t.Fatalf("mutation began after drift: factory=%t pending=%v", factoryCalled, detail.State.Plan.PendingSlices)
	}
}

func TestAppendedVerificationRepairResumesAsOrdinaryRun(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "failure"}
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	request := plan.VerificationRepairRequest{Binding: plan.VerificationRepairBinding{Command: "make verify", HeadSHA: "failed-head", Fingerprint: "failure"}, CreatedAt: time.Now().UTC()}
	if err := record.AppendVerificationRepair(request); err != nil {
		t.Fatal(err)
	}

	if err := CheckRequestCanStart(detail, Request{}); err != nil {
		t.Fatalf("ordinary crash recovery run refused: %v", err)
	}
	if got := plan.VerificationRepairAttemptCount(detail); got != 1 {
		t.Fatalf("ordinary resume consumed another attempt: %d", got)
	}
}

func TestRequireCurrentFailedFinalVerificationBoundaryRefusesStaleBranchOrHead(t *testing.T) {
	tests := []struct {
		name       string
		liveBranch string
		liveHead   string
	}{
		{name: "branch", liveBranch: "other", liveHead: "failed-head"},
		{name: "head", liveBranch: "feature", liveHead: "other-head"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, Fingerprint: "failure"}
			runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
				if command != "git" {
					return nil
				}
				switch strings.Join(args, " ") {
				case "branch --show-current":
					_, _ = io.WriteString(stdout, test.liveBranch+"\n")
				case "rev-parse HEAD":
					_, _ = io.WriteString(stdout, test.liveHead+"\n")
				}
				return nil
			}
			execution := testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})
			execution.ExecutionRoot = t.TempDir()

			_, err := requireCurrentFailedFinalVerificationBoundary(context.Background(), detail, execution, "reverification")
			if err == nil || !strings.Contains(err.Error(), "reverification worktree boundary is stale") {
				t.Fatalf("boundary error = %v", err)
			}
		})
	}
}

func TestRequireReverifyFinalVerificationBoundary(t *testing.T) {
	tests := []struct {
		name       string
		liveBranch string
		liveHead   string
		status     string
		diverged   bool
		unsettled  bool
		wantErr    string
	}{
		{name: "exact head", liveBranch: "feature", liveHead: "failed-head"},
		{name: "advanced clean head", liveBranch: "feature", liveHead: "advanced-head"},
		{name: "diverged head", liveBranch: "feature", liveHead: "diverged-head", diverged: true, wantErr: "or its descendant"},
		{name: "dirty worktree", liveBranch: "feature", liveHead: "failed-head", status: " M repaired.go\n", wantErr: "clean worktree"},
		{name: "different branch", liveBranch: "other", liveHead: "failed-head", wantErr: "does not match recorded branch"},
		{name: "unsettled slice work", liveBranch: "feature", liveHead: "failed-head", unsettled: true, wantErr: "settled slice work"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{Branch: "feature", HeadSHA: "failed-head"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, Fingerprint: "failure"}
			if test.unsettled {
				detail.State.Status = plan.StatusInProgress
				detail.State.Plan.CompletedSlices = nil
				detail.State.Plan.PendingSlices = []string{"001-a"}
				detail.Slices.Slices[0].Status = plan.StatusPending
			}
			runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
				if command != "git" {
					return nil
				}
				switch runGitKey(args) {
				case "status --porcelain":
					_, _ = io.WriteString(stdout, test.status)
				case "branch --show-current":
					_, _ = io.WriteString(stdout, test.liveBranch+"\n")
				case "rev-parse HEAD":
					_, _ = io.WriteString(stdout, test.liveHead+"\n")
				case "merge-base --is-ancestor failed-head diverged-head":
					if test.diverged {
						return fmt.Errorf("exit status 1")
					}
				}
				return nil
			}
			execution := testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})
			execution.ExecutionRoot = t.TempDir()

			failure, err := requireReverifyFinalVerificationBoundary(context.Background(), detail, execution)
			if test.wantErr == "" {
				if err != nil || failure != detail.State.Plan.FinalVerification {
					t.Fatalf("boundary = %+v, %v", failure, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("boundary error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestRequireNoCurrentFinalVerificationFailureAcceptsPassingEvidence(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Result: finalVerificationPassed}
	if err := requireNoCurrentFinalVerificationFailure(context.Background(), detail, testRunExecution(ExecutionConfig{}, RunDependencies{})); err != nil {
		t.Fatalf("passing verification refused review: %v", err)
	}
}
