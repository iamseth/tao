package run

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestRequireNoCurrentFinalVerificationFailureRefusesReview(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{HeadSHA: "head-current"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "head-current", Result: finalVerificationFailed, Fingerprint: "failure"}
	runner := func(_ context.Context, _ string, command string, args []string, stdout, _ io.Writer) error {
		if command == "git" && len(args) >= 3 && args[len(args)-2] == "rev-parse" {
			_, _ = io.WriteString(stdout, "head-current\n")
			return nil
		}
		return nil
	}
	execution := testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})
	execution.ExecutionRoot = t.TempDir()

	err := requireNoCurrentFinalVerificationFailure(context.Background(), detail, execution)
	if err == nil || !strings.Contains(err.Error(), "--repair-verification") {
		t.Fatalf("review gate error = %v", err)
	}
	detail.State.Plan.FinalVerification.Result = finalVerificationPassed
	if err := requireNoCurrentFinalVerificationFailure(context.Background(), detail, execution); err != nil {
		t.Fatalf("passing verification refused review: %v", err)
	}
}
