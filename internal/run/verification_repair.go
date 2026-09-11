package run

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

func appendVerificationRepair(ctx context.Context, detail *plan.PlanDetail, execution runExecution) error {
	if execution.Config.CommitPolicy != CommitPolicySlice {
		return fmt.Errorf("verification repair requires automatic slice commit policy")
	}
	if execution.Config.ExecutionMode != ExecutionModeIsolated {
		return fmt.Errorf("verification repair requires isolated execution mode")
	}
	decision := plan.DeriveVerificationRecovery(detail)
	if decision.Kind != plan.PlanActionRepairVerification {
		return fmt.Errorf("verification repair refused: %s", decision.Reason)
	}
	if detail.State.Plan.MergeCommitIntent != nil || detail.State.Plan.PullRequestIntent != nil {
		return fmt.Errorf("verification repair refuses unsettled post-slice intent")
	}
	for i := range detail.Slices.Slices {
		slice := &detail.Slices.Slices[i]
		if slice.CommitIntent != nil && slice.Completion == nil {
			return fmt.Errorf("verification repair refuses unsettled slice commit intent at %s", slice.ID)
		}
	}
	git := gitClient(execution, execution.ExecutionRoot)
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect verification repair worktree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("verification repair requires a clean worktree")
	}
	failure, err := requireCurrentFailedFinalVerificationBoundary(ctx, detail, execution, "verification repair")
	if err != nil {
		return err
	}
	record, err := planMutationRecord(execution, detail)
	if err != nil {
		return fmt.Errorf("prepare verification repair: %w", err)
	}
	appender, ok := record.(VerificationRepairAppender)
	if !ok {
		return fmt.Errorf("plan record does not support verification repair")
	}
	request := plan.VerificationRepairRequest{
		Binding:   plan.VerificationRepairBinding{Command: failure.Command, HeadSHA: failure.HeadSHA, Fingerprint: failure.Fingerprint},
		CreatedAt: now(execution).UTC(),
	}
	if err := appender.AppendVerificationRepair(request); err != nil {
		return fmt.Errorf("prepare verification repair: %w", err)
	}
	return nil
}

func requireReverifyFinalVerificationBoundary(ctx context.Context, detail *plan.PlanDetail, execution runExecution) (*plan.FinalVerification, error) {
	failure := plan.CurrentFailedFinalVerification(detail)
	if failure == nil {
		return nil, fmt.Errorf("reverification requires current failed final-verification evidence")
	}
	if !plan.AnalyzeRunCapabilities(detail).Complete {
		return nil, fmt.Errorf("reverification requires settled slice work with all slices complete")
	}
	git := gitClient(execution, execution.ExecutionRoot)
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect reverification worktree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return nil, fmt.Errorf("reverification requires a clean worktree")
	}
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect reverification branch: %w", err)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("inspect reverification head: %w", err)
	}
	head = strings.TrimSpace(head)
	if detail.State.Workspace == nil || strings.TrimSpace(branch) == "" || branch != detail.State.Workspace.Branch {
		return nil, fmt.Errorf("reverification worktree boundary is stale: branch %q does not match recorded branch", branch)
	}
	if head != failure.HeadSHA {
		descendsFromFailure, err := git.IsAncestor(ctx, failure.HeadSHA, head)
		if err != nil {
			return nil, fmt.Errorf("inspect reverification ancestry: %w", err)
		}
		if !descendsFromFailure {
			return nil, fmt.Errorf("reverification worktree boundary is stale: head %s is not the recorded failed head %s or its descendant", diagnosticSHA(head), diagnosticSHA(failure.HeadSHA))
		}
	}
	return failure, nil
}

func requireCurrentFailedFinalVerificationBoundary(ctx context.Context, detail *plan.PlanDetail, execution runExecution, operation string) (*plan.FinalVerification, error) {
	failure := plan.CurrentFailedFinalVerification(detail)
	if failure == nil {
		return nil, fmt.Errorf("%s requires current failed final-verification evidence", operation)
	}
	git := gitClient(execution, execution.ExecutionRoot)
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect %s branch: %w", operation, err)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("inspect %s head: %w", operation, err)
	}
	head = strings.TrimSpace(head)
	if detail.State.Workspace == nil || strings.TrimSpace(branch) == "" || branch != detail.State.Workspace.Branch || head != failure.HeadSHA {
		return nil, fmt.Errorf("%s worktree boundary is stale: branch %q head %s does not match recorded failed head %s", operation, branch, diagnosticSHA(head), diagnosticSHA(failure.HeadSHA))
	}
	return failure, nil
}

func requireNoCurrentFinalVerificationFailure(ctx context.Context, detail *plan.PlanDetail, execution runExecution) error {
	failure := plan.CurrentFailedFinalVerification(detail)
	if failure == nil {
		return nil
	}
	head, err := gitClient(execution, execution.ExecutionRoot).RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("inspect final-verification review gate: %w", err)
	}
	if strings.TrimSpace(head) != failure.HeadSHA {
		return nil
	}
	decision := plan.DeriveVerificationRecovery(detail)
	if command := strings.TrimSpace(decision.Command); command != "" {
		return fmt.Errorf("final repository verification failed for current head %s; run `%s` before review", diagnosticSHA(failure.HeadSHA), command)
	}
	if instruction := strings.TrimSpace(decision.Instruction); instruction != "" {
		return fmt.Errorf("final repository verification failed for current head %s; recovery required before review: %s", diagnosticSHA(failure.HeadSHA), instruction)
	}
	return fmt.Errorf("final repository verification failed for current head %s; recovery required before review: %s", diagnosticSHA(failure.HeadSHA), decision.Reason)
}
