package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

func TestRenderGoldenColorModes(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{
		RepositoryName: "repo",
		PlanID:         "plan",
		Status:         plan.StatusPlanned,
	}}}
	plain := clearScreenSequence + `Tao UI | Plans | Repositories: all | 1 plan

PLANNED / IN REVIEW
  REPO  PLAN  STATUS   NEXT  PHASE/SLICE  RUN AGE  SLICES  UPDATED
> repo  plan  planned  RUN   -            -        0/0     -
SELECTED PLAN — advisory context
Benefit: -
Readiness: -
Disposition: - — -
Priority: unranked
Sequence: -
Slice scope: -
Relationships: -
`
	colored := strings.Replace(plain, "Plans", "\x1b[1mPlans\x1b[0m", 1)
	colored = strings.Replace(colored, "planned  RUN", "\x1b[33mplanned\x1b[0m  RUN", 1)
	tests := []struct {
		name     string
		useColor bool
		want     string
	}{
		{name: "color off", want: plain},
		{name: "color on", useColor: true, want: colored},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Render(Model{Snapshot: snapshot, UseColor: test.useColor})
			if got != test.want {
				t.Fatalf("Render() mismatch\nwant:\n%q\n got:\n%q", test.want, got)
			}
		})
	}
}

func TestRenderSectionsAndOperationalLabels(t *testing.T) {
	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-30 * time.Minute)
	snapshot := monitor.Snapshot{CollectedAt: now, Rows: []monitor.Row{
		{RepositoryName: "run", PlanID: "live", Status: plan.StatusInProgress, Liveness: monitor.LivenessLive, SliceID: "004-ui", InvocationDuration: 2 * time.Minute, OriginalCompletedCount: 2, OriginalTotalCount: 4, UpdatedAt: &updated},
		{RepositoryName: "attn", PlanID: "dead", Status: plan.StatusBlocked, Liveness: monitor.LivenessStale, AttentionReasons: []monitor.AttentionReason{monitor.AttentionRunCrashed}, UpdatedAt: &updated},
		{RepositoryName: "plan", PlanID: "planned", Status: plan.StatusPlanned, UpdatedAt: &updated},
		{RepositoryName: "plan", PlanID: "review", Status: plan.StatusInReview, UpdatedAt: &updated},
		{RepositoryName: "done", PlanID: "complete", Status: plan.StatusCompleted, OriginalCompletedCount: 1, OriginalTotalCount: 1, UpdatedAt: &updated},
		{RepositoryName: "run", PlanID: "stale", Status: plan.StatusInProgress, Liveness: monitor.LivenessStale, HeartbeatAge: 45 * time.Second, RunLockPresent: true, RunLockProcessAlive: true, InvocationDuration: time.Hour, UpdatedAt: &updated},
	}}

	got := Render(Model{Snapshot: snapshot, Selected: 2})
	ordered := []string{"NEEDS ATTENTION", "RUNNING", "PLANNED / IN REVIEW", "COMPLETED"}
	previous := -1
	for _, label := range ordered {
		index := strings.Index(got, label)
		if index < 0 || index <= previous {
			t.Fatalf("section %q missing or out of order in:\n%s", label, got)
		}
		previous = index
	}
	for _, want := range []string{
		"NEXT", "RUN AGE", "RUN", "REVIEW", "DONE", "PHASE/SLICE", "ATTENTION", "crashed?", "stalled? (45s old)",
		"004-ui", "2m", "2/4", "30m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "> run") {
		t.Errorf("selection did not cross into the running section:\n%s", got)
	}
}

func TestPhaseLabelRequiresLiveRunLockForStalledLabel(t *testing.T) {
	base := monitor.Row{Liveness: monitor.LivenessStale, HeartbeatAge: 45 * time.Second, Phase: "verify"}
	tests := []struct {
		name string
		row  monitor.Row
		want string
	}{
		{name: "missing lock", row: base, want: "verify"},
		{name: "dead lock", row: func() monitor.Row { row := base; row.RunLockPresent = true; return row }(), want: "verify"},
		{name: "live lock", row: func() monitor.Row { row := base; row.RunLockPresent = true; row.RunLockProcessAlive = true; return row }(), want: "stalled? (45s old)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := phaseLabel(test.row); got != test.want {
				t.Fatalf("phaseLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPhaseLabelTruncatesByCells(t *testing.T) {
	got := phaseLabel(monitor.Row{Phase: "running_slice", SliceID: strings.Repeat("界", 11)})
	if width := visibleWidth(got); width != maxSliceIDCells {
		t.Fatalf("phaseLabel() width = %d, want %d: %q", width, maxSliceIDCells, got)
	}
}

func TestRenderOmitsEmptyAndHiddenCompletedSections(t *testing.T) {
	tests := []struct {
		name  string
		model Model
		want  string
	}{
		{
			name:  "empty snapshot",
			model: Model{},
			want: clearScreenSequence + `Tao UI | Plans | Repositories: all | 0 plans

  No plans.
`,
		},
		{
			name: "completed hidden",
			model: Model{
				Snapshot:      monitor.Snapshot{Rows: []monitor.Row{{PlanID: "done", Status: plan.StatusCompleted}}},
				HideCompleted: true,
			},
			want: clearScreenSequence + `Tao UI | Plans | Repositories: all | 0 plans

  No plans.
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Render(test.model)
			if got != test.want {
				t.Fatalf("Render() mismatch\nwant:\n%q\n got:\n%q", test.want, got)
			}
			if strings.Contains(got, "COMPLETED") {
				t.Fatalf("Render() unexpectedly included empty completed section: %q", got)
			}
		})
	}
}

func TestRenderShowsRepositoryFocusAndFiltersRows(t *testing.T) {
	got := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{RepositoryID: "repo-a", RepositoryName: "alpha", PlanID: "one", Status: plan.StatusPlanned},
			{RepositoryID: "repo-b", RepositoryName: "beta", PlanID: "two", Status: plan.StatusPlanned},
		}},
		FocusRepositoryID:   "repo-b",
		FocusRepositoryName: "beta",
	})
	if !strings.Contains(got, "Tao UI | Plans | Repository: beta | 1 plan") || !strings.Contains(got, "> beta  two") {
		t.Fatalf("focused render missing header or row:\n%s", got)
	}
	if strings.Contains(got, "alpha") {
		t.Fatalf("focused render included another repository:\n%s", got)
	}

	empty := Render(Model{FocusRepositoryID: "repo-b", FocusRepositoryName: "beta"})
	if !strings.Contains(empty, "Tao UI | Plans | Repository: beta | 0 plans") || !strings.Contains(empty, "No plans.") {
		t.Fatalf("empty focused render is ambiguous:\n%s", empty)
	}
}

func TestRenderHeaderTracksActivePage(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{RepositoryName: "repo", PlanID: "plan", Status: plan.StatusPlanned}}}

	plans := Render(Model{Snapshot: snapshot})
	for _, want := range []string{"Tao UI | Plans | Repositories: all | 1 plan", "> repo  plan"} {
		if !strings.Contains(plans, want) {
			t.Fatalf("plans page missing %q:\n%s", want, plans)
		}
	}

	notes := Render(Model{Snapshot: snapshot, Page: PageNotes, ActionMessage: "plan action"})
	for _, want := range []string{"Tao UI | Notes | Repositories: all | 0 open notes", "Notes page."} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes page missing %q:\n%s", want, notes)
		}
	}
	for _, unavailable := range []string{"> repo  plan", "r run", "c completed", "Enter plan", "plan action", "q quit"} {
		if strings.Contains(notes, unavailable) {
			t.Fatalf("notes page exposed plan-only content %q:\n%s", unavailable, notes)
		}
	}

	for _, test := range []struct {
		page PageID
		want string
	}{
		{page: PagePlans, want: "Tao UI | \x1b[1mPlans\x1b[0m | Repositories: all | 1 plan"},
		{page: PageNotes, want: "Tao UI | \x1b[1mNotes\x1b[0m | Repositories: all | 0 open notes"},
		{page: PageSettings, want: "Tao UI | \x1b[1mSettings\x1b[0m | 0 repositories"},
		{page: PageDebug, want: "Tao UI | \x1b[1mDebug\x1b[0m | diagnostics"},
	} {
		got := Render(Model{Snapshot: snapshot, Page: test.page, UseColor: true})
		if !strings.Contains(got, test.want) || strings.Contains(got, "[Plans]") || strings.Contains(got, "[Notes]") {
			t.Fatalf("colored header for %s missing bold active page or retained tab line:\n%q", test.page, got)
		}
	}
}

func TestRenderConstrainedDimensionsKeepPageIdentity(t *testing.T) {
	got := Render(Model{Page: PageNotes, Width: 18, Height: 1})
	lines := renderedLines(got)
	if len(lines) != 1 || lines[0] != "Tao UI | Notes | R" {
		t.Fatalf("constrained frame = %#v, want truncated header with active page", lines)
	}
	for _, line := range lines {
		if visibleWidth(line) > 18 {
			t.Fatalf("constrained line exceeds width: %q", line)
		}
	}
}

func TestRenderHeightViewportKeepsSelectionVisible(t *testing.T) {
	rows := make([]monitor.Row, 12)
	for index := range rows {
		rows[index] = monitor.Row{
			RepositoryName: "repo",
			PlanID:         fmt.Sprintf("plan-%02d", index),
			Status:         plan.StatusPlanned,
		}
	}

	for _, selected := range []int{0, 6, 11} {
		t.Run(fmt.Sprintf("selected row %d", selected), func(t *testing.T) {
			got := Render(Model{
				Snapshot: monitor.Snapshot{Rows: rows},
				Selected: selected,
				Height:   8,
			})
			lines := renderedLines(got)
			if len(lines) != 8 {
				t.Fatalf("rendered lines = %d, want 8:\n%s", len(lines), got)
			}
			if !strings.HasPrefix(lines[0], "Tao UI | ") {
				t.Fatalf("viewport header = %q, want Tao UI header", lines[0])
			}
			if strings.HasSuffix(got, "\n") {
				t.Fatal("full-height frame ends with a newline that can scroll the terminal")
			}
			selectedLabel := fmt.Sprintf("> repo  plan-%02d", selected)
			if !strings.Contains(got, selectedLabel) {
				t.Fatalf("viewport does not contain selected row %q:\n%s", selectedLabel, got)
			}
		})
	}
}

func TestRenderVerticalResizeReflowsAroundSelection(t *testing.T) {
	rows := make([]monitor.Row, 10)
	for index := range rows {
		rows[index] = monitor.Row{RepositoryName: "repo", PlanID: fmt.Sprintf("plan-%02d", index), Status: plan.StatusPlanned}
	}
	model := Model{Snapshot: monitor.Snapshot{Rows: rows}, Selected: 7, Height: 20}
	if lines := renderedLines(Render(model)); len(lines) != 20 {
		t.Fatalf("tall rendered lines = %d, want bounded 20-line dashboard", len(lines))
	}

	model.Height = 6
	got := Render(model)
	if lines := renderedLines(got); len(lines) != 6 {
		t.Fatalf("resized rendered lines = %d, want 6:\n%s", len(lines), got)
	}
	if !strings.Contains(got, "> repo  plan-07") {
		t.Fatalf("resized viewport lost selected row:\n%s", got)
	}
}

func TestRenderSelectedPlanPreviewShowsBoundedDecisionContext(t *testing.T) {
	row := monitor.Row{
		RepositoryName: "repo", PlanID: "priority-plan", Status: plan.StatusPlanned,
		SliceID: "002-preview", SliceTitle: "Render decision context",
		Overview: plan.DecisionOverview{
			ExpectedBenefit: "Operators can choose the right plan.", Readiness: plan.DecisionReadinessReady,
			Disposition: plan.DecisionDispositionConditional, DispositionReason: "Confirm the dependency first.",
			Priority: &plan.Priority{Level: plan.PriorityOverallLevelMust, Impact: plan.PriorityLevelHigh, Urgency: plan.PriorityLevelMedium, Effort: plan.PriorityEffortSmall, Risk: plan.PriorityLevelLow, Confidence: plan.PriorityLevelHigh, Rationale: "High benefit for little work."},
			Sequence: &plan.Sequence{Position: 2, Total: 4},
		},
		Relationships: []monitor.ResolvedRelationship{{PlanID: "foundation", Type: plan.PlanRelationAfter, State: monitor.RelationshipComplete}},
	}
	got := Render(Model{Snapshot: monitor.Snapshot{Rows: []monitor.Row{row}}, Width: 120})
	for _, want := range []string{
		"RUN AGE", "NEXT", "SELECTED PLAN — advisory context",
		"Benefit: Operators can choose the right plan.", "Readiness: ready",
		"Disposition: conditional — Confirm the dependency first.",
		"Priority: level=must  impact=high  urgency=medium  effort=small  risk=low  confidence=high",
		"Priority rationale: High benefit for little work.", "Sequence: 2 of 4",
		"Slice scope: 002-preview — Render decision context", "Relationships: after foundation [complete]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q:\n%s", want, got)
		}
	}
}

func TestRenderPlanPreviewYieldsToTableSelectionAndConfirmation(t *testing.T) {
	rows := []monitor.Row{
		{RepositoryName: "repo", PlanID: "first", Status: plan.StatusPlanned},
		{RepositoryName: "repo", PlanID: "selected", Status: plan.StatusPlanned, Overview: plan.DecisionOverview{ExpectedBenefit: strings.Repeat("benefit ", 20), Readiness: plan.DecisionReadinessReady}},
	}
	got := Render(Model{Snapshot: monitor.Snapshot{Rows: rows}, Selected: 1, Width: 36, Height: 7, ConfirmMessage: "Run selected plan?"})
	lines := renderedLines(got)
	if len(lines) != 7 {
		t.Fatalf("rendered lines = %d, want 7:\n%s", len(lines), got)
	}
	for _, want := range []string{"Tao UI | Plans", "> repo  selected", "SELECTED PLAN", "Run selected plan? [y/n]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("responsive frame missing %q:\n%s", want, got)
		}
	}
	for _, line := range lines {
		if width := visibleWidth(line); width > 36 {
			t.Fatalf("responsive line width = %d, want <= 36: %q", width, line)
		}
	}
}

func TestRenderNarrowWidthTruncatesRunesAndPreservesColor(t *testing.T) {
	got := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{{RepositoryName: "répo", PlanID: "plan", Status: plan.StatusPlanned}}},
		Width:    18,
		UseColor: true,
	})
	body := strings.TrimPrefix(got, clearScreenSequence)
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if width := visibleWidth(line); width > 18 {
			t.Fatalf("rendered line %q has %d visible cells, want at most 18", line, width)
		}
		if strings.Count(line, "\x1b[")%2 != 0 {
			t.Fatalf("rendered line has an unterminated color sequence: %q", line)
		}
	}
	if !strings.Contains(got, "\x1b[33mplan\x1b[0m") {
		t.Fatalf("narrow colored status was not safely truncated: %q", got)
	}
}

func TestRenderShortcutLegendAsBoundedPopover(t *testing.T) {
	for _, test := range []struct {
		page        PageID
		want        []string
		unavailable string
	}{
		{
			page: PagePlans,
			want: []string{"Keyboard shortcuts", "KEY", "ACTION", "r", "Run selected plan", "/", "Search plans and notes", "Backspace", "Go back / clear search", "? / Esc", "Close shortcuts"},
		},
		{
			page:        PageNotes,
			want:        []string{"Keyboard shortcuts", "Open selected item", "Cycle repository filter", "/", "Search plans and notes", "Backspace", "Go back / clear search", "? / Esc"},
			unavailable: "Run selected plan",
		},
	} {
		frame := Render(Model{Page: test.page, ShowShortcuts: true, Width: 64, Height: 18, UseColor: true})
		for _, want := range test.want {
			if !strings.Contains(frame, want) {
				t.Fatalf("%s shortcut popover missing %q:\n%s", test.page, want, frame)
			}
		}
		if test.unavailable != "" && strings.Contains(frame, test.unavailable) {
			t.Fatalf("%s shortcut popover contains unavailable action %q:\n%s", test.page, test.unavailable, frame)
		}
		lines := renderedLines(frame)
		if len(lines) != 18 {
			t.Fatalf("%s shortcut popover lines = %d, want 18", test.page, len(lines))
		}
		for _, line := range lines {
			if width := visibleWidth(line); width > 64 {
				t.Fatalf("%s shortcut line width = %d, want at most 64: %q", test.page, width, line)
			}
		}
	}
}

func TestRenderConfirmation(t *testing.T) {
	got := Render(Model{ConfirmMessage: "Approve this slice?"})
	if !strings.Contains(got, "Approve this slice? [y/n]") {
		t.Fatalf("Render() missing confirmation prompt: %q", got)
	}
}

func TestRenderActionFeedback(t *testing.T) {
	row := monitor.Row{RepositoryID: "repo", RepositoryName: "repo", PlanID: "plan", Status: plan.StatusPlanned}
	got := Render(Model{
		Snapshot:      monitor.Snapshot{Rows: []monitor.Row{row}},
		ActionLabels:  map[string]string{actionRowKey(row): "starting…"},
		ActionMessage: "Failed to start plan; inspect `tao log plan`.",
	})
	for _, want := range []string{"starting…", "tao log plan"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Render() missing action feedback %q in %q", want, got)
		}
	}
}

func TestRenderBoundedViewportPreservesActionFooter(t *testing.T) {
	rows := make([]monitor.Row, 8)
	for index := range rows {
		rows[index] = monitor.Row{RepositoryName: "repo", PlanID: fmt.Sprintf("plan-%02d", index), Status: plan.StatusPlanned}
	}
	got := Render(Model{
		Snapshot:       monitor.Snapshot{Rows: rows},
		Selected:       7,
		Height:         6,
		ActionMessage:  "Run failed.",
		ConfirmMessage: "Approve this slice?",
	})
	if lines := renderedLines(got); len(lines) != 6 {
		t.Fatalf("rendered lines = %d, want 6:\n%s", len(lines), got)
	}
	for _, want := range []string{"> repo  plan-07", "Run failed.", "Approve this slice? [y/n]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded viewport missing %q:\n%s", want, got)
		}
	}
}

func renderedLines(frame string) []string {
	body := strings.TrimPrefix(frame, clearScreenSequence)
	return strings.Split(strings.TrimSuffix(body, "\n"), "\n")
}
