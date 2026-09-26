package run

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/reviewcontract"
)

// Each caller retains its own durable consumption and failure-settlement
// authority. In particular, fresh reviews settle by compare-and-swap, while
// historical approvals may supersede an exact pre-correction workspace failure.
type reviewProposalCorrectionStrategy struct {
	session    AgentSessionExecutor
	recorder   func() (ReviewProposalCorrectionRecorder, error)
	consume    func(ReviewProposalCorrectionRecorder, plan.FinalizationFailure) error
	settle     func(plan.FinalizationFailure, string, error) error
	reinspect  func(context.Context) (string, error)
	promptData reviewPromptData
	agentLabel string
	now        func() time.Time
}

func correctReviewProposal(ctx context.Context, request AgentSessionRequest, review plan.PlanReview, strategy reviewProposalCorrectionStrategy) (plan.PlanReview, error) {
	prompt, err := renderReviewPrompt(strategy.promptData)
	if err != nil {
		return plan.PlanReview{}, strategy.settle(plan.FinalizationFailure{}, plan.FinalizationCategoryProposalPromptFailed, err)
	}
	recorder, err := strategy.recorder()
	if err != nil {
		return plan.PlanReview{}, err
	}
	consumed := plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhaseProposalRepair, Category: plan.FinalizationCategoryProposalCorrectionStarted,
		ReviewBase: strings.TrimSpace(review.Base), ReviewHead: strings.TrimSpace(review.Head),
		FailedAt: strategy.now().UTC(), RecoveryAction: plan.FinalizationRecoveryRerunReview,
	}
	if err := strategy.consume(recorder, consumed); err != nil {
		return plan.PlanReview{}, fmt.Errorf("record consumed proposal correction attempt: %w", err)
	}
	request.Prompt = prompt
	request.CaptureOutput = true
	request.Metrics = &AgentSessionMetricsRequest{Role: plan.AgentRoleReview}
	result, sessionErr := strategy.session.RunAgentSession(ctx, request)
	// Worktree mutations take precedence even when the session itself failed.
	if category, err := strategy.reinspect(ctx); err != nil {
		return plan.PlanReview{}, strategy.settle(consumed, category, err)
	}
	if sessionErr != nil {
		return plan.PlanReview{}, strategy.settle(consumed, plan.FinalizationCategoryProposalCorrectionFailed, sessionErr)
	}
	proposal := reviewcontract.ParseCommitProposal(result.Output, strategy.promptData.ChangeType)
	if proposal == nil {
		return plan.PlanReview{}, strategy.settle(consumed, plan.FinalizationCategoryProposalInvalid, fmt.Errorf("proposal-only correction did not return a valid typed commit proposal"))
	}
	corrected := review
	corrected.CommitMessage = proposal
	if err := recorder.RecordReviewProposalCorrection(consumed, corrected, strategy.agentLabel); err != nil {
		return plan.PlanReview{}, strategy.settle(consumed, plan.FinalizationCategoryProposalRecordingFailed, err)
	}
	return corrected, nil
}

type proposalCorrectionBoundary struct {
	recordedBranch, recordedHead string
	capturedBranch, capturedHead string
}

// Identity gates and Git adapters stay with each caller to retain their
// different preconditions and diagnostics. Historical boundaries are required
// and exact; fresh reviews allow absent workspace metadata and trim intent keys.
func reinspectReviewProposalCorrection(ctx context.Context, detail *plan.PlanDetail,
	identity func(context.Context) (string, error),
	read func(context.Context) (branch, head, status string, err error),
	boundary func() proposalCorrectionBoundary, strict bool, dirtyAction string,
) (string, error) {
	if category, err := identity(ctx); err != nil {
		return category, err
	}
	liveBranch, liveHead, status, err := read(ctx)
	if err != nil {
		return plan.FinalizationCategoryWorkspacePreflightFailed, err
	}
	b := boundary()
	var drift bool
	if strict {
		drift = b.recordedBranch == "" || b.recordedHead == "" || liveBranch != b.capturedBranch || liveHead != b.capturedHead || liveBranch != b.recordedBranch || liveHead != b.recordedHead
	} else {
		drift = liveHead != strings.TrimSpace(b.capturedHead) || (b.recordedHead != "" && liveHead != b.recordedHead) || (b.capturedBranch != "" && liveBranch != b.capturedBranch) || (b.recordedBranch != "" && liveBranch != b.recordedBranch)
	}
	if drift {
		return plan.FinalizationCategoryHeadDrift, fmt.Errorf("proposal correction changed the reviewed worktree boundary: recorded branch %q HEAD %s; captured branch %q HEAD %s; live branch %q HEAD %s", b.recordedBranch, diagnosticSHA(b.recordedHead), b.capturedBranch, diagnosticSHA(b.capturedHead), liveBranch, diagnosticSHA(liveHead))
	}
	if strings.TrimSpace(status) != "" {
		return plan.FinalizationCategoryWorkspaceDirty, fmt.Errorf("proposal correction left the reviewed worktree dirty; refusing %s", dirtyAction)
	}
	if intent := detail.State.Plan.PullRequestIntent; intent != nil {
		branch, head := intent.Branch, intent.HeadSHA
		if !strict {
			branch, head = strings.TrimSpace(branch), strings.TrimSpace(head)
		}
		if liveBranch != branch || liveHead != head {
			return plan.FinalizationCategoryIntentMismatch, fmt.Errorf("proposal correction worktree branch %q HEAD %s does not match recorded intent branch %q HEAD %s", liveBranch, diagnosticSHA(liveHead), intent.Branch, diagnosticSHA(intent.HeadSHA))
		}
	}
	return "", nil
}
