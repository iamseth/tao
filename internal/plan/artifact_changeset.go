package plan

import (
	"encoding/json"
	"fmt"
)

// artifactChangeSet binds explicit clear-or-replace persistence intent to one
// plan detail. Its typed methods mutate the detail and retain the matching
// intent until payload preparation; callers cannot supply JSON paths.
type artifactChangeSet struct {
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

// newArtifactChangeSet creates an empty, preserve-only change set for detail.
func newArtifactChangeSet(detail *PlanDetail) *artifactChangeSet {
	return &artifactChangeSet{detail: detail}
}

// ClearWorkspaceDependencyFailure explicitly clears the persisted dependency
// preparation failure while preserving unrelated workspace fields.
func (c *artifactChangeSet) ClearWorkspaceDependencyFailure() {
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
func (c *artifactChangeSet) ClearWorkspaceDependencyFingerprint() {
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
func (c *artifactChangeSet) ClearWorkspaceRebaseIntent() {
	if c == nil {
		return
	}
	c.clearWorkspaceRebaseIntent = true
	if c.detail != nil && c.detail.State.Workspace != nil {
		c.detail.State.Workspace.RebaseIntent = nil
	}
}

// ClearSliceBlockerNote explicitly clears one slice's persisted blocker note.
func (c *artifactChangeSet) ClearSliceBlockerNote(sliceID string) error {
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
func (c *artifactChangeSet) ClearSliceExecutionBoundary(sliceID string) error {
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

func (c *artifactChangeSet) ClearPlanCurrentSlice() {
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
func (c *artifactChangeSet) ClearPlanFinalizationFailure() {
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
func (c *artifactChangeSet) ClearSingleMergeResolution() {
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
func (c *artifactChangeSet) ReplacePlanFinalizationFailure(failure FinalizationFailure) error {
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
func (c *artifactChangeSet) replaceFinalVerification(verification FinalVerification) error {
	if c == nil || c.detail == nil {
		return fmt.Errorf("plan detail is nil")
	}
	c.finalVerification = cloneFinalVerification(&verification)
	c.detail.State.Plan.FinalVerification = cloneFinalVerification(&verification)
	return nil
}

// ReplacePlanReview replaces every known persisted review field. Empty findings
// are normalized to [] so replacement cannot retain an older findings array.
func (c *artifactChangeSet) ReplacePlanReview(review PlanReview) error {
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
func (c *artifactChangeSet) ClearPlanReview() {
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
	changes *artifactChangeSet
}

func stateJSONChanges(changes *artifactChangeSet) artifactJSONChanges {
	return artifactJSONChanges{kind: artifactJSONState, changes: changes}
}

func slicesJSONChanges(changes *artifactChangeSet) artifactJSONChanges {
	return artifactJSONChanges{kind: artifactJSONSlices, changes: changes}
}

func (c *artifactChangeSet) applyState(state *State) {
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

func validateSlicesChangeDeclarations(baseline, intended SlicesFile, changes *artifactChangeSet) error {
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

func validateStateChangeDeclarations(baseline, intended State, changes *artifactChangeSet) error {
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

func validatePlanReviewChangeDeclaration(baseline, intended *PlanReview, changes *artifactChangeSet) error {
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

func lowerStateJSONChanges(root map[string]any, changes *artifactChangeSet) error {
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

func lowerSlicesJSONChanges(root map[string]any, changes *artifactChangeSet) error {
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
