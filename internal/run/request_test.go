package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
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

func TestExecutionDefaultsCannotGrantRecovery(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[ExecutionConfig](), reflect.TypeFor[Options]()} {
		for _, name := range []string{"RecoveryMode", "RestartBlocked", "RepairVerification", "Reverify"} {
			if _, ok := typ.FieldByName(name); ok {
				t.Errorf("%s exposes request-only recovery field %s", typ, name)
			}
		}
	}
}

func TestSequentialRequestsKeepRecoveryInvocationLocal(t *testing.T) {
	service := NewService(&memoryRunRepository{}, io.Discard, Options{ExecutionConfig: ExecutionConfig{
		ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, PullRequest: true},
	}})
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Repo.Root = t.TempDir()
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: detail.State.Repo.Root, Branch: "feature", HeadSHA: "head123"}
	for _, mode := range []RecoveryMode{{RestartBlocked: true}, {RepairVerification: true}, {Reverify: true}} {
		request := Request{RecoveryMode: mode, ResolvedRunOptions: service.config.ResolvedRunOptions}
		config, err := prepareRequestConfig(service.config, request)
		if err != nil {
			t.Fatal(err)
		}
		first, err := service.prepareRequestRunExecution(context.Background(), detail, request, config)
		if err != nil {
			t.Fatal(err)
		}
		ordinaryRequest := Request{ResolvedRunOptions: service.config.ResolvedRunOptions}
		secondConfig, err := prepareRequestConfig(service.config, ordinaryRequest)
		if err != nil {
			t.Fatal(err)
		}
		second, err := service.prepareRequestRunExecution(context.Background(), detail, ordinaryRequest, secondConfig)
		if err != nil {
			t.Fatal(err)
		}
		if first.Request != request || second.Request.RecoveryMode != (RecoveryMode{}) || first.Config != second.Config {
			t.Fatalf("recovery leaked between requests: first=%+v second=%+v", first, second)
		}
		for _, ordinary := range []runExecution{newRunExecution(config, RunDependencies{}), runExecutionFromOptions(Options{ExecutionConfig: config})} {
			if ordinary.Request.RecoveryMode != (RecoveryMode{}) {
				t.Fatalf("options-only execution granted recovery: %+v", ordinary.Request)
			}
		}
	}
}

func TestRequestForNextRoundClearsSingleShotRecoveryModes(t *testing.T) {
	request := Request{
		Input:        "plan-a",
		RecoveryMode: RecoveryMode{RestartBlocked: true, RepairVerification: true, Reverify: true},
		ResolvedRunOptions: ResolvedRunOptions{
			MaxSlices:      3,
			Continue:       true,
			CommitPolicy:   CommitPolicyNone,
			ExecutionMode:  ExecutionModeCurrent,
			Agent:          AgentClaude,
			PullRequest:    true,
			ReviewEnabled:  true,
			SessionTimeout: 5 * time.Minute,
		},
	}
	original := request
	want := request
	want.Continue = false
	want.RestartBlocked = false
	want.RepairVerification = false

	if got := request.ForNextRound(); got != want {
		t.Fatalf("ForNextRound() = %#v, want %#v", got, want)
	}
	if request != original {
		t.Fatalf("ForNextRound modified receiver: %#v, want %#v", request, original)
	}
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
		{RecoveryMode: RecoveryMode{RestartBlocked: true}},
		{RecoveryMode: RecoveryMode{RepairVerification: true}},
		{RecoveryMode: RecoveryMode{Reverify: true}},
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
		MaxSlices:     new(1),
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
	if got.MaxSlices != 1 || got.Continue || !got.SkipPermissions || got.MaxReworkAttempts != 7 || got.CommitPolicy != CommitPolicySlice || got.ExecutionMode != ExecutionModeCurrent || got.Agent != AgentPi || got.PullRequest {
		t.Fatalf("unexpected execution config: %#v", got)
	}
}

func TestCheckRequestCanStartRequiresRepairVerificationDecision(t *testing.T) {
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
			detail.State.Workspace = &plan.Workspace{HeadSHA: "failed-head"}
			detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: test.kind, Fingerprint: "failure"}

			err := CheckRequestCanStart(detail, Request{RecoveryMode: RecoveryMode{RepairVerification: true}})
			if err == nil || !strings.Contains(err.Error(), "does not authorize code repair") {
				t.Fatalf("repair admission error = %v", err)
			}
		})
	}
}

func TestCheckRequestCanStartAdmitsFirstTwoRepairAttemptsAndRefusesThird(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{HeadSHA: "failed-head"}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", HeadSHA: "failed-head", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindCode, Fingerprint: "failure"}

	if err := CheckRequestCanStart(detail, Request{RecoveryMode: RecoveryMode{RepairVerification: true}}); err != nil {
		t.Fatalf("first repair refused: %v", err)
	}
	for attempt := 1; attempt <= plan.VerificationRepairAttemptCap; attempt++ {
		binding := plan.VerificationRepairBinding{Command: "make verify", HeadSHA: "prior-head", Fingerprint: fmt.Sprintf("prior-%d", attempt)}
		detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: fmt.Sprintf("vr%02d", attempt), Status: plan.StatusCompleted, VerificationRepair: &binding, Completion: &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted}})
		if attempt == 1 {
			if err := CheckRequestCanStart(detail, Request{RecoveryMode: RecoveryMode{RepairVerification: true}}); err != nil {
				t.Fatalf("second repair refused: %v", err)
			}
		}
	}
	if err := CheckRequestCanStart(detail, Request{RecoveryMode: RecoveryMode{RepairVerification: true}}); err == nil || !strings.Contains(err.Error(), "attempt cap reached") {
		t.Fatalf("third repair admission error = %v", err)
	}
}

func TestCheckRequestCanStartRefusesReverifyWithoutCurrentFailedEvidence(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	err := CheckRequestCanStart(detail, Request{RecoveryMode: RecoveryMode{Reverify: true}})
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
