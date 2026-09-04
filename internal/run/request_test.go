package run

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/workspace"
)

// newTestRequest builds a resolved run.Request by merging built-in defaults with
// request overrides, then carrying the resolved options on the request.
func newTestRequest(input string, overrides runtimeconfig.RunOptionsPatch) (Request, error) {
	config, err := runtimeconfig.NewConfigFromStages(runtimeconfig.DefaultRunOptionsPatch(), overrides)
	if err != nil {
		return Request{}, err
	}
	return Request{Input: input, ResolvedRunOptions: config.ResolvedOptions()}, nil
}

func TestRunReturnsCapabilityDisabledReasonsBeforeExecutor(t *testing.T) {
	tests := []struct {
		name   string
		detail *plan.PlanDetail
		want   string
	}{
		{
			name:   "blocked plan",
			detail: runPlanDetail(plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil),
			want:   "plan plan-a is blocked",
		},
		{
			name:   "blocked slice",
			detail: runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusBlocked, nil, nil),
			want:   "slice 001-a is blocked",
		},
		{
			name: "approval required",
			detail: &plan.PlanDetail{
				Dir:   "/plans/plan-a",
				State: plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Root: "/repo", Branch: "feature"}, Plan: plan.PlanState{ID: "plan-a", Title: "Plan A", PendingSlices: []string{"001-a"}}},
				Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPending, Approval: &plan.Approval{
					Required: true,
					Reason:   "external review",
				}}}},
			},
			want: "slice 001-a requires approval: external review",
		},
		{
			name: "incomplete dependency",
			detail: &plan.PlanDetail{
				Dir:    "/plans/plan-a",
				State:  plan.State{Status: plan.StatusPlanned, Repo: plan.Repo{Root: "/repo", Branch: "feature"}, Plan: plan.PlanState{ID: "plan-a", Title: "Plan A", PendingSlices: []string{"002-b"}}},
				Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPending}, {ID: "002-b", Status: plan.StatusPending, DependsOn: []string{"001-a"}}}},
			},
			want: "slice 002-b is blocked by incomplete dependencies: 001-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			executor := &countingSliceExecutor{}

			err := executeDetail(context.Background(), tt.detail, nil, &out, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies: RunDependencies{SliceExecutor: executor}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
			if executor.calls != 0 {
				t.Fatalf("expected executor not to run, got %d calls", executor.calls)
			}
			if out.Len() != 0 {
				t.Fatalf("expected no output before executor, got %q", out.String())
			}
		})
	}
}

func TestCheckRequestCanStartRejectsEveryAbandonedRunModeWithSafeReason(t *testing.T) {
	detail := runPlanDetail(plan.StatusAbandoned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Events = append(detail.Events, plan.Event{
		Type: plan.EventTypePlanAbandoned, Reason: "superseded\nby safer work\x1b[31m",
	})
	requests := []Request{
		{},
		{ResolvedRunOptions: ResolvedRunOptions{Continue: true}},
		{RestartBlocked: true},
		{RepairVerification: true},
		{Reverify: true},
		{ResolvedRunOptions: ResolvedRunOptions{PullRequest: true}},
	}
	for _, request := range requests {
		err := CheckRequestCanStart(detail, request)
		if err == nil || !strings.Contains(err.Error(), "plan plan-a is abandoned: superseded by safer work [31m") {
			t.Fatalf("CheckRequestCanStart(%+v) error = %v", request, err)
		}
		if !errors.Is(err, ErrCannotStart) {
			t.Fatalf("abandoned request error = %v, want ErrCannotStart", err)
		}
		if strings.ContainsAny(err.Error(), "\n\r\x1b") {
			t.Fatalf("abandoned request emitted unsafe controls: %q", err)
		}
	}
}

func TestPrepareRequestConfigMapsRunRequestToExecutionConfig(t *testing.T) {
	request, err := newTestRequest("plan-a", runtimeconfig.RunOptionsPatch{
		Mode:          ModeStep,
		CommitPolicy:  CommitPolicySlice,
		ExecutionMode: ExecutionModeCurrent,
		Agent:         AgentPi,
	}.WithContinue(false).WithPullRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	request.Reverify = true
	got, err := prepareRequestConfig(ExecutionConfig{
		ResolvedRunOptions: ResolvedRunOptions{
			MaxSlices:     3,
			Continue:      true,
			CommitPolicy:  CommitPolicySlice,
			ExecutionMode: ExecutionModeIsolated,
			Agent:         AgentPi,
			PullRequest:   true,
		},
		SkipPermissions:   true,
		MaxReworkAttempts: 7,
	}, request)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxSlices != 1 || got.Continue || !got.SkipPermissions || got.MaxReworkAttempts != 7 || got.CommitPolicy != CommitPolicySlice || got.ExecutionMode != ExecutionModeCurrent || got.Agent != AgentPi || got.PullRequest || !got.Reverify {
		t.Fatalf("unexpected execution config: %#v", got)
	}
}

func TestPrepareRequestConfigPreservesWorkspaceConfig(t *testing.T) {
	want := workspace.Config{
		Root:            "/tmp/tao-workspaces",
		Strategy:        workspace.StrategyCurrent,
		MaxParallelRuns: 4,
	}
	got, err := prepareRequestConfig(ExecutionConfig{WorkspaceConfig: want}, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceConfig.Root != want.Root {
		t.Fatalf("WorkspaceConfig.Root = %q, want %q", got.WorkspaceConfig.Root, want.Root)
	}
	if got.WorkspaceConfig.Strategy != want.Strategy {
		t.Fatalf("WorkspaceConfig.Strategy = %q, want %q", got.WorkspaceConfig.Strategy, want.Strategy)
	}
	if got.WorkspaceConfig.MaxParallelRuns != want.MaxParallelRuns {
		t.Fatalf("WorkspaceConfig.MaxParallelRuns = %d, want %d", got.WorkspaceConfig.MaxParallelRuns, want.MaxParallelRuns)
	}
}

func TestCheckRequestCanStartRefusesReverifyWithoutCurrentFailedEvidence(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	err := CheckRequestCanStart(detail, Request{Reverify: true})
	if err == nil || !strings.Contains(err.Error(), "--reverify requires current failed final-verification evidence") {
		t.Fatalf("reverify admission error = %v", err)
	}
}

func TestOptionsExecutionConfigPassesThroughExecutionMode(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    ExecutionMode
	}{
		{name: "unset", options: Options{}},
		{name: "isolated", options: Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated}}}, want: ExecutionModeIsolated},
		{name: "current", options: Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeCurrent}}}, want: ExecutionModeCurrent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.options.executionConfig()
			if got.ExecutionMode != tt.want {
				t.Fatalf("unexpected execution config: %#v", got)
			}
		})
	}
}

func TestPrepareRequestConfigDefaultsExecutionModeToIsolated(t *testing.T) {
	request, err := newTestRequest("plan-a", runtimeconfig.RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := prepareRequestConfig(ExecutionConfig{SkipPermissions: true}, request)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionMode != ExecutionModeIsolated || !got.SkipPermissions || got.Agent != AgentPi {
		t.Fatalf("expected isolated execution mode with service-only skip permissions, got %#v", got)
	}
}
