package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

const globalSchema = "tao.config.v1"

type document struct {
	root   map[string]json.RawMessage
	values configtypes.SettingsValues
	field  string
}

// object rejects duplicate keys, including inside unknown metadata, so a write
// cannot silently pick one of two conflicting representations.
func object(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	raw, err := objectDecoder(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	return raw, nil
}

func objectDecoder(dec *json.Decoder) (map[string]json.RawMessage, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	raw := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected field name")
		}
		if _, exists := raw[key]; exists {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if err := checkObjects(value); err != nil {
			return nil, err
		}
		raw[key] = value
	}
	_, err = dec.Token()
	return raw, err
}

func checkObjects(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		_, err := object(raw)
		return err
	}
	if len(raw) > 0 && raw[0] == '[' {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, v := range values {
			if err := checkObjects(v); err != nil {
				return err
			}
		}
	}
	return nil
}

func safePath(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || (current == path && !info.Mode().IsRegular())) {
			return fmt.Errorf("unsafe non-regular or symlink path %s", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func loadDocument(path, scope, id string) (document, error) {
	field, schema := "settings", globalSchema
	if scope == "repo" {
		field, schema = "run_defaults", taodata.RepoSchema
	}
	d := document{root: map[string]json.RawMessage{}, values: configtypes.SettingsValues{}, field: field}
	if err := safePath(path); err != nil {
		return d, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is confined to the constructed data-home target
	if os.IsNotExist(err) && scope == "global" {
		d.root["schema"], _ = json.Marshal(schema)
		return d, nil
	}
	if err != nil {
		return d, err
	}
	d.root, err = object(data)
	if err != nil {
		return d, err
	}
	var version string
	if json.Unmarshal(d.root["schema"], &version) != nil || version != schema {
		return d, fmt.Errorf("unsupported schema %s", d.root["schema"])
	}
	if scope == "repo" {
		// Inspect identity independently of defaults, which may need field repair.
		var identity struct{ ID, Name, Root string }
		if err := json.Unmarshal(data, &identity); err != nil {
			return d, err
		}
		if identity.ID != id || strings.TrimSpace(identity.Name) == "" || !filepath.IsAbs(identity.Root) {
			return d, fmt.Errorf("invalid registered repository identity %q", id)
		}
	}
	raw, exists := d.root[field]
	if !exists {
		if scope == "global" {
			return d, fmt.Errorf("missing settings object")
		}
		return d, nil
	}
	if err := flatten(raw, "", d.values); err != nil {
		return d, err
	}
	// The strict shared decoder is the fast path; salvage retains invalid and
	// unknown fields for diagnostics/repair without ever treating them as valid.
	if values, err := runtimeconfig.DecodeSettings(raw, scope); err == nil {
		d.values = values
	}
	return d, nil
}

func flatten(data []byte, prefix string, values configtypes.SettingsValues) error {
	raw, err := object(data)
	if err != nil {
		return err
	}
	for field, value := range raw {
		if strings.Contains(field, ".") || field == "" {
			return fmt.Errorf("ambiguous settings field %q", field)
		}
		key := prefix + field
		container := false
		for _, d := range runtimeconfig.SettingDefinitions() {
			if len(d.Scopes) > 0 && strings.HasPrefix(d.Key, key+".") {
				container = true
				break
			}
		}
		if container {
			if err := flatten(value, key+".", values); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		} else {
			values[key] = value
		}
	}
	return nil
}

func (d document) encode(scope string) ([]byte, error) {
	// Use the shared codec for valid values. Invalid unrelated values and future
	// fields are retained as raw JSON during a partial repair.
	encoded, err := runtimeconfig.EncodeSettings(d.values, scope)
	if err != nil {
		root := map[string]any{}
		for key, value := range d.values {
			parts := strings.Split(key, ".")
			obj := root
			for _, part := range parts[:len(parts)-1] {
				if child, exists := obj[part]; exists {
					next, ok := child.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("conflicting setting %q", key)
					}
					obj = next
				} else {
					next := map[string]any{}
					obj[part] = next
					obj = next
				}
			}
			last := parts[len(parts)-1]
			if _, exists := obj[last]; exists {
				return nil, fmt.Errorf("conflicting setting %q", key)
			}
			obj[last] = value
		}
		encoded, err = json.Marshal(root)
		if err != nil {
			return nil, err
		}
	}
	d.root[d.field] = encoded
	return json.MarshalIndent(d.root, "", "  ")
}
