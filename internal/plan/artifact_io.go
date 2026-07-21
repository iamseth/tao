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

	return planFiles{dir: dir, state: state, slices: slices, events: events, planningSession: planningSession, planningBrief: planningBrief, review: review, planNarrative: planNarrative, warnings: warnings}, nil
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

type artifactMutationFunc func(*PlanDetail) (lifecycleMutation, error)

func startSliceMutation(sliceID string, executionRoot string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func() ([]Event, error) {
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
		return applyLifecycleMutation(detail, func() ([]Event, error) {
			event, appendEvent, err := MarkSliceCompletedWithOutcome(detail, sliceID, notes, verificationResults, outcome, now)
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
		return applyLifecycleMutation(detail, func() ([]Event, error) {
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
		return applyLifecycleMutation(detail, func() ([]Event, error) {
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

func continueBlockedMutation(now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func() ([]Event, error) {
			return nil, MarkBlockedContinued(detail, now)
		})
	}
}

func removeSliceMutation(sliceID string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		expected := Event{Type: EventTypeSliceRemoved, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Pending slice removed by plan edit"}
		if semanticEventsWereRecorded(detail.Events, []Event{expected}) && findSlice(detail, sliceID) == nil && !slices.Contains(detail.State.Plan.PendingSlices, sliceID) {
			return unchangedLifecycleMutation(detail), nil
		}
		return applyLifecycleMutation(detail, func() ([]Event, error) {
			event, err := MarkSliceRemoved(detail, sliceID, now)
			if err != nil {
				return nil, err
			}
			return []Event{event}, nil
		})
	}
}

func skipSliceMutation(sliceID string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		expected := Event{Type: EventTypeSliceSkipped, Timestamp: now, PlanID: detail.State.Plan.ID, SliceID: sliceID, Message: "Pending slice skipped by plan edit"}
		slice := findSlice(detail, sliceID)
		if semanticEventsWereRecorded(detail.Events, []Event{expected}) && slice != nil && slice.Status == StatusSkipped && !slices.Contains(detail.State.Plan.PendingSlices, sliceID) {
			return unchangedLifecycleMutation(detail), nil
		}
		return applyLifecycleMutation(detail, func() ([]Event, error) {
			event, err := MarkSliceSkipped(detail, sliceID, now)
			if err != nil {
				return nil, err
			}
			return []Event{event}, nil
		})
	}
}

func reorderPendingSlicesMutation(pendingOrder []string, now time.Time) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		expected := Event{Type: EventTypeSlicesReordered, Timestamp: now, PlanID: detail.State.Plan.ID, Message: "Pending slices reordered by plan edit"}
		if semanticEventsWereRecorded(detail.Events, []Event{expected}) && slices.Equal(detail.State.Plan.PendingSlices, pendingOrder) {
			return unchangedLifecycleMutation(detail), nil
		}
		return applyLifecycleMutation(detail, func() ([]Event, error) {
			event, err := MarkPendingSlicesReordered(detail, pendingOrder, now)
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

func blockedContinuationWasRecovered(stale, settled *PlanDetail) bool {
	if stale == nil || settled == nil {
		return false
	}
	expected := clonePlanDetail(stale)
	if err := MarkBlockedContinued(expected, settled.State.UpdatedAt); err != nil {
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
	statePayload, err := prepareJSON(filepath.Join(planDir, "state.json"), mutation.State)
	if err != nil {
		return fmt.Errorf("prepare state.json: %w", err)
	}
	slicesPayload, err := prepareJSON(filepath.Join(planDir, "slices.json"), mutation.Slices)
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
func applyStateEventMutationWithRefresh(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, mutate func(*PlanDetail) ([]Event, error)) error {
	return store.withMutationLock(planDir, func() error {
		return applyStateEventMutationLocked(store, planDir, detail, forceRefresh, mutate)
	})
}

// applyStateEventMutationLocked requires the store's mutation lock for planDir.
func applyStateEventMutationLocked(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, mutate func(*PlanDetail) ([]Event, error)) error {
	clone, _, err := artifactMutationWorkingDetailLocked(store, planDir, detail, forceRefresh, true)
	if err != nil {
		return err
	}
	baseline := clonePlanDetail(clone)
	eventsToAppend, err := mutate(clone)
	if err != nil {
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
	statePayload, err := prepareJSON(filepath.Join(planDir, "state.json"), clone.State)
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
func applyStateArtifactUpdate(store artifactMutationStore, planDir string, detail *PlanDetail, baseline, intended State) (*PlanDetail, error) {
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
		publishPendingRecovery := func() {
			if recovered {
				working.State = state
				*detail = *working
			}
		}
		payload, err := prepareJSON(filepath.Join(planDir, "state.json"), state)
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

func applySlicesArtifactUpdate(store artifactMutationStore, planDir string, detail *PlanDetail, mutate func(*PlanDetail) error) error {
	return store.withMutationLock(planDir, func() error {
		working, _, err := artifactMutationWorkingDetailLocked(store, planDir, detail, true, true)
		if err != nil {
			return err
		}
		if err := mutate(working); err != nil {
			return err
		}
		payload, err := prepareJSON(filepath.Join(planDir, "slices.json"), working.Slices)
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
// path before writing atomically.  The merge preserves unknown fields from
// older plan artifacts.
//
// Merge-write contract for struct fields:
//   - Fields with omitempty are MERGE-ONLY: when the Go value is zero/nil the
//     key is absent from the encoded JSON, so the merge leaves any previously
//     stored non-zero value untouched.  A later write cannot clear such a field.
//   - Fields WITHOUT omitempty are CLEARABLE: zero/nil/empty values emit an
//     explicit JSON key (null, "", or []), and the merge overwrites the prior
//     stored value.  Known clearable fields include State.Plan.CurrentSlice,
//     Workspace.DependencyFailure, PlanReview.Findings, and Slice.BlockerNote.
//     See clearable_fields_test.go for the full registry.
//
// Adding omitempty to a currently-clearable field is a breaking schema change.
func writeJSON(path string, value any) error {
	encoded, err := prepareJSON(path, value)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, encoded)
}

// prepareJSON returns the exact final bytes for a merge-preserving artifact
// write. Callers may durably journal these bytes before installing the same
// byte slice with atomicWriteFile.
func prepareJSON(path string, value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
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
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, encoded, "", "  "); err != nil {
		return nil, err
	}
	return append(formatted.Bytes(), '\n'), nil
}

func atomicWriteFile(path string, data []byte) error {
	return atomicfile.Write(path, data, atomicfile.Options{})
}

func isUnsupportedDirSyncError(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}

// mergeJSON deep-merges update over existing: objects are merged key-by-key,
// arrays are merged by id field (or fully replaced when no id is present or
// when update is empty), and all other values are replaced by the update.
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
	existingByID := make(map[string]any, len(existing))
	for _, value := range existing {
		if id := objectID(value); id != "" {
			existingByID[id] = value
		}
	}
	merged := make([]any, 0, len(update))
	for _, value := range update {
		if id := objectID(value); id != "" {
			if existingValue, ok := existingByID[id]; ok {
				value = mergeJSONValue(existingValue, value)
			}
		}
		merged = append(merged, value)
	}
	return merged
}

func objectID(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := object["id"].(string)
	return id
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
