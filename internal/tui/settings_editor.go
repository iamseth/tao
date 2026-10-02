package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/term"
)

const settingsInputLimit = 512

type settingsEditorState struct {
	repositoryID string
	index        int
	editing      bool
	input        string
	original     SettingsValue
	message      string
}

// Parsing is shared with the CLI and storage service, not a UI-specific grammar.
func parseSettingsInput(key, input string) (string, error) {
	if len(input) > settingsInputLimit || strings.IndexFunc(input, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("input must be at most %d bytes with no control characters", settingsInputLimit)
	}
	if strings.TrimSpace(input) == "null" && !strings.HasSuffix(key, ".stop") {
		return "", fmt.Errorf("%s: null is only valid for STOP caps", key)
	}
	raw, err := runtimeconfig.ParseSetting(key, input)
	if err != nil {
		return "", err
	}
	if err = runtimeconfig.ValidateSettings(configtypes.SettingsValues{key: raw}, "repo"); err != nil {
		return "", err
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && string(raw) != "null" {
		return text, nil
	}
	return string(raw), nil
}

func (e *settingsEditorState) row(snapshot SettingsSnapshot) (SettingsValue, bool) {
	for _, repo := range snapshot.Repositories {
		if repo.ID == e.repositoryID && len(repo.Values) > 0 {
			e.index = max(0, min(e.index, len(repo.Values)-1))
			return repo.Values[e.index], true
		}
	}
	return SettingsValue{}, false
}
func settingsChoices(row SettingsValue) []string {
	if row.Kind == "boolean" {
		return []string{"false", "true"}
	}
	return row.Choices
}
func (a App) handleSettingsEditor(ctx context.Context, s *loopState, key term.KeyEvent) {
	e := s.settingsEditor
	if key.Key == term.KeyEsc {
		if e.editing {
			e.editing = false
			e.message = ""
		} else {
			s.settingsEditor = nil
		}
		return
	}
	row, ok := e.row(s.settingsSnapshot)
	if !ok {
		e.message = "Repository unavailable; escape to return and refresh."
		return
	}
	if e.editing {
		switch key.Key {
		case term.KeyEnter:
			value, err := parseSettingsInput(e.original.Key, e.input)
			if err != nil {
				e.message = err.Error()
				return
			}
			a.confirmSetting(ctx, s, e.original, &value)
		case term.KeyBackspace:
			chars := []rune(e.input)
			if len(chars) > 0 {
				e.input = string(chars[:len(chars)-1])
			}
		case term.KeyArrowLeft, term.KeyArrowRight:
			choices := settingsChoices(e.original)
			if len(choices) > 0 {
				i := slices.Index(choices, e.input)
				delta := 1
				if key.Key == term.KeyArrowLeft {
					delta = -1
				}
				e.input = choices[(i+delta+len(choices))%len(choices)]
			}
		case term.KeyRune:
			if !unicode.IsControl(key.Rune) && len(e.input)+len(string(key.Rune)) <= settingsInputLimit {
				e.input += string(key.Rune)
			} else {
				e.message = "Input limit reached or control character rejected."
			}
		}
		return
	}
	switch {
	case key.Key == term.KeyArrowDown || key.Key == term.KeyTab:
		e.index++
		e.row(s.settingsSnapshot)
	case key.Key == term.KeyArrowUp || key.Key == term.KeyShiftTab:
		e.index = max(0, e.index-1)
	case key.Key == term.KeyEnter:
		if !row.Editable {
			e.message = "This setting is read-only."
			return
		}
		if _, ok := a.Settings.(SettingsEditor); !ok {
			e.message = "Settings service is read-only."
			return
		}
		e.original = row
		e.input = row.Stored
		if row.Stored == "" {
			e.input = row.Value
		}
		e.editing = true
		e.message = ""
	case key.Key == term.KeyRune && key.Rune == 'u':
		a.confirmSetting(ctx, s, row, nil)
	case key.Key == term.KeyRune && key.Rune == 'd' && strings.HasSuffix(row.Key, ".stop"):
		value := "null"
		a.confirmSetting(ctx, s, row, &value)
	}
}

func (a App) confirmSetting(ctx context.Context, s *loopState, row SettingsValue, value *string) {
	e := s.settingsEditor
	editor, ok := a.Settings.(SettingsEditor)
	if !ok || !row.Editable {
		e.message = "This setting is read-only."
		return
	}
	stored := "inherit (unset)"
	if value != nil {
		canonical, err := parseSettingsInput(row.Key, *value)
		if err != nil {
			e.message = err.Error()
			return
		}
		value = &canonical
		stored = canonical
		if canonical == "null" && strings.HasSuffix(row.Key, ".stop") {
			stored = "null (disabled)"
		} else if row.Kind == "string" {
			stored = fmt.Sprintf("%q", canonical)
		}
	}
	message := fmt.Sprintf("Set %s stored value to %s? Current effective: %s [%s].", row.Key, stored, row.Value, row.Source)
	if row.Source == "env" {
		message += " Effective value remains masked by environment."
	} else {
		message += " Effective value will be refreshed after saving."
	}
	consumed := false
	s.beginConfirm(message, func(accepted bool) {
		if consumed {
			return
		}
		consumed = true
		if !accepted {
			return
		}
		// Refresh files against the service's frozen environment before a stale proposal
		// can write; persistence still owns locking and fresh-document merging.
		fresh := a.collectSettings(ctx)
		probe := &settingsEditorState{repositoryID: e.repositoryID, index: e.index}
		current, exists := probe.row(fresh)
		s.settingsSnapshot = fresh
		s.restoreSettingsSelection(e.repositoryID)
		if fresh.CollectionError != "" || !exists || current.Key != row.Key || current.Stored != row.Stored || current.Value != row.Value || current.Source != row.Source || !current.Editable {
			s.settingsMessage = "Settings changed or repository unavailable; reopen the value and try again."
			e.editing = false
			return
		}
		if err := editor.SetSetting(ctx, e.repositoryID, row.Key, value); err != nil {
			s.settingsMessage = "Settings update failed: " + err.Error()
			return
		}
		s.settingsSnapshot = a.collectSettings(ctx)
		s.restoreSettingsSelection(e.repositoryID)
		s.settingsMessage = "Updated " + row.Key + "; refreshed saved and effective values."
		e.editing = false
		e.message = ""
	})
}

func renderSettingsEditor(model Model) ([]string, int, tableViewportMetadata) {
	e := model.settingsEditor
	row, ok := e.row(model.SettingsSnapshot)
	lines := []string{}
	add := func(text string) {
		lines = append(lines, wrapDetailWords(singleLineDetail(text), max(1, model.Width-4))...)
	}
	add("REPOSITORY SETTINGS · " + e.repositoryID)
	add("↑/↓ select · Enter edit · u inherit · d disable STOP · Esc back")
	if !ok {
		add("Repository unavailable; escape to return and refresh.")
		return lines, 0, tableViewportMetadata{}
	}
	selected := len(lines)
	add(fmt.Sprintf("%d: %s (%s)", e.index+1, row.Key, row.Kind))
	add("Effective: " + row.Value + " [" + row.Source + "]")
	stored := row.Stored
	if stored == "" {
		stored = "inherit"
	}
	add("Stored: " + stored)
	if !row.Editable {
		add("Read-only")
	}
	if row.Warning != "" {
		add("Warning: " + row.Warning)
	}
	if e.editing {
		add("Proposed stored input: " + fmt.Sprintf("%q", e.input))
		selected = len(lines) - 1
		if choices := settingsChoices(row); len(choices) > 0 {
			add("←/→ choices: " + strings.Join(choices, " | "))
		}
		add("Type / Backspace · Enter validate and confirm · Esc cancel")
	}
	if e.message != "" {
		add(e.message)
		selected = len(lines) - 1
	}
	return lines, selected, tableViewportMetadata{}
}
