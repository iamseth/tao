package plan

import (
	"fmt"
	"strings"
	"time"
)

// Run packet rendering is intentionally a compact read-only projection for agents;
// fallback artifacts remain available when this context is stale or insufficient.
const defaultRunPacketRecentEvents = 5

// RunPacketOptions controls optional context included in the selected-slice packet.
type RunPacketOptions struct {
	CommitPolicy  string
	ExecutionMode string
	WorkingRoot   string
	RecentEvents  int
	Resuming      bool
	ResumeAttempt int
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
	case EventTypeAgentMetrics, EventTypeRunContext:
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
