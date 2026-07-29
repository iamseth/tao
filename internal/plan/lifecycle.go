package plan

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// lifecycleCheck carries one executable-slice decision and the reason it failed.
type lifecycleCheck struct {
	SliceID string
	Slice   *Slice
	OK      bool
	Err     error
}

type lifecycleMutation struct {
	State  State
	Slices SlicesFile
	Events []Event
}

func applyLifecycleMutation(detail *PlanDetail, mutate func() ([]Event, error)) (lifecycleMutation, error) {
	events, err := mutate()
	if err != nil {
		return lifecycleMutation{}, err
	}
	return lifecycleMutation{State: detail.State, Slices: detail.Slices, Events: events}, nil
}

// lifecycleState is the read-only lifecycle decision tree; mutation helpers below
// must preserve the same current/next/pending semantics.
func lifecycleState(detail *PlanDetail, index detailIndex) Lifecycle {
	currentID := currentSliceID(detail, index)
	nextID := nextSliceID(detail, currentID)
	currentSlice := index.slice(currentID)
	complete := lifecycleComplete(detail, currentID, currentSlice)
	runnable := runnableState(detail, index, complete, nextID)
	continuable := blockedContinueState(detail, index)
	return Lifecycle{
		CurrentSliceID: currentID,
		CurrentSlice:   currentSlice,
		NextSliceID:    runnable.SliceID,
		NextSlice:      runnable.Slice,
		Complete:       complete,
		Active:         lifecycleActive(detail.State.Status, currentID, currentSlice, complete),
		Runnable:       runnable.OK,
		RunnableError:  runnable.Err,
		Continuable:    continuable.OK,
		ContinueError:  continuable.Err,
	}
}

func nextSliceID(detail *PlanDetail, currentID string) string {
	if currentID != "" {
		return currentID
	}
	if len(detail.State.Plan.PendingSlices) > 0 {
		return detail.State.Plan.PendingSlices[0]
	}
	return ""
}

// lifecycleComplete is the strict "plan is done" decision: every slice reached a
// terminal state (completed or skipped) and the executable queue is drained (see
// slicesComplete), with no active slice. It shares the drained-queue predicate
// with slicesComplete so status projection and lifecycle gating cannot drift.
//
// A skipped slice is terminal, so a plan that skips its final pending slice still
// completes. slicesComplete already allowlists skipped; gating additionally on a
// raw pending count (which counts skipped slices as pending) would re-impose
// all-completed and strand such plans as permanently incomplete.
func lifecycleComplete(detail *PlanDetail, currentID string, currentSlice *Slice) bool {
	if detail.State.Status == StatusCompleted {
		return true
	}
	if automaticSliceCompletionError(detail) != nil {
		return false
	}
	if slicesComplete(detail) {
		return !lifecycleActive(detail.State.Status, currentID, currentSlice, false)
	}
	// The slices artifact disagrees with state.json (a torn final-slice write
	// left a slice non-terminal, which also reads as an active current slice).
	// A persisted post-slice status with a drained queue proves legacy completion
	// on its own. Automatic slices are excluded above until their outcome is
	// recovered in slices.json.
	return sliceWorkSettled(detail)
}

// sliceWorkSettled reports whether the plan's executable work is finished. It
// trusts the slices artifact when it agrees (slicesComplete) and otherwise a
// persisted post-slice status with a drained queue. That fallback keeps legacy
// cross-file failures readable, but an automatic commit intent must always have
// a valid persisted outcome before finalization or review can proceed.
func sliceWorkSettled(detail *PlanDetail) bool {
	if automaticSliceCompletionError(detail) != nil {
		return false
	}
	if slicesComplete(detail) {
		return true
	}
	return IsPostSliceStatus(detail.State.Status) && len(detail.State.Plan.PendingSlices) == 0 && detail.State.Plan.CurrentSlice == nil
}

func automaticSliceCompletionError(detail *PlanDetail) error {
	if detail == nil {
		return nil
	}
	for i := range detail.Slices.Slices {
		slice := &detail.Slices.Slices[i]
		if slice.CommitIntent == nil || slice.CommitIntent.Policy != "slice" {
			continue
		}
		if slice.Completion == nil {
			return &SliceCompletionPendingError{SliceID: slice.ID, Reason: "completion outcome is missing"}
		}
		switch slice.Completion.Outcome {
		case SliceCompletionCommitted, SliceCompletionNoChanges:
			if strings.TrimSpace(slice.Completion.CommitSHA) == "" {
				return &SliceCompletionPendingError{SliceID: slice.ID, Reason: "completion commit SHA is missing"}
			}
		default:
			return &SliceCompletionPendingError{SliceID: slice.ID, Reason: fmt.Sprintf("completion outcome %q is invalid", slice.Completion.Outcome)}
		}
	}
	return nil
}

func slicesComplete(detail *PlanDetail) bool {
	if detail == nil || len(detail.Slices.Slices) == 0 {
		return false
	}
	for _, slice := range detail.Slices.Slices {
		// Allowlist the terminal statuses so a slice in any non-terminal or
		// unknown state (pending, in_progress, blocked, planned, invalid, "")
		// keeps the plan from projecting as slice-complete.
		if slice.Status != StatusCompleted && slice.Status != StatusSkipped {
			return false
		}
	}
	return len(detail.State.Plan.PendingSlices) == 0 && detail.State.Plan.CurrentSlice == nil
}

func lifecycleActive(status string, currentID string, currentSlice *Slice, complete bool) bool {
	return !complete && (status == StatusInProgress || currentID != "" || (currentSlice != nil && currentSlice.Status == StatusInProgress))
}

func active(status string, currentID string, currentSlice *Slice, complete bool) bool {
	return lifecycleActive(status, currentID, currentSlice, complete)
}

func runnableState(detail *PlanDetail, index detailIndex, complete bool, nextID string) lifecycleCheck {
	state := lifecycleCheck{SliceID: nextID, Slice: index.slice(nextID)}
	if complete {
		state.Err = fmt.Errorf("plan %s is complete", detail.State.Plan.ID)
		return state
	}
	if err := automaticSliceCompletionError(detail); err != nil {
		state.Err = err
		return state
	}
	if detail.State.Status == StatusBlocked {
		state.Err = fmt.Errorf("plan %s is blocked", detail.State.Plan.ID)
		return state
	}
	if len(detail.State.Plan.PendingSlices) == 0 || nextID == "" {
		state.Err = fmt.Errorf("plan %s has no pending slices", detail.State.Plan.ID)
		return state
	}
	if err := executableSliceError(index, state.SliceID, state.Slice, false); err != nil {
		state.Err = err
		return state
	}
	state.OK = true
	return state
}

func blockedContinueState(detail *PlanDetail, index detailIndex) lifecycleCheck {
	state := lifecycleCheck{}
	if detail.State.Status == StatusCompleted {
		state.Err = fmt.Errorf("plan %s is complete", detail.State.Plan.ID)
		return state
	}
	state.SliceID = blockedContinueSliceID(detail)
	if state.SliceID == "" {
		state.Err = fmt.Errorf("plan %s has no pending slices", detail.State.Plan.ID)
		return state
	}
	state.Slice = index.slice(state.SliceID)
	if state.Slice == nil {
		state.Err = fmt.Errorf("slice %s not found in slices.json", state.SliceID)
		return state
	}
	if detail.State.Status != StatusBlocked && state.Slice.Status != StatusBlocked {
		state.Err = fmt.Errorf("plan %s is not blocked; continue is not meaningful", detail.State.Plan.ID)
		return state
	}
	if err := executableSliceError(index, state.SliceID, state.Slice, true); err != nil {
		state.Err = err
		return state
	}
	state.OK = true
	return state
}

func blockedContinueSliceID(detail *PlanDetail) string {
	if detail.State.Plan.CurrentSlice != nil {
		return *detail.State.Plan.CurrentSlice
	}
	if len(detail.State.Plan.PendingSlices) > 0 {
		return detail.State.Plan.PendingSlices[0]
	}
	return ""
}

// ApprovalRequiredError is returned when a slice requires human approval before it can run.
// The Error() text is byte-identical to the previous fmt.Errorf message so existing
// string matching in logs and tests continues to work; callers that need typed access
// use errors.As.
type ApprovalRequiredError struct {
	SliceID string
	Reason  string
}

func (e *ApprovalRequiredError) Error() string {
	return fmt.Sprintf("slice %s requires approval: %s", e.SliceID, e.Reason)
}

func executableSliceError(index detailIndex, sliceID string, slice *Slice, allowBlocked bool) error {
	if slice == nil {
		return fmt.Errorf("slice %s not found in slices.json", sliceID)
	}
	if slice.Status == StatusBlocked && !allowBlocked {
		return fmt.Errorf("slice %s is blocked", slice.ID)
	}
	if slice.Approval != nil && slice.Approval.Required && !slice.Approval.Approved {
		return &ApprovalRequiredError{SliceID: slice.ID, Reason: slice.Approval.Reason}
	}
	if missing := missingDependencies(slice, index); len(missing) > 0 {
		return fmt.Errorf("slice %s is blocked by incomplete dependencies: %s", slice.ID, strings.Join(missing, ", "))
	}
	return nil
}

func missingDependencies(slice *Slice, index detailIndex) []string {
	var missing []string
	for _, dependency := range slice.DependsOn {
		if !index.stateCompleted[dependency] {
			missing = append(missing, dependency)
		}
	}
	return missing
}

// MarkSliceStarted and the following Mark* helpers mutate only the in-memory plan
// detail, applying deterministic metadata and returning events for artifact_io.go to persist.
func markSliceExecutionRoot(detail *PlanDetail, sliceID string, executionRoot string) error {
	if detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return classify(ErrNotFound, "slice %s not found", sliceID)
	}
	slice.ExecutionRoot = executionRoot
	return nil
}

// MarkSliceExecutionStart records the prepared branch boundary immediately
// before automatic agent work begins. The workspace mirror is part of the
// state.json write that precedes slices.json, so a torn later-slice start can
// still validate the newly captured boundary after reload.
func MarkSliceExecutionStart(detail *PlanDetail, sliceID string, start SliceExecutionStart) error {
	if detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if strings.TrimSpace(start.Branch) == "" || strings.TrimSpace(start.Head) == "" {
		return fmt.Errorf("slice %s execution boundary requires branch and head", sliceID)
	}
	if existing := slice.ExecutionStart; existing != nil {
		if *existing != start {
			if existing.Branch != start.Branch || existing.Head != start.Head {
				return fmt.Errorf("slice %s execution boundary is immutable: refusing to overwrite branch or head", sliceID)
			}
			return fmt.Errorf("slice %s execution boundary is immutable: recorded metadata differs", sliceID)
		}
	} else {
		slice.ExecutionStart = &start
	}
	if detail.State.Workspace == nil {
		detail.State.Workspace = &Workspace{}
	}
	if detail.State.Workspace.Strategy == "" {
		detail.State.Workspace.Strategy = start.WorkspaceStrategy
	}
	detail.State.Workspace.Branch = start.Branch
	detail.State.Workspace.HeadSHA = start.Head
	return nil
}

// MarkRunStartMetadata records metadata captured at run start. LastRunStartingDirty
// is intentionally normalized to an empty slice, not nil, when clean so state
// writes clear stale path tolerances with a persisted [].
func MarkRunStartMetadata(detail *PlanDetail, commitPolicy string, startingDirtyPaths []string) error {
	if detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	detail.State.Plan.LastRunCommitPolicy = commitPolicy
	detail.State.Plan.LastRunStartingDirty = cloneRunStartPaths(startingDirtyPaths)
	return nil
}

func cloneRunStartPaths(paths []string) []string {
	if len(paths) == 0 {
		return []string{}
	}
	cloned := append([]string(nil), paths...)
	slices.Sort(cloned)
	return cloned
}

// MarkFinalVerification records the repository-wide verification result and
// its activity timestamps without coupling callers to the State layout.
func MarkFinalVerification(detail *PlanDetail, verification FinalVerification) error {
	if detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	verification.VerifiedAt = verification.VerifiedAt.UTC()
	detail.State.Plan.FinalVerification = &verification
	detail.State.UpdatedAt = verification.VerifiedAt
	detail.State.Plan.Timing.LastActivityAt = &verification.VerifiedAt
	return nil
}

func MarkSliceStarted(detail *PlanDetail, sliceID string, now time.Time) (Event, bool, error) {
	if detail == nil {
		return Event{}, false, fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return Event{}, false, classify(ErrNotFound, "slice %s not found", sliceID)
	}
	detail.State.Status = StatusInProgress
	detail.State.UpdatedAt = now
	detail.State.Plan.CurrentSlice = new(sliceID)
	if detail.State.Plan.Timing.StartedAt == nil {
		detail.State.Plan.Timing.StartedAt = new(now)
	}
	detail.State.Plan.Timing.LastActivityAt = new(now)

	slice.Status = StatusInProgress
	if slice.Timing.StartedAt == nil {
		slice.Timing.StartedAt = new(now)
	}
	slice.Timing.UpdatedAt = now
	slice.Timing.LastActivityAt = new(now)

	event := Event{Type: EventTypeSliceStarted, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Work started on slice"}
	return event, !hasSliceStartedEvent(detail.Events, sliceID), nil
}

// MarkSliceCommitIntent records the immutable intent that precedes Git mutation.
func MarkSliceCommitIntent(detail *PlanDetail, sliceID string, intent SliceCommitIntent) error {
	if detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if slice.CommitIntent != nil {
		if *slice.CommitIntent == intent {
			return nil
		}
		return fmt.Errorf("slice %s has a conflicting commit intent", sliceID)
	}
	if slice.Completion != nil {
		return fmt.Errorf("slice %s already has a completion outcome", sliceID)
	}
	slice.CommitIntent = &intent
	return nil
}

// MarkSliceCompleted applies deterministic completion metadata to an in-memory plan detail.
func MarkSliceCompleted(detail *PlanDetail, sliceID string, notes string, verificationResults []VerificationRun, now time.Time) (Event, bool, error) {
	return MarkSliceCompletedWithOutcome(detail, sliceID, notes, verificationResults, nil, now)
}

// MarkSliceCompletedWithOutcome applies completion metadata and its Git outcome.
func MarkSliceCompletedWithOutcome(detail *PlanDetail, sliceID string, notes string, verificationResults []VerificationRun, outcome *SliceCompletionOutcome, now time.Time) (Event, bool, error) {
	if detail == nil {
		return Event{}, false, fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return Event{}, false, classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if slice.Timing.StartedAt == nil {
		return Event{}, false, fmt.Errorf("slice %s has no started_at", sliceID)
	}
	durationSeconds := max(int64(now.Sub(*slice.Timing.StartedAt).Seconds()), 0)

	slice.Status = StatusCompleted
	slice.Timing.CompletedAt = new(now)
	slice.Timing.UpdatedAt = now
	slice.Timing.LastActivityAt = new(now)
	slice.Timing.DurationSeconds = &durationSeconds
	slice.Notes = notes
	slice.VerificationResults = verificationResults
	if outcome != nil {
		if slice.CommitIntent == nil {
			return Event{}, false, fmt.Errorf("slice %s has no commit intent", sliceID)
		}
		if slice.Completion != nil && *slice.Completion != *outcome {
			return Event{}, false, fmt.Errorf("slice %s has a conflicting completion outcome", sliceID)
		}
		slice.Completion = outcome
		refreshCompletedWorkspaceBoundary(detail, slice, *outcome)
	}

	detail.State.Plan.PendingSlices = slices.DeleteFunc(detail.State.Plan.PendingSlices, func(value string) bool { return value == sliceID })
	if !slices.Contains(detail.State.Plan.CompletedSlices, sliceID) {
		detail.State.Plan.CompletedSlices = append(detail.State.Plan.CompletedSlices, sliceID)
	}
	detail.State.Plan.CurrentSlice = nil
	detail.State.UpdatedAt = now
	detail.State.Plan.Timing.LastActivityAt = new(now)
	if len(detail.State.Plan.PendingSlices) == 0 {
		detail.State.Status = StatusInReview
		detail.State.Plan.Timing.CompletedAt = new(now)
	} else {
		detail.State.Status = StatusInProgress
	}

	event := Event{Type: EventTypeSliceCompleted, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, DurationSeconds: &durationSeconds, Message: "Slice completed and verified"}
	return event, !hasSliceCompletedEvent(detail.Events, sliceID), nil
}

func refreshCompletedWorkspaceBoundary(detail *PlanDetail, slice *Slice, outcome SliceCompletionOutcome) {
	if detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != slice.ID ||
		slice.ExecutionStart == nil || slice.ExecutionStart.WorkspaceStrategy != WorkspaceStrategyWorktree ||
		(outcome.Outcome != SliceCompletionCommitted && outcome.Outcome != SliceCompletionNoChanges) ||
		strings.TrimSpace(outcome.CommitSHA) == "" {
		return
	}
	if detail.State.Workspace == nil {
		detail.State.Workspace = &Workspace{Strategy: WorkspaceStrategyWorktree}
	}
	detail.State.Workspace.Branch = slice.ExecutionStart.Branch
	detail.State.Workspace.HeadSHA = outcome.CommitSHA
}

// MarkSliceApproved records human approval for an approval-gated slice.
func MarkSliceApproved(detail *PlanDetail, sliceID string, approvedBy string, now time.Time) (Event, bool, error) {
	if detail == nil {
		return Event{}, false, fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return Event{}, false, classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if slice.Approval == nil || !slice.Approval.Required {
		return Event{}, false, classify(ErrApprovalNotRequired, "slice %s does not require approval", sliceID)
	}
	if strings.TrimSpace(approvedBy) == "" {
		return Event{}, false, classify(ErrApproverRequired, "approved_by is required")
	}
	if slice.Approval.Approved {
		return Event{Type: EventTypeSliceApproved, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Slice approval already recorded"}, false, nil
	}

	approvedAt := now.UTC().Format(time.RFC3339)
	slice.Approval.Approved = true
	slice.Approval.ApprovedBy = new(strings.TrimSpace(approvedBy))
	slice.Approval.ApprovedAt = new(approvedAt)
	detail.State.UpdatedAt = now
	detail.State.Plan.Timing.LastActivityAt = new(now)
	slice.Timing.UpdatedAt = now
	slice.Timing.LastActivityAt = new(now)

	event := Event{Type: EventTypeSliceApproved, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Slice approved"}
	return event, !hasSliceApprovedEvent(detail.Events, sliceID), nil
}

const maxBlockerNoteRunes = 16 * 1024

// MarkSliceBlocked records a canonical exceptional stop while retaining all
// execution and queue metadata needed to continue or recover the slice.
func MarkSliceBlocked(detail *PlanDetail, sliceID string, reason string, now time.Time) (Event, bool, error) {
	if detail == nil {
		return Event{}, false, fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return Event{}, false, classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if slice.Status == StatusCompleted || slices.Contains(detail.State.Plan.CompletedSlices, sliceID) {
		return Event{}, false, fmt.Errorf("slice %s is completed and cannot be blocked", sliceID)
	}
	if slice.Status == StatusSkipped {
		return Event{}, false, fmt.Errorf("slice %s is skipped and cannot be blocked", sliceID)
	}
	note := strings.TrimSpace(reason)
	if note == "" {
		return Event{}, false, fmt.Errorf("blocker reason is required")
	}
	if runes := []rune(note); len(runes) > maxBlockerNoteRunes {
		note = string(runes[:maxBlockerNoteRunes])
	}
	if blockedID := conflictingBlockedSliceID(detail, sliceID); blockedID != "" {
		return Event{}, false, fmt.Errorf("cannot block slice %s while slice %s is already blocked", sliceID, blockedID)
	}
	if current := detail.State.Plan.CurrentSlice; current != nil && *current != sliceID {
		return Event{}, false, fmt.Errorf("cannot block slice %s while current slice is %s", sliceID, *current)
	}

	appendEvent := slice.Status != StatusBlocked || !hasSliceBlockedEvent(detail.Events, sliceID)
	detail.State.Status = StatusBlocked
	detail.State.UpdatedAt = now
	if detail.State.Plan.CurrentSlice == nil {
		detail.State.Plan.CurrentSlice = new(sliceID)
	}
	if !slices.Contains(detail.State.Plan.PendingSlices, sliceID) {
		detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, sliceID)
	}
	detail.State.Plan.Timing.LastActivityAt = new(now)
	slice.Status = StatusBlocked
	slice.BlockerNote = note
	slice.Timing.UpdatedAt = now
	slice.Timing.LastActivityAt = new(now)

	event := Event{Type: EventTypeSliceBlocked, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Reason: note, Message: "Slice blocked"}
	return event, appendEvent, nil
}

func conflictingBlockedSliceID(detail *PlanDetail, sliceID string) string {
	for i := range detail.Slices.Slices {
		slice := &detail.Slices.Slices[i]
		if slice.ID != sliceID && slice.Status == StatusBlocked {
			return slice.ID
		}
	}
	if detail.State.Status == StatusBlocked {
		blockedID := blockedContinueSliceID(detail)
		if blockedID != "" && blockedID != sliceID {
			return blockedID
		}
	}
	return ""
}

// MarkSliceBudgetBlocked records an enforced telemetry stop. Unlike an ordinary
// exceptional stop, it may supersede completion recorded by the active agent:
// the completion evidence stays intact so continuation enters the standard
// guarded completion-recovery path instead of running the agent again.
func MarkSliceBudgetBlocked(detail *PlanDetail, sliceID string, reason string, now time.Time) (Event, bool, error) {
	if detail == nil {
		return Event{}, false, fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return Event{}, false, classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if slice.Status == StatusCompleted || slices.Contains(detail.State.Plan.CompletedSlices, sliceID) {
		detail.State.Plan.CompletedSlices = slices.DeleteFunc(detail.State.Plan.CompletedSlices, func(value string) bool { return value == sliceID })
		detail.State.Plan.Timing.CompletedAt = nil
		slice.Status = StatusInProgress
		slice.Timing.CompletedAt = nil
		slice.Timing.DurationSeconds = nil
	}
	return MarkSliceBlocked(detail, sliceID, reason, now)
}

// MarkBlockedContinued selects the blocked/current slice and marks plan-owned lifecycle back in progress.
func MarkBlockedContinued(detail *PlanDetail, now time.Time) error {
	if detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	continuable := blockedContinueState(detail, newDetailIndex(detail))
	if !continuable.OK {
		return continuable.Err
	}
	slice := continuable.Slice

	detail.State.Status = StatusInProgress
	detail.State.UpdatedAt = now
	detail.State.Plan.CurrentSlice = new(slice.ID)
	if detail.State.Plan.Timing.StartedAt == nil {
		detail.State.Plan.Timing.StartedAt = new(now)
	}
	detail.State.Plan.Timing.LastActivityAt = new(now)
	slice.Status = StatusInProgress
	slice.BlockerNote = ""
	slice.Timing.UpdatedAt = now
	slice.Timing.LastActivityAt = new(now)
	return nil
}

// Reopen transitions a reviewed plan back to runnable state by appending new pending slices.
func Reopen(detail *PlanDetail, newSlices []Slice, now time.Time) (Event, error) {
	if detail == nil {
		return Event{}, fmt.Errorf("plan detail is nil")
	}
	if !ReopenableStatus(detail.State.Status) {
		return Event{}, classify(ErrInvalid, "plan %s is %s; only reviewed plans can be reopened", detail.State.Plan.ID, detail.State.Status)
	}
	if len(newSlices) == 0 {
		return Event{}, classify(ErrInvalid, "plan %s cannot be reopened without pending slices", detail.State.Plan.ID)
	}
	if err := validateReopenSlices(detail, newSlices); err != nil {
		return Event{}, err
	}

	for _, slice := range newSlices {
		pending := cloneSlice(slice)
		detail.Slices.Slices = append(detail.Slices.Slices, pending)
		detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, pending.ID)
	}
	detail.State.Status = StatusInProgress
	detail.State.UpdatedAt = now
	detail.State.Plan.CurrentSlice = nil
	detail.State.Plan.Timing.CompletedAt = nil
	if detail.State.Plan.Timing.StartedAt == nil {
		detail.State.Plan.Timing.StartedAt = new(now)
	}
	detail.State.Plan.Timing.LastActivityAt = new(now)

	event := Event{Type: EventTypePlanReopened, Timestamp: now, PlanID: detail.State.Plan.ID, Message: "Plan reopened for rework"}
	return event, nil
}

// Reopen transitions the bound completed plan through the file-backed mutation path.
func (r *PlanRecord) Reopen(newSlices []Slice, now time.Time) error {
	return r.apply(reopenMutation(newSlices, now, false))
}

// ReopenForced explicitly bypasses the reopen status gate while still applying
// the override to the latest settled detail under the mutation lock.
func (r *PlanRecord) ReopenForced(newSlices []Slice, now time.Time) error {
	return r.apply(reopenMutation(newSlices, now, true))
}

func reopenMutation(newSlices []Slice, now time.Time, force bool) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		expected := Event{Type: EventTypePlanReopened, Timestamp: now, PlanID: detail.State.Plan.ID, Message: "Plan reopened for rework"}
		if semanticEventsWereRecorded(detail.Events, []Event{expected}) && reopenPostconditionMatches(detail, newSlices) {
			return unchangedLifecycleMutation(detail), nil
		}
		return applyLifecycleMutation(detail, func() ([]Event, error) {
			if force && !ReopenableStatus(detail.State.Status) {
				detail.State.Status = StatusChangesRequested
			}
			event, err := Reopen(detail, newSlices, now)
			if err != nil {
				return nil, err
			}
			return []Event{event}, nil
		})
	}
}

func reopenPostconditionMatches(detail *PlanDetail, reopened []Slice) bool {
	if detail == nil || len(reopened) == 0 || detail.State.Status != StatusInProgress || detail.State.Plan.CurrentSlice != nil || detail.State.Plan.Timing.CompletedAt != nil {
		return false
	}
	for _, candidate := range reopened {
		slice := findSlice(detail, candidate.ID)
		if slice == nil || !slices.Contains(detail.State.Plan.PendingSlices, candidate.ID) || !reopenSliceSemanticallyEqual(*slice, candidate) {
			return false
		}
	}
	return true
}

func reopenSliceSemanticallyEqual(recorded, requested Slice) bool {
	recorded.Timing.CreatedAt = time.Time{}
	recorded.Timing.StartedAt = nil
	recorded.Timing.CompletedAt = nil
	recorded.Timing.UpdatedAt = time.Time{}
	recorded.Timing.LastActivityAt = nil
	requested.Timing.CreatedAt = time.Time{}
	requested.Timing.StartedAt = nil
	requested.Timing.CompletedAt = nil
	requested.Timing.UpdatedAt = time.Time{}
	requested.Timing.LastActivityAt = nil
	return reflect.DeepEqual(recorded, requested)
}

func validateReopenSlices(detail *PlanDetail, newSlices []Slice) error {
	seen := make(map[string]bool, len(detail.Slices.Slices)+len(newSlices))
	for _, slice := range detail.Slices.Slices {
		if slice.ID != "" {
			seen[slice.ID] = true
		}
	}
	for i, slice := range newSlices {
		if strings.TrimSpace(slice.ID) == "" {
			return classify(ErrInvalid, "reopen slice at index %d has empty id", i)
		}
		if seen[slice.ID] {
			return classify(ErrInvalid, "cannot reopen plan %s with duplicate slice id %s", detail.State.Plan.ID, slice.ID)
		}
		if slice.Status != StatusPending {
			return classify(ErrInvalid, "reopen slice %s is %s; new slices must be pending", slice.ID, slice.Status)
		}
		seen[slice.ID] = true
	}
	return nil
}

// MarkSliceRemoved removes one pending slice from the executable plan queue.
func MarkSliceRemoved(detail *PlanDetail, sliceID string, now time.Time) (Event, error) {
	if detail == nil {
		return Event{}, fmt.Errorf("plan detail is nil")
	}
	slice, err := editablePendingSlice(detail, sliceID)
	if err != nil {
		return Event{}, err
	}
	if dependents := pendingDependents(detail, sliceID); len(dependents) > 0 {
		return Event{}, fmt.Errorf("cannot remove slice %s; pending slices depend on it: %s", sliceID, strings.Join(dependents, ", "))
	}

	detail.State.Plan.PendingSlices = slices.DeleteFunc(detail.State.Plan.PendingSlices, func(value string) bool { return value == sliceID })
	detail.Slices.Slices = removeSlice(detail.Slices.Slices, sliceID)
	markPlanEdited(detail, now)
	slice.Timing.UpdatedAt = now
	event := Event{Type: EventTypeSliceRemoved, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Pending slice removed by plan edit"}
	return event, nil
}

// MarkSliceSkipped marks one pending slice skipped while preserving its audit record.
func MarkSliceSkipped(detail *PlanDetail, sliceID string, now time.Time) (Event, error) {
	if detail == nil {
		return Event{}, fmt.Errorf("plan detail is nil")
	}
	slice, err := editablePendingSlice(detail, sliceID)
	if err != nil {
		return Event{}, err
	}
	if dependents := pendingDependents(detail, sliceID); len(dependents) > 0 {
		return Event{}, fmt.Errorf("cannot skip slice %s; pending slices depend on it: %s", sliceID, strings.Join(dependents, ", "))
	}

	detail.State.Plan.PendingSlices = slices.DeleteFunc(detail.State.Plan.PendingSlices, func(value string) bool { return value == sliceID })
	slice.Status = StatusSkipped
	slice.Timing.UpdatedAt = now
	slice.Timing.LastActivityAt = new(now)
	markPlanEdited(detail, now)
	event := Event{Type: EventTypeSliceSkipped, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Pending slice skipped by plan edit"}
	return event, nil
}

// MarkPendingSlicesReordered replaces the pending queue after dependency validation.
func MarkPendingSlicesReordered(detail *PlanDetail, pendingOrder []string, now time.Time) (Event, error) {
	if detail == nil {
		return Event{}, fmt.Errorf("plan detail is nil")
	}
	if err := validatePendingReorder(detail, pendingOrder); err != nil {
		return Event{}, err
	}
	detail.State.Plan.PendingSlices = append([]string(nil), pendingOrder...)
	markPlanEdited(detail, now)
	event := Event{Type: EventTypeSlicesReordered, Timestamp: now, PlanID: detail.State.Plan.ID, Message: "Pending slices reordered by plan edit"}
	return event, nil
}

func findSlice(detail *PlanDetail, sliceID string) *Slice {
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == sliceID {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}

func editablePendingSlice(detail *PlanDetail, sliceID string) (*Slice, error) {
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return nil, classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if slice.Status != StatusPending {
		return nil, fmt.Errorf("slice %s is %s; only pending slices can be edited", sliceID, slice.Status)
	}
	if !slices.Contains(detail.State.Plan.PendingSlices, sliceID) {
		return nil, fmt.Errorf("slice %s is not in pending_slices", sliceID)
	}
	return slice, nil
}

func pendingDependents(detail *PlanDetail, dependencyID string) []string {
	var dependents []string
	for _, slice := range detail.Slices.Slices {
		if slice.ID == dependencyID || slice.Status != StatusPending || !slices.Contains(detail.State.Plan.PendingSlices, slice.ID) {
			continue
		}
		if slices.Contains(slice.DependsOn, dependencyID) {
			dependents = append(dependents, slice.ID)
		}
	}
	return dependents
}

func validatePendingReorder(detail *PlanDetail, pendingOrder []string) error {
	if len(pendingOrder) != len(detail.State.Plan.PendingSlices) {
		return fmt.Errorf("pending reorder must include every pending slice")
	}
	existing := make(map[string]bool, len(detail.State.Plan.PendingSlices))
	for _, id := range detail.State.Plan.PendingSlices {
		existing[id] = true
	}
	seen := make(map[string]bool, len(pendingOrder))
	position := make(map[string]int, len(pendingOrder))
	for i, id := range pendingOrder {
		if !existing[id] {
			return fmt.Errorf("slice %s is not in pending_slices", id)
		}
		if seen[id] {
			return fmt.Errorf("pending reorder contains duplicate slice %s", id)
		}
		seen[id] = true
		position[id] = i
		slice, err := editablePendingSlice(detail, id)
		if err != nil {
			return err
		}
		for _, dependency := range slice.DependsOn {
			dependencyPosition, dependencyPending := position[dependency]
			if slices.Contains(detail.State.Plan.PendingSlices, dependency) && (!dependencyPending || dependencyPosition > i) {
				return fmt.Errorf("cannot reorder slice %s before pending dependency %s", id, dependency)
			}
		}
	}
	return nil
}

func markPlanEdited(detail *PlanDetail, now time.Time) {
	detail.State.UpdatedAt = now
	detail.State.Plan.Timing.LastActivityAt = new(now)
	if detail.State.Plan.CurrentSlice != nil && !slices.Contains(detail.State.Plan.PendingSlices, *detail.State.Plan.CurrentSlice) {
		detail.State.Plan.CurrentSlice = nil
	}
	if len(detail.State.Plan.PendingSlices) == 0 && detail.State.Plan.CurrentSlice == nil {
		detail.State.Status = StatusInReview
		detail.State.Plan.Timing.CompletedAt = new(now)
	}
}

// ReopenableStatus reports whether a plan status permits reopening for rework.
// The reopen mutation and the CLI rework gate share this set so a new
// post-review status is admitted (or refused) in exactly one place.
func ReopenableStatus(status string) bool {
	return status == StatusReviewed || status == StatusChangesRequested || status == StatusCompleted
}

func removeSlice(slices []Slice, sliceID string) []Slice {
	filtered := slices[:0]
	for _, slice := range slices {
		if slice.ID != sliceID {
			filtered = append(filtered, slice)
		}
	}
	return filtered
}

func hasSliceStartedEvent(events []Event, sliceID string) bool {
	for _, event := range events {
		if event.Type == EventTypeSliceStarted && event.SliceID == sliceID {
			return true
		}
	}
	return false
}

func hasSliceCompletedEvent(events []Event, sliceID string) bool {
	for _, event := range events {
		if event.Type == EventTypeSliceCompleted && event.SliceID == sliceID {
			return true
		}
	}
	return false
}

func hasSliceApprovedEvent(events []Event, sliceID string) bool {
	for _, event := range events {
		if event.Type == EventTypeSliceApproved && event.SliceID == sliceID {
			return true
		}
	}
	return false
}

func hasSliceBlockedEvent(events []Event, sliceID string) bool {
	for _, event := range events {
		if event.Type == EventTypeSliceBlocked && event.SliceID == sliceID {
			return true
		}
	}
	return false
}
