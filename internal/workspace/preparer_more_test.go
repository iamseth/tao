package workspace

import (
	"strings"
	"testing"
)

func TestExecutionPreparerHelpers(t *testing.T) {
	if got, err := executionModeWorkspaceStrategy(""); err != nil || got != StrategyWorktree {
		t.Fatalf("default strategy = %q, %v", got, err)
	}
	if got, err := executionModeWorkspaceStrategy("isolated"); err != nil || got != StrategyWorktree {
		t.Fatalf("isolated strategy = %q, %v", got, err)
	}
	if got, err := executionModeWorkspaceStrategy("current"); err != nil || got != StrategyCurrent {
		t.Fatalf("current strategy = %q, %v", got, err)
	}
	if _, err := executionModeWorkspaceStrategy("shared"); err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("expected unsupported execution mode error, got %v", err)
	}
	if (ExecutionPreparer{}).runner() == nil || (ExecutionPreparer{}).now().IsZero() {
		t.Fatal("default preparer helpers returned invalid values")
	}
}
