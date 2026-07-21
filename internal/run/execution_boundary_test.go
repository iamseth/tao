package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestExecutionBoundaryActionsCharacterizeEveryDisposition(t *testing.T) {
	const root = "/workspace/plan"
	tests := []struct {
		name        string
		mutate      func(*InterruptedSliceInput)
		disposition InterruptedSliceDisposition
		effective   InterruptedSliceDisposition
		fixedRoot   string
		repair      ExecutionBoundaryRepairRequirement
		prepare     bool
		handoff     bool
	}{
		{
			name: "new start",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.State.Plan.CurrentSlice = nil
				input.Detail.Slices.Slices[0].Status = plan.StatusPending
				input.Detail.Slices.Slices[0].ExecutionRoot = ""
				input.Detail.Slices.Slices[0].ExecutionStart = nil
				input.ExecutionRoot = ""
			},
			disposition: InterruptedSliceNewStart, effective: InterruptedSliceNewStart,
			repair: ExecutionBoundaryRepairNone, prepare: true, handoff: true,
		},
		{
			name: "blocked fresh-start continuation",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.State.Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].ExecutionRoot = ""
				input.Detail.Slices.Slices[0].ExecutionStart = nil
				input.ExecutionRoot = ""
				input.ContinueBlocked = true
			},
			disposition: InterruptedSliceBlockedContinue, effective: InterruptedSliceNewStart,
			repair: ExecutionBoundaryRepairNone, prepare: true, handoff: true,
		},
		{
			name: "clean torn-start repair",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.Slices.Slices[0].ExecutionStart = nil
			},
			disposition: InterruptedSliceCleanStartRepair, effective: InterruptedSliceCleanStartRepair,
			fixedRoot: root, repair: ExecutionBoundaryRepairSliceStart, handoff: true,
		},
		{
			name: "blocked torn-start repair",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.State.Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].ExecutionStart = nil
				input.ContinueBlocked = true
			},
			disposition: InterruptedSliceBlockedContinue, effective: InterruptedSliceCleanStartRepair,
			fixedRoot: root, repair: ExecutionBoundaryRepairSliceStart, handoff: true,
		},
		{
			name:        "isolated pre-intent resume",
			disposition: InterruptedSliceResume, effective: InterruptedSliceResume,
			fixedRoot: root, repair: ExecutionBoundaryRepairNone, handoff: true,
		},
		{
			name: "blocked resume continuation",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.State.Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].Status = plan.StatusBlocked
				input.ContinueBlocked = true
			},
			disposition: InterruptedSliceBlockedContinue, effective: InterruptedSliceResume,
			fixedRoot: root, repair: ExecutionBoundaryRepairNone, handoff: true,
		},
		{
			name: "completion recovery",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: CommitPolicySlice.String()}
			},
			disposition: InterruptedSliceCompletionRecovery, effective: InterruptedSliceCompletionRecovery,
			fixedRoot: root, repair: ExecutionBoundaryRepairCompletion,
		},
		{
			name: "blocked completion recovery",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.State.Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: CommitPolicySlice.String()}
				input.ContinueBlocked = true
			},
			disposition: InterruptedSliceBlockedContinue, effective: InterruptedSliceCompletionRecovery,
			fixedRoot: root, repair: ExecutionBoundaryRepairCompletion,
		},
		{
			name: "manual completion",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
				input.Detail.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
				input.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
			},
			disposition: InterruptedSliceManualCompletion, effective: InterruptedSliceManualCompletion,
			fixedRoot: root, repair: ExecutionBoundaryRepairManualCompletion,
		},
		{
			name: "blocked manual completion",
			mutate: func(input *InterruptedSliceInput) {
				input.Detail.State.Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].Status = plan.StatusBlocked
				input.Detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
				input.Detail.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
				input.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
				input.ContinueBlocked = true
			},
			disposition: InterruptedSliceBlockedContinue, effective: InterruptedSliceManualCompletion,
			fixedRoot: root, repair: ExecutionBoundaryRepairManualCompletion,
		},
		{
			name: "refusal",
			mutate: func(input *InterruptedSliceInput) {
				input.Branch = "other"
			},
			disposition: InterruptedSliceRefuse, effective: InterruptedSliceRefuse,
			repair: ExecutionBoundaryRepairNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := interruptedInput()
			input.Detail.State.Plan.LastRunStartingDirty = []string{"captured.go"}
			if tt.mutate != nil {
				tt.mutate(&input)
			}
			before, err := json.Marshal(input.Detail)
			if err != nil {
				t.Fatal(err)
			}
			spy := &executionBoundaryEffectSpy{}

			action := classifyExecutionBoundary(input)

			if action.Disposition != tt.disposition || action.EffectiveDisposition != tt.effective {
				t.Fatalf("dispositions = %q/%q, want %q/%q; diagnostics=%#v", action.Disposition, action.EffectiveDisposition, tt.disposition, tt.effective, action.Diagnostics)
			}
			if action.FixedRoot != tt.fixedRoot || action.RepairRequirement != tt.repair {
				t.Fatalf("boundary action = root:%q repair:%q, want root:%q repair:%q", action.FixedRoot, action.RepairRequirement, tt.fixedRoot, tt.repair)
			}
			if tt.fixedRoot != "" {
				if action.StartingBranch != input.Branch || len(action.StartingDirtyPaths) != 1 || action.StartingDirtyPaths[0] != "captured.go" {
					t.Fatalf("captured start metadata = branch:%q dirty:%v", action.StartingBranch, action.StartingDirtyPaths)
				}
			} else if action.StartingBranch != "" || len(action.StartingDirtyPaths) != 0 {
				t.Fatalf("unfixed action captured start metadata = branch:%q dirty:%v", action.StartingBranch, action.StartingDirtyPaths)
			}
			if action.AllowWorkspacePreparation != tt.prepare || action.AllowAgentHandoff != tt.handoff {
				t.Fatalf("effect permissions = prepare:%t handoff:%t, want prepare:%t handoff:%t", action.AllowWorkspacePreparation, action.AllowAgentHandoff, tt.prepare, tt.handoff)
			}
			if action.Diagnostics.Reason == "" || action.Diagnostics.Facts.SliceID != input.SliceID {
				t.Fatalf("diagnostics = %#v, want reason and selected slice facts", action.Diagnostics)
			}
			after, err := json.Marshal(input.Detail)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("action construction mutated durable plan facts")
			}
			spy.requireNoEffects(t)
		})
	}
}

func TestExecutionBoundaryPreservesLegacyInferenceAndPostIntentRefusals(t *testing.T) {
	t.Run("legacy boundary inference", func(t *testing.T) {
		input := interruptedInput()
		input.Detail.Slices.Slices[0].ExecutionStart.CommitPolicy = ""
		input.Detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = ""
		input.Detail.State.Plan.LastRunCommitPolicy = ""

		action := classifyExecutionBoundary(input)
		if action.EffectiveDisposition != InterruptedSliceResume || action.Diagnostics.Facts.CommitPolicy != CommitPolicySlice.String() || action.Diagnostics.Facts.WorkspaceStrategy != plan.WorkspaceStrategyWorktree {
			t.Fatalf("legacy boundary action = %#v", action)
		}
	})

	t.Run("post-intent recovery never hands off", func(t *testing.T) {
		input := interruptedInput()
		input.Detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: CommitPolicySlice.String()}
		input.Branch = "drifted"
		input.Head = "advanced"
		input.PorcelainStatus = "UU conflict.go"
		input.ActiveGitOperation = "rebase"

		action := classifyExecutionBoundary(input)
		if action.EffectiveDisposition != InterruptedSliceCompletionRecovery || action.RepairRequirement != ExecutionBoundaryRepairCompletion || action.AllowWorkspacePreparation || action.AllowAgentHandoff {
			t.Fatalf("post-intent action = %#v", action)
		}
	})

	t.Run("manual ownership refuses preparation and handoff", func(t *testing.T) {
		input := interruptedInput()
		input.Detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
		input.Detail.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
		input.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
		input.PorcelainStatus = " M manual.go"

		action := classifyExecutionBoundary(input)
		if action.EffectiveDisposition != InterruptedSliceManualCompletion || action.RepairRequirement != ExecutionBoundaryRepairManualCompletion || action.AllowWorkspacePreparation || action.AllowAgentHandoff {
			t.Fatalf("manual action = %#v", action)
		}
	})
}

func TestExecutionBoundarySetupSkipsPreparationEffectsForTerminalActions(t *testing.T) {
	tests := []struct {
		name   string
		config ExecutionConfig
		mutate func(*plan.PlanDetail)
		runner func(*testing.T, string, *int) CommandRunner
	}{
		{
			name: "refusal",
			config: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
				ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
			}},
			mutate: func(*plan.PlanDetail) {},
			runner: func(t *testing.T, root string, gitCalls *int) CommandRunner {
				base := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "other", "base")
				return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
					*gitCalls++
					return base(ctx, cwd, name, args, stdout, stderr)
				}
			},
		},
		{
			name: "manual ownership",
			config: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
				ExecutionMode: ExecutionModeCurrent, CommitPolicy: CommitPolicyNone,
			}},
			mutate: func(detail *plan.PlanDetail) {
				slice := &detail.Slices.Slices[0]
				slice.ExecutionStart.CommitPolicy = CommitPolicyNone.String()
				slice.ExecutionStart.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
				detail.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
			},
			runner: func(t *testing.T, root string, gitCalls *int) CommandRunner {
				base := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M manual.go\n" }, "tao/plan-a", "base")
				return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
					*gitCalls++
					return base(ctx, cwd, name, args, stdout, stderr)
				}
			},
		},
		{
			name: "post-intent recovery",
			config: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
				ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
			}},
			mutate: func(detail *plan.PlanDetail) {
				detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: CommitPolicySlice.String()}
			},
			runner: func(_ *testing.T, _ string, gitCalls *int) CommandRunner {
				return func(context.Context, string, string, []string, io.Writer, io.Writer) error {
					*gitCalls++
					return errors.New("post-intent recovery must not inspect Git")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			tt.mutate(detail)
			var spy struct {
				preparation  int
				rebase       int
				dependencies int
				persistence  int
			}
			gitCalls := 0
			service := NewService(&memoryRunRepository{}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner: tt.runner(t, root, &gitCalls),
				RootResolver: ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) {
					spy.preparation++
					return "", errors.New("terminal boundary action must not resolve an ordinary execution root")
				}),
				WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
					spy.preparation++
					spy.rebase++
					spy.dependencies++
					return "", errors.New("terminal boundary action must not prepare a workspace")
				},
				PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) {
					spy.persistence++
					return nil, errors.New("terminal boundary action must not persist")
				},
			}})

			_, err := service.prepareRunExecution(context.Background(), detail, tt.config)
			if err == nil {
				t.Fatal("terminal boundary action unexpectedly prepared an execution")
			}
			if spy.preparation != 0 || spy.rebase != 0 || spy.dependencies != 0 || spy.persistence != 0 {
				t.Fatalf("terminal boundary effects = prepare:%d rebase:%d dependencies:%d persistence:%d", spy.preparation, spy.rebase, spy.dependencies, spy.persistence)
			}
			if tt.name == "post-intent recovery" && gitCalls != 0 {
				t.Fatalf("post-intent Git calls = %d, want none", gitCalls)
			}
		})
	}
}

func classifyExecutionBoundary(input InterruptedSliceInput) ExecutionBoundaryAction {
	return (ExecutionBoundaryController{}).Classify(
		ExecutionBoundaryDurableFacts{Detail: input.Detail, SliceID: input.SliceID, ContinueBlocked: input.ContinueBlocked},
		ExecutionBoundaryLiveFacts{
			ExecutionRoot: input.ExecutionRoot, WorkspaceStrategy: input.WorkspaceStrategy, CommitPolicy: input.CommitPolicy,
			Branch: input.Branch, Head: input.Head, PorcelainStatus: input.PorcelainStatus, ActiveGitOperation: input.ActiveGitOperation,
		},
	)
}

// executionBoundaryEffectSpy is shared scaffolding for later controller-routing
// tests. Pure action construction has no collaborator through which to invoke
// these effects; the counters make that contract explicit.
type executionBoundaryEffectSpy struct {
	workspace   int
	git         int
	persistence int
	agent       int
}

func (spy *executionBoundaryEffectSpy) requireNoEffects(t *testing.T) {
	t.Helper()
	if spy.workspace != 0 || spy.git != 0 || spy.persistence != 0 || spy.agent != 0 {
		t.Fatalf("boundary effects = workspace:%d git:%d persistence:%d agent:%d", spy.workspace, spy.git, spy.persistence, spy.agent)
	}
}
