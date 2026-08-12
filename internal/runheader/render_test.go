package runheader

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

func TestRenderWideHeaderExact(t *testing.T) {
	state := testHeaderState()
	want := expectedHeader(
		"repo tao · plan run-header · id 20260812-185523-run-header · title Pinned run header",
		"agent pi · mode isolated · branch tao/20260812 · review on",
		"slices 1/4 · current Render header · elapsed -",
		"sessions 2 · tokens 12345 · cost $1.25",
		"slices ✓001-seam ▶002-render ○003-wire ○004-docs",
	)
	assertLines(t, Render(state, 140, false), want)
}

func TestRenderBatchPositionExact(t *testing.T) {
	state := testHeaderState()
	state.BatchPosition = 2
	state.BatchTotal = 7
	want := expectedHeader(
		"repo tao · plan run-header · id 20260812-185523-run-header · title Pinned run header",
		"agent pi · mode isolated · branch tao/20260812 · review on",
		"plan 2/7 · slices 1/4 · current Render header · elapsed -",
		"sessions 2 · tokens 12345 · cost $1.25",
		"slices ✓001-seam ▶002-render ○003-wire ○004-docs",
	)
	assertLines(t, Render(state, 140, false), want)
}

func TestRenderNarrowHeaderExact(t *testing.T) {
	state := testHeaderState()
	want := []string{
		"┌──────────────────────────────┐",
		"│repo tao · plan run-header · …│",
		"│agent pi · mode isolated · br…│",
		"│slices 1/4 · current Render h…│",
		"│sessions 2 · tokens … · cost …│",
		"│slices ✓001-seam ▶002-render …│",
		"└──────────────────────────────┘",
	}
	assertLines(t, Render(state, 32, false), want)
}

func TestRenderLongPlanTitleExact(t *testing.T) {
	state := testHeaderState()
	state.PlanTitle = "Render an exceptionally long Ελληνικό plan title without splitting multibyte runes"
	want := []string{
		"┌──────────────────────────────────────────────────────────────────────────┐",
		"│repo tao · plan run-header · id 20260812-185523… · title Render an except…│",
		"│agent pi · mode isolated · branch tao/20260812 · review on                │",
		"│slices 1/4 · current Render header · elapsed -                            │",
		"│sessions 2 · tokens 12345 · cost $1.25                                    │",
		"│slices ✓001-seam ▶002-render ○003-wire ○004-docs                          │",
		"└──────────────────────────────────────────────────────────────────────────┘",
	}
	assertLines(t, Render(state, 76, false), want)
}

func TestRenderChecklistWindowsAroundCurrentExact(t *testing.T) {
	state := testHeaderState()
	state.Slices = []run.HeaderSlice{
		{ID: "000", Status: plan.StatusCompleted},
		{ID: "001", Status: plan.StatusCompleted},
		{ID: "002", Status: plan.StatusCompleted},
		{ID: "003", Status: plan.StatusInProgress},
		{ID: "004", Status: plan.StatusPending},
		{ID: "005", Status: plan.StatusPending},
		{ID: "006", Status: plan.StatusPending},
	}
	state.CurrentSliceID = "003"
	state.CurrentSliceTitle = "Current"
	state.CompletedCount = 3
	state.TotalCount = 7
	want := []string{
		"┌─────────────────────────┐",
		"│repo tao · plan run-head…│",
		"│agent pi · mode isolated…│",
		"│slices 3/7 · current Cur…│",
		"│sessions 2 · tokens 1234…│",
		"│slices … ✓002 ▶003 ○004 …│",
		"└─────────────────────────┘",
	}
	assertLines(t, Render(state, 27, false), want)
}

func TestRenderCodexCostNotReportedExact(t *testing.T) {
	state := testHeaderState()
	state.Agent = "codex"
	state.Cost = 12.34
	state.CostReported = false
	want := expectedHeader(
		"repo tao · plan run-header · id 20260812-185523-run-header · title Pinned run header",
		"agent codex · mode isolated · branch tao/20260812 · review on",
		"slices 1/4 · current Render header · elapsed -",
		"sessions 2 · tokens 12345 · cost not reported",
		"slices ✓001-seam ▶002-render ○003-wire ○004-docs",
	)
	assertLines(t, Render(state, 140, false), want)
}

func TestRenderReworkRoundPresentAndAbsentExact(t *testing.T) {
	state := testHeaderState()
	without := expectedHeader(
		"repo tao · plan run-header · id 20260812-185523-run-header · title Pinned run header",
		"agent pi · mode isolated · branch tao/20260812 · review on",
		"slices 1/4 · current Render header · elapsed -",
		"sessions 2 · tokens 12345 · cost $1.25",
		"slices ✓001-seam ▶002-render ○003-wire ○004-docs",
	)
	assertLines(t, Render(state, 140, false), without)

	state.ReworkRound = 2
	state.MaxReworkAttempts = 5
	with := expectedHeader(
		"repo tao · plan run-header · id 20260812-185523-run-header · title Pinned run header",
		"agent pi · mode isolated · branch tao/20260812 · review on · rework 2/5",
		"slices 1/4 · current Render header · elapsed -",
		"sessions 2 · tokens 12345 · cost $1.25",
		"slices ✓001-seam ▶002-render ○003-wire ○004-docs",
	)
	assertLines(t, Render(state, 140, false), with)
}

func TestRenderSanitizesMetadataControls(t *testing.T) {
	state := testHeaderState()
	state.RepoName = "tao\nrepo"
	state.PlanID = "20260812-185523-run\x1b[2Jheader"
	state.PlanTitle = "Pinned\r\nheader"
	state.Agent = "p\ti"
	state.ExecutionMode = "iso\u009blated"
	state.Branch = "tao/20260812\x7f"
	state.CurrentSliceTitle = "Render\nheader"
	state.CurrentSliceID = "002\x1b[Hrender"
	state.Slices[1].ID = state.CurrentSliceID

	lines := Render(state, 180, false)
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"repo tao�repo",
		"plan run�[2Jheader",
		"id 20260812-185523-run�[2Jheader",
		"title Pinned��header",
		"agent p�i",
		"mode iso�lated",
		"branch tao/20260812�",
		"current Render�header",
		"▶002�[Hrender",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Render() missing sanitized metadata %q in %q", want, text)
		}
	}
	for lineNumber, line := range lines {
		for _, r := range line {
			if r < ' ' || r == 0x7f || r >= 0x80 && r <= 0x9f {
				t.Fatalf("line %d contains terminal control %U: %q", lineNumber, r, line)
			}
		}
	}
}

func TestRenderSanitizesPhaseControls(t *testing.T) {
	state := testHeaderState()
	state.CurrentSliceTitle = ""
	state.Phase = "running\n\x1b[Hslice"

	text := strings.Join(Render(state, 140, false), "\n")
	if !strings.Contains(text, "current running��[Hslice") {
		t.Fatalf("Render() did not sanitize phase: %q", text)
	}
}

func TestRenderColorPreservesPlainVisibleText(t *testing.T) {
	state := testHeaderState()
	plain := Render(state, 80, false)
	colored := Render(state, 80, true)
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	for i := range plain {
		if strings.Contains(plain[i], "\x1b[") {
			t.Fatalf("plain line %d contains ANSI: %q", i, plain[i])
		}
		if got := ansi.ReplaceAllString(colored[i], ""); got != plain[i] {
			t.Fatalf("colored line %d changed visible text\n got: %q\nwant: %q", i, got, plain[i])
		}
	}
	if !strings.Contains(strings.Join(colored, ""), "\x1b[") {
		t.Fatal("colored render contains no ANSI sequences")
	}
}

func TestRenderAlwaysReturnsFixedLineCount(t *testing.T) {
	for _, width := range []int{-1, 0, 1, 2, 3, 20} {
		got := Render(run.HeaderState{}, width, false)
		if len(got) != LineCount {
			t.Fatalf("Render(width %d) returned %d lines, want %d", width, len(got), LineCount)
		}
	}
}

func testHeaderState() run.HeaderState {
	return run.HeaderState{
		RepoName:      "tao",
		PlanID:        "20260812-185523-run-header",
		PlanTitle:     "Pinned run header",
		Agent:         "pi",
		ExecutionMode: "isolated",
		Branch:        "tao/20260812",
		ReviewEnabled: true,
		Slices: []run.HeaderSlice{
			{ID: "001-seam", Status: plan.StatusCompleted},
			{ID: "002-render", Status: plan.StatusInProgress},
			{ID: "003-wire", Status: plan.StatusPending},
			{ID: "004-docs", Status: plan.StatusPending},
		},
		CompletedCount:    1,
		TotalCount:        4,
		Phase:             run.PhaseRunningSlice,
		CurrentSliceID:    "002-render",
		CurrentSliceTitle: "Render header",
		AgentSessionCount: 2,
		TotalTokens:       12345,
		Cost:              1.25,
		CostReported:      true,
	}
}

func expectedHeader(contents ...string) []string {
	const width = 140
	top := "┌" + strings.Repeat("─", width-2) + "┐"
	bottom := "└" + strings.Repeat("─", width-2) + "┘"
	result := []string{top}
	for _, content := range contents {
		result = append(result, "│"+content+strings.Repeat(" ", width-2-len([]rune(content)))+"│")
	}
	return append(result, bottom)
}

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Render() mismatch\n got: %#v\nwant: %#v", got, want)
	}
	for i, line := range got {
		if len([]rune(line)) == 0 {
			continue
		}
		if len([]rune(line)) != len([]rune(got[0])) {
			t.Fatalf("line %d has %d runes, first line has %d", i, len([]rune(line)), len([]rune(got[0])))
		}
	}
}
