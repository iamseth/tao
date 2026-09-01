package run

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

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

func TestRequireNoCurrentFinalVerificationFailureRefusesReviewByClassification(t *testing.T) {
	tests := []struct {
		name       string
		kind       plan.FinalVerificationFailureKind
		wantCause  string
		wantAction string
	}{
		{name: "code", kind: plan.FinalVerificationFailureKindCode, wantCause: "code verification failed", wantAction: "tao run --repair-verification plan-a"},
		{name: "tool missing", kind: plan.FinalVerificationFailureKindToolMissing, wantCause: "required verification tool is missing", wantAction: "tao run --reverify plan-a"},
		{name: "timeout", kind: plan.FinalVerificationFailureKindTimeout, wantCause: "verification timed out", wantAction: "tao run --reverify plan-a"},
		{name: "cancelled", kind: plan.FinalVerificationFailureKindCancelled, wantCause: "verification was cancelled", wantAction: "tao run --reverify plan-a"},
		{name: "invalid command", kind: plan.FinalVerificationFailureKindInvalidCommand, wantCause: "verification command is invalid", wantAction: "tao run --reverify plan-a"},
		{name: "legacy unclassified", wantCause: "without a current classification", wantAction: "tao run --reverify plan-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace = &plan.Workspace{HeadSHA: "head-current"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "head-current", Result: finalVerificationFailed, FailureKind: test.kind, Fingerprint: "failure"}
			runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
				if command == "git" && len(args) >= 3 && args[len(args)-2] == "rev-parse" {
					_, _ = io.WriteString(stdout, "head-current\n")
				}
				return nil
			}
			execution := testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})
			execution.ExecutionRoot = t.TempDir()

			err := requireNoCurrentFinalVerificationFailure(context.Background(), detail, execution)
			if err == nil || !strings.Contains(err.Error(), test.wantCause) || !strings.Contains(err.Error(), test.wantAction) {
				t.Fatalf("review gate error = %v", err)
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
