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
	failure := plan.CurrentFailedFinalVerification(detail)
	if failure == nil {
		return fmt.Errorf("verification repair requires current failed final-verification evidence")
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
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("inspect verification repair branch: %w", err)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("inspect verification repair head: %w", err)
	}
	head = strings.TrimSpace(head)
	if detail.State.Workspace == nil || strings.TrimSpace(branch) == "" || branch != detail.State.Workspace.Branch || head != failure.HeadSHA {
		return fmt.Errorf("verification repair worktree boundary is stale: branch %q head %s does not match recorded failed head %s", branch, diagnosticSHA(head), diagnosticSHA(failure.HeadSHA))
	}
	record, err := planMutationRecord(execution, detail)
	if err != nil {
		return fmt.Errorf("prepare verification repair: %w", err)
	}
	appender, ok := record.(interface {
		AppendVerificationRepair(plan.VerificationRepairRequest) error
	})
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
	return fmt.Errorf("final repository verification failed for current head %s; run `tao run --repair-verification %s` before review", diagnosticSHA(failure.HeadSHA), detail.State.Plan.ID)
}
