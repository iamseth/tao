package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/configtypes"
)

// SettingDefinition describes a canonical setting. Empty Scopes denotes an
// environment-only control; deprecated aliases remain in the environment table,
// not in this persisted-key inventory. Kind describes the native JSON scalar.
type SettingDefinition struct {
	Key     string
	EnvKey  string
	Kind    string
	Scopes  []string
	Choices []string
}

func scopedSetting(key, kind string, choices ...string) SettingDefinition {
	return SettingDefinition{Key: key, Kind: kind, Scopes: []string{"global", "repo"}, Choices: choices}
}

func budgetSetting(name, kind string) SettingDefinition {
	parts := strings.Split(strings.ToLower(strings.TrimPrefix(name, "TAO_BUDGET_")), "_")
	key := "budget." + parts[0] + "." + strings.Join(parts[1:len(parts)-1], "_") + "." + parts[len(parts)-1]
	return scopedSetting(key, kind)
}

// SettingDefinitions returns sorted, defensive metadata copies from the runtime
// table, plus max_slices, which deliberately has no environment alias.
func SettingDefinitions() []SettingDefinition {
	defs := []SettingDefinition{scopedSetting("max_slices", "integer")}
	for _, row := range runtimeEnvVars {
		if row.aliasOf != "" {
			continue
		}
		d := row.setting
		if d.Key == "" {
			d = SettingDefinition{Key: strings.ToLower(strings.TrimPrefix(row.name, "TAO_")), Kind: "string"}
		}
		d.EnvKey = row.name
		d.Scopes = slices.Clone(d.Scopes)
		d.Choices = slices.Clone(d.Choices)
		defs = append(defs, d)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Key < defs[j].Key })
	return defs
}

func settingDefinition(key string) (SettingDefinition, error) {
	for _, d := range SettingDefinitions() {
		if d.Key == key && len(d.Scopes) > 0 {
			return d, nil
		}
	}
	return SettingDefinition{}, fmt.Errorf("unknown persistable setting %q", key)
}

// ParseSetting uses the environment table's parser without loading environment
// state or applying cross-layer budget constraints. Only STOP accepts null.
func ParseSetting(key, value string) (json.RawMessage, error) {
	d, err := settingDefinition(key)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(key, ".stop") && strings.TrimSpace(value) == "null" {
		return json.RawMessage("null"), nil
	}
	var canonical string
	if key == "max_slices" {
		n, e := strconv.Atoi(strings.TrimSpace(value))
		if e != nil || n < 0 {
			return nil, fmt.Errorf("%s: must be a non-negative integer", key)
		}
		canonical = strconv.Itoa(n)
	} else {
		for _, row := range runtimeEnvVars {
			if row.name == d.EnvKey {
				canonical, err = row.apply(&EnvDefaults{}, value)
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	if strings.HasSuffix(key, ".stop") && canonical == "disabled" {
		return json.RawMessage("null"), nil
	}
	if d.Kind == "string" {
		return json.Marshal(canonical)
	}
	return json.RawMessage(canonical), nil
}

func validateSetting(key string, raw json.RawMessage, scope string) error {
	d, err := settingDefinition(key)
	if err != nil {
		return err
	}
	if !slices.Contains(d.Scopes, scope) {
		return fmt.Errorf("%s: not allowed in %s scope", key, scope)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if err := requireJSONEnd(dec); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	var text string
	switch v := value.(type) {
	case nil:
		if !strings.HasSuffix(key, ".stop") {
			return fmt.Errorf("%s: null is only valid for STOP caps", key)
		}
		text = "null"
	case string:
		if d.Kind != "string" {
			return fmt.Errorf("%s: expected %s", key, d.Kind)
		}
		text = v
	case bool:
		if d.Kind != "boolean" {
			return fmt.Errorf("%s: expected %s", key, d.Kind)
		}
		text = strconv.FormatBool(v)
	case json.Number:
		if d.Kind != "integer" && d.Kind != "number" {
			return fmt.Errorf("%s: expected %s", key, d.Kind)
		}
		text = v.String()
	default:
		return fmt.Errorf("%s: expected a scalar", key)
	}
	_, err = ParseSetting(key, text)
	return err
}

// ValidateSettings checks a sparse layer, never STOP/WARN relationships that
// require fully resolved layers. It does not mutate or retain caller memory.
func ValidateSettings(values configtypes.SettingsValues, scope string) error {
	if scope != "global" && scope != "repo" {
		return fmt.Errorf("unknown settings scope %q", scope)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := validateSetting(key, values[key], scope); err != nil {
			return err
		}
	}
	return nil
}

func requireJSONEnd(dec *json.Decoder) error {
	_, err := dec.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unexpected trailing JSON: %w", err)
	}
	return fmt.Errorf("unexpected trailing JSON")
}

// DecodeSettings reads a settings object, retaining legacy nested models and
// pull_request encoding. Dotted object fields, duplicates, unknown containers,
// and trailing input are rejected rather than silently merged or discarded.
func DecodeSettings(data []byte, scope string) (configtypes.SettingsValues, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	values := make(configtypes.SettingsValues)
	if err := decodeSettingsObject(dec, "", values); err != nil {
		return nil, err
	}
	if err := requireJSONEnd(dec); err != nil {
		return nil, err
	}
	if err := ValidateSettings(values, scope); err != nil {
		return nil, err
	}
	return values, nil
}

func decodeSettingsObject(dec *json.Decoder, prefix string, values configtypes.SettingsValues) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("%s: expected settings object", prefix)
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		field, ok := token.(string)
		if !ok || strings.Contains(field, ".") || seen[field] {
			return fmt.Errorf("%s: duplicate or ambiguous field %q", prefix, field)
		}
		seen[field] = true
		key := prefix + field
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if _, err := settingDefinition(key); err == nil {
			values[key] = raw
			continue
		}
		container := false
		for _, d := range SettingDefinitions() {
			if len(d.Scopes) > 0 && strings.HasPrefix(d.Key, key+".") {
				container = true
				break
			}
		}
		if !container {
			return fmt.Errorf("unknown setting %q", key)
		}
		child := json.NewDecoder(bytes.NewReader(raw))
		if err := decodeSettingsObject(child, key+".", values); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// EncodeSettings produces the settings object only, not a storage envelope.
// It validates first and never retains or mutates caller-owned values.
func EncodeSettings(values configtypes.SettingsValues, scope string) ([]byte, error) {
	if err := ValidateSettings(values, scope); err != nil {
		return nil, err
	}
	root := map[string]any{}
	for key, value := range values {
		parts := strings.Split(key, ".")
		obj := root
		for _, part := range parts[:len(parts)-1] {
			child, ok := obj[part].(map[string]any)
			if !ok {
				child = map[string]any{}
				obj[part] = child
			}
			obj = child
		}
		obj[parts[len(parts)-1]] = value
	}
	return json.Marshal(root)
}
