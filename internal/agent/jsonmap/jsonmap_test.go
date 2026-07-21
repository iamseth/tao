package jsonmap

import (
	"encoding/json"
	"testing"
)

func TestInt64Coercions(t *testing.T) {
	values := map[string]any{
		"float":  float64(3),
		"int64":  int64(4),
		"int":    5,
		"number": json.Number("6"),
		"string": "7",
		"nil":    nil,
	}
	cases := map[string]int64{
		"float": 3, "int64": 4, "int": 5, "number": 6,
		"string": 0, "nil": 0, "missing": 0,
	}
	for key, want := range cases {
		if got := Int64(values, key); got != want {
			t.Errorf("Int64(%q) = %d, want %d", key, got, want)
		}
	}
}

func TestFirstInt64SkipsZeroAndMissing(t *testing.T) {
	values := map[string]any{"camel": float64(0), "snake": float64(9)}
	if got := FirstInt64(values, "missing", "camel", "snake"); got != 9 {
		t.Errorf("FirstInt64 = %d, want 9", got)
	}
	if got := FirstInt64(values, "missing"); got != 0 {
		t.Errorf("FirstInt64 missing = %d, want 0", got)
	}
}

func TestStringAndFirstString(t *testing.T) {
	values := map[string]any{"text": "ok", "num": 1, "empty": "", "nil": nil}
	if got := String(values, "text"); got != "ok" {
		t.Errorf("String(text) = %q, want ok", got)
	}
	for _, key := range []string{"num", "empty", "nil", "missing"} {
		if got := String(values, key); got != "" {
			t.Errorf("String(%q) = %q, want empty", key, got)
		}
	}
	if got := FirstString(values, "empty", "missing", "text"); got != "ok" {
		t.Errorf("FirstString = %q, want ok", got)
	}
	if got := FirstString(values, "empty", "missing"); got != "" {
		t.Errorf("FirstString empty = %q, want empty", got)
	}
}

func TestStringify(t *testing.T) {
	if got := Stringify(nil); got != "" {
		t.Errorf("Stringify(nil) = %q, want empty", got)
	}
	if got := Stringify("raw"); got != "raw" {
		t.Errorf("Stringify(string) = %q, want raw", got)
	}
	if got := Stringify(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Errorf("Stringify(map) = %q, want {\"a\":1}", got)
	}
}
