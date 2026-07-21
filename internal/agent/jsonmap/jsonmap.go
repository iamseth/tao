// Package jsonmap provides defensive typed accessors for values decoded from
// JSON into map[string]any. Every agent runtime parses provider stream output
// into generic maps and needs the same coercion (float64/int64/json.Number for
// numbers, type-asserted strings, JSON fallback for stringification).
// Centralizing it here keeps the pi, claude, opencode, and codex runtimes from
// drifting. Coercion that is genuinely runtime-specific (e.g. Pi's two-map
// lookup, opencode's json.Number-aware numberValue) stays in its runtime.
package jsonmap

import "encoding/json"

// Int64 returns values[key] coerced to int64, or 0 when missing or non-numeric.
func Int64(values map[string]any, key string) int64 {
	switch value := values[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

// FirstInt64 returns the first non-zero Int64 among keys, or 0 when none match.
// It lets callers tolerate snake_case/camelCase key variants.
func FirstInt64(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value := Int64(values, key); value != 0 {
			return value
		}
	}
	return 0
}

// String returns values[key] when it is a string, or "" otherwise.
func String(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// FirstString returns the first non-empty String among keys, or "" when none.
func FirstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := String(values, key); text != "" {
			return text
		}
	}
	return ""
}

// Stringify renders value as a string: strings pass through, nil becomes "",
// and anything else is JSON-encoded ("" if encoding fails).
func Stringify(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(data)
	}
}
