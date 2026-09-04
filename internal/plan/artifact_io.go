package plan

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

// planFiles is the raw artifact bundle loaded from a plan directory before deriving
// lifecycle state, summaries, or validation warnings for callers.
type planFiles struct {
	dir             string
	state           State
	slices          SlicesFile
	events          []Event
	planningSession PlanningSessionArtifacts
	planningBrief   PlanningBriefArtifact
	review          PlanReviewArtifact
	planNarrative   PlanNarrativeArtifact
	warnings        []string
}

// loadPlanFiles owns the artifact schema boundary: required JSON files are fatal,
// while optional sidecars contribute warnings so older plans remain readable.
// Journal-capable plans are read under the persistence lock. Legacy plans that
// have neither a journal nor a lock remain inspectable without creating files.
func loadPlanFiles(dir string) (planFiles, error) {
	return withMutationPersistenceReadBoundary(dir, func(recover bool) (planFiles, error) {
		if recover {
			return loadPlanFilesLocked(dir)
		}
		return readPlanFiles(dir)
	})
}

// loadPlanFilesLocked requires mutationPersistenceLock for dir.
func loadPlanFilesLocked(dir string) (planFiles, error) {
	if _, err := settlePendingMutationLocked(fileMutationJournalIO{}, dir, filepath.Base(filepath.Clean(dir))); err != nil {
		return planFiles{}, fmt.Errorf("recover plan mutation: %w", err)
	}
	return readPlanFiles(dir)
}

func readPlanFiles(dir string) (planFiles, error) {
	statePath := filepath.Join(dir, "state.json")
	slicesPath := filepath.Join(dir, "slices.json")
	eventsPath := filepath.Join(dir, "events.jsonl")

	var state State
	if err := readJSON(statePath, &state); err != nil {
		return planFiles{}, fmt.Errorf("read state.json: %w", err)
	}

	var slices SlicesFile
	if err := readJSON(slicesPath, &slices); err != nil {
		return planFiles{}, fmt.Errorf("read slices.json: %w", err)
	}

	events, warnings, err := readEvents(eventsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return planFiles{}, fmt.Errorf("read events.jsonl: %w", err)
	}
	planningSession, artifactWarnings := readPlanningSessionArtifacts(dir)
	warnings = append(warnings, artifactWarnings...)
	planningBrief, briefWarnings := readPlanningBriefArtifact(dir)
	warnings = append(warnings, briefWarnings...)
	review, reviewWarnings := readReviewArtifact(dir)
	warnings = append(warnings, reviewWarnings...)
	planNarrative, narrativeWarnings := readPlanNarrativeArtifact(dir)
	warnings = append(warnings, narrativeWarnings...)
	warnings = append(warnings, validateRuntimePrerequisiteCycle(state.Plan.ID, state.Plan.RuntimePrerequisites, runtimePrerequisiteResolver(filepath.Dir(filepath.Clean(dir))))...)

	return planFiles{dir: dir, state: state, slices: slices, events: events, planningSession: planningSession, planningBrief: planningBrief, review: review, planNarrative: planNarrative, warnings: warnings}, nil
}

func runtimePrerequisiteResolver(plansDir string) func(string) ([]RuntimePrerequisite, bool) {
	return func(planID string) ([]RuntimePrerequisite, bool) {
		if planID == "" || len(planID) > maxRuntimePrerequisitePlanID || planID == "." || planID == ".." || strings.ContainsAny(planID, `/\\`) {
			return nil, false
		}
		var state State
		if err := readJSON(filepath.Join(plansDir, planID, "state.json"), &state); err != nil || state.Plan.ID != planID {
			return nil, false
		}
		return state.Plan.RuntimePrerequisites, true
	}
}

// ReadState reads the mutable state.json artifact from a plan directory after
// settling any durable mutation intent. Legacy plans without recovery metadata
// remain readable without creating a persistence lock file.
func ReadState(planDir string) (State, error) {
	return withMutationPersistenceReadBoundary(planDir, func(recover bool) (State, error) {
		if recover {
			if _, err := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, filepath.Base(filepath.Clean(planDir))); err != nil {
				return State{}, fmt.Errorf("recover plan mutation: %w", err)
			}
		}
		var state State
		if err := readJSON(filepath.Join(planDir, "state.json"), &state); err != nil {
			return state, fmt.Errorf("read state.json: %w", err)
		}
		return state, nil
	})
}

// writeState writes the mutable state.json artifact while coordinating with
// journal recovery. PlanRecord callers use the rebasing helpers below; this
// lower-level writer is retained for artifact creation and test support.
func writeState(planDir string, state State) error {
	_, err := withMutationPersistenceLock(planDir, func() (struct{}, error) {
		if _, settleErr := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, state.Plan.ID); settleErr != nil {
			return struct{}{}, fmt.Errorf("recover plan mutation: %w", settleErr)
		}
		if writeErr := writeJSON(filepath.Join(planDir, "state.json"), state); writeErr != nil {
			return struct{}{}, fmt.Errorf("write state.json: %w", writeErr)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return fmt.Errorf("write state.json: %w", err)
	}
	return nil
}

// writeSlices writes the mutable slices.json artifact while coordinating with
// journal recovery. PlanRecord callers use the rebasing helpers below.
func writeSlices(planDir string, slices SlicesFile) error {
	_, err := withMutationPersistenceLock(planDir, func() (struct{}, error) {
		if _, settleErr := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, slices.PlanID); settleErr != nil {
			return struct{}{}, fmt.Errorf("recover plan mutation: %w", settleErr)
		}
		if writeErr := writeJSON(filepath.Join(planDir, "slices.json"), slices); writeErr != nil {
			return struct{}{}, fmt.Errorf("write slices.json: %w", writeErr)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return fmt.Errorf("write slices.json: %w", err)
	}
	return nil
}

// ArtifactChangeSet binds explicit clear-or-replace persistence intent to one
// plan detail. Its typed methods mutate the detail and retain the matching
// intent until payload preparation; callers cannot supply JSON paths.
type ArtifactChangeSet struct {
	detail *PlanDetail

	clearWorkspaceDependencyFailure     bool
	clearWorkspaceDependencyFingerprint bool
	clearWorkspaceRebaseIntent          bool
	clearPlanCurrentSlice               bool
	clearPlanFinalizationFailure        bool
	clearSingleMergeResolution          bool
	planFinalizationFailure             *FinalizationFailure
	finalVerification                   *FinalVerification
	planReview                          planReviewChange
	clearSliceBlockerNotes              map[string]struct{}
	clearSliceExecutionBoundaries       map[string]struct{}
}

type planReviewChange struct {
	kind   planReviewChangeKind
	review PlanReview
}

type planReviewChangeKind uint8

const (
	planReviewUnchanged planReviewChangeKind = iota
	planReviewReplaced
	planReviewCleared
)

// NewArtifactChangeSet creates an empty, preserve-only change set for detail.
func NewArtifactChangeSet(detail *PlanDetail) *ArtifactChangeSet {
	return &ArtifactChangeSet{detail: detail}
}

// ClearWorkspaceDependencyFailure explicitly clears the persisted dependency
// preparation failure while preserving unrelated workspace fields.
func (c *ArtifactChangeSet) ClearWorkspaceDependencyFailure() {
	if c == nil {
		return
	}
	c.clearWorkspaceDependencyFailure = true
	if c.detail == nil {
		return
	}
	if c.detail.State.Workspace == nil {
		c.detail.State.Workspace = &Workspace{}
	}
	c.detail.State.Workspace.DependencyFailure = ""
}

// ClearWorkspaceDependencyFingerprint explicitly clears the persisted
// dependency fingerprint while preserving unrelated workspace fields.
func (c *ArtifactChangeSet) ClearWorkspaceDependencyFingerprint() {
	if c == nil {
		return
	}
	c.clearWorkspaceDependencyFingerprint = true
	if c.detail == nil {
		return
	}
	if c.detail.State.Workspace == nil {
		c.detail.State.Workspace = &Workspace{}
	}
	c.detail.State.Workspace.DependencyFingerprint = ""
}

// ClearWorkspaceRebaseIntent explicitly clears durable rebase recovery evidence
// while preserving unrelated workspace fields.
func (c *ArtifactChangeSet) ClearWorkspaceRebaseIntent() {
	if c == nil {
		return
	}
	c.clearWorkspaceRebaseIntent = true
	if c.detail != nil && c.detail.State.Workspace != nil {
		c.detail.State.Workspace.RebaseIntent = nil
	}
}

// ClearSliceBlockerNote explicitly clears one slice's persisted blocker note.
func (c *ArtifactChangeSet) ClearSliceBlockerNote(sliceID string) error {
	if c == nil || c.detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(c.detail, sliceID)
	if slice == nil {
		return classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if c.clearSliceBlockerNotes == nil {
		c.clearSliceBlockerNotes = make(map[string]struct{})
	}
	c.clearSliceBlockerNotes[sliceID] = struct{}{}
	slice.BlockerNote = ""
	return nil
}

// ClearPlanCurrentSlice explicitly clears the persisted current slice.
// ClearSliceExecutionBoundary explicitly supersedes one slice's execution
// root and immutable start while retaining the prior values in restart evidence.
func (c *ArtifactChangeSet) ClearSliceExecutionBoundary(sliceID string) error {
	if c == nil || c.detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(c.detail, sliceID)
	if slice == nil {
		return classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if c.clearSliceExecutionBoundaries == nil {
		c.clearSliceExecutionBoundaries = make(map[string]struct{})
	}
	c.clearSliceExecutionBoundaries[sliceID] = struct{}{}
	slice.ExecutionRoot = ""
	slice.ExecutionStart = nil
	return nil
}

func (c *ArtifactChangeSet) ClearPlanCurrentSlice() {
	if c == nil {
		return
	}
	c.clearPlanCurrentSlice = true
	if c.detail != nil {
		c.detail.State.Plan.CurrentSlice = nil
	}
}

// ClearPlanFinalizationFailure explicitly supersedes the current bounded
// failure evidence while preserving its append-only lifecycle event.
func (c *ArtifactChangeSet) ClearPlanFinalizationFailure() {
	if c == nil {
		return
	}
	c.clearPlanFinalizationFailure = true
	c.planFinalizationFailure = nil
	if c.detail != nil {
		c.detail.State.Plan.FinalizationFailure = nil
	}
}

// ClearSingleMergeResolution explicitly clears only provisional resolution
// evidence while retaining its parent single-merge commit intent.
func (c *ArtifactChangeSet) ClearSingleMergeResolution() {
	if c == nil {
		return
	}
	c.clearSingleMergeResolution = true
	if c.detail != nil && c.detail.State.Plan.MergeCommitIntent != nil {
		c.detail.State.Plan.MergeCommitIntent.Resolution = nil
	}
}

// ReplacePlanFinalizationFailure replaces every persisted failure field so a
// transition between phase-specific evidence cannot retain omitted JSON keys.
func (c *ArtifactChangeSet) ReplacePlanFinalizationFailure(failure FinalizationFailure) error {
	if c == nil || c.detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	if err := failure.Validate(); err != nil {
		return err
	}
	c.clearPlanFinalizationFailure = false
	c.planFinalizationFailure = cloneFinalizationFailure(&failure)
	c.detail.State.Plan.FinalizationFailure = cloneFinalizationFailure(&failure)
	return nil
}

// replaceFinalVerification binds a complete final-verification replacement to
// the evidence mutation so omitted classifications cannot survive a fresh write.
func (c *ArtifactChangeSet) replaceFinalVerification(verification FinalVerification) error {
	if c == nil || c.detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	c.finalVerification = cloneFinalVerification(&verification)
	c.detail.State.Plan.FinalVerification = cloneFinalVerification(&verification)
	return nil
}

// ReplacePlanReview replaces every known persisted review field. Empty findings
// are normalized to [] so replacement cannot retain an older findings array.
func (c *ArtifactChangeSet) ReplacePlanReview(review PlanReview) error {
	if c == nil || c.detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	review = normalizePlanReviewReplacement(review)
	c.planReview = planReviewChange{kind: planReviewReplaced, review: review}
	c.detail.State.Plan.Review = clonePlanReview(&review)
	return nil
}

func normalizePlanReviewReplacement(review PlanReview) PlanReview {
	review.Findings = append([]ReviewFinding{}, review.Findings...)
	if review.CommitMessage != nil {
		message := *review.CommitMessage
		review.CommitMessage = &message
	}
	return review
}

// ClearPlanReview explicitly replaces the whole persisted review block with
// null.
func (c *ArtifactChangeSet) ClearPlanReview() {
	if c == nil {
		return
	}
	c.planReview = planReviewChange{kind: planReviewCleared}
	if c.detail != nil {
		c.detail.State.Plan.Review = nil
	}
}

type artifactJSONKind uint8

const (
	artifactJSONNone artifactJSONKind = iota
	artifactJSONState
	artifactJSONSlices
)

type artifactJSONChanges struct {
	kind    artifactJSONKind
	changes *ArtifactChangeSet
}

func stateJSONChanges(changes *ArtifactChangeSet) artifactJSONChanges {
	return artifactJSONChanges{kind: artifactJSONState, changes: changes}
}

func slicesJSONChanges(changes *ArtifactChangeSet) artifactJSONChanges {
	return artifactJSONChanges{kind: artifactJSONSlices, changes: changes}
}

func (c *ArtifactChangeSet) applyState(state *State) {
	if c == nil || state == nil {
		return
	}
	if c.clearWorkspaceDependencyFailure || c.clearWorkspaceDependencyFingerprint || c.clearWorkspaceRebaseIntent {
		if state.Workspace == nil {
			state.Workspace = &Workspace{}
		}
		if c.clearWorkspaceDependencyFailure {
			state.Workspace.DependencyFailure = ""
		}
		if c.clearWorkspaceDependencyFingerprint {
			state.Workspace.DependencyFingerprint = ""
		}
		if c.clearWorkspaceRebaseIntent {
			state.Workspace.RebaseIntent = nil
		}
	}
	if c.clearPlanCurrentSlice {
		state.Plan.CurrentSlice = nil
	}
	if c.clearSingleMergeResolution && state.Plan.MergeCommitIntent != nil {
		state.Plan.MergeCommitIntent.Resolution = nil
	}
	if c.clearPlanFinalizationFailure {
		state.Plan.FinalizationFailure = nil
	} else if c.planFinalizationFailure != nil {
		state.Plan.FinalizationFailure = cloneFinalizationFailure(c.planFinalizationFailure)
	}
	if c.finalVerification != nil {
		state.Plan.FinalVerification = cloneFinalVerification(c.finalVerification)
	}
	switch c.planReview.kind {
	case planReviewReplaced:
		state.Plan.Review = clonePlanReview(&c.planReview.review)
	case planReviewCleared:
		state.Plan.Review = nil
	}
}

type artifactMutationFunc func(*PlanDetail) (lifecycleMutation, error)

func startSliceMutation(sliceID string, executionRoot string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(_ *ArtifactChangeSet) ([]Event, error) {
			if executionRoot != "" {
				if err := markSliceExecutionRoot(detail, sliceID, executionRoot); err != nil {
					return nil, err
				}
			}
			event, appendEvent, err := MarkSliceStarted(detail, sliceID, now)
			if err != nil {
				return nil, err
			}
			if !appendEvent {
				return nil, nil
			}
			return []Event{event}, nil
		})
	}
}

func startSliceWithRunBoundaryMutation(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary *SliceExecutionStart, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		if err := MarkRunStartMetadata(detail, commitPolicy, startingDirtyPaths); err != nil {
			return lifecycleMutation{}, err
		}
		if boundary != nil {
			if err := MarkSliceExecutionStart(detail, sliceID, *boundary); err != nil {
				return lifecycleMutation{}, err
			}
		}
		return startSliceMutation(sliceID, executionRoot, now)(detail)
	}
}

func repairMissingSliceStartedEventMutation(sliceID string, startedAt time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		if detail == nil {
			return lifecycleMutation{}, fmt.Errorf("plan detail is nil")
		}
		slice := findSlice(detail, sliceID)
		if slice == nil {
			return lifecycleMutation{}, classify(ErrNotFound, "slice %s not found", sliceID)
		}
		if slice.Timing.StartedAt == nil {
			return lifecycleMutation{}, fmt.Errorf("slice %s has no recorded start time", sliceID)
		}
		if !slice.Timing.StartedAt.Equal(startedAt) {
			return lifecycleMutation{}, fmt.Errorf("slice %s recorded start time changed", sliceID)
		}
		if hasSliceStartedEvent(detail.Events, sliceID) {
			return lifecycleMutation{State: detail.State, Slices: detail.Slices}, nil
		}
		event := Event{Type: EventTypeSliceStarted, Timestamp: startedAt.UTC(), PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Work started on slice"}
		return lifecycleMutation{State: detail.State, Slices: detail.Slices, Events: []Event{event}}, nil
	}
}

func completeSliceMutation(sliceID string, notes string, verificationResults []VerificationRun, now time.Time) artifactMutationFunc {
	return completeSliceWithOutcomeMutation(sliceID, notes, verificationResults, nil, now)
}

func completeSliceWithOutcomeMutation(sliceID string, notes string, verificationResults []VerificationRun, outcome *SliceCompletionOutcome, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(changes *ArtifactChangeSet) ([]Event, error) {
			event, appendEvent, err := markSliceCompletedWithOutcome(detail, changes, sliceID, notes, verificationResults, outcome, now)
			if err != nil {
				return nil, err
			}
			if !appendEvent {
				return nil, nil
			}
			return []Event{event}, nil
		})
	}
}

func approveSliceMutation(sliceID string, approvedBy string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(_ *ArtifactChangeSet) ([]Event, error) {
			event, appendEvent, err := MarkSliceApproved(detail, sliceID, approvedBy, now)
			if err != nil {
				return nil, err
			}
			if !appendEvent {
				return nil, nil
			}
			return []Event{event}, nil
		})
	}
}

func blockSliceMutation(sliceID string, reason string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(_ *ArtifactChangeSet) ([]Event, error) {
			event, appendEvent, err := MarkSliceBlocked(detail, sliceID, reason, now)
			if err != nil {
				return nil, err
			}
			if !appendEvent {
				return nil, nil
			}
			return []Event{event}, nil
		})
	}
}

func blockSliceForBudgetMutation(sliceID string, reason string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(_ *ArtifactChangeSet) ([]Event, error) {
			event, appendEvent, err := MarkSliceBudgetBlocked(detail, sliceID, reason, now)
			if err != nil {
				return nil, err
			}
			if !appendEvent {
				return nil, nil
			}
			return []Event{event}, nil
		})
	}
}

func continueBlockedMutation(now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(changes *ArtifactChangeSet) ([]Event, error) {
			return nil, markBlockedContinued(detail, changes, now)
		})
	}
}

func blockedSliceRestartMutation(request BlockedSliceRestartRequest) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(changes *ArtifactChangeSet) ([]Event, error) {
			event, err := markBlockedSliceRestarted(detail, changes, request)
			if err != nil {
				return nil, err
			}
			return []Event{event}, nil
		})
	}
}

func removeSliceMutation(sliceID string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		if err := RequireNotAbandoned(detail); err != nil {
			return lifecycleMutation{}, err
		}
		expected := Event{Type: EventTypeSliceRemoved, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Pending slice removed by plan edit"}
		if semanticEventsWereRecorded(detail.Events, []Event{expected}) && findSlice(detail, sliceID) == nil && !slices.Contains(detail.State.Plan.PendingSlices, sliceID) {
			return unchangedLifecycleMutation(detail), nil
		}
		return applyLifecycleMutation(detail, func(changes *ArtifactChangeSet) ([]Event, error) {
			event, err := markSliceRemoved(detail, changes, sliceID, now)
			if err != nil {
				return nil, err
			}
			return []Event{event}, nil
		})
	}
}

func skipSliceMutation(sliceID string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		if err := RequireNotAbandoned(detail); err != nil {
			return lifecycleMutation{}, err
		}
		expected := Event{Type: EventTypeSliceSkipped, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Pending slice skipped by plan edit"}
		slice := findSlice(detail, sliceID)
		if semanticEventsWereRecorded(detail.Events, []Event{expected}) && slice != nil && slice.Status == StatusSkipped && !slices.Contains(detail.State.Plan.PendingSlices, sliceID) {
			return unchangedLifecycleMutation(detail), nil
		}
		return applyLifecycleMutation(detail, func(changes *ArtifactChangeSet) ([]Event, error) {
			event, err := markSliceSkipped(detail, changes, sliceID, now)
			if err != nil {
				return nil, err
			}
			return []Event{event}, nil
		})
	}
}

func reorderPendingSlicesMutation(pendingOrder []string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		if err := RequireNotAbandoned(detail); err != nil {
			return lifecycleMutation{}, err
		}
		expected := Event{Type: EventTypeSlicesReordered, Timestamp: now, PlanID: detail.State.Plan.ID, Message: "Pending slices reordered by plan edit"}
		if semanticEventsWereRecorded(detail.Events, []Event{expected}) && slices.Equal(detail.State.Plan.PendingSlices, pendingOrder) {
			return unchangedLifecycleMutation(detail), nil
		}
		return applyLifecycleMutation(detail, func(changes *ArtifactChangeSet) ([]Event, error) {
			event, err := markPendingSlicesReordered(detail, changes, pendingOrder, now)
			if err != nil {
				return nil, err
			}
			return []Event{event}, nil
		})
	}
}

func unchangedLifecycleMutation(detail *PlanDetail) lifecycleMutation {
	return lifecycleMutation{State: detail.State, Slices: detail.Slices}
}

func blockedSliceRestartWasRecovered(request BlockedSliceRestartRequest) recoveredArtifactMutationMatch {
	return func(stale, settled *PlanDetail) bool {
		if stale == nil || settled == nil {
			return false
		}
		expected := clonePlanDetail(stale)
		changes := NewArtifactChangeSet(expected)
		event, err := markBlockedSliceRestarted(expected, changes, request)
		if err != nil {
			return false
		}
		return reflect.DeepEqual(settled.State, expected.State) && reflect.DeepEqual(settled.Slices, expected.Slices) && semanticEventsWereRecorded(settled.Events, []Event{event})
	}
}

func blockedContinuationWasRecovered(stale, settled *PlanDetail) bool {
	if stale == nil || settled == nil {
		return false
	}
	expected := clonePlanDetail(stale)
	if err := markBlockedContinued(expected, NewArtifactChangeSet(expected), settled.State.UpdatedAt); err != nil {
		return false
	}
	return reflect.DeepEqual(settled.State, expected.State) && reflect.DeepEqual(settled.Slices, expected.Slices)
}

type recoveredArtifactMutationMatch func(stale, settled *PlanDetail) bool

type mutationDetailRefresh struct {
	detail    *PlanDetail
	refreshed bool
	recovered bool
}

func artifactMutationWorkingDetailLocked(store artifactMutationStore, planDir string, detail *PlanDetail, force, publishRecovered bool) (*PlanDetail, bool, error) {
	refresh, err := store.refreshMutationDetailLocked(planDir, detail.State.Plan.ID, force)
	if err != nil {
		return nil, false, err
	}
	if refresh.recovered && publishRecovered {
		*detail = *clonePlanDetail(refresh.detail)
	}
	if refresh.refreshed {
		return clonePlanDetail(refresh.detail), refresh.recovered, nil
	}
	return clonePlanDetail(detail), false, nil
}

// applyArtifactMutation prepares the exact merge-preserving target bytes and
// settles state, slices, and lifecycle events through one durable journal. Any
// earlier intent is settled and published before the requested mutation is
// evaluated; the requested values are published only after their settlement.
func applyArtifactMutation(store artifactMutationStore, planDir string, detail *PlanDetail, mutate artifactMutationFunc) error {
	return applyArtifactMutationWithRefresh(store, planDir, detail, mutate, true, nil, nil)
}

// applyArtifactMutationPreservingDetail is for callers that intentionally
// persist edits already made to detail. It refreshes the latest settled
// artifacts, then rebases those edits from the record baseline over them.
func applyArtifactMutationPreservingDetail(store artifactMutationStore, planDir string, detail, baseline *PlanDetail, mutate artifactMutationFunc) error {
	intended := clonePlanDetail(detail)
	return applyArtifactMutationWithRefresh(store, planDir, detail, mutate, true, baseline, intended)
}

func applyArtifactMutationWithRefresh(store artifactMutationStore, planDir string, detail *PlanDetail, mutate artifactMutationFunc, forceRefresh bool, baseline, intended *PlanDetail) error {
	return applyArtifactMutationWithRecoveredMatch(store, planDir, detail, mutate, forceRefresh, baseline, intended, nil)
}

func applyArtifactMutationWithRecoveredMatch(store artifactMutationStore, planDir string, detail *PlanDetail, mutate artifactMutationFunc, forceRefresh bool, baseline, intended *PlanDetail, recoveredMatch recoveredArtifactMutationMatch) error {
	return store.withMutationLock(planDir, func() error {
		return applyArtifactMutationLocked(store, planDir, detail, mutate, forceRefresh, baseline, intended, recoveredMatch)
	})
}

// applyArtifactMutationLocked requires the store's mutation lock for planDir.
func applyArtifactMutationLocked(store artifactMutationStore, planDir string, detail *PlanDetail, mutate artifactMutationFunc, forceRefresh bool, baseline, intended *PlanDetail, recoveredMatch recoveredArtifactMutationMatch) error {
	stale := clonePlanDetail(detail)
	clone, recovered, err := artifactMutationWorkingDetailLocked(store, planDir, detail, forceRefresh, baseline == nil && intended == nil)
	if err != nil {
		return err
	}
	if baseline != nil && intended != nil {
		if err := validateStateChangeDeclarations(baseline.State, intended.State, nil); err != nil {
			return err
		}
		if err := validateSlicesChangeDeclarations(baseline.Slices, intended.Slices, nil); err != nil {
			return err
		}
		if conflictingArtifactStructureChanges(baseline, intended, clone) {
			return fmt.Errorf("persist artifacts: concurrent structural change; reload the plan and retry")
		}
		clone.State = rebaseArtifact(baseline.State, intended.State, clone.State)
		clone.Slices = rebaseArtifact(baseline.Slices, intended.Slices, clone.Slices)
	} else if recovered {
		requested, requestErr := mutate(clonePlanDetail(stale))
		if requestErr == nil && (artifactMutationWasRecovered(clone, requested) || recoveredMatch != nil && recoveredMatch(stale, clone)) {
			*detail = *clone
			return nil
		}
	}
	beforeMutation := clonePlanDetail(clone)
	mutation, err := mutate(clone)
	if err != nil {
		return err
	}
	if err := validateStateChangeDeclarations(beforeMutation.State, mutation.State, mutation.Changes); err != nil {
		return err
	}
	if err := validateSlicesChangeDeclarations(beforeMutation.Slices, mutation.Slices, mutation.Changes); err != nil {
		return err
	}
	if baseline == nil && intended == nil && len(mutation.Events) == 0 && reflect.DeepEqual(mutation.State, beforeMutation.State) && reflect.DeepEqual(mutation.Slices, beforeMutation.Slices) {
		*detail = *clone
		return nil
	}
	if mutation.Slices.PlanID == "" {
		mutation.Slices.PlanID = mutation.State.Plan.ID
	}

	mutationID, err := newArtifactMutationID()
	if err != nil {
		return err
	}
	statePayload, err := prepareJSON(filepath.Join(planDir, "state.json"), mutation.State, stateJSONChanges(mutation.Changes))
	if err != nil {
		return fmt.Errorf("prepare state.json: %w", err)
	}
	slicesPayload, err := prepareJSON(filepath.Join(planDir, "slices.json"), mutation.Slices, slicesJSONChanges(mutation.Changes))
	if err != nil {
		return fmt.Errorf("prepare slices.json: %w", err)
	}
	events := make([]mutationJournalEvent, 0, len(mutation.Events))
	for i := range mutation.Events {
		mutation.Events[i].MutationID = mutationID
		payload, marshalErr := json.Marshal(mutation.Events[i])
		if marshalErr != nil {
			return fmt.Errorf("prepare events.jsonl: %w", marshalErr)
		}
		events = append(events, newMutationJournalEvent(payload))
	}
	journal := mutationJournal{
		Schema:     mutationJournalSchema,
		MutationID: mutationID,
		PlanID:     mutation.State.Plan.ID,
		CreatedAt:  time.Now().UTC(),
		State:      newMutationJournalPayload(statePayload),
		Slices:     newMutationJournalPayload(slicesPayload),
		Events:     events,
	}
	if err := store.settleMutationLocked(planDir, journal); err != nil {
		return err
	}

	clone.State = mutation.State
	clone.Slices = mutation.Slices
	clone.Events = append(clone.Events, mutation.Events...)
	*detail = *clone
	return nil
}

// applyStateEventMutation settles a state update and its coupled events without
// rewriting slices.json. Earlier intent is recovered and published before the
// requested mutation is evaluated, and the caller's detail changes only after
// settlement.
func applyStateEventMutationWithRefresh(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, mutate func(*PlanDetail, *ArtifactChangeSet) ([]Event, error)) error {
	return applyStateEventMutationWithReview(store, planDir, detail, forceRefresh, nil, mutate)
}

// applyStateEventMutationWithReview includes review.md in the same refreshed,
// journaled mutation as its state and event metadata.
func applyStateEventMutationWithReview(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, reviewContent *string, mutate func(*PlanDetail, *ArtifactChangeSet) ([]Event, error)) error {
	return store.withMutationLock(planDir, func() error {
		return applyStateEventMutationLocked(store, planDir, detail, forceRefresh, reviewContent, mutate)
	})
}

// applyStateEventMutationLocked requires the store's mutation lock for planDir.
func applyStateEventMutationLocked(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, reviewContent *string, mutate func(*PlanDetail, *ArtifactChangeSet) ([]Event, error)) error {
	clone, _, err := artifactMutationWorkingDetailLocked(store, planDir, detail, forceRefresh, true)
	if err != nil {
		return err
	}
	baseline := clonePlanDetail(clone)
	changes := NewArtifactChangeSet(clone)
	eventsToAppend, err := mutate(clone, changes)
	if err != nil {
		return err
	}
	if err := validateStateChangeDeclarations(baseline.State, clone.State, changes); err != nil {
		return err
	}
	if len(eventsToAppend) > 0 && eventsWereRecorded(baseline.Events, eventsToAppend) {
		*detail = *baseline
		return nil
	}
	if len(eventsToAppend) == 0 && reflect.DeepEqual(clone.State, baseline.State) {
		*detail = *clone
		return nil
	}

	mutationID, err := newArtifactMutationID()
	if err != nil {
		return err
	}
	statePayload, err := prepareJSON(filepath.Join(planDir, "state.json"), clone.State, stateJSONChanges(changes))
	if err != nil {
		return fmt.Errorf("prepare state.json: %w", err)
	}
	events := make([]mutationJournalEvent, 0, len(eventsToAppend))
	for i := range eventsToAppend {
		eventsToAppend[i].MutationID = mutationID
		payload, marshalErr := json.Marshal(eventsToAppend[i])
		if marshalErr != nil {
			return fmt.Errorf("prepare events.jsonl: %w", marshalErr)
		}
		events = append(events, newMutationJournalEvent(payload))
	}
	journal := mutationJournal{
		Schema:     mutationJournalSchema,
		MutationID: mutationID,
		PlanID:     clone.State.Plan.ID,
		CreatedAt:  time.Now().UTC(),
		State:      newMutationJournalPayload(statePayload),
		Events:     events,
	}
	if reviewContent != nil {
		journal.Review = newMutationJournalPayload([]byte(*reviewContent))
	}
	if err := store.settleMutationLocked(planDir, journal); err != nil {
		return err
	}

	clone.Events = append(clone.Events, eventsToAppend...)
	*detail = *clone
	return nil
}

// applyStateArtifactUpdate and applySlicesArtifactUpdate persist an intended
// single-target edit without letting a stale PlanRecord erase fields installed
// by a journal that settled first. State updates rebase fields changed since
// the record baseline; slices updates re-evaluate their semantic mutation on
// the latest settled detail.
func applyStateArtifactUpdate(store artifactMutationStore, planDir string, detail *PlanDetail, baseline, intended State, changes *ArtifactChangeSet) (*PlanDetail, error) {
	if err := validateStateChangeDeclarations(baseline, intended, changes); err != nil {
		return nil, err
	}
	// Incomplete in-memory fixtures and legacy callers may not identify a plan.
	// Such a payload cannot form a valid journal, but the low-level writer still
	// takes the persistence lock and settles any valid pending intent first.
	if intended.Plan.ID == "" {
		return nil, store.writeState(planDir, intended)
	}
	var recoveredBaseline *PlanDetail
	err := store.withMutationLock(planDir, func() error {
		working, recovered, err := artifactMutationWorkingDetailLocked(store, planDir, detail, true, true)
		if err != nil {
			return err
		}
		if recovered {
			recoveredBaseline = clonePlanDetail(working)
		}
		state := rebaseArtifact(baseline, intended, working.State)
		changes.applyState(&state)
		publishPendingRecovery := func() {
			if recovered {
				working.State = state
				*detail = *working
			}
		}
		payload, err := prepareJSON(filepath.Join(planDir, "state.json"), state, stateJSONChanges(changes))
		if err != nil {
			publishPendingRecovery()
			return fmt.Errorf("prepare state.json: %w", err)
		}
		mutationID, err := newArtifactMutationID()
		if err != nil {
			publishPendingRecovery()
			return err
		}
		journal := mutationJournal{
			Schema: mutationJournalSchema, MutationID: mutationID, PlanID: state.Plan.ID,
			CreatedAt: time.Now().UTC(), State: newMutationJournalPayload(payload),
		}
		if err := store.settleMutationLocked(planDir, journal); err != nil {
			publishPendingRecovery()
			return err
		}
		working.State = state
		*detail = *working
		return nil
	})
	return recoveredBaseline, err
}

func applySlicesArtifactUpdate(store artifactMutationStore, planDir string, detail *PlanDetail, mutate func(*PlanDetail, *ArtifactChangeSet) error) error {
	return store.withMutationLock(planDir, func() error {
		working, _, err := artifactMutationWorkingDetailLocked(store, planDir, detail, true, true)
		if err != nil {
			return err
		}
		baseline := cloneSlicesFile(working.Slices)
		changes := NewArtifactChangeSet(working)
		if err := mutate(working, changes); err != nil {
			return err
		}
		if err := validateSlicesChangeDeclarations(baseline, working.Slices, changes); err != nil {
			return err
		}
		payload, err := prepareJSON(filepath.Join(planDir, "slices.json"), working.Slices, slicesJSONChanges(changes))
		if err != nil {
			return fmt.Errorf("prepare slices.json: %w", err)
		}
		mutationID, err := newArtifactMutationID()
		if err != nil {
			return err
		}
		journal := mutationJournal{
			Schema: mutationJournalSchema, MutationID: mutationID, PlanID: working.Slices.PlanID,
			CreatedAt: time.Now().UTC(), Slices: newMutationJournalPayload(payload),
		}
		if err := store.settleMutationLocked(planDir, journal); err != nil {
			return err
		}
		*detail = *working
		return nil
	})
}

func rebaseArtifact[T any](baseline, intended, settled T) T {
	return rebaseArtifactValue(reflect.ValueOf(baseline), reflect.ValueOf(intended), reflect.ValueOf(settled)).Interface().(T)
}

type artifactStructure struct {
	status          string
	currentSlice    string
	hasCurrentSlice bool
	completedSlices []string
	pendingSlices   []string
	slices          []artifactSliceStructure
}

type artifactSliceStructure struct {
	id        string
	status    string
	dependsOn []string
}

// conflictingArtifactStructureChanges rejects ambiguous three-way merges before
// state.json and slices.json are prepared independently. Field-level edits can
// safely rebase over a lifecycle transition, but two different structural
// changes require a reload so their coupled state and slice lists cannot drift.
func conflictingArtifactStructureChanges(baseline, intended, settled *PlanDetail) bool {
	baselineStructure := planArtifactStructure(baseline)
	intendedStructure := planArtifactStructure(intended)
	settledStructure := planArtifactStructure(settled)
	return !reflect.DeepEqual(baselineStructure, intendedStructure) &&
		!reflect.DeepEqual(baselineStructure, settledStructure) &&
		!reflect.DeepEqual(intendedStructure, settledStructure)
}

func planArtifactStructure(detail *PlanDetail) artifactStructure {
	if detail == nil {
		return artifactStructure{}
	}
	structure := artifactStructure{
		status:          detail.State.Status,
		completedSlices: append([]string(nil), detail.State.Plan.CompletedSlices...),
		pendingSlices:   append([]string(nil), detail.State.Plan.PendingSlices...),
		slices:          make([]artifactSliceStructure, 0, len(detail.Slices.Slices)),
	}
	if detail.State.Plan.CurrentSlice != nil {
		structure.currentSlice = *detail.State.Plan.CurrentSlice
		structure.hasCurrentSlice = true
	}
	for _, slice := range detail.Slices.Slices {
		structure.slices = append(structure.slices, artifactSliceStructure{
			id:        slice.ID,
			status:    slice.Status,
			dependsOn: append([]string(nil), slice.DependsOn...),
		})
	}
	return structure
}

func rebaseArtifactValue(baseline, intended, settled reflect.Value) reflect.Value {
	if reflect.DeepEqual(baseline.Interface(), intended.Interface()) {
		return settled
	}
	if baseline.Kind() != intended.Kind() || intended.Kind() != settled.Kind() {
		return intended
	}
	switch intended.Kind() {
	case reflect.Struct:
		result := reflect.New(intended.Type()).Elem()
		for i := range intended.NumField() {
			if !result.Field(i).CanSet() || intended.Type().Field(i).PkgPath != "" {
				return intended
			}
			result.Field(i).Set(rebaseArtifactValue(baseline.Field(i), intended.Field(i), settled.Field(i)))
		}
		return result
	case reflect.Pointer:
		if intended.IsNil() || settled.IsNil() {
			return intended
		}
		baselineElement := reflect.Zero(intended.Type().Elem())
		if !baseline.IsNil() {
			baselineElement = baseline.Elem()
		}
		result := reflect.New(intended.Type().Elem())
		result.Elem().Set(rebaseArtifactValue(baselineElement, intended.Elem(), settled.Elem()))
		return result
	case reflect.Slice:
		if baseline.IsNil() != intended.IsNil() {
			return intended
		}
		if intended.Type() == reflect.TypeFor[[]Slice]() {
			return reflect.ValueOf(rebaseArtifactSlices(
				baseline.Interface().([]Slice),
				intended.Interface().([]Slice),
				settled.Interface().([]Slice),
			))
		}
		if baseline.Len() != intended.Len() || settled.Len() != intended.Len() {
			return intended
		}
		result := reflect.MakeSlice(intended.Type(), intended.Len(), intended.Len())
		for i := range intended.Len() {
			result.Index(i).Set(rebaseArtifactValue(baseline.Index(i), intended.Index(i), settled.Index(i)))
		}
		return result
	case reflect.Array:
		result := reflect.New(intended.Type()).Elem()
		for i := range intended.Len() {
			result.Index(i).Set(rebaseArtifactValue(baseline.Index(i), intended.Index(i), settled.Index(i)))
		}
		return result
	default:
		return intended
	}
}

// rebaseArtifactSlices uses slice IDs as stable identities so a stale
// full-artifact writer cannot undo lifecycle additions or removals merely
// because the slices changed length after the record was bound.
func rebaseArtifactSlices(baseline, intended, settled []Slice) []Slice {
	baselineByID, baselineIDs, ok := artifactSlicesByID(baseline)
	if !ok {
		return intended
	}
	intendedByID, intendedIDs, ok := artifactSlicesByID(intended)
	if !ok {
		return intended
	}
	settledByID, settledIDs, ok := artifactSlicesByID(settled)
	if !ok {
		return intended
	}

	order := settledIDs
	switch {
	case reflect.DeepEqual(settledIDs, baselineIDs):
		order = intendedIDs
	case !reflect.DeepEqual(intendedIDs, baselineIDs):
		order = append(append([]string(nil), settledIDs...), intendedIDs...)
	}

	result := make([]Slice, 0, len(order))
	included := make(map[string]bool, len(order))
	for _, id := range order {
		if included[id] {
			continue
		}
		baselineSlice, existed := baselineByID[id]
		intendedSlice, intendedPresent := intendedByID[id]
		settledSlice, settledPresent := settledByID[id]
		switch {
		case existed:
			// Removal by either side wins over a stale copy of the removed slice.
			if !intendedPresent || !settledPresent {
				continue
			}
			result = append(result, rebaseArtifact(baselineSlice, intendedSlice, settledSlice))
		case intendedPresent:
			result = append(result, intendedSlice)
		case settledPresent:
			result = append(result, settledSlice)
		}
		included[id] = true
	}
	return result
}

func artifactSlicesByID(values []Slice) (map[string]Slice, []string, bool) {
	byID := make(map[string]Slice, len(values))
	ids := make([]string, 0, len(values))
	for _, slice := range values {
		if slice.ID == "" {
			return nil, nil, false
		}
		if _, exists := byID[slice.ID]; exists {
			return nil, nil, false
		}
		byID[slice.ID] = slice
		ids = append(ids, slice.ID)
	}
	return byID, ids, true
}

// artifactMutationWasRecovered recognizes the transition derived from the
// caller's stale pre-attempt detail after a journal replay refreshed the record.
func artifactMutationWasRecovered(detail *PlanDetail, requested lifecycleMutation) bool {
	if !reflect.DeepEqual(detail.State, requested.State) || !reflect.DeepEqual(detail.Slices, requested.Slices) {
		return false
	}
	return len(requested.Events) == 0 || eventsWereRecorded(detail.Events, requested.Events)
}

func eventsWereRecorded(recorded []Event, requested []Event) bool {
	return eventsWereRecordedBy(recorded, requested, func(event *Event) {
		event.MutationID = ""
	})
}

func semanticEventsWereRecorded(recorded []Event, requested []Event) bool {
	return eventsWereRecordedBy(recorded, requested, func(event *Event) {
		event.Timestamp = time.Time{}
		event.MutationID = ""
	})
}

func eventsWereRecordedBy(recorded []Event, requested []Event, normalize func(*Event)) bool {
	for _, requestedEvent := range requested {
		normalize(&requestedEvent)
		found := false
		for _, recordedEvent := range recorded {
			normalize(&recordedEvent)
			if reflect.DeepEqual(recordedEvent, requestedEvent) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func newArtifactMutationID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate mutation id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func loadFileMutationBaseline(planDir, expectedPlanID string) (*PlanDetail, bool, error) {
	if _, err := os.Stat(planDir); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	type baselineResult struct {
		detail    *PlanDetail
		recovered bool
	}
	result, err := withMutationPersistenceLock(planDir, func() (baselineResult, error) {
		recovered, settleErr := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, expectedPlanID)
		if settleErr != nil {
			return baselineResult{}, fmt.Errorf("recover plan mutation: %w", settleErr)
		}
		files, loadErr := loadPlanFilesLocked(planDir)
		if loadErr != nil {
			if !recovered && errors.Is(loadErr, os.ErrNotExist) {
				return baselineResult{}, nil
			}
			return baselineResult{}, loadErr
		}
		return baselineResult{detail: detailFromFiles(files), recovered: recovered}, nil
	})
	return result.detail, result.recovered, err
}

func (fileArtifactStore) withMutationLock(planDir string, operation func() error) error {
	_, err := withMutationPersistenceLock(planDir, func() (struct{}, error) {
		return struct{}{}, operation()
	})
	return err
}

func (fileArtifactStore) settleMutationLocked(planDir string, journal mutationJournal) error {
	return installAndSettleMutationLocked(fileMutationJournalIO{}, planDir, journal)
}

func (fileArtifactStore) refreshMutationDetailLocked(planDir string, expectedPlanID string, force bool) (mutationDetailRefresh, error) {
	recovered, err := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, expectedPlanID)
	if err != nil {
		return mutationDetailRefresh{}, fmt.Errorf("recover plan mutation: %w", err)
	}
	if !force && !recovered {
		return mutationDetailRefresh{}, nil
	}
	files, err := loadPlanFilesLocked(planDir)
	if err != nil {
		if !recovered && errors.Is(err, os.ErrNotExist) {
			return mutationDetailRefresh{}, nil
		}
		return mutationDetailRefresh{}, err
	}
	return mutationDetailRefresh{detail: detailFromFiles(files), refreshed: true, recovered: recovered}, nil
}

// AppendEvent appends one lifecycle event to events.jsonl in a plan directory.
func AppendEvent(planDir string, event Event) error {
	file, err := os.OpenFile(filepath.Join(planDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		_ = file.Close()
		return err
	}
	if _, err := fmt.Fprintln(file, string(encoded)); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// writeJSON encodes value as JSON and deep-merges it over any existing file at
// path before writing atomically. The merge preserves unknown fields from older
// plan artifacts. PlanRecord writers carry explicit clear-or-replace intent in
// ArtifactChangeSet; migrated omitempty groups emit explicit replacement or
// empty values only when that intent is declared. Remaining clearable fields
// retain the tag-driven contract. The
// low-level creation/test writer remains preserve-free and emits exactly what
// the current struct tags encode.
func writeJSON(path string, value any) error {
	encoded, err := prepareJSON(path, value, artifactJSONChanges{})
	if err != nil {
		return err
	}
	return atomicWriteFile(path, encoded)
}

// prepareJSON returns the exact final bytes for a merge-preserving artifact
// write. Typed clear-or-replace intent is lowered before the unknown-field
// preserving merge. Callers may durably journal these bytes before installing
// the same byte slice with atomicWriteFile.
func prepareJSON(path string, value any, changes artifactJSONChanges) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded, err = lowerArtifactJSONChanges(encoded, changes)
	if err != nil {
		return nil, err
	}
	if existing, err := os.ReadFile(path); err == nil { //nolint:gosec // G304: path is internally constructed plan artifact path
		if merged, err := mergeJSON(existing, encoded); err == nil {
			encoded = merged
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	encoded, err = removeOmittedArtifactJSONFields(encoded, changes)
	if err != nil {
		return nil, err
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, encoded, "", "  "); err != nil {
		return nil, err
	}
	return append(formatted.Bytes(), '\n'), nil
}

func lowerArtifactJSONChanges(encoded []byte, projection artifactJSONChanges) ([]byte, error) {
	changes := projection.changes
	if changes == nil {
		return encoded, nil
	}
	hasStateChanges := changes.clearWorkspaceDependencyFailure || changes.clearWorkspaceDependencyFingerprint || changes.clearWorkspaceRebaseIntent || changes.clearPlanCurrentSlice || changes.clearPlanFinalizationFailure || changes.clearSingleMergeResolution || changes.planFinalizationFailure != nil || changes.finalVerification != nil || changes.planReview.kind != planReviewUnchanged
	hasSliceChanges := len(changes.clearSliceBlockerNotes) > 0 || len(changes.clearSliceExecutionBoundaries) > 0
	if projection.kind == artifactJSONState && !hasStateChanges || projection.kind == artifactJSONSlices && !hasSliceChanges || projection.kind == artifactJSONNone {
		return encoded, nil
	}

	var root map[string]any
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil, fmt.Errorf("lower artifact changes: %w", err)
	}
	switch projection.kind {
	case artifactJSONState:
		if err := lowerStateJSONChanges(root, changes); err != nil {
			return nil, err
		}
	case artifactJSONSlices:
		if err := lowerSlicesJSONChanges(root, changes); err != nil {
			return nil, err
		}
	}
	lowered, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("lower artifact changes: %w", err)
	}
	return lowered, nil
}

func removeOmittedArtifactJSONFields(encoded []byte, projection artifactJSONChanges) ([]byte, error) {
	changes := projection.changes
	if projection.kind != artifactJSONState || changes == nil || changes.finalVerification == nil || changes.finalVerification.FailureKind != "" && changes.finalVerification.ExitCode != nil {
		return encoded, nil
	}
	var root map[string]any
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil, fmt.Errorf("remove omitted artifact fields: %w", err)
	}
	plan, _ := root["plan"].(map[string]any)
	verification, _ := plan["final_verification"].(map[string]any)
	if changes.finalVerification.FailureKind == "" {
		delete(verification, "failure_kind")
	}
	if changes.finalVerification.ExitCode == nil {
		delete(verification, "exit_code")
	}
	cleaned, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("remove omitted artifact fields: %w", err)
	}
	return cleaned, nil
}

func validateSlicesChangeDeclarations(baseline, intended SlicesFile, changes *ArtifactChangeSet) error {
	baselineByID, _, ok := artifactSlicesByID(baseline.Slices)
	if !ok {
		return nil
	}
	for _, slice := range intended.Slices {
		before, existed := baselineByID[slice.ID]
		if !existed || before.BlockerNote == "" || slice.BlockerNote != "" {
			continue
		}
		if changes == nil {
			return fmt.Errorf("persist slices: Slice.BlockerNote changed from non-zero to zero without ClearSliceBlockerNote for slice %s", slice.ID)
		}
		if _, declared := changes.clearSliceBlockerNotes[slice.ID]; !declared {
			return fmt.Errorf("persist slices: Slice.BlockerNote changed from non-zero to zero without ClearSliceBlockerNote for slice %s", slice.ID)
		}
	}
	return nil
}

func validateStateChangeDeclarations(baseline, intended State, changes *ArtifactChangeSet) error {
	var baselineFailure, baselineFingerprint string
	var baselineRebaseIntent, intendedRebaseIntent *WorkspaceRebaseIntent
	if baseline.Workspace != nil {
		baselineFailure = baseline.Workspace.DependencyFailure
		baselineFingerprint = baseline.Workspace.DependencyFingerprint
		baselineRebaseIntent = baseline.Workspace.RebaseIntent
	}
	var intendedFailure, intendedFingerprint string
	if intended.Workspace != nil {
		intendedFailure = intended.Workspace.DependencyFailure
		intendedFingerprint = intended.Workspace.DependencyFingerprint
		intendedRebaseIntent = intended.Workspace.RebaseIntent
	}
	if baselineFailure != "" && intendedFailure == "" && (changes == nil || !changes.clearWorkspaceDependencyFailure) {
		return fmt.Errorf("persist state: Workspace.DependencyFailure changed from non-zero to zero without ClearWorkspaceDependencyFailure")
	}
	if baselineFingerprint != "" && intendedFingerprint == "" && (changes == nil || !changes.clearWorkspaceDependencyFingerprint) {
		return fmt.Errorf("persist state: Workspace.DependencyFingerprint changed from non-zero to zero without ClearWorkspaceDependencyFingerprint")
	}
	if baselineRebaseIntent != nil && intendedRebaseIntent == nil && (changes == nil || !changes.clearWorkspaceRebaseIntent) {
		return fmt.Errorf("persist state: Workspace.RebaseIntent changed from non-zero to zero without ClearWorkspaceRebaseIntent")
	}
	if baseline.Plan.CurrentSlice != nil && intended.Plan.CurrentSlice == nil && (changes == nil || !changes.clearPlanCurrentSlice) {
		return fmt.Errorf("persist state: State.Plan.CurrentSlice changed from non-zero to zero without ClearPlanCurrentSlice")
	}
	if baseline.Plan.FinalizationFailure != nil && intended.Plan.FinalizationFailure == nil && (changes == nil || !changes.clearPlanFinalizationFailure) {
		return fmt.Errorf("persist state: State.Plan.FinalizationFailure changed from non-zero to zero without ClearPlanFinalizationFailure")
	}
	if baseline.Plan.MergeCommitIntent != nil && baseline.Plan.MergeCommitIntent.Resolution != nil && intended.Plan.MergeCommitIntent != nil && intended.Plan.MergeCommitIntent.Resolution == nil && (changes == nil || !changes.clearSingleMergeResolution) {
		return fmt.Errorf("persist state: SingleMergeCommitIntent.Resolution changed from non-zero to zero without ClearSingleMergeResolution")
	}
	if err := validatePlanReviewChangeDeclaration(baseline.Plan.Review, intended.Plan.Review, changes); err != nil {
		return err
	}
	return nil
}

func validatePlanReviewChangeDeclaration(baseline, intended *PlanReview, changes *ArtifactChangeSet) error {
	declared := planReviewUnchanged
	if changes != nil {
		declared = changes.planReview.kind
	}
	if baseline == nil {
		return nil
	}
	if intended == nil {
		if declared != planReviewCleared {
			return fmt.Errorf("persist state: PlanState.Review changed from non-zero to zero without ClearPlanReview")
		}
		return nil
	}
	if declared == planReviewReplaced {
		return nil
	}
	if baseline.Verdict != "" && intended.Verdict == "" {
		return fmt.Errorf("persist state: PlanReview.Verdict changed from non-zero to zero without ReplacePlanReview")
	}
	if baseline.Summary != "" && intended.Summary == "" {
		return fmt.Errorf("persist state: PlanReview.Summary changed from non-zero to zero without ReplacePlanReview")
	}
	if baseline.FindingsCount != 0 && intended.FindingsCount == 0 {
		return fmt.Errorf("persist state: PlanReview.FindingsCount changed from non-zero to zero without ReplacePlanReview")
	}
	if len(baseline.Findings) != 0 && len(intended.Findings) == 0 {
		return fmt.Errorf("persist state: PlanReview.Findings changed from non-zero to zero without ReplacePlanReview")
	}
	if baseline.CommitMessage != nil && intended.CommitMessage == nil {
		return fmt.Errorf("persist state: PlanReview.CommitMessage changed from non-zero to zero without ReplacePlanReview")
	}
	if !baseline.ReviewedAt.IsZero() && intended.ReviewedAt.IsZero() {
		return fmt.Errorf("persist state: PlanReview.ReviewedAt changed from non-zero to zero without ReplacePlanReview")
	}
	return nil
}

func lowerStateJSONChanges(root map[string]any, changes *ArtifactChangeSet) error {
	if changes.clearWorkspaceDependencyFailure || changes.clearWorkspaceDependencyFingerprint || changes.clearWorkspaceRebaseIntent {
		workspace := jsonObject(root, "workspace")
		if changes.clearWorkspaceDependencyFailure {
			workspace["dependency_preparation_failure"] = ""
		}
		if changes.clearWorkspaceDependencyFingerprint {
			workspace["dependency_fingerprint"] = ""
		}
		if changes.clearWorkspaceRebaseIntent {
			workspace["rebase_intent"] = nil
		}
	}
	plan := jsonObject(root, "plan")
	if changes.clearSingleMergeResolution {
		intent := jsonObject(plan, "merge_commit_intent")
		intent["resolution"] = nil
	}
	if changes.clearPlanCurrentSlice {
		plan["current_slice"] = nil
	}
	if changes.finalVerification != nil {
		encoded, err := json.Marshal(changes.finalVerification)
		if err != nil {
			return fmt.Errorf("lower final verification replacement: %w", err)
		}
		var replacement map[string]any
		if err := json.Unmarshal(encoded, &replacement); err != nil {
			return fmt.Errorf("lower final verification replacement: %w", err)
		}
		plan["final_verification"] = replacement
	}
	if changes.clearPlanFinalizationFailure {
		plan["finalization_failure"] = nil
	} else if changes.planFinalizationFailure != nil {
		encoded, err := json.Marshal(changes.planFinalizationFailure)
		if err != nil {
			return fmt.Errorf("lower finalization failure replacement: %w", err)
		}
		var replacement map[string]any
		if err := json.Unmarshal(encoded, &replacement); err != nil {
			return fmt.Errorf("lower finalization failure replacement: %w", err)
		}
		// The merge-preserving writer combines objects recursively, so include
		// every phase-specific key to overwrite fields omitted by struct tags.
		replacement["branch"] = changes.planFinalizationFailure.Branch
		replacement["head_sha"] = changes.planFinalizationFailure.HeadSHA
		replacement["review_base"] = changes.planFinalizationFailure.ReviewBase
		replacement["review_head"] = changes.planFinalizationFailure.ReviewHead
		plan["finalization_failure"] = replacement
	}
	switch changes.planReview.kind {
	case planReviewReplaced:
		review, err := replacementReviewJSON(changes.planReview.review)
		if err != nil {
			return err
		}
		plan["review"] = review
	case planReviewCleared:
		plan["review"] = nil
	}
	return nil
}

func replacementReviewJSON(review PlanReview) (map[string]any, error) {
	encoded, err := json.Marshal(review)
	if err != nil {
		return nil, fmt.Errorf("lower review replacement: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, fmt.Errorf("lower review replacement: %w", err)
	}
	object["status"] = review.Status
	object["verdict"] = review.Verdict
	object["summary"] = review.Summary
	object["findings_count"] = review.FindingsCount
	if len(review.Findings) == 0 {
		object["findings"] = []any{}
	}
	if review.CommitMessage == nil {
		object["commit_message"] = nil
	}
	object["base"] = review.Base
	object["head"] = review.Head
	object["agent"] = review.Agent
	object["reviewed_at"] = review.ReviewedAt
	return object, nil
}

func lowerSlicesJSONChanges(root map[string]any, changes *ArtifactChangeSet) error {
	values, _ := root["slices"].([]any)
	found := make(map[string]bool, len(changes.clearSliceBlockerNotes)+len(changes.clearSliceExecutionBoundaries))
	for _, value := range values {
		object, _ := value.(map[string]any)
		id, _ := object["id"].(string)
		if _, ok := changes.clearSliceBlockerNotes[id]; ok {
			object["blocker_note"] = ""
			found[id] = true
		}
		if _, ok := changes.clearSliceExecutionBoundaries[id]; ok {
			object["execution_root"] = ""
			object["execution_start"] = nil
			found[id] = true
		}
	}
	for id := range changes.clearSliceBlockerNotes {
		if !found[id] {
			return fmt.Errorf("lower artifact changes: slice %s not found", id)
		}
	}
	for id := range changes.clearSliceExecutionBoundaries {
		if !found[id] {
			return fmt.Errorf("lower artifact changes: slice %s not found", id)
		}
	}
	return nil
}

func jsonObject(parent map[string]any, key string) map[string]any {
	if object, ok := parent[key].(map[string]any); ok {
		return object
	}
	object := make(map[string]any)
	parent[key] = object
	return object
}

func atomicWriteFile(path string, data []byte) error {
	return atomicfile.Write(path, data, atomicfile.Options{})
}

func isUnsupportedDirSyncError(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}

// mergeJSON deep-merges update over existing: objects are merged key-by-key,
// arrays are merged by their id or plan_id field (or fully replaced when no
// identity is present or when update is empty), and all other values are
// replaced by the update.
// See writeJSON for the clearable-field contract that this function enforces.
func mergeJSON(existing []byte, update []byte) ([]byte, error) {
	var existingValue any
	if err := json.Unmarshal(existing, &existingValue); err != nil {
		return nil, err
	}
	var updateValue any
	if err := json.Unmarshal(update, &updateValue); err != nil {
		return nil, err
	}
	return json.Marshal(mergeJSONValue(existingValue, updateValue))
}

func mergeJSONValue(existing any, update any) any {
	existingObject, existingIsObject := existing.(map[string]any)
	updateObject, updateIsObject := update.(map[string]any)
	if existingIsObject && updateIsObject {
		for key, value := range updateObject {
			existingObject[key] = mergeJSONValue(existingObject[key], value)
		}
		return existingObject
	}
	existingArray, existingIsArray := existing.([]any)
	updateArray, updateIsArray := update.([]any)
	if existingIsArray && updateIsArray {
		return mergeJSONArray(existingArray, updateArray)
	}
	return update
}

func mergeJSONArray(existing []any, update []any) []any {
	existingByID := make(map[objectIdentity]any, len(existing))
	for _, value := range existing {
		for _, identity := range objectIdentities(value) {
			existingByID[identity] = value
		}
	}
	merged := make([]any, 0, len(update))
	for _, value := range update {
		if identity, ok := objectID(value); ok {
			if existingValue, found := existingByID[identity]; found {
				value = mergeJSONValue(existingValue, value)
			}
		}
		merged = append(merged, value)
	}
	return merged
}

type objectIdentity struct {
	field string
	value string
}

func objectIdentities(value any) []objectIdentity {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	identities := make([]objectIdentity, 0, 2)
	for _, field := range []string{"id", "plan_id"} {
		if id, _ := object[field].(string); id != "" {
			identities = append(identities, objectIdentity{field: field, value: id})
		}
	}
	return identities
}

func objectID(value any) (objectIdentity, bool) {
	identities := objectIdentities(value)
	if len(identities) == 0 {
		return objectIdentity{}, false
	}
	return identities[0], true
}

func readJSON(path string, out any) error {
	file, err := os.Open(path) // #nosec G304 -- plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	return nil
}

const maxEventJSONLLineBytes = 1024 * 1024

func readEvents(path string) ([]Event, []string, error) {
	file, err := os.Open(path) // #nosec G304 -- plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()

	var events []Event
	var warnings []string
	reader := bufio.NewReader(file)
	line := 0
	for {
		lineBytes, oversized, err := readLimitedJSONLLine(reader, maxEventJSONLLineBytes)
		if errors.Is(err, io.EOF) && len(lineBytes) == 0 && !oversized {
			break
		}
		line++
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, warnings, err
		}
		if oversized {
			warnings = append(warnings, fmt.Sprintf("events.jsonl line %d exceeds %d bytes; skipped", line, maxEventJSONLLineBytes))
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		text := strings.TrimSpace(string(lineBytes))
		if text != "" {
			var event Event
			if err := json.Unmarshal([]byte(text), &event); err != nil {
				warnings = append(warnings, fmt.Sprintf("events.jsonl line %d: %v", line, err))
			} else {
				events = append(events, event)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return events, warnings, nil
}

func readLimitedJSONLLine(reader *bufio.Reader, maxBytes int) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 && !oversized {
			if len(line)+len(fragment) > maxBytes {
				oversized = true
				line = nil
			} else {
				line = append(line, fragment...)
			}
		}
		switch {
		case err == nil:
			return line, oversized, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(fragment) == 0 && len(line) == 0 && !oversized {
				return nil, false, io.EOF
			}
			return line, oversized, io.EOF
		default:
			return line, oversized, err
		}
	}
}

// Optional sidecar readers below keep local-only planning artifacts out of the
// core state/slices schema and surface unreadable data as warnings.
func readPlanningSessionArtifacts(dir string) (PlanningSessionArtifacts, []string) {
	artifacts := PlanningSessionArtifacts{}
	var warnings []string

	exportPath := filepath.Join(dir, PlanningSessionExportFile)
	if info, err := os.Stat(exportPath); err == nil && !info.IsDir() {
		artifacts.ExportPath = exportPath
		artifacts.HasExport = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("%s: %v", PlanningSessionExportFile, err))
	}

	statsPath := filepath.Join(dir, PlanningSessionStatsFile)
	var stats PlanningSessionStats
	if ok, err := readOptionalJSON(statsPath, &stats); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: %v", PlanningSessionStatsFile, err))
	} else if ok {
		artifacts.Stats = &stats
		if !stats.PromptExtracted && stats.PromptExtractionNote != "" {
			warnings = append(warnings, fmt.Sprintf("%s: planning prompt extraction failed: %s", PlanningSessionStatsFile, stats.PromptExtractionNote))
		}
	}

	promptPath := filepath.Join(dir, PlanningPromptFile)
	if content, err := os.ReadFile(promptPath); err == nil { //nolint:gosec // G304: promptPath is internally constructed plan artifact path
		artifacts.PromptPath = promptPath
		artifacts.Prompt = string(content)
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("%s: %v", PlanningPromptFile, err))
	}

	return artifacts, warnings
}

func readPlanningBriefArtifact(dir string) (PlanningBriefArtifact, []string) {
	path := filepath.Join(dir, PlanningBriefFile)
	content, err := os.ReadFile(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanningBriefArtifact{}, nil
		}
		return PlanningBriefArtifact{}, []string{fmt.Sprintf("%s: %v", PlanningBriefFile, err)}
	}
	return PlanningBriefArtifact{Path: path, Content: string(content)}, nil
}

// WriteReviewArtifact writes the human-readable review.md artifact atomically.
func WriteReviewArtifact(planDir string, content string) error {
	if err := atomicWriteFile(filepath.Join(planDir, ReviewFile), []byte(content)); err != nil {
		return fmt.Errorf("write %s: %w", ReviewFile, err)
	}
	return nil
}

func readReviewArtifact(dir string) (PlanReviewArtifact, []string) {
	path := filepath.Join(dir, ReviewFile)
	content, err := os.ReadFile(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanReviewArtifact{}, nil
		}
		return PlanReviewArtifact{}, []string{fmt.Sprintf("%s: %v", ReviewFile, err)}
	}
	return PlanReviewArtifact{Path: path, Content: string(content)}, nil
}

func readPlanNarrativeArtifact(dir string) (PlanNarrativeArtifact, []string) {
	path := filepath.Join(dir, PlanMarkdownFile)
	content, err := os.ReadFile(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanNarrativeArtifact{}, nil
		}
		return PlanNarrativeArtifact{}, []string{fmt.Sprintf("%s: %v", PlanMarkdownFile, err)}
	}
	return PlanNarrativeArtifact{Path: path, Content: string(content)}, nil
}

func readOptionalJSON(path string, out any) (bool, error) {
	file, err := os.Open(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(out); err != nil {
		return true, err
	}
	return true, nil
}
