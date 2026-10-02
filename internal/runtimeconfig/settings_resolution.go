package runtimeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/configtypes"
)

// SettingStatus retains saved values even when a higher layer masks them.
// Source describes the effective value; Warning retains rejected layer diagnostics.
type SettingStatus struct {
	Key             string
	Value           string
	Source          string
	GlobalValue     string
	RepositoryValue string
	Warning         string
}

// ResolveSettings composes built-ins, sparse saved layers, and only the original
// captured environment. Passing a composed snapshot never promotes saved values
// into environment overrides. Admission remains operation-scoped through Require.
func ResolveSettings(global, repository configtypes.SettingsValues, environment EnvSnapshot) EnvSnapshot {
	s := LoadEnv(nil)
	statuses := make(map[string]SettingStatus)
	indices := make(map[string]int)
	for i, row := range s.rows {
		indices[row.Name] = i
	}
	definitions := SettingDefinitions()
	for _, d := range definitions {
		status := SettingStatus{Key: d.Key, Source: "default"}
		if d.EnvKey != "" {
			status.Value = s.rows[indices[d.EnvKey]].Value
		} else {
			status.Value = "0"
		}
		statuses[d.Key] = status
	}
	apply := func(values configtypes.SettingsValues, scope, source string) {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			raw := values[key]
			status, exists := statuses[key]
			if !exists {
				status = SettingStatus{Key: key, Source: "invalid"}
			}
			text := settingText(raw)
			d, _ := settingDefinition(key)
			var row *runtimeEnvVar
			for i := range runtimeEnvVars {
				if d.EnvKey != "" && runtimeEnvVars[i].name == d.EnvKey {
					row = &runtimeEnvVars[i]
					break
				}
			}
			err := validateSetting(key, raw, scope)
			if err == nil {
				canonical, parseErr := ParseSetting(key, text)
				err = parseErr
				text = settingText(canonical)
			}
			if source == "global" {
				status.GlobalValue = text
			} else {
				status.RepositoryValue = text
			}
			if err != nil {
				warning := fmt.Sprintf("invalid %s value %q: %v; rejected", source, string(raw), err)
				status.Warning = joinSettingWarning(status.Warning, warning)
				if row == nil || !row.fallbackOnInvalid {
					status.Source = "invalid"
					admission := key
					if d.EnvKey != "" {
						admission = d.EnvKey
					}
					s.failures[admission] = errors.Join(s.failures[admission], errors.New(warning))
				}
			} else {
				if key == "max_slices" {
					n, _ := strconv.Atoi(text)
					s.defaults.MaxSlices = &n
				} else {
					input := text
					if text == "null" && strings.HasSuffix(key, ".stop") {
						input = ""
					}
					text, _ = row.apply(&s.defaults, input)
				}
				status.Value, status.Source = text, source
			}
			statuses[key] = status
			if d.EnvKey != "" {
				i := indices[d.EnvKey]
				s.rows[i].Value, s.rows[i].Source, s.rows[i].Warning = status.Value, status.Source, status.Warning
			}
		}
	}
	apply(global, "global", "global")
	apply(repository, "repo", "repository")
	s = loadEnvOnto(environment.captured, s)
	for _, d := range definitions {
		status := statuses[d.Key]
		if d.EnvKey != "" {
			for _, row := range s.rows {
				if row.Name == d.EnvKey {
					status.Value, status.Source, status.Warning = row.Value, row.Source, row.Warning
					break
				}
			}
			if err := s.failures[d.EnvKey]; err != nil {
				status.Source = "invalid"
			}
		} else if s.failures[d.Key] != nil {
			status.Source = "invalid"
		}
		s.settings = append(s.settings, status)
		delete(statuses, d.Key)
	}
	for _, status := range statuses {
		s.settings = append(s.settings, status)
	}
	slices.SortFunc(s.settings, func(a, b SettingStatus) int { return strings.Compare(a.Key, b.Key) })
	return s
}

func settingText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil && string(raw) != "null" {
		return text
	}
	return strings.TrimSpace(string(raw))
}

func joinSettingWarning(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// SettingsStatus returns a defensive canonical-key projection, including
// environment-only controls. Deprecated aliases remain available through Status.
func (s EnvSnapshot) SettingsStatus() []SettingStatus {
	if s.settings == nil {
		return ResolveSettings(nil, nil, s).SettingsStatus()
	}
	return slices.Clone(s.settings)
}
