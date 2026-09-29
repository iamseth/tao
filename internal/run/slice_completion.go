package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	commitcontract "github.com/iamseth/tao/internal/commit"
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
	VerificationClaims  []VerificationClaim
	CommitProposal      *commitcontract.Proposal
	Now                 time.Time
}

// SliceCompletionService owns the intent -> Git -> completion transaction.
type SliceCompletionService struct {
	CommandRunner commandrunner.Runner
	Output        io.Writer
}

// Complete observes declared gates before any new intent. Only a recorded
// historical intent can enter claim-based historical settlement.
func (s SliceCompletionService) Complete(ctx context.Context, request SliceCompletionRequest) error {
	return s.CompleteVerified(ctx, request)
}

// UsesHistoricalCompletionInputs selects the original decoder only for a
// pre-verification intent. The transaction rechecks this after locked reload.
func UsesHistoricalCompletionInputs(detail *plan.PlanDetail, sliceID string) bool {
	if detail == nil {
		return false
	}
	slice := completionSlice(detail, sliceID)
	return slice != nil && slice.CommitIntent != nil && slice.CommitIntent.Verification == nil && !strings.HasPrefix(slice.CommitIntent.Hash, verifiedCompletionHashPrefix)
}

func (s SliceCompletionService) settleHistoricalCompletion(ctx context.Context, request SliceCompletionRequest) error {
	if request.Record == nil || request.Record.Detail() == nil {
		return fmt.Errorf("slice completion requires a plan record")
	}
	detail := request.Record.Detail()
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	slice := completionSlice(detail, request.SliceID)
	if slice == nil {
		return fmt.Errorf("slice %s not found", request.SliceID)
	}
	policy := strings.TrimSpace(detail.State.Plan.LastRunCommitPolicy)
	if policy != CommitPolicySlice.String() && policy != CommitPolicyNone.String() {
		return fmt.Errorf("slice %s has unsupported commit policy %q", request.SliceID, policy)
	}

	if !UsesHistoricalCompletionInputs(detail, request.SliceID) {
		return fmt.Errorf("historical settlement requires a recorded historical intent")
	}
	intent := slice.CommitIntent
	if intent.Policy != policy {
		return fmt.Errorf("historical intent policy differs from recorded run policy")
	}
	message := ""
	legacyIntent := false
	if policy == CommitPolicySlice.String() {
		// Persisted intent is the recovery authority, verbatim.
		message = intent.Message
	}
	hash, err := sliceCompletionHash(detail.State.Plan.ID, request.SliceID, policy, request.Notes, request.VerificationResults, message)
	if err != nil {
		return err
	}
	legacyHash, err := legacySliceCompletionHash(detail.State.Plan.ID, request.SliceID, policy, request.Notes, request.VerificationResults)
	if err != nil {
		return err
	}
	if intent != nil {
		legacyIntent = intent.Hash == legacyHash
		if intent.Hash != hash && !legacyIntent {
			return fmt.Errorf("slice %s has a conflicting commit intent", request.SliceID)
		}
		if policy == CommitPolicySlice.String() && request.CommitProposal != nil && !legacyIntent {
			proposedMessage, err := formatSliceCommitMessage(detail.State.Plan.ID, request.SliceID, *request.CommitProposal)
			if err != nil {
				return err
			}
			if proposedMessage != intent.Message {
				return fmt.Errorf("slice %s has a conflicting commit proposal", request.SliceID)
			}
		}
	}
	if slice.Completion != nil {
		if intent == nil || (intent.Hash != hash && !legacyIntent) {
			return fmt.Errorf("slice %s has conflicting completion metadata", request.SliceID)
		}
		completedAt := request.Now
		if slice.Timing.CompletedAt != nil {
			completedAt = *slice.Timing.CompletedAt
		}
		return persistSliceCompletion(request, slice.Completion, completedAt)
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

	return s.finishSliceCompletion(ctx, git, request, *intent, legacyIntent, func() error { return nil })
}

// Shared Git policy for historical and verified transactions. The verified caller
// supplies a synchronous lifetime/boundary check before every mutation.
func (s SliceCompletionService) finishSliceCompletion(ctx context.Context, git gitops.Client, request SliceCompletionRequest, intent plan.SliceCommitIntent, legacyIntent bool, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	if intent.Policy == CommitPolicyNone.String() {
		// Historical manual intents retain their original settlement semantics.
		// A verified fingerprint alone cannot detect a different clean tree.
		if intent.Verification != nil {
			branch, err := git.CurrentBranch(ctx)
			if err != nil {
				return fmt.Errorf("inspect slice completion branch: %w", err)
			}
			if branch != intent.StartingBranch {
				return fmt.Errorf("slice completion refused: execution branch changed from %q to %q", intent.StartingBranch, branch)
			}
			head, err := git.RevParse(ctx, "HEAD")
			if err != nil {
				return fmt.Errorf("inspect slice completion head: %w", err)
			}
			if head != intent.StartingHead {
				return fmt.Errorf("slice completion refused: execution HEAD changed from %q to %q", intent.StartingHead, head)
			}
		}
		if err := checkFrozenVerificationWorktree(ctx, git, intent); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
		outcome := plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionManualUncommitted}
		return persistSliceCompletion(request, &outcome, request.Now)
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
		return s.recoverCommit(ctx, git, request, intent, head)
	}

	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect slice completion paths: %w", err)
	}
	classification := commitcontract.ClassifyStatus(status, nil)
	if len(classification.AmbiguousLines) > 0 {
		return fmt.Errorf("slice commit refused: ambiguous git status entry %q", classification.AmbiguousLines[0])
	}
	paths := commitcontract.UniquePaths(classification.CommitCandidates)
	unexpected := commitcontract.UnexpectedPaths(paths, expectedPlanCommitPaths(request.Record.Detail(), request.SliceID))
	if err := commitcontract.SafetyError(paths, nil); err != nil {
		return fmt.Errorf("slice commit refused: %w", err)
	}
	if err := checkFrozenVerificationWorktree(ctx, git, intent); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if len(paths) == 0 {
		outcome := plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionNoChanges, CommitSHA: head}
		return persistSliceCompletion(request, &outcome, request.Now)
	}
	if len(classification.TaoStagedPaths) > 0 {
		if err := git.RestoreStaged(ctx, commitcontract.UniquePaths(classification.TaoStagedPaths)...); err != nil {
			return fmt.Errorf("unstage Tao metadata: %w", err)
		}
	}
	if err := check(); err != nil {
		return err
	}
	if err := git.Add(ctx, paths...); err != nil {
		return fmt.Errorf("stage slice completion paths: %w", err)
	}
	if err := checkFrozenVerificationWorktree(ctx, git, intent); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	commitSHA := ""
	if legacyIntent {
		// Historical intents predate the central message contract. Their exact
		// recorded message remains authoritative and must not be revalidated.
		if err := git.Commit(ctx, intent.Message); err != nil {
			return fmt.Errorf("create legacy slice completion commit: %w", err)
		}
		commitSHA, err = git.RevParse(ctx, "HEAD")
		if err != nil {
			return fmt.Errorf("resolve legacy slice completion commit: %w", err)
		}
	} else {
		result, err := commitcontract.CommitPrepared(ctx, git, intent.Message)
		if err != nil {
			return fmt.Errorf("create slice completion commit: %w", err)
		}
		commitSHA = result.SHA
	}
	if commitSHA == intent.StartingHead {
		return fmt.Errorf("slice completion commit did not advance HEAD")
	}
	if err := writeSliceCompletionUnexpectedWarning(s.Output, unexpected); err != nil {
		return err
	}
	outcome := plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: commitSHA}
	return persistSliceCompletion(request, &outcome, request.Now)
}

func expectedPlanCommitPaths(detail *plan.PlanDetail, additionallyCompleted ...string) commitcontract.ExpectedPaths {
	exact := map[string]bool{}
	var patterns []string
	completed := map[string]bool{}
	for _, id := range detail.State.Plan.CompletedSlices {
		completed[id] = true
	}
	for _, id := range additionallyCompleted {
		completed[id] = true
	}
	for _, slice := range detail.Slices.Slices {
		if !completed[slice.ID] {
			continue
		}
		for _, path := range slice.ExpectedFiles {
			path = commitcontract.NormalizePath(path)
			if commitcontract.HasPathGlobMeta(path) {
				patterns = append(patterns, path)
				continue
			}
			exact[path] = true
		}
	}
	for path := range exact {
		patterns = append(patterns, path)
	}
	return commitcontract.NewExpectedPaths(patterns...)
}

func persistSliceCommitIntent(request SliceCompletionRequest, intent plan.SliceCommitIntent) error {
	if err := plan.RequireNotAbandoned(request.Record.Detail()); err != nil {
		return err
	}
	return request.Record.RecordSliceCommitIntent(request.SliceID, intent)
}

func persistSliceCompletion(request SliceCompletionRequest, outcome *plan.SliceCompletionOutcome, completedAt time.Time) error {
	if err := plan.RequireNotAbandoned(request.Record.Detail()); err != nil {
		return err
	}
	if outcome == nil {
		return request.Record.CompleteSlice(request.SliceID, request.Notes, request.VerificationResults, completedAt)
	}
	return request.Record.CompleteSliceWithOutcome(request.SliceID, request.Notes, request.VerificationResults, *outcome, completedAt)
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
	if strings.TrimRight(message, "\n") != intent.Message {
		return fmt.Errorf("slice commit recovery refused: HEAD commit does not match recorded intent")
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect recovered slice completion worktree: %w", err)
	}
	classification := commitcontract.ClassifyStatus(status, nil)
	if len(classification.CommitCandidates) > 0 || len(classification.AmbiguousLines) > 0 {
		return fmt.Errorf("slice commit recovery refused: worktree is not clean")
	}
	outcome := plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: head}
	return persistSliceCompletion(request, &outcome, request.Now)
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

func sliceCompletionHash(planID, sliceID, policy, notes string, results []plan.VerificationRun, message string) (string, error) {
	payload, err := json.Marshal(struct {
		PlanID  string                 `json:"plan_id"`
		SliceID string                 `json:"slice_id"`
		Policy  string                 `json:"policy"`
		Notes   string                 `json:"notes"`
		Results []plan.VerificationRun `json:"results"`
		Message string                 `json:"message"`
	}{planID, sliceID, policy, notes, results, message})
	if err != nil {
		return "", fmt.Errorf("encode slice completion intent: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func legacySliceCompletionHash(planID, sliceID, policy, notes string, results []plan.VerificationRun) (string, error) {
	payload, err := json.Marshal(struct {
		PlanID  string                 `json:"plan_id"`
		SliceID string                 `json:"slice_id"`
		Policy  string                 `json:"policy"`
		Notes   string                 `json:"notes"`
		Results []plan.VerificationRun `json:"results"`
	}{planID, sliceID, policy, notes, results})
	if err != nil {
		return "", fmt.Errorf("encode legacy slice completion intent: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func formatSliceCommitMessage(planID, sliceID string, proposal commitcontract.Proposal) (string, error) {
	planTrailer, err := commitcontract.NewTrustedTrailer("Tao-Plan", planID)
	if err != nil {
		return "", fmt.Errorf("format slice commit proposal: %w", err)
	}
	sliceTrailer, err := commitcontract.NewTrustedTrailer("Tao-Slice", sliceID)
	if err != nil {
		return "", fmt.Errorf("format slice commit proposal: %w", err)
	}
	message, err := commitcontract.Format(proposal, planTrailer, sliceTrailer)
	if err != nil {
		return "", fmt.Errorf("validate slice commit proposal: %w", err)
	}
	return message, nil
}
