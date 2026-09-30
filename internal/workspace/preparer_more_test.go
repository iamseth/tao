package workspace

import (
	"context"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestExecutionPreparerHelpers(t *testing.T) {
	if (ExecutionPreparer{}).runner() == nil || (ExecutionPreparer{}).now().IsZero() {
		t.Fatal("default preparer helpers returned invalid values")
	}
}

func TestExecutionPreparerRejectsNonRuntimeModes(t *testing.T) {
	for _, mode := range []runtimeconfig.ExecutionMode{"shared", "worktree", " isolated "} {
		t.Run(mode.String(), func(t *testing.T) {
			preparer := ExecutionPreparer{PlanRecordFactory: func(*plan.PlanDetail) (PlanRecord, error) {
				t.Fatal("invalid mode reached persistence")
				return nil, nil
			}}
			detail := &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: t.TempDir()}}}
			if _, err := preparer.Prepare(context.Background(), detail, ExecutionPrepareOptions{ExecutionMode: mode}); err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
				t.Fatalf("expected unsupported execution mode error, got %v", err)
			}
		})
	}
}
