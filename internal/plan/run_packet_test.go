package plan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRunPacketIncludesSelectedSliceContext(t *testing.T) {
	detail := runPacketDetail()
	detail.PlanningBrief.Content = "## User Goal\nShip compact context.\n"

	packet, err := RenderRunPacket(detail, RunPacketOptions{CommitPolicy: "plan", RecentEvents: 10})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"# Tao Run Packet",
		"- Repo Root: /repo/root",
		"- Repo Branch: main",
		"- Workspace Branch: tao/plan",
		"- Pending Slices: 002-build",
		"- ID: 002-build",
		"- Approval: required, approved: approval gate",
		"- Goal: Build the formatter",
		"## Required Inputs\n- go.mod (file): module definition",
		"- internal/plan/run_packet.go",
		"- Source: focused package tests",
		"- go test ./internal/plan -run TestRunPacket",
		"- 001-base: completed",
		"## Prior Completions\n- 001-base: Base Packet - Base rendering done.",
		"- Keep compatibility.",
		"- Commit Policy: plan",
		"- Execution Mode: isolated",
		"## User Goal\nShip compact context.",
		"- Use this packet first; read full fallback artifacts only with a concrete reason",
		"- plan.md",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("expected packet to contain %q:\n%s", want, packet)
		}
	}
}

func TestRunPacketRendersWorkingRootWhenProvided(t *testing.T) {
	packet, err := RenderRunPacket(runPacketDetail(), RunPacketOptions{WorkingRoot: "/repo/.tao/worktrees/plan"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- Workspace Root: /repo/.tao/worktrees/plan",
		"- Control Repo Root (do not touch): /repo/root",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("expected packet to contain %q:\n%s", want, packet)
		}
	}
	if strings.Contains(packet, "- Repo Root: /repo/root") {
		t.Fatalf("expected working-root packet to replace the generic repo root label:\n%s", packet)
	}
}

func TestRunPacketShowsNoRequiredInputsForLegacySlices(t *testing.T) {
	detail := runPacketDetail()
	detail.Slices.Slices[1].RequiredInputs = nil

	packet, err := RenderRunPacket(detail, RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packet, "## Required Inputs\n- none") {
		t.Fatalf("expected legacy slice packet to show no required inputs:\n%s", packet)
	}
}

func TestRunPacketFallsBackToRepoRootWhenWorkingRootEmpty(t *testing.T) {
	packet, err := RenderRunPacket(runPacketDetail(), RunPacketOptions{WorkingRoot: ""})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packet, "- Repo Root: /repo/root") {
		t.Fatalf("expected empty working root to fall back to repo root:\n%s", packet)
	}
	if strings.Contains(packet, "Workspace Root") || strings.Contains(packet, "Control Repo Root") {
		t.Fatalf("expected fallback packet to omit workspace/control root labels:\n%s", packet)
	}
}

func TestRunPacketExecutionModeOutput(t *testing.T) {
	packet, err := RenderRunPacket(runPacketDetail(), RunPacketOptions{ExecutionMode: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packet, "- Execution Mode: current") {
		t.Fatalf("expected current execution mode in packet:\n%s", packet)
	}
}

func TestRunPacketRerenderPreservesDistinctWorkspaceBranch(t *testing.T) {
	detail := runPacketDetail()
	detail.State.Repo.Branch = "main"
	detail.State.Workspace.Branch = "tao/durable-plan"

	encoded, err := json.Marshal(detail.State)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded State
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	detail.State = reloaded

	packet, err := RenderRunPacket(detail, RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- Repo Branch: main", "- Workspace Branch: tao/durable-plan"} {
		if !strings.Contains(packet, want) {
			t.Fatalf("rerendered packet missing %q:\n%s", want, packet)
		}
	}
}

func TestRunPacketIncludesInterruptedResumeInstructions(t *testing.T) {
	packet, err := RenderRunPacket(runPacketDetail(), RunPacketOptions{Resuming: true, ResumeAttempt: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Interrupted Slice Resume",
		"- Resume Attempt: 2",
		"Inspect staged, unstaged, and untracked work",
		"Continue or correct the preserved work; do not discard it or restart",
		"Rerun every declared verification command, then call tao slice-complete",
		"Never commit manually",
	} {
		if !strings.Contains(packet, want) {
			t.Fatalf("resume packet missing %q:\n%s", want, packet)
		}
	}
}

func TestRunPacketFiltersRecentRelevantEvents(t *testing.T) {
	detail := runPacketDetail()
	detail.Events = []Event{
		{Type: "plan_created", Timestamp: runPacketTime(0), PlanID: "plan", Message: "created"},
		{Type: "slice_started", Timestamp: runPacketTime(1), PlanID: "plan", SliceID: "001-base", Message: "base started"},
		{Type: "slice_completed", Timestamp: runPacketTime(2), PlanID: "plan", SliceID: "002-build", Message: "build done"},
		{Type: EventTypeAgentMetrics, Timestamp: runPacketTime(3), PlanID: "plan", SliceID: "002-build", Message: "metrics"},
		{Type: EventTypeRunContext, Timestamp: runPacketTime(4), PlanID: "plan", SliceID: "002-build", Message: "context telemetry"},
	}

	packet, err := RenderRunPacket(detail, RunPacketOptions{RecentEvents: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(packet, "base started") || strings.Contains(packet, "created") {
		t.Fatalf("expected unrelated and older events to be filtered:\n%s", packet)
	}
	if !strings.Contains(packet, "build done") {
		t.Fatalf("expected recent selected-slice event:\n%s", packet)
	}
	if strings.Contains(packet, "metrics") || strings.Contains(packet, "context telemetry") {
		t.Fatalf("expected telemetry events to be omitted:\n%s", packet)
	}
}

func TestRunPacketHandlesMissingOptionalArtifacts(t *testing.T) {
	detail := runPacketDetail()
	detail.PlanningBrief = PlanningBriefArtifact{}
	detail.State.GlobalInvariants = nil
	detail.State.OpenQuestions = nil
	detail.State.Repo = Repo{}
	detail.State.Workspace = nil
	detail.State.Plan.CompletedSlices = nil
	detail.Slices.Slices[1].Verification.Source = ""
	detail.Slices.Slices[1].Approval = nil
	detail.Events = nil

	packet, err := RenderRunPacket(detail, RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- Repo Root: none", "- Repo Branch: none", "- Workspace Branch: none", "- Approval: not required", "- Source: none", "## Prior Completions\n- none", "## Planning Brief\n- not present", "## Recent Relevant Events\n- none", "## Open Questions\n- none"} {
		if !strings.Contains(packet, want) {
			t.Fatalf("expected packet to contain %q:\n%s", want, packet)
		}
	}
}

func TestRunPacketDependencyStatusShowsMissingDependency(t *testing.T) {
	detail := runPacketDetail()
	detail.State.Plan.CompletedSlices = nil
	detail.Slices.Slices[0].Status = StatusPending

	packet, err := RenderRunPacket(detail, RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packet, "- 001-base: missing") {
		t.Fatalf("expected missing dependency status:\n%s", packet)
	}
}

func TestRunPacketTelemetryFeedbackNone(t *testing.T) {
	packet, err := RenderRunPacket(runPacketDetail(), RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runPacketTelemetryFeedbackSection(packet), "- none\n"; got != want {
		t.Fatalf("telemetry feedback:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunPacketTelemetryFeedbackBudgetWarnings(t *testing.T) {
	detail := runPacketDetail()
	detail.Events = append(detail.Events,
		Event{Type: EventTypeAgentMetrics, Timestamp: runPacketTime(2), PlanID: "plan", SliceID: "001-base", Metrics: &AgentMetrics{SessionID: "old", OutputTokens: 100}},
		Event{Type: EventTypeAgentMetrics, Timestamp: runPacketTime(3), PlanID: "plan", SliceID: "002-build", Metrics: &AgentMetrics{SessionID: "current", OutputTokens: 20}},
	)
	thresholds := AgentBudgetThresholds{
		Plan:  AgentBudgetScopeThresholds{OutputTokens: 50},
		Slice: AgentBudgetScopeThresholds{OutputTokens: 10},
	}
	packet, err := RenderRunPacket(detail, RunPacketOptions{BudgetThresholds: &thresholds})
	if err != nil {
		t.Fatal(err)
	}
	want := "- Budget warning (plan): output_tokens observed 120 > threshold 50\n" +
		"- Budget warning (slice 002-build): output_tokens observed 20 > threshold 10\n"
	if got := runPacketTelemetryFeedbackSection(packet); got != want {
		t.Fatalf("telemetry feedback:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunPacketTelemetryFeedbackAllZeroThresholds(t *testing.T) {
	detail := runPacketDetail()
	detail.Events = append(detail.Events, Event{
		Type:      EventTypeAgentMetrics,
		Timestamp: runPacketTime(2),
		PlanID:    "plan",
		SliceID:   "002-build",
		Metrics: &AgentMetrics{
			SessionID:         "all-metrics",
			OutputTokens:      1,
			Cost:              1,
			ToolCalls:         1,
			AssistantMessages: 1,
			ErroredMessages:   1,
		},
	})
	thresholds := AgentBudgetThresholds{}

	packet, err := RenderRunPacket(detail, RunPacketOptions{BudgetThresholds: &thresholds})
	if err != nil {
		t.Fatal(err)
	}
	feedback := runPacketTelemetryFeedbackSection(packet)
	if got, want := strings.Count(feedback, "threshold 0\n"), 10; got != want {
		t.Fatalf("zero-threshold warnings = %d, want %d:\n%s", got, want, feedback)
	}
}

func TestRunPacketTelemetryFeedbackReworkHistory(t *testing.T) {
	detail := runPacketDetail()
	detail.Events = append(detail.Events,
		Event{Type: EventTypeReworkRound, Timestamp: runPacketTime(2), PlanID: "plan", Round: 1},
		Event{Type: EventTypeReworkRound, Timestamp: runPacketTime(3), PlanID: "plan", Round: 2},
		Event{Type: EventTypeReworkStopped, Timestamp: runPacketTime(4), PlanID: "plan", Reason: "same findings repeated\nfull details"},
	)
	packet, err := RenderRunPacket(detail, RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "- Rework rounds: 2\n- Latest rework stop: same findings repeated\n"
	if got := runPacketTelemetryFeedbackSection(packet); got != want {
		t.Fatalf("telemetry feedback:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunPacketTelemetryFeedbackFailureSignals(t *testing.T) {
	detail := runPacketDetail()
	detail.Events = []Event{
		{Type: EventTypeSliceBlocked, Timestamp: runPacketTime(1), PlanID: "plan", SliceID: "001-base", Reason: "dependency unavailable\nretry later"},
		{Type: EventTypeVerificationCommandInvalid, Timestamp: runPacketTime(2), PlanID: "plan", Reason: "package path missing"},
		{Type: EventTypeSliceResumeFailed, Timestamp: runPacketTime(3), PlanID: "another-plan", Reason: "not this plan"},
		{Type: EventTypeSessionTimeout, Timestamp: runPacketTime(4), PlanID: "plan", Message: "pi timed out"},
		{Type: EventTypeAgentMetrics, Timestamp: runPacketTime(5), PlanID: "plan", SliceID: "002-build", Metrics: &AgentMetrics{SessionID: "session-2", Status: "failed", OutputTokens: 12, Cost: 0.5, ToolCalls: 3, ErroredMessages: 1}},
	}
	thresholds := DefaultAgentBudgetThresholds()
	thresholds.Plan.ErroredMessages = 10
	thresholds.Slice.ErroredMessages = 10
	packet, err := RenderRunPacket(detail, RunPacketOptions{BudgetThresholds: &thresholds})
	if err != nil {
		t.Fatal(err)
	}
	want := "- Failure 2026-05-03T23:00:01Z slice_blocked: dependency unavailable\n" +
		"- Failure 2026-05-03T23:00:02Z verification_command_invalid: package path missing\n" +
		"- Failure 2026-05-03T23:00:04Z session_timeout: pi timed out\n" +
		"- Failure 2026-05-03T23:00:05Z agent_metrics: agent session session-2 failed (output_tokens=12 cost=0.5 tool_calls=3 errored_messages=1)\n"
	if got := runPacketTelemetryFeedbackSection(packet); got != want {
		t.Fatalf("telemetry feedback:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunPacketTelemetryFeedbackSizeCap(t *testing.T) {
	detail := runPacketDetail()
	metrics := &AgentMetrics{SessionID: "large", OutputTokens: 100, Cost: 100, ToolCalls: 100, AssistantMessages: 100, ErroredMessages: 100}
	detail.Events = []Event{{Type: EventTypeAgentMetrics, Timestamp: runPacketTime(1), PlanID: "plan", SliceID: "002-build", Metrics: metrics}}
	for i := 2; i < 9; i++ {
		detail.Events = append(detail.Events, Event{Type: EventTypeSessionTimeout, Timestamp: runPacketTime(i), PlanID: "plan", Message: "timeout"})
	}
	thresholds := AgentBudgetThresholds{Plan: AgentBudgetScopeThresholds{OutputTokens: 1}}
	packet, err := RenderRunPacket(detail, RunPacketOptions{BudgetThresholds: &thresholds})
	if err != nil {
		t.Fatal(err)
	}
	feedback := runPacketTelemetryFeedbackSection(packet)
	if got := strings.Count(feedback, "\n"); got != maxRunPacketFeedbackLines {
		t.Fatalf("feedback lines = %d, want %d:\n%s", got, maxRunPacketFeedbackLines, feedback)
	}
}

func TestRunPacketTelemetryFeedbackByteCap(t *testing.T) {
	detail := runPacketDetail()
	detail.Events = []Event{{
		Type:      EventTypeSessionTimeout,
		Timestamp: runPacketTime(1),
		PlanID:    "plan",
		Message:   strings.Repeat("界", maxRunPacketFeedbackBytes),
	}}

	packet, err := RenderRunPacket(detail, RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	feedback := runPacketTelemetryFeedbackSection(packet)
	if len(feedback) > maxRunPacketFeedbackBytes {
		t.Fatalf("feedback bytes = %d, want at most %d", len(feedback), maxRunPacketFeedbackBytes)
	}
	if !utf8.ValidString(feedback) {
		t.Fatal("feedback byte cap split a UTF-8 encoding")
	}
	if !strings.HasSuffix(feedback, "…\n") {
		t.Fatalf("oversized feedback was not visibly truncated: %q", feedback[len(feedback)-16:])
	}
}

func runPacketTelemetryFeedbackSection(packet string) string {
	prefix := "## Telemetry Feedback\n"
	_, section, ok := strings.Cut(packet, prefix)
	if !ok {
		return ""
	}
	if before, _, found := strings.Cut(section, "\n## "); found {
		return strings.TrimSuffix(before, "\n") + "\n"
	}
	return section
}

func runPacketDetail() *PlanDetail {
	current := "002-build"
	return &PlanDetail{
		State: State{
			Status:           StatusInProgress,
			Repo:             Repo{Root: "/repo/root", Branch: "main"},
			Workspace:        &Workspace{Branch: "tao/plan"},
			GlobalInvariants: []string{"Keep compatibility."},
			OpenQuestions:    []string{"Should this be exported?"},
			Plan: PlanState{
				ID:              "plan",
				Title:           "Plan Title",
				CurrentSlice:    &current,
				CompletedSlices: []string{"001-base"},
				PendingSlices:   []string{"002-build"},
			},
		},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-base", Title: "Base Packet", Status: StatusCompleted, Notes: "Base rendering done.\nExtra details."},
			{
				ID:        "002-build",
				Title:     "Build Packet",
				Status:    StatusInProgress,
				DependsOn: []string{"001-base"},
				Goal:      "Build the formatter",
				Context:   "Avoid full artifact reads.",
				Tasks:     []string{"Define packet", "Render Markdown"},
				RequiredInputs: []RequiredInput{{
					Path: "go.mod", Kind: RequiredInputFile, Reason: "module definition",
				}},
				ExpectedFiles: []string{"internal/plan/run_packet.go"},
				Verification: Verification{
					Commands:     []string{"go test ./internal/plan -run TestRunPacket"},
					Source:       "focused package tests",
					ManualChecks: []string{"Compare packet size"},
				},
				Approval: &Approval{Required: true, Approved: true, Reason: "approval gate"},
			},
		}},
		Events: []Event{{Type: "slice_started", Timestamp: runPacketTime(1), PlanID: "plan", SliceID: "002-build", Message: "started"}},
	}
}

func runPacketTime(seconds int) time.Time {
	return time.Date(2026, 5, 3, 23, 0, seconds, 0, time.UTC)
}
