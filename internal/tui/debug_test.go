package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
)

func TestRenderDebugShowsRuntimeDoctorAndUIInformation(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	model := Model{
		Page: PageDebug, Width: 100, Height: 60, Now: now,
		Snapshot: monitor.Snapshot{CollectedAt: now, Rows: []monitor.Row{
			{RepositoryID: "repo-a", RepositoryName: "alpha", Warnings: []string{"plan store damaged"}},
		}},
		NoteSnapshot: note.Snapshot{
			Notes:    []note.CatalogNote{{RepositoryID: "repo-a", ID: "note-a"}},
			Warnings: []note.CatalogWarning{{RepositoryID: "repo-a", Path: "/notes/bad.json", Err: errors.New("invalid note")}},
		},
		DebugSnapshot: DebugSnapshot{
			CollectedAt: now,
			System: []DebugValue{
				{Label: "version", Value: "v1.2.3"},
				{Label: "data home", Value: "/tmp/tao"},
			},
			SelectedAgent:   "pi",
			InstalledAgents: []string{"Pi"},
			DoctorProblems:  []DebugProblem{{Category: "tool recommended", Name: "jq", Status: "warning", Detail: "missing"}},
			RuntimeDefaults: []DebugRuntimeDefault{
				{Name: "TAO_AGENT", Value: "pi", Source: "default"},
				{Name: "TAO_PULL_REQUEST", Value: "true", Source: "repository", Warning: "test warning"},
			},
		},
	}
	frame := Render(model)
	for _, want := range []string{
		"Tao UI | Debug | diagnostics", "UI", "viewport", "100x60", "plan rows", "open notes", "SYSTEM", "version", "v1.2.3",
		"DOCTOR", "selected agent", "installed agents", "tool recommended jq", "RUNTIME DEFAULTS", "TAO_AGENT", "TAO_PULL_REQUEST", "repository",
		"warning: test warning", "COLLECTOR WARNINGS", "plan store damaged", "/notes/bad.json: invalid note",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("debug frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Repositories: all") || strings.Contains(frame, "open notes |") {
		t.Fatalf("debug frame retained table-page header metadata:\n%s", frame)
	}
}

func TestDebugPageScrollAndShortcuts(t *testing.T) {
	defaults := make([]DebugRuntimeDefault, 30)
	for index := range defaults {
		defaults[index] = DebugRuntimeDefault{Name: "TAO_SETTING_" + strings.Repeat("X", index%4), Value: "value", Source: "default"}
	}
	state := loopState{
		page: PageDebug, size: term.Size{Width: 70, Height: 10},
		debugSnapshot: DebugSnapshot{RuntimeDefaults: defaults},
	}
	if state.pageRowCount() != 0 {
		t.Fatalf("debug row count = %d, want no selection rows", state.pageRowCount())
	}
	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	if state.debugOffset != state.debugPageMaxOffset() || state.debugOffset == 0 {
		t.Fatalf("debug bottom offset = %d max=%d", state.debugOffset, state.debugPageMaxOffset())
	}
	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'g'})
	if state.debugOffset != 0 {
		t.Fatalf("debug top offset = %d", state.debugOffset)
	}
	state.handleKey(term.KeyEvent{Key: term.KeyRune, Rune: 'j'})
	if state.debugOffset != 1 {
		t.Fatalf("debug down offset = %d", state.debugOffset)
	}

	frame := Render(Model{Page: PageDebug, DebugSnapshot: state.debugSnapshot, Width: 70, Height: 12, ShowShortcuts: true})
	for _, want := range []string{"Keyboard shortcuts", "Scroll diagnostics", "Jump to top / bottom", "Switch tabs"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("debug shortcuts missing %q:\n%s", want, frame)
		}
	}
	for _, unavailable := range []string{"Run selected plan", "Search plans and notes", "Cycle repository filter"} {
		if strings.Contains(frame, unavailable) {
			t.Fatalf("debug shortcuts contain unavailable action %q:\n%s", unavailable, frame)
		}
	}
}
