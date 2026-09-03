package plan

import (
	"fmt"
	"slices"
	"strings"
)

// requiredPlanningBriefSections names only navigation-level sections; detailed plan
// artifact schema remains documented in docs/plan-format.md.
var requiredPlanningBriefSections = []string{
	"User Goal",
	"Constraints",
	"Non-goals",
	"Expected Files/Packages",
	"Validation Strategy",
	"Open Questions",
}

const (
	largeSliceTaskThreshold         = 8
	largeSliceExpectedFileThreshold = 10
	maxRuntimePrerequisites         = 32
	maxRuntimePrerequisitePlanID    = 200
	maxRuntimePrerequisiteReason    = 1000
)

// ValidateDetail reports artifact consistency warnings without rejecting loadable plans.
func ValidateDetail(detail *PlanDetail) []string {
	var warnings []string
	if detail.State.Plan.ID == "" {
		warnings = append(warnings, "state.json missing plan.id")
	}
	if err := ValidateChangeType(detail.State.Plan.ChangeType); err != nil {
		warnings = append(warnings, "state.json plan.change_type is invalid: "+err.Error())
	}
	warnings = append(warnings, validateApprovedProposalType(detail.State.Plan)...)
	if verification := detail.State.Plan.FinalVerification; verification != nil && !validFinalVerificationFailureKind(verification.FailureKind) {
		warnings = append(warnings, "state.json plan.final_verification.failure_kind is invalid")
	}
	abandonmentEvents := 0
	for i, event := range detail.Events {
		if !validFinalVerificationFailureKind(event.FailureKind) {
			warnings = append(warnings, fmt.Sprintf("events.jsonl event %d failure_kind is invalid", i+1))
		}
		if event.Type == EventTypePlanAbandoned {
			abandonmentEvents++
			if err := ValidateAbandonmentReason(event.Reason); err != nil {
				warnings = append(warnings, fmt.Sprintf("events.jsonl event %d plan_abandoned reason is invalid: %v", i+1, err))
			}
			if event.Timestamp.IsZero() {
				warnings = append(warnings, fmt.Sprintf("events.jsonl event %d plan_abandoned timestamp is required", i+1))
			}
		}
	}
	if detail.State.Status == StatusAbandoned && abandonmentEvents == 0 {
		warnings = append(warnings, "state.json status is abandoned but events.jsonl has no plan_abandoned evidence")
	}
	if detail.State.Status != StatusAbandoned && abandonmentEvents > 0 {
		warnings = append(warnings, "events.jsonl has plan_abandoned evidence but state.json status is not abandoned")
	}
	if abandonmentEvents > 1 {
		warnings = append(warnings, "events.jsonl has multiple plan_abandoned events; the first remains authoritative")
	}
	if failure := detail.State.Plan.FinalizationFailure; failure != nil {
		if err := failure.Validate(); err != nil {
			warnings = append(warnings, "state.json plan.finalization_failure is invalid: "+err.Error())
		}
	}
	if intent := detail.State.Plan.MergeCommitIntent; intent != nil {
		if err := validateSingleMergeCommitIntent(*intent); err != nil {
			warnings = append(warnings, "state.json plan.merge_commit_intent is invalid: "+err.Error())
		}
	}
	warnings = append(warnings, validateDecision(detail.State.Plan.Decision)...)
	warnings = append(warnings, validateSequence(detail.State.Plan.ID, detail.State.Plan.Sequence)...)
	warnings = append(warnings, validateRuntimePrerequisites(detail.State.Plan.ID, detail.State.Plan.RuntimePrerequisites)...)
	if detail.Slices.PlanID != "" && detail.State.Plan.ID != "" && detail.Slices.PlanID != detail.State.Plan.ID {
		warnings = append(warnings, "state.json plan.id does not match slices.json plan_id")
	}

	index := newDetailIndex(detail)
	warnings = append(warnings, validateSliceReferences(detail, index)...)
	warnings = append(warnings, validatePlanningBrief(detail)...)
	warnings = append(warnings, validateWorkspace(detail.State.Workspace)...)

	if detail.State.Plan.CurrentSlice != nil {
		current := index.slice(*detail.State.Plan.CurrentSlice)
		switch {
		case current == nil:
			warnings = append(warnings, "state.json current_slice does not exist in slices.json")
		case current.Status == StatusCompleted && len(detail.State.Plan.PendingSlices) > 0:
			warnings = append(warnings, "state.json current_slice references a completed slice while pending_slices is not empty; recovering to first pending slice")
		case current.Status == StatusBlocked && !slices.Contains(detail.State.Plan.PendingSlices, current.ID):
			warnings = append(warnings, fmt.Sprintf("state.json current_slice references blocked slice %s missing from pending_slices", current.ID))
		case current.Status != StatusPending && current.Status != StatusInProgress && current.Status != StatusBlocked:
			warnings = append(warnings, fmt.Sprintf("state.json current_slice references %s slice %s", current.Status, current.ID))
		}
	}
	if detail.State.Status != StatusCompleted && len(detail.State.Plan.PendingSlices) == 0 && (detail.State.Status == StatusInProgress || detail.State.Plan.CurrentSlice != nil) {
		warnings = append(warnings, "state.json has active lifecycle metadata but pending_slices is empty; plan remains active but is not runnable")
	}
	if index.inProgressCount > 0 && detail.State.Plan.CurrentSlice == nil {
		warnings = append(warnings, "slice is in_progress but state.json current_slice is null")
	}
	if index.inProgressCount > 1 {
		warnings = append(warnings, "multiple slices are in_progress")
	}
	warnings = append(warnings, validatePendingOrder(detail, index)...)
	return warnings
}

func validateWorkspace(workspace *Workspace) []string {
	if workspace == nil {
		return nil
	}
	var warnings []string
	if workspace.Strategy != WorkspaceStrategyWorktree && workspace.Strategy != WorkspaceStrategyCurrent {
		warnings = append(warnings, fmt.Sprintf("state.json workspace.strategy must be %q or %q", WorkspaceStrategyWorktree, WorkspaceStrategyCurrent))
	}
	if workspace.LifecycleStatus != "" && !validValue(workspace.LifecycleStatus, []string{WorkspaceStatusPending, WorkspaceStatusPreparing, WorkspaceStatusReady, WorkspaceStatusFailed, WorkspaceStatusCleaning, WorkspaceStatusCleaned, WorkspaceStatusCleanupHeld}) {
		warnings = append(warnings, "state.json workspace.lifecycle_status is invalid")
	}
	if workspace.DependencyPreparation != "" && !validValue(workspace.DependencyPreparation, []string{DependencyPreparationStatusPending, DependencyPreparationStatusRunning, DependencyPreparationStatusReady, DependencyPreparationStatusFailed, DependencyPreparationStatusSkipped}) {
		warnings = append(warnings, "state.json workspace.dependency_preparation_status is invalid")
	}
	if workspace.CleanupStatus != "" && !validValue(workspace.CleanupStatus, []string{WorkspaceCleanupStatusPending, WorkspaceCleanupStatusHeld, WorkspaceCleanupStatusRunning, WorkspaceCleanupStatusDone, WorkspaceCleanupStatusFailed}) {
		warnings = append(warnings, "state.json workspace.cleanup_status is invalid")
	}
	if workspace.BaseStatus != "" && !validValue(workspace.BaseStatus, []string{WorkspaceBaseStatusUnknown, WorkspaceBaseStatusCurrent, WorkspaceBaseStatusStale}) {
		warnings = append(warnings, "state.json workspace.base_status is invalid")
	}
	if workspace.RefreshStatus != "" && !validValue(workspace.RefreshStatus, []string{WorkspaceRefreshStatusUnknown, WorkspaceRefreshStatusNotNeeded, WorkspaceRefreshStatusNeeded}) {
		warnings = append(warnings, "state.json workspace.refresh_status is invalid")
	}
	if workspace.RebaseStatus != "" && !validValue(workspace.RebaseStatus, []string{WorkspaceRebaseStatusUnknown, WorkspaceRebaseStatusNotNeeded, WorkspaceRebaseStatusNeeded}) {
		warnings = append(warnings, "state.json workspace.rebase_status is invalid")
	}
	return warnings
}

func validValue(value string, allowed []string) bool {
	return slices.Contains(allowed, value)
}

func validFinalVerificationFailureKind(kind FinalVerificationFailureKind) bool {
	switch kind {
	case "", FinalVerificationFailureKindCode, FinalVerificationFailureKindToolMissing, FinalVerificationFailureKindTimeout, FinalVerificationFailureKindCancelled, FinalVerificationFailureKindInvalidCommand:
		return true
	default:
		return false
	}
}

func validateApprovedProposalType(state PlanState) []string {
	review := state.Review
	if state.ChangeType == "" || review == nil || !review.IsApproved() || review.CommitMessage == nil {
		return nil
	}
	subject := strings.TrimSpace(review.CommitMessage.Subject)
	open := strings.IndexByte(subject, '(')
	colon := strings.Index(subject, "): ")
	if open <= 0 || colon <= open {
		return nil
	}
	observed := subject[:open]
	if observed == string(state.ChangeType) {
		return nil
	}
	return []string{fmt.Sprintf("state.json plan.review.commit_message type mismatch: expected %q, observed %q", state.ChangeType, observed)}
}

func validateDecision(decision *Decision) []string {
	if decision == nil {
		return nil
	}
	var warnings []string
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "problem", value: decision.Problem},
		{name: "why_now", value: decision.WhyNow},
		{name: "expected_benefit", value: decision.ExpectedBenefit},
		{name: "disposition_reason", value: decision.DispositionReason},
		{name: "priority.rationale", value: decision.Priority.Rationale},
	} {
		if strings.TrimSpace(field.value) == "" {
			warnings = append(warnings, "state.json plan.decision."+field.name+" is required")
		}
	}
	if !validDecisionReadiness(decision.Readiness) {
		warnings = append(warnings, "state.json plan.decision.readiness is invalid")
	}
	if !validDecisionDisposition(decision.Disposition) {
		warnings = append(warnings, "state.json plan.decision.disposition is invalid")
	}
	if len(decision.SuccessCriteria) == 0 {
		warnings = append(warnings, "state.json plan.decision.success_criteria must contain at least one criterion")
	} else {
		for i, criterion := range decision.SuccessCriteria {
			if strings.TrimSpace(criterion) == "" {
				warnings = append(warnings, fmt.Sprintf("state.json plan.decision.success_criteria[%d] is required", i))
			}
		}
	}
	if !validPriorityOverallLevel(decision.Priority.Level) {
		warnings = append(warnings, "state.json plan.decision.priority.level is invalid")
	}
	for _, field := range []struct {
		name  string
		value PriorityLevel
	}{
		{name: "impact", value: decision.Priority.Impact},
		{name: "urgency", value: decision.Priority.Urgency},
		{name: "risk", value: decision.Priority.Risk},
		{name: "confidence", value: decision.Priority.Confidence},
	} {
		if !validPriorityLevel(field.value) {
			warnings = append(warnings, "state.json plan.decision.priority."+field.name+" is invalid")
		}
	}
	if !validPriorityEffort(decision.Priority.Effort) {
		warnings = append(warnings, "state.json plan.decision.priority.effort is invalid")
	}
	return warnings
}

func validateSequence(planID string, sequence *Sequence) []string {
	if sequence == nil {
		return nil
	}
	var warnings []string
	if sequence.Position < 1 {
		warnings = append(warnings, "state.json plan.sequence.position must be at least 1")
	}
	if sequence.Total < 1 {
		warnings = append(warnings, "state.json plan.sequence.total must be at least 1")
	} else if sequence.Position > sequence.Total {
		warnings = append(warnings, "state.json plan.sequence.position cannot exceed total")
	}
	seen := make(map[string]struct{}, len(sequence.Relationships))
	for i, relationship := range sequence.Relationships {
		path := fmt.Sprintf("state.json plan.sequence.relationships[%d]", i)
		target := strings.TrimSpace(relationship.PlanID)
		if target == "" {
			warnings = append(warnings, path+".plan_id is required")
		} else {
			if target == strings.TrimSpace(planID) && target != "" {
				warnings = append(warnings, path+" cannot reference its own plan")
			}
			if _, duplicate := seen[target]; duplicate {
				warnings = append(warnings, path+" duplicates relationship to plan "+target)
			}
			seen[target] = struct{}{}
		}
		if !validPlanRelationType(relationship.Type) {
			warnings = append(warnings, path+".type is invalid")
		}
		if strings.TrimSpace(relationship.Reason) == "" {
			warnings = append(warnings, path+".reason is required")
		}
	}
	return warnings
}

func validateRuntimePrerequisites(planID string, prerequisites []RuntimePrerequisite) []string {
	var warnings []string
	if len(prerequisites) > maxRuntimePrerequisites {
		warnings = append(warnings, fmt.Sprintf("state.json plan.runtime_prerequisites must contain at most %d entries", maxRuntimePrerequisites))
	}
	seen := make(map[string]struct{}, len(prerequisites))
	for i, prerequisite := range prerequisites {
		path := fmt.Sprintf("state.json plan.runtime_prerequisites[%d]", i)
		target := strings.TrimSpace(prerequisite.PlanID)
		switch {
		case target == "":
			warnings = append(warnings, path+".plan_id is required")
		case target != prerequisite.PlanID:
			warnings = append(warnings, path+".plan_id must not have surrounding whitespace")
		case len(target) > maxRuntimePrerequisitePlanID:
			warnings = append(warnings, fmt.Sprintf("%s.plan_id must be at most %d bytes", path, maxRuntimePrerequisitePlanID))
		case target == "." || target == ".." || strings.ContainsAny(target, `/\\`):
			warnings = append(warnings, path+".plan_id must be an exact plan ID, not a path")
		case target == strings.TrimSpace(planID):
			warnings = append(warnings, path+" cannot reference its own plan")
		}
		if target != "" {
			if _, duplicate := seen[target]; duplicate {
				warnings = append(warnings, path+" duplicates prerequisite plan "+target)
			}
			seen[target] = struct{}{}
		}
		reason := strings.TrimSpace(prerequisite.Reason)
		if reason == "" {
			warnings = append(warnings, path+".reason is required")
		} else if len(prerequisite.Reason) > maxRuntimePrerequisiteReason {
			warnings = append(warnings, fmt.Sprintf("%s.reason must be at most %d bytes", path, maxRuntimePrerequisiteReason))
		}
	}
	return warnings
}

// validateRuntimePrerequisiteCycle follows only prerequisites that can be
// resolved in the same repository. Missing plans remain a runtime gate concern.
func validateRuntimePrerequisiteCycle(planID string, prerequisites []RuntimePrerequisite, resolve func(string) ([]RuntimePrerequisite, bool)) []string {
	visiting := map[string]bool{planID: true}
	visited := make(map[string]bool)
	var walk func([]RuntimePrerequisite) bool
	walk = func(edges []RuntimePrerequisite) bool {
		for _, edge := range edges {
			target := edge.PlanID
			if target == "" {
				continue
			}
			if visiting[target] {
				return true
			}
			if visited[target] {
				continue
			}
			next, ok := resolve(target)
			if !ok {
				continue
			}
			visiting[target] = true
			if walk(next) {
				return true
			}
			delete(visiting, target)
			visited[target] = true
		}
		return false
	}
	if walk(prerequisites) {
		return []string{"state.json plan.runtime_prerequisites contains a resolvable cycle"}
	}
	return nil
}

func validDecisionReadiness(value DecisionReadiness) bool {
	return value == DecisionReadinessReady || value == DecisionReadinessNeedsRefinement || value == DecisionReadinessBlocked
}

func validDecisionDisposition(value DecisionDisposition) bool {
	return value == DecisionDispositionReady || value == DecisionDispositionConditional || value == DecisionDispositionDeferred || value == DecisionDispositionObsolete
}

func validPriorityOverallLevel(value PriorityOverallLevel) bool {
	return value == PriorityOverallLevelMust || value == PriorityOverallLevelShould || value == PriorityOverallLevelCould
}

func validPriorityLevel(value PriorityLevel) bool {
	return value == PriorityLevelLow || value == PriorityLevelMedium || value == PriorityLevelHigh
}

func validPriorityEffort(value PriorityEffort) bool {
	return value == PriorityEffortSmall || value == PriorityEffortMedium || value == PriorityEffortLarge
}

func validPlanRelationType(value PlanRelationType) bool {
	return value == PlanRelationBefore || value == PlanRelationAfter || value == PlanRelationRelated
}

func validatePlanningBrief(detail *PlanDetail) []string {
	if detail.PlanningBrief.Content == "" {
		return []string{"planning-brief.md missing; new plans should include a concise planning brief"}
	}
	var warnings []string
	for _, section := range requiredPlanningBriefSections {
		if !containsMarkdownHeading(detail.PlanningBrief.Content, section) {
			warnings = append(warnings, fmt.Sprintf("planning-brief.md missing section %q", section))
		}
	}
	return warnings
}

func validateSliceReferences(detail *PlanDetail, index detailIndex) []string {
	var warnings []string
	seenSlices := make(map[string]bool, len(detail.Slices.Slices))
	for _, slice := range detail.Slices.Slices {
		if slice.ID == "" {
			warnings = append(warnings, "slices.json contains slice with empty id")
			continue
		}
		if seenSlices[slice.ID] {
			warnings = append(warnings, fmt.Sprintf("slices.json contains duplicate slice id %s", slice.ID))
		}
		seenSlices[slice.ID] = true
		for _, dependency := range slice.DependsOn {
			if index.slice(dependency) == nil {
				warnings = append(warnings, fmt.Sprintf("slice %s depends on missing slice %s", slice.ID, dependency))
			}
		}
	}
	for _, id := range detail.State.Plan.CompletedSlices {
		if slice := index.slice(id); slice == nil {
			warnings = append(warnings, fmt.Sprintf("state.json completed_slices references missing slice %s", id))
		} else if slice.Status != StatusCompleted {
			warnings = append(warnings, fmt.Sprintf("state.json completed_slices references %s slice %s", slice.Status, id))
		}
	}
	for _, id := range detail.State.Plan.PendingSlices {
		if index.slice(id) == nil {
			warnings = append(warnings, fmt.Sprintf("state.json pending_slices references missing slice %s", id))
		}
	}
	return warnings
}

func validatePendingOrder(detail *PlanDetail, index detailIndex) []string {
	var warnings []string
	position := make(map[string]int, len(detail.State.Plan.PendingSlices))
	for i, id := range detail.State.Plan.PendingSlices {
		if _, exists := position[id]; exists {
			warnings = append(warnings, fmt.Sprintf("state.json pending_slices contains duplicate slice %s", id))
		}
		position[id] = i
	}
	for i, id := range detail.State.Plan.PendingSlices {
		slice := index.slice(id)
		if slice == nil {
			continue
		}
		isCurrentActive := detail.State.Plan.CurrentSlice != nil && id == *detail.State.Plan.CurrentSlice && (slice.Status == StatusInProgress || slice.Status == StatusBlocked)
		if slice.Status != StatusPending && !isCurrentActive {
			warnings = append(warnings, fmt.Sprintf("state.json pending_slices references %s slice %s", slice.Status, id))
		}
		for _, dependency := range slice.DependsOn {
			dependencyPosition, dependencyPending := position[dependency]
			if dependencyPending && dependencyPosition > i {
				warnings = append(warnings, fmt.Sprintf("state.json pending_slices orders slice %s before dependency %s", id, dependency))
			}
		}
	}
	return warnings
}

// Verification guardrails live here because they compare command choices with plan-wide
// lifecycle and artifact expectations rather than parsing individual command syntax.
func validatePlanGuardrails(detail *PlanDetail) []VerificationFinding {
	findings := make([]VerificationFinding, 0)
	for _, slice := range detail.Slices.Slices {
		findings = append(findings, validateSliceGuardrails(slice, false)...)
	}
	return findings
}

func validateSelectedSliceGuardrails(detail *PlanDetail) []VerificationFinding {
	lifecycle := AnalyzeLifecycle(detail)
	if lifecycle.NextSlice == nil {
		return nil
	}
	return validateSliceGuardrails(*lifecycle.NextSlice, true)
}

func validateSliceGuardrails(slice Slice, selected bool) []VerificationFinding {
	findings := make([]VerificationFinding, 0)
	if len(slice.Tasks) > largeSliceTaskThreshold {
		findings = append(findings, guardrailFinding(slice.ID, "slice_task_count", fmt.Sprintf("slice has %d tasks; consider splitting if implementation feels broad", len(slice.Tasks))))
	}
	if len(slice.ExpectedFiles) > largeSliceExpectedFileThreshold {
		findings = append(findings, guardrailFinding(slice.ID, "slice_expected_file_count", fmt.Sprintf("slice lists %d expected files; consider splitting if changes are not tightly coupled", len(slice.ExpectedFiles))))
	}
	for _, path := range slice.ExpectedFiles {
		if reason := unsafeExpectedFile(path); reason != "" {
			findings = append(findings, guardrailFinding(slice.ID, "slice_expected_file_unsafe", fmt.Sprintf("slice expected file %q is unsafe (%s); use repo-relative concrete paths", path, reason)))
		}
		if vagueExpectedFile(path) {
			findings = append(findings, guardrailFinding(slice.ID, "slice_expected_file_vague", fmt.Sprintf("slice expected file %q is broad or vague; prefer concrete files before implementation", path)))
		}
	}
	if !hasConcreteVerificationCommand(slice.Verification.Commands) {
		finding := guardrailFinding(slice.ID, "slice_verification_missing", "slice has no non-blank verification commands; add the narrowest documented command before implementation")
		if selected {
			finding.Severity = VerificationFindingError
		}
		findings = append(findings, finding)
	}
	for _, command := range slice.Verification.Commands {
		if broadVerificationCommand(command) {
			findings = append(findings, VerificationFinding{
				Severity: VerificationFindingWarning,
				SliceID:  slice.ID,
				Code:     "slice_verification_broad",
				Message:  "slice verification command is broad; prefer focused verification when possible before implementation",
				Command:  command,
			})
		}
	}
	return findings
}

func hasConcreteVerificationCommand(commands []string) bool {
	for _, command := range commands {
		if strings.TrimSpace(command) != "" {
			return true
		}
	}
	return false
}

func guardrailFinding(sliceID string, code string, message string) VerificationFinding {
	return VerificationFinding{Severity: VerificationFindingWarning, SliceID: sliceID, Code: code, Message: message}
}

func unsafeExpectedFile(path string) string {
	value := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/") || expectedFileHasWindowsDrivePrefix(value) {
		return "absolute path"
	}
	if slices.Contains(strings.Split(value, "/"), "..") {
		return "parent traversal"
	}
	return ""
}

func expectedFileHasWindowsDrivePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	first := value[0]
	return (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')
}

func vagueExpectedFile(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == "." || trimmed == "./..." || trimmed == "*" || trimmed == "**" || trimmed == "..." {
		return true
	}
	return strings.Contains(trimmed, "*") || strings.HasSuffix(trimmed, "/...") || strings.HasSuffix(trimmed, "/")
}

func broadVerificationCommand(command string) bool {
	tokens := verificationCommandFields(command)
	if len(tokens) == 0 {
		return false
	}
	if len(tokens) >= 2 && tokens[0] == "go" && tokens[1] == "test" {
		for _, token := range tokens[2:] {
			if token == "./..." || strings.HasSuffix(token, "/...") {
				return true
			}
		}
	}
	if len(tokens) == 2 && tokens[0] == "make" && (tokens[1] == "test" || tokens[1] == "lint") {
		return true
	}
	if len(tokens) >= 2 && (tokens[0] == "npm" || tokens[0] == "pnpm" || tokens[0] == "yarn") && tokens[1] == "test" {
		return true
	}
	if len(tokens) >= 3 && (tokens[0] == "npm" || tokens[0] == "pnpm" || tokens[0] == "yarn") && tokens[1] == "run" && broadTestScript(tokens[2]) {
		return true
	}
	return false
}

func broadTestScript(script string) bool {
	return script == "test" || script == "test:ci" || script == "test:all"
}
