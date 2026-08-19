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
	want := expectedRows(100,
		"tao / run-header · Pinned run header · pi · isolated · tao/20260812 · review on",
		"▶ 002 Render header · elapsed -",
		"[█████░░░░░░░░░░░░░░░] 1/4 · 25%",
		"SLICES  ✓ 001 Terminal seam   ▶ 002 Render header   ○ 003 Wire output   ○ 004 Document",
		"AGENT  2 sessions · 12.3k tokens · $1.25",
		strings.Repeat("─", 100),
		"LIVE OUTPUT",
	)
	assertLines(t, Render(state, 100, false), want, 100)
}

func TestRenderNarrowHeaderExact(t *testing.T) {
	state := testHeaderState()
	want := expectedRows(60,
		"tao / run-header · pi · isolated · tao/20260812 · review on",
		"▶ 002 Render header · elapsed -",
		"[█████░░░░░░░░░░░░░░░] 1/4 · 25%",
		"SLICES  ✓ 001 Terminal seam   ▶ 002 Render header   …",
		"AGENT  2 sessions · 12.3k tokens · $1.25",
		strings.Repeat("─", 60),
		"LIVE OUTPUT",
	)
	assertLines(t, Render(state, 60, false), want, 60)
}

func TestRenderProgressCompletedAndZeroTotals(t *testing.T) {
	state := testHeaderState()
	state.CompletedCount = 4
	state.TotalCount = 4
	lines := Render(state, 60, false)
	if !strings.Contains(lines[2], "[████████████████████] 4/4 · 100%") {
		t.Fatalf("completed progress = %q", lines[2])
	}

	state.CompletedCount = 0
	state.TotalCount = 0
	state.Slices = nil
	state.CurrentSliceID = ""
	state.CurrentSliceTitle = ""
	state.Phase = "reviewing"
	lines = Render(state, 60, false)
	for line, want := range map[int]string{
		1: "PHASE  reviewing · elapsed -",
		2: "[░░░░░░░░░░░░░░░░░░░░] 0/0 · 0%",
		3: "SLICES  -",
	} {
		if !strings.Contains(lines[line], want) {
			t.Errorf("line %d = %q, want content %q", line, lines[line], want)
		}
	}
}

func TestRenderOptionalBatchReworkAndUnavailableCost(t *testing.T) {
	state := testHeaderState()
	state.Agent = "agent"
	state.BatchPosition = 2
	state.BatchTotal = 7
	state.ReworkRound = 2
	state.MaxReworkAttempts = 5
	state.CostReported = false
	text := strings.Join(Render(state, 140, false), "\n")
	for _, want := range []string{"batch 2/7", "rework 2/5", "agent", "cost —"} {
		if !strings.Contains(text, want) {
			t.Errorf("Render() missing %q in %q", want, text)
		}
	}
}

func TestRenderLongTitlePreservesRunContextAtSixtyColumns(t *testing.T) {
	state := testHeaderState()
	state.RepoName = "t"
	state.PlanID = "20260812-185523-p"
	state.PlanTitle = strings.Repeat("long title ", 20)
	state.Agent = "p"
	state.ExecutionMode = "i"
	state.Branch = "b"
	state.BatchPosition = 2
	state.BatchTotal = 7
	state.ReworkRound = 2
	state.MaxReworkAttempts = 5

	line := strings.TrimSpace(Render(state, 60, false)[0])
	want := "t / p · batch 2/7 · rework 2/5 · p · i · b · review on"
	if line != want {
		t.Fatalf("identity line = %q, want %q", line, want)
	}
}

func TestRenderCompactsTokenCounts(t *testing.T) {
	state := testHeaderState()
	for _, test := range []struct {
		tokens int64
		want   string
	}{{999, "999 tokens"}, {1_000, "1k tokens"}, {12_345, "12.3k tokens"}, {1_250_000, "1.2m tokens"}} {
		state.TotalTokens = test.tokens
		if got := Render(state, 100, false)[4]; !strings.Contains(got, test.want) {
			t.Errorf("tokens %d: metrics = %q, want %q", test.tokens, got, test.want)
		}
	}
}

func TestRenderChecklistCentersCurrentAndUsesNumericPrefixes(t *testing.T) {
	state := testHeaderState()
	state.Slices = []run.HeaderSlice{
		{ID: "000-before", Title: "Before", Status: plan.StatusCompleted},
		{ID: "001-seam", Title: "Seam", Status: plan.StatusCompleted},
		{ID: "002-render", Title: "A deliberately long current title", Status: plan.StatusInProgress},
		{ID: "003-wire", Title: "Wire", Status: plan.StatusPending},
		{ID: "004-after", Title: "After", Status: plan.StatusPending},
	}
	state.CurrentSliceID = "002-render"
	line := Render(state, 60, false)[3]
	if !strings.Contains(line, "▶ 002") || strings.Contains(line, "002-render") {
		t.Fatalf("checklist did not use the numeric slice prefix: %q", line)
	}
	if !strings.Contains(line, "…") {
		t.Fatalf("checklist did not indicate its current-centered window: %q", line)
	}
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
	state.CurrentSliceID = "002\x1b[H-render"
	state.Slices[1].ID = state.CurrentSliceID
	state.Slices[1].Title = "Render\nheader"

	lines := Render(state, 180, false)
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"tao�repo / run�[2Jheader", "Pinned��header", "p�i", "iso�lated",
		"tao/20260812�", "▶ 002�[H Render�header",
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

func TestRenderUsesTerminalCellWidthForUnicode(t *testing.T) {
	state := testHeaderState()
	state.PlanTitle = "界界 cafe\u0301"
	state.Slices[1].Title = "界面"
	const width = 60
	for i, line := range Render(state, width, false) {
		if got := cellWidth(line); got != width {
			t.Fatalf("line %d occupies %d cells, want %d: %q", i, got, width, line)
		}
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
	for _, width := range []int{-1, 0, 1, 2, 3, 20, 60} {
		got := Render(run.HeaderState{}, width, false)
		if len(got) != LineCount {
			t.Fatalf("Render(width %d) returned %d lines, want %d", width, len(got), LineCount)
		}
		for i, line := range got {
			if gotWidth := cellWidth(line); gotWidth != max(width, 0) {
				t.Fatalf("Render(width %d) line %d occupies %d cells", width, i, gotWidth)
			}
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
			{ID: "001-seam", Title: "Terminal seam", Status: plan.StatusCompleted},
			{ID: "002-render", Title: "Render header", Status: plan.StatusInProgress},
			{ID: "003-wire", Title: "Wire output", Status: plan.StatusPending},
			{ID: "004-docs", Title: "Document", Status: plan.StatusPending},
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

func expectedRows(width int, contents ...string) []string {
	result := make([]string, len(contents))
	for i, content := range contents {
		result[i] = content + strings.Repeat(" ", width-cellWidth(content))
	}
	return result
}

func assertLines(t *testing.T, got, want []string, width int) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Render() mismatch\n got: %#v\nwant: %#v", got, want)
	}
	for i, line := range got {
		if gotWidth := cellWidth(line); gotWidth != width {
			t.Fatalf("line %d occupies %d cells, want %d", i, gotWidth, width)
		}
	}
}
