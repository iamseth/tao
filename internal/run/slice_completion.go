package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

// SliceCompletionRequest contains the validated report used by the recoverable
// slice commit transaction.
type SliceCompletionRequest struct {
	Record              *plan.PlanRecord
	SliceID             string
	Notes               string
	VerificationResults []plan.VerificationRun
	Now                 time.Time
}

// SliceCompletionService owns the intent -> Git -> completion transaction.
type SliceCompletionService struct {
	CommandRunner commandrunner.Runner
	Output        io.Writer
}

// Complete creates or recovers a slice-policy commit, then persists completion.
// Historical plan policy keeps its legacy metadata-only behavior until that
// policy is retired; none records explicit manual ownership without mutating Git.
func (s SliceCompletionService) Complete(ctx context.Context, request SliceCompletionRequest) error {
	if request.Record == nil || request.Record.Detail() == nil {
		return fmt.Errorf("slice completion requires a plan record")
	}
	detail := request.Record.Detail()
	slice := completionSlice(detail, request.SliceID)
	if slice == nil {
		return fmt.Errorf("slice %s not found", request.SliceID)
	}
	policy := strings.TrimSpace(detail.State.Plan.LastRunCommitPolicy)
	if policy == "" || policy == CommitPolicyPlan.String() {
		return request.Record.CompleteSlice(request.SliceID, request.Notes, request.VerificationResults, request.Now)
	}
	if policy != CommitPolicySlice.String() && policy != CommitPolicyNone.String() {
		return fmt.Errorf("slice %s has unsupported commit policy %q", request.SliceID, policy)
	}

	hash, err := sliceCompletionHash(detail.State.Plan.ID, request.SliceID, policy, request.Notes, request.VerificationResults)
	if err != nil {
		return err
	}
	if slice.CommitIntent != nil && slice.CommitIntent.Hash != hash {
		return fmt.Errorf("slice %s has a conflicting commit intent", request.SliceID)
	}
	if slice.Completion != nil {
		if slice.CommitIntent == nil || slice.CommitIntent.Hash != hash {
			return fmt.Errorf("slice %s has conflicting completion metadata", request.SliceID)
		}
		completedAt := request.Now
		if slice.Timing.CompletedAt != nil {
			completedAt = *slice.Timing.CompletedAt
		}
		return request.Record.CompleteSliceWithOutcome(request.SliceID, request.Notes, request.VerificationResults, *slice.Completion, completedAt)
	}

	root := strings.TrimSpace(slice.ExecutionRoot)
	if root == "" && detail.State.Workspace != nil {
		root = strings.TrimSpace(detail.State.Workspace.Root)
		if root == "" {
			root = strings.TrimSpace(detail.State.Workspace.Path)
		}
	}
	if root == "" {
		root = detail.State.Repo.Root
	}
	git := gitops.NewClient(root, s.CommandRunner)
	intent := slice.CommitIntent
	if intent == nil {
		branch, err := git.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("capture slice completion branch: %w", err)
		}
		head, err := git.RevParse(ctx, "HEAD")
		if err != nil {
			return fmt.Errorf("capture slice completion head: %w", err)
		}
		message := ""
		if policy == CommitPolicySlice.String() {
			message = sliceCommitMessage(detail, slice, request.VerificationResults)
			if slice.ExecutionStart != nil {
				if slice.ExecutionStart.Branch != "" {
					branch = slice.ExecutionStart.Branch
				}
				if slice.ExecutionStart.Head != "" {
					head = slice.ExecutionStart.Head
				}
			}
		}
		intent = &plan.SliceCommitIntent{Hash: hash, Policy: policy, StartingBranch: branch, StartingHead: head, Message: message, CreatedAt: request.Now}
		if err := request.Record.RecordSliceCommitIntent(request.SliceID, *intent); err != nil {
			return fmt.Errorf("record slice commit intent: %w", err)
		}
	}

	if policy == CommitPolicyNone.String() {
		return request.Record.CompleteSliceWithOutcome(request.SliceID, request.Notes, request.VerificationResults, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionManualUncommitted}, request.Now)
	}
	if gitops.ProtectedBranch(intent.StartingBranch) || intent.StartingBranch == "" {
		return fmt.Errorf("slice commit refused: unsafe execution branch %q", intent.StartingBranch)
	}
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("inspect slice completion branch: %w", err)
	}
	if branch != intent.StartingBranch {
		return fmt.Errorf("slice commit refused: execution branch changed from %q to %q", intent.StartingBranch, branch)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("inspect slice completion head: %w", err)
	}
	if head != intent.StartingHead {
		return s.recoverCommit(ctx, git, request, *intent, head)
	}

	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect slice completion paths: %w", err)
	}
	classification := classifyGitStatus(status, nil)
	if len(classification.AmbiguousLines) > 0 {
		return fmt.Errorf("slice commit refused: ambiguous git status entry %q", classification.AmbiguousLines[0])
	}
	paths := sortedUniquePlanCommitPaths(classification.CommitCandidates)
	unexpected := unexpectedPlanCommitPaths(paths, expectedPlanCommitPaths(detail, request.SliceID))
	if err := commitSafetyScreenError(paths, nil); err != nil {
		return fmt.Errorf("slice commit refused: %w", err)
	}
	if len(paths) == 0 {
		return request.Record.CompleteSliceWithOutcome(request.SliceID, request.Notes, request.VerificationResults, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionNoChanges, CommitSHA: head}, request.Now)
	}
	if len(classification.TaoStagedPaths) > 0 {
		if err := git.RestoreStaged(ctx, sortedUniquePlanCommitPaths(classification.TaoStagedPaths)...); err != nil {
			return fmt.Errorf("unstage Tao metadata: %w", err)
		}
	}
	if err := git.Add(ctx, paths...); err != nil {
		return fmt.Errorf("stage slice completion paths: %w", err)
	}
	if err := git.Commit(ctx, intent.Message); err != nil {
		return fmt.Errorf("create slice completion commit: %w", err)
	}
	commitSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve slice completion commit: %w", err)
	}
	if commitSHA == intent.StartingHead {
		return fmt.Errorf("slice completion commit did not advance HEAD")
	}
	if err := writeSliceCompletionUnexpectedWarning(s.Output, unexpected); err != nil {
		return err
	}
	return request.Record.CompleteSliceWithOutcome(request.SliceID, request.Notes, request.VerificationResults, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: commitSHA}, request.Now)
}

func (s SliceCompletionService) recoverCommit(ctx context.Context, git gitops.Client, request SliceCompletionRequest, intent plan.SliceCommitIntent, head string) error {
	parent, err := git.RevParse(ctx, head+"^")
	if err != nil || parent != intent.StartingHead {
		return fmt.Errorf("slice commit recovery refused: HEAD %s does not match intent starting at %s", head, intent.StartingHead)
	}
	message, err := git.CommitMessage(ctx, head)
	if err != nil {
		return fmt.Errorf("inspect slice completion commit: %w", err)
	}
	if strings.TrimSpace(message) != strings.TrimSpace(intent.Message) {
		return fmt.Errorf("slice commit recovery refused: HEAD commit does not match recorded intent")
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect recovered slice completion worktree: %w", err)
	}
	classification := classifyGitStatus(status, nil)
	if len(classification.CommitCandidates) > 0 || len(classification.AmbiguousLines) > 0 {
		return fmt.Errorf("slice commit recovery refused: worktree is not clean")
	}
	return request.Record.CompleteSliceWithOutcome(request.SliceID, request.Notes, request.VerificationResults, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: head}, request.Now)
}

func writeSliceCompletionUnexpectedWarning(out io.Writer, unexpected []string) error {
	if out == nil || len(unexpected) == 0 {
		return nil
	}
	if err := writef(out, "Warning: committed path(s) outside completed slice expected_files: %s\n", strings.Join(unexpected, ", ")); err != nil {
		return fmt.Errorf("write slice completion warning: %w", err)
	}
	return nil
}

func completionSlice(detail *plan.PlanDetail, id string) *plan.Slice {
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == id {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}

func sliceCompletionHash(planID, sliceID, policy, notes string, results []plan.VerificationRun) (string, error) {
	payload, err := json.Marshal(struct {
		PlanID  string                 `json:"plan_id"`
		SliceID string                 `json:"slice_id"`
		Policy  string                 `json:"policy"`
		Notes   string                 `json:"notes"`
		Results []plan.VerificationRun `json:"results"`
	}{planID, sliceID, policy, notes, results})
	if err != nil {
		return "", fmt.Errorf("encode slice completion intent: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func sliceCommitMessage(detail *plan.PlanDetail, slice *plan.Slice, results []plan.VerificationRun) string {
	title := strings.Join(strings.Fields(slice.Title), " ")
	var lines []string
	for _, result := range results {
		lines = append(lines, fmt.Sprintf("- %s (%s)", strings.Join(strings.Fields(result.Command), " "), strings.Join(strings.Fields(result.Result), " ")))
	}
	sort.Strings(lines)
	verification := "- not reported"
	if len(lines) > 0 {
		verification = strings.Join(lines, "\n")
	}
	return fmt.Sprintf("chore(tao): complete %s — %s\n\nVerification:\n%s\n\nTao-Plan: %s\nTao-Slice: %s", slice.ID, title, verification, detail.State.Plan.ID, slice.ID)
}
