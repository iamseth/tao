package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/term"
)

type editorSettingsService struct {
	fakeSettingsService
	value *string
	key   string
	err   error
}

func (s *editorSettingsService) SetSetting(_ context.Context, _, key string, value *string) error {
	s.calls++
	s.key = key
	s.value = value
	return s.err
}
func editorFixture() (*editorSettingsService, App, *loopState) {
	s := &editorSettingsService{fakeSettingsService: fakeSettingsService{snapshot: SettingsSnapshot{Repositories: []RepositorySetting{{ID: "repo-a", Name: "alpha", Values: []SettingsValue{{Key: "agent", Kind: "string", Choices: []string{"pi", "claude"}, Editable: true, Value: "pi", Source: "env", Stored: "claude"}}}}}}}
	return s, App{Settings: s}, &loopState{page: PageSettings, settingsSnapshot: s.snapshot}
}
func pressSetting(a App, s *loopState, key term.Key, r rune) {
	a.handleKey(context.Background(), s, term.KeyEvent{Key: key, Rune: r})
}
func TestSettingsEditorLifecycle(t *testing.T) {
	for _, action := range []string{"set", "unset", "cancel", "escape", "stale", "missing", "failure"} {
		t.Run(action, func(t *testing.T) {
			svc, app, state := editorFixture()
			pressSetting(app, state, term.KeyRune, 'e')
			if state.settingsEditor == nil {
				t.Fatal("editor not opened")
			}
			if action == "unset" {
				pressSetting(app, state, term.KeyRune, 'u')
			} else {
				pressSetting(app, state, term.KeyEnter, 0)
				pressSetting(app, state, term.KeyArrowRight, 0)
				pressSetting(app, state, term.KeyEnter, 0)
			}
			if state.confirm == nil || !strings.Contains(state.confirm.message, "stored") {
				t.Fatal("missing stored-value confirmation")
			}
			if !strings.Contains(state.confirm.message, "environment") {
				t.Fatal("missing mask warning")
			}
			switch action {
			case "stale":
				svc.snapshot.Repositories[0].Values[0].Stored = "pi"
			case "missing":
				svc.snapshot.Repositories = nil
			case "failure":
				svc.err = errors.New("lock busy: retry")
			}
			switch action {
			case "cancel":
				pressSetting(app, state, term.KeyRune, 'n')
			case "escape":
				pressSetting(app, state, term.KeyEsc, 0)
			default:
				pressSetting(app, state, term.KeyRune, 'y')
			}
			pressSetting(app, state, term.KeyRune, 'y')
			want := 1
			if action == "cancel" || action == "escape" || action == "stale" || action == "missing" {
				want = 0
			}
			if svc.calls != want {
				t.Fatalf("writes %d want %d", svc.calls, want)
			}
			if action == "unset" && svc.value != nil {
				t.Fatal("unset must pass nil")
			}
			if action == "failure" && !strings.Contains(state.settingsMessage, "lock busy") {
				t.Fatal(state.settingsMessage)
			}
		})
	}
}
func TestSettingsEditorSchema(t *testing.T) {
	for _, d := range runtimeconfig.SettingDefinitions() {
		editable := false
		for _, scope := range d.Scopes {
			editable = editable || scope == "repo"
		}
		if !editable {
			continue
		}
		t.Run(d.Key, func(t *testing.T) {
			value := "1"
			if d.Kind == "boolean" {
				value = "false"
			}
			if d.Kind == "string" {
				value = "model-name"
				if strings.Contains(d.Key, "timeout") {
					value = "0s"
				}
			}
			if len(d.Choices) > 0 {
				value = d.Choices[0]
			}
			if _, err := parseSettingsInput(d.Key, value); err != nil {
				t.Fatalf("%s=%s: %v", d.Key, value, err)
			}
			svc, app, state := editorFixture()
			svc.snapshot.Repositories[0].Values = []SettingsValue{{Key: d.Key, Kind: d.Kind, Choices: d.Choices, Editable: true}}
			state.settingsSnapshot = svc.snapshot
			pressSetting(app, state, term.KeyRune, 'e')
			pressSetting(app, state, term.KeyEnter, 0)
			for _, r := range value {
				pressSetting(app, state, term.KeyRune, r)
			}
			pressSetting(app, state, term.KeyEnter, 0)
			if state.confirm == nil {
				t.Fatalf("no confirmation: %s", state.settingsEditor.message)
			}
			pressSetting(app, state, term.KeyRune, 'y')
			if svc.calls != 1 || svc.key != d.Key || svc.value == nil {
				t.Fatalf("write: %+v", svc)
			}
			if _, err := parseSettingsInput(d.Key, "\x1bunsafe"); err == nil {
				t.Fatal("accepted control input")
			}
			if strings.HasSuffix(d.Key, ".stop") {
				if got, err := parseSettingsInput(d.Key, "null"); err != nil || got != "null" {
					t.Fatalf("disabled %q %v", got, err)
				}
			}
		})
	}
}

func TestSettingsEditorInputAndNavigation(t *testing.T) {
	svc, app, state := editorFixture()
	svc.snapshot.Repositories[0].Values = append(svc.snapshot.Repositories[0].Values,
		SettingsValue{Key: "session_timeout", Kind: "string", Editable: true, Value: "1m"},
		SettingsValue{Key: "budget.slice.cost.stop", Kind: "number", Editable: true},
		SettingsValue{Key: "theme", Kind: "string"})
	state.settingsSnapshot = svc.snapshot
	pressSetting(app, state, term.KeyRune, 'e')
	pressSetting(app, state, term.KeyTab, 0)
	pressSetting(app, state, term.KeyEnter, 0)
	for range 2 {
		pressSetting(app, state, term.KeyBackspace, 0)
	}
	for _, r := range "invalid" {
		pressSetting(app, state, term.KeyRune, r)
	}
	pressSetting(app, state, term.KeyEnter, 0)
	if state.confirm != nil || state.settingsEditor.message == "" || svc.calls != 0 {
		t.Fatal("invalid duration confirmed")
	}
	pressSetting(app, state, term.KeyEsc, 0)
	if state.settingsEditor == nil || state.settingsEditor.editing {
		t.Fatal("escape did not cancel input")
	}
	pressSetting(app, state, term.KeyTab, 0)
	pressSetting(app, state, term.KeyRune, 'd')
	pressSetting(app, state, term.KeyRune, 'y')
	if svc.calls != 1 || svc.value == nil || *svc.value != "null" {
		t.Fatal("disable cap must pass null")
	}
	pressSetting(app, state, term.KeyTab, 0)
	pressSetting(app, state, term.KeyEnter, 0)
	pressSetting(app, state, term.KeyRune, 'u')
	if state.confirm != nil || state.settingsEditor.editing {
		t.Fatal("read-only row editable")
	}
	for _, width := range []int{1, 12, 40, 120} {
		state.size = term.Size{Width: width, Height: 12}
		frame := Render(Model{Page: PageSettings, Width: width, Height: 12, SettingsSnapshot: state.settingsSnapshot, settingsEditor: state.settingsEditor})
		if frame == "" || state.settingsEditor.index != 3 {
			t.Fatal("resize lost focus")
		}
	}
	pressSetting(app, state, term.KeyShiftTab, 0)
	if state.settingsEditor.index != 2 {
		t.Fatal("reverse focus")
	}
	pressSetting(app, state, term.KeyEsc, 0)
	if state.settingsEditor != nil {
		t.Fatal("escape did not close detail")
	}
}

func TestSettingsEditorRenderShowsStoredAndEffective(t *testing.T) {
	_, app, state := editorFixture()
	pressSetting(app, state, term.KeyRune, 'e')
	pressSetting(app, state, term.KeyEnter, 0)
	frame := Render(Model{Page: PageSettings, Width: 72, Height: 24, SettingsSnapshot: state.settingsSnapshot, settingsEditor: state.settingsEditor})
	for _, want := range []string{"Effective: pi [env]", "Stored: claude", "Proposed stored input:", "choices: pi | claude"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing %q in %s", want, frame)
		}
	}
	pressSetting(app, state, term.KeyEnter, 0)
	frame = Render(Model{Page: PageSettings, Width: 40, Height: 30, SettingsSnapshot: state.settingsSnapshot, settingsEditor: state.settingsEditor, ConfirmMessage: state.confirmMessage()})
	for _, want := range []string{"stored value", "environment", "[y/n]"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing %q: %s", want, frame)
		}
	}
}

func TestSettingsEditorUnsupportedAndBounds(t *testing.T) {
	svc, _, state := editorFixture()
	app := App{Settings: &svc.fakeSettingsService}
	pressSetting(app, state, term.KeyRune, 'e')
	pressSetting(app, state, term.KeyEnter, 0)
	pressSetting(app, state, term.KeyRune, 'u')
	if state.confirm != nil || state.settingsEditor.editing || svc.calls != 0 {
		t.Fatal("unsupported service wrote")
	}
	for _, value := range []string{"-1", "not-a-number", "null"} {
		if _, err := parseSettingsInput("max_slices", value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if _, err := parseSettingsInput("models.model", strings.Repeat("x", settingsInputLimit+1)); err == nil {
		t.Fatal("unbounded input")
	}
	if _, err := parseSettingsInput("models.model", ""); err == nil {
		t.Fatal("empty model must be rejected, not silently unset")
	}
}
