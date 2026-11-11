// Package gateenv validates bounded, child-only gate environment pins.
package gateenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

func validate(key, value string) error {
	switch key {
	case "GOTOOLCHAIN", "GOFLAGS", "CI":
	default:
		return fmt.Errorf("unsupported gate environment key %q", key)
	}
	if len(value) > 4096 || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid gate environment value for %s", key)
	}
	return nil
}

// Parse accepts exactly one bounded JSON object with unique allowlisted string keys.
func Parse(data []byte) (map[string]string, error) {
	if len(data) > 16384 {
		return nil, fmt.Errorf("gate environment input exceeds 16384 bytes")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("gate environment must be a JSON object")
	}
	pins := make(map[string]string)
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected environment key")
		}
		if _, exists := pins[key]; exists {
			return nil, fmt.Errorf("duplicate gate environment key %q", key)
		}
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		value, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("gate environment value must be a string")
		}
		if err := validate(key, value); err != nil {
			return nil, err
		}
		pins[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing gate environment JSON")
	}
	return pins, nil
}

// Apply copies the inherited environment and replaces every pinned occurrence.
// Sorted pin entries are literal data, never shell expressions.
func Apply(environ []string, pins map[string]string) ([]string, error) {
	keys := make([]string, 0, len(pins))
	for key, value := range pins {
		if err := validate(key, value); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(environ)+len(keys))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := pins[key]; !ok {
			result = append(result, entry)
		}
	}
	for _, key := range keys {
		result = append(result, key+"="+pins[key])
	}
	return result, nil
}
