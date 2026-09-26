package plan

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"
)

type artifactMutationFunc func(*PlanDetail) (lifecycleMutation, error)

func startSliceRequestMutation(sliceID string, request SliceStartRequest) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		if request.Run != nil {
			if err := markRunStartMetadata(detail, request.Run.CommitPolicy, request.Run.StartingDirtyPaths); err != nil {
				return lifecycleMutation{}, err
			}
		}
		if request.Boundary != nil {
			if err := markSliceExecutionStart(detail, sliceID, *request.Boundary); err != nil {
				return lifecycleMutation{}, err
			}
		}
		return applyLifecycleMutation(detail, func(_ *artifactChangeSet) ([]Event, error) {
			if request.ExecutionRoot != "" {
				if err := markSliceExecutionRoot(detail, sliceID, request.ExecutionRoot); err != nil {
					return nil, err
				}
			}
			event, appendEvent, err := markSliceStarted(detail, sliceID, request.StartedAt)
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
		return applyLifecycleMutation(detail, func(changes *artifactChangeSet) ([]Event, error) {
			event, appendEvent, err := markSliceCompletedWithOutcomeWithChanges(detail, changes, sliceID, notes, verificationResults, outcome, now)
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
		return applyLifecycleMutation(detail, func(_ *artifactChangeSet) ([]Event, error) {
			event, appendEvent, err := markSliceApproved(detail, sliceID, approvedBy, now)
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
		return applyLifecycleMutation(detail, func(_ *artifactChangeSet) ([]Event, error) {
			event, appendEvent, err := markSliceBlocked(detail, sliceID, reason, now)
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
		return applyLifecycleMutation(detail, func(_ *artifactChangeSet) ([]Event, error) {
			event, appendEvent, err := markSliceBudgetBlocked(detail, sliceID, reason, now)
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
		return applyLifecycleMutation(detail, func(changes *artifactChangeSet) ([]Event, error) {
			return nil, markBlockedContinuedWithChanges(detail, changes, now)
		})
	}
}

func blockedSliceRestartMutation(request BlockedSliceRestartRequest) artifactMutationFunc {
	return func(detail *PlanDetail) (lifecycleMutation, error) {
		return applyLifecycleMutation(detail, func(changes *artifactChangeSet) ([]Event, error) {
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
		return applyLifecycleMutation(detail, func(changes *artifactChangeSet) ([]Event, error) {
			event, err := markSliceRemovedWithChanges(detail, changes, sliceID, now)
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
		return applyLifecycleMutation(detail, func(changes *artifactChangeSet) ([]Event, error) {
			event, err := markSliceSkippedWithChanges(detail, changes, sliceID, now)
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
		return applyLifecycleMutation(detail, func(changes *artifactChangeSet) ([]Event, error) {
			event, err := markPendingSlicesReorderedWithChanges(detail, changes, pendingOrder, now)
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
		changes := newArtifactChangeSet(expected)
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
	if err := markBlockedContinuedWithChanges(expected, newArtifactChangeSet(expected), settled.State.UpdatedAt); err != nil {
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
func applyStateEventMutationWithRefresh(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, mutate func(*PlanDetail, *artifactChangeSet) ([]Event, error)) error {
	return applyStateEventMutationWithReview(store, planDir, detail, forceRefresh, nil, mutate)
}

// applyStateEventMutationWithReview includes review.md in the same refreshed,
// journaled mutation as its state and event metadata.
func applyStateEventMutationWithReview(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, reviewContent *string, mutate func(*PlanDetail, *artifactChangeSet) ([]Event, error)) error {
	return store.withMutationLock(planDir, func() error {
		return applyStateEventMutationLocked(store, planDir, detail, forceRefresh, reviewContent, mutate)
	})
}

// applyStateEventMutationLocked requires the store's mutation lock for planDir.
func applyStateEventMutationLocked(store artifactMutationStore, planDir string, detail *PlanDetail, forceRefresh bool, reviewContent *string, mutate func(*PlanDetail, *artifactChangeSet) ([]Event, error)) error {
	clone, _, err := artifactMutationWorkingDetailLocked(store, planDir, detail, forceRefresh, true)
	if err != nil {
		return err
	}
	baseline := clonePlanDetail(clone)
	changes := newArtifactChangeSet(clone)
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
func applyStateArtifactUpdate(store artifactMutationStore, planDir string, detail *PlanDetail, baseline, intended State, changes *artifactChangeSet) (*PlanDetail, error) {
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

func applySlicesArtifactUpdate(store artifactMutationStore, planDir string, detail *PlanDetail, mutate func(*PlanDetail, *artifactChangeSet) error) error {
	return store.withMutationLock(planDir, func() error {
		working, _, err := artifactMutationWorkingDetailLocked(store, planDir, detail, true, true)
		if err != nil {
			return err
		}
		baseline := cloneSlicesFile(working.Slices)
		changes := newArtifactChangeSet(working)
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
