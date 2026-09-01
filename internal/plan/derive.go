package plan

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DerivedPlan collects read-only state computed from durable plan artifacts.
type DerivedPlan struct {
	Lifecycle
	Capabilities           RunCapabilities
	CompletedCount         int
	PendingCount           int
	TotalCount             int
	OriginalCompletedCount int
	OriginalTotalCount     int
	ReworkCompletedCount   int
	ReworkTotalCount       int
	CompletedAt            *time.Time
	Elapsed                time.Duration
	SliceCompletionPending bool
	UnresolvedReworkStop   bool
	FinalizationRecovery   *FinalizationRecovery
	NextAction             PlanNextAction
}

type Lifecycle struct {
	CurrentSliceID string
	CurrentSlice   *Slice
	NextSliceID    string
	NextSlice      *Slice
	Complete       bool
	Active         bool
	Runnable       bool
	RunnableError  error `json:"-"`
	Continuable    bool
	ContinueError  error `json:"-"`
}

type ProgressSnapshot struct {
	Status         string
	NextSliceID    string
	CompletedCount int
	PendingCount   int
}

type PlanningSessionMetricSummary struct {
	Present           bool
	Valid             bool
	UnavailableReason string
	Duration          time.Duration
	TotalTokens       int64
	TotalMessages     int64
}

// Derive is the read-side entry point for lifecycle, capability, count, and elapsed-time views.
func Derive(detail *PlanDetail, now time.Time) DerivedPlan {
	index := newDetailIndex(detail)
	lifecycle := lifecycleState(detail, index)
	completedAt := completedAt(detail, index)
	derived := DerivedPlan{
		Lifecycle:              lifecycle,
		Capabilities:           runCapabilitiesForDetail(detail, lifecycle),
		CompletedCount:         index.completedCount,
		PendingCount:           index.pendingCount,
		TotalCount:             len(detail.Slices.Slices),
		OriginalCompletedCount: index.originalCompletedCount,
		OriginalTotalCount:     index.originalTotalCount,
		ReworkCompletedCount:   index.reworkCompletedCount,
		ReworkTotalCount:       index.reworkTotalCount,
		CompletedAt:            completedAt,
		SliceCompletionPending: SliceCompletionPending(detail),
		UnresolvedReworkStop:   HasUnresolvedReworkStop(detail.Events),
		FinalizationRecovery:   CurrentFinalizationRecovery(detail),
	}
	derived.NextAction = deriveNextAction(detail, derived)
	if !now.IsZero() {
		derived.Elapsed = elapsed(detail, completedAt, now)
	}
	return derived
}

func AnalyzeLifecycle(detail *PlanDetail) Lifecycle {
	return lifecycleState(detail, newDetailIndex(detail))
}

// DeriveNextAction projects one safest next action from durable lifecycle
// evidence. It is advisory only: every returned command must still pass its
// authoritative command-side gates when invoked.
func DeriveNextAction(detail *PlanDetail) PlanNextAction {
	derived := Derive(detail, time.Time{})
	return derived.NextAction
}

func deriveNextAction(detail *PlanDetail, derived DerivedPlan) PlanNextAction {
	id := strings.TrimSpace(detail.State.Plan.ID)
	if id == "" {
		id = "<plan>"
	}
	command := func(name string) string { return name + " " + id }
	primary := func(kind PlanActionKind, class PlanActionClass, cmd, reason string, alternatives ...PlanAction) PlanNextAction {
		if alternatives == nil {
			alternatives = []PlanAction{}
		}
		return PlanNextAction{Primary: PlanAction{Kind: kind, Class: class, Command: cmd, Reason: reason}, Alternatives: alternatives}
	}
	instruction := func(text, reason string) PlanNextAction {
		return PlanNextAction{Primary: PlanAction{Kind: PlanActionRecoverPullRequest, Class: PlanActionClassRecovery, Instruction: text, Reason: reason}, Alternatives: []PlanAction{}}
	}
	administrativeMerge := PlanAction{
		Kind: PlanActionMerge, Class: PlanActionClassAdministrative,
		Command: command("tao merge --force"), Reason: "administrative exception that bypasses review and merge safeguards",
	}
	currentReviewAction := func(reason string) PlanNextAction {
		review := CurrentReview(detail)
		if review != nil && review.Status == ReviewStatusCompleted && review.Verdict == ReviewVerdictChangesRequested {
			if derived.UnresolvedReworkStop {
				return primary(PlanActionRestartRework, PlanActionClassRecovery, command("tao run --rework-restart"), reason+"; the actionable review is behind a stopped automatic rework cycle")
			}
			return primary(PlanActionRework, PlanActionClassRecovery, command("tao rework"), reason+"; the current review has actionable changes")
		}
		return primary(PlanActionReview, PlanActionClassRecovery, command("tao review --run"), reason+"; record a fresh current review before any pull-request mutation")
	}

	// Durable, unsettled transactions take priority over gates and ordinary
	// progression. Starting another operation could destroy the exact recovery
	// boundary that Tao needs to settle first.
	if derived.SliceCompletionPending {
		return PlanNextAction{
			Primary: PlanAction{
				Kind:        PlanActionRecoverSliceCompletion,
				Class:       PlanActionClassRecovery,
				Instruction: "Rerun the original complete tao slice-complete invocation with all previously supplied file arguments",
				Reason:      "an automatic slice commit intent is not settled",
			},
			Alternatives: []PlanAction{},
		}
	}
	if detail.State.Workspace != nil && detail.State.Workspace.RebaseIntent != nil {
		return primary(PlanActionRecoverRebase, PlanActionClassRecovery, command("tao run"), "an interrupted workspace rebase must be settled before other work")
	}
	if detail.State.Plan.MergeCommitIntent != nil {
		return primary(PlanActionRecoverMerge, PlanActionClassRecovery, command("tao merge"), "an interrupted merge transaction must be settled before other work")
	}
	if recovery := derived.FinalizationRecovery; recovery != nil {
		category := strings.ReplaceAll(recovery.Category, "_", " ")
		switch recovery.Phase {
		case FinalizationFailurePhaseProposalRepair:
			switch recovery.RecoveryAction {
			case FinalizationRecoveryRestoreBoundary:
				if recovery.Category == "workspace_mismatch" {
					return instruction("Repair or restore the plan's recorded linked worktree at its recorded path, branch, and HEAD before recording a fresh review", "review proposal repair failed ("+category+"); a fresh review cannot restore the durable linked-worktree boundary")
				}
				if recovery.Category == "workspace_dirty" {
					return instruction("Restore a clean plan worktree at its recorded branch and HEAD before recording a fresh review", "review proposal repair failed ("+category+"); a fresh review cannot repair a dirty worktree")
				}
				if recovery.Category == "workspace_preflight_failed" {
					return instruction("Restore a clean plan worktree at its recorded branch and HEAD before recording a fresh review", "review proposal repair failed ("+category+"); a fresh review cannot repair an uninspectable worktree")
				}
				return instruction("Restore the plan worktree to its recorded branch and HEAD before recording a fresh review", "review proposal repair failed ("+category+"); a fresh review cannot restore the durable workspace boundary")
			case FinalizationRecoveryRepairIntent:
				return instruction("Repair the conflicting durable pull-request intent so it matches the recorded workspace boundary before recording a fresh review", "review proposal repair failed ("+category+"); a fresh review cannot repair conflicting intent evidence")
			default:
				return currentReviewAction("review proposal repair failed (" + category + ")")
			}
		case FinalizationFailurePhasePullRequest:
			switch recovery.RecoveryAction {
			case FinalizationRecoveryRestoreBoundary:
				if recovery.Category == "workspace_mismatch" {
					return instruction("Repair or restore the plan's recorded linked worktree at its recorded path, branch, and HEAD before retrying pull-request finalization", "pull-request finalization failed ("+category+"); the recorded linked worktree is missing or has incompatible identity or lifecycle state")
				}
				if recovery.Category == "workspace_dirty" {
					return instruction("Restore a clean plan worktree at its recorded branch and HEAD before retrying pull-request finalization", "pull-request finalization failed ("+category+"); retry cannot safely proceed with uncommitted worktree changes")
				}
				return instruction("Restore the plan worktree to its recorded branch and HEAD before retrying pull-request finalization", "pull-request finalization failed ("+category+"); the live worktree is outside the durable recovery boundary")
			case FinalizationRecoveryRerunReview:
				return currentReviewAction("pull-request finalization failed (" + category + ")")
			case FinalizationRecoveryRepairIntent:
				return instruction("Repair the conflicting durable pull-request intent so it matches the recorded workspace boundary before retrying finalization", "pull-request finalization failed ("+category+"); retry cannot resolve conflicting intent evidence")
			case FinalizationRecoveryRepairIdentity:
				return instruction("Repair the stale recorded pull-request number and URL only from existing Tao-owned identity evidence, or clear both fields while retaining the recorded branch and HEAD; do not adopt a remotely discovered identity or authorize metadata mutation", "pull-request finalization failed ("+category+"); retry cannot reconcile stale recorded identity without changing its ownership meaning")
			default:
				return primary(PlanActionRecoverPullRequest, PlanActionClassRecovery, command("tao run --pull-request"), "pull-request finalization failed ("+category+"); retry the exact-head handoff")
			}
		}
	}
	// A current merge is terminal even when RecordMerged follows an interrupted
	// pull-request handoff and leaves its exact identity intent intact.
	if PlanIsMerged(detail.Events) {
		return primary(PlanActionNone, PlanActionClassTerminal, "", "recorded merge evidence proves the plan is integrated")
	}
	if detail.State.Plan.PullRequestIntent != nil && derived.Complete {
		if review := CurrentReview(detail); review == nil || !review.IsApproved() {
			return currentReviewAction("an interrupted pull-request handoff no longer has a current approval")
		}
		return primary(PlanActionRecoverPullRequest, PlanActionClassRecovery, command("tao run --pull-request"), "an interrupted pull-request handoff has a current approval and must be settled before other work")
	}
	if derived.UnresolvedReworkStop {
		return primary(PlanActionRestartRework, PlanActionClassRecovery, command("tao run --rework-restart"), "automatic rework stopped and requires an explicit bounded restart")
	}
	if derived.Capabilities.NeedsApproval {
		sliceID := derived.Capabilities.ApprovalSliceID
		cmd := "tao approve"
		if sliceID != "" {
			cmd += " --slice " + sliceID
		}
		cmd += " " + id
		reason := "the next slice requires approval before execution"
		if derived.Capabilities.ApprovalReason != "" {
			reason += ": " + derived.Capabilities.ApprovalReason
		}
		return primary(PlanActionApprove, PlanActionClassProgress, cmd, reason)
	}
	if derived.Capabilities.CanContinue && !derived.Capabilities.CanRun {
		alternatives := []PlanAction{}
		if slice := derived.CurrentSlice; slice != nil && slice.ExecutionStart != nil && slice.CommitIntent == nil && slice.Completion == nil {
			alternatives = append(alternatives, PlanAction{
				Kind: PlanActionRestartBlocked, Class: PlanActionClassRecovery, Command: command("tao run --restart"),
				Reason: "a clean isolated pre-intent boundary may be restarted only after its baseline advances",
			})
		}
		return primary(PlanActionContinue, PlanActionClassRecovery, command("tao run --continue"), "continue at the preserved boundary after resolving its blocker; use restart only for an eligible newer baseline", alternatives...)
	}
	if derived.Active {
		return primary(PlanActionRun, PlanActionClassRecovery, command("tao run"), "the active slice was interrupted before a durable commit intent")
	}
	if derived.Capabilities.CanRun {
		return primary(PlanActionRun, PlanActionClassProgress, command("tao run"), "the next pending slice is runnable")
	}
	if failure := CurrentFailedFinalVerification(detail); failure != nil {
		return primary(PlanActionRepairVerification, PlanActionClassRecovery, command("tao run --repair-verification"), "current final repository verification failed on the completed branch")
	}

	if PlanIsPullRequestComplete(detail) {
		return primary(PlanActionNone, PlanActionClassTerminal, "", "the approved pull-request handoff is complete; remote integration is not asserted")
	}
	if detail.State.Status == StatusCompleted && !anyPlanMergedEvent(detail.Events) {
		return primary(PlanActionNone, PlanActionClassTerminal, "", "legacy completed state is preserved without asserting merge evidence")
	}

	review := CurrentReview(detail)
	status := PlanLifecycleStatus(detail)
	switch {
	case status == StatusChangesRequested || (review != nil && review.Status == ReviewStatusCompleted && review.Verdict == ReviewVerdictChangesRequested):
		return primary(PlanActionRework, PlanActionClassProgress, command("tao rework"), "the current review has actionable changes", administrativeMerge)
	case review != nil && review.IsApproved():
		return primary(PlanActionMerge, PlanActionClassProgress, command("tao merge"), "the current review approves the completed plan", administrativeMerge)
	case derived.Complete || status == StatusInReview || status == StatusReviewed:
		return primary(PlanActionReview, PlanActionClassProgress, command("tao review --run"), "completed slice work needs a current approved review", administrativeMerge)
	default:
		return primary(PlanActionNone, PlanActionClassTerminal, "", "no safe action can be derived from the current lifecycle evidence")
	}
}

func AnalyzeRunCapabilities(detail *PlanDetail) RunCapabilities {
	return runCapabilitiesForDetail(detail, AnalyzeLifecycle(detail))
}

func runCapabilitiesForDetail(detail *PlanDetail, lifecycle Lifecycle) RunCapabilities {
	capabilities := RunCapabilitiesFromLifecycle(lifecycle)
	capabilities.Reviewed = CurrentReview(detail) != nil
	return capabilities
}

func RunCapabilitiesFromLifecycle(lifecycle Lifecycle) RunCapabilities {
	capabilities := RunCapabilities{
		CanRun:   lifecycle.Runnable,
		Complete: lifecycle.Complete,
		Active:   lifecycle.Active,
	}
	if lifecycle.RunnableError != nil {
		capabilities.DisabledReason = lifecycle.RunnableError.Error()
		if approvalErr, ok := errors.AsType[*ApprovalRequiredError](lifecycle.RunnableError); ok {
			capabilities.NeedsApproval = true
			capabilities.ApprovalSliceID = approvalErr.SliceID
			capabilities.ApprovalReason = approvalErr.Reason
		}
	}
	capabilities.CanContinue = lifecycle.Continuable
	if lifecycle.ContinueError != nil {
		capabilities.ContinueDisabledReason = lifecycle.ContinueError.Error()
		if !capabilities.NeedsApproval {
			if approvalErr, ok := errors.AsType[*ApprovalRequiredError](lifecycle.ContinueError); ok {
				capabilities.NeedsApproval = true
				capabilities.ApprovalSliceID = approvalErr.SliceID
				capabilities.ApprovalReason = approvalErr.Reason
			}
		}
	}
	return capabilities
}

func SnapshotProgress(detail *PlanDetail) ProgressSnapshot {
	derived := Derive(detail, time.Time{})
	return ProgressSnapshot{Status: detail.State.Status, NextSliceID: derived.NextSliceID, CompletedCount: derived.CompletedCount, PendingCount: derived.PendingCount}
}

func ProgressedSince(detail *PlanDetail, before ProgressSnapshot) bool {
	return SnapshotProgress(detail) != before
}

func SliceCompleted(detail *PlanDetail, sliceID string) bool {
	if slices.Contains(detail.State.Plan.CompletedSlices, sliceID) {
		return true
	}
	for _, slice := range detail.Slices.Slices {
		if slice.ID == sliceID {
			return slice.Status == StatusCompleted
		}
	}
	return false
}

// SliceCompletionPending reports whether an automatic slice commit intent has
// not yet been settled by a valid completion outcome.
func SliceCompletionPending(detail *PlanDetail) bool {
	return automaticSliceCompletionError(detail) != nil
}

// CurrentFinalizationRecovery returns only valid current bounded evidence.
// Failure evidence is descriptive and never grants command-side authority.
func CurrentFinalizationRecovery(detail *PlanDetail) *FinalizationRecovery {
	if detail == nil || PlanIsMerged(detail.Events) || PlanIsPullRequestComplete(detail) {
		return nil
	}
	failure := detail.State.Plan.FinalizationFailure
	recovery := FinalizationRecoveryFromFailure(failure)
	if recovery == nil {
		return nil
	}
	switch failure.Phase {
	case FinalizationFailurePhaseProposalRepair:
		expectedAction := ProposalRepairRecoveryAction(failure.Category)
		// Older proposal-repair failures used generic resume or fresh-review
		// guidance. Keep them readable, but project structural failures to the
		// prerequisite workspace or intent repair they actually require.
		if failure.RecoveryAction != expectedAction && failure.RecoveryAction != FinalizationRecoveryResumePullRequest && failure.RecoveryAction != FinalizationRecoveryRerunReview {
			return nil
		}
		review := CurrentReview(detail)
		if review == nil {
			return nil
		}
		reviewBase, reviewHead := finalizationReviewRange(detail, review)
		if reviewBase != failure.ReviewBase || reviewHead != failure.ReviewHead {
			return nil
		}
		recovery.RecoveryAction = expectedAction
	case FinalizationFailurePhasePullRequest:
		expectedAction := PullRequestFinalizationRecoveryAction(failure.Category)
		// Historical mismatch failures were recorded with the generic resume
		// action. Keep them readable while projecting the now-classified safe
		// action; failure metadata remains descriptive and grants no authority.
		legacyIdentityAction := failure.Category == "identity_mismatch" && failure.RecoveryAction == FinalizationRecoveryRepairIntent
		if failure.RecoveryAction != expectedAction && failure.RecoveryAction != FinalizationRecoveryResumePullRequest && !legacyIdentityAction {
			return nil
		}
		workspace := detail.State.Workspace
		if workspace == nil || workspace.Branch != failure.Branch || workspace.HeadSHA != failure.HeadSHA {
			return nil
		}
		recovery.RecoveryAction = expectedAction
	}
	return recovery
}

// finalizationReviewRange applies the compatibility boundary used when
// finalizing historical approvals that predate persisted review bases.
func finalizationReviewRange(detail *PlanDetail, review *PlanReview) (string, string) {
	base := strings.TrimSpace(review.Base)
	if base == "" && detail.State.Workspace != nil {
		base = strings.TrimSpace(detail.State.Workspace.BaseSHA)
	}
	if base == "" {
		base = strings.TrimSpace(detail.State.Repo.BaseCommit)
	}
	return base, strings.TrimSpace(review.Head)
}

// FinalizationRecoveryFromFailure creates the safe shared read projection used
// by status, event, and report views.
func FinalizationRecoveryFromFailure(failure *FinalizationFailure) *FinalizationRecovery {
	if failure == nil || failure.Validate() != nil {
		return nil
	}
	return &FinalizationRecovery{
		Phase: failure.Phase, Category: failure.Category, FailedAt: failure.FailedAt,
		RecoveryAction: failure.RecoveryAction,
	}
}

// HasUnresolvedReworkStop reports whether the latest automatic-rework signal
// is a stop. A later reopen or rework-round event starts a new attempt window.
func HasUnresolvedReworkStop(events []Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case EventTypeReworkStopped:
			return true
		case EventTypePlanReopened, EventTypeReworkRound:
			return false
		}
	}
	return false
}

// ReworkRoundFromSliceID returns the positive round encoded before the final
// two index characters in a persisted r<round><NN>- rework slice ID. It keeps
// the historical classifier used by generated rework slices.
func ReworkRoundFromSliceID(id string) int {
	id = strings.TrimSpace(id)
	dash := strings.IndexByte(id, '-')
	if !strings.HasPrefix(id, "r") || dash < 0 {
		return 0
	}
	digits := id[1:dash]
	if len(digits) <= 2 {
		return 0
	}
	round, err := strconv.Atoi(digits[:len(digits)-2])
	if err != nil || round <= 0 {
		return 0
	}
	return round
}

// IsReworkSliceID reports whether id uses the historical generated rework ID
// form understood by ReworkRoundFromSliceID.
func IsReworkSliceID(id string) bool {
	return ReworkRoundFromSliceID(id) > 0
}

type detailIndex struct {
	slicesByID             map[string]*Slice
	stateCompleted         map[string]bool
	inProgressCount        int
	inProgressSlice        *Slice
	completedCount         int
	pendingCount           int
	originalCompletedCount int
	originalTotalCount     int
	reworkCompletedCount   int
	reworkTotalCount       int
	completedAt            *time.Time
}

// detailIndex centralizes slice lookup and counts so derive, lifecycle, and validation
// use the same interpretation of the loaded artifacts.
func newDetailIndex(detail *PlanDetail) detailIndex {
	index := detailIndex{
		slicesByID:     make(map[string]*Slice, len(detail.Slices.Slices)),
		stateCompleted: make(map[string]bool, len(detail.State.Plan.CompletedSlices)),
	}
	for _, id := range detail.State.Plan.CompletedSlices {
		index.stateCompleted[id] = true
	}
	for i := range detail.Slices.Slices {
		slice := &detail.Slices.Slices[i]
		index.slicesByID[slice.ID] = slice
		if IsReworkSliceID(slice.ID) {
			index.reworkTotalCount++
			if slice.Status == StatusCompleted {
				index.reworkCompletedCount++
			}
		} else {
			index.originalTotalCount++
			if slice.Status == StatusCompleted {
				index.originalCompletedCount++
			}
		}
		if slice.Status == StatusInProgress {
			index.inProgressCount++
			if index.inProgressSlice == nil {
				index.inProgressSlice = slice
			}
		}
		switch slice.Status {
		case StatusCompleted:
			index.completedCount++
			if slice.Timing.CompletedAt != nil && (index.completedAt == nil || slice.Timing.CompletedAt.After(*index.completedAt)) {
				value := *slice.Timing.CompletedAt
				index.completedAt = &value
			}
		default:
			index.pendingCount++
		}
	}
	return index
}

func (i detailIndex) slice(id string) *Slice {
	if id == "" {
		return nil
	}
	return i.slicesByID[id]
}

func currentSliceID(detail *PlanDetail, index detailIndex) string {
	if detail.State.Plan.CurrentSlice != nil {
		currentID := *detail.State.Plan.CurrentSlice
		if current := index.slice(currentID); current != nil && current.Status == StatusCompleted && len(detail.State.Plan.PendingSlices) > 0 {
			return ""
		}
		return currentID
	}
	if index.inProgressSlice != nil {
		return index.inProgressSlice.ID
	}
	return ""
}

// Summarize is the repository-facing projection used for lists and invalid-plan fallbacks.
func Summarize(detail *PlanDetail, now time.Time) PlanSummary {
	state := detail.State
	derived := Derive(detail, now)
	planningSummary := SummarizePlanningSessionMetrics(detail.PlanningSession.Stats, state.CreatedAt)
	summary := PlanSummary{
		ID:                               state.Plan.ID,
		Title:                            state.Plan.Title,
		ChangeType:                       state.Plan.ChangeType,
		Overview:                         ProjectDecisionOverview(detail),
		Status:                           PlanLifecycleStatus(detail),
		Dir:                              detail.Dir,
		CompletedCount:                   derived.CompletedCount,
		PendingCount:                     derived.PendingCount,
		TotalCount:                       derived.TotalCount,
		OriginalCompletedCount:           derived.OriginalCompletedCount,
		OriginalTotalCount:               derived.OriginalTotalCount,
		ReworkCompletedCount:             derived.ReworkCompletedCount,
		ReworkTotalCount:                 derived.ReworkTotalCount,
		StartedAt:                        state.Plan.Timing.StartedAt,
		CompletedAt:                      derived.CompletedAt,
		LastActivityAt:                   state.Plan.Timing.LastActivityAt,
		Elapsed:                          derived.Elapsed,
		Complete:                         derived.Complete,
		Reviewed:                         derived.Capabilities.Reviewed,
		ReviewVerdict:                    reviewVerdict(CurrentReview(detail)),
		IsActive:                         derived.Active,
		Capabilities:                     derived.Capabilities,
		SliceCompletionPending:           derived.SliceCompletionPending,
		UnresolvedReworkStop:             derived.UnresolvedReworkStop,
		NextAction:                       derived.NextAction,
		FinalizationRecovery:             derived.FinalizationRecovery,
		PlanningSessionPresent:           planningSummary.Present,
		PlanningSessionValid:             planningSummary.Valid,
		PlanningSessionUnavailableReason: planningSummary.UnavailableReason,
		PlanningSessionDuration:          planningSummary.Duration,
		PlanningSessionTotalTokens:       planningSummary.TotalTokens,
		PlanningSessionTotalMessages:     planningSummary.TotalMessages,
		Metrics:                          SummarizeAgentTelemetry(detail).Totals,
		PullRequest:                      clonePullRequest(state.Plan.PullRequest),
		FinalizationFailure:              cloneFinalizationFailure(state.Plan.FinalizationFailure),
		Workspace:                        cloneWorkspace(state.Workspace),
		Warnings:                         detail.Warnings,
	}

	if currentID := derived.CurrentSliceID; summary.Status != StatusCompleted && currentID != "" {
		summary.CurrentSliceID = currentID
		summary.CurrentSlice = derived.CurrentSlice
	}

	return summary
}

// PlanLifecycleStatus is the user-facing plan lifecycle projection. A plan is
// completed after an actual recorded merge, after matching current PR/review
// evidence, or when legacy state already records completion. Other
// slice-complete plans surface their review readiness.
func PlanLifecycleStatus(detail *PlanDetail) string {
	if detail == nil {
		return ""
	}
	if PlanIsMerged(detail.Events) || PlanIsPullRequestComplete(detail) {
		return StatusCompleted
	}
	// A persisted completed status with no plan_merged event anywhere in the
	// log is legacy data: the old write path stamped completed at final slice
	// completion, so the plan finished under the old semantics and was merged
	// manually or before merge events existed. It keeps its persisted status —
	// demoting it would mass-revert months-old plans to in_review on upgrade,
	// and `tao merge` cannot prove a historical merge once the branch and head
	// snapshots are gone. Current matching PR evidence is handled above, while
	// RecordMerged appends its event, so this remaining arm preserves legacy
	// artifacts; lifecycleComplete trusts the same persisted status, keeping
	// projection and gating aligned.
	if detail.State.Status == StatusCompleted && !anyPlanMergedEvent(detail.Events) {
		return StatusCompleted
	}
	if slicesComplete(detail) || IsPostSliceStatus(detail.State.Status) {
		return reviewProjectedStatus(CurrentReview(detail))
	}
	return detail.State.Status
}

// IsPostSliceStatus reports whether a status is one a plan reaches after its
// slices are done: awaiting review, reviewed, changes requested, or the terminal
// completed. Status projection and lifecycle gating share this set so a future
// review-phase status is added in exactly one place.
func IsPostSliceStatus(status string) bool {
	switch status {
	case StatusInReview, StatusReviewed, StatusChangesRequested, StatusCompleted:
		return true
	default:
		return false
	}
}

// PlanIsMerged reports whether the plan is currently in its terminal merged
// state: a plan_merged event exists and no later plan_reopened event supersedes
// it. A plan reopened for rework after a merge is not merged again until it is
// re-merged, so a stale plan_merged event must not project `completed`.
func PlanIsMerged(events []Event) bool {
	merged := false
	for _, event := range events {
		switch event.Type {
		case EventTypePlanMerged:
			merged = true
		case EventTypePlanReopened:
			merged = false
		}
	}
	return merged
}

// PlanIsPullRequestComplete reports whether Tao has current local evidence for
// terminal PR-mode completion: a non-superseded approved review and durable PR
// metadata bound to the same non-empty head. It does not imply that the PR was
// merged or inspect any remote state.
func PlanIsPullRequestComplete(detail *PlanDetail) bool {
	if detail == nil || detail.State.Plan.PullRequest == nil {
		return false
	}
	review := CurrentReview(detail)
	prHead := detail.State.Plan.PullRequest.HeadSHA
	return review.IsApproved() && strings.TrimSpace(prHead) != "" && prHead == review.Head
}

// anyPlanMergedEvent reports whether the plan ever recorded a merge, superseded
// or not. Plans with no plan_merged event at all never participated in
// merge-event tracking, which is how PlanLifecycleStatus recognizes legacy
// completed data.
func anyPlanMergedEvent(events []Event) bool {
	for _, event := range events {
		if event.Type == EventTypePlanMerged {
			return true
		}
	}
	return false
}

// CurrentReview returns the persisted review only when it still describes the
// current plan state. A plan_reopened event recorded after the last review
// supersedes that verdict: the pending rework has not been re-reviewed, so the
// prior verdict must not leak into status projection or merge approval.
func CurrentReview(detail *PlanDetail) *PlanReview {
	if detail == nil {
		return nil
	}
	review := PersistedReview(detail)
	if review == nil || ReviewSupersededByReopen(detail.Events) {
		return nil
	}
	return review
}

// PersistedReview returns the review metadata currently stored in state.json,
// even if later events supersede it. Prefer PersistedReview when comparing or
// displaying on-disk review state; use CurrentReview when gating behavior on
// whether the plan is reviewed now.
func PersistedReview(detail *PlanDetail) *PlanReview {
	if detail == nil {
		return nil
	}
	return detail.State.Plan.Review
}

// SetPersistedReview publishes already-persisted review metadata in memory.
// It applies the same canonical replacement shape as ArtifactChangeSet without
// creating persistence intent or writing an artifact. It is a no-op for nil
// detail.
func SetPersistedReview(detail *PlanDetail, review PlanReview) {
	if detail == nil {
		return
	}
	review = normalizePlanReviewReplacement(review)
	detail.State.Plan.Review = clonePlanReview(&review)
}

// ReviewSupersededByReopen reports whether a plan_reopened event was recorded
// after the last plan_reviewed event. When true, every head snapshot captured
// during the prior cycle (review head, PR head, workspace head) predates the
// pending rework and must not be trusted as current plan state.
func ReviewSupersededByReopen(events []Event) bool {
	reopenedAfterReview := false
	for _, event := range events {
		switch event.Type {
		case EventTypePlanReviewed:
			// Only a completed review supersedes the reopen. A failed-review
			// event (RecordReviewError) copies its head snapshot from
			// pre-reopen state, so letting it reset the guard would restore
			// merge trust in stale heads while the rework remains unreviewed.
			// Events without a review payload predate this distinction and
			// keep the historical reset.
			if event.Review == nil || event.Review.Status != ReviewStatusError {
				reopenedAfterReview = false
			}
		case EventTypePlanReopened:
			reopenedAfterReview = true
		}
	}
	return reopenedAfterReview
}

// reviewProjectedStatus maps a review to the lifecycle status for a
// slice-complete, unmerged plan. A nil, error, or otherwise non-completed
// review projects in_review (awaiting a fresh verdict). It is the single source
// of the review-verdict-to-status mapping shared with RecordReviewCompleted.
func reviewProjectedStatus(review *PlanReview) string {
	if review == nil || review.Status != ReviewStatusCompleted {
		return StatusInReview
	}
	if review.Verdict == ReviewVerdictChangesRequested {
		return StatusChangesRequested
	}
	return StatusReviewed
}

func reviewVerdict(review *PlanReview) string {
	if review == nil {
		return ""
	}
	return review.Verdict
}

func SummarizePlanningSessionMetrics(stats *PlanningSessionStats, planCreatedAt time.Time) PlanningSessionMetricSummary {
	summary := PlanningSessionMetricSummary{Present: stats != nil}
	if stats == nil {
		return summary
	}
	if stats.CaptureSuspect {
		summary.UnavailableReason = stats.CaptureSuspectReason
		if summary.UnavailableReason == "" {
			summary.UnavailableReason = "planning capture marked suspect"
		}
		return summary
	}
	duration := PlanningSessionDurationForPlan(stats, planCreatedAt)
	if duration <= 0 {
		summary.UnavailableReason = "planning session duration unavailable"
		return summary
	}
	summary.Valid = true
	summary.Duration = duration
	summary.TotalTokens = stats.TotalTokens
	summary.TotalMessages = stats.TotalMessages
	return summary
}

func PlanningSessionMetricsValid(stats *PlanningSessionStats, planCreatedAt time.Time) bool {
	return SummarizePlanningSessionMetrics(stats, planCreatedAt).Valid
}

func PlanningSessionDurationForPlan(stats *PlanningSessionStats, planCreatedAt time.Time) time.Duration {
	if stats == nil || stats.CaptureSuspect || planCreatedAt.IsZero() {
		return 0
	}
	if stats.PlanningStartedAt != nil && !stats.PlanningStartedAt.IsZero() {
		if duration := positiveRoundedDuration(planCreatedAt.Sub(*stats.PlanningStartedAt)); duration > 0 {
			return duration
		}
	}
	if stats.TimeCreated == nil || stats.TimeUpdated == nil || stats.TimeCreated.IsZero() || stats.TimeUpdated.IsZero() {
		return 0
	}
	if stats.TimeCreated.After(planCreatedAt) || stats.TimeUpdated.Before(planCreatedAt) {
		return 0
	}
	return positiveRoundedDuration(planCreatedAt.Sub(*stats.TimeCreated))
}

func PlanningSessionDuration(stats *PlanningSessionStats) time.Duration {
	if stats == nil || stats.CaptureSuspect || stats.TimeCreated == nil || stats.TimeUpdated == nil || stats.TimeCreated.IsZero() || stats.TimeUpdated.IsZero() {
		return 0
	}
	return positiveRoundedDuration(stats.TimeUpdated.Sub(*stats.TimeCreated))
}

func positiveRoundedDuration(duration time.Duration) time.Duration {
	duration = duration.Round(time.Second)
	if duration <= 0 {
		return 0
	}
	return duration
}

func completedAt(detail *PlanDetail, index detailIndex) *time.Time {
	if detail.State.Plan.Timing.CompletedAt != nil {
		return detail.State.Plan.Timing.CompletedAt
	}
	if detail.State.Status != StatusCompleted {
		return nil
	}
	return index.completedAt
}

func elapsed(detail *PlanDetail, completedAt *time.Time, now time.Time) time.Duration {
	started := detail.State.Plan.Timing.StartedAt
	if started == nil {
		return 0
	}
	end := now
	if completedAt != nil {
		end = *completedAt
	}
	return end.Sub(*started).Round(time.Second)
}
