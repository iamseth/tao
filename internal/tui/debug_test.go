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
		SettingsSnapshot: SettingsSnapshot{
			RuntimeDefaults: []SettingsRuntimeDefault{
				{Name: "TAO_AGENT", Value: "pi", Source: "default"},
				{Name: "TAO_PULL_REQUEST", Value: "false", Source: "env"},
			},
			Repositories: []RepositorySetting{{ID: "repo-a"}, {ID: "repo-b"}},
		},
	}
	frame := Render(model)
	for _, want := range []string{
		"tao │ notes  plans  settings ▸debug", "agent pi", "UI", "viewport", "100x60", "plan rows", "open notes", "SYSTEM", "version", "v1.2.3",
		"DOCTOR", "selected agent", "installed agents", "tool recommended jq", "RUNTIME ANOMALIES", "TAO_PULL_REQUEST", "repository", "false",
		"1 active of 2 registered", "warning: test warning", "COLLECTOR WARNINGS", "plan store damaged", "/notes/bad.json: invalid note",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("debug frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "TAO_AGENT") {
		t.Fatalf("debug frame retained a runtime value identical to the global baseline:\n%s", frame)
	}
	if strings.Contains(frame, "Repositories: all") || strings.Contains(frame, "open notes |") {
		t.Fatalf("debug frame retained table-page header metadata:\n%s", frame)
	}

	model.Profile = ProfileTrueColor
	styled := Render(model)
	for _, title := range []string{"UI", "SYSTEM", "DOCTOR", "RUNTIME ANOMALIES", "COLLECTOR WARNINGS"} {
		want := Paint(ProfileTrueColor, RoleDebugSection, "▌ "+title+" ")
		if !strings.Contains(styled, want) {
			t.Errorf("debug section %q does not use the debug section color: %q", title, styled)
		}
	}
}

func TestRenderDebugRuntimeAnomalies(t *testing.T) {
	model := Model{
		Page: PageDebug, Width: 120, Height: 60,
		DebugSnapshot: DebugSnapshot{RuntimeDefaults: []DebugRuntimeDefault{
			{Name: "TAO_DIFFERENT", Value: "repository-value", Source: "repository"},
			{Name: "TAO_WARNING_ONLY", Value: "same", Source: "default", Warning: "fallback retained"},
			{Name: "TAO_MISSING_BASELINE", Value: "repository-only", Source: "repository"},
			{Name: "TAO_IDENTICAL", Value: "same", Source: "default"},
		}},
		SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: []SettingsRuntimeDefault{
			{Name: "TAO_DIFFERENT", Value: "global-value", Source: "env"},
			{Name: "TAO_WARNING_ONLY", Value: "same", Source: "default"},
			{Name: "TAO_IDENTICAL", Value: "same", Source: "default"},
		}},
	}
	frame := Render(model)
	for _, want := range []string{
		"RUNTIME ANOMALIES", "TAO_DIFFERENT", "repository-value", "global-value", "repository",
		"TAO_WARNING_ONLY", "warning: fallback retained", "TAO_MISSING_BASELINE", "repository-only", "(missing)",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("debug anomalies missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "TAO_IDENTICAL") {
		t.Fatalf("debug anomalies include identical runtime value:\n%s", frame)
	}

	model.DebugSnapshot.RuntimeDefaults = []DebugRuntimeDefault{{Name: "TAO_IDENTICAL", Value: "same", Source: "default"}}
	frame = Render(model)
	if strings.Contains(frame, "RUNTIME ANOMALIES") || strings.Contains(frame, "TAO_IDENTICAL") {
		t.Fatalf("debug frame renders an empty anomaly section:\n%s", frame)
	}
}

func TestDebugPageScrollAndShortcuts(t *testing.T) {
	system := make([]DebugValue, 30)
	for index := range system {
		system[index] = DebugValue{Label: "diagnostic " + strings.Repeat("X", index%4), Value: "value"}
	}
	state := loopState{
		page: PageDebug, size: term.Size{Width: 70, Height: 10},
		debugSnapshot: DebugSnapshot{System: system},
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
