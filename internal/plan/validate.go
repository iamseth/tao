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
)

// ValidateDetail reports artifact consistency warnings without rejecting loadable plans.
func ValidateDetail(detail *PlanDetail) []string {
	var warnings []string
	if detail.State.Plan.ID == "" {
		warnings = append(warnings, "state.json missing plan.id")
	}
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
