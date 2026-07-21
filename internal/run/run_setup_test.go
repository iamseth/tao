package run

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

// TestPrepareRunExecutionResolvesAllRequiredDependencies asserts that a normal
// Service-backed run leaves no required dependency nil. prepareRunExecution runs
// the completeness guard internally, so a regression in defaulting surfaces here
// rather than as a nil dereference during execution.
func TestPrepareRunExecutionResolvesAllRequiredDependencies(t *testing.T) {
	execution := preparedServiceExecution(t)
	if err := requireResolvedDependencies(execution.Dependencies); err != nil {
		t.Fatalf("expected a normal service-backed run to resolve all dependencies: %v", err)
	}
}

// TestRequireResolvedDependenciesNamesMissingDependency asserts the guard trips
// loudly and names the offending field when a required dependency stays nil.
func TestRequireResolvedDependenciesNamesMissingDependency(t *testing.T) {
	execution := preparedServiceExecution(t)
	execution.Dependencies.SliceExecutor = nil

	err := requireResolvedDependencies(execution.Dependencies)
	if err == nil {
		t.Fatal("expected the completeness guard to trip when a required dependency is nil")
	}
	if !strings.Contains(err.Error(), "SliceExecutor") {
		t.Fatalf("expected the guard error to name the missing SliceExecutor dependency, got %v", err)
	}
}

// preparedServiceExecution resolves a full Service-backed dependency graph with
// only the minimum injected collaborators, mirroring a real run.
func preparedServiceExecution(t *testing.T) runExecution {
	t.Helper()
	repo := &memoryRunRepository{}
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = t.TempDir()
	var out bytes.Buffer
	workspaceRoot := t.TempDir()

	execution, err := NewService(repo, &out, Options{RunDependencies: RunDependencies{
		CommandRunner: runGitFake(&[]string{}, nil),
		WorkspacePreparer: func(ctx context.Context, detail *plan.PlanDetail, input WorkspaceResolverInput) (string, error) {
			return workspaceRoot, nil
		},
	}}).prepareRunExecution(context.Background(), detail, ExecutionConfig{})
	if err != nil {
		t.Fatalf("prepare run execution: %v", err)
	}
	return execution
}
