package run

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
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

func TestPrepareRunExecutionAllowsCurrentModeReverifyWithPullRequestDefault(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Repo.Root = "/current-root"
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyCurrent,
		Path:     "/recorded-worktree",
		Branch:   "feature/plan-a",
		HeadSHA:  "head123",
	}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{
		Result:      finalVerificationFailed,
		HeadSHA:     "head123",
		Fingerprint: "failed-verification",
	}
	var out bytes.Buffer
	service := NewService(&memoryRunRepository{}, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
		CommitPolicy:  CommitPolicySlice,
		ExecutionMode: ExecutionModeCurrent,
		PullRequest:   true,
	}}})
	config := service.config
	config.OptionSources = RunOptionSources{PullRequest: "repository", ExecutionMode: "flag"}
	request := Request{RecoveryMode: RecoveryMode{Reverify: true}}

	execution, err := service.prepareRequestRunExecution(context.Background(), detail, request, config)
	if err != nil {
		t.Fatalf("prepare current-mode reverification with pull-request default: %v", err)
	}
	if execution.ExecutionRoot != detail.State.Repo.Root {
		t.Fatalf("reverification execution root = %q, want current root %q", execution.ExecutionRoot, detail.State.Repo.Root)
	}
	if !execution.Request.Reverify || !execution.Config.PullRequest || execution.Config.ExecutionMode != ExecutionModeCurrent {
		t.Fatalf("reverification config changed inherited settings: %#v", execution.Config)
	}
}

func TestPlacementNoticeMetadata(t *testing.T) {
	for _, tt := range []struct {
		name, recorded string
		requested      runtimeconfig.ExecutionMode
		want           string
	}{
		{"current requested", "isolated", ExecutionModeCurrent, "Info: requested execution mode current differs from recorded mode isolated; existing placement and safety checks still apply.\n"},
		{"isolated requested", "current", ExecutionModeIsolated, "Info: requested execution mode isolated differs from recorded mode current; existing placement and safety checks still apply.\n"},
		{"legacy mismatch", "worktree", ExecutionModeCurrent, "Info: requested execution mode current differs from recorded mode isolated; existing placement and safety checks still apply.\n"},
		{"legacy equivalent", "worktree", ExecutionModeIsolated, ""},
		{"isolated match", "isolated", ExecutionModeIsolated, ""},
		{"current match", "current", ExecutionModeCurrent, ""},
		{"default isolated", "worktree", "", ""},
		{"missing strategy", "", ExecutionModeCurrent, ""},
		{"invalid strategy", "unknown\nunsafe", ExecutionModeCurrent, ""},
		{"missing workspace", "absent", ExecutionModeCurrent, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := runPlanDetail(plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked, nil, nil)
			detail.Dir = t.TempDir()
			detail.State.Workspace = nil
			if tt.recorded != "absent" {
				detail.State.Workspace = &plan.Workspace{Strategy: tt.recorded}
			}
			var out bytes.Buffer
			service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, &out, Options{})
			request := Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: tt.requested}}
			// One outer invocation can drive multiple Execute calls (rework or
			// recovery), without repeating presentation or changing admission.
			err := service.WithPlanRunLock(context.Background(), request, func(ctx context.Context) error {
				for range 2 {
					if err := service.Execute(ctx, request); err == nil {
						t.Fatal("blocked plan unexpectedly admitted")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Fatalf("output = %q, want %q", out.String(), tt.want)
			}
			if request.ExecutionMode != tt.requested || (detail.State.Workspace != nil && detail.State.Workspace.Strategy != tt.recorded) {
				t.Fatal("notice changed placement inputs")
			}
			// A new invocation on the same service gets its own notice.
			_ = service.Execute(context.Background(), request)
			if out.String() != tt.want+tt.want {
				t.Fatalf("new invocation output = %q", out.String())
			}
		})
	}
}

type placementFailWriter struct{ calls int }

func (w *placementFailWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, io.ErrClosedPipe
}

func TestPlacementNoticePreservesPRRefusalDespiteOutputFailure(t *testing.T) {
	failed := &placementFailWriter{}
	for _, out := range []io.Writer{nil, io.Discard, failed} {
		detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
		detail.Dir = t.TempDir()
		detail.State.Workspace = &plan.Workspace{Strategy: "worktree", Path: "/recorded-root"}
		service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, out, Options{ExecutionConfig: ExecutionConfig{OptionSources: RunOptionSources{PullRequest: "repository", ExecutionMode: "flag"}}})
		err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeCurrent, CommitPolicy: CommitPolicySlice, PullRequest: true}})
		if err == nil || !strings.Contains(err.Error(), "--pull-request requires --execution-mode isolated") {
			t.Fatalf("PR refusal = %v", err)
		}
		for _, source := range []string{"TAO_PULL_REQUEST source=repository", "TAO_EXECUTION_MODE source=flag"} {
			if !strings.Contains(err.Error(), source) {
				t.Fatalf("missing %s: %v", source, err)
			}
		}
		if detail.State.Workspace.Path != "/recorded-root" {
			t.Fatal("recorded root changed")
		}
	}
	if failed.calls != 1 {
		t.Fatalf("failed output attempts = %d", failed.calls)
	}
}

func TestPlacementNoticeDoesNotChangeRepeatedPreparation(t *testing.T) {
	for _, tt := range []struct {
		requested          runtimeconfig.ExecutionMode
		recorded, wantRoot string
	}{
		{ExecutionModeCurrent, "isolated", "/recorded-root"},
		{ExecutionModeIsolated, "current", "/current-root"},
	} {
		detail := completedReviewPlanDetail(t.TempDir())
		detail.State.Repo.Root = "/current-root"
		detail.State.Workspace = &plan.Workspace{Strategy: tt.recorded, Path: "/recorded-root", Branch: "feature/test"}
		var out bytes.Buffer
		service := NewService(&memoryRunRepository{}, &out, Options{})
		config := ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: tt.requested, PullRequest: true}}
		request := Request{RecoveryMode: RecoveryMode{Reverify: true}}
		service.reportPlacementMismatch(detail, config)
		before := out.String()
		for range 2 {
			execution, err := service.prepareRequestRunExecution(context.Background(), detail, request, config)
			if err != nil {
				t.Fatal(err)
			}
			if execution.ExecutionRoot != tt.wantRoot || execution.Config.ExecutionMode != tt.requested || !execution.Config.PullRequest || !execution.Request.Reverify {
				t.Fatalf("preparation changed: %#v", execution)
			}
		}
		if out.String() != before {
			t.Fatal("preparation repeated notice")
		}
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
