package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/term"
)

func TestRenderNoteDetailShowsFullSanitizedMultilineText(t *testing.T) {
	created := time.Date(2026, 8, 18, 10, 30, 0, 0, time.UTC)
	updated := created.Add(2 * time.Hour)
	frame := RenderNoteDetail(note.CatalogNote{
		RepositoryName: "répo\x1b]0;owned\a",
		ID:             "note-完整\x1b[31m",
		Tags:           []string{"one", "tw\x1b[2Jo"},
		CreatedAt:      created,
		UpdatedAt:      updated,
		Text:           "first line\n第二行\x1b]52;c;payload\a\nthird\tline",
	}, 80, 30)
	body := strings.TrimPrefix(frame, clearScreenSequence)
	for _, want := range []string{"NOTE DETAIL", "Repository: répo", "Note: note-完整", "Status: open", "Tags: one, two", created.Format(time.RFC3339), updated.Format(time.RFC3339), "  first line", "  第二行", "  third line", "Esc back"} {
		if !strings.Contains(body, want) {
			t.Fatalf("note detail missing %q:\n%s", want, frame)
		}
	}
	for _, absent := range []string{"]0;", "[31m", "[2J", "]52;", "payload"} {
		if strings.Contains(body, absent) {
			t.Fatalf("note detail retained terminal sequence %q: %q", absent, body)
		}
	}
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("note detail contains control %U: %q", r, body)
		}
	}
}

func TestRenderNoteDetailScrollReachesWrappedTailWithFixedChrome(t *testing.T) {
	item := note.CatalogNote{
		RepositoryName: "repo",
		ID:             "long-note",
		Text:           strings.Repeat("a", 100) + "\nfinal wrapped line",
	}
	frame := renderNoteDetail(item, 40, 11, 100)
	body := strings.TrimPrefix(frame, clearScreenSequence)
	for _, want := range []string{"Tao UI | NOTE DETAIL", "Text:", "  final wrapped line", noteDetailFooter} {
		if !strings.Contains(body, want) {
			t.Fatalf("scrolled note detail missing %q:\n%s", want, frame)
		}
	}
	if lines := strings.Count(body, "\n") + 1; lines != 11 {
		t.Fatalf("scrolled detail lines = %d, want 11:\n%s", lines, frame)
	}
}

func TestRenderSlicesPaneUsesStateQueueOrderAndShowsCurrentMetadata(t *testing.T) {
	current := "002-build"
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{
			CurrentSlice:    &current,
			CompletedSlices: []string{"001-setup"},
			PendingSlices:   []string{"002-build", "003-release"},
		}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "003-release", Title: "Release", Status: plan.StatusBlocked, BlockerNote: "waiting on credentials", Approval: &plan.Approval{Required: true}},
			{ID: "002-build", Title: "Build it", Status: plan.StatusInProgress},
			{ID: "001-setup", Title: "Set up", Status: plan.StatusCompleted},
		}},
	}

	lines := RenderSlicesPane(detail, 120, 10, true)
	text := strings.Join(lines, "\n")
	setup := strings.Index(text, "001-setup")
	build := strings.Index(text, "002-build")
	release := strings.Index(text, "003-release")
	if setup < 0 || build < setup || release < build {
		t.Fatalf("slice order does not follow completed/pending queues:\n%s", text)
	}
	if !strings.Contains(text, "> ") || !strings.Contains(text, "\x1b[36m") {
		t.Fatalf("current colored slice is not highlighted:\n%q", text)
	}
	if !strings.Contains(text, "[approval required]") || !strings.Contains(text, "blocker: waiting on credentials") {
		t.Fatalf("approval or blocker metadata missing:\n%s", text)
	}

	plain := RenderSlicesPane(detail, 120, 10, false)
	ids := []string{"001-setup", "002-build", "003-release"}
	titles := []string{"Set up", "Build it", "Release"}
	idColumn, titleColumn := -1, -1
	for index := range ids {
		gotIDColumn := strings.Index(plain[index], ids[index])
		gotTitleColumn := strings.Index(plain[index], titles[index])
		if index == 0 {
			idColumn, titleColumn = gotIDColumn, gotTitleColumn
		}
		if gotIDColumn != idColumn || gotTitleColumn != titleColumn {
			t.Fatalf("slice columns are not aligned at row %d: id=%d/%d title=%d/%d lines=%q", index, gotIDColumn, idColumn, gotTitleColumn, titleColumn, plain)
		}
	}
}

func TestRenderSlicesPaneKeepsCurrentVisibleAtNarrowSizes(t *testing.T) {
	current := "003-current"
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{CurrentSlice: &current, PendingSlices: []string{"001-a", "002-b", current}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "001-a", Title: "A very long title", Status: plan.StatusPending},
			{ID: "002-b", Title: "Another long title", Status: plan.StatusPending},
			{ID: current, Title: "Current long title", Status: plan.StatusInProgress},
		}},
	}

	lines := RenderSlicesPane(detail, 18, 1, false)
	if len(lines) != 1 || !strings.Contains(lines[0], "> ") {
		t.Fatalf("narrow current slice lines = %q, want highlighted current line", lines)
	}
	if utf8.RuneCountInString(lines[0]) > 18 {
		t.Fatalf("narrow slice line = %q, exceeds width", lines[0])
	}
}

func TestRenderLogPaneRendersFramesPassesPlainLinesAndPinsTail(t *testing.T) {
	var framed bytes.Buffer
	if err := logrecord.Write(&framed, logrecord.Record{Type: logrecord.TypeAssistant, Content: "implemented", Timestamp: "2026-08-22T12:34:56Z"}); err != nil {
		t.Fatal(err)
	}
	if err := logrecord.Write(&framed, logrecord.Record{Type: logrecord.TypeToolResult, Name: "test", Content: "passed", Timestamp: "2026-08-22T12:35:01Z"}); err != nil {
		t.Fatal(err)
	}
	text := "legacy output\n" + framed.String() + "latest output\n"

	all := strings.Join(RenderLogPane(text, 80, 10), "\n")
	for _, want := range []string{"legacy output", "[12:34:56] assistant: implemented", "[12:35:01] ✓ test", "[12:35:01] passed", "latest output"} {
		if !strings.Contains(all, want) {
			t.Fatalf("rendered log missing %q:\n%s", want, all)
		}
	}

	pinned := RenderLogPane("one\ntwo\nthree\nfour\n", 80, 2)
	if got := strings.Join(pinned, "\n"); got != "three\nfour" {
		t.Fatalf("pinned log = %q, want newest two lines", got)
	}
	narrow := RenderLogPane("界界界界\n", 3, 1)
	if len(narrow) != 1 || utf8.RuneCountInString(narrow[0]) != 3 {
		t.Fatalf("narrow log = %q, want three runes", narrow)
	}
}

func TestRenderLogPaneExpandsTabsBeforeBoundingWidth(t *testing.T) {
	lines := RenderLogPane("\t\tpackage settings\n", 12, 1)
	if len(lines) != 1 || lines[0] != "        pack" {
		t.Fatalf("tabbed log line = %q, want expanded and width-bounded output", lines)
	}
	if strings.ContainsRune(lines[0], '\t') {
		t.Fatalf("tabbed log line retained a terminal tab: %q", lines[0])
	}
}

func TestRenderDetailIncludesHeaderAndFitsTerminal(t *testing.T) {
	current := "001-work"
	detail := &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusInProgress,
			Repo:   plan.Repo{Name: "alpha"},
			Plan:   plan.PlanState{ID: "plan-a", Title: "Plan A", CurrentSlice: &current, PendingSlices: []string{current}},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: current, Title: "Work", Status: plan.StatusInProgress}}},
	}
	frame := RenderDetail(DetailModel{
		Plan: detail,
		Row: monitor.Row{
			Liveness:     monitor.LivenessLive,
			Phase:        runstatus.Phase("implement"),
			HeartbeatAge: 7 * time.Second,
		},
		Log:   "working\n",
		Width: 100, Height: 18,
	})
	for _, want := range []string{"Tao UI | plan-a | alpha | in_progress | implement | 7s ago", "┌ DESCRIPTION ", "│ Plan A", "┌ SLICES ", "┌ LOG ", "│ working", "└"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("detail frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Esc back") || strings.Contains(frame, "Enter slice") {
		t.Fatalf("detail frame retained the shortcut legend:\n%s", frame)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(frame, clearScreenSequence), "\n")
	lines := strings.Split(body, "\n")
	if len(lines) < 3 || lines[1] != "" || !strings.HasPrefix(lines[2], "┌ DESCRIPTION ") {
		t.Fatalf("detail frame did not preserve the shared section gap below its header: %q", lines)
	}
	descriptionTop, descriptionBottom, sliceTop, sliceBottom, logTop := -1, -1, -1, -1, -1
	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "┌ DESCRIPTION "):
			descriptionTop = index
		case strings.HasPrefix(line, "┌ SLICES "):
			sliceTop = index
		case strings.HasPrefix(line, "┌ LOG "):
			logTop = index
		case strings.HasPrefix(line, "└") && descriptionTop >= 0 && sliceTop < 0:
			descriptionBottom = index
		case strings.HasPrefix(line, "└") && sliceTop >= 0 && logTop < 0:
			sliceBottom = index
		}
	}
	if sliceTop-descriptionBottom-1 != planDetailPaneGap || logTop-sliceBottom-1 != planDetailPaneGap {
		t.Fatalf("detail pane gaps description=%d..%d slices=%d..%d log=%d lines=%q", descriptionTop, descriptionBottom, sliceTop, sliceBottom, logTop, lines)
	}
	if strings.Contains(lines[0], "PLAN DETAIL") || strings.Contains(lines[0], "Plan A") {
		t.Fatalf("detail header retained page label or plan description: %q", lines[0])
	}
	if len(lines) > 18 {
		t.Fatalf("detail frame has %d lines, want at most 18", len(lines))
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 100 {
			t.Fatalf("detail line %q exceeds width", line)
		}
	}
}

func TestRenderDetailFullHeightOmitsTrailingNewline(t *testing.T) {
	frame := RenderDetail(DetailModel{
		Log:    "one\ntwo\nthree\nfour\n",
		Width:  80,
		Height: 10,
	})
	if lines := renderedLines(frame); len(lines) != 10 {
		t.Fatalf("full-height detail frame has %d lines, want 10:\n%s", len(lines), frame)
	}
	if strings.HasSuffix(frame, "\n") {
		t.Fatal("full-height detail frame ends with a newline that can scroll the terminal")
	}
}

func TestRenderDetailVerticalResizeKeepsFrameInsideTerminal(t *testing.T) {
	model := DetailModel{
		Row:    monitor.Row{PlanID: "plan-a", PlanTitle: "Plan A"},
		Log:    strings.Repeat("output\n", 10),
		Width:  80,
		Height: 20,
	}
	if frame := RenderDetail(model); strings.HasSuffix(frame, "\n") || len(renderedLines(frame)) != 20 {
		t.Fatalf("full-height detail frame should occupy 20 lines without a trailing newline:\n%s", frame)
	}

	model.Height = 6
	frame := RenderDetail(model)
	lines := renderedLines(frame)
	if len(lines) > 6 {
		t.Fatalf("resized detail frame has %d lines, want at most 6:\n%s", len(lines), frame)
	}
	if !strings.HasPrefix(lines[0], "Tao UI | plan-a |") || !strings.HasPrefix(lines[2], "┌ DESCRIPTION ") {
		t.Fatalf("resized detail frame lost compact header or spaced bordered description pane: %q", lines)
	}
}

func TestRenderDetailShortcutPopoverIsContextAware(t *testing.T) {
	frame := RenderDetail(DetailModel{ShowShortcuts: true, Width: 64, Height: 14})
	for _, want := range []string{"Keyboard shortcuts", "Move slice selection", "Open selected slice", "Return to plans", "Close shortcuts"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("plan detail shortcuts missing %q:\n%s", want, frame)
		}
	}
	for _, unavailable := range []string{"Search plans and notes", "Run selected plan", "Switch tabs"} {
		if strings.Contains(frame, unavailable) {
			t.Fatalf("plan detail shortcuts included dashboard action %q:\n%s", unavailable, frame)
		}
	}
}

func TestRenderSliceDetailShowsUsefulFieldsAndOmitsEmptyOnNarrowTerminal(t *testing.T) {
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	duration := int64(90)
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{PendingSlices: []string{"001-work"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID: "001-work", Title: "Work", Status: plan.StatusCompleted,
			Goal: "ship it", Context: "bounded context", Tasks: []string{"change code"},
			DependsOn: []string{"000-setup"}, ExpectedFiles: []string{"internal/tui/detail.go"},
			RequiredInputs: []plan.RequiredInput{{Path: "go.mod", Kind: plan.RequiredInputFile, Reason: "module"}},
			Verification:   plan.Verification{Commands: []string{"go test ./internal/tui"}, ManualChecks: []string{"check keys"}},
			Approval:       &plan.Approval{Required: true, Approved: true, Reason: "owner choice"},
			BlockerNote:    "resolved blocker", Notes: "implemented",
			VerificationResults: []plan.VerificationRun{{Command: "go test ./internal/tui", Result: "passed", Details: "ok"}},
			Completion:          &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "abc123"},
			Timing:              plan.SliceTiming{CreatedAt: now, StartedAt: &now, CompletedAt: &now, DurationSeconds: &duration},
		}}},
	}
	var agentLog bytes.Buffer
	_ = logrecord.Write(&agentLog, logrecord.Record{Type: logrecord.TypeSession, Content: "running 000-setup"})
	_ = logrecord.Write(&agentLog, logrecord.Record{Type: logrecord.TypeAssistant, Content: "unrelated output"})
	_ = logrecord.Write(&agentLog, logrecord.Record{Type: logrecord.TypeSession, Content: "running 001-work"})
	_ = logrecord.Write(&agentLog, logrecord.Record{Type: logrecord.TypeAssistant, Content: "slice output"})
	_ = logrecord.Write(&agentLog, logrecord.Record{Type: logrecord.TypeSession, Content: "reviewing plan plan-a"})
	_ = logrecord.Write(&agentLog, logrecord.Record{Type: logrecord.TypeAssistant, Content: "review output"})
	frame := RenderSliceDetail(DetailModel{Plan: detail, SelectedSliceID: "001-work", Log: agentLog.String(), Width: 200})
	for _, want := range []string{"Tao UI | 001-work | completed | approval: approved", "Goal: ship it", "┌ DETAIL ", "Tasks:", "Expected files:", "Verification commands:", "Blocker: resolved blocker", "Notes: implemented", "Verification results:", "Commit outcome: committed (abc123)", "┌ LOG ", "--- running 001-work ---", "assistant: slice output"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("slice detail missing %q:\n%s", want, frame)
		}
	}
	for _, removed := range []string{"SLICE DETAIL", "GOAL & CONTEXT", "Context:", "Status:", "Title:", "Dependencies:", "Required inputs:", "Manual checks:", "Approval:", "Approval reason:", "Timing:", "EVENTS", "unrelated output", "review output", "Bksp/Esc", "Execution root", "null"} {
		if strings.Contains(frame, removed) {
			t.Fatalf("slice detail retained removed or raw field %q:\n%s", removed, frame)
		}
	}
	detailLines := renderedLines(frame)
	if len(detailLines) < 5 || detailLines[1] != "" || detailLines[2] != "Goal: ship it" || detailLines[3] != "" || !strings.HasPrefix(detailLines[4], "┌ DETAIL ") {
		t.Fatalf("slice detail shared header, goal, or section gaps are wrong: %q", detailLines)
	}
	detailBottom, logTop := -1, -1
	for index, line := range detailLines {
		if detailBottom < 0 && index > 4 && strings.HasPrefix(line, "└") {
			detailBottom = index
		}
		if strings.HasPrefix(line, "┌ LOG ") {
			logTop = index
			break
		}
	}
	if detailBottom < 0 || logTop-detailBottom-1 != planDetailPaneGap {
		t.Fatalf("slice detail-to-log gap detail-bottom=%d log-top=%d lines=%q", detailBottom, logTop, detailLines)
	}
	empty := RenderSliceDetail(DetailModel{Plan: &plan.PlanDetail{
		State:  plan.State{Plan: plan.PlanState{PendingSlices: []string{"empty"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "empty", Title: "Empty"}}},
	}, SelectedSliceID: "empty", Width: 80})
	if !strings.Contains(empty, "Goal: -") || !strings.Contains(empty, "┌ DETAIL ") || !strings.Contains(empty, "No additional details.") {
		t.Fatalf("empty slice lost simple goal or detail section:\n%s", empty)
	}
	for _, omitted := range []string{"Context:", "Title:", "Tasks:", "Required inputs:", "Manual checks:", "Approval:", "Approval reason:", "Notes:", "Timing:", "┌ EVENTS "} {
		if strings.Contains(empty, omitted) {
			t.Fatalf("empty slice rendered %q:\n%s", omitted, empty)
		}
	}

	narrow := RenderSliceDetail(DetailModel{Plan: detail, SelectedSliceID: "001-work", Width: 18, Height: 5})
	lines := renderedLines(narrow)
	if len(lines) != 5 || strings.HasSuffix(narrow, "\n") {
		t.Fatalf("narrow slice frame lines=%d trailing-newline=%t:\n%s", len(lines), strings.HasSuffix(narrow, "\n"), narrow)
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 18 {
			t.Fatalf("narrow slice detail line %q exceeds width", line)
		}
	}
}

func TestFilterSliceLogCombinesMatchingRunSessions(t *testing.T) {
	var log bytes.Buffer
	for _, record := range []logrecord.Record{
		{Type: logrecord.TypeSession, Content: "running 001-work"},
		{Type: logrecord.TypeAssistant, Content: "first attempt"},
		{Type: logrecord.TypeSession, Content: "running 002-other"},
		{Type: logrecord.TypeAssistant, Content: "other slice"},
		{Type: logrecord.TypeSession, Content: "running 001-work"},
		{Type: logrecord.TypeAssistant, Content: "retry"},
		{Type: logrecord.TypeSession, Content: "reviewing plan plan-a"},
		{Type: logrecord.TypeAssistant, Content: "review"},
	} {
		if err := logrecord.Write(&log, record); err != nil {
			t.Fatal(err)
		}
	}
	logs, active := projectSliceLogs(log.String(), detailLogKeepLines)
	if active != "" {
		t.Fatalf("active slice = %q, want none after review session", active)
	}
	got := presentPlanLog(logs["001-work"])
	for _, want := range []string{"running 001-work", "first attempt", "retry"} {
		if !strings.Contains(got, want) {
			t.Fatalf("filtered slice log missing %q:\n%s", want, got)
		}
	}
	for _, excluded := range []string{"002-other", "other slice", "reviewing plan", "review"} {
		if strings.Contains(got, excluded) {
			t.Fatalf("filtered slice log retained %q:\n%s", excluded, got)
		}
	}
}

func TestDetailStateAttributesLiveLogUpdatesToActiveSlice(t *testing.T) {
	var seed bytes.Buffer
	if err := logrecord.Write(&seed, logrecord.Record{Type: logrecord.TypeSession, Content: "running 001-work"}); err != nil {
		t.Fatal(err)
	}
	logs, active := projectSliceLogs(seed.String(), detailLogKeepLines)
	state := detailState{sliceLogs: logs, activeLogSlice: active}

	var update bytes.Buffer
	_ = logrecord.Write(&update, logrecord.Record{Type: logrecord.TypeAssistant, Content: "live output"})
	_ = logrecord.Write(&update, logrecord.Record{Type: logrecord.TypeSession, Content: "reviewing plan plan-a"})
	_ = logrecord.Write(&update, logrecord.Record{Type: logrecord.TypeAssistant, Content: "review output"})
	state.appendLog(update.String())

	got := presentPlanLog(state.sliceLogs["001-work"])
	if !strings.Contains(got, "live output") || strings.Contains(got, "review output") {
		t.Fatalf("live slice log attribution is wrong:\n%s", got)
	}
	if state.activeLogSlice != "" {
		t.Fatalf("active slice = %q, want cleared by review session", state.activeLogSlice)
	}
}

func TestRenderSliceDetailShortcutPopoverIsContextAware(t *testing.T) {
	frame := RenderSliceDetail(DetailModel{ShowShortcuts: true, Width: 64, Height: 12})
	for _, want := range []string{"Keyboard shortcuts", "Return to plan", "Quit", "Close shortcuts"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("slice detail shortcuts missing %q:\n%s", want, frame)
		}
	}
	for _, unavailable := range []string{"Move slice selection", "Open selected slice", "Search plans and notes"} {
		if strings.Contains(frame, unavailable) {
			t.Fatalf("slice detail shortcuts included unavailable action %q:\n%s", unavailable, frame)
		}
	}
}

func TestRenderSliceDetailSanitizesArtifactControls(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{PendingSlices: []string{"001-work"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{
			ID:      "001-work",
			Title:   "unsafe\x1b[31m title\x1b[0m",
			Status:  "pend\x1b]0;status\aing",
			Goal:    "goal\x1b]8;;https://example.invalid\a link\x1b]8;;\a",
			Context: "context\twith\x00controls",
			Tasks:   []string{"task \x1b[2Jcontent"},
			RequiredInputs: []plan.RequiredInput{{
				Path: "input\r.txt", Kind: plan.RequiredInputFile, Reason: "reason\x1b]0;owned\x1b\\ safe",
			}},
			Verification: plan.Verification{
				Commands:     []string{"go test\u009b31m ./internal/tui\u009b0m"},
				ManualChecks: []string{"check\voutput"},
			},
			Approval:    &plan.Approval{Required: true, Reason: "owner\x1b[?25l choice"},
			BlockerNote: "blocked\bnote",
			Notes:       "notes\x1b]52;c;payload\a remain",
			VerificationResults: []plan.VerificationRun{{
				Command: "verify\ncommand", Result: "pass\x7fed", Details: "details\x1b[1m styled\x1b[0m",
			}},
			Completion: &plan.SliceCompletionOutcome{Outcome: "commit\x1b[5mted", CommitSHA: "abc\x1b]0;sha\a123"},
		}}},
	}

	frame := RenderSliceDetail(DetailModel{Plan: detail, SelectedSliceID: "001-work", Width: 200})
	body := strings.TrimPrefix(frame, clearScreenSequence)
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("slice detail contains artifact control %U: %q", r, body)
		}
	}
	for _, unsafe := range []string{"[31m", "]0;status", "example.invalid", "[2J", "]52;", "[?25l"} {
		if strings.Contains(body, unsafe) {
			t.Fatalf("slice detail retained terminal sequence content %q: %q", unsafe, body)
		}
	}
	for _, want := range []string{"Goal: goal link", "task content", "Notes: notes remain", "details styled"} {
		if !strings.Contains(body, want) {
			t.Fatalf("slice detail lost printable content %q: %q", want, body)
		}
	}
}

func TestDetailSliceSelectionInitializesAndSurvivesRefresh(t *testing.T) {
	current := "002-current"
	detail := &detailState{plan: &plan.PlanDetail{
		State:  plan.State{Plan: plan.PlanState{CompletedSlices: []string{"001-done"}, PendingSlices: []string{current, "003-next"}, CurrentSlice: &current}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "003-next"}, {ID: current}, {ID: "001-done"}}},
	}}
	detail.reconcileSliceSelection()
	if detail.selectedSliceID != current {
		t.Fatalf("initial selected slice = %q, want current %q", detail.selectedSliceID, current)
	}
	detail.moveSlice(1)
	if detail.selectedSliceID != "003-next" {
		t.Fatalf("moved selected slice = %q, want 003-next", detail.selectedSliceID)
	}
	detail.plan = &plan.PlanDetail{
		State:  plan.State{Plan: plan.PlanState{CompletedSlices: []string{"001-done", current}, PendingSlices: []string{"003-next"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-done"}, {ID: current}, {ID: "003-next"}}},
	}
	detail.reconcileSliceSelection()
	if detail.selectedSliceID != "003-next" {
		t.Fatalf("refresh selected slice = %q, want identity preserved", detail.selectedSliceID)
	}
}

func TestFollowDetailLogSkipsSeedReplayAndEmitsAppends(t *testing.T) {
	repository := &fakeDetailRepository{
		follow: func(_ context.Context, _ string, out io.Writer) error {
			if _, err := io.WriteString(out, "older\nse"); err != nil {
				return err
			}
			if _, err := io.WriteString(out, "ed\nnew line\n"); err != nil {
				return err
			}
			return nil
		},
	}
	updates := make(chan detailFollowUpdate, 4)
	followDetailLog(context.Background(), repository, "/plans/one", "seed\n", updates)

	var got strings.Builder
	for update := range updates {
		if update.err != nil {
			t.Fatalf("follow update error: %v", update.err)
		}
		got.WriteString(update.text)
	}
	if got.String() != "new line\n" {
		t.Fatalf("follow updates = %q, want only live append", got.String())
	}
}

func TestMissingPlanCannotOpenNestedSlice(t *testing.T) {
	state := loopState{snapshot: monitor.Snapshot{Rows: []monitor.Row{{PlanID: "missing", PlanDir: "/plans/missing"}}}, showCompleted: true}
	app := App{Details: &fakeDetailRepository{}}
	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEnter}); quit || state.detail == nil || state.detail.loadError == "" {
		t.Fatalf("missing plan open quit=%t detail=%#v", quit, state.detail)
	}
	app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyEnter})
	if state.detail.sliceOpen {
		t.Fatal("missing plan opened a nested slice")
	}
}

func TestDetailNavigationEnterBackspaceAndEscape(t *testing.T) {
	current := "001-work"
	started := make(chan struct{})
	stopped := make(chan struct{})
	var startedOnce sync.Once
	repository := &fakeDetailRepository{
		detail: &plan.PlanDetail{
			State:  plan.State{Plan: plan.PlanState{ID: "plan-a", CurrentSlice: &current, PendingSlices: []string{current}}},
			Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: current, Status: plan.StatusInProgress}}},
		},
		tail: "seed\n",
		follow: func(ctx context.Context, _ string, out io.Writer) error {
			startedOnce.Do(func() { close(started) })
			if _, err := io.WriteString(out, "seed\n"); err != nil {
				return err
			}
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	}
	state := loopState{
		snapshot:      monitor.Snapshot{Rows: []monitor.Row{{RepositoryID: "repo-a", PlanID: "plan-a", PlanDir: "/plans/one"}}},
		showCompleted: true,
	}
	app := App{Details: repository}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEnter}); quit || state.detail == nil {
		t.Fatalf("Enter navigation quit=%t detail=%#v", quit, state.detail)
	}
	if state.detail.plan == nil || state.detail.log != "seed\n" || repository.tailLines != 0 {
		t.Fatalf("opened detail = %#v tail lines=%d", state.detail, repository.tailLines)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("detail follower did not start")
	}

	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'}); quit || !state.showShortcuts {
		t.Fatalf("plan detail ? quit=%t show=%t, want open shortcuts", quit, state.showShortcuts)
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEsc}); quit || state.showShortcuts || state.detail == nil {
		t.Fatalf("plan detail shortcut Esc quit=%t show=%t detail=%#v", quit, state.showShortcuts, state.detail)
	}

	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEnter}); quit || state.detail == nil || !state.detail.sliceOpen {
		t.Fatalf("slice Enter quit=%t detail=%#v, want nested slice", quit, state.detail)
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyRune, Rune: '?'}); quit || !state.showShortcuts {
		t.Fatalf("slice detail ? quit=%t show=%t, want open shortcuts", quit, state.showShortcuts)
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEsc}); quit || state.showShortcuts || state.detail == nil || !state.detail.sliceOpen {
		t.Fatalf("slice shortcut Esc quit=%t show=%t detail=%#v", quit, state.showShortcuts, state.detail)
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyBackspace}); quit || state.detail == nil || state.detail.sliceOpen {
		t.Fatalf("nested Backspace quit=%t detail=%#v, want plan detail", quit, state.detail)
	}
	select {
	case <-stopped:
		t.Fatal("detail follower was canceled while closing nested slice")
	default:
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyBackspace}); quit || state.detail != nil {
		t.Fatalf("plan detail Backspace quit=%t detail=%#v, want table", quit, state.detail)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("detail follower was not canceled")
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEsc}); quit {
		t.Fatal("first table Esc unexpectedly quit")
	}
	if quit := app.handleKey(ctx, &state, term.KeyEvent{Key: term.KeyEsc}); !quit {
		t.Fatal("second table Esc did not quit")
	}
}

type fakeDetailRepository struct {
	detail    *plan.PlanDetail
	tail      string
	tailLines int
	follow    func(context.Context, string, io.Writer) error
}

func (r *fakeDetailRepository) ResolvePlan(context.Context, string) (*plan.PlanDetail, error) {
	if r.detail == nil {
		return nil, errors.New("plan unavailable")
	}
	return r.detail, nil
}

func (r *fakeDetailRepository) ReadLogTail(_ string, lines int) (string, error) {
	r.tailLines = lines
	return r.tail, nil
}

func (r *fakeDetailRepository) FollowLog(ctx context.Context, dir string, out io.Writer) error {
	if r.follow == nil {
		return nil
	}
	return r.follow(ctx, dir, out)
}
