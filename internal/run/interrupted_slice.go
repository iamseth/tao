package run

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

// InterruptedSliceDisposition describes the only safe next actions at an
// automatic slice boundary. Classification is deliberately independent of
// provider results and event telemetry.
type InterruptedSliceDisposition string

const (
	InterruptedSliceNewStart           InterruptedSliceDisposition = "new_start"
	InterruptedSliceBlockedContinue    InterruptedSliceDisposition = "blocked_continue"
	InterruptedSliceBlockedRestart     InterruptedSliceDisposition = "blocked_restart"
	InterruptedSliceCleanStartRepair   InterruptedSliceDisposition = "clean_torn_start_repair"
	InterruptedSliceResume             InterruptedSliceDisposition = "isolated_pre_intent_resume"
	InterruptedSliceCompletionRecovery InterruptedSliceDisposition = "existing_completion_recovery"
	InterruptedSliceManualCompletion   InterruptedSliceDisposition = "manual_completion"
	InterruptedSliceRefuse             InterruptedSliceDisposition = "refuse"
)

// InterruptedSliceInput combines durable plan data with a snapshot of live Git
// state. Callers gather the snapshot; the classifier itself performs no I/O.
type InterruptedSliceInput struct {
	Detail             *plan.PlanDetail
	SliceID            string
	ExecutionRoot      string
	WorkspaceStrategy  string
	CommitPolicy       string
	Branch             string
	Head               string
	PorcelainStatus    string
	ActiveGitOperation string
	ContinueBlocked    bool
	RestartBlocked     bool
	BaselineBranch     string
	BaselineHead       string
	BoundaryAncestor   bool
	AncestryKnown      bool
}

// InterruptedSliceFacts contains diagnostics safe to show to an operator. It
// intentionally excludes provider errors, events, and the contents of files.
type InterruptedSliceFacts struct {
	SliceID            string
	CurrentSliceID     string
	SliceStatus        string
	Pending            bool
	RecordedRoot       string
	RecordedBranch     string
	RecordedHead       string
	LiveRoot           string
	WorkspaceStrategy  string
	CommitPolicy       string
	Branch             string
	Head               string
	Dirty              bool
	ChangedPaths       []string
	AmbiguousStatus    bool
	Conflicted         bool
	ActiveGitOperation string
	IntentPresent      bool
	CompletionPresent  bool
	BaselineBranch     string
	BaselineHead       string
	BoundaryAncestor   bool
	AncestryKnown      bool
}

type InterruptedSliceResult struct {
	Disposition             InterruptedSliceDisposition
	ContinuationDisposition InterruptedSliceDisposition
	Reason                  string
	Facts                   InterruptedSliceFacts
}

// ClassifyInterruptedSlice decides whether a selected slice is a new start, a
// repairable torn start, resumable agent work, a completion transaction, or
// work that Tao must leave untouched.
func ClassifyInterruptedSlice(input InterruptedSliceInput) InterruptedSliceResult {
	facts := interruptedSliceFacts(input)
	refuse := func(format string, args ...any) InterruptedSliceResult {
		return InterruptedSliceResult{Disposition: InterruptedSliceRefuse, Reason: fmt.Sprintf(format, args...), Facts: facts}
	}
	if input.Detail == nil {
		return refuse("plan detail is missing")
	}
	slice := interruptedSlice(input.Detail, input.SliceID)
	if slice == nil {
		return refuse("selected slice %q is missing", input.SliceID)
	}
	if facts.CurrentSliceID != "" && facts.CurrentSliceID != input.SliceID {
		return refuse("current slice is %q, not selected slice %q", facts.CurrentSliceID, input.SliceID)
	}
	blockedContinuation := isBlockedContinuation(input, slice)
	blockedRestart := isBlockedRestart(input, slice)
	if input.ContinueBlocked && input.RestartBlocked {
		return refuse("blocked continuation and restart are mutually exclusive")
	}
	if (input.Detail.State.Status == plan.StatusBlocked || slice.Status == plan.StatusBlocked) && !blockedContinuation && !blockedRestart {
		return refuse("blocked slice requires explicit --continue or --restart")
	}
	action := func(disposition InterruptedSliceDisposition, reason string) InterruptedSliceResult {
		result := InterruptedSliceResult{Disposition: disposition, Reason: reason, Facts: facts}
		if blockedContinuation {
			result.Disposition = InterruptedSliceBlockedContinue
			result.ContinuationDisposition = disposition
		}
		if blockedRestart {
			result.Disposition = InterruptedSliceBlockedRestart
			result.ContinuationDisposition = disposition
		}
		return result
	}
	freshStart := slice.Status == plan.StatusPending && facts.CurrentSliceID == "" && slice.ExecutionStart == nil && slice.CommitIntent == nil && slice.Completion == nil
	blockedFreshStart := blockedContinuation && slice.ExecutionRoot == "" && slice.ExecutionStart == nil && slice.CommitIntent == nil && slice.Completion == nil
	if blockedRestart {
		return classifyBlockedRestart(input, slice, facts)
	}
	if freshStart || blockedFreshStart {
		if !facts.Pending {
			return refuse("selected pending slice is absent from the pending queue")
		}
		return action(InterruptedSliceNewStart, "slice has no recorded execution boundary")
	}
	stateAdvancedStart := stateAdvancedSliceStart(input.Detail, slice)
	if !stateAdvancedStart && !blockedContinuation && (slice.Status != plan.StatusInProgress || facts.CurrentSliceID != input.SliceID) {
		return refuse("slice is not the current in-progress slice")
	}
	if !facts.Pending {
		return refuse("current slice is absent from the pending queue")
	}

	policy, strategy, reason := effectiveInterruptedBoundary(input.Detail, slice)
	facts.CommitPolicy = policy
	facts.WorkspaceStrategy = strategy
	if reason != "" {
		return InterruptedSliceResult{Disposition: InterruptedSliceRefuse, Reason: reason, Facts: facts}
	}
	if slice.CommitIntent != nil || slice.Completion != nil {
		return action(InterruptedSliceCompletionRecovery, "durable completion metadata must be settled without rerunning the agent")
	}
	recordedRoot := slice.ExecutionRoot
	if stateAdvancedStart {
		recordedRoot = workspace.ResolveRecordedWorktree(input.Detail).Path
		facts.RecordedRoot = recordedRoot
	}
	if strategy == plan.WorkspaceStrategyWorktree {
		if reason := interruptedWorktreeIdentityError(input.Detail, recordedRoot); reason != "" {
			return refuse("%s", reason)
		}
	}
	if recordedRoot == "" || input.ExecutionRoot == "" || filepath.Clean(recordedRoot) != filepath.Clean(input.ExecutionRoot) {
		return refuse("live execution root does not match the recorded root")
	}
	if facts.ActiveGitOperation != "" {
		return refuse("Git operation %q is active", facts.ActiveGitOperation)
	}
	if facts.AmbiguousStatus {
		return refuse("git status contains an ambiguous entry")
	}
	if facts.Conflicted {
		return refuse("worktree contains conflicted entries")
	}
	if input.CommitPolicy != "" && policy != input.CommitPolicy {
		return InterruptedSliceResult{Disposition: InterruptedSliceRefuse, Reason: "effective commit policy differs from the recorded boundary", Facts: facts}
	}
	if input.WorkspaceStrategy != "" && strategy != input.WorkspaceStrategy {
		return InterruptedSliceResult{Disposition: InterruptedSliceRefuse, Reason: "effective workspace strategy differs from the recorded boundary", Facts: facts}
	}
	if policy == CommitPolicyNone.String() {
		return action(InterruptedSliceManualCompletion, "current or manually owned work must not be attributed to an interrupted agent run")
	}
	if slice.ExecutionStart == nil {
		if facts.Dirty {
			return refuse("dirty worktree has no immutable execution boundary")
		}
		if policy != CommitPolicySlice.String() || strategy != plan.WorkspaceStrategyWorktree {
			return refuse("only a clean isolated automatic start can be repaired")
		}
		if !matchesWorkspaceBoundary(input.Detail.State.Workspace, input.Branch, input.Head) {
			return refuse("live branch or HEAD differs from prepared workspace metadata")
		}
		if input.Branch == "" || gitops.ProtectedBranch(input.Branch) {
			return refuse("automatic slice is on an unsafe branch")
		}
		return action(InterruptedSliceCleanStartRepair, "lifecycle start exists but its clean execution boundary is missing")
	}
	boundary := slice.ExecutionStart
	if input.Branch != boundary.Branch {
		return refuse("live branch differs from the original execution branch")
	}
	if input.Branch == "" || gitops.ProtectedBranch(input.Branch) {
		return refuse("automatic slice is on an unsafe branch")
	}
	if input.Head != boundary.Head {
		return refuse("live HEAD advanced beyond the original execution boundary")
	}
	if strategy == plan.WorkspaceStrategyCurrent {
		return action(InterruptedSliceManualCompletion, "current or manually owned work must not be attributed to an interrupted agent run")
	}
	if policy != CommitPolicySlice.String() || strategy != plan.WorkspaceStrategyWorktree {
		return refuse("boundary is not an isolated automatic slice")
	}
	return action(InterruptedSliceResume, "isolated pre-intent work remains inside its immutable boundary")
}

func classifyBlockedRestart(input InterruptedSliceInput, slice *plan.Slice, facts InterruptedSliceFacts) InterruptedSliceResult {
	refuse := func(reason string) InterruptedSliceResult {
		return InterruptedSliceResult{Disposition: InterruptedSliceRefuse, Reason: reason, Facts: facts}
	}
	if slice.CommitIntent != nil || slice.Completion != nil {
		return refuse("post-intent or completion evidence cannot be restarted")
	}
	policy, strategy, reason := effectiveInterruptedBoundary(input.Detail, slice)
	facts.CommitPolicy, facts.WorkspaceStrategy = policy, strategy
	if reason != "" {
		return refuse(reason)
	}
	if policy != CommitPolicySlice.String() || strategy != plan.WorkspaceStrategyWorktree {
		return refuse("only an isolated automatic slice can be restarted")
	}
	if input.CommitPolicy != "" && input.CommitPolicy != policy {
		return refuse("requested commit policy differs from the prior automatic boundary")
	}
	if input.WorkspaceStrategy != "" && input.WorkspaceStrategy != strategy {
		return refuse("requested execution mode differs from the prior isolated boundary")
	}
	if slice.ExecutionStart == nil || strings.TrimSpace(slice.ExecutionRoot) == "" {
		return refuse("restart requires an immutable execution boundary")
	}
	if input.ExecutionRoot == "" || filepath.Clean(input.ExecutionRoot) != filepath.Clean(slice.ExecutionRoot) {
		return refuse("live execution root does not match the recorded root")
	}
	if input.ActiveGitOperation != "" {
		return refuse(fmt.Sprintf("Git operation %q is active", input.ActiveGitOperation))
	}
	if facts.Dirty || facts.AmbiguousStatus || facts.Conflicted {
		return refuse("restart requires a clean, unconflicted worktree with unambiguous status")
	}
	if input.Branch != slice.ExecutionStart.Branch || input.Head != slice.ExecutionStart.Head {
		return refuse("live branch and HEAD must match the prior immutable boundary")
	}
	if input.Branch == "" || gitops.ProtectedBranch(input.Branch) {
		return refuse("automatic slice is on an unsafe branch")
	}
	if input.BaselineBranch == "" || input.BaselineHead == "" {
		return refuse("fresh baseline is unavailable or ambiguous")
	}
	if !input.AncestryKnown || !input.BoundaryAncestor {
		return refuse("prior immutable boundary is not an ancestor of the fresh baseline")
	}
	if input.BaselineHead == slice.ExecutionStart.Head {
		return refuse("fresh baseline has not advanced beyond the prior immutable boundary")
	}
	return InterruptedSliceResult{Disposition: InterruptedSliceBlockedRestart, ContinuationDisposition: InterruptedSliceNewStart, Reason: "clean blocked automatic slice may start a fresh attempt on the advanced baseline", Facts: facts}
}

func isBlockedRestart(input InterruptedSliceInput, slice *plan.Slice) bool {
	if !input.RestartBlocked || input.Detail == nil || slice == nil {
		return false
	}
	lifecycle := plan.AnalyzeLifecycle(input.Detail)
	return lifecycle.Continuable && blockedSelectedSliceID(input.Detail) == slice.ID && (input.Detail.State.Status == plan.StatusBlocked || slice.Status == plan.StatusBlocked)
}

func isBlockedContinuation(input InterruptedSliceInput, slice *plan.Slice) bool {
	if !input.ContinueBlocked || input.Detail == nil || slice == nil {
		return false
	}
	lifecycle := plan.AnalyzeLifecycle(input.Detail)
	if !lifecycle.Continuable {
		return false
	}
	selectedID := blockedSelectedSliceID(input.Detail)
	return selectedID == slice.ID && (input.Detail.State.Status == plan.StatusBlocked || slice.Status == plan.StatusBlocked)
}

func blockedSelectedSliceID(detail *plan.PlanDetail) string {
	if detail.State.Plan.CurrentSlice != nil {
		return *detail.State.Plan.CurrentSlice
	}
	if len(detail.State.Plan.PendingSlices) > 0 {
		return detail.State.Plan.PendingSlices[0]
	}
	return ""
}

// EffectiveDisposition unwraps the validated action represented by an
// explicit blocked continuation.
func (result InterruptedSliceResult) EffectiveDisposition() InterruptedSliceDisposition {
	if result.Disposition == InterruptedSliceBlockedContinue || result.Disposition == InterruptedSliceBlockedRestart {
		return result.ContinuationDisposition
	}
	return result.Disposition
}

func interruptedSliceFacts(input InterruptedSliceInput) InterruptedSliceFacts {
	facts := InterruptedSliceFacts{
		SliceID: input.SliceID, LiveRoot: input.ExecutionRoot, WorkspaceStrategy: input.WorkspaceStrategy, CommitPolicy: input.CommitPolicy,
		Branch: input.Branch, Head: input.Head, ActiveGitOperation: strings.TrimSpace(input.ActiveGitOperation),
		BaselineBranch: input.BaselineBranch, BaselineHead: input.BaselineHead, BoundaryAncestor: input.BoundaryAncestor, AncestryKnown: input.AncestryKnown,
	}
	if input.Detail != nil {
		if input.Detail.State.Plan.CurrentSlice != nil {
			facts.CurrentSliceID = *input.Detail.State.Plan.CurrentSlice
		}
		facts.Pending = slices.Contains(input.Detail.State.Plan.PendingSlices, input.SliceID)
		if slice := interruptedSlice(input.Detail, input.SliceID); slice != nil {
			facts.SliceStatus = slice.Status
			facts.RecordedRoot = slice.ExecutionRoot
			if slice.ExecutionStart != nil {
				facts.RecordedBranch = slice.ExecutionStart.Branch
				facts.RecordedHead = slice.ExecutionStart.Head
			}
			facts.IntentPresent = slice.CommitIntent != nil
			facts.CompletionPresent = slice.Completion != nil
		}
	}
	paths, ambiguous := gitops.PorcelainPaths(input.PorcelainStatus)
	facts.ChangedPaths = paths
	facts.Dirty = strings.TrimSpace(input.PorcelainStatus) != ""
	facts.AmbiguousStatus = len(ambiguous) > 0
	for line := range strings.SplitSeq(input.PorcelainStatus, "\n") {
		if porcelainConflict(line) {
			facts.Conflicted = true
			break
		}
	}
	return facts
}

func effectiveInterruptedBoundary(detail *plan.PlanDetail, slice *plan.Slice) (policy string, strategy string, reason string) {
	if slice.ExecutionStart != nil {
		policy = strings.TrimSpace(slice.ExecutionStart.CommitPolicy)
		strategy = strings.TrimSpace(slice.ExecutionStart.WorkspaceStrategy)
	}
	// Older execution_start records omitted these fields. Infer only from the
	// durable run/workspace records; never from invocation defaults.
	if policy == "" {
		policy = strings.TrimSpace(detail.State.Plan.LastRunCommitPolicy)
		if policy == "" && slice.ExecutionStart != nil {
			// Legacy execution_start was written only for the automatic slice
			// policy, so the boundary itself is unambiguous durable evidence.
			policy = CommitPolicySlice.String()
		}
	}
	if strategy == "" && detail.State.Workspace != nil {
		strategy = strings.TrimSpace(detail.State.Workspace.Strategy)
	}
	if strategy == "" && policy == CommitPolicyNone.String() {
		executionRoot := strings.TrimSpace(slice.ExecutionRoot)
		repoRoot := strings.TrimSpace(detail.State.Repo.Root)
		if executionRoot != "" && repoRoot != "" && filepath.Clean(executionRoot) == filepath.Clean(repoRoot) {
			strategy = plan.WorkspaceStrategyCurrent
		}
	}
	if policy != CommitPolicySlice.String() && policy != CommitPolicyNone.String() {
		return policy, strategy, "recorded commit policy is missing or unsupported"
	}
	if strategy != plan.WorkspaceStrategyWorktree && strategy != plan.WorkspaceStrategyCurrent {
		return policy, strategy, "recorded workspace strategy is missing or unsupported"
	}
	return policy, strategy, ""
}

func interruptedWorktreeIdentityError(detail *plan.PlanDetail, sliceRoot string) string {
	if detail == nil || detail.State.Workspace == nil {
		return "interrupted automatic slice has no durable workspace metadata"
	}
	if detail.State.Workspace.LifecycleStatus != plan.WorkspaceStatusReady {
		return "interrupted automatic slice workspace is not ready"
	}
	return recordedWorktreeIdentityError(detail, sliceRoot)
}

func recordedWorktreeIdentityError(detail *plan.PlanDetail, sliceRoot string) string {
	if detail == nil || detail.State.Workspace == nil {
		return "interrupted automatic slice has no durable workspace metadata"
	}
	workspaceState := detail.State.Workspace
	if !recordedAutomaticWorktree(workspaceState) {
		return "interrupted automatic slice does not record a worktree workspace"
	}
	switch workspaceState.CleanupStatus {
	case "", plan.WorkspaceCleanupStatusPending, plan.WorkspaceCleanupStatusHeld:
	default:
		return "interrupted automatic slice workspace cleanup is not compatible with resume"
	}
	workspacePath := workspace.ResolveRecordedWorktree(detail).Path
	if workspacePath == "" || !filepath.IsAbs(workspacePath) {
		return "interrupted automatic slice does not record an absolute worktree path"
	}
	recordedRoot := strings.TrimSpace(sliceRoot)
	if recordedRoot == "" || !filepath.IsAbs(recordedRoot) || filepath.Clean(recordedRoot) != workspacePath {
		return "slice execution root does not match the recorded worktree path"
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	if repoRoot == "" || !filepath.IsAbs(repoRoot) {
		return "interrupted automatic slice does not record an absolute repository root"
	}
	if filepath.Clean(repoRoot) == workspacePath {
		return "interrupted automatic slice worktree is not separate from the repository checkout"
	}
	return ""
}

func matchesWorkspaceBoundary(workspace *plan.Workspace, branch string, head string) bool {
	if workspace == nil {
		return false
	}
	return workspace.Branch != "" && workspace.HeadSHA != "" && workspace.Branch == branch && workspace.HeadSHA == head
}

func stateAdvancedSliceStart(detail *plan.PlanDetail, slice *plan.Slice) bool {
	if detail == nil || slice == nil || detail.State.Plan.CurrentSlice == nil {
		return false
	}
	return *detail.State.Plan.CurrentSlice == slice.ID &&
		slice.Status == plan.StatusPending &&
		slice.ExecutionRoot == "" &&
		slice.ExecutionStart == nil &&
		slice.CommitIntent == nil &&
		slice.Completion == nil
}

func interruptedSlice(detail *plan.PlanDetail, sliceID string) *plan.Slice {
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == sliceID {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}

func porcelainConflict(line string) bool {
	if len(line) < 2 {
		return false
	}
	status := line[:2]
	return strings.Contains(status, "U") || status == "AA" || status == "DD"
}
