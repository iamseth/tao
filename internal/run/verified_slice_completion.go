package run

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

const verifiedCompletionHashPrefix = "verified-v1:sha256:"

// CompleteVerified observes gates before intent; a durable intent owns exact recovery and
// never revives an old session or consults mutable latest-attempt evidence.
func (s SliceCompletionService) CompleteVerified(ctx context.Context, request SliceCompletionRequest) error {
	if request.Record == nil || request.Record.Detail() == nil {
		return fmt.Errorf("slice completion requires a plan record")
	}
	release, err := acquireSliceCompletionLock(request.Record.Dir())
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	if err := reloadCompletionRequest(ctx, &request); err != nil {
		return err
	}
	detail := request.Record.Detail()
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	slice := completionSlice(detail, request.SliceID)
	if slice == nil {
		return fmt.Errorf("slice %s not found", request.SliceID)
	}
	if err := s.repairCompletionTiming(ctx, request); err != nil {
		return err
	}
	if slice.CommitIntent != nil {
		if UsesHistoricalCompletionInputs(detail, request.SliceID) {
			return s.settleHistoricalCompletion(ctx, request)
		}
		return s.recoverVerifiedCompletion(ctx, request, *slice.CommitIntent)
	}
	if err := admitVerifiedCompletion(request); err != nil {
		return err
	}
	policy := detail.State.Plan.LastRunCommitPolicy
	if policy == CommitPolicySlice.String() {
		if request.CommitProposal == nil {
			return fmt.Errorf("slice %s requires a commit proposal before verification", request.SliceID)
		}
		if _, err := formatSliceCommitMessage(detail.State.Plan.ID, request.SliceID, *request.CommitProposal); err != nil {
			return err
		}
	}
	lifetime, err := BindSliceCompletionLifetime(ctx, request.Record.Dir(), request.SliceID)
	if err != nil {
		return err
	}
	defer func() { _ = lifetime.Close() }()
	ctx = lifetime.Context()
	before, err := s.inspectVerifiedCompletion(ctx, request)
	if err != nil {
		return err
	}
	if prior := slice.VerificationAttempt; prior != nil {
		settlement := request
		settlement.VerificationClaims = nil // New claims cannot describe an older invocation.
		if err := recordVerificationDiagnostics(settlement, *prior); err != nil {
			return err
		}
	}
	declaration := slice.Verification
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return fmt.Errorf("create verification attempt identity: %w", err)
	}
	runs, gateErr := (SliceVerifier{CommandRunner: s.CommandRunner}).Verify(ctx, SliceVerificationRequest{ExecutionRoot: before.root, Verification: declaration})
	snapshot := plan.SliceVerificationSnapshot{
		Version: plan.SliceVerificationSnapshotVersion, AttemptID: hex.EncodeToString(id),
		ExecutionRoot: before.root, StartingBranch: before.branch, StartingHead: before.head,
		WorktreeFingerprint: before.fingerprint, DeclarationDigest: before.declaration,
		Runs: runs, RecordedAt: time.Now().UTC(),
	}
	// Persist failed/cancelled attempts as diagnostics too. Persistence has no
	// lifetime authority and cannot advance the slice, stage, or record intent.
	if err := request.Record.RecordSliceVerification(request.SliceID, snapshot); err != nil {
		return fmt.Errorf("record slice verification: %w", err)
	}
	if err := recordVerificationDiagnostics(request, snapshot); err != nil {
		return err
	}
	if gateErr != nil {
		return fmt.Errorf("%w\nNo intent recorded. Repair only permitted slice/Plan-Owned Files in this active session and retry slice-complete. Otherwise use slice-blocked --gate-command with --failing-path for file-specific failures; invalid declarations use --invalid-command/--invalid-reason and, when applicable, --corrected-command. Do not commit or start another repair session", gateErr)
	}
	if s.Output != nil {
		for _, observed := range runs {
			_, _ = fmt.Fprintf(s.Output, "Gate %d: %s %s\n", observed.CommandIndex, observed.Result, verificationPresentation(observed.Command, 512))
			if observed.OriginalCommand != "" {
				_, _ = fmt.Fprintf(s.Output, "  corrected from: %s\n", verificationPresentation(observed.OriginalCommand, 512))
			}
		}
	}
	if err := lifetime.Check(); err != nil {
		return err
	}
	if err := reloadCompletionRequest(ctx, &request); err != nil {
		return err
	}
	if err := admitVerifiedCompletion(request); err != nil {
		return err
	}
	if err := s.repairCompletionTiming(ctx, request); err != nil {
		return err
	}
	after, err := s.inspectVerifiedCompletion(ctx, request)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("slice verification boundary or declarations changed; repair and rerun all gates")
	}
	current := completionSlice(request.Record.Detail(), request.SliceID)
	if !reflect.DeepEqual(current.VerificationAttempt, &snapshot) {
		return fmt.Errorf("slice verification attempt changed before intent")
	}
	message := ""
	if policy == CommitPolicySlice.String() {
		message, err = formatVerifiedSliceMessage(detail.State.Plan.ID, request.SliceID, *request.CommitProposal, snapshot)
		if err != nil {
			return err
		}
	}
	hash, err := verifiedSliceCompletionHash(request.Notes, message, snapshot)
	if err != nil {
		return err
	}
	intent := plan.SliceCommitIntent{Hash: hash, Policy: policy, StartingBranch: before.branch, StartingHead: before.head, Message: message, CreatedAt: snapshot.RecordedAt, Verification: &snapshot}
	if err := lifetime.Check(); err != nil {
		return err
	}
	if err := persistSliceCommitIntent(request, intent); err != nil {
		return err
	}
	request.VerificationResults = snapshot.Runs
	return s.finishSliceCompletion(ctx, gitops.NewClient(before.root, s.CommandRunner), request, intent, false, lifetime.Check)
}

// Timing repair is metadata-only, before both fresh gates and frozen recovery.
// Settlement independently resolves timing again inside the durable mutation.
func (s SliceCompletionService) repairCompletionTiming(ctx context.Context, request SliceCompletionRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, recovered, err := plan.ResolveSliceStartedAt(request.Record.Detail(), request.SliceID)
	if err != nil {
		return err
	}
	if !recovered {
		return nil
	}
	if err := request.Record.RepairSliceStartedAt(request.SliceID); err != nil {
		return err
	}
	if s.Output != nil {
		_, _ = fmt.Fprintf(s.Output, "Warning: restored slice %s started_at from durable slice_started evidence\n", request.SliceID)
	}
	return nil
}

func reloadCompletionRequest(ctx context.Context, request *SliceCompletionRequest) error {
	record, err := plan.NewFileRepository("").ResolvePlanRecord(ctx, request.Record.Dir())
	if err != nil {
		return fmt.Errorf("reload slice completion: %w", err)
	}
	request.Record = record
	return nil
}

func admitVerifiedCompletion(request SliceCompletionRequest) error {
	detail := request.Record.Detail()
	if err := plan.RequireNotAbandoned(detail); err != nil {
		return err
	}
	slice := completionSlice(detail, request.SliceID)
	if slice == nil || detail.State.Status != plan.StatusInProgress || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != request.SliceID || slice.Status != plan.StatusInProgress || !slices.Contains(detail.State.Plan.PendingSlices, request.SliceID) {
		return fmt.Errorf("slice %s is not the selected in-progress slice", request.SliceID)
	}
	if slice.CommitIntent != nil || slice.Completion != nil {
		return fmt.Errorf("slice %s already has completion metadata", request.SliceID)
	}
	if slice.Approval != nil && slice.Approval.Required && !slice.Approval.Approved {
		return fmt.Errorf("slice %s requires approval", request.SliceID)
	}
	for _, id := range slice.DependsOn {
		dependency := completionSlice(detail, id)
		if dependency == nil || dependency.Status != plan.StatusCompleted {
			return fmt.Errorf("slice %s dependency %s is not completed", request.SliceID, id)
		}
	}
	switch detail.State.Plan.LastRunCommitPolicy {
	case CommitPolicySlice.String(), CommitPolicyNone.String():
	default:
		return fmt.Errorf("verified completion requires a supported slice or none commit policy")
	}
	return nil
}

type verifiedCompletionBoundary struct{ root, branch, head, fingerprint, indexFingerprint, declaration, policy, strategy string }

func (s SliceCompletionService) inspectVerifiedCompletion(ctx context.Context, request SliceCompletionRequest) (verifiedCompletionBoundary, error) {
	var result verifiedCompletionBoundary
	detail := request.Record.Detail()
	slice := completionSlice(detail, request.SliceID)
	root, err := canonicalVerificationDirectory(slice.ExecutionRoot)
	if err != nil {
		return result, err
	}
	if root != slice.ExecutionRoot {
		return result, fmt.Errorf("recorded execution root is not canonical")
	}
	policy, strategy, reason := effectiveInterruptedBoundary(detail, slice)
	if reason != "" {
		return result, fmt.Errorf("slice completion refused: %s", reason)
	}
	if strategy == plan.WorkspaceStrategyWorktree {
		if err := inspectLinkedWorktreeIdentity(ctx, detail, root, s.CommandRunner); err != nil {
			return result, err
		}
	}
	git := gitops.NewClient(root, s.CommandRunner)
	top, err := git.TopLevel(ctx)
	if err != nil {
		return result, err
	}
	canonicalTop, err := canonicalVerificationDirectory(top)
	if err != nil || canonicalTop != root {
		return result, fmt.Errorf("execution root is not the Git top-level")
	}
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return result, err
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return result, err
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return result, err
	}
	active, err := gitops.ActiveOperation(root)
	if err != nil {
		return result, err
	}
	action := (ExecutionBoundaryController{}).Classify(ExecutionBoundaryDurableFacts{Detail: detail, SliceID: request.SliceID}, ExecutionBoundaryLiveFacts{ExecutionRoot: root, WorkspaceStrategy: strategy, CommitPolicy: detail.State.Plan.LastRunCommitPolicy, Branch: branch, Head: head, PorcelainStatus: status, ActiveGitOperation: active})
	if action.EffectiveDisposition != InterruptedSliceResume && action.EffectiveDisposition != InterruptedSliceManualCompletion {
		return result, fmt.Errorf("slice completion boundary refused: %s", action.Diagnostics.Reason)
	}
	if start := slice.ExecutionStart; start != nil && (branch != start.Branch || head != start.Head) {
		return result, fmt.Errorf("slice completion branch or HEAD differs from recorded execution start")
	}
	if policy == CommitPolicySlice.String() && (branch == "" || gitops.ProtectedBranch(branch)) {
		return result, fmt.Errorf("slice completion unsafe branch %q", branch)
	}
	classification := commitcontract.ClassifyStatus(status, nil)
	if err := commitcontract.SafetyError(commitcontract.UniquePaths(classification.CommitCandidates), nil); err != nil && policy == CommitPolicySlice.String() {
		return result, err
	}
	indexFingerprint, err := git.DirtyFingerprint(ctx)
	if err != nil {
		return result, err
	}
	fingerprint, err := verifiedWorktreeFingerprint(ctx, git)
	if err != nil {
		return result, err
	}
	// Include all durable admission/identity fields, not just command text: a gate
	// cannot silently change its policy, dependencies, approval, or prepared root.
	declaration, err := json.Marshal(struct {
		PlanID       string
		Repo         plan.Repo
		Workspace    *plan.Workspace
		Verification plan.Verification
		Start        *plan.SliceExecutionStart
		Dependencies []string
		Approval     *plan.Approval
	}{detail.State.Plan.ID, detail.State.Repo, detail.State.Workspace, slice.Verification, slice.ExecutionStart, slice.DependsOn, slice.Approval})
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(declaration)
	return verifiedCompletionBoundary{root, branch, head, fingerprint, indexFingerprint.Hash, hex.EncodeToString(digest[:]), policy, strategy}, nil
}

func verifiedSliceCompletionHash(notes, message string, snapshot plan.SliceVerificationSnapshot) (string, error) {
	payload, err := json.Marshal(struct {
		Notes        string
		Message      string
		Verification plan.SliceVerificationSnapshot
	}{notes, message, snapshot})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return verifiedCompletionHashPrefix + hex.EncodeToString(digest[:]), nil
}

func formatVerifiedSliceMessage(planID, sliceID string, proposal commitcontract.Proposal, snapshot plan.SliceVerificationSnapshot) (string, error) {
	planTrailer, err := commitcontract.NewTrustedTrailer("Tao-Plan", planID)
	if err != nil {
		return "", err
	}
	sliceTrailer, err := commitcontract.NewTrustedTrailer("Tao-Slice", sliceID)
	if err != nil {
		return "", err
	}
	trailers := []commitcontract.TrustedTrailer{planTrailer, sliceTrailer}
	for i, run := range snapshot.Runs {
		observed, err := commitcontract.VerificationTrailers(i+1, run.Command, run.ExitCode, run.OutputDigest)
		if err != nil {
			return "", err
		}
		trailers = append(trailers, observed...)
	}
	return commitcontract.Format(proposal, trailers...)
}

func (s SliceCompletionService) recoverVerifiedCompletion(ctx context.Context, request SliceCompletionRequest, intent plan.SliceCommitIntent) error {
	snapshot := intent.Verification
	if snapshot == nil {
		return fmt.Errorf("verified intent is missing its frozen snapshot")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	hash, err := verifiedSliceCompletionHash(request.Notes, intent.Message, *snapshot)
	if err != nil {
		return err
	}
	if hash != intent.Hash || intent.StartingBranch != snapshot.StartingBranch || intent.StartingHead != snapshot.StartingHead {
		return fmt.Errorf("slice has a conflicting verified commit intent")
	}
	if intent.Policy != CommitPolicySlice.String() && intent.Policy != CommitPolicyNone.String() {
		return fmt.Errorf("unsupported verified intent policy")
	}
	if request.CommitProposal != nil && intent.Policy == CommitPolicySlice.String() {
		message, err := formatVerifiedSliceMessage(request.Record.Detail().State.Plan.ID, request.SliceID, *request.CommitProposal, *snapshot)
		if err != nil {
			return err
		}
		if message != intent.Message {
			return fmt.Errorf("slice has a conflicting commit proposal")
		}
	}
	request.VerificationResults = snapshot.Runs
	slice := completionSlice(request.Record.Detail(), request.SliceID)
	if slice.Completion != nil {
		completedAt := request.Now
		if slice.Timing.CompletedAt != nil {
			completedAt = *slice.Timing.CompletedAt
		}
		return persistSliceCompletion(request, slice.Completion, completedAt)
	}
	return s.finishSliceCompletion(ctx, gitops.NewClient(snapshot.ExecutionRoot, s.CommandRunner), request, intent, false, ctx.Err)
}

// Deterministic event identity makes settlement of the same snapshot idempotent,
// while a pre-intent retry has a fresh attempt ID and reruns the complete sequence.
func recordVerificationDiagnostics(request SliceCompletionRequest, snapshot plan.SliceVerificationSnapshot) error {
	events := MatchVerificationClaims(snapshot.ExecutionRoot, request.VerificationClaims, snapshot.Runs)
	for i, run := range snapshot.Runs {
		if run.FailureKind != plan.FinalVerificationFailureKindInvalidCommand {
			continue
		}
		event := plan.Event{Type: plan.EventTypeVerificationCommandInvalid, Command: run.Command, Reason: run.Details, Result: run.Result, ExitCode: run.ExitCode, FailureKind: run.FailureKind, Attempts: i + 1, Message: "Verification command invalid"}
		if i+1 < len(snapshot.Runs) && snapshot.Runs[i+1].OriginalCommand == run.Command && snapshot.Runs[i+1].CommandIndex == run.CommandIndex {
			correction := snapshot.Runs[i+1]
			event.CorrectedCommand = correction.Command
			event.Result = correction.Result
		}
		events = append(events, event)
	}
	for _, event := range events {
		event.PlanID = request.Record.Detail().State.Plan.ID
		event.SliceID = request.SliceID
		event.VerificationAttemptID = snapshot.AttemptID
		event.Timestamp = snapshot.RecordedAt
		if slices.ContainsFunc(request.Record.Detail().Events, func(prior plan.Event) bool { return reflect.DeepEqual(prior, event) }) {
			continue
		}
		if err := plan.AppendEvent(request.Record.Dir(), event); err != nil {
			return fmt.Errorf("record verification diagnostic: %w", err)
		}
		request.Record.Detail().Events = append(request.Record.Detail().Events, event)
	}
	return nil
}

// The frozen fingerprint ignores index placement (staging is Tao's own effect),
// but includes exact changed-file bytes and modes, including binary files and
// untracked files. Ignored build outputs and Tao metadata cannot enter a commit.
func verifiedWorktreeFingerprint(ctx context.Context, git gitops.Client) (string, error) {
	status, err := git.StatusPorcelainAllUntracked(ctx)
	if err != nil {
		return "", err
	}
	classification := commitcontract.ClassifyStatus(status, nil)
	facts := interruptedSliceFacts(InterruptedSliceInput{PorcelainStatus: status})
	if len(classification.AmbiguousLines) != 0 || facts.Conflicted {
		return "", fmt.Errorf("cannot fingerprint ambiguous or conflicted worktree")
	}
	paths := commitcontract.UniquePaths(classification.CommitCandidates)
	slices.Sort(paths)
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
			return "", fmt.Errorf("unsafe verification path")
		}
		full := filepath.Join(git.Root(), path)
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			if err := encoder.Encode([]string{path, "deleted"}); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		parent, err := canonicalVerificationDirectory(filepath.Dir(full))
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(git.Root(), parent)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("verification path escapes execution root")
		}
		var mode, content string
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			mode = "120000"
			content, err = os.Readlink(full)
		case info.Mode().IsRegular():
			mode = "100644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "100755"
			}
			content, err = verifiedFileDigest(full)
		default:
			return "", fmt.Errorf("unsupported verification path %q", path)
		}
		if err != nil {
			return "", err
		}
		if err := encoder.Encode([]string{path, mode, content}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifiedFileDigest(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- Git supplied a checked repository-relative worktree path.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func checkFrozenVerificationWorktree(ctx context.Context, git gitops.Client, intent plan.SliceCommitIntent) error {
	if intent.Verification == nil {
		return nil
	}
	active, err := gitops.ActiveOperation(git.Root())
	if err != nil {
		return err
	}
	if active != "" {
		return fmt.Errorf("verified completion refused during Git operation %q", active)
	}
	fingerprint, err := verifiedWorktreeFingerprint(ctx, git)
	if err != nil {
		return err
	}
	if fingerprint != intent.Verification.WorktreeFingerprint {
		return fmt.Errorf("verified completion worktree differs from frozen intent")
	}
	return nil
}
