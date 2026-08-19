package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

func TestRenderGoldenColorModes(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{
		RepositoryName: "repo",
		PlanID:         "plan",
		Status:         plan.StatusPlanned,
	}}}
	plain := clearScreenSequence + `Tao UI | Repositories: all | 1 plan
Tabs: [Plans]  Notes

PLANNED / IN REVIEW
  REPO  PLAN  STATUS   PHASE/SLICE  RUN  SLICES  UPDATED
> repo  plan  planned  -            -    0/0     -

r run  a approve  m merge  M merge all  f repository  c completed  Enter plan  Tab/←/→ tabs  q quit  Esc Esc quit
`
	colored := strings.Replace(plain, "planned  -", "\x1b[33mplanned\x1b[0m  -", 1)
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
		"PHASE/SLICE", "ATTENTION", "crashed?", "stalled? (45s old)",
		"004-ui", "2m", "2/4", "30m", footerHints,
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

func TestRenderOmitsEmptyAndHiddenCompletedSections(t *testing.T) {
	tests := []struct {
		name  string
		model Model
		want  string
	}{
		{
			name:  "empty snapshot",
			model: Model{},
			want: clearScreenSequence + `Tao UI | Repositories: all | 0 plans
Tabs: [Plans]  Notes

  No plans.

r run  a approve  m merge  M merge all  f repository  c completed  Enter plan  Tab/←/→ tabs  q quit  Esc Esc quit
`,
		},
		{
			name: "completed hidden",
			model: Model{
				Snapshot:      monitor.Snapshot{Rows: []monitor.Row{{PlanID: "done", Status: plan.StatusCompleted}}},
				HideCompleted: true,
			},
			want: clearScreenSequence + `Tao UI | Repositories: all | 0 plans
Tabs: [Plans]  Notes

  No plans.

r run  a approve  m merge  M merge all  f repository  c completed  Enter plan  Tab/←/→ tabs  q quit  Esc Esc quit
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
	if !strings.Contains(got, "Tao UI | Repository: beta | 1 plan") || !strings.Contains(got, "> beta  two") {
		t.Fatalf("focused render missing header or row:\n%s", got)
	}
	if strings.Contains(got, "alpha") {
		t.Fatalf("focused render included another repository:\n%s", got)
	}

	empty := Render(Model{FocusRepositoryID: "repo-b", FocusRepositoryName: "beta"})
	if !strings.Contains(empty, "Tao UI | Repository: beta | 0 plans") || !strings.Contains(empty, "No plans.") {
		t.Fatalf("empty focused render is ambiguous:\n%s", empty)
	}
}

func TestRenderTabsAndPageAwareFooter(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{RepositoryName: "repo", PlanID: "plan", Status: plan.StatusPlanned}}}

	plans := Render(Model{Snapshot: snapshot})
	for _, want := range []string{"Tabs: [Plans]  Notes", "> repo  plan", "r run", "c completed", "Enter plan"} {
		if !strings.Contains(plans, want) {
			t.Fatalf("plans page missing %q:\n%s", want, plans)
		}
	}

	notes := Render(Model{Snapshot: snapshot, Page: PageNotes, ActionMessage: "plan action"})
	for _, want := range []string{"Tabs: Plans  [Notes]", "Notes page.", notesFooterHints} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes page missing %q:\n%s", want, notes)
		}
	}
	for _, unavailable := range []string{"> repo  plan", "r run", "c completed", "Enter plan", "plan action"} {
		if strings.Contains(notes, unavailable) {
			t.Fatalf("notes page exposed plan-only content %q:\n%s", unavailable, notes)
		}
	}
}

func TestRenderConstrainedDimensionsKeepTabIdentity(t *testing.T) {
	got := Render(Model{Page: PageNotes, Width: 18, Height: 2})
	lines := renderedLines(got)
	if len(lines) != 2 || !strings.Contains(lines[1], "Plans  [Note") {
		t.Fatalf("constrained tab frame = %#v, want two safely truncated header lines with active tab", lines)
	}
	for _, line := range lines {
		if utf8.RuneCountInString(stripANSI(line)) > 18 {
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
			if lines[len(lines)-1] != footerHints {
				t.Fatalf("viewport footer = %q, want %q", lines[len(lines)-1], footerHints)
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
	if lines := renderedLines(Render(model)); len(lines) != 17 {
		t.Fatalf("tall rendered lines = %d, want complete 17-line dashboard", len(lines))
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

func TestRenderNarrowWidthTruncatesRunesAndPreservesColor(t *testing.T) {
	got := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{{RepositoryName: "répo", PlanID: "plan", Status: plan.StatusPlanned}}},
		Width:    18,
		UseColor: true,
	})
	body := strings.TrimPrefix(got, clearScreenSequence)
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		plain := stripANSI(line)
		if count := utf8.RuneCountInString(plain); count > 18 {
			t.Fatalf("rendered line %q has %d visible runes, want at most 18", line, count)
		}
		if strings.Count(line, "\x1b[")%2 != 0 {
			t.Fatalf("rendered line has an unterminated color sequence: %q", line)
		}
	}
	if !strings.Contains(got, "\x1b[33mplan\x1b[0m") {
		t.Fatalf("narrow colored status was not safely truncated: %q", got)
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
	for _, want := range []string{"> repo  plan-07", "Run failed.", "Approve this slice? [y/n]", footerHints} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded viewport missing %q:\n%s", want, got)
		}
	}
}

func renderedLines(frame string) []string {
	body := strings.TrimPrefix(frame, clearScreenSequence)
	return strings.Split(strings.TrimSuffix(body, "\n"), "\n")
}

func stripANSI(value string) string {
	for {
		start := strings.Index(value, "\x1b[")
		if start < 0 {
			return value
		}
		end := strings.IndexByte(value[start:], 'm')
		if end < 0 {
			return value[:start]
		}
		value = value[:start] + value[start+end+1:]
	}
}
