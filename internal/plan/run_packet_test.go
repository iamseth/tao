package plan

import (
	"strings"
	"testing"
	"time"
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
	detail.State.Plan.CompletedSlices = nil
	detail.Slices.Slices[1].Verification.Source = ""
	detail.Slices.Slices[1].Approval = nil
	detail.Events = nil

	packet, err := RenderRunPacket(detail, RunPacketOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- Repo Root: none", "- Repo Branch: none", "- Approval: not required", "- Source: none", "## Prior Completions\n- none", "## Planning Brief\n- not present", "## Recent Relevant Events\n- none", "## Open Questions\n- none"} {
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

func runPacketDetail() *PlanDetail {
	current := "002-build"
	return &PlanDetail{
		State: State{
			Status:           StatusInProgress,
			Repo:             Repo{Root: "/repo/root", Branch: "main"},
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
