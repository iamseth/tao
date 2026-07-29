package plan

import (
	"fmt"
	"strings"
	"time"
)

// Run packet rendering is intentionally a compact read-only projection for agents;
// fallback artifacts remain available when this context is stale or insufficient.
const (
	defaultRunPacketRecentEvents     = 5
	maxRunPacketFeedbackLines        = 12
	maxRunPacketFeedbackBytes        = 4 * 1024
	maxRunPacketRecentFailureSignals = 5
)

// RunPacketOptions controls optional context included in the selected-slice packet.
type RunPacketOptions struct {
	CommitPolicy     string
	ExecutionMode    string
	WorkingRoot      string
	RecentEvents     int
	Resuming         bool
	ResumeAttempt    int
	BudgetThresholds *AgentBudgetThresholds
}

// RenderRunPacket renders deterministic Markdown context for the selected runnable slice.
func RenderRunPacket(detail *PlanDetail, options RunPacketOptions) (string, error) {
	derived := Derive(detail, time.Time{})
	slice := derived.NextSlice
	if slice == nil {
		if derived.RunnableError == nil {
			return "", fmt.Errorf("no selected slice")
		}
		return "", fmt.Errorf("no selected slice: %w", derived.RunnableError)
	}
	commitPolicy := options.CommitPolicy
	if commitPolicy == "" {
		commitPolicy = "provided by run command"
	}
	executionMode := options.ExecutionMode
	if executionMode == "" {
		executionMode = "isolated"
	}
	eventLimit := options.RecentEvents
	if eventLimit <= 0 {
		eventLimit = defaultRunPacketRecentEvents
	}

	var b strings.Builder
	b.WriteString("# Tao Run Packet\n\n")
	writeRunPacketSection(&b, "Plan")
	writeRunPacketLine(&b, "ID", detail.State.Plan.ID)
	writeRunPacketLine(&b, "Title", detail.State.Plan.Title)
	writeRunPacketLine(&b, "Status", detail.State.Status)
	if options.WorkingRoot != "" {
		writeRunPacketLine(&b, "Workspace Root", options.WorkingRoot)
		writeRunPacketLine(&b, "Control Repo Root (do not touch)", detail.State.Repo.Root)
	} else {
		writeRunPacketLine(&b, "Repo Root", detail.State.Repo.Root)
	}
	writeRunPacketLine(&b, "Repo Branch", detail.State.Repo.Branch)
	writeRunPacketLine(&b, "Current Slice", derived.CurrentSliceID)
	writeRunPacketLine(&b, "Next Slice", derived.NextSliceID)
	writeRunPacketLine(&b, "Pending Slices", strings.Join(detail.State.Plan.PendingSlices, ", "))
	writeRunPacketLine(&b, "Commit Policy", commitPolicy)
	writeRunPacketLine(&b, "Execution Mode", executionMode)

	if options.Resuming {
		writeResumeContext(&b, options.ResumeAttempt)
	}

	writeRunPacketSection(&b, "Selected Slice")
	writeRunPacketLine(&b, "ID", slice.ID)
	writeRunPacketLine(&b, "Title", slice.Title)
	writeRunPacketLine(&b, "Status", slice.Status)
	writeRunPacketLine(&b, "Approval", approvalStatus(slice.Approval))
	writeRunPacketLine(&b, "Goal", slice.Goal)
	writeRunPacketLine(&b, "Context", slice.Context)

	writeRunPacketList(&b, "Tasks", slice.Tasks)
	writeRequiredInputs(&b, slice.RequiredInputs)
	writeRunPacketList(&b, "Expected Files", slice.ExpectedFiles)
	writeDependencyStatus(&b, detail, slice)
	writeRunPacketList(&b, "Global Invariants", detail.State.GlobalInvariants)
	writeRunPacketList(&b, "Open Questions", detail.State.OpenQuestions)

	writeRunPacketSection(&b, "Verification")
	writeRunPacketLine(&b, "Source", slice.Verification.Source)
	writeRunPacketList(&b, "Commands", slice.Verification.Commands)
	writeRunPacketList(&b, "Manual Checks", slice.Verification.ManualChecks)

	writePriorCompletions(&b, detail)
	writeTelemetryFeedback(&b, detail, slice.ID, options.BudgetThresholds)
	writeRecentEvents(&b, detail.Events, slice.ID, eventLimit)
	writePlanningBrief(&b, detail.PlanningBrief.Content)
	writeRunPacketList(&b, "Fallback Read Guidance", []string{"Use this packet first; read full fallback artifacts only with a concrete reason such as stale packet data, blockers, verification failure diagnosis, or insufficient context."})
	writeRunPacketList(&b, "Fallback Files", []string{"planning-brief.md", "plan.md", "state.json", "slices.json", "handoff.md", "events.jsonl"})

	return b.String(), nil
}

func writeResumeContext(b *strings.Builder, attempt int) {
	writeRunPacketSection(b, "Interrupted Slice Resume")
	if attempt > 0 {
		writeRunPacketLine(b, "Resume Attempt", fmt.Sprintf("%d", attempt))
	}
	writeRunPacketList(b, "Resume Instructions", []string{
		"Inspect staged, unstaged, and untracked work before editing.",
		"Continue or correct the preserved work; do not discard it or restart the implementation.",
		"Rerun every declared verification command, then call tao slice-complete.",
		"Never commit manually; tao slice-complete owns automatic-policy commits.",
	})
}

func approvalStatus(approval *Approval) string {
	if approval == nil || !approval.Required {
		return "not required"
	}
	status := "required, not approved"
	if approval.Approved {
		status = "required, approved"
	}
	if approval.Reason != "" {
		status += ": " + approval.Reason
	}
	return status
}

func writeRunPacketSection(b *strings.Builder, title string) {
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteString("\n")
}

func writeRunPacketLine(b *strings.Builder, label, value string) {
	if value == "" {
		value = "none"
	}
	b.WriteString("- ")
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\n")
}

func writeRunPacketList(b *strings.Builder, title string, values []string) {
	writeRunPacketSection(b, title)
	if len(values) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, value := range values {
		b.WriteString("- ")
		b.WriteString(value)
		b.WriteString("\n")
	}
}

func writeRequiredInputs(b *strings.Builder, inputs []RequiredInput) {
	writeRunPacketSection(b, "Required Inputs")
	if len(inputs) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, input := range inputs {
		fmt.Fprintf(b, "- %s (%s): %s\n", input.Path, input.Kind, input.Reason)
	}
}

func writeDependencyStatus(b *strings.Builder, detail *PlanDetail, slice *Slice) {
	writeRunPacketSection(b, "Dependencies")
	if len(slice.DependsOn) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, dependency := range slice.DependsOn {
		status := "missing"
		if SliceCompleted(detail, dependency) {
			status = "completed"
		}
		b.WriteString("- ")
		b.WriteString(dependency)
		b.WriteString(": ")
		b.WriteString(status)
		b.WriteString("\n")
	}
}

// Packet sections below keep formatting deterministic and omit schema-level detail.
func writePriorCompletions(b *strings.Builder, detail *PlanDetail) {
	writeRunPacketSection(b, "Prior Completions")
	completed := detail.State.Plan.CompletedSlices
	if len(completed) == 0 {
		b.WriteString("- none\n")
		return
	}
	const limit = 3
	if len(completed) > limit {
		completed = completed[len(completed)-limit:]
	}
	for _, id := range completed {
		slice := findRunPacketSlice(detail, id)
		b.WriteString("- ")
		b.WriteString(id)
		if slice == nil {
			b.WriteString(": completed")
			b.WriteString("\n")
			continue
		}
		if slice.Title != "" {
			b.WriteString(": ")
			b.WriteString(slice.Title)
		}
		notes := strings.TrimSpace(slice.Notes)
		if notes != "" {
			b.WriteString(" - ")
			b.WriteString(firstLine(notes))
		}
		b.WriteString("\n")
	}
}

func findRunPacketSlice(detail *PlanDetail, id string) *Slice {
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == id {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}

func firstLine(value string) string {
	if before, _, ok := strings.Cut(value, "\n"); ok {
		return strings.TrimSpace(before)
	}
	return value
}

func writeTelemetryFeedback(b *strings.Builder, detail *PlanDetail, sliceID string, thresholds *AgentBudgetThresholds) {
	writeRunPacketSection(b, "Telemetry Feedback")
	resolvedThresholds := DefaultAgentBudgetThresholds()
	if thresholds != nil {
		resolvedThresholds = *thresholds
	}

	lines := make([]string, 0, maxRunPacketFeedbackLines)
	for _, warning := range AgentBudgetWarnings(detail, resolvedThresholds) {
		if warning.Scope != "plan" && warning.SliceID != sliceID {
			continue
		}
		scope := warning.Scope
		if warning.SliceID != "" {
			scope += " " + warning.SliceID
		}
		lines = append(lines, fmt.Sprintf("Budget warning (%s): %s observed %g > threshold %g", scope, warning.Metric, warning.Observed, warning.Threshold))
	}

	rounds := 0
	var latestStop *Event
	for i := range detail.Events {
		event := &detail.Events[i]
		if !runPacketPlanEvent(*event, detail.State.Plan.ID) || (event.SliceID != "" && event.SliceID != sliceID) {
			continue
		}
		switch event.Type {
		case EventTypeReworkRound:
			if event.Round > rounds {
				rounds = event.Round
			} else if event.Round == 0 {
				rounds++
			}
		case EventTypeReworkStopped:
			latestStop = event
		}
	}
	if rounds > 0 {
		lines = append(lines, fmt.Sprintf("Rework rounds: %d", rounds))
	}
	if latestStop != nil {
		reason := firstNonemptyLine(latestStop.Reason, latestStop.Message, "reason not recorded")
		lines = append(lines, "Latest rework stop: "+reason)
	}

	failures := make([]string, 0, maxRunPacketRecentFailureSignals)
	for _, event := range detail.Events {
		if !runPacketPlanEvent(event, detail.State.Plan.ID) {
			continue
		}
		if message, ok := runPacketFailureSignal(event); ok {
			failures = append(failures, fmt.Sprintf("Failure %s %s: %s", event.Timestamp.Format(time.RFC3339Nano), event.Type, message))
		}
	}
	if len(failures) > maxRunPacketRecentFailureSignals {
		failures = failures[len(failures)-maxRunPacketRecentFailureSignals:]
	}
	lines = append(lines, failures...)

	if len(lines) == 0 {
		b.WriteString("- none\n")
		return
	}
	if len(lines) > maxRunPacketFeedbackLines {
		lines = lines[:maxRunPacketFeedbackLines]
	}
	remaining := maxRunPacketFeedbackBytes
	for _, line := range lines {
		const framingBytes = len("- ") + len("\n")
		if remaining <= framingBytes {
			break
		}
		line = firstLine(line)
		truncated := len(line) > remaining-framingBytes
		line = boundedRunPacketFeedbackText(line, remaining-framingBytes)
		if line == "" {
			break
		}
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
		remaining -= framingBytes + len(line)
		if remaining == 0 || truncated {
			break
		}
	}
}

func boundedRunPacketFeedbackText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "…"
	if maxBytes < len(suffix) {
		return ""
	}
	limit := maxBytes - len(suffix)
	for limit > 0 && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit] + suffix
}

func runPacketPlanEvent(event Event, planID string) bool {
	return event.PlanID == "" || event.PlanID == planID
}

func runPacketFailureSignal(event Event) (string, bool) {
	switch event.Type {
	case EventTypeSessionTimeout:
		return firstNonemptyLine(event.Message, event.Reason, "agent session timed out"), true
	case EventTypeSliceBlocked:
		return firstNonemptyLine(event.Reason, event.Message, "slice blocked"), true
	case EventTypeVerificationCommandInvalid:
		return firstNonemptyLine(event.Reason, event.Message, event.Command, "verification command invalid"), true
	case EventTypeSliceResumeFailed:
		return firstNonemptyLine(event.Reason, event.Message, "slice resume failed"), true
	case EventTypeAgentMetrics, "opencode_metrics":
		if event.Metrics == nil || (event.Metrics.Status != "failed" && event.Metrics.Result != "failed") {
			return "", false
		}
		session := event.Metrics.SessionID
		if session == "" {
			session = "unknown"
		}
		return fmt.Sprintf("agent session %s failed (output_tokens=%d cost=%g tool_calls=%d errored_messages=%d)", session, event.Metrics.OutputTokens, event.Metrics.Cost, event.Metrics.ToolCalls, event.Metrics.ErroredMessages), true
	default:
		return "", false
	}
}

func firstNonemptyLine(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(firstLine(value)); value != "" {
			return value
		}
	}
	return ""
}

func writeRecentEvents(b *strings.Builder, events []Event, sliceID string, limit int) {
	writeRunPacketSection(b, "Recent Relevant Events")
	relevant := make([]Event, 0, len(events))
	for _, event := range events {
		if runPacketTelemetryEvent(event.Type) {
			continue
		}
		if event.SliceID == "" || event.SliceID == sliceID {
			relevant = append(relevant, event)
		}
	}
	if len(relevant) == 0 {
		b.WriteString("- none\n")
		return
	}
	if len(relevant) > limit {
		relevant = relevant[len(relevant)-limit:]
	}
	for _, event := range relevant {
		message := event.Message
		if message == "" {
			message = event.Type
		}
		b.WriteString("- ")
		b.WriteString(event.Timestamp.Format(time.RFC3339Nano))
		b.WriteString(" ")
		b.WriteString(event.Type)
		if event.SliceID != "" {
			b.WriteString(" ")
			b.WriteString(event.SliceID)
		}
		b.WriteString(": ")
		b.WriteString(message)
		b.WriteString("\n")
	}
}

func runPacketTelemetryEvent(eventType string) bool {
	switch eventType {
	case EventTypeAgentMetrics, "opencode_metrics", EventTypeRunContext:
		return true
	default:
		return false
	}
}

func writePlanningBrief(b *strings.Builder, brief string) {
	writeRunPacketSection(b, "Planning Brief")
	brief = strings.TrimSpace(brief)
	if brief == "" {
		b.WriteString("- not present\n")
		return
	}
	b.WriteString(brief)
	b.WriteString("\n")
}
