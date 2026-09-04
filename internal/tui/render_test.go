package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
)

func TestRenderGoldenColorModes(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{
		RepositoryName: "repo",
		PlanID:         "plan",
		Status:         plan.StatusPlanned,
	}}}
	plain := Render(Model{Snapshot: snapshot})
	for _, want := range []string{
		"tao │ notes ▸plans  settings  debug", "all repos", "agent -", "1 plan",
		"NEXT", "REPO  NEXT   PLAN", "  repo   RUN   plan",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain frame missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "\x1b[") && !strings.HasPrefix(plain, clearScreenSequence) {
		t.Fatalf("plain frame contains color styling: %q", plain)
	}

	colored := Render(Model{Snapshot: snapshot, Profile: ProfileTrueColor})
	for _, want := range []string{
		Paint(ProfileTrueColor, RoleAccent, "plans"),
		Paint(ProfileTrueColor, RoleNeutral2, "notes"),
	} {
		if !strings.Contains(colored, want) {
			t.Fatalf("colored frame missing %q:\n%q", want, colored)
		}
	}
}

func TestPlanSectionsUseDedicatedColors(t *testing.T) {
	for kind, want := range map[SectionKind]Role{
		SectionNow:     RolePlanNow,
		SectionNext:    RolePlanNext,
		SectionHistory: RolePlanHistory,
	} {
		if got := planSectionRole(kind); got != want {
			t.Errorf("planSectionRole(%q) = %d, want %d", kind, got, want)
		}
	}
}

func TestPlanRowsUseSectionBackgroundsAndNeutralHistoryText(t *testing.T) {
	frame := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{RepositoryName: "alpha", PlanID: "now-row", Status: plan.StatusBlocked, AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
			{RepositoryName: "alpha", PlanID: "next-row", Status: plan.StatusPlanned},
			{RepositoryName: "alpha", PlanID: "history-row", Status: plan.StatusCompleted},
		}},
		Selected: 99,
		Width:    120,
		Profile:  ProfileTrueColor,
	})

	backgrounds := map[string]Role{
		"now-row":     RolePlanNowBackground,
		"next-row":    RolePlanNextBackground,
		"history-row": RolePlanHistoryBackground,
	}
	for planID, role := range backgrounds {
		var rowLine string
		for _, line := range renderedLines(frame) {
			if strings.Contains(line, planID) {
				rowLine = line
				break
			}
		}
		prefix := colorSequence(mustRoleColor(ProfileTrueColor, role), true)
		if rowLine == "" || !strings.HasPrefix(rowLine, prefix) {
			t.Errorf("%s row does not use background role %d: %q", planID, role, rowLine)
		}
	}
	for _, line := range renderedLines(frame) {
		if strings.Contains(line, "history-row") && !strings.Contains(line, colorSequence(mustRoleColor(ProfileTrueColor, RolePlanHistoryText), false)) {
			t.Errorf("history row does not use dim neutral text: %q", line)
		}
	}
}

func TestNextActionsRenderAsNormalText(t *testing.T) {
	row := monitor.Row{RepositoryID: "planned", PlanID: "planned", Status: plan.StatusPlanned}
	got := Render(Model{
		Snapshot:     monitor.Snapshot{Rows: []monitor.Row{row}},
		Profile:      ProfileTrueColor,
		ActionLabels: map[string]string{actionRowKey(row): "custom-action"},
	})
	if !strings.Contains(got, " custom-action ") {
		t.Fatalf("rendered frame missing NEXT action text: %q", got)
	}
	if strings.Contains(got, colorSequence(Accent(ProfileTrueColor), true)) {
		t.Fatalf("NEXT action has a highlighted background: %q", got)
	}
}

func TestColorStatusRendersVerificationFailureAsWarning(t *testing.T) {
	got := colorStatus(ProfileTrueColor, plan.StatusVerificationFailed, plan.StatusVerificationFailed)
	want := Paint(ProfileTrueColor, RoleWarn, plan.StatusVerificationFailed)
	if got != want {
		t.Fatalf("verification-failed status color = %q, want %q", got, want)
	}
}

func TestRenderSectionsAndOperationalLabels(t *testing.T) {
	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-30 * time.Minute)
	snapshot := monitor.Snapshot{CollectedAt: now, Rows: []monitor.Row{
		{RepositoryName: "run", PlanID: "live", Status: plan.StatusInProgress, Liveness: monitor.LivenessLive, SliceID: "004-ui", InvocationDuration: 2 * time.Minute, OriginalCompletedCount: 2, OriginalTotalCount: 4, UpdatedAt: &updated},
		{RepositoryName: "attn", PlanID: "dead", Status: plan.StatusBlocked, Liveness: monitor.LivenessStale, AttentionReasons: []monitor.AttentionReason{monitor.AttentionRunCrashed}, UpdatedAt: &updated},
		{RepositoryName: "plan", PlanID: "planned", Status: plan.StatusPlanned, UpdatedAt: &updated},
		{RepositoryName: "plan", PlanID: "review", Status: plan.StatusInReview, NextAction: "MERGE", UpdatedAt: &updated},
		{RepositoryName: "done", PlanID: "complete", Status: plan.StatusCompleted, OriginalCompletedCount: 1, OriginalTotalCount: 1, UpdatedAt: &updated},
		{RepositoryName: "old", PlanID: "abandoned", Status: plan.StatusAbandoned, UpdatedAt: &updated},
		{RepositoryName: "run", PlanID: "stale", Status: plan.StatusInProgress, Liveness: monitor.LivenessStale, HeartbeatAge: 45 * time.Second, RunLockPresent: true, RunLockProcessAlive: true, InvocationDuration: time.Hour, UpdatedAt: &updated},
	}}

	got := Render(Model{Snapshot: snapshot, Selected: 2})
	ordered := []string{"NOW", "NEXT", "DONE"}
	previous := -1
	for _, label := range ordered {
		index := strings.Index(got, label)
		if index < 0 || index <= previous {
			t.Fatalf("section %q missing or out of order in:\n%s", label, got)
		}
		previous = index
	}
	for _, want := range []string{
		"NEXT", "RUN", "AGE", "MERGE", "DONE", "ABANDONED", "SLICES", "stalled? (45s old)",
		"004-ui 2m", "2/4", "30m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "  run    MONITOR") {
		t.Errorf("selection did not cross into the running section:\n%s", got)
	}
}

func TestSlicesValueAddsTenCellProgressBarWithoutChangingLabel(t *testing.T) {
	row := monitor.Row{
		OriginalCompletedCount: 2,
		OriginalTotalCount:     3,
		ReworkCompletedCount:   1,
		ReworkTotalCount:       2,
	}
	if got := slicesLabel(row); got != "3/3+2" {
		t.Fatalf("slicesLabel() = %q, want existing combined label", got)
	}
	if got := renderSlicesValue(ProfileNone, row); got != "━━━━━━──── 3/3+2" {
		t.Fatalf("renderSlicesValue() = %q, want ten-cell thin bar followed by label", got)
	}

	complete := monitor.Row{
		OriginalCompletedCount: 3,
		OriginalTotalCount:     3,
		ReworkCompletedCount:   5,
		ReworkTotalCount:       5,
	}
	if got := renderSlicesValue(ProfileNone, complete); got != "━━━━━━━━━━ 8/3+5" {
		t.Fatalf("complete renderSlicesValue() = %q", got)
	}
}

func TestSelectedPlanRowUsesTokyoNightSelectionColors(t *testing.T) {
	const width = 120
	row := monitor.Row{
		RepositoryID:           "repo-id",
		RepositoryName:         "repo",
		PlanID:                 "selected-plan",
		Status:                 plan.StatusPlanned,
		OriginalCompletedCount: 1,
		OriginalTotalCount:     3,
	}
	frame := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{row}},
		Width:    width,
		Profile:  ProfileTrueColor,
	})
	var selectedLine string
	for _, line := range renderedLines(frame) {
		if strings.Contains(line, "selected-plan") {
			selectedLine = line
			break
		}
	}
	if selectedLine == "" {
		t.Fatalf("selected row not found in frame: %q", frame)
	}
	selectionPrefix := boldSequence +
		colorSequence(mustRoleColor(ProfileTrueColor, RolePlanSelectionBackground), true) +
		colorSequence(mustRoleColor(ProfileTrueColor, RolePlanSelectionText), false)
	if !strings.HasPrefix(selectedLine, selectionPrefix) || !strings.HasSuffix(selectedLine, resetSequence) {
		t.Fatalf("selected row does not wrap the full rendered line: %q", selectedLine)
	}
	if got := cells.Width(selectedLine); got != width {
		t.Fatalf("selected row width = %d, want pane width %d: %q", got, width, selectedLine)
	}
	if !strings.HasSuffix(strings.TrimSuffix(selectedLine, resetSequence), " ") {
		t.Fatalf("selection background was applied before full-width padding: %q", selectedLine)
	}
	for _, role := range []Role{RoleRepoSelected, RoleNeutral5, RoleNeutral2} {
		color, _ := RoleColor(ProfileTrueColor, role)
		if strings.Contains(selectedLine, colorSequence(color, false)) {
			t.Errorf("selected row retained foreground role %d instead of the selection text color: %q", role, selectedLine)
		}
	}
	if !strings.Contains(selectedLine, " RUN ") {
		t.Errorf("selected row lost NEXT text: %q", selectedLine)
	}
	if strings.Contains(selectedLine, "\x1b[7m") || strings.Contains(selectedLine, "> repo") {
		t.Fatalf("selected row uses a reverse-video or cursor marker: %q", selectedLine)
	}
}

func TestPlanLabelUsesReadableSlugWithoutFullPlanID(t *testing.T) {
	const id = "20260828-181339-tui-plans-rows"
	if got := planLabel(monitor.Row{PlanID: id, PlanTitle: "TUI Plans tab"}); got != "tui-plans-rows" {
		t.Fatalf("planLabel() = %q, want readable slug", got)
	}
	values := tableRowValues(monitor.Row{PlanID: id, Status: plan.StatusPlanned}, time.Time{}, "")
	if strings.Contains(values.plan, id) {
		t.Fatalf("plan list value contains full ID %q", values.plan)
	}
}

func TestRelativeAgeRemainsRelativeBeyondOneDay(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		age  time.Duration
		want string
	}{
		{age: 3 * 24 * time.Hour, want: "3d"},
		{age: 14 * 24 * time.Hour, want: "2w"},
		{age: 120 * 24 * time.Hour, want: "4mo"},
		{age: 365 * 24 * time.Hour, want: "1y"},
	}
	for _, test := range tests {
		updated := now.Add(-test.age)
		if got := relativeAge(&updated, now); got != test.want {
			t.Errorf("relativeAge(%s) = %q, want %q", test.age, got, test.want)
		}
	}
}

func TestPlanColumnsVaryAcrossMixedAndStressScenarios(t *testing.T) {
	now := time.Date(2026, time.August, 21, 23, 0, 0, 0, time.UTC)
	updated := func(age time.Duration) *time.Time {
		value := now.Add(-age)
		return &value
	}
	mixed := []monitor.Row{
		{RepositoryName: "alpha", PlanID: "blocked", Status: plan.StatusBlocked, OriginalTotalCount: 3, UpdatedAt: updated(3 * time.Minute)},
		{RepositoryName: "beta", PlanID: "live", Status: plan.StatusInProgress, Liveness: monitor.LivenessLive, SliceID: "002-render", InvocationDuration: 7 * time.Minute, OriginalCompletedCount: 1, OriginalTotalCount: 3, UpdatedAt: updated(2 * time.Hour)},
		{RepositoryName: "alpha", PlanID: "done", Status: plan.StatusCompleted, OriginalCompletedCount: 2, OriginalTotalCount: 2, UpdatedAt: updated(3 * 24 * time.Hour)},
	}
	stress := make([]monitor.Row, 12)
	for index := range stress {
		status := plan.StatusPlanned
		liveness := monitor.LivenessMissing
		if index%5 == 0 {
			status = plan.StatusInProgress
			liveness = monitor.LivenessLive
		} else if index%4 == 0 {
			status = plan.StatusCompleted
		}
		stress[index] = monitor.Row{
			RepositoryName: fmt.Sprintf("repo-%d", index%4), PlanID: fmt.Sprintf("stress-%02d", index),
			Status: status, Liveness: liveness, SliceID: fmt.Sprintf("%03d-slice", index), InvocationDuration: time.Duration(index+1) * time.Minute,
			OriginalCompletedCount: index % 4, OriginalTotalCount: 5, UpdatedAt: updated(time.Duration(index+1) * time.Hour),
		}
	}

	for name, rows := range map[string][]monitor.Row{"mixed": mixed, "stress": stress} {
		t.Run(name, func(t *testing.T) {
			values := make([]rowValues, len(rows))
			for index, row := range rows {
				values[index] = tableRowValues(row, now, "")
			}
			columns := []struct {
				name  string
				value func(rowValues) string
			}{
				{name: "REPO", value: func(value rowValues) string { return value.repo }},
				{name: "NEXT", value: func(value rowValues) string { return value.next }},
				{name: "PLAN", value: func(value rowValues) string { return value.plan }},
				{name: "SLICES", value: func(value rowValues) string { return value.slices }},
				{name: "RUN", value: func(value rowValues) string { return value.run }},
				{name: "AGE", value: func(value rowValues) string { return value.age }},
			}
			for _, column := range columns {
				seen := make(map[string]struct{})
				for _, value := range values {
					seen[column.value(value)] = struct{}{}
				}
				if len(seen) < 2 {
					t.Errorf("%s renders an identical value across all visible rows: %v", column.name, seen)
				}
			}
		})
	}
}

func TestPlanColumnsAlignAcrossNowNextAndHistory(t *testing.T) {
	frame := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{
			{RepositoryName: "alpha", PlanID: "blocked", Status: plan.StatusBlocked, AttentionReasons: []monitor.AttentionReason{monitor.AttentionBlocked}},
			{RepositoryName: "alpha", PlanID: "planned", Status: plan.StatusPlanned},
			{RepositoryName: "beta", PlanID: "completed", Status: plan.StatusCompleted},
		}},
		Width: 160,
	})

	var headers []string
	for _, line := range renderedLines(frame) {
		if strings.HasPrefix(line, "  REPO") {
			headers = append(headers, line)
		}
	}
	if len(headers) != 3 {
		t.Fatalf("plan header count = %d, want 3:\n%s", len(headers), frame)
	}
	for _, name := range []string{"REPO", "NEXT", "PLAN", "SLICES", "AGE"} {
		wantOffset := strings.Index(headers[0], name)
		for index, header := range headers[1:] {
			if got := strings.Index(header, name); got != wantOffset {
				t.Errorf("%s offset in section %d = %d, want %d:\n%s", name, index+1, got, wantOffset, frame)
			}
		}
	}
	for _, header := range headers {
		if strings.Contains(header, "ATTENTION") {
			t.Fatalf("plan header retained removed ATTENTION column:\n%s", frame)
		}
	}
}

func TestPlanColumnsKeepSemanticCoreThroughNarrowDegradation(t *testing.T) {
	widths := tableWidths{
		repo: 32, next: 8, plan: 64, slices: 15, run: 12, age: 4,
		hasRunning: true,
	}
	for _, width := range []int{100, 80, 70} {
		names := columnNames(planTableColumns(widths, width))
		for _, required := range []string{"NEXT", "PLAN"} {
			if !slices.Contains(names, required) {
				t.Errorf("width %d dropped required %s column: %v", width, required, names)
			}
		}
	}
	if names := columnNames(planTableColumns(widths, 70)); slices.Contains(names, "RUN") || slices.Contains(names, "AGE") {
		t.Fatalf("70-cell attention table retained low-priority operational context: %v", names)
	}
	if names := columnNames(planTableColumns(widths, 44)); strings.Join(names, ",") != "NEXT,PLAN" {
		t.Fatalf("44-cell table columns = %v, want trustworthy action and plan identity", names)
	}
}

func TestRender44CellNowTableKeepsRecognizableActions(t *testing.T) {
	rows := []monitor.Row{
		{
			RepositoryName: "repo", PlanID: "continue-plan", Status: plan.StatusBlocked,
			NextAction: "CONTINUE", AttentionReasons: []monitor.AttentionReason{monitor.AttentionChangesRequested},
		},
		{
			RepositoryName: "repo", PlanID: "finalize-plan", Status: plan.StatusBlocked,
			NextAction: "FINALIZE PR", AttentionReasons: []monitor.AttentionReason{monitor.AttentionChangesRequested},
		},
	}
	frame := Render(Model{Snapshot: monitor.Snapshot{Rows: rows}, Width: 44, Height: 20})
	lines := renderedLines(frame)
	for action, planID := range map[string]string{"CONTINUE": "continue-plan", "FINALIZE PR": "finalize-plan"} {
		var rowLine string
		for _, line := range lines {
			if strings.Contains(line, planID) {
				rowLine = line
				break
			}
		}
		if rowLine == "" || !strings.Contains(rowLine, action) {
			t.Errorf("44-cell NOW row lost recognizable %s action: %q\n%s", action, rowLine, frame)
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "  NEXT") && strings.Contains(line, "ATTENTION") {
			t.Fatalf("44-cell table retained removed ATTENTION column: %q", line)
		}
	}
}

func TestPlanRunColumnIsConditional(t *testing.T) {
	now := time.Now()
	withoutRun := measureTable([]Section{{Rows: []monitor.Row{{PlanID: "planned", Status: plan.StatusPlanned}}}}, now, nil)
	if names := columnNames(planTableColumns(withoutRun, 199)); slices.Contains(names, "RUN") {
		t.Fatalf("non-running plans rendered RUN column: %v", names)
	}
	withRun := measureTable([]Section{{Rows: []monitor.Row{{PlanID: "live", Status: plan.StatusInProgress, Liveness: monitor.LivenessLive}}}}, now, nil)
	if names := columnNames(planTableColumns(withRun, 199)); !slices.Contains(names, "RUN") {
		t.Fatalf("running plan omitted RUN column: %v", names)
	}
}

func columnNames(columns []column) []string {
	names := make([]string, len(columns))
	for index, column := range columns {
		names[index] = column.name
	}
	return names
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
	if width := cells.Width(got); width != maxSliceIDCells {
		t.Fatalf("phaseLabel() width = %d, want %d: %q", width, maxSliceIDCells, got)
	}
}

func TestRenderOmitsEmptySectionsAndAlwaysShowsDonePlans(t *testing.T) {
	const wantEmpty = clearScreenSequence + `tao │ notes ▸plans  settings  debug  all repos  agent -  ●

  No plans.
0 plans
`
	if got := Render(Model{}); got != wantEmpty {
		t.Fatalf("empty Render() mismatch\nwant:\n%q\n got:\n%q", wantEmpty, got)
	}

	got := Render(Model{Snapshot: monitor.Snapshot{Rows: []monitor.Row{
		{PlanID: "done", Status: plan.StatusCompleted},
		{PlanID: "abandoned", Status: plan.StatusAbandoned},
	}}})
	for _, want := range []string{"DONE", "done", "abandoned", "2 plans"} {
		if !strings.Contains(got, want) {
			t.Fatalf("terminal plans Render() missing %q: %q", want, got)
		}
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
	if !strings.Contains(got, "repo beta") || !strings.Contains(got, "1 plan") || !strings.Contains(got, "  beta   RUN   two") {
		t.Fatalf("focused render missing header or row:\n%s", got)
	}
	if strings.Contains(got, "alpha") {
		t.Fatalf("focused render included another repository:\n%s", got)
	}

	empty := Render(Model{FocusRepositoryID: "repo-b", FocusRepositoryName: "beta"})
	if !strings.Contains(empty, "repo beta") || !strings.Contains(empty, "0 plans") || !strings.Contains(empty, "No plans.") {
		t.Fatalf("empty focused render is ambiguous:\n%s", empty)
	}
}

func TestRenderHeaderTracksActivePage(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{RepositoryName: "repo", PlanID: "plan", Status: plan.StatusPlanned}}}

	plans := Render(Model{Snapshot: snapshot})
	for _, want := range []string{"tao │ notes ▸plans  settings  debug", "1 plan", "  repo   RUN   plan"} {
		if !strings.Contains(plans, want) {
			t.Fatalf("plans page missing %q:\n%s", want, plans)
		}
	}

	notes := Render(Model{Snapshot: snapshot, Page: PageNotes, ActionMessage: "plan action"})
	for _, want := range []string{"tao │▸notes  plans  settings  debug", "0 open notes", "Notes page."} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes page missing %q:\n%s", want, notes)
		}
	}
	for _, unavailable := range []string{"> repo  plan", "r run", "c completed", "Enter plan", "plan action"} {
		if strings.Contains(notes, unavailable) {
			t.Fatalf("notes page exposed plan-only content %q:\n%s", unavailable, notes)
		}
	}

	for _, test := range []struct {
		page PageID
		want string
	}{
		{page: PagePlans, want: "plans"},
		{page: PageNotes, want: "notes"},
		{page: PageSettings, want: "settings"},
		{page: PageDebug, want: "debug"},
	} {
		got := Render(Model{Snapshot: snapshot, Page: test.page, Profile: ProfileTrueColor})
		if !strings.Contains(got, Paint(ProfileTrueColor, RoleAccent, test.want)) {
			t.Fatalf("colored tab strip for %s does not accent the active tab:\n%q", test.page, got)
		}
	}
}

func TestRenderNotesSummaryCountsRepositoriesStablyAndRespectsFocus(t *testing.T) {
	snapshot := note.Snapshot{Notes: []note.CatalogNote{
		{RepositoryID: "repo-b", RepositoryName: "beta", ID: "b"},
		{RepositoryID: "repo-a", RepositoryName: "alpha", ID: "a-1"},
		{RepositoryID: "repo-a", RepositoryName: "alpha", ID: "a-2"},
	}}

	all := Render(Model{Page: PageNotes, NoteSnapshot: snapshot})
	if !strings.Contains(all, "3 open notes  ·  alpha 2 · beta 1") {
		t.Fatalf("all-repository Notes summary lacks stable counts:\n%s", all)
	}

	focused := Render(Model{
		Page: PageNotes, NoteSnapshot: snapshot,
		FocusRepositoryID: "repo-b", FocusRepositoryName: "beta",
	})
	if !strings.Contains(focused, "1 open note  ·  beta 1") || strings.Contains(focused, "alpha") {
		t.Fatalf("focused Notes summary or rows ignored repository focus:\n%s", focused)
	}
}

func TestRenderNotesSummaryPreservesActiveSearchBeforeRepositoryBreakdown(t *testing.T) {
	snapshot := note.Snapshot{Notes: []note.CatalogNote{
		{RepositoryID: "repo-b", RepositoryName: "long-beta-repository", ID: "b", Text: "needle beta"},
		{RepositoryID: "repo-a", RepositoryName: "long-alpha-repository", ID: "a-1", Text: "needle alpha one"},
		{RepositoryID: "repo-a", RepositoryName: "long-alpha-repository", ID: "a-2", Text: "needle alpha two"},
	}}

	lines := renderedLines(Render(Model{
		Page: PageNotes, NoteSnapshot: snapshot, SearchQuery: "needle", SearchActive: true, Width: 50,
	}))
	summary := lines[len(lines)-1]
	search := "Search: /needle█"
	if !strings.Contains(summary, search) {
		t.Fatalf("narrow Notes summary lost the complete active search indicator: %q", summary)
	}
	if repositoryAt := strings.Index(summary, "long-alpha"); repositoryAt < 0 || strings.Index(summary, search) > repositoryAt {
		t.Fatalf("narrow Notes summary does not keep search ahead of repository breakdown: %q", summary)
	}
	if strings.Contains(summary, "long-beta-repository") {
		t.Fatalf("narrow Notes summary preserved trailing repository data instead of search state: %q", summary)
	}
}

func TestRenderConstrainedDimensionsKeepPageIdentity(t *testing.T) {
	got := Render(Model{Page: PageNotes, Width: 18, Height: 1})
	lines := renderedLines(got)
	if len(lines) != 1 || lines[0] != "tao │▸notes  plans" {
		t.Fatalf("constrained frame = %#v, want truncated header with active page", lines)
	}
	for _, line := range lines {
		if cells.Width(line) > 18 {
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
			if !strings.HasPrefix(lines[0], "tao │ notes ▸plans") {
				t.Fatalf("viewport header = %q, want shared tab strip", lines[0])
			}
			if strings.HasSuffix(got, "\n") {
				t.Fatal("full-height frame ends with a newline that can scroll the terminal")
			}
			selectedLabel := fmt.Sprintf("  repo   RUN   plan-%02d", selected)
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
	if lines := renderedLines(Render(model)); len(lines) > 20 {
		t.Fatalf("tall rendered lines = %d, want at most 20", len(lines))
	}

	model.Height = 6
	got := Render(model)
	if lines := renderedLines(got); len(lines) != 6 {
		t.Fatalf("resized rendered lines = %d, want 6:\n%s", len(lines), got)
	}
	if !strings.Contains(got, "  repo   RUN   plan-07") {
		t.Fatalf("resized viewport lost selected row:\n%s", got)
	}
}

func TestRenderPlanListOmitsSelectedPlanDetails(t *testing.T) {
	row := monitor.Row{
		RepositoryName: "repo", PlanID: "priority-plan", Status: plan.StatusPlanned,
		Overview: plan.DecisionOverview{ExpectedBenefit: "Only visible after opening the plan."},
	}
	got := Render(Model{Snapshot: monitor.Snapshot{Rows: []monitor.Row{row}}, Width: 120})
	for _, want := range []string{"AGE", "NEXT", "priority-plan"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan list missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"╭─ priority-plan ", "Benefit:", "Only visible after opening the plan."} {
		if strings.Contains(got, unwanted) {
			t.Errorf("plan list includes selected-plan detail %q:\n%s", unwanted, got)
		}
	}
}

func TestRenderNarrowPlanTableKeepsPlanVisibleWithLongRepositoryName(t *testing.T) {
	repositoryName := strings.Repeat("repository-", 8)
	const planID = "plan-visible"
	frame := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{{
			RepositoryName: repositoryName,
			PlanID:         planID,
			Status:         plan.StatusPlanned,
		}}},
		Width:  70,
		Height: 20,
	})

	var header, selectedRow string
	for _, line := range renderedLines(frame) {
		if strings.HasPrefix(line, "  NEXT") {
			header = line
		}
		if strings.HasPrefix(line, "   RUN") && strings.Contains(line, planID) {
			selectedRow = line
		}
	}
	if header == "" || !strings.Contains(header, "PLAN") || strings.Contains(header, "REPO") {
		t.Fatalf("70-column table did not prioritize NEXT and PLAN over oversized repository context:\n%s", frame)
	}
	if selectedRow == "" || !strings.Contains(selectedRow, planID) {
		t.Fatalf("70-column table lost the meaningful plan identifier with an overlong repository name:\n%s", frame)
	}
	if strings.Contains(selectedRow, repositoryName) {
		t.Fatalf("70-column table retained oversized repository context: %q", selectedRow)
	}
	if got := cells.Width(selectedRow); got != 70 {
		t.Fatalf("selected row width = %d, want 70: %q", got, selectedRow)
	}
}

func TestRenderAbandonmentAsSafeHistoricalOutcome(t *testing.T) {
	at := time.Date(2026, 9, 1, 17, 0, 0, 0, time.FixedZone("offset", -4*60*60))
	row := monitor.Row{
		RepositoryID: "repo", RepositoryName: "repo", PlanID: "old-plan", Status: plan.StatusAbandoned,
		AbandonedAt: &at, AbandonmentReason: "superseded\nby\t" + strings.Repeat("界", 120) + "\x1b[31m",
		Liveness: monitor.LivenessLive, Phase: "merge", InvocationDuration: time.Hour,
		AttentionReasons: []monitor.AttentionReason{monitor.AttentionApprovalRequired, monitor.AttentionFinalizationFailed},
		NextAction:       "FINALIZE PR",
	}
	got := Render(Model{Snapshot: monitor.Snapshot{Rows: []monitor.Row{row}}, Width: 120})
	for _, want := range []string{"DONE", "ABANDONED   old-plan"} {
		if !strings.Contains(got, want) {
			t.Fatalf("abandonment render missing %q:\n%s", want, got)
		}
	}
	bodyLines := renderedLines(got)
	bodyWithoutFooter := strings.Join(bodyLines[:len(bodyLines)-1], "\n")
	for _, forbidden := range []string{"NOW", "FINALIZE PR", "Abandoned at:", "Abandonment reason:", "1h", "merge", "\x1b[31m"} {
		if strings.Contains(bodyWithoutFooter, forbidden) {
			t.Fatalf("abandonment render retained %q:\n%s", forbidden, got)
		}
	}

	narrow := Render(Model{Snapshot: monitor.Snapshot{Rows: []monitor.Row{row}}, Width: 24})
	for _, line := range renderedLines(narrow) {
		if cells.Width(line) > 24 {
			t.Fatalf("narrow abandonment line exceeds width: %q", line)
		}
	}
}

func TestRenderPlanTableKeepsSelectionAndConfirmation(t *testing.T) {
	rows := []monitor.Row{
		{RepositoryName: "repo", PlanID: "first", Status: plan.StatusPlanned},
		{RepositoryName: "repo", PlanID: "selected", Status: plan.StatusPlanned, Overview: plan.DecisionOverview{ExpectedBenefit: strings.Repeat("benefit ", 20), Readiness: plan.DecisionReadinessReady}},
	}
	got := Render(Model{Snapshot: monitor.Snapshot{Rows: rows}, Selected: 1, Width: 36, Height: 7, ConfirmMessage: "Run selected plan?"})
	lines := renderedLines(got)
	if len(lines) != 7 {
		t.Fatalf("rendered lines = %d, want 7:\n%s", len(lines), got)
	}
	for _, want := range []string{"tao │ notes ▸plans", "  repo   RUN     selected", "Run selected plan? [y/n]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("responsive frame missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "╭─ selected ") {
		t.Fatalf("responsive frame includes the removed selected-plan detail pane:\n%s", got)
	}
	for _, line := range lines {
		if width := cells.Width(line); width > 36 {
			t.Fatalf("responsive line width = %d, want <= 36: %q", width, line)
		}
	}
}

func TestRenderPlanTruncationCountsOnlyHiddenPlanRows(t *testing.T) {
	rows := make([]monitor.Row, 0, 36)
	for index := 0; index < 36; index++ {
		status := plan.StatusPlanned
		liveness := monitor.LivenessMissing
		nextAction := ""
		if index%7 == 0 {
			status = plan.StatusReviewed
			nextAction = "MERGE"
		} else if index%5 == 0 {
			status = plan.StatusInProgress
			liveness = monitor.LivenessLive
		}
		rows = append(rows, monitor.Row{
			RepositoryName: "repo",
			PlanID:         fmt.Sprintf("stress-%02d", index),
			Status:         status,
			Liveness:       liveness,
			NextAction:     nextAction,
		})
	}

	got := Render(Model{Snapshot: monitor.Snapshot{Rows: rows}, Width: 70, Height: 20})
	if !strings.Contains(got, "NOW") || !strings.Contains(got, "NEXT") {
		t.Fatalf("constrained frame did not span plan sections:\n%s", got)
	}
	if visible := strings.Count(got, "\n  repo "); visible != 13 {
		t.Fatalf("visible plan rows = %d, want 13:\n%s", visible, got)
	}
	if !strings.Contains(got, "+ 23 more  ↓") {
		t.Fatalf("hidden plan count does not exclude section and preview lines:\n%s", got)
	}
	if strings.Contains(got, "+ 38 more  ↓") {
		t.Fatalf("hidden plan count includes structural lines:\n%s", got)
	}
}

func TestRenderSelectedLastPlanKeepsSectionSkeleton(t *testing.T) {
	rows := make([]monitor.Row, 30)
	for index := range rows {
		rows[index] = monitor.Row{
			RepositoryName: "repo",
			PlanID:         fmt.Sprintf("plan-%02d", index),
			Status:         plan.StatusPlanned,
		}
	}

	got := Render(Model{Snapshot: monitor.Snapshot{Rows: rows}, Selected: 29, Width: 70, Height: 20})
	for _, want := range []string{"NEXT", "REPO", "  repo   RUN     plan-29"} {
		if !strings.Contains(got, want) {
			t.Fatalf("selected-last viewport missing %q:\n%s", want, got)
		}
	}
	if visible := strings.Count(got, "\n  repo "); visible != 15 {
		t.Fatalf("visible plan rows = %d, want 15 after preserving the section skeleton:\n%s", visible, got)
	}
	if !strings.Contains(got, "+ 15 more  ↓") {
		t.Fatalf("selected-last viewport has the wrong hidden-plan count:\n%s", got)
	}
}

func TestRenderNarrowWidthTruncatesRunesAndPreservesColor(t *testing.T) {
	const width = 29
	got := Render(Model{
		Snapshot: monitor.Snapshot{Rows: []monitor.Row{{RepositoryName: "répo", PlanID: "plan", Status: plan.StatusPlanned}}},
		Width:    width,
		Profile:  ProfileTrueColor,
	})
	body := strings.TrimPrefix(got, clearScreenSequence)
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		lineWidth := cells.Width(line)
		if lineWidth > width {
			t.Fatalf("rendered line %q has %d visible cells, want at most %d", line, lineWidth, width)
		}
		if padded := cells.Pad(line, lineWidth); padded != line {
			t.Fatalf("rendered line has an unterminated color sequence: %q", line)
		}
	}
	if !strings.Contains(got, " RUN ") {
		t.Fatalf("narrow NEXT text was not preserved: %q", got)
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
			want: []string{"Keyboard shortcuts", "KEY", "ACTION", "gg / G", "Jump to top / bottom", "Shift+Tab", "r", "Run selected plan", "/", "Search plans and notes", "Backspace", "Go back / clear search", "? / Esc", "Close shortcuts"},
		},
		{
			page:        PageNotes,
			want:        []string{"Keyboard shortcuts", "gg / G", "Jump to top / bottom", "Open selected item", "Ctrl+G", "Edit selected note", "Cycle repository filter", "/", "Search plans and notes", "Backspace", "Go back / clear search", "? / Esc"},
			unavailable: "Run selected plan",
		},
	} {
		frame := Render(Model{Page: test.page, ShowShortcuts: true, Width: 64, Height: 18, Profile: ProfileTrueColor})
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
			if width := cells.Width(line); width > 64 {
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
	for _, want := range []string{"  repo   RUN   plan-07", "Run failed.", "Approve this slice? [y/n]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded viewport missing %q:\n%s", want, got)
		}
	}
}

func renderedLines(frame string) []string {
	body := strings.TrimPrefix(frame, clearScreenSequence)
	return strings.Split(strings.TrimSuffix(body, "\n"), "\n")
}
