package plan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// PlanRecord binds one loaded plan detail to the directory that owns its mutable artifacts.
type PlanRecord struct {
	dir      string
	detail   *PlanDetail
	baseline *PlanDetail
	store    artifactMutationStore
}

// NewPlanRecord prepares a file-backed record for lifecycle and edit mutations.
func NewPlanRecord(planDir string, detail *PlanDetail) (*PlanRecord, error) {
	return newPlanRecord(fileArtifactStore{}, planDir, detail)
}

// ArtifactStore is the combined exported mutation interface for plan record operations.
// Implement it to redirect plan artifact mutations, for example to an in-memory
// store in a test-support package.
type ArtifactStore interface {
	WriteState(planDir string, state State) error
	WriteSlices(planDir string, slices SlicesFile) error
	AppendEvent(planDir string, event Event) error
}

// NewPlanRecordWithStore creates a plan record backed by the provided store.
// Test-support packages use this to redirect mutations away from the filesystem.
func NewPlanRecordWithStore(store ArtifactStore, planDir string, detail *PlanDetail) (*PlanRecord, error) {
	return newPlanRecord(artStoreAdapter{store: store}, planDir, detail)
}

type artStoreAdapter struct{ store ArtifactStore }

func (a artStoreAdapter) writeState(d string, s State) error { return a.store.WriteState(d, s) }
func (a artStoreAdapter) writeSlices(d string, sf SlicesFile) error {
	return a.store.WriteSlices(d, sf)
}
func (a artStoreAdapter) appendEvent(d string, e Event) error { return a.store.AppendEvent(d, e) }
func (a artStoreAdapter) withMutationLock(_ string, operation func() error) error {
	return operation()
}
func (a artStoreAdapter) refreshMutationDetailLocked(string, string, bool) (mutationDetailRefresh, error) {
	return mutationDetailRefresh{}, nil
}
func (a artStoreAdapter) settleMutationLocked(planDir string, journal mutationJournal) error {
	if journal.State != nil {
		var state State
		if err := json.Unmarshal(journal.State.Payload, &state); err != nil {
			return fmt.Errorf("decode prepared state.json: %w", err)
		}
		if err := a.store.WriteState(planDir, state); err != nil {
			return err
		}
	}
	if journal.Slices != nil {
		var slicesFile SlicesFile
		if err := json.Unmarshal(journal.Slices.Payload, &slicesFile); err != nil {
			return fmt.Errorf("decode prepared slices.json: %w", err)
		}
		if err := a.store.WriteSlices(planDir, slicesFile); err != nil {
			return err
		}
	}
	for _, entry := range journal.Events {
		var event Event
		if err := json.Unmarshal(entry.Payload, &event); err != nil {
			return fmt.Errorf("decode prepared events.jsonl: %w", err)
		}
		if err := a.store.AppendEvent(planDir, event); err != nil {
			return fmt.Errorf("append events.jsonl: %w", err)
		}
	}
	return nil
}

func newPlanRecord(store artifactMutationStore, planDir string, detail *PlanDetail) (*PlanRecord, error) {
	if detail == nil {
		return nil, fmt.Errorf("plan detail is nil")
	}
	dir := planDir
	if dir == "" {
		dir = detail.Dir
	}
	if dir == "" {
		return nil, fmt.Errorf("plan directory is required")
	}
	if detail.Dir != "" && !samePlanDir(dir, detail.Dir) {
		return nil, fmt.Errorf("plan directory %q does not match loaded detail directory %q", dir, detail.Dir)
	}
	if store == nil {
		store = fileArtifactStore{}
	}
	baseline := clonePlanDetail(detail)
	hasLoadedBaseline := detail.loadedStateBaseline != nil
	if detail.loadedStateBaseline != nil {
		baseline.State = cloneState(*detail.loadedStateBaseline)
	}
	if detail.loadedSlicesBaseline != nil {
		baseline.Slices = cloneSlicesFile(*detail.loadedSlicesBaseline)
	}
	if _, fileBacked := store.(fileArtifactStore); fileBacked {
		persisted, recovered, err := loadFileMutationBaseline(dir, detail.State.Plan.ID)
		if err != nil {
			return nil, err
		}
		if persisted != nil && recovered {
			if conflictingArtifactStructureChanges(baseline, detail, persisted) {
				return nil, fmt.Errorf("bind plan record: concurrent structural change; reload the plan and retry")
			}
			rebased := clonePlanDetail(persisted)
			rebased.State = rebaseArtifact(baseline.State, detail.State, persisted.State)
			rebased.Slices = rebaseArtifact(baseline.Slices, detail.Slices, persisted.Slices)
			baseline = clonePlanDetail(persisted)
			*detail = *rebased
		} else if persisted != nil && !hasLoadedBaseline {
			baseline = clonePlanDetail(persisted)
		}
	}
	return &PlanRecord{dir: dir, detail: detail, baseline: baseline, store: store}, nil
}

// Dir returns the artifact directory bound to the record.
func (r *PlanRecord) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// Detail returns the loaded detail bound to the record.
func (r *PlanRecord) Detail() *PlanDetail {
	if r == nil {
		return nil
	}
	return r.detail
}

func (r *PlanRecord) StartSlice(sliceID string, now time.Time) error {
	return r.apply(startSliceMutation(sliceID, "", now))
}

func (r *PlanRecord) StartSliceWithExecutionRoot(sliceID string, executionRoot string, now time.Time) error {
	return r.apply(startSliceMutation(sliceID, executionRoot, now))
}

func (r *PlanRecord) StartSliceWithRunCommitPolicy(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, now time.Time) error {
	return r.startSliceWithRunBoundary(sliceID, executionRoot, commitPolicy, startingDirtyPaths, nil, now)
}

// StartSliceWithRunBoundary atomically persists lifecycle start metadata and the
// Git boundary prepared for automatic slice work.
func (r *PlanRecord) StartSliceWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary SliceExecutionStart, now time.Time) error {
	return r.startSliceWithRunBoundary(sliceID, executionRoot, commitPolicy, startingDirtyPaths, &boundary, now)
}

func (r *PlanRecord) startSliceWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary *SliceExecutionStart, now time.Time) error {
	return r.apply(startSliceWithRunBoundaryMutation(sliceID, executionRoot, commitPolicy, startingDirtyPaths, boundary, now))
}

// RepairSliceStartWithRunBoundary completes a torn automatic start using its
// previously validated execution boundary and original start time. It is
// idempotent for state-advanced, slices-advanced, and missing-event prefixes.
func (r *PlanRecord) RepairSliceStartWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary SliceExecutionStart, startedAt time.Time) error {
	return r.apply(startSliceWithRunBoundaryMutation(sliceID, executionRoot, commitPolicy, startingDirtyPaths, &boundary, startedAt))
}

// RepairMissingSliceStartedEvent restores only missing start-event evidence for
// an otherwise complete start, retaining the lifecycle's original timestamp.
func (r *PlanRecord) RepairMissingSliceStartedEvent(sliceID string, startedAt time.Time) error {
	return r.apply(repairMissingSliceStartedEventMutation(sliceID, startedAt))
}

func (r *PlanRecord) CompleteSlice(sliceID string, notes string, verificationResults []VerificationRun, now time.Time) error {
	return r.apply(completeSliceMutation(sliceID, notes, verificationResults, now))
}

// RecordSliceCommitIntent durably records completion intent before Git mutation.
func (r *PlanRecord) RecordSliceCommitIntent(sliceID string, intent SliceCommitIntent) error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	if err := MarkSliceCommitIntent(r.detail, sliceID, intent); err != nil {
		return err
	}
	return r.applySlicesUpdate(store, func(detail *PlanDetail) error {
		return MarkSliceCommitIntent(detail, sliceID, intent)
	})
}

// CompleteSliceWithOutcome persists lifecycle completion and its Git outcome.
func (r *PlanRecord) CompleteSliceWithOutcome(sliceID string, notes string, verificationResults []VerificationRun, outcome SliceCompletionOutcome, now time.Time) error {
	return r.apply(completeSliceWithOutcomeMutation(sliceID, notes, verificationResults, &outcome, now))
}

func (r *PlanRecord) ApproveSlice(sliceID string, approvedBy string, now time.Time) error {
	return r.apply(approveSliceMutation(sliceID, approvedBy, now))
}

// BlockSlice records an exceptional stop without discarding execution-boundary metadata.
func (r *PlanRecord) BlockSlice(sliceID string, reason string, now time.Time) error {
	return r.apply(blockSliceMutation(sliceID, reason, now))
}

func (r *PlanRecord) ContinueBlocked(now time.Time) error {
	return r.applyWithRecoveredMatch(continueBlockedMutation(now), blockedContinuationWasRecovered)
}

func (r *PlanRecord) RemoveSlice(sliceID string, now time.Time) error {
	return r.apply(removeSliceMutation(sliceID, now))
}

func (r *PlanRecord) SkipSlice(sliceID string, now time.Time) error {
	return r.apply(skipSliceMutation(sliceID, now))
}

func (r *PlanRecord) ReorderPendingSlices(pendingOrder []string, now time.Time) error {
	return r.apply(reorderPendingSlicesMutation(pendingOrder, now))
}

// storeOrDefault returns the record's store (defaulting to the file-backed
// store) after validating the record's essential fields. It is the shared guard
// for PlanRecord operations that write selected artifacts directly.
func (r *PlanRecord) storeOrDefault() (artifactMutationStore, error) {
	if r == nil {
		return nil, fmt.Errorf("plan record is nil")
	}
	if r.detail == nil {
		return nil, fmt.Errorf("plan detail is nil")
	}
	if r.dir == "" {
		return nil, fmt.Errorf("plan directory is required")
	}
	if r.store != nil {
		return r.store, nil
	}
	return fileArtifactStore{}, nil
}

// PersistState writes the record's current in-memory state to the artifact
// directory without any lifecycle transition or event. Use this for incremental
// metadata stamps (workspace preparation milestones, starting-branch recording)
// that only update state.json and generate no events.
func (r *PlanRecord) PersistState() error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateUpdate(store, r.stateBaseline(), r.detail.State)
}

// RecordFinalVerification stamps repository-wide verification state and its
// activity timestamps, then persists state.json without exposing artifact
// layout or timestamp-field selection to the caller.
func (r *PlanRecord) RecordFinalVerification(verification FinalVerification) error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	baseline := r.stateBaseline()
	if err := MarkFinalVerification(r.detail, verification); err != nil {
		return err
	}
	return r.applyStateUpdate(store, baseline, r.detail.State)
}

// PersistArtifacts writes the record's current state and slices to the artifact
// directory without any lifecycle transition or event. Both targets settle
// through the same recoverable journal used by lifecycle mutations.
func (r *PlanRecord) PersistArtifacts() error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	err = applyArtifactMutationPreservingDetail(store, r.dir, r.detail, r.baseline, func(detail *PlanDetail) (lifecycleMutation, error) {
		return lifecycleMutation{State: detail.State, Slices: detail.Slices}, nil
	})
	if err == nil {
		r.advanceBaseline()
	}
	return err
}

// RecordStartingBranch stamps the resolved repo branch onto state and persists
// it. Called when execution mode is current and the live branch differs from
// the plan-recorded branch.
func (r *PlanRecord) RecordStartingBranch(branch string) error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	baseline := r.stateBaseline()
	r.detail.State.Repo.Branch = branch
	return r.applyStateUpdate(store, baseline, r.detail.State)
}

// RecordPullRequest stamps pull request metadata onto state, updates workspace
// tracking fields, and appends a pull_request_created event.
func (r *PlanRecord) RecordPullRequest(pr PullRequest, branch, headSHA string) error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateEvent(store, func(detail *PlanDetail) ([]Event, error) {
		pr.Branch = branch
		pr.HeadSHA = headSHA
		detail.State.Plan.PullRequest = &pr
		if detail.State.Workspace == nil {
			detail.State.Workspace = &Workspace{}
		}
		detail.State.Workspace.Branch = branch
		detail.State.Workspace.HeadSHA = headSHA
		detail.State.Workspace.PushedSHA = headSHA
		detail.State.UpdatedAt = pr.CreatedAt
		detail.State.Plan.Timing.LastActivityAt = &pr.CreatedAt
		event := Event{Type: EventTypePullRequestCreated, Timestamp: pr.CreatedAt, PlanID: detail.State.Plan.ID, PullRequest: &pr, Message: "Pull request created"}
		return []Event{event}, nil
	})
}

// RecordSingleMergeCommitIntent persists the exact squash transaction boundary
// before Tao checks out or mutates the default branch. Matching retries are
// idempotent; callers must explicitly clear a stale source intent first.
func (r *PlanRecord) RecordSingleMergeCommitIntent(intent SingleMergeCommitIntent) error {
	if err := validateSingleMergeCommitIntent(intent); err != nil {
		return err
	}
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateEvent(store, func(detail *PlanDetail) ([]Event, error) {
		existing := detail.State.Plan.MergeCommitIntent
		if existing != nil {
			if *existing == intent {
				return nil, nil
			}
			return nil, fmt.Errorf("plan %s has a conflicting single-merge commit intent", detail.State.Plan.ID)
		}
		detail.State.Plan.MergeCommitIntent = cloneSingleMergeCommitIntent(&intent)
		return nil, nil
	})
}

// ClearSingleMergeCommitIntent clears only the exact intent the caller
// inspected, preventing a stale recovery path from deleting newer intent.
func (r *PlanRecord) ClearSingleMergeCommitIntent(expected SingleMergeCommitIntent) error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateEvent(store, func(detail *PlanDetail) ([]Event, error) {
		existing := detail.State.Plan.MergeCommitIntent
		if existing == nil {
			return nil, nil
		}
		if *existing != expected {
			return nil, fmt.Errorf("plan %s single-merge commit intent changed; reload and retry", detail.State.Plan.ID)
		}
		detail.State.Plan.MergeCommitIntent = nil
		return nil, nil
	})
}

func validateSingleMergeCommitIntent(intent SingleMergeCommitIntent) error {
	if strings.TrimSpace(intent.Message) == "" || intent.Message != strings.TrimSpace(intent.Message) {
		return fmt.Errorf("single-merge commit intent requires an exact trimmed message")
	}
	for label, value := range map[string]string{
		"plan id": intent.PlanID, "source head": intent.SourceHead,
		"default branch": intent.DefaultBranch, "default parent": intent.DefaultParent,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("single-merge commit intent requires a valid %s", label)
		}
	}
	if intent.CreatedAt.IsZero() {
		return fmt.Errorf("single-merge commit intent requires creation time")
	}
	return nil
}

// RecordReviewError stamps a failed-review result onto state and appends a
// plan_reviewed event. The caller sets review.ReviewedAt before calling.
func (r *PlanRecord) RecordReviewError(review PlanReview, agent string) error {
	review.CommitMessage = nil
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateEvent(store, func(detail *PlanDetail) ([]Event, error) {
		if err := automaticSliceCompletionError(detail); err != nil {
			return nil, fmt.Errorf("record plan review: %w", err)
		}
		reviewedAt := review.ReviewedAt
		if intent := detail.State.Plan.MergeCommitIntent; intent != nil && (strings.TrimSpace(review.Head) != intent.SourceHead || !review.IsApproved()) {
			detail.State.Plan.MergeCommitIntent = nil
		}
		detail.State.Plan.Review = &review
		if sliceWorkSettled(detail) && !PlanIsMerged(detail.Events) {
			detail.State.Status = StatusInReview
		}
		detail.State.UpdatedAt = reviewedAt
		detail.State.Plan.Timing.LastActivityAt = &reviewedAt
		event := Event{Type: EventTypePlanReviewed, Timestamp: reviewedAt, PlanID: detail.State.Plan.ID, Agent: agent, Review: &review, Message: "Plan review failed"}
		return []Event{event}, nil
	})
}

// RecordReviewCompleted stamps a completed review onto state and appends a
// plan_reviewed event. The caller sets review.ReviewedAt before calling.
func (r *PlanRecord) RecordReviewCompleted(review PlanReview, agent string) error {
	if review.Status != ReviewStatusCompleted || review.Verdict != ReviewVerdictApprove {
		review.CommitMessage = nil
	}
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	return r.applyStateEvent(store, func(detail *PlanDetail) ([]Event, error) {
		if err := automaticSliceCompletionError(detail); err != nil {
			return nil, fmt.Errorf("record plan review: %w", err)
		}
		reviewedAt := review.ReviewedAt
		if intent := detail.State.Plan.MergeCommitIntent; intent != nil && (strings.TrimSpace(review.Head) != intent.SourceHead || !review.IsApproved()) {
			detail.State.Plan.MergeCommitIntent = nil
		}
		detail.State.Plan.Review = &review
		if sliceWorkSettled(detail) && !PlanIsMerged(detail.Events) {
			detail.State.Status = reviewProjectedStatus(&review)
		}
		detail.State.UpdatedAt = reviewedAt
		detail.State.Plan.Timing.LastActivityAt = &reviewedAt
		event := Event{Type: EventTypePlanReviewed, Timestamp: reviewedAt, PlanID: detail.State.Plan.ID, Agent: agent, Review: &review, Message: "Plan reviewed"}
		return []Event{event}, nil
	})
}

// RecordMerged marks the plan fully completed after a verified merge and appends
// the plan_merged event. The plan status intentionally reaches completed only
// here; slice completion and review use pre-merge statuses.
func (r *PlanRecord) RecordMerged(branch string, mergedDefaultSHA string, mergedAt time.Time) error {
	store, err := r.storeOrDefault()
	if err != nil {
		return err
	}
	branch = strings.TrimSpace(branch)
	mergedDefaultSHA = strings.TrimSpace(mergedDefaultSHA)
	mergedAt = mergedAt.UTC()
	return r.applyStateEvent(store, func(detail *PlanDetail) ([]Event, error) {
		for _, event := range slices.Backward(detail.Events) {
			if event.Type == EventTypePlanReopened {
				break
			}
			if event.Type != EventTypePlanMerged {
				continue
			}
			if strings.TrimSpace(event.Branch) == branch && strings.TrimSpace(event.MergedDefaultSHA) == mergedDefaultSHA {
				return nil, nil
			}
			return nil, fmt.Errorf("plan merge is already recorded with different evidence")
		}
		detail.State.Status = StatusCompleted
		detail.State.Plan.MergeCommitIntent = nil
		detail.State.UpdatedAt = mergedAt
		detail.State.Plan.Timing.LastActivityAt = &mergedAt
		// CompletedAt is stamped when the final slice completes; the merge may
		// happen days later and must not overwrite it, or elapsed durations
		// inflate by the review/merge gap. Legacy plans without the stamp record
		// the merge instant instead.
		if detail.State.Plan.Timing.CompletedAt == nil {
			detail.State.Plan.Timing.CompletedAt = &mergedAt
		}
		event := Event{Type: EventTypePlanMerged, Timestamp: mergedAt, PlanID: detail.State.Plan.ID, Branch: branch, MergedDefaultSHA: mergedDefaultSHA, Message: "Plan merged into default branch"}
		return []Event{event}, nil
	})
}

func (r *PlanRecord) apply(mutate artifactMutationFunc) error {
	return r.applyWithRecoveredMatch(mutate, nil)
}

func (r *PlanRecord) applyWithRecoveredMatch(mutate artifactMutationFunc, recoveredMatch recoveredArtifactMutationMatch) error {
	if r == nil {
		return fmt.Errorf("plan record is nil")
	}
	if r.detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	if r.dir == "" {
		return fmt.Errorf("plan directory is required")
	}
	store := r.store
	if store == nil {
		store = fileArtifactStore{}
	}
	err := applyArtifactMutationWithRecoveredMatch(store, r.dir, r.detail, mutate, true, nil, nil, recoveredMatch)
	if err == nil {
		r.advanceBaseline()
	}
	return err
}

func (r *PlanRecord) applyStateEvent(store artifactMutationStore, mutate func(*PlanDetail) ([]Event, error)) error {
	err := applyStateEventMutationWithRefresh(store, r.dir, r.detail, true, mutate)
	if err == nil {
		r.advanceBaseline()
	}
	return err
}

func (r *PlanRecord) applyStateUpdate(store artifactMutationStore, baseline, intended State) error {
	recoveredBaseline, err := applyStateArtifactUpdate(store, r.dir, r.detail, baseline, intended)
	if err == nil {
		r.advanceBaseline()
	} else if recoveredBaseline != nil {
		r.baseline = clonePlanDetail(recoveredBaseline)
		stateBaseline := cloneState(recoveredBaseline.State)
		slicesBaseline := cloneSlicesFile(recoveredBaseline.Slices)
		r.detail.loadedStateBaseline = &stateBaseline
		r.detail.loadedSlicesBaseline = &slicesBaseline
	}
	return err
}

func (r *PlanRecord) applySlicesUpdate(store artifactMutationStore, mutate func(*PlanDetail) error) error {
	err := applySlicesArtifactUpdate(store, r.dir, r.detail, mutate)
	if err == nil {
		r.advanceBaseline()
	}
	return err
}

func (r *PlanRecord) advanceBaseline() {
	stateBaseline := cloneState(r.detail.State)
	slicesBaseline := cloneSlicesFile(r.detail.Slices)
	r.detail.loadedStateBaseline = &stateBaseline
	r.detail.loadedSlicesBaseline = &slicesBaseline
	r.baseline = clonePlanDetail(r.detail)
}

func (r *PlanRecord) stateBaseline() State {
	if r.baseline != nil {
		return r.baseline.State
	}
	return r.detail.State
}

func samePlanDir(left string, right string) bool {
	return cleanPlanDir(left) == cleanPlanDir(right)
}

func cleanPlanDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Clean(dir)
}
