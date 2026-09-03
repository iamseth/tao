package plan

import (
	"maps"
	"time"
)

func clonePlanDetail(detail *PlanDetail) *PlanDetail {
	if detail == nil {
		return nil
	}
	clone := *detail
	clone.State = cloneState(detail.State)
	clone.Slices = cloneSlicesFile(detail.Slices)
	clone.Events = cloneEvents(detail.Events)
	clone.Warnings = cloneStringSlice(detail.Warnings)
	if detail.loadedStateBaseline != nil {
		baseline := cloneState(*detail.loadedStateBaseline)
		clone.loadedStateBaseline = &baseline
	}
	if detail.loadedSlicesBaseline != nil {
		baseline := cloneSlicesFile(*detail.loadedSlicesBaseline)
		clone.loadedSlicesBaseline = &baseline
	}
	return &clone
}

func cloneState(state State) State {
	clone := state
	clone.Plan = clonePlanState(state.Plan)
	clone.Workspace = cloneWorkspace(state.Workspace)
	clone.GlobalInvariants = cloneStringSlice(state.GlobalInvariants)
	clone.OpenQuestions = cloneStringSlice(state.OpenQuestions)
	return clone
}

func clonePlanState(plan PlanState) PlanState {
	clone := plan
	clone.Decision = cloneDecision(plan.Decision)
	clone.Sequence = cloneSequence(plan.Sequence)
	clone.RuntimePrerequisites = cloneRuntimePrerequisites(plan.RuntimePrerequisites)
	clone.CurrentSlice = cloneStringPtr(plan.CurrentSlice)
	clone.CompletedSlices = cloneStringSlice(plan.CompletedSlices)
	clone.PendingSlices = cloneStringSlice(plan.PendingSlices)
	clone.LastRunStartingDirty = cloneStringSlice(plan.LastRunStartingDirty)
	clone.Timing = clonePlanTiming(plan.Timing)
	clone.PullRequest = clonePullRequest(plan.PullRequest)
	clone.PullRequestIntent = clonePullRequest(plan.PullRequestIntent)
	clone.PRFeedbackTriage = clonePRFeedbackTriageResult(plan.PRFeedbackTriage)
	clone.PRFeedbackConsumedThreadIDs = cloneStringSlice(plan.PRFeedbackConsumedThreadIDs)
	clone.Review = clonePlanReview(plan.Review)
	clone.MergeCommitIntent = cloneSingleMergeCommitIntent(plan.MergeCommitIntent)
	clone.FinalVerification = cloneFinalVerification(plan.FinalVerification)
	clone.FinalizationFailure = cloneFinalizationFailure(plan.FinalizationFailure)
	return clone
}

func cloneDecision(decision *Decision) *Decision {
	if decision == nil {
		return nil
	}
	clone := *decision
	clone.SuccessCriteria = cloneStringSlice(decision.SuccessCriteria)
	return &clone
}

func cloneSequence(sequence *Sequence) *Sequence {
	if sequence == nil {
		return nil
	}
	clone := *sequence
	if sequence.Relationships != nil {
		clone.Relationships = append([]PlanRelation(nil), sequence.Relationships...)
	}
	return &clone
}

func cloneRuntimePrerequisites(prerequisites []RuntimePrerequisite) []RuntimePrerequisite {
	if prerequisites == nil {
		return nil
	}
	return append([]RuntimePrerequisite(nil), prerequisites...)
}

func clonePlanTiming(timing PlanTiming) PlanTiming {
	return PlanTiming{
		StartedAt:      cloneTimePtr(timing.StartedAt),
		CompletedAt:    cloneTimePtr(timing.CompletedAt),
		LastActivityAt: cloneTimePtr(timing.LastActivityAt),
	}
}

func cloneWorkspace(workspace *Workspace) *Workspace {
	if workspace == nil {
		return nil
	}
	clone := *workspace
	clone.Timing = WorkspaceTiming{
		CreatedAt:      cloneTimePtr(workspace.Timing.CreatedAt),
		PreparedAt:     cloneTimePtr(workspace.Timing.PreparedAt),
		LastActivityAt: cloneTimePtr(workspace.Timing.LastActivityAt),
		CleanedAt:      cloneTimePtr(workspace.Timing.CleanedAt),
	}
	clone.DependencyStartedAt = cloneTimePtr(workspace.DependencyStartedAt)
	clone.DependencyCompletedAt = cloneTimePtr(workspace.DependencyCompletedAt)
	if workspace.RebaseIntent != nil {
		intent := *workspace.RebaseIntent
		clone.RebaseIntent = &intent
	}
	return &clone
}

func clonePullRequest(pr *PullRequest) *PullRequest {
	if pr == nil {
		return nil
	}
	clone := *pr
	return &clone
}

func clonePRFeedbackTriageResult(result PRFeedbackTriageResult) PRFeedbackTriageResult {
	if result == nil {
		return nil
	}
	clone := make(PRFeedbackTriageResult, len(result))
	for threadID, entry := range result {
		clone[threadID] = entry
	}
	return clone
}

func cloneSingleMergeCommitIntent(intent *SingleMergeCommitIntent) *SingleMergeCommitIntent {
	if intent == nil {
		return nil
	}
	clone := *intent
	clone.Resolution = cloneSingleMergeResolution(intent.Resolution)
	return &clone
}

func cloneSingleMergeResolution(resolution *SingleMergeResolution) *SingleMergeResolution {
	if resolution == nil {
		return nil
	}
	clone := *resolution
	clone.ConflictFiles = cloneStringSlice(resolution.ConflictFiles)
	clone.ChangedPaths = cloneStringSlice(resolution.ChangedPaths)
	clone.Review = cloneSingleMergeResolutionReview(resolution.Review)
	return &clone
}

func cloneSingleMergeResolutionReview(review *SingleMergeResolutionReview) *SingleMergeResolutionReview {
	if review == nil {
		return nil
	}
	clone := *review
	clone.Findings = append([]ReviewFinding{}, review.Findings...)
	return &clone
}

func cloneSingleMergeResolutionEvent(event *SingleMergeResolutionEvent) *SingleMergeResolutionEvent {
	if event == nil {
		return nil
	}
	clone := *event
	clone.ConflictFiles = cloneStringSlice(event.ConflictFiles)
	clone.ChangedPaths = cloneStringSlice(event.ChangedPaths)
	clone.Review = cloneSingleMergeResolutionReview(event.Review)
	return &clone
}

func cloneFinalVerification(verification *FinalVerification) *FinalVerification {
	if verification == nil {
		return nil
	}
	clone := *verification
	clone.ExitCode = cloneIntPtr(verification.ExitCode)
	return &clone
}

func cloneFinalizationFailure(failure *FinalizationFailure) *FinalizationFailure {
	if failure == nil {
		return nil
	}
	clone := *failure
	return &clone
}

func clonePlanReview(review *PlanReview) *PlanReview {
	if review == nil {
		return nil
	}
	clone := *review
	if review.CommitMessage != nil {
		message := *review.CommitMessage
		clone.CommitMessage = &message
	}
	return &clone
}

func cloneSlicesFile(slices SlicesFile) SlicesFile {
	clone := slices
	clone.Slices = cloneSlices(slices.Slices)
	return clone
}

func cloneSlices(slices []Slice) []Slice {
	if slices == nil {
		return nil
	}
	clone := make([]Slice, len(slices))
	for i := range slices {
		clone[i] = cloneSlice(slices[i])
	}
	return clone
}

func cloneSlice(slice Slice) Slice {
	clone := slice
	clone.Tags = cloneStringSlice(slice.Tags)
	clone.DependsOn = cloneStringSlice(slice.DependsOn)
	clone.Timing = SliceTiming{
		CreatedAt:       slice.Timing.CreatedAt,
		StartedAt:       cloneTimePtr(slice.Timing.StartedAt),
		CompletedAt:     cloneTimePtr(slice.Timing.CompletedAt),
		UpdatedAt:       slice.Timing.UpdatedAt,
		LastActivityAt:  cloneTimePtr(slice.Timing.LastActivityAt),
		DurationSeconds: cloneInt64Ptr(slice.Timing.DurationSeconds),
	}
	clone.Tasks = cloneStringSlice(slice.Tasks)
	clone.ExpectedFiles = cloneStringSlice(slice.ExpectedFiles)
	clone.RequiredInputs = cloneRequiredInputs(slice.RequiredInputs)
	clone.Verification = cloneVerification(slice.Verification)
	clone.Approval = cloneApproval(slice.Approval)
	if slice.VerificationRepair != nil {
		binding := *slice.VerificationRepair
		clone.VerificationRepair = &binding
	}
	clone.VerificationResults = cloneVerificationRuns(slice.VerificationResults)
	clone.Extra = cloneMap(slice.Extra)
	return clone
}

func cloneRequiredInputs(inputs []RequiredInput) []RequiredInput {
	if inputs == nil {
		return nil
	}
	clone := make([]RequiredInput, len(inputs))
	copy(clone, inputs)
	return clone
}

func cloneVerification(verification Verification) Verification {
	clone := verification
	clone.Commands = cloneStringSlice(verification.Commands)
	clone.Steps = cloneVerificationSteps(verification.Steps)
	clone.ManualChecks = cloneStringSlice(verification.ManualChecks)
	return clone
}

func cloneVerificationSteps(steps []VerificationStep) []VerificationStep {
	if steps == nil {
		return nil
	}
	clone := make([]VerificationStep, len(steps))
	copy(clone, steps)
	return clone
}

func cloneApproval(approval *Approval) *Approval {
	if approval == nil {
		return nil
	}
	clone := *approval
	clone.ApprovedBy = cloneStringPtr(approval.ApprovedBy)
	clone.ApprovedAt = cloneStringPtr(approval.ApprovedAt)
	return &clone
}

func cloneVerificationRuns(runs []VerificationRun) []VerificationRun {
	if runs == nil {
		return nil
	}
	clone := make([]VerificationRun, len(runs))
	copy(clone, runs)
	return clone
}

func cloneEvents(events []Event) []Event {
	if events == nil {
		return nil
	}
	clone := make([]Event, len(events))
	for i := range events {
		clone[i] = cloneEvent(events[i])
	}
	return clone
}

func cloneEvent(event Event) Event {
	clone := event
	clone.DurationSeconds = cloneInt64Ptr(event.DurationSeconds)
	clone.ExitCode = cloneIntPtr(event.ExitCode)
	if event.Metrics != nil {
		metrics := *event.Metrics
		clone.Metrics = &metrics
	}
	clone.PullRequest = clonePullRequest(event.PullRequest)
	clone.PRFeedbackTriage = clonePRFeedbackTriageResult(event.PRFeedbackTriage)
	clone.Review = clonePlanReview(event.Review)
	clone.SingleMergeResolution = cloneSingleMergeResolutionEvent(event.SingleMergeResolution)
	clone.FinalizationFailure = cloneFinalizationFailure(event.FinalizationFailure)
	return clone
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	clone := make(map[string]any, len(values))
	maps.Copy(clone, values)
	return clone
}
